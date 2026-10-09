// Package rules 加载并索引静态域名表（data/platforms.yaml）。
//
// 它只承载“几乎不变”的静态数据：平台 -> 域名 / IP 池 / 黑名单 / 内置重写。
// 是否启用某平台由 config 控制，本包不掺和运行期开关，保持单一职责。
package rules

import (
	"fmt"
	"os"
	"strings"

	"xboxspeedup/internal/config"

	"gopkg.in/yaml.v3"
)

// RedirectPair 表示一条内置 302 改写：from 域名跳到 to 域名。
type RedirectPair struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// Pool 是一个测速 IP 池：对应一个上游 IP 列表文件 + 一个测速 URL。
type Pool struct {
	IPFile  string `yaml:"ip_file"`
	TestURL string `yaml:"test_url"`
}

// Platform 是一个加速平台的域名定义。
type Platform struct {
	Description string         `yaml:"description"`
	Pool        string         `yaml:"pool"`
	Hosts       []string       `yaml:"hosts"`
	Redirects   []RedirectPair `yaml:"redirects"`
	Blacklist   []string       `yaml:"blacklist"`
}

// HostInfo 是某个加速域名的归属信息。
type HostInfo struct {
	Platform string
	Pool     string
}

// Table 是整张域名表，外加若干预计算索引以便 O(1) 查询。
type Table struct {
	Pools     map[string]Pool     `yaml:"pools"`
	Platforms map[string]Platform `yaml:"platforms"`

	// 以下为 Load 时预计算，不参与 YAML。
	hostToInfo   map[string]HostInfo // 加速域名 -> 平台/池
	blacklist    map[string]string   // 屏蔽域名 -> 平台
	builtinRedir []RedirectPair      // 所有平台的内置重写
}

// Load 读取 platforms.yaml 并构建索引。
func Load(path string) (*Table, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取域名表失败: %w", err)
	}
	var t Table
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("解析域名表失败: %w", err)
	}
	t.buildIndex()
	return &t, nil
}

func (t *Table) buildIndex() {
	t.hostToInfo = make(map[string]HostInfo)
	t.blacklist = make(map[string]string)
	t.builtinRedir = nil

	for name, p := range t.Platforms {
		for _, h := range p.Hosts {
			t.hostToInfo[strings.ToLower(strings.TrimSpace(h))] = HostInfo{Platform: name, Pool: p.Pool}
		}
		for _, b := range p.Blacklist {
			t.blacklist[strings.ToLower(strings.TrimSpace(b))] = name
		}
		for _, r := range p.Redirects {
			// 内置重写源也属于该平台，供开关、TLS 与回源查找使用。
			t.hostToInfo[strings.ToLower(strings.TrimSpace(r.From))] = HostInfo{Platform: name, Pool: p.Pool}
			t.builtinRedir = append(t.builtinRedir, RedirectPair{
				From: strings.ToLower(strings.TrimSpace(r.From)),
				To:   strings.ToLower(strings.TrimSpace(r.To)),
			})
		}
	}
}

// EffectiveRedirects is shared by DNS and HTTP so disabled platforms cannot be
// re-enabled through global rules. User rules override built-in rules.
func (t *Table) EffectiveRedirects(c *config.Config) map[string]string {
	out := make(map[string]string)
	if !c.Redirect.Enabled {
		return out
	}
	allowed := func(host string) bool {
		platform := t.PlatformOf(host)
		if platform == "" {
			return true
		}
		toggle := c.Platforms[platform]
		return toggle.Enabled && !toggle.Hidden
	}
	for name, platform := range t.Platforms {
		toggle := c.Platforms[name]
		if !toggle.Enabled || toggle.Hidden {
			continue
		}
		for _, r := range platform.Redirects {
			from, to := strings.ToLower(strings.TrimSpace(r.From)), strings.ToLower(strings.TrimSpace(r.To))
			if allowed(from) && allowed(to) {
				out[from] = to
			}
		}
	}
	for _, r := range c.Redirect.Rules {
		if !r.Enabled {
			delete(out, r.From)
		} else if allowed(r.From) && allowed(r.To) {
			out[r.From] = r.To
		}
	}
	return out
}

// ValidateConfig catches cycles including built-in redirects before applying.
func (t *Table) ValidateConfig(c *config.Config) error {
	return config.ValidateRedirectGraph(t.EffectiveRedirects(c))
}

// HostInfo 返回某加速域名的平台/池归属。
func (t *Table) HostInfo(host string) (HostInfo, bool) {
	info, ok := t.hostToInfo[strings.ToLower(host)]
	if !ok && t.hostToInfo == nil {
		for name, p := range t.Platforms {
			for _, h := range p.Hosts {
				if strings.EqualFold(h, host) {
					return HostInfo{Platform: name, Pool: p.Pool}, true
				}
			}
			for _, r := range p.Redirects {
				if strings.EqualFold(r.From, host) {
					return HostInfo{Platform: name, Pool: p.Pool}, true
				}
			}
		}
	}
	return info, ok
}

// PoolOfHost 返回某加速域名所属的 IP 池名（找不到返回空）。
func (t *Table) PoolOfHost(host string) string {
	if info, ok := t.hostToInfo[strings.ToLower(host)]; ok {
		return info.Pool
	}
	return ""
}

// Blacklisted 判断某域名是否在黑名单中，返回所属平台。
func (t *Table) Blacklisted(host string) (string, bool) {
	p, ok := t.blacklist[strings.ToLower(host)]
	return p, ok
}

// BuiltinRedirects 返回所有平台内置的 302 改写对。
func (t *Table) BuiltinRedirects() []RedirectPair {
	return t.builtinRedir
}

// PlatformOf 返回域名所属平台名（加速域名或黑名单域名）。
func (t *Table) PlatformOf(host string) string {
	host = strings.ToLower(host)
	if info, ok := t.HostInfo(host); ok {
		return info.Platform
	}
	if p, ok := t.blacklist[host]; ok {
		return p
	}
	return ""
}
