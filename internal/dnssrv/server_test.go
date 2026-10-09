package dnssrv

import (
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/rules"
)

func upstream(t *testing.T, truncated, fail bool) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		pc.Close()
		t.Fatal(err)
	}
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		if fail {
			m.Rcode = dns.RcodeServerFailure
		} else if truncated && w.RemoteAddr().Network() == "udp" {
			m.Truncated = true
		} else if r.Question[0].Qtype == dns.TypeAAAA {
			m.Answer = []dns.RR{&dns.AAAA{Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET}, AAAA: net.ParseIP("2001:db8::1")}}
		} else {
			m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("203.0.113.10")}}
		}
		w.WriteMsg(m)
	})
	ready := make(chan struct{}, 2)
	u := &dns.Server{PacketConn: pc, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	c := &dns.Server{Listener: ln, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	go u.ActivateAndServe()
	go c.ActivateAndServe()
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("upstream did not start")
		}
	}
	t.Cleanup(func() { u.Shutdown(); c.Shutdown() })
	return pc.LocalAddr().String()
}

func testServer(t *testing.T, c *config.Config, store *ipstore.Store, ready ...func() bool) (*Server, string) {
	t.Helper()
	c.Listen.DNS = "127.0.0.1:0"
	m, err := config.NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	table := &rules.Table{Platforms: map[string]rules.Platform{"XboxGlobal": {Pool: "Akamai", Hosts: []string{"assets1.xboxlive.com"}}}}
	s := New(m, table, store, logstore.New(20), "192.0.2.99", ready...)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	return s, s.tcp.Listener.Addr().String()
}

func lookup(t *testing.T, addr, network string, kind uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion("assets1.xboxlive.com.", kind)
	r, _, err := (&dns.Client{Net: network, Timeout: 3 * time.Second}).Exchange(m, addr)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertIP(t *testing.T, r *dns.Msg, want string) {
	t.Helper()
	if len(r.Answer) != 1 {
		t.Fatalf("answers: %v", r.Answer)
	}
	a, ok := r.Answer[0].(*dns.A)
	if !ok || a.A.String() != want {
		t.Fatalf("answer = %v, want %s", r.Answer, want)
	}
}

func TestUnmeasuredAndFailedPoolsForwardUpstream(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmeasured", true: "failed"}[failed], func(t *testing.T) {
			c := config.Default()
			c.Upstream.DNS = []string{upstream(t, false, false)}
			store := ipstore.New()
			pool := store.Pool("Akamai")
			pool.SetList([]ipstore.IPEntry{{IP: "192.0.2.1"}})
			if failed {
				pool.UpdatePing("192.0.2.1", 5)
				pool.UpdateSpeed("192.0.2.1", 0)
				pool.ComputeBest()
			}
			_, addr := testServer(t, c, store)
			assertIP(t, lookup(t, addr, "udp", dns.TypeA), "203.0.113.10")
			if r := lookup(t, addr, "udp", dns.TypeAAAA); len(r.Answer) != 1 {
				t.Fatal("unhealthy pool suppressed working upstream IPv6")
			}
		})
	}
}

func TestTCPClientRecoversTruncatedUpstreamUDP(t *testing.T) {
	c := config.Default()
	c.Upstream.DNS = []string{upstream(t, true, false)}
	_, addr := testServer(t, c, ipstore.New())
	r := lookup(t, addr, "tcp", dns.TypeA)
	if r.Truncated {
		t.Fatal("TCP client still received a truncated reply")
	}
	assertIP(t, r, "203.0.113.10")
}

func TestSERVFAILTriesNextUpstream(t *testing.T) {
	c := config.Default()
	c.Upstream.DNS = []string{upstream(t, false, true), upstream(t, false, false)}
	_, addr := testServer(t, c, ipstore.New())
	assertIP(t, lookup(t, addr, "udp", dns.TypeA), "203.0.113.10")
}

func TestHiddenPlatformCannotBeHijackedByGlobalRule(t *testing.T) {
	c := config.Default()
	c.Redirect.Enabled = true
	c.Platforms["XboxGlobal"] = config.PlatformToggle{Hidden: true}
	c.Upstream.DNS = []string{upstream(t, false, false)}
	s, addr := testServer(t, c, ipstore.New())
	s.SetRedirectReady(true)
	assertIP(t, lookup(t, addr, "udp", dns.TypeA), "203.0.113.10")
}

func TestDNSHijackRequiresHealthyDownloadListeners(t *testing.T) {
	c := config.Default()
	c.Redirect.Enabled = true
	c.Upstream.DNS = []string{upstream(t, false, false)}
	var ready atomic.Bool
	_, addr := testServer(t, c, ipstore.New(), ready.Load)
	assertIP(t, lookup(t, addr, "udp", dns.TypeA), "203.0.113.10")
	ready.Store(true)
	assertIP(t, lookup(t, addr, "udp", dns.TypeA), "192.0.2.99")
	ready.Store(false)
	assertIP(t, lookup(t, addr, "udp", dns.TypeA), "203.0.113.10")
}
