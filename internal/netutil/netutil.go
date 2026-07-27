// Package netutil 提供出口 IP 探测与“强制连指定 IP”的 HTTP 工具。
//
// 测速和 302 探测都需要：连到某个具体 CDN IP，但请求里带的是域名 Host。
// 这靠自定义 Transport 的 DialContext 把目标地址改写成指定 IP 实现。
package netutil

import (
	"context"
	"net"
	"net/http"
	"time"
)

// OutboundIP 通过向上游拨一个 UDP “连接”探测本机出口 IP。
// 不会真的发包，只用于让内核选出本地地址。macvlan 下即容器自身 LAN IP。
func OutboundIP() string {
	conn, err := net.Dial("udp", "223.5.5.5:53")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
}

// ForcedIPTransport 返回一个把所有连接都拨向 ip（保留原端口）的 Transport。
// 这样 http.Get("http://域名/路径") 实际连的是 ip，但 Host 头仍是域名。
func ForcedIPTransport(ip string, dialTimeout time.Duration) *http.Transport {
	dialer := &net.Dialer{Timeout: dialTimeout}
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				port = "80"
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableKeepAlives:     true,
	}
}

// ForcedIPClient 返回一个强制连 ip 的 HTTP 客户端，不自动跟随重定向。
func ForcedIPClient(ip string, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: ForcedIPTransport(ip, timeout),
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
