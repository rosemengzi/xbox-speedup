// Package ipsync 从上游 GitHub 在线同步各平台 IP 列表，并落本地缓存。
//
// 上游就是 skydevil88/XboxDownload 仓库的 IP/ 目录（app 自己也是这么拉的）。
// 国内直连 raw.githubusercontent 经常失败，所以按配置里的代理前缀依次重试，
// 最后兜底 jsdelivr。拉不到就用本地缓存，绝不因网络问题瘫痪。
package ipsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/rules"
)

const rawBase = "https://raw.githubusercontent.com/skydevil88/XboxDownload/refs/heads/master/IP/"
const jsdelivrBase = "https://testingcf.jsdelivr.net/gh/skydevil88/XboxDownload/IP/"

// Syncer 负责把上游 IP 列表同步进 ipstore 并写缓存。
type Syncer struct {
	cfg    *config.Manager
	table  *rules.Table
	store  *ipstore.Store
	logs   *logstore.Store
	ipDir  string
	client *http.Client
}

// New 创建同步器；ipDir 是本地缓存目录（如 /data/ip）。
func New(cfg *config.Manager, table *rules.Table, store *ipstore.Store, logs *logstore.Store, ipDir string) *Syncer {
	return &Syncer{
		cfg:    cfg,
		table:  table,
		store:  store,
		logs:   logs,
		ipDir:  ipDir,
		client: &http.Client{Timeout: 12 * time.Second},
	}
}

// LoadLocal 启动时把本地缓存的 IP 文件读入 ipstore（不联网）。
func (s *Syncer) LoadLocal() {
	for poolName, pool := range s.table.Pools {
		path := filepath.Join(s.ipDir, pool.IPFile)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		entries := parseIPList(string(raw))
		if len(entries) > 0 {
			s.store.Pool(poolName).SetList(entries)
		}
	}
}

// SyncAll 同步全部池，返回每个池新增的 IP（供增量测速）。
func (s *Syncer) SyncAll(ctx context.Context) map[string][]string {
	added := make(map[string][]string)
	for poolName, pool := range s.table.Pools {
		newIPs, err := s.syncPool(ctx, poolName, pool)
		if err != nil {
			s.logs.Log(logstore.KindSync, pool.IPFile, "", "同步失败: "+err.Error())
			continue
		}
		if len(newIPs) > 0 {
			added[poolName] = newIPs
		}
		s.logs.Log(logstore.KindSync, pool.IPFile, "",
			fmt.Sprintf("同步完成，新增 %d 个 IP", len(newIPs)))
	}
	return added
}

func (s *Syncer) syncPool(ctx context.Context, poolName string, pool rules.Pool) ([]string, error) {
	keyword := strings.TrimSuffix(strings.TrimPrefix(pool.IPFile, "IP."), ".txt")
	content, err := s.fetch(ctx, pool.IPFile, keyword)
	if err != nil {
		return nil, err
	}
	if err := s.writeCache(pool.IPFile, content); err != nil {
		s.logs.Log(logstore.KindSync, pool.IPFile, "", "写缓存失败: "+err.Error())
	}
	entries := parseIPList(content)
	if len(entries) == 0 {
		return nil, fmt.Errorf("解析到 0 个 IP")
	}
	return s.store.Pool(poolName).SetList(entries), nil
}

// fetch 按代理前缀依次尝试，最后兜底 jsdelivr；校验首行关键字。
func (s *Syncer) fetch(ctx context.Context, file, keyword string) (string, error) {
	var candidates []string
	for _, p := range s.cfg.Get().IPSync.Proxies {
		candidates = append(candidates, p+rawBase+file)
	}
	candidates = append(candidates, jsdelivrBase+file)

	var lastErr error
	for _, url := range candidates {
		body, err := s.get(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(body), keyword) {
			lastErr = fmt.Errorf("内容校验失败(首行非 %q)", keyword)
			continue
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无可用下载源")
	}
	return "", lastErr
}

func (s *Syncer) get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (s *Syncer) writeCache(file, content string) error {
	if err := os.MkdirAll(s.ipDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.ipDir, file), []byte(content), 0o644)
}

// parseIPList 解析 IP 列表文本：跳过首行关键字，逐行取 "IP\t(位置)"。
func parseIPList(content string) []ipstore.IPEntry {
	lines := strings.Split(content, "\n")
	var out []ipstore.IPEntry
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexAny(line, " \t")
		ip := line
		loc := ""
		if idx >= 0 {
			ip = strings.TrimSpace(line[:idx])
			loc = strings.Trim(strings.TrimSpace(line[idx:]), "()")
		}
		if ip == "" {
			continue
		}
		out = append(out, ipstore.IPEntry{IP: ip, Location: loc})
	}
	return out
}
