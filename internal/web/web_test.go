package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/proxy"
	"xboxspeedup/internal/rules"
)

func testWeb(t *testing.T) *Server {
	t.Helper()
	m, err := config.NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	store := ipstore.New()
	logs := logstore.New(20)
	return New(Deps{Cfg: m, Store: store, Logs: logs, Proxy: proxy.New(m, store, logs, func(string) string { return "" }), Table: &rules.Table{Pools: map[string]rules.Pool{"Akamai": {}}, Platforms: map[string]rules.Platform{"XboxGlobal": {Pool: "Akamai"}}}, AdminToken: "test-password", StartedConfig: config.Clone(m.Get()), TriggerSpeedTest: func(string) {}, TriggerSync: func() {}})
}

func request(s *Server, method, path, password, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://nas.example"+path, nil)
	if password != "" {
		r.SetBasicAuth("admin", password)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.guard(s.mux()).ServeHTTP(w, r)
	return w
}

func TestManagementAuthenticationAndPublicHealth(t *testing.T) {
	s := testWeb(t)
	for _, password := range []string{"", "wrong"} {
		if w := request(s, "GET", "/api/config", password, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous/incorrect login allowed: %d", w.Code)
		}
	}
	if w := request(s, "GET", "/api/config", "test-password", ""); w.Code != 200 {
		t.Fatal("correct login rejected")
	}
	if w := request(s, "GET", "/healthz", "", ""); w.Code != 200 {
		t.Fatal("health requires login")
	}
	s.d.Healthy = func() bool { return false }
	if w := request(s, "GET", "/healthz", "", ""); w.Code != 503 {
		t.Fatal("unhealthy service reported ready")
	}
}

func TestActionsRequirePOSTAndRejectCrossOrigin(t *testing.T) {
	s := testWeb(t)
	for _, path := range []string{"/api/speedtest", "/api/sync"} {
		if w := request(s, "GET", path, "test-password", ""); w.Code != 405 {
			t.Fatalf("GET action allowed: %s", path)
		}
		if w := request(s, "POST", path, "test-password", "https://evil.example"); w.Code != 403 {
			t.Fatal("cross-origin action allowed")
		}
	}
}

func TestStatusReportsPinnedIPAndPendingRestart(t *testing.T) {
	s := testWeb(t)
	p := s.d.Store.Pool("Akamai")
	p.SetList([]ipstore.IPEntry{{IP: "192.0.2.1"}})
	p.UpdateSpeed("192.0.2.1", 10)
	p.ComputeBest()
	c := config.Clone(s.d.Cfg.Get())
	toggle := c.Platforms["XboxGlobal"]
	toggle.PinnedIP = "192.0.2.2"
	c.Platforms["XboxGlobal"] = toggle
	c.Listen.Web = ":9090"
	if err := s.d.Cfg.Replace(c); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/status", "test-password", "")
	var status statusResp
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.RestartRequired || status.Listen.Web != ":8080" {
		t.Fatal("desired listener misreported as running")
	}
	if status.Platforms[0].BestIP != "192.0.2.2" || status.Platforms[0].AutoIP != "192.0.2.1" {
		t.Fatal("pinned and automatic IPs not distinguished")
	}
}

func TestGeneratedPasswordSurvivesRestart(t *testing.T) {
	t.Setenv("XBOX_WEB_TOKEN", "")
	dir := t.TempDir()
	one, err := LoadAdminToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	two, err := LoadAdminToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) < 32 || one != two {
		t.Fatal("generated password was weak or changed on restart")
	}
}
