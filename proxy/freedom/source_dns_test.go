package freedom

import (
	"context"
	goerrors "errors"
	"os"
	"runtime"
	"testing"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"golang.org/x/net/dns/dnsmessage"
)

// refusingDialer records what it is asked to dial and refuses it the way a
// router's abuse guard does.
type refusingDialer struct {
	gateway net.Address
	dialed  []net.Destination
}

func (d *refusingDialer) Dial(_ context.Context, dest net.Destination) (stat.Connection, error) {
	d.dialed = append(d.dialed, dest)
	return nil, os.ErrPermission
}

func (d *refusingDialer) DestIpAddress() net.IP { return nil }

func (d *refusingDialer) SetOutboundGateway(_ context.Context, ob *session.Outbound) {
	ob.Gateway = d.gateway
}

// serveDNS answers every A question with answer and reports the source
// address of each query.
func serveDNS(t *testing.T, conn net.PacketConn, answer [4]byte, from chan<- net.Addr) {
	buf := make([]byte, 512)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var query dnsmessage.Message
		if err := query.Unpack(buf[:n]); err != nil || len(query.Questions) != 1 {
			t.Errorf("bad query: %v", err)
			continue
		}
		reply := dnsmessage.Message{
			Header:    dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true},
			Questions: query.Questions,
		}
		if q := query.Questions[0]; q.Type == dnsmessage.TypeA {
			reply.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class, TTL: 60},
				Body:   &dnsmessage.AResource{A: answer},
			}}
		}
		packed, err := reply.Pack()
		if err != nil {
			t.Error(err)
			continue
		}
		from <- addr
		conn.WriteTo(packed, addr)
	}
}

func TestFinalRulesResolveThroughSourceDNS(t *testing.T) {
	dnsConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.LocalHostIP.IP()})
	if err != nil {
		t.Fatal(err)
	}
	defer dnsConn.Close()
	queries := make(chan net.Addr, 16)
	answer := [4]byte{203, 0, 113, 7}
	go serveDNS(t, dnsConn, answer, queries)

	defer func(dns string) { internet.SourceDNS = dns }(internet.SourceDNS)
	internet.SourceDNS = dnsConn.LocalAddr().String()

	// Only Linux can send from all of 127.0.0.0/8 without configuring it.
	src := net.LocalHostIP
	if runtime.GOOS == "linux" {
		src = net.IPAddress([]byte{127, 0, 0, 2})
	}
	h := &Handler{
		config:     &Config{},
		finalRules: []*FinalRule{{action: RuleAction_Allow, network: allNetworks}},
	}
	dialer := &refusingDialer{gateway: src}
	target := net.TCPDestination(net.DomainAddress("freedom.source-dns.test"), 443)
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: target}})

	err = h.Process(ctx, &transport.Link{}, dialer)
	if !goerrors.Is(err, os.ErrPermission) {
		t.Fatalf("Process() = %v, want a permission error", err)
	}

	want := net.TCPDestination(net.IPAddress(answer[:]), 443)
	if len(dialer.dialed) != 1 || dialer.dialed[0] != want {
		t.Errorf("dialed %v, want once %v", dialer.dialed, want)
	}
	if n := len(queries); n != 1 {
		t.Fatalf("SourceDNS got %d queries, want 1", n)
	}
	if ip := (<-queries).(*net.UDPAddr).IP; !ip.Equal(src.IP()) {
		t.Errorf("query from %v, want %v", ip, src)
	}
}
