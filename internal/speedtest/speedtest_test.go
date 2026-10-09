package speedtest

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadRejectsRedirectsInvalidRangesAndShortBodies(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		rangeHeader string
		size        int
	}{
		{"redirect", http.StatusFound, "", 65536},
		{"invalid partial", http.StatusPartialContent, "", 65536},
		{"short error page", http.StatusOK, "", 100},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", test.rangeHeader)
				w.WriteHeader(test.status)
				w.Write([]byte(strings.Repeat("x", test.size)))
			}))
			defer s.Close()
			ip, _, _ := net.SplitHostPort(s.Listener.Addr().String())
			if got := (&Engine{}).downloadSpeed(context.Background(), ip, s.URL, 65536, time.Second); got != 0 {
				t.Fatalf("invalid response counted as %.3f MiB/s", got)
			}
		})
	}
}

func TestIgnoredRangeStillHasHardReadLimit(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-65535" {
			t.Error("incorrect requested sample")
		}
		w.Write([]byte(strings.Repeat("x", 65536)))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	ip, _, _ := net.SplitHostPort(s.Listener.Addr().String())
	done := make(chan float64, 1)
	go func() { done <- (&Engine{}).downloadSpeed(context.Background(), ip, s.URL, 65536, 3*time.Second) }()
	select {
	case speed := <-done:
		if speed <= 0 {
			t.Fatal("valid sample rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("sample read past configured byte limit")
	}
}

func TestValidPartialDownloadIsMeasured(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-65535/1048576")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(strings.Repeat("x", 65536)))
	}))
	defer s.Close()
	ip, _, _ := net.SplitHostPort(s.Listener.Addr().String())
	if speed := (&Engine{}).downloadSpeed(context.Background(), ip, s.URL, 65536, time.Second); speed <= 0 {
		t.Fatal("valid 206 sample rejected")
	}
}
