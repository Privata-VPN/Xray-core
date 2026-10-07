package splithttp

import (
	"bytes"
	"context"
	gotls "crypto/tls"
	goerrors "errors"
	gonet "net"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/task"
	"golang.org/x/net/http2"
)

// h2WriteBufferSize is the per-connection frame buffer of the HTTP/2 server:
// eight full 16 KiB DATA frames, so the serve goroutine encodes a whole
// downlink write without starting a writer goroutine per frame.
const h2WriteBufferSize = 8 * (16384 + 9)

// handshakeTimeout bounds the TLS handshake and the wait for the HTTP/2
// client preface, as http.Server's ReadHeaderTimeout did.
const handshakeTimeout = 4 * time.Second

var clientPreface = []byte(http2.ClientPreface)

func newH2Server() *http2.Server {
	return &http2.Server{
		WriteBufferSize:   h2WriteBufferSize,
		LazyWindowUpdates: true,
		BatchFlushes:      true,
		StartHandler:      task.Go,
		NewWriteScheduler: http2.NewReadyRoundRobinWriteScheduler,
	}
}

// acceptLoop serves HTTP/2 connections, h2 by TLS ALPN or prior knowledge
// as over REALITY, with the x/net server and queues the rest for net/http.
func (l *Listener) acceptLoop(ctx context.Context) {
	var delay time.Duration
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if ne, ok := err.(gonet.Error); ok && ne.Temporary() { //nolint:staticcheck // same retry as http.Server.Serve
				delay = min(max(2*delay, 5*time.Millisecond), time.Second)
				time.Sleep(delay)
				continue
			}
			l.h1conns.Close()
			if !goerrors.Is(err, gonet.ErrClosed) {
				errors.LogErrorInner(ctx, err, "failed to serve HTTP for XHTTP")
			}
			return
		}
		delay = 0
		go l.serveConn(conn)
	}
}

func (l *Listener) serveConn(conn gonet.Conn) {
	if tc, ok := conn.(*gotls.Conn); ok {
		ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
		err := tc.HandshakeContext(ctx)
		cancel()
		if err != nil {
			conn.Close()
			return
		}
		if tc.ConnectionState().NegotiatedProtocol == http2.NextProtoTLS {
			l.serveH2(conn, false)
		} else {
			l.h1conns.push(conn)
		}
		return
	}

	var prefix [len(http2.ClientPreface)]byte
	n := 0
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	for n < len(prefix) {
		m, err := conn.Read(prefix[n:])
		n += m
		if !bytes.HasPrefix(clientPreface, prefix[:n]) {
			break
		}
		if err != nil {
			conn.Close()
			return
		}
	}
	conn.SetReadDeadline(time.Time{})
	if n == len(prefix) && bytes.Equal(prefix[:], clientPreface) {
		l.serveH2(conn, true)
		return
	}
	l.h1conns.push(&prefixConn{Conn: conn, prefix: bytes.Clone(prefix[:n])})
}

func (l *Listener) serveH2(conn gonet.Conn, sawPreface bool) {
	l.h2server.ServeConn(conn, &http2.ServeConnOpts{
		BaseConfig:       &l.server,
		Handler:          l.server.Handler,
		SawClientPreface: sawPreface,
	})
}

// prefixConn replays bytes read while sniffing for the HTTP/2 preface.
type prefixConn struct {
	gonet.Conn
	prefix []byte
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if len(c.prefix) == 0 {
		return c.Conn.Read(b)
	}
	n := copy(b, c.prefix)
	c.prefix = c.prefix[n:]
	return n, nil
}

// connQueue is the net.Listener the HTTP/1.1 server accepts from.
type connQueue struct {
	addr  gonet.Addr
	conns chan gonet.Conn
	done  chan struct{}
	once  sync.Once
}

func newConnQueue(addr gonet.Addr) *connQueue {
	return &connQueue{addr: addr, conns: make(chan gonet.Conn), done: make(chan struct{})}
}

func (q *connQueue) push(c gonet.Conn) {
	select {
	case q.conns <- c:
	case <-q.done:
		c.Close()
	}
}

func (q *connQueue) Accept() (gonet.Conn, error) {
	select {
	case c := <-q.conns:
		return c, nil
	case <-q.done:
		return nil, gonet.ErrClosed
	}
}

func (q *connQueue) Close() error {
	q.once.Do(func() { close(q.done) })
	return nil
}

func (q *connQueue) Addr() gonet.Addr { return q.addr }
