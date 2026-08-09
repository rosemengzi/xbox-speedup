// Package ipstore 持有各 IP 池的候选 IP 与测速记录，并维护“当前最快 IP”。
//
// 测速结果会过期，所以记录都带时间戳；选最快时比较当前候选的
// 最近一次下载速度，避免历史平滑分数继续占用当前最快位置。
package ipstore

import (
	"sort"
	"sync"
	"time"
)

// Record 是单个 IP 的测速记录。
type Record struct {
	IP        string    `json:"ip"`
	Location  string    `json:"location"`
	RTTms     float64   `json:"rtt_ms"`     // 最近一次 ping 往返毫秒；<0 表示失败/未测
	SpeedMBps float64   `json:"speed_mbps"` // 最近一次下载速度 MiB/s；<0 表示未测，0 表示测了但失败
	TestedAt  time.Time `json:"tested_at"`
}

// IPEntry 是一条从 IP 列表解析出的候选项。
type IPEntry struct {
	IP       string
	Location string
}

// Pool 是单个池的 IP 集合与最快结果。
type Pool struct {
	mu      sync.RWMutex
	name    string
	records map[string]*Record
	order   []string // 维持列表内顺序，供未测速时的稳定 fallback
	best    string
}

func newPool(name string) *Pool {
	return &Pool{name: name, records: make(map[string]*Record)}
}

// SetList 用新列表替换池内 IP，保留仍存在 IP 的历史记录，返回新增的 IP。
func (p *Pool) SetList(entries []IPEntry) []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	next := make(map[string]*Record, len(entries))
	order := make([]string, 0, len(entries))
	var added []string
	for _, e := range entries {
		order = append(order, e.IP)
		if old, ok := p.records[e.IP]; ok {
			old.Location = e.Location
			next[e.IP] = old
		} else {
			next[e.IP] = &Record{IP: e.IP, Location: e.Location, RTTms: -1, SpeedMBps: -1}
			added = append(added, e.IP)
		}
	}
	p.records = next
	p.order = order
	if _, ok := next[p.best]; !ok {
		p.best = ""
	}
	return added
}

// IPs 返回池内全部候选 IP。
func (p *Pool) IPs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.order))
	copy(out, p.order)
	return out
}

// Records 返回记录快照（按最近一次下载速度降序、其次 RTT 升序）。
func (p *Pool) Records() []Record {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Record, 0, len(p.order))
	for _, ip := range p.order {
		if r, ok := p.records[ip]; ok {
			out = append(out, *r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SpeedMBps != out[j].SpeedMBps {
			return out[i].SpeedMBps > out[j].SpeedMBps
		}
		ri, rj := out[i].RTTms, out[j].RTTms
		if ri < 0 {
			ri = 1e9
		}
		if rj < 0 {
			rj = 1e9
		}
		return ri < rj
	})
	return out
}

// Location 返回某 IP 的地理位置（无则空）。
func (p *Pool) Location(ip string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if r, ok := p.records[ip]; ok {
		return r.Location
	}
	return ""
}

// Fresh 判断某 IP 的下载测速记录是否在 window 内仍新鲜。
func (p *Pool) Fresh(ip string, window time.Duration) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.records[ip]
	if !ok || r.SpeedMBps < 0 || r.TestedAt.IsZero() {
		return false
	}
	return time.Since(r.TestedAt) < window
}

// UpdatePing 写入一次 ping 结果（rtt<0 表示失败）。
func (p *Pool) UpdatePing(ip string, rtt float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.records[ip]; ok {
		r.RTTms = rtt
	}
}

// UpdateSpeed 写入最近一次下载测速结果。
func (p *Pool) UpdateSpeed(ip string, speed float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.records[ip]
	if !ok {
		return
	}
	r.SpeedMBps = speed
	r.TestedAt = time.Now()
}

// RetainSpeeds 只保留本轮候选 IP 的下载成绩。
// 全量测速的延迟排名变化后，未进入 top-N 的旧成绩不得继续参与选择或显示。
func (p *Pool) RetainSpeeds(ips []string) {
	keep := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		keep[ip] = struct{}{}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for ip, r := range p.records {
		if _, ok := keep[ip]; ok {
			continue
		}
		r.SpeedMBps = -1
		r.TestedAt = time.Time{}
	}
}

// ComputeBest 依据记录重新选出最快 IP：优先选择最近一次下载速度最高者，
// 没有有效下载成绩时退化到 RTT 最低者。
func (p *Pool) ComputeBest() string {
	recs := p.Records()
	if len(recs) == 0 {
		return ""
	}
	best := ""
	// Records 已按下载速度排序，取第一个成功的下载成绩。
	for _, r := range recs {
		if r.SpeedMBps > 0 {
			best = r.IP
			break
		}
	}
	// 没有成功的下载结果时，按 RTT 兜底。
	if best == "" {
		bestRTT := float64(1 << 62)
		for _, r := range recs {
			if r.RTTms >= 0 && r.RTTms < bestRTT {
				best = r.IP
				bestRTT = r.RTTms
			}
		}
	}

	p.mu.Lock()
	p.best = best
	p.mu.Unlock()
	return best
}

// SetBest 手动锁定最快 IP。
func (p *Pool) SetBest(ip string) {
	p.mu.Lock()
	p.best = ip
	p.mu.Unlock()
}

// Best 返回当前最快 IP，未选出时退化到列表首个 IP。
func (p *Pool) Best() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.best != "" {
		return p.best
	}
	if len(p.order) > 0 {
		return p.order[0]
	}
	return ""
}

// Store 管理全部池。
type Store struct {
	mu    sync.RWMutex
	pools map[string]*Pool
}

// New 创建空的池集合。
func New() *Store {
	return &Store{pools: make(map[string]*Pool)}
}

// Pool 返回指定池，不存在则创建。
func (s *Store) Pool(name string) *Pool {
	s.mu.RLock()
	p, ok := s.pools[name]
	s.mu.RUnlock()
	if ok {
		return p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok = s.pools[name]; ok {
		return p
	}
	p = newPool(name)
	s.pools[name] = p
	return p
}

// Names 返回全部池名。
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.pools))
	for n := range s.pools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// BestForPool 是 Pool(name).Best() 的便捷封装。
func (s *Store) BestForPool(name string) string {
	return s.Pool(name).Best()
}
