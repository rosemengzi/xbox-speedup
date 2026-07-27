// Package dnssrv 是核心 DNS 服务：命中加速域名返回最快 IP，
// 命中黑名单返回 0.0.0.0，命中重写源域名返回容器自身 IP（交给 80 端口 302），
// 其余转发上游。加速域名的 AAAA 按需返回空，逼客户端走 IPv4 快 IP。
package dnssrv

import (
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/rules"
)

const answerTTL = 30

// Server 是 DNS 服务器。
type Server struct {
	cfg         *config.Manager
	table       *rules.Table
	store       *ipstore.Store
	logs        *logstore.Store
	advertiseIP string

	idx      atomic.Pointer[index]
	upClient *dns.Client

	udp *dns.Server
	tcp *dns.Server
}

// index 是从 config + table 预计算的解析索引，随配置变更原子替换。
type index struct {
	accelPlatform map[string]string // 加速域名 -> 平台
	hostPool      map[string]string // 加速域名 -> IP 池
	pinned        map[string]string // 平台 -> 锁定 IP
	blacklist     map[string]bool   // 屏蔽域名
	hijack        map[string]bool   // 重写源域名（redirect 开启时返回容器 IP）
	ipv6Filter    bool
	logAllQueries bool // 记录全部查询（含转发）
}

// New 创建 DNS 服务器。advertiseIP 为容器对外 IP（被劫持域名的 A 记录）。
func New(cfg *config.Manager, table *rules.Table, store *ipstore.Store, logs *logstore.Store, advertiseIP string) *Server {
	s := &Server{
		cfg:         cfg,
		table:       table,
		store:       store,
		logs:        logs,
		advertiseIP: advertiseIP,
		upClient:    &dns.Client{Timeout: 5 * time.Second},
	}
	s.Reload(cfg.Get())
	return s
}

// Reload 依据新配置重建解析索引。
func (s *Server) Reload(c *config.Config) {
	idx := &index{
		accelPlatform: make(map[string]string),
		hostPool:      make(map[string]string),
		pinned:        make(map[string]string),
		blacklist:     make(map[string]bool),
		hijack:        make(map[string]bool),
		ipv6Filter:    c.IPv6Filter,
		logAllQueries: c.LogAllQueries,
	}
	for name, p := range s.table.Platforms {
		tg, ok := c.Platforms[name]
		if !ok || !tg.Enabled {
			continue
		}
		for _, h := range p.Hosts {
			h = strings.ToLower(h)
			idx.accelPlatform[h] = name
			idx.hostPool[h] = p.Pool
		}
		for _, b := range p.Blacklist {
			idx.blacklist[strings.ToLower(b)] = true
		}
		if tg.PinnedIP != "" {
			idx.pinned[name] = tg.PinnedIP
		}
	}
	if c.Redirect.Enabled && s.advertiseIP != "" {
		for _, r := range c.Redirect.Rules {
			if r.Enabled {
				idx.hijack[strings.ToLower(r.From)] = true
			}
		}
		for name, p := range s.table.Platforms {
			if tg, ok := c.Platforms[name]; !ok || !tg.Enabled {
				continue
			}
			for _, r := range p.Redirects {
				idx.hijack[strings.ToLower(r.From)] = true
			}
		}
	}
	s.idx.Store(idx)
}

// Start 在 UDP 与 TCP 上监听 DNS。
func (s *Server) Start() error {
	addr := s.cfg.Get().Listen.DNS
	handler := dns.HandlerFunc(s.handle)
	s.udp = &dns.Server{Addr: addr, Net: "udp", Handler: handler}
	s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: handler}

	errCh := make(chan error, 2)
	go func() { errCh <- s.udp.ListenAndServe() }()
	go func() { errCh <- s.tcp.ListenAndServe() }()
	// 给监听一点启动时间，及时暴露端口占用等错误。
	select {
	case err := <-errCh:
		return err
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

// Shutdown 停止 DNS 服务。
func (s *Server) Shutdown() {
	if s.udp != nil {
		_ = s.udp.Shutdown()
	}
	if s.tcp != nil {
		_ = s.tcp.Shutdown()
	}
}

func (s *Server) handle(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) == 0 {
		s.forward(w, r)
		return
	}
	q := r.Question[0]
	name := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	client := clientIP(w.RemoteAddr())
	idx := s.idx.Load()

	switch q.Qtype {
	case dns.TypeA:
		s.handleA(w, r, idx, name, client)
	case dns.TypeAAAA:
		s.handleAAAA(w, r, idx, name, client)
	default:
		s.forward(w, r)
	}
}

func (s *Server) handleA(w dns.ResponseWriter, r *dns.Msg, idx *index, name, client string) {
	if idx.blacklist[name] {
		s.writeA(w, r, "0.0.0.0")
		s.logs.Log(logstore.KindBlock, name, client, "0.0.0.0")
		return
	}
	if idx.hijack[name] {
		s.writeA(w, r, s.advertiseIP)
		s.logs.Log(logstore.KindRedirect, name, client, "劫持到本机 "+s.advertiseIP)
		return
	}
	if platform, ok := idx.accelPlatform[name]; ok {
		ip := idx.pinned[platform]
		if ip == "" {
			ip = s.store.BestForPool(idx.hostPool[name])
		}
		if ip == "" {
			s.forward(w, r) // 还没测速结果，先转发保证可用
			return
		}
		s.writeA(w, r, ip)
		s.logs.Log(logstore.KindDNSA, name, client, ip)
		return
	}
	s.forwardMaybeLog(w, r, idx, name, client, "A")
}

func (s *Server) handleAAAA(w dns.ResponseWriter, r *dns.Msg, idx *index, name, client string) {
	managed := idx.blacklist[name] || idx.hijack[name]
	if _, ok := idx.accelPlatform[name]; ok {
		managed = true
	}
	if managed && idx.ipv6Filter {
		m := new(dns.Msg)
		m.SetReply(r)
		m.RecursionAvailable = true
		_ = w.WriteMsg(m) // 空应答：无 AAAA 记录，逼走 IPv4
		s.logs.Log(logstore.KindDNSAAAA, name, client, "过滤 IPv6")
		return
	}
	s.forwardMaybeLog(w, r, idx, name, client, "AAAA")
}

func (s *Server) writeA(w dns.ResponseWriter, r *dns.Msg, ip string) {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		m := new(dns.Msg)
		m.SetReply(r)
		m.RecursionAvailable = true
		_ = w.WriteMsg(m)
		return
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = true
	m.Answer = append(m.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: answerTTL},
		A:   parsed.To4(),
	})
	_ = w.WriteMsg(m)
}

// forwardMaybeLog 在开启「记录全部查询」时记一条 FORWARD，再转发上游。
func (s *Server) forwardMaybeLog(w dns.ResponseWriter, r *dns.Msg, idx *index, name, client, qtype string) {
	if idx.logAllQueries {
		s.logs.Log(logstore.KindForward, name, client, qtype+" → 上游")
	}
	s.forward(w, r)
}

// forward 把查询转发给上游 DNS，返回首个成功应答。
func (s *Server) forward(w dns.ResponseWriter, r *dns.Msg) {
	ups := s.cfg.Get().Upstream.DNS
	for _, up := range ups {
		resp, _, err := s.upClient.Exchange(r, up)
		if err == nil && resp != nil {
			_ = w.WriteMsg(resp)
			return
		}
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeServerFailure
	_ = w.WriteMsg(m)
}

func clientIP(addr net.Addr) string {
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		return host
	}
	return addr.String()
}
