// Package speedtest 实现 IP 测速：沿用原项目思路——
// 先 ping 全表挑出延迟最低的 top-N，再只对这 top-N 做下载测速比带宽，选最快。
//
// 关键点：测速结果会过期，所以下载阶段带新鲜度缓存（窗口内不重测）；
// ICMP 需要 NET_RAW，不可用时自动退化为 TCP 连接延迟。
package speedtest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/netutil"
)

// Engine 是测速引擎。
type Engine struct {
	cfg   *config.Manager
	store *ipstore.Store
	logs  *logstore.Store

	poolTestURL func(pool string) string // 注入：池 -> 测速 URL

	mu      sync.Mutex
	running bool

	icmpOnce sync.Once
	icmpOK   bool
}

// New 创建引擎。poolTestURL 用于查池的测速 URL，speedtestPools 决定哪些池要测。
func New(cfg *config.Manager, store *ipstore.Store, logs *logstore.Store, poolTestURL func(string) string) *Engine {
	return &Engine{cfg: cfg, store: store, logs: logs, poolTestURL: poolTestURL}
}

// RunPools 对给定池做完整重排（ping 全表 -> top-N 下载）。
func (e *Engine) RunPools(ctx context.Context, pools []string) {
	if !e.acquire() {
		e.logs.Log(logstore.KindSpeedTest, "", "", "已有测速在进行，跳过本次")
		return
	}
	defer e.release()
	for _, pool := range pools {
		select {
		case <-ctx.Done():
			return
		default:
		}
		e.testPool(ctx, pool, e.store.Pool(pool).IPs(), true)
	}
}

// RunIncremental 只测各池新增的 IP，然后重算最快。
func (e *Engine) RunIncremental(ctx context.Context, added map[string][]string) {
	if len(added) == 0 {
		return
	}
	if !e.acquire() {
		return
	}
	defer e.release()
	for pool, ips := range added {
		select {
		case <-ctx.Done():
			return
		default:
		}
		e.testPool(ctx, pool, ips, false)
	}
}

// Running 返回当前是否有测速在进行。
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) acquire() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return false
	}
	e.running = true
	return true
}

func (e *Engine) release() {
	e.mu.Lock()
	e.running = false
	e.mu.Unlock()
}

// testPool 对一个池的候选 IP 执行 ping + 下载测速。
func (e *Engine) testPool(ctx context.Context, poolName string, candidates []string, fullRun bool) {
	if len(candidates) == 0 {
		return
	}
	cfg := e.cfg.Get().SpeedTest
	pool := e.store.Pool(poolName)
	testURL := e.poolTestURL(poolName)

	// 阶段一：ping 全部候选，挑延迟最低的 top-N。
	type pinged struct {
		ip  string
		rtt float64
	}
	results := make([]pinged, 0, len(candidates))
	var mu sync.Mutex
	sem := make(chan struct{}, 50)
	var wg sync.WaitGroup
	for _, ip := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			rtt, ok := e.latency(ip, time.Second)
			val := -1.0
			if ok {
				val = rtt
			}
			pool.UpdatePing(ip, val)
			if ok {
				mu.Lock()
				results = append(results, pinged{ip, rtt})
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].rtt < results[j].rtt })
	topN := cfg.PingTopN
	if topN <= 0 {
		topN = 10
	}
	if len(results) > topN {
		results = results[:topN]
	}

	// 测速 URL 为空：只按 ping 排序，最快=延迟最低。
	if strings.TrimSpace(testURL) == "" {
		best := pool.ComputeBest()
		e.logs.Log(logstore.KindSpeedTest, poolName, "",
			fmt.Sprintf("仅 ping 排序，候选 %d，最快 %s", len(candidates), best))
		return
	}

	// 阶段二：对 top-N 做下载测速（新鲜的可跳过）。
	freshness := time.Duration(cfg.FreshnessMinutes) * time.Minute
	dlBytes := int64(cfg.DownloadMB) * 1024 * 1024
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second

	var dwg sync.WaitGroup
	dsem := make(chan struct{}, 8)
	for _, r := range results {
		if fullRun && freshness > 0 && pool.Fresh(r.ip, freshness) {
			continue
		}
		dwg.Add(1)
		dsem <- struct{}{}
		go func(ip string) {
			defer dwg.Done()
			defer func() { <-dsem }()
			speed := e.downloadSpeed(ctx, ip, testURL, dlBytes, timeout)
			pool.UpdateSpeed(ip, speed, cfg.EWMA)
		}(r.ip)
	}
	dwg.Wait()

	best := pool.ComputeBest()
	loc := pool.Location(best)
	e.logs.Log(logstore.KindSpeedTest, poolName, "",
		fmt.Sprintf("候选 %d，测速 %d，最快 %s (%s)", len(candidates), len(results), best, loc))
}

// latency 返回到 ip 的延迟毫秒；优先 ICMP，不可用时退化 TCP 连接耗时。
func (e *Engine) latency(ip string, timeout time.Duration) (float64, bool) {
	e.icmpOnce.Do(func() { e.icmpOK = icmpProbe("127.0.0.1") })
	if e.icmpOK {
		if rtt, ok := icmpPing(ip, timeout); ok {
			return rtt, true
		}
	}
	return tcpPing(ip, timeout)
}

func icmpProbe(ip string) bool {
	p, err := probing.NewPinger(ip)
	if err != nil {
		return false
	}
	p.Count = 1
	p.Timeout = 500 * time.Millisecond
	p.SetPrivileged(true)
	if err := p.Run(); err != nil {
		return false
	}
	return true
}

func icmpPing(ip string, timeout time.Duration) (float64, bool) {
	p, err := probing.NewPinger(ip)
	if err != nil {
		return 0, false
	}
	p.Count = 1
	p.Timeout = timeout
	p.SetPrivileged(true)
	if err := p.Run(); err != nil {
		return 0, false
	}
	st := p.Statistics()
	if st.PacketsRecv == 0 {
		return 0, false
	}
	return float64(st.AvgRtt.Microseconds()) / 1000.0, true
}

func tcpPing(ip string, timeout time.Duration) (float64, bool) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "80"), timeout)
	if err != nil {
		return 0, false
	}
	_ = conn.Close()
	return float64(time.Since(start).Microseconds()) / 1000.0, true
}

// downloadSpeed 连到 ip、按测速 URL 的 Host 取一段数据，返回 MiB/s。
func (e *Engine) downloadSpeed(ctx context.Context, ip, testURL string, dlBytes int64, timeout time.Duration) float64 {
	u, err := url.Parse(testURL)
	if err != nil {
		return 0
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, testURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", dlBytes-1))
	ua := "XboxDownload"
	if strings.HasSuffix(strings.ToLower(u.Hostname()), ".nintendo.net") {
		ua = "XboxDownload/Nintendo NX"
	}
	req.Header.Set("User-Agent", ua)

	client := netutil.ForcedIPClient(ip, timeout)
	defer client.CloseIdleConnections()

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0
	}

	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, rerr := resp.Body.Read(buf)
		total += int64(n)
		if rerr != nil {
			break
		}
		if cctx.Err() != nil {
			break
		}
	}
	elapsed := time.Since(start).Seconds()
	if elapsed < 0.1 || total == 0 {
		return 0
	}
	return float64(total) / (1024.0 * 1024.0 * elapsed)
}
