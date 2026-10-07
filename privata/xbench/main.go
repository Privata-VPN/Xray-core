// Command xbench measures Xray transport throughput, connection churn and
// latency on a Privata router. "serve" is the far-end target (data source,
// sink and echo); "load" drives traffic either through in-process Xray client
// instances (-xray, no SOCKS hop) or through SOCKS5 inbounds (-socks) and
// reports rates together with busy CPU time of named CPU groups.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
)

const (
	modeDownload = 'D'
	modeUpload   = 'U'
	modePing     = 'P'
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "load":
		load(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: xbench serve|load [flags]")
	os.Exit(2)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", ":9000", "listen address")
	tlsListen := fs.String("tls-listen", "", "optional HTTPS listener, the REALITY target")
	cert := fs.String("cert", "", "certificate for -tls-listen")
	key := fs.String("key", "", "key for -tls-listen")
	_ = fs.Parse(args)
	if *tlsListen != "" {
		go func() {
			fatal(http.ListenAndServeTLS(*tlsListen, *cert, *key, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("ok\n"))
			})))
		}()
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fatal(err)
	}
	zero := make([]byte, 1<<20)
	for {
		c, err := ln.Accept()
		if err != nil {
			fatal(err)
		}
		go handle(c.(*net.TCPConn), zero)
	}
}

func handle(c *net.TCPConn, zero []byte) {
	defer c.Close()
	var hdr [9]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	size := binary.BigEndian.Uint64(hdr[1:])
	switch hdr[0] {
	case modeDownload:
		for left := size; size == 0 || left > 0; {
			n := uint64(len(zero))
			if size != 0 && left < n {
				n = left
			}
			if _, err := c.Write(zero[:n]); err != nil {
				return
			}
			left -= n
		}
		_ = c.CloseWrite()
		drain(c)
	case modeUpload:
		n := drain(c)
		binary.BigEndian.PutUint64(hdr[1:], uint64(n))
		_, _ = c.Write(hdr[1:])
	case modePing:
		b := make([]byte, 64)
		for {
			n, err := c.Read(b)
			if err != nil {
				return
			}
			if _, err := c.Write(b[:n]); err != nil {
				return
			}
		}
	}
}

func drain(r io.Reader) int64 {
	b := make([]byte, 256<<10)
	var total int64
	for {
		n, err := r.Read(b)
		total += int64(n)
		if err != nil {
			return total
		}
	}
}

type loadConfig struct {
	dial     func(i int) (net.Conn, error)
	mode     string
	conns    int
	size     uint64
	warmup   time.Duration
	duration time.Duration
	groups   []cpuGroup
}

func load(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	socks := fs.String("socks", "127.0.0.1:1080", "comma-separated SOCKS5 endpoints, used round-robin")
	xrayConfigs := fs.String("xray", "", "comma-separated Xray client configs run in-process, used round-robin instead of -socks")
	target := fs.String("target", "127.0.0.1:9000", "xbench serve address as seen by the proxy")
	mode := fs.String("mode", "down", "down | up | churn | ping")
	conns := fs.Int("conns", 1, "concurrent connections (bulk/ping) or workers (churn)")
	size := fs.String("size", "64k", "bytes downloaded per connection in churn mode")
	warmup := fs.Duration("warmup", 3*time.Second, "warmup before measuring")
	duration := fs.Duration("duration", 15*time.Second, "measurement window")
	cpus := fs.String("cpus", "", "CPU groups to account, e.g. server=0,4;client=2,3,6,7")
	_ = fs.Parse(args)

	addr, err := net.ResolveTCPAddr("tcp4", *target)
	if err != nil {
		fatal(err)
	}
	groups, err := parseGroups(*cpus)
	if err != nil {
		fatal(err)
	}
	cfg := loadConfig{
		dial:     socksDialer(strings.Split(*socks, ","), addr),
		mode:     *mode,
		conns:    *conns,
		size:     parseSize(*size),
		warmup:   *warmup,
		duration: *duration,
		groups:   groups,
	}
	if *xrayConfigs != "" {
		cfg.dial = xrayDialer(strings.Split(*xrayConfigs, ","), addr)
	}
	switch cfg.mode {
	case "down", "up":
		runBulk(cfg)
	case "churn":
		runChurn(cfg)
	case "ping":
		runPing(cfg)
	default:
		fatal(fmt.Errorf("unknown mode %q", cfg.mode))
	}
}

// window counts bytes only inside the measurement window that follows the
// warmup, so connection ramp-up and teardown don't skew rates.
type window struct {
	bytes    atomic.Int64
	measured atomic.Bool
	stop     atomic.Bool
}

func (w *window) add(n int) {
	if w.measured.Load() {
		w.bytes.Add(int64(n))
	}
}

func (w *window) run(cfg loadConfig) (cpuSample, cpuSample, time.Duration) {
	time.Sleep(cfg.warmup)
	before := readCPU()
	start := time.Now()
	w.measured.Store(true)
	time.Sleep(cfg.duration)
	w.measured.Store(false)
	elapsed := time.Since(start)
	after := readCPU()
	w.stop.Store(true)
	return before, after, elapsed
}

