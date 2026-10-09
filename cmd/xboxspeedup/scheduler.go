package main

import (
	"sync"

	"github.com/robfig/cron/v3"

	"xboxspeedup/internal/config"
)

// schedHolder 持有 cron 调度器，支持随配置变更整体重建，
// 这样在 Web 界面改测速/同步周期能即时生效，无需重启容器。
type schedHolder struct {
	mu   sync.Mutex
	cron *cron.Cron
}

// rebuild 按当前配置重建调度任务（先停旧的再起新的）。
func (h *schedHolder) rebuild(c *config.Config, speedTest, syncIP func()) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cr := cron.New()
	if c.SpeedTest.Enabled && c.SpeedTest.Schedule != "" {
		if _, err := cr.AddFunc(c.SpeedTest.Schedule, speedTest); err != nil {
			return err
		}
	}
	if c.IPSync.Enabled && c.IPSync.Schedule != "" {
		if _, err := cr.AddFunc(c.IPSync.Schedule, syncIP); err != nil {
			return err
		}
	}
	if h.cron != nil {
		h.cron.Stop()
	}
	cr.Start()
	h.cron = cr
	return nil
}

// stop 停止调度器。
func (h *schedHolder) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cron != nil {
		h.cron.Stop()
	}
}
