// Package proxy 是 80 端口的 302 重写层（可开关）。
//
// 命中重写规则的请求：默认 302 跳到目标域名（目标域名 DNS 解析到真实快 IP，
// 直连下载，容器不碰流量）。开启智能兜底时，先探测目标 CDN 是否有该资源，
// 若返回 404/403（如 cn 没有该游戏）则就地反向代理回源域名，避免下载失败。
package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/netutil"
)

// poolLookup 把域名映射到 IP 池名（由 rules.Table 注入，静态）。
type poolLookup func(host string) string

// Proxy 是 302 重写服务。
type Proxy struct {
	cfg        *config.Manager
	store      *ipstore.Store
	logs       *logstore.Store
	poolOf     poolLookup
	rulesOf    func(*config.Config) map[string]string
	ipOf       func(string) string
	selfIP     string
	probes     singleflight.Group
	generation atomic.Uint64

	idx       atomic.Pointer[pindex]
	decisions *decisionCache
	builtin   []RedirectPair // 平台内置重写（main 注入）

	mu          sync.Mutex
	server      *http.Server
	tlsListener net.Listener
	tlsConns    map[net.Conn]struct{}
	tlsSlots    chan struct{}
	tlsDial     func(context.Context, string) (net.Conn, error)
}

// RedirectPair 是一条 from->to 重写。
type RedirectPair struct{ From, To string }

type pindex struct {
	rules      map[string]string // from -> to（已启用）
	smart      bool
	generation uint64
}

// New 创建重写代理。
func New(cfg *config.Manager, store *ipstore.Store, logs *logstore.Store, poolOf poolLookup) *Proxy {
	p := &Proxy{
		cfg:       cfg,
		store:     store,
		logs:      logs,
		poolOf:    poolOf,
		decisions: newDecisionCache(5*time.Minute, 4096),
		tlsConns:  make(map[net.Conn]struct{}),
		tlsSlots:  make(chan struct{}, 128),
	}
	p.tlsDial = func(ctx context.Context, ip string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	}
	p.Reload(cfg.Get())
	return p
}

// Reload 根据配置重建重写规则（用户规则 + 平台内置重写）。
func (p *Proxy) Reload(c *config.Config) {
	p.mu.Lock()
	rulesOf := p.rulesOf
	builtin := append([]RedirectPair(nil), p.builtin...)
	p.mu.Unlock()
	idx := &pindex{rules: make(map[string]string), smart: c.Redirect.SmartFallback, generation: p.generation.Add(1)}
	if !c.Redirect.Enabled {
		p.idx.Store(idx)
		return
	}
	for _, r := range c.Redirect.Rules {
		if r.Enabled && r.From != "" && r.To != "" {
			idx.rules[strings.ToLower(r.From)] = strings.ToLower(r.To)
		}
	}
	for _, r := range builtin {
		idx.rules[strings.ToLower(r.From)] = strings.ToLower(r.To)
	}
	if rulesOf != nil {
		idx.rules = rulesOf(c)
	}
	p.idx.Store(idx)
}

// SetBuiltin 注入平台内置重写对（来自 rules.Table.BuiltinRedirects），随后重建规则。
func (p *Proxy) SetBuiltin(pairs []RedirectPair) {
	p.mu.Lock()
	p.builtin = append([]RedirectPair(nil), pairs...)
	p.mu.Unlock()
	p.Reload(p.cfg.Get())
}

// SetResolvers injects the same platform-aware rules and pinned IPs used by DNS.
// Call at startup before serving requests.
func (p *Proxy) SetResolvers(rulesOf func(*config.Config) map[string]string, ipOf func(string) string, selfIP string) {
	p.mu.Lock()
	p.rulesOf, p.ipOf, p.selfIP = rulesOf, ipOf, selfIP
	p.mu.Unlock()
	p.Reload(p.cfg.Get())
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(hostOnly(r.Host))
	idx := p.idx.Load()
	to, ok := idx.rules[host]
	if !ok {
		http.Error(w, "no acceleration rule for host", http.StatusNotFound)
		return
	}
	client := clientIP(r.RemoteAddr)

	if idx.smart && p.targetMissing(r.Context(), to, r.URL.RequestURI(), idx.generation) {
		p.reverseProxy(w, r, host)
		p.logs.Log(logstore.KindProxy, host, client, "目标无此资源，回源代理 "+host)
		return
	}

	target := "http://" + to + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusFound)
	p.logs.Log(logstore.KindRedirect, host, client, "302 -> "+to)
}

