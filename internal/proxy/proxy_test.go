package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
)

func testProxy(t *testing.T, target *httptest.Server) *Proxy {
	t.Helper()
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	c := config.Default()
	c.Redirect.Enabled = true
	c.Redirect.Rules = []config.RedirectRule{{From: "source.example", To: "target.example:" + port, Enabled: true}}
	m, err := config.NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Replace(c); err != nil {
		t.Fatal(err)
	}
	s := ipstore.New()
	s.Pool("target").SetList([]ipstore.IPEntry{{IP: "127.0.0.1"}})
	s.Pool("target").UpdateSpeed("127.0.0.1", 10)
	s.Pool("target").ComputeBest()
	return New(m, s, logstore.New(20), func(host string) string {
		if host == "target.example" {
			return "target"
		}
		return ""
	})
}

func TestProbePreservesQueryAndEscapedPath(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "valid" || r.URL.EscapedPath() != "/game/a%2Fb" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer target.Close()
	p := testProxy(t, target)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "http://source.example/game/a%2Fb?token=valid", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("valid target misclassified: %d", w.Code)
	}
	if want := p.cfg.Get().Redirect.Rules[0].To + "/game/a%2Fb?token=valid"; w.Header().Get("Location") != "http://"+want {
		t.Fatal("redirect lost original URI")
	}
}

func TestMissingResourceDoesNotPoisonSibling(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/game/missing" {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusPartialContent)
		}
	}))
	defer target.Close()
	p := testProxy(t, target)
	to := p.cfg.Get().Redirect.Rules[0].To
	generation := p.idx.Load().generation
	if !p.targetMissing(context.Background(), to, "/game/missing", generation) {
		t.Fatal("missing resource not detected")
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "http://source.example/game/exists", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("existing sibling misclassified: %d", w.Code)
	}
}

func TestProbeFailuresAreNotCached(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusPartialContent)
		}
	}))
	defer target.Close()
	p := testProxy(t, target)
	to := p.cfg.Get().Redirect.Rules[0].To
	generation := p.idx.Load().generation
	if !p.targetMissing(context.Background(), to, "/game/chunk", generation) {
		t.Fatal("failed probe should use original CDN")
	}
	if p.targetMissing(context.Background(), to, "/game/chunk", generation) {
		t.Fatal("failed probe was cached instead of retried")
	}
	if requests.Load() != 2 {
		t.Fatal("probe did not retry")
	}
}

func TestReloadInvalidatesProbeDecision(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusPartialContent)
		}
	}))
	defer target.Close()
	p := testProxy(t, target)
	to := p.cfg.Get().Redirect.Rules[0].To
	if !p.targetMissing(context.Background(), to, "/game/chunk", p.idx.Load().generation) {
		t.Fatal("expected missing")
	}
	p.Reload(p.cfg.Get())
	if p.targetMissing(context.Background(), to, "/game/chunk", p.idx.Load().generation) {
		t.Fatal("old rule decision survived reload")
	}
}

func TestTLSBindFailureDoesNotLeaveHTTPRunning(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	p := testProxy(t, target)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := p.Start("127.0.0.1:0", occupied.Addr().String()); err == nil {
		p.Stop()
		t.Fatal("expected occupied TLS port error")
	}
	if p.Running() {
		t.Fatal("partial download service advertised as healthy")
	}
}
