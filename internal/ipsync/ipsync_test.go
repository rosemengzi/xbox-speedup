package ipsync

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xboxspeedup/internal/config"
	"xboxspeedup/internal/ipstore"
	"xboxspeedup/internal/logstore"
	"xboxspeedup/internal/rules"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestParserRejectsInvalidIPv4AndDeduplicates(t *testing.T) {
	entries := parseIPList("Akamai\nnot-an-ip\n::1\n0.0.0.0\n192.0.2.1\t(first)\n192.0.2.1\t(duplicate)\n")
	if len(entries) != 1 || entries[0].IP != "192.0.2.1" || entries[0].Location != "first" {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

func TestInvalidRemoteListCannotOverwriteGoodCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "IP.Akamai.txt")
	good := "Akamai\n192.0.2.1\n"
	if err := os.WriteFile(path, []byte(good), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := config.NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	store := ipstore.New()
	store.Pool("Akamai").SetList([]ipstore.IPEntry{{IP: "192.0.2.1"}})
	s := New(m, &rules.Table{}, store, logstore.New(20), dir)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("Akamai\ninvalid\n")), Header: make(http.Header)}, nil
	})
	if _, err := s.syncPool(context.Background(), "Akamai", rules.Pool{IPFile: "IP.Akamai.txt"}); err == nil {
		t.Fatal("invalid list accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != good {
		t.Fatal("good cache was overwritten")
	}
	if ips := store.Pool("Akamai").IPs(); len(ips) != 1 || ips[0] != "192.0.2.1" {
		t.Fatal("live pool was changed")
	}
}
