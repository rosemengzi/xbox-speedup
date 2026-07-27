// Package web 提供管理界面：状态、实时日志(SSE)、手动测速/同步、配置编辑。
package web

import (
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"time"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/proxy"
	"xboxspeedup/internal/rules"
)

//go:embed ui/*
var uiFS embed.FS

// Deps 是 Web 服务的依赖注入。
type Deps struct {
	Cfg              *config.Manager
	Table            *rules.Table
	Store            *ipstore.Store
	Logs             *logstore.Store
	Proxy            *proxy.Proxy
	AdvertiseIP      string
	TriggerSpeedTest func(pool string)
	TriggerSync      func()
	SpeedTestRunning func() bool
}

// Server 是 Web 管理服务。
type Server struct {
	d    Deps
	srvs []*http.Server
}

// New 创建 Web 服务。
func New(d Deps) *Server { return &Server{d: d} }

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/logs", s.handleLogsSSE)
	mux.HandleFunc("/api/logs/recent", s.handleLogsRecent)
	mux.HandleFunc("/api/pool", s.handlePool)
	mux.HandleFunc("/api/speedtest", s.handleSpeedTest)
	mux.HandleFunc("/api/sync", s.handleSync)

	sub, _ := fs.Sub(uiFS, "ui")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

// Start 监听明文 HTTP addr。
func (s *Server) Start(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.mux(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.srvs = append(s.srvs, srv)
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// StartTLS 监听 HTTPS addr。
func (s *Server) StartTLS(addr, certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: s.mux(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	s.srvs = append(s.srvs, srv)
	go func() { _ = srv.Serve(tlsLn) }()
	return nil
}

// Shutdown 停止 Web 服务。
func (s *Server) Shutdown() {
	for _, srv := range s.srvs {
		_ = srv.Close()
	}
}

// ---- 状态 ----

type platformStatus struct {
	Key         string  `json:"key"`
	Description string  `json:"description"`
	Pool        string  `json:"pool"`
	Enabled     bool    `json:"enabled"`
	SpeedTest   bool    `json:"speedtest"`
	Hidden      bool    `json:"hidden"`
	Pinned      string  `json:"pinned"`
	BestIP      string  `json:"best_ip"`
	BestLoc     string  `json:"best_loc"`
	BestRTT     float64 `json:"best_rtt"`
	BestSpeed   float64 `json:"best_speed"`
	IPCount     int     `json:"ip_count"`
}

type statusResp struct {
	AdvertiseIP     string           `json:"advertise_ip"`
	Listen          config.Listen    `json:"listen"`
	Upstream        []string         `json:"upstream"`
	IPv6Filter      bool             `json:"ipv6_filter"`
	RedirectEnabled bool             `json:"redirect_enabled"`
	SmartFallback   bool             `json:"smart_fallback"`
	ProxyRunning    bool             `json:"proxy_running"`
	SpeedTestBusy   bool             `json:"speedtest_running"`
	Platforms       []platformStatus `json:"platforms"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	c := s.d.Cfg.Get()
	resp := statusResp{
		AdvertiseIP:     s.d.AdvertiseIP,
		Listen:          c.Listen,
		Upstream:        c.Upstream.DNS,
		IPv6Filter:      c.IPv6Filter,
		RedirectEnabled: c.Redirect.Enabled,
		SmartFallback:   c.Redirect.SmartFallback,
		ProxyRunning:    s.d.Proxy.Running(),
		SpeedTestBusy:   s.d.SpeedTestRunning != nil && s.d.SpeedTestRunning(),
	}

	keys := make([]string, 0, len(s.d.Table.Platforms))
	for k := range s.d.Table.Platforms {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		p := s.d.Table.Platforms[k]
		tg := c.Platforms[k]
		pool := s.d.Store.Pool(p.Pool)
		best := pool.Best()
		ps := platformStatus{
			Key:         k,
			Description: p.Description,
			Pool:        p.Pool,
			Enabled:     tg.Enabled,
			SpeedTest:   tg.SpeedTest,
			Hidden:      tg.Hidden,
			Pinned:      tg.PinnedIP,
			BestIP:      best,
			IPCount:     len(pool.IPs()),
		}
		for _, r := range pool.Records() {
			if r.IP == best {
				ps.BestLoc = r.Location
				ps.BestRTT = r.RTTms
				ps.BestSpeed = r.SpeedMBps
				break
			}
		}
		resp.Platforms = append(resp.Platforms, ps)
	}
	writeJSON(w, resp)
}

// ---- 配置 ----

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.d.Cfg.Get())
	case http.MethodPost:
		var c config.Config
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "无效配置: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.d.Cfg.Replace(&c); err != nil {
			http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.d.Logs.Log(logstore.KindSystem, "", "", "配置已更新")
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---- 日志 ----

func (s *Server) handleLogsRecent(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.d.Logs.Snapshot())
}

func (s *Server) handleLogsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.d.Logs.Subscribe()
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// ---- IP 池 ----

func (s *Server) handlePool(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing pool name", http.StatusBadRequest)
		return
	}
	pool := s.d.Store.Pool(name)
	writeJSON(w, struct {
		Name    string           `json:"name"`
		Best    string           `json:"best"`
		Records []ipstore.Record `json:"records"`
	}{Name: name, Best: pool.Best(), Records: pool.Records()})
}

// ---- 动作 ----

func (s *Server) handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pool string `json:"pool"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	go s.d.TriggerSpeedTest(body.Pool)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSync(w http.ResponseWriter, _ *http.Request) {
	go s.d.TriggerSync()
	writeJSON(w, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
