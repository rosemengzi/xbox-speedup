package netutil

import (
	"context"
	"fmt"
	"time"

	"github.com/miekg/dns"
)

// ExchangeDNS retries truncated replies over TCP and tries another upstream on
// transport errors or SERVFAIL. The context bounds the entire operation.
func ExchangeDNS(ctx context.Context, msg *dns.Msg, upstreams []string) (*dns.Msg, error) {
	var lastErr error
	for _, upstream := range upstreams {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		client := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
		resp, _, err := client.ExchangeContext(ctx, msg, upstream)
		if err == nil && resp != nil && resp.Truncated {
			client.Net = "tcp"
			resp, _, err = client.ExchangeContext(ctx, msg, upstream)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if resp == nil || resp.Truncated || resp.Rcode == dns.RcodeServerFailure || resp.Rcode == dns.RcodeRefused {
			lastErr = fmt.Errorf("upstream %s returned an unusable DNS reply", upstream)
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no DNS upstream available")
	}
	return nil, lastErr
}

// ResolveIPv4 deliberately queries independent upstreams, avoiding our own DNS
// redirection back into the HTTP/TLS proxy.
func ResolveIPv4(ctx context.Context, host string, upstreams []string) (string, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(host), dns.TypeA)
	msg.SetEdns0(1232, false)
	resp, err := ExchangeDNS(ctx, msg, upstreams)
	if err != nil {
		return "", err
	}
	if resp.Rcode == dns.RcodeSuccess {
		for _, rr := range resp.Answer {
			if a, ok := rr.(*dns.A); ok && a.A.To4() != nil && !a.A.IsUnspecified() && !a.A.IsMulticast() {
				return a.A.To4().String(), nil
			}
		}
	}
	return "", fmt.Errorf("no IPv4 address for %s", host)
}
