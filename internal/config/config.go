// Package config validates, persists and serializes runtime configuration changes.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/miekg/dns"
	"github.com/robfig/cron/v3"
	"xboxspeedup/internal/persist"

	"gopkg.in/yaml.v3"
)

// Config 是整份运行期配置，对应 config.yaml。
// 同时被 YAML（落盘）与 JSON（Web API）序列化，故两套标签都带。
type Config struct {
	Listen        Listen                    `yaml:"listen" json:"listen"`
	WebTLS        WebTLS                    `yaml:"web_tls" json:"web_tls"`
	AdvertiseIP   string                    `yaml:"advertise_ip" json:"advertise_ip"` // 容器对外 IP，留空=自动探测
	Upstream      Upstream                  `yaml:"upstream" json:"upstream"`
	IPv6Filter    bool                      `yaml:"ipv6_filter" json:"ipv6_filter"`
	LogAllQueries bool                      `yaml:"log_all_queries" json:"log_all_queries"` // 记录全部 DNS 查询（含转发），默认只记加速命中
	DataDir       string                    `yaml:"data_dir" json:"data_dir"`
	Platforms     map[string]PlatformToggle `yaml:"platforms" json:"platforms"`
	Redirect      Redirect                  `yaml:"redirect" json:"redirect"`
	SpeedTest     SpeedTest                 `yaml:"speedtest" json:"speedtest"`
	IPSync        IPSync                    `yaml:"ip_sync" json:"ip_sync"`
}

// Listen 是各服务监听地址。
type Listen struct {
	DNS  string `yaml:"dns" json:"dns"`
	HTTP string `yaml:"http" json:"http"`
	Web  string `yaml:"web" json:"web"`
}

// WebTLS 是管理界面的 HTTPS 监听配置。
type WebTLS struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Addr     string `yaml:"addr" json:"addr"`
	CertFile string `yaml:"cert_file" json:"cert_file"`
	KeyFile  string `yaml:"key_file" json:"key_file"`
}

// Upstream 是非加速域名的上游 DNS。
type Upstream struct {
	DNS []string `yaml:"dns" json:"dns"`
}

// PlatformToggle 是单个平台的运行期开关。
type PlatformToggle struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	SpeedTest bool   `yaml:"speedtest" json:"speedtest"`
	Hidden    bool   `yaml:"hidden" json:"hidden"`       // 隐藏=从界面移除，且强制不启用、不测速
	PinnedIP  string `yaml:"pinned_ip" json:"pinned_ip"` // 非空=锁定用该 IP，跳过测速结果
}

// Redirect 是 80 端口 302 重写层配置。
type Redirect struct {
	Enabled       bool           `yaml:"enabled" json:"enabled"`
	SmartFallback bool           `yaml:"smart_fallback" json:"smart_fallback"`
	TLSAddr       string         `yaml:"tls_addr" json:"tls_addr"` // 下载 TLS 透传，随重写层启停
	Rules         []RedirectRule `yaml:"rules" json:"rules"`
}