func runBulk(cfg loadConfig) {
	var w window
	var failures atomic.Int64
	var wg sync.WaitGroup
	conns := make([]net.Conn, cfg.conns)
	for i := range conns {
		c, err := cfg.dial(i)
		if err != nil {
			fatal(fmt.Errorf("conn %d: %w", i, err))
		}
		conns[i] = c
		mode := byte(modeDownload)
		if cfg.mode == "up" {
			mode = modeUpload
		}
		if err := writeHeader(c, mode, 0); err != nil {
			fatal(err)
		}
	}
	for _, c := range conns {
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			b := make([]byte, 256<<10)
			mbr, _ := c.(buf.Reader)
			mbw, _ := c.(buf.Writer)
			for !w.stop.Load() {
				var n int
				var err error
				switch {
				case cfg.mode == "up" && mbw != nil:
					n, err = writeZeroBuffers(mbw, 32)
				case cfg.mode == "up":
					n, err = c.Write(b)
				case mbr != nil:
					var mb buf.MultiBuffer
					mb, err = mbr.ReadMultiBuffer()
					n = int(mb.Len())
					buf.ReleaseMulti(mb)
				default:
					n, err = c.Read(b)
				}
				w.add(n)
				if err != nil {
					if !w.stop.Load() {
						failures.Add(1)
					}
					return
				}
			}
		}(c)
	}
	before, after, elapsed := w.run(cfg)
	for _, c := range conns {
		_ = c.Close()
	}
	wg.Wait()
	bytes := w.bytes.Load()
	gbps := float64(bytes) * 8 / elapsed.Seconds() / 1e9
	fmt.Printf("mode=%s conns=%d gbps=%.3f bytes=%d failures=%d\n", cfg.mode, cfg.conns, gbps, bytes, failures.Load())
	reportCPU(cfg.groups, before, after, elapsed, float64(bytes))
}

func runChurn(cfg loadConfig) {
	var w window
	var done, failures atomic.Int64
	var mu sync.Mutex
	var lat []time.Duration
	var wg sync.WaitGroup
	for i := 0; i < cfg.conns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := make([]byte, 64<<10)
			local := make([]time.Duration, 0, 4096)
			for k := 0; !w.stop.Load(); k++ {
				t0 := time.Now()
				n, err := fetch(cfg.dial, i+k, cfg.size, b)
				w.add(int(n))
				if err != nil {
					if !w.stop.Load() {
						failures.Add(1)
						time.Sleep(10 * time.Millisecond)
					}
					continue
				}
				if w.measured.Load() {
					done.Add(1)
					local = append(local, time.Since(t0))
				}
			}
			mu.Lock()
			lat = append(lat, local...)
			mu.Unlock()
		}(i)
	}
	before, after, elapsed := w.run(cfg)
	wg.Wait()
	n := done.Load()
	fmt.Printf("mode=churn workers=%d size=%d conns_per_s=%.0f gbps=%.3f failures=%d %s\n",
		cfg.conns, cfg.size, float64(n)/elapsed.Seconds(), float64(w.bytes.Load())*8/elapsed.Seconds()/1e9,
		failures.Load(), percentiles(lat))
	reportCPU(cfg.groups, before, after, elapsed, float64(n))
}

func fetch(dial func(int) (net.Conn, error), i int, size uint64, b []byte) (int64, error) {
	c, err := dial(i)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if err := writeHeader(c, modeDownload, size); err != nil {
		return 0, err
	}
	var total int64
	for uint64(total) < size {
		n, err := c.Read(b)
		total += int64(n)
		if err != nil {
			if errors.Is(err, io.EOF) && uint64(total) == size {
				break
			}
			return total, err
		}
	}
	return total, nil
}

func runPing(cfg loadConfig) {
	var w window
	var mu sync.Mutex
	var lat []time.Duration
	var wg sync.WaitGroup
	for i := 0; i < cfg.conns; i++ {
		c, err := cfg.dial(i)
		if err != nil {
			fatal(err)
		}
		if err := writeHeader(c, modePing, 0); err != nil {
			fatal(err)
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer c.Close()
			b := make([]byte, 1)
			local := make([]time.Duration, 0, 1<<16)
			for !w.stop.Load() {
				t0 := time.Now()
				if _, err := c.Write(b); err != nil {
					return
				}
				if _, err := io.ReadFull(c, b); err != nil {
					return
				}
				if w.measured.Load() {
					local = append(local, time.Since(t0))
				}
			}
			mu.Lock()
			lat = append(lat, local...)
			mu.Unlock()
		}(c)
	}
	before, after, elapsed := w.run(cfg)
	wg.Wait()
	fmt.Printf("mode=ping conns=%d rtt_per_s=%.0f %s\n", cfg.conns, float64(len(lat))/elapsed.Seconds(), percentiles(lat))
	reportCPU(cfg.groups, before, after, elapsed, float64(len(lat)))
}

