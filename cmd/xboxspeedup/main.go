// 命令 xboxspeedup 是跑在 Docker 里的游戏主机下载加速器：
// 自建 DNS + 可选 HTTP 换源/HTTPS 透传 + 周期测速选优 + Web 管理界面。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/dnssrv"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/ipsync"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/netutil"
	"xboxspeedup/internal/proxy"
	"xboxspeedup/internal/rules"
	"xboxspeedup/internal/speedtest"
	"xboxspeedup/internal/web"
)

func main() {
	dataDir := env("XBOX_DATA_DIR", "/data")
	seedDir := env("XBOX_SEED_DIR", "/app/data")
	ipDir := filepath.Join(dataDir, "ip")
	configPath := filepath.Join(dataDir, "config.yaml")
	platformsPath := filepath.Join(dataDir, "platforms.yaml")

	if err := seed(dataDir, seedDir); err != nil {
		log.Printf("[seed] %v", err)
	}

	cfgMgr, err := config.NewManager(configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	table, err := rules.Load(platformsPath)
	if err != nil {
		log.Fatalf("加载域名表失败: %v", err)
	}
	if err := table.ValidateConfig(cfgMgr.Get()); err != nil {
		log.Fatal(err)
	}
	cfgMgr.SetValidator(table.ValidateConfig)
	started := config.Clone(cfgMgr.Get())

	logs := logstore.New(3000)
	store := ipstore.New()

	advertiseIP := cfgMgr.Get().AdvertiseIP
	if advertiseIP == "" {
		advertiseIP = netutil.OutboundIP()
	}
	logs.Log(logstore.KindSystem, "", "", "启动，本机 IP "+advertiseIP)
	log.Printf("本机对外 IP: %s", advertiseIP)

	// IP 同步器：先读本地缓存，让加速器离线也能用。
	syncer := ipsync.New(cfgMgr, table, store, logs, ipDir)
	syncer.LoadLocal()

	// 测速引擎：池 -> 测速 URL 由域名表提供。
	engine := speedtest.New(cfgMgr, store, logs, func(pool string) string {
		return table.Pools[pool].TestURL
	})

	prx := proxy.New(cfgMgr, store, logs, table.PoolOfHost)
	prx.SetResolvers(table.EffectiveRedirects, func(host string) string {
		c := cfgMgr.Get()
		if platform := table.PlatformOf(host); platform != "" {
			toggle := c.Platforms[platform]
			if !toggle.Enabled || toggle.Hidden {
				return ""
			}
			if toggle.PinnedIP != "" {
				return toggle.PinnedIP
			}
		}
		if pool := table.PoolOfHost(host); pool != "" {
			return store.BestForPool(pool)
		}
		return ""
	}, advertiseIP)
	// DNS only redirects while both the HTTP and TLS listeners are healthy.
	dnsSrv := dnssrv.New(cfgMgr, table, store, logs, advertiseIP, prx.Running)
	if err := dnsSrv.Start(); err != nil {
		log.Fatalf("DNS 启动失败: %v", err)
	}
	log.Printf("DNS 监听 %s", cfgMgr.Get().Listen.DNS)

	// HTTP 换源与 HTTPS 透传（可选）。
	if cfgMgr.Get().Redirect.Enabled {
		if err := prx.Start(started.Listen.HTTP, started.Redirect.TLSAddr); err != nil {
			log.Fatalf("下载 HTTP/TLS 启动失败: %v", err)
		} else {
			log.Printf("下载监听 HTTP %s，TLS %s", started.Listen.HTTP, started.Redirect.TLSAddr)
		}
	}

	// 触发器（供 Web 与调度器复用）。
	ctx, cancel := context.WithCancel(context.Background())
	triggerSpeedTest := func(pool string) {
		var pools []string
		if pool != "" {
			pools = []string{pool}
		} else {
			pools = speedtestPools(cfgMgr.Get(), table)
		}
		engine.RunPools(ctx, pools)
	}
	triggerSync := func() {
		added := syncer.SyncAll(ctx)
		engine.RunIncremental(ctx, eligibleIncremental(cfgMgr.Get(), table, added))
	}

	sched := &schedHolder{}
	speedTestJob := func() { triggerSpeedTest("") }

	// 配置热应用：重建索引、按开关启停下载监听、重建调度周期。
	cfgMgr.OnApply(func(c *config.Config) error {
		prx.Reload(c)
		if c.Redirect.Enabled && !prx.Running() {
			if err := prx.Start(started.Listen.HTTP, started.Redirect.TLSAddr); err != nil {
				return fmt.Errorf("下载监听启动失败: %w", err)
			}
		} else if !c.Redirect.Enabled && prx.Running() {
			prx.Stop()
		}
		dnsSrv.Reload(c)
		return sched.rebuild(c, speedTestJob, triggerSync)
	})

	// Web 管理界面。
	adminToken, err := web.LoadAdminToken(dataDir)
	if err != nil {
		log.Fatalf("加载管理密码失败: %v", err)
	}
	log.Printf("管理用户 admin；密码使用 XBOX_WEB_TOKEN，未配置时保存在 %s", filepath.Join(dataDir, "web-token"))
	webSrv := web.New(web.Deps{
		Cfg: cfgMgr, Table: table, Store: store, Logs: logs, Proxy: prx,
		AdvertiseIP: advertiseIP, TriggerSpeedTest: triggerSpeedTest, TriggerSync: triggerSync,
		SpeedTestRunning: engine.Running,
		AdminToken:       adminToken, StartedConfig: started,
		Healthy: func() bool { return dnsSrv.Running() && (!cfgMgr.Get().Redirect.Enabled || prx.Running()) },
	})
	if err := webSrv.Start(cfgMgr.Get().Listen.Web); err != nil {
		log.Fatalf("Web 启动失败: %v", err)
	}
	log.Printf("Web 界面 http://%s", displayAddr(advertiseIP, cfgMgr.Get().Listen.Web))
	if cfgMgr.Get().WebTLS.Enabled {
		tlsCfg := cfgMgr.Get().WebTLS
		if err := webSrv.StartTLS(tlsCfg.Addr, tlsCfg.CertFile, tlsCfg.KeyFile); err != nil {
			log.Fatalf("Web HTTPS 启动失败: %v", err)
		}
		log.Printf("Web HTTPS https://%s", displayAddr(advertiseIP, tlsCfg.Addr))
	}

	// 调度器：测速与 IP 同步按 cron 周期跑（随配置热重建）。
	if err := sched.rebuild(cfgMgr.Get(), speedTestJob, triggerSync); err != nil {
		log.Fatal(err)
	}

	// 启动后台首测，让最快 IP 尽快填充。
	if cfgMgr.Get().SpeedTest.Enabled {
		go triggerSpeedTest("")
	}

	waitForSignal()
	log.Printf("正在退出…")
	cancel()
	sched.stop()
	webSrv.Shutdown()
	prx.Stop()
	dnsSrv.Shutdown()
}

// speedtestPools 返回启用且勾选测速的平台所对应的池（去重）。
func speedtestPools(c *config.Config, table *rules.Table) []string {
	set := make(map[string]struct{})
	for name, p := range table.Platforms {
		if tg, ok := c.Platforms[name]; ok && tg.Enabled && !tg.Hidden && tg.SpeedTest && tg.PinnedIP == "" {
			set[p.Pool] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func eligibleIncremental(c *config.Config, table *rules.Table, added map[string][]string) map[string][]string {
	out := make(map[string][]string)
	if !c.SpeedTest.Enabled {
		return out
	}
	for _, pool := range speedtestPools(c, table) {
		if ips := added[pool]; len(ips) > 0 {
			out[pool] = ips
		}
	}
	return out
}

func waitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}

// displayAddr 把监听地址拼成可读 URL：":8080" -> "<ip>:8080"，完整地址原样返回。
func displayAddr(ip, listen string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return ip + listen
	}
	return listen
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
