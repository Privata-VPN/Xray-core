package internet_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/xtls/xray-core/common/net"
	. "github.com/xtls/xray-core/transport/internet"
	"golang.org/x/net/dns/dnsmessage"
)

// serveDNS answers every A question with 127.0.0.1 and reports the source
// address of each query.
func serveDNS(t *testing.T, conn net.PacketConn, from chan<- net.Addr) {
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
				Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}},
			}}
		}
		packed, err := reply.Pack()
		if err != nil {
			t.Error(err)
			continue
		}
		conn.WriteTo(packed, addr)
		from <- addr
	}
}

func TestSourceDNS(t *testing.T) {
	dnsConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.LocalHostIP.IP()})
	if err != nil {
		t.Fatal(err)
	}
	defer dnsConn.Close()
	queries := make(chan net.Addr, 16)
	go serveDNS(t, dnsConn, queries)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	defer func(dns string) { SourceDNS = dns }(SourceDNS)
	SourceDNS = dnsConn.LocalAddr().String()

	// Only Linux can send from all of 127.0.0.0/8 without configuring it.
	src := net.LocalHostIP
	if runtime.GOOS == "linux" {
		src = net.IPAddress([]byte{127, 0, 0, 2})
	}
	port := net.Port(listener.Addr().(*net.TCPAddr).Port)
	for _, dest := range []net.Destination{
		net.TCPDestination(net.DomainAddress("tcp.source-dns.test"), port),
		net.UDPDestination(net.DomainAddress("udp.source-dns.test"), port),
	} {
		conn, err := (&DefaultSystemDialer{}).Dial(context.Background(), src, dest, nil)
		if err != nil {
			t.Fatalf("dial %v: %v", dest, err)
		}
		conn.Close()

		select {
		case addr := <-queries:
			if ip := addr.(*net.UDPAddr).IP; !ip.Equal(src.IP()) {
				t.Errorf("query for %v from %v, want %v", dest, ip, src)
			}
		default:
			t.Errorf("%v was not resolved through SourceDNS", dest)
		}
		for len(queries) > 0 {
			<-queries
		}
	}
}
