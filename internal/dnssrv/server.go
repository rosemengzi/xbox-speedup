// Package dnssrv 是核心 DNS 服务：命中加速域名返回最快 IP，
// 命中黑名单返回 0.0.0.0，命中重写源域名返回容器自身 IP（交给 80 端口 302），
// 其余转发上游。加速域名的 AAAA 按需返回空，逼客户端走 IPv4 快 IP。
package dnssrv

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/netutil"
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

	idx           atomic.Pointer[index]
	redirectReady atomic.Bool
	redirectCheck func() bool
	running       atomic.Bool

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
func New(cfg *config.Manager, table *rules.Table, store *ipstore.Store, logs *logstore.Store, advertiseIP string, ready ...func() bool) *Server {
	s := &Server{
		cfg:         cfg,
		table:       table,
		store:       store,
		logs:        logs,
		advertiseIP: advertiseIP,
	}
	if len(ready) > 0 {
		s.redirectCheck = ready[0]
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
		if !ok || !tg.Enabled || tg.Hidden {
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
		for from := range s.table.EffectiveRedirects(c) {
			idx.hijack[from] = true
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

	// Bind both transports synchronously; readiness is not a timed guess.
	packet, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", packet.LocalAddr().String())
	if err != nil {
		packet.Close()
		return err
	}
	s.udp.PacketConn = packet
	s.tcp.Listener = listener
	started := make(chan struct{}, 2)
	errCh := make(chan error, 2)
	s.udp.NotifyStartedFunc = func() { started <- struct{}{} }
	s.tcp.NotifyStartedFunc = func() { started <- struct{}{} }
	s.running.Store(true)
	go func() { errCh <- s.udp.ActivateAndServe(); s.running.Store(false) }()
	go func() { errCh <- s.tcp.ActivateAndServe(); s.running.Store(false) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case err := <-errCh:
			packet.Close()
			listener.Close()
			s.running.Store(false)
			return err
		}
	}
	return nil
}

// SetRedirectReady prevents DNS hijacking before both download listeners exist.
func (s *Server) SetRedirectReady(ready bool) { s.redirectReady.Store(ready) }

func (s *Server) canRedirect() bool {
	if s.redirectCheck != nil {
		return s.redirectCheck()
	}
	return s.redirectReady.Load()
}

func (s *Server) Running() bool { return s.running.Load() }

// Shutdown 停止 DNS 服务。
func (s *Server) Shutdown() {
	s.running.Store(false)
	s.redirectReady.Store(false)
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
	if idx.hijack[name] && s.canRedirect() {
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
	managed := idx.blacklist[name] || (idx.hijack[name] && s.canRedirect())
	if platform, ok := idx.accelPlatform[name]; ok {
		managed = managed || idx.pinned[platform] != "" || s.store.BestForPool(idx.hostPool[name]) != ""
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := netutil.ExchangeDNS(ctx, r, s.cfg.Get().Upstream.DNS)
	if err == nil {
		_ = w.WriteMsg(resp)
		return
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