// RedirectRule 是一条用户可开关的双向重写规则。
type RedirectRule struct {
	From    string `yaml:"from" json:"from"`
	To      string `yaml:"to" json:"to"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

// SpeedTest 是测速配置。
type SpeedTest struct {
	Enabled          bool   `yaml:"enabled" json:"enabled"`
	Schedule         string `yaml:"schedule" json:"schedule"` // cron 表达式
	PingTopN         int    `yaml:"ping_top_n" json:"ping_top_n"`
	DownloadMB       int    `yaml:"download_mb" json:"download_mb"`
	TimeoutSeconds   int    `yaml:"timeout_seconds" json:"timeout_seconds"`
	FreshnessMinutes int    `yaml:"freshness_minutes" json:"freshness_minutes"`
}

// IPSync 是 IP 列表在线同步配置。
type IPSync struct {
	Enabled  bool     `yaml:"enabled" json:"enabled"`
	Schedule string   `yaml:"schedule" json:"schedule"`
	Proxies  []string `yaml:"proxies" json:"proxies"` // 代理前缀，按序尝试；""=直连 raw.githubusercontent
}

// Default 返回内置默认配置。
func Default() *Config {
	return &Config{
		Listen:      Listen{DNS: ":53", HTTP: ":80", Web: ":8080"},
		WebTLS:      WebTLS{Enabled: false, Addr: ":8443", CertFile: "/certs/fullchain.pem", KeyFile: "/certs/privkey.pem"},
		AdvertiseIP: "",
		Upstream:    Upstream{DNS: []string{"223.5.5.5:53", "119.29.29.29:53"}},
		IPv6Filter:  true,
		DataDir:     "/data",
		Platforms: map[string]PlatformToggle{
			"XboxGlobal": {Enabled: true, SpeedTest: true},
			"XboxCn1":    {Enabled: true, SpeedTest: true},
			"XboxCn2":    {Enabled: true, SpeedTest: true},
			"XboxApp":    {Enabled: true, SpeedTest: false},
			"Ps":         {Enabled: false, SpeedTest: false},
			"Ns":         {Enabled: false, SpeedTest: false},
			"Ea":         {Enabled: false, SpeedTest: false},
			"Battle":     {Enabled: false, SpeedTest: false},
		},
		Redirect: Redirect{
			Enabled:       false,
			SmartFallback: true,
			TLSAddr:       ":443",
			Rules: []RedirectRule{
				{From: "assets1.xboxlive.com", To: "assets1.xboxlive.cn", Enabled: true},
				{From: "assets2.xboxlive.com", To: "assets2.xboxlive.cn", Enabled: true},
				{From: "dlassets.xboxlive.com", To: "dlassets.xboxlive.cn", Enabled: true},
				{From: "dlassets2.xboxlive.com", To: "dlassets2.xboxlive.cn", Enabled: true},
				{From: "assets1.xboxlive.cn", To: "assets1.xboxlive.com", Enabled: false},
			},
		},
		SpeedTest: SpeedTest{
			Enabled:          true,
			Schedule:         "0 */6 * * *",
			PingTopN:         10,
			DownloadMB:       30,
			TimeoutSeconds:   10,
			FreshnessMinutes: 30,
		},
		IPSync: IPSync{
			Enabled:  true,
			Schedule: "0 4 * * *",
			Proxies: []string{
				"https://pxy1.skydevil.xyz/",
				"https://pxy2.skydevil.xyz/",
				"https://gh-proxy.com/",
				"https://ghproxy.net/",
				"",
			},
		},
	}
}

// Manager serializes disk writes and callbacks; Get snapshots are immutable.
type Manager struct {
	path      string
	cur       *Config
	mu        sync.RWMutex
	applyMu   sync.Mutex
	onApply   []func(*Config) error
	validator func(*Config) error
}

// NewManager 从 path 加载配置；文件不存在则写入默认配置。
func NewManager(path string) (*Manager, error) {
	m := &Manager{path: path}
	cfg, err := load(path)
	if err != nil {
		return nil, err
	}
	m.cur = cfg
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err := save(path, cfg); err != nil {
			return nil, fmt.Errorf("保存默认配置失败: %w", err)
		}
	}
	return m, nil
}

func load(path string) (*Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	normalize(cfg)
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalize 修补缺省/非法字段，避免零值导致服务异常。
func normalize(c *Config) {
	d := Default()
	if c.Listen.DNS == "" {
		c.Listen.DNS = d.Listen.DNS
	}
	if c.Listen.Web == "" {
		c.Listen.Web = d.Listen.Web
	}
	if c.Listen.HTTP == "" {
		c.Listen.HTTP = d.Listen.HTTP
	}
	if c.WebTLS.Addr == "" {
		c.WebTLS.Addr = d.WebTLS.Addr
	}
	if c.WebTLS.CertFile == "" {
		c.WebTLS.CertFile = d.WebTLS.CertFile
	}
	if c.WebTLS.KeyFile == "" {
		c.WebTLS.KeyFile = d.WebTLS.KeyFile
	}
	if c.Redirect.TLSAddr == "" {
		c.Redirect.TLSAddr = d.Redirect.TLSAddr
	}
	for i := range c.Redirect.Rules {
		c.Redirect.Rules[i].From = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Redirect.Rules[i].From)), ".")
		c.Redirect.Rules[i].To = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Redirect.Rules[i].To)), ".")
	}
	if len(c.Upstream.DNS) == 0 {
		c.Upstream.DNS = d.Upstream.DNS
	}
	if c.DataDir == "" {
		c.DataDir = d.DataDir
	}
	if c.SpeedTest.PingTopN <= 0 {
		c.SpeedTest.PingTopN = d.SpeedTest.PingTopN
	}
	if c.SpeedTest.DownloadMB <= 0 {
		c.SpeedTest.DownloadMB = d.SpeedTest.DownloadMB
	}
	if c.SpeedTest.TimeoutSeconds <= 0 {
		c.SpeedTest.TimeoutSeconds = d.SpeedTest.TimeoutSeconds
	}
	if c.SpeedTest.FreshnessMinutes < 0 {
		c.SpeedTest.FreshnessMinutes = d.SpeedTest.FreshnessMinutes
	}
	if len(c.IPSync.Proxies) == 0 {
		c.IPSync.Proxies = d.IPSync.Proxies
	}
	if c.Platforms == nil {
		c.Platforms = d.Platforms
	}
	// 不变量：隐藏的平台一律不启用、不测速（不依赖前端维持）。
	for k, tg := range c.Platforms {
		if tg.Hidden {
			tg.Enabled = false
			tg.SpeedTest = false
			c.Platforms[k] = tg
		}
	}
}

func save(path string, c *Config) error {
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return persist.WriteFile(path, raw, 0o644)
}

// Get 返回当前配置快照（只读对待）。
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Replace 落盘并原子替换当前配置，随后触发已注册的回调。
func (m *Manager) Replace(c *Config) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	c = Clone(c)
	normalize(c)
	if err := Validate(c); err != nil {
		return err
	}
	if m.validator != nil {
		if err := m.validator(c); err != nil {
			return err
		}
	}
	if err := save(m.path, c); err != nil {
		return err
	}
	m.mu.Lock()
	old := m.cur
	m.cur = c
	cbs := append([]func(*Config) error{}, m.onApply...)
	m.mu.Unlock()
	for _, cb := range cbs {
		if err := cb(c); err != nil {
			m.mu.Lock()
			m.cur = old
			m.mu.Unlock()
			rollbackErr := save(m.path, old)
			for _, restore := range cbs {
				rollbackErr = errors.Join(rollbackErr, restore(old))
			}
			return errors.Join(fmt.Errorf("配置应用失败: %w", err), rollbackErr)
		}
	}
	return nil
}

// OnApply 注册配置变更回调（如 DNS / 代理重建索引）。
func (m *Manager) OnApply(fn func(*Config) error) {
	m.mu.Lock()
	m.onApply = append(m.onApply, fn)
	m.mu.Unlock()
}

func (m *Manager) SetValidator(fn func(*Config) error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.validator = fn
}

// Clone owns all mutable maps/slices, avoiding changes through caller aliases.
func Clone(c *Config) *Config {
	out := *c
	if c.Platforms != nil {
		out.Platforms = make(map[string]PlatformToggle, len(c.Platforms))
	}
	for name, toggle := range c.Platforms {
		out.Platforms[name] = toggle
	}
	out.Upstream.DNS = append([]string(nil), c.Upstream.DNS...)
	out.Redirect.Rules = append([]RedirectRule(nil), c.Redirect.Rules...)
	out.IPSync.Proxies = append([]string(nil), c.IPSync.Proxies...)
	return &out
}

// RestartRequired reports fields whose sockets/files are only loaded at startup.
func RestartRequired(started, desired *Config) bool {
	return started.Listen != desired.Listen || started.WebTLS != desired.WebTLS ||
		started.AdvertiseIP != desired.AdvertiseIP || started.Redirect.TLSAddr != desired.Redirect.TLSAddr ||
		started.DataDir != desired.DataDir
}

func Validate(c *Config) error {
	for name, addr := range map[string]string{"DNS": c.Listen.DNS, "HTTP": c.Listen.HTTP, "Web": c.Listen.Web, "Web TLS": c.WebTLS.Addr, "download TLS": c.Redirect.TLSAddr} {
		if err := validateAddress(addr); err != nil {
			return fmt.Errorf("%s 监听地址无效: %w", name, err)
		}
	}
	if c.WebTLS.Enabled && c.Redirect.Enabled {
		_, webPort, _ := net.SplitHostPort(c.WebTLS.Addr)
		_, downloadPort, _ := net.SplitHostPort(c.Redirect.TLSAddr)
		if webPort == downloadPort && webPort != "0" {
			return fmt.Errorf("管理 HTTPS 与下载 TLS 不能共用端口，请将 web_tls.addr 改为 :8443")
		}
	}
	validIP := func(ip string) bool {
		parsed := net.ParseIP(ip)
		return parsed != nil && parsed.To4() != nil && !parsed.IsUnspecified() && !parsed.IsMulticast()
	}
	if c.AdvertiseIP != "" && !validIP(c.AdvertiseIP) {
		return fmt.Errorf("advertise_ip 必须是有效 IPv4 地址")
	}
	for name, toggle := range c.Platforms {
		if toggle.PinnedIP != "" && !validIP(toggle.PinnedIP) {
			return fmt.Errorf("平台 %s 的 pinned_ip 必须是有效 IPv4 地址", name)
		}
	}
	for _, addr := range c.Upstream.DNS {
		if err := validateAddress(addr); err != nil {
			return fmt.Errorf("上游 DNS 地址无效: %w", err)
		}
	}
	for name, schedule := range map[string]string{"speedtest": c.SpeedTest.Schedule, "ip_sync": c.IPSync.Schedule} {
		if schedule == "" && ((name == "speedtest" && !c.SpeedTest.Enabled) || (name == "ip_sync" && !c.IPSync.Enabled)) {
			continue
		}
		if _, err := cron.ParseStandard(schedule); err != nil {
			return fmt.Errorf("%s 周期无效: %w", name, err)
		}
	}
	if c.SpeedTest.PingTopN > 100 || c.SpeedTest.DownloadMB > 1024 || c.SpeedTest.TimeoutSeconds > 120 || c.SpeedTest.FreshnessMinutes > 1440 {
		return fmt.Errorf("测速参数超出范围（top-N ≤100、下载 ≤1024 MiB、超时 ≤120 秒、新鲜度 ≤1440 分钟）")
	}
	graph := make(map[string]string)
	seen := make(map[string]bool)
	for _, rule := range c.Redirect.Rules {
		if _, ok := dns.IsDomainName(rule.From); !ok || strings.ContainsAny(rule.From, ":/\\") || rule.From == "" {
			return fmt.Errorf("重写源域名无效: %q", rule.From)
		}
		target := rule.To
		if host, _, err := net.SplitHostPort(target); err == nil {
			if err := validateAddress(target); err != nil {
				return err
			}
			target = host
		}
		if _, ok := dns.IsDomainName(target); !ok || strings.ContainsAny(target, ":/\\") || target == "" {
			return fmt.Errorf("重写目标域名无效: %q", rule.To)
		}
		if seen[rule.From] {
			return fmt.Errorf("重写源域名重复: %s", rule.From)
		}
		seen[rule.From] = true
		if rule.Enabled {
			graph[rule.From] = target
		}
	}
	for _, prefix := range c.IPSync.Proxies {
		if prefix == "" {
			continue
		}
		u, err := url.Parse(prefix)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("IP 同步代理前缀无效")
		}
	}
	return ValidateRedirectGraph(graph)
}

func validateAddress(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("端口无效: %q", port)
	}
	return nil
}

func ValidateRedirectGraph(graph map[string]string) error {
	visited := make(map[string]uint8)
	var visit func(string) error
	visit = func(host string) error {
		if visited[host] == 1 {
			return fmt.Errorf("重写规则形成循环: %s", host)
		}
		if visited[host] == 2 {
			return nil
		}
		visited[host] = 1
		if target, ok := graph[host]; ok {
			if h, _, err := net.SplitHostPort(target); err == nil {
				target = h
			}
			if err := visit(target); err != nil {
				return err
			}
		}
		visited[host] = 2
		return nil
	}
	for host := range graph {
		if err := visit(host); err != nil {
			return err
		}
	}
	return nil
}