func socksDialer(proxies []string, target *net.TCPAddr) func(int) (net.Conn, error) {
	return func(i int) (net.Conn, error) { return dialVia(proxies[i%len(proxies)], target) }
}

// xrayDialer starts one Xray client instance per config (one user each) and
// dials through its default outbound, skipping the SOCKS hop and its copies.
func xrayDialer(configs []string, target *net.TCPAddr) func(int) (net.Conn, error) {
	instances := make([]*core.Instance, len(configs))
	for i, path := range configs {
		f, err := os.Open(path)
		if err != nil {
			fatal(err)
		}
		config, err := serial.LoadJSONConfig(f)
		f.Close()
		if err != nil {
			fatal(fmt.Errorf("%s: %w", path, err))
		}
		inst, err := core.New(config)
		if err != nil {
			fatal(err)
		}
		if err := inst.Start(); err != nil {
			fatal(err)
		}
		instances[i] = inst
	}
	dest := xnet.TCPDestination(xnet.IPAddress(target.IP.To4()), xnet.Port(target.Port))
	return func(i int) (net.Conn, error) {
		return core.Dial(context.Background(), instances[i%len(instances)], dest)
	}
}

func writeZeroBuffers(w buf.Writer, count int) (int, error) {
	mb := make(buf.MultiBuffer, 0, count)
	for range count {
		b := buf.New()
		b.Extend(buf.Size)
		mb = append(mb, b)
	}
	n := int(mb.Len())
	return n, w.WriteMultiBuffer(mb)
}

func dialVia(proxy string, target *net.TCPAddr) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", proxy, 5*time.Second)
	if err != nil {
		return nil, err
	}
	ip := target.IP.To4()
	req := []byte{5, 1, 0, 5, 1, 0, 1, ip[0], ip[1], ip[2], ip[3], byte(target.Port >> 8), byte(target.Port)}
	if _, err := c.Write(req); err != nil {
		c.Close()
		return nil, err
	}
	var resp [12]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		c.Close()
		return nil, err
	}
	if resp[0] != 5 || resp[1] != 0 || resp[3] != 0 {
		c.Close()
		return nil, fmt.Errorf("socks: rejected %v", resp[:4])
	}
	return c, nil
}

func writeHeader(c net.Conn, mode byte, size uint64) error {
	var hdr [9]byte
	hdr[0] = mode
	binary.BigEndian.PutUint64(hdr[1:], size)
	_, err := c.Write(hdr[:])
	return err
}

func percentiles(d []time.Duration) string {
	if len(d) == 0 {
		return "p50=- p90=- p99=-"
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	at := func(q float64) time.Duration { return d[int(q*float64(len(d)-1))] }
	return fmt.Sprintf("p50=%s p90=%s p99=%s max=%s", at(0.5), at(0.9), at(0.99), d[len(d)-1])
}

type cpuGroup struct {
	name string
	cpus []int
}

// cpuSample holds busy and total jiffies per CPU from /proc/stat.
type cpuSample map[int][2]uint64

func readCPU() cpuSample {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil
	}
	s := cpuSample{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 9 || !strings.HasPrefix(f[0], "cpu") || f[0] == "cpu" {
			continue
		}
		id, err := strconv.Atoi(f[0][3:])
		if err != nil {
			continue
		}
		var total, idle uint64
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i < 8 {
				total += n
			}
			if i == 3 || i == 4 {
				idle += n
			}
		}
		s[id] = [2]uint64{total - idle, total}
	}
	return s
}

// reportCPU prints busy cores and CPU-seconds per unit (GB for bulk modes,
// 1k connections for churn, 1k round trips for ping) for each group.
func reportCPU(groups []cpuGroup, before, after cpuSample, elapsed time.Duration, units float64) {
	const hz = 100.0
	for _, g := range groups {
		var busy uint64
		for _, c := range g.cpus {
			busy += after[c][0] - before[c][0]
		}
		sec := float64(busy) / hz
		fmt.Printf("  cpu %-8s busy_cores=%.2f/%d", g.name, sec/elapsed.Seconds(), len(g.cpus))
		if units > 0 {
			if units > 1e6 {
				fmt.Printf(" cpu_s_per_GB=%.3f", sec/(units/1e9))
			} else {
				fmt.Printf(" cpu_ms_per_unit=%.3f", sec*1000/units)
			}
		}
		fmt.Println()
	}
}

func parseGroups(s string) ([]cpuGroup, error) {
	var out []cpuGroup
	if s == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ";") {
		name, list, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("bad cpu group %q", part)
		}
		g := cpuGroup{name: name}
		for _, c := range strings.Split(list, ",") {
			n, err := strconv.Atoi(c)
			if err != nil {
				return nil, err
			}
			g.cpus = append(g.cpus, n)
		}
		out = append(out, g)
	}
	return out, nil
}

func parseSize(s string) uint64 {
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		fatal(err)
	}
	return n * mult
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "xbench:", err)
	os.Exit(1)
}
