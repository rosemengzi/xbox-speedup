// Package proxy 是 80 端口的 302 重写层（可开关）。
//
// 命中重写规则的请求：默认 302 跳到目标域名（目标域名 DNS 解析到真实快 IP，
// 直连下载，容器不碰流量）。开启智能兜底时，先探测目标 CDN 是否有该资源，
// 若返回 404/403（如 cn 没有该游戏）则就地反向代理回源域名，避免下载失败。
package proxy

import (
	"net"
	"net/http"
	"net/http/httputil"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/netutil"
)

// poolLookup 把域名映射到 IP 池名（由 rules.Table 注入，静态）。
type poolLookup func(host string) string

// Proxy 是 302 重写服务。
type Proxy struct {
	cfg      *config.Manager
	store    *ipstore.Store
	logs     *logstore.Store
	poolOf   poolLookup
	probeCli *http.Client

	idx       atomic.Pointer[pindex]
	decisions *decisionCache
	builtin   []RedirectPair // 平台内置重写（main 注入）

	mu     sync.Mutex
	server *http.Server
}

// RedirectPair 是一条 from->to 重写。
type RedirectPair struct{ From, To string }

type pindex struct {
	rules map[string]string // from -> to（已启用）
	smart bool
}

// New 创建重写代理。
func New(cfg *config.Manager, store *ipstore.Store, logs *logstore.Store, poolOf poolLookup) *Proxy {
	p := &Proxy{
		cfg:       cfg,
		store:     store,
		logs:      logs,
		poolOf:    poolOf,
		probeCli:  &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		decisions: newDecisionCache(time.Hour, 4096),
	}
	p.Reload(cfg.Get())
	return p
}

// Reload 根据配置重建重写规则（用户规则 + 平台内置重写）。
func (p *Proxy) Reload(c *config.Config) {
	idx := &pindex{rules: make(map[string]string), smart: c.Redirect.SmartFallback}
	for _, r := range c.Redirect.Rules {
		if r.Enabled && r.From != "" && r.To != "" {
			idx.rules[strings.ToLower(r.From)] = strings.ToLower(r.To)
		}
	}
	for _, r := range p.builtin {
		idx.rules[strings.ToLower(r.From)] = strings.ToLower(r.To)
	}
	p.idx.Store(idx)
}

// SetBuiltin 注入平台内置重写对（来自 rules.Table.BuiltinRedirects），随后重建规则。
func (p *Proxy) SetBuiltin(pairs []RedirectPair) {
	p.builtin = pairs
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

	if idx.smart && p.targetMissing(to, r.URL.Path) {
		p.reverseProxy(w, r, host)
		p.logs.Log(logstore.KindProxy, host, client, "目标无此资源，回源代理 "+host)
		return
	}

	target := "http://" + to + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusFound)
	p.logs.Log(logstore.KindRedirect, host, client, "302 -> "+to)
}

// targetMissing 探测目标域名的最快 IP 是否缺该资源（404/403）。
func (p *Proxy) targetMissing(toHost, urlPath string) bool {
	pool := p.poolOf(toHost)
	if pool == "" {
		return false
	}
	bestIP := p.store.BestForPool(pool)
	if bestIP == "" {
		return false // 无可探测的 IP，不阻断
	}
	key := toHost + "|" + path.Dir(urlPath)
	if v, ok := p.decisions.get(key); ok {
		return v
	}

	missing := false
	req, err := http.NewRequest(http.MethodGet, "http://"+toHost+urlPath, nil)
	if err == nil {
		req.Header.Set("Range", "bytes=0-1")
		req.Header.Set("User-Agent", "XboxDownload")
		cli := &http.Client{
			Timeout:       4 * time.Second,
			Transport:     netutil.ForcedIPTransport(bestIP, 4*time.Second),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		resp, derr := cli.Do(req)
		if derr == nil {
			switch resp.StatusCode {
			case http.StatusNotFound, http.StatusForbidden, http.StatusGone:
				missing = true
			}
			resp.Body.Close()
		}
		cli.CloseIdleConnections()
	}
	p.decisions.set(key, missing)
	return missing
}

// reverseProxy 把请求就地转发到 host 自己池里的最快 IP（Host 保持不变）。
func (p *Proxy) reverseProxy(w http.ResponseWriter, r *http.Request, host string) {
	pool := p.poolOf(host)
	bestIP := ""
	if pool != "" {
		bestIP = p.store.BestForPool(pool)
	}
	if bestIP == "" {
		http.Error(w, "no upstream ip", http.StatusBadGateway)
		return
	}
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = host
			req.Host = host
		},
		Transport: netutil.ForcedIPTransport(bestIP, 10*time.Second),
	}
	rp.ServeHTTP(w, r)
}

// Start 在 addr 上启动 80 端口监听。
func (p *Proxy) Start(addr string) error {
	p.mu.Lock()
	if p.server != nil {
		p.mu.Unlock()
		return nil
	}
	srv := &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 10 * time.Second}
	p.server = srv
	p.mu.Unlock()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		p.mu.Lock()
		p.server = nil
		p.mu.Unlock()
		return err
	}
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// Stop 停止 80 端口监听。
func (p *Proxy) Stop() {
	p.mu.Lock()
	srv := p.server
	p.server = nil
	p.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// Running 返回 80 端口是否在监听。
func (p *Proxy) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.server != nil
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
