package proxy

import (
	"sync"
	"time"
)

// decisionCache 缓存“目标域名是否缺某资源”的探测结论，按 ContentID 目录粒度。
// 同一游戏只探一次，避免每个分块请求都触发探测。
type decisionCache struct {
	mu  sync.Mutex
	ttl time.Duration
	cap int
	m   map[string]decisionEntry
}

type decisionEntry struct {
	missing bool
	at      time.Time
}

func newDecisionCache(ttl time.Duration, capacity int) *decisionCache {
	return &decisionCache{ttl: ttl, cap: capacity, m: make(map[string]decisionEntry)}
}

func (c *decisionCache) get(key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return false, false
	}
	if time.Since(e.at) > c.ttl {
		delete(c.m, key)
		return false, false
	}
	return e.missing, true
}

func (c *decisionCache) set(key string, missing bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.cap {
		// 容量到顶，清掉过期项；仍满则整桶清空（简单稳妥）。
		now := time.Now()
		for k, e := range c.m {
			if now.Sub(e.at) > c.ttl {
				delete(c.m, k)
			}
		}
		if len(c.m) >= c.cap {
			c.m = make(map[string]decisionEntry)
		}
	}
	c.m[key] = decisionEntry{missing: missing, at: time.Now()}
}
