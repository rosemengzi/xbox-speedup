// Package logstore 是一个带订阅能力的环形日志缓冲。
//
// 连接日志写入是高频路径，Add 必须快且不阻塞；
// 订阅者（Web 的 SSE 推送）消费慢时直接丢弃，不拖累写入方。
package logstore

import (
	"sync"
	"time"
)

// 日志类别常量。
const (
	KindDNSA      = "DNS-A"
	KindDNSAAAA   = "DNS-AAAA"
	KindForward   = "FORWARD"
	KindBlock     = "BLOCK"
	KindRedirect  = "302"
	KindProxy     = "PROXY"
	KindSpeedTest = "SPEEDTEST"
	KindSync      = "SYNC"
	KindSystem    = "SYSTEM"
)

// Entry 是一条日志。
type Entry struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Host   string    `json:"host"`
	Client string    `json:"client"`
	Detail string    `json:"detail"`
}

// Store 是环形缓冲 + 订阅分发。
type Store struct {
	mu   sync.Mutex
	buf  []Entry
	cap  int
	subs map[chan Entry]struct{}
}

// New 创建容量为 capacity 的日志存储。
func New(capacity int) *Store {
	if capacity <= 0 {
		capacity = 2000
	}
	return &Store{
		buf:  make([]Entry, 0, capacity),
		cap:  capacity,
		subs: make(map[chan Entry]struct{}),
	}
}

// Add 写入一条日志并广播给订阅者（非阻塞）。
func (s *Store) Add(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	s.mu.Lock()
	if len(s.buf) >= s.cap {
		copy(s.buf, s.buf[1:])
		s.buf = s.buf[:s.cap-1]
	}
	s.buf = append(s.buf, e)
	for ch := range s.subs {
		select {
		case ch <- e:
		default: // 订阅者太慢，丢弃这条，绝不阻塞写入
		}
	}
	s.mu.Unlock()
}

// Log 是 Add 的便捷封装。
func (s *Store) Log(kind, host, client, detail string) {
	s.Add(Entry{Kind: kind, Host: host, Client: client, Detail: detail})
}

// Snapshot 返回当前缓冲的拷贝（最旧在前）。
func (s *Store) Snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.buf))
	copy(out, s.buf)
	return out
}

// Subscribe 返回一个事件通道和取消函数。
func (s *Store) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
	return ch, cancel
}
