package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"xboxspeedup/internal/persist"
)

// LoadAdminToken keeps the first generated password across container upgrades.
// A deployment may explicitly set XBOX_WEB_TOKEN instead.
func LoadAdminToken(dataDir string) (string, error) {
	if token := os.Getenv("XBOX_WEB_TOKEN"); token != "" {
		return token, nil
	}
	path := filepath.Join(dataDir, "web-token")
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); token != "" {
			return token, nil
		}
		return "", fmt.Errorf("管理密码文件为空: %s", path)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := persist.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			s.handleHealth(w, r)
			return
		}
		if s.d.AdminToken != "" {
			user, password, ok := r.BasicAuth()
			actual, expected := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(s.d.AdminToken))
			if !ok || user != "admin" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Xbox-Speedup", charset="UTF-8"`)
				http.Error(w, "需要管理账号", http.StatusUnauthorized)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
					http.Error(w, "不允许跨来源修改", http.StatusForbidden)
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "不允许跨来源修改", http.StatusForbidden)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.d.Healthy != nil && !s.d.Healthy() {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}
