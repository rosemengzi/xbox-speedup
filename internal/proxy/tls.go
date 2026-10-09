package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var errHelloRead = errors.New("ClientHello captured")

// helloConn lets Go parse ClientHello, captures every consumed wire byte and
// discards the parser's alert. No TLS response or substitute certificate is sent.
type helloConn struct {
	net.Conn
	prefix bytes.Buffer
}

func (c *helloConn) Read(b []byte) (int, error) {
	remaining := 128*1024 - c.prefix.Len()
	if remaining <= 0 {
		return 0, errors.New("ClientHello too large")
	}
	if len(b) > remaining {
		b = b[:remaining]
	}
	n, err := c.Conn.Read(b)
	c.prefix.Write(b[:n])
	return n, err
}

func (c *helloConn) Write(b []byte) (int, error) { return len(b), nil }

func readClientHello(conn net.Conn) (string, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	peek := &helloConn{Conn: conn}
	host := ""
	parser := tls.Server(peek, &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		host = strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
		return nil, errHelloRead
	}})
	err := parser.HandshakeContext(ctx)
	conn.SetDeadline(time.Time{})
	if !errors.Is(err, errHelloRead) || host == "" {
		return "", nil, errors.New("valid SNI ClientHello required")
	}
	return host, peek.prefix.Bytes(), nil
}

func (p *Proxy) serveTLS(listener net.Listener, srv *http.Server) {
	defer p.stopIfCurrent(srv)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case p.tlsSlots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		p.mu.Lock()
		if p.tlsListener != listener {
			p.mu.Unlock()
			conn.Close()
			<-p.tlsSlots
			return
		}
		p.tlsConns[conn] = struct{}{}
		p.mu.Unlock()
		go func() {
			defer func() { p.untrackTLS(conn); <-p.tlsSlots }()
			p.relayTLS(conn)
		}()
	}
}

func (p *Proxy) untrackTLS(conn net.Conn) {
	conn.Close()
	p.mu.Lock()
	delete(p.tlsConns, conn)
	p.mu.Unlock()
}

func (p *Proxy) relayTLS(client net.Conn) {
	host, prefix, err := readClientHello(client)
	if err != nil {
		return
	}
	if _, allowed := p.idx.Load().rules[host]; !allowed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ip, err := p.resolveIP(ctx, host)
	if err != nil {
		return
	}
	upstream, err := p.tlsDial(ctx, ip)
	if err != nil {
		return
	}
	p.mu.Lock()
	if _, stillActive := p.tlsConns[client]; !stillActive {
		p.mu.Unlock()
		upstream.Close()
		return
	}
	p.tlsConns[upstream] = struct{}{}
	p.mu.Unlock()
	defer p.untrackTLS(upstream)
	client.SetDeadline(time.Now().Add(2 * time.Minute))
	upstream.SetDeadline(time.Now().Add(2 * time.Minute))
	c, u := &activeConn{client}, &activeConn{upstream}
	if _, err := u.Write(prefix); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		io.Copy(u, c)
		u.CloseWrite()
		close(done)
	}()
	io.Copy(c, u)
	c.CloseWrite()
	client.Close()
	upstream.Close()
	<-done
}

// Either direction refreshes both deadlines on its connection, so a long
// one-way download remains alive while truly idle connections expire.
type activeConn struct{ net.Conn }

func (c *activeConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	return n, err
}

func (c *activeConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	return n, err
}

func (c *activeConn) CloseWrite() {
	if tcp, ok := c.Conn.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}
}
