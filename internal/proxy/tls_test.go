package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func originCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "source.example"}, DNSNames: []string{"source.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func TestTLSPassthroughPreservesCDNCertificateAndRequest(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(tls.VersionName(version), func(t *testing.T) {
			cert, roots := originCertificate(t)
			origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "source.example" || r.URL.RequestURI() != "/game/chunk?token=valid" || r.Header.Get("Range") != "bytes=1-2" {
					t.Errorf("original request changed: %s %s", r.Host, r.URL.RequestURI())
				}
				w.Header().Set("Content-Range", "bytes 1-2/100")
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte("ok"))
			}))
			origin.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
			origin.StartTLS()
			t.Cleanup(origin.Close)
			p := testProxy(t, origin)
			p.SetResolvers(nil, func(string) string { return "127.0.0.1" }, "")
			p.tlsDial = func(ctx context.Context, ip string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", origin.Listener.Addr().String())
			}
			if err := p.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.Stop)
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: version, MaxVersion: version}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", p.tlsListener.Addr().String())
			}}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			req, _ := http.NewRequest("GET", "https://source.example/game/chunk?token=valid", nil)
			req.Header.Set("Range", "bytes=1-2")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusPartialContent || string(body) != "ok" {
				t.Fatalf("passthrough response: %d %s", resp.StatusCode, body)
			}
			if len(resp.TLS.VerifiedChains) == 0 || !resp.TLS.PeerCertificates[0].Equal(origin.Certificate()) {
				t.Fatal("did not verify the original CDN certificate")
			}
		})
	}
}

func TestTLSRejectsUnmanagedSNIWithoutDialing(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	p := testProxy(t, target)
	var dials atomic.Int32
	p.tlsDial = func(context.Context, string) (net.Conn, error) { dials.Add(1); return nil, io.EOF }
	if err := p.Start("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	_, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", p.tlsListener.Addr().String(), &tls.Config{ServerName: "unmanaged.example"})
	if err == nil || dials.Load() != 0 {
		t.Fatal("unmanaged SNI was accepted or dialed")
	}
}