// targetMissing probes the exact URI and node. Uncertain results use the original
// CDN for this request but are never cached as either present or missing.
func (p *Proxy) targetMissing(ctx context.Context, toHost, uri string, generation uint64) bool {
	resolveCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	bestIP, err := p.resolveIP(resolveCtx, hostOnly(toHost))
	if err != nil {
		return true
	}
	key := fmt.Sprintf("%d|%s|%s|%s", generation, toHost, bestIP, uri)
	if v, ok := p.decisions.get(key); ok {
		return v
	}
	result := p.probes.DoChan(key, func() (any, error) {
		if v, ok := p.decisions.get(key); ok {
			return v, nil
		}
		probeCtx, stop := context.WithTimeout(context.Background(), 4*time.Second)
		defer stop()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+toHost+uri, nil)
		if err != nil {
			return true, err
		}
		req.Header.Set("Range", "bytes=0-1")
		req.Header.Set("User-Agent", "XboxDownload")
		req.Header.Set("Accept-Encoding", "identity")
		cli := &http.Client{
			Timeout:       4 * time.Second,
			Transport:     netutil.ForcedIPTransport(bestIP, 4*time.Second),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		defer cli.CloseIdleConnections()
		resp, err := cli.Do(req)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		missing := false
		switch resp.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
		case http.StatusNotFound, http.StatusGone:
			missing = true
		default:
			return true, fmt.Errorf("probe HTTP %d", resp.StatusCode)
		}
		p.decisions.set(key, missing)
		return missing, nil
	})
	select {
	case <-ctx.Done():
		return true
	case r := <-result:
		if r.Err != nil {
			return true
		}
		return r.Val.(bool)
	}
}

func (p *Proxy) resolveIP(ctx context.Context, host string) (string, error) {
	ip := ""
	if p.ipOf != nil {
		ip = p.ipOf(host)
	} else if pool := p.poolOf(host); pool != "" {
		ip = p.store.BestForPool(pool)
	}
	if ip == "" || ip == p.selfIP {
		var err error
		ip, err = netutil.ResolveIPv4(ctx, host, p.cfg.Get().Upstream.DNS)
		if err != nil {
			return "", err
		}
	}
	if ip == p.selfIP {
		return "", fmt.Errorf("refusing proxy loop to local IP")
	}
	return ip, nil
}

// reverseProxy 把请求就地转发到 host 自己池里的最快 IP（Host 保持不变）。
func (p *Proxy) reverseProxy(w http.ResponseWriter, r *http.Request, host string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	bestIP, err := p.resolveIP(ctx, host)
	cancel()
	if err != nil {
		http.Error(w, "no upstream ip", http.StatusBadGateway)
		return
	}
	transport := netutil.ForcedIPTransport(bestIP, 10*time.Second)
	transport.ResponseHeaderTimeout = 10 * time.Second
	defer transport.CloseIdleConnections()
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = host
			req.Host = host
		},
		Transport: transport,
	}
	rp.ServeHTTP(w, r)
}

// Start 在 addr 上启动 80 端口监听。
func (p *Proxy) Start(addr, tlsAddr string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return nil
	}
	srv := &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	tlsLn, err := net.Listen("tcp", tlsAddr)
	if err != nil {
		ln.Close()
		return err
	}
	p.server, p.tlsListener = srv, tlsLn
	go func() {
		_ = srv.Serve(ln)
		p.stopIfCurrent(srv)
	}()
	go p.serveTLS(tlsLn, srv)
	return nil
}

// Stop 停止 80 端口监听。
func (p *Proxy) Stop() {
	p.mu.Lock()
	srv := p.server
	p.server = nil
	tlsLn := p.tlsListener
	p.tlsListener = nil
	for conn := range p.tlsConns {
		conn.Close()
		delete(p.tlsConns, conn)
	}
	p.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	if tlsLn != nil {
		tlsLn.Close()
	}
}

func (p *Proxy) stopIfCurrent(srv *http.Server) {
	p.mu.Lock()
	if p.server != srv {
		p.mu.Unlock()
		return
	}
	p.server = nil
	ln := p.tlsListener
	p.tlsListener = nil
	for conn := range p.tlsConns {
		conn.Close()
		delete(p.tlsConns, conn)
	}
	p.mu.Unlock()
	srv.Close()
	if ln != nil {
		ln.Close()
	}
}

// Running 返回 80 端口是否在监听。
func (p *Proxy) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.server != nil && p.tlsListener != nil
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func clientIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
