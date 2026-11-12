package notification

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookFormatsAndHeaders(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"json", "slack", "discord", "teams", "ntfy"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			received := make(chan []byte, 1)
			headers := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				received <- b
				headers <- r.Header.Clone()
				w.WriteHeader(204)
			}))
			defer server.Close()
			m := Message{ID: "delivery-id", Kind: "opened", Instance: "AppTown", Title: "App failed", Lines: []string{"Check the app."}, Link: "https://apps.example/projects/app", Urgent: true}
			if reason := sendWebhook(t.Context(), channelConfig{URLFrom: []string{"URL"}, Format: format}, map[string]string{"URL": server.URL}, m); reason != "" {
				t.Fatal(reason)
			}
			b := <-received
			h := <-headers
			if h.Get("X-AppTown-Delivery") != m.ID {
				t.Fatal("external id missing")
			}
			if format == "ntfy" {
				if string(b) != "App failed\nCheck the app." || h.Get("Title") != "App failed" || h.Get("Priority") != "high" || h.Get("Click") != m.Link {
					t.Fatalf("ntfy: %s %+v", b, h)
				}
				return
			}
			var got any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			golden := map[string]string{
				"json":    `{"id":"delivery-id","kind":"opened","instance":"AppTown"}`,
				"slack":   `{"text":"App failed\nCheck the app."}`,
				"discord": `{"content":"App failed\nCheck the app."}`,
				"teams":   `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard","version":"1.4","body":[{"type":"TextBlock","text":"App failed\nCheck the app.","wrap":true}]}}]}`,
			}
			var want any
			_ = json.Unmarshal([]byte(golden[format]), &want)
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Fatalf("%s: %s, want %s", format, g, w)
			}
		})
	}
}
func TestWebhookDoesNotFollowRedirectsOrReadUnboundedReplies(t *testing.T) {
	t.Parallel()
	var second bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { second = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
		_, _ = io.WriteString(w, strings.Repeat("secret-response", 1000))
	}))
	defer server.Close()
	reason := sendWebhook(t.Context(), channelConfig{URLFrom: []string{"URL"}}, map[string]string{"URL": server.URL}, Message{})
	if reason != "HTTP 302" || second {
		t.Fatalf("redirect: %s %v", reason, second)
	}
}
func TestDiscordCharacterLimit(t *testing.T) {
	t.Parallel()
	_, body := render(Message{Title: strings.Repeat("é", 2100)}, "discord")
	var m struct{ Content string }
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if len([]rune(m.Content)) != 2000 {
		t.Fatal("discord message exceeds its character limit")
	}
}

func smtpTLS(t *testing.T) (tls.Certificate, *tls.Config) {
	t.Helper()
	at := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.example"}, DNSNames: []string{"smtp.example"}, NotBefore: at.Add(-time.Hour), NotAfter: at.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	return cert, &tls.Config{ServerName: "smtp.example", RootCAs: roots, MinVersion: tls.VersionTLS12, Time: func() time.Time { return at }}
}

type smtpReceipt struct {
	body, auth string
	err        error
}

// fakeSMTP runs entirely over net.Pipe, including STARTTLS and PLAIN auth.
func fakeSMTP(ctx context.Context, conn net.Conn, cert tls.Certificate, accept bool) smtpReceipt {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reply := func(s string) error {
		if _, err := w.WriteString(s + "\r\n"); err != nil {
			return err
		}
		return w.Flush()
	}
	if err := reply("220 smtp.example"); err != nil {
		return smtpReceipt{err: err}
	}
	out := smtpReceipt{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			out.err = err
			return out
		}
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "EHLO "):
			err = reply("250-smtp.example\r\n250-STARTTLS\r\n250 AUTH PLAIN")
		case line == "STARTTLS":
			if err = reply("220 start TLS"); err == nil {
				secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				err = secured.HandshakeContext(ctx)
				conn = secured
				r = bufio.NewReader(conn)
				w = bufio.NewWriter(conn)
			}
		case strings.HasPrefix(line, "AUTH PLAIN "):
			decoded, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			if e != nil {
				return smtpReceipt{err: e}
			}
			out.auth = string(decoded)
			if accept {
				err = reply("235 authenticated")
			} else {
				err = reply("535 secret-password echoed by hostile server")
			}
		case strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:"):
			err = reply("250 accepted")
		case line == "DATA":
			if err = reply("354 send message"); err != nil {
				break
			}
			var b strings.Builder
			for {
				l, e := r.ReadString('\n')
				if e != nil {
					return smtpReceipt{err: e}
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			out.body = b.String()
			err = reply("250 accepted")
		case line == "QUIT":
			_ = reply("221 bye")
			return out
		case line == "*":
			err = reply("501 cancelled")
		default:
			err = fmt.Errorf("unexpected SMTP command")
		}
		if err != nil {
			out.err = err
			return out
		}
	}
}
func TestSMTPSTARTTLSAuthenticationAndSafeRejection(t *testing.T) {
	t.Parallel()
	for _, accept := range []bool{true, false} {
		t.Run(fmt.Sprint(accept), func(t *testing.T) {
			t.Parallel()
			cert, cfg := smtpTLS(t)
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan smtpReceipt, 1)
			go func() { done <- fakeSMTP(ctx, server, cert, accept) }()
			m := Message{ID: "stable-id", Title: "App failed", Lines: []string{"Check the app."}}
			c := channelConfig{Host: "smtp.example", Port: 587, Security: "starttls", Username: "admin", PasswordFrom: []string{"PASSWORD"}, From: "AppTown <notices@example.com>", To: "admin@example.com,team@example.com"}
			reason := smtpSend(ctx, c, map[string]string{"PASSWORD": "secret-password"}, m, func(context.Context, string, string) (net.Conn, error) { return client, nil }, cfg)
			var receipt smtpReceipt
			select {
			case receipt = <-done:
			case <-ctx.Done():
				t.Fatal("SMTP peer did not finish")
			}
			if receipt.err != nil {
				t.Fatal(receipt.err)
			}
			if receipt.auth != "\x00admin\x00secret-password" {
				t.Fatalf("authentication missing: %q", receipt.auth)
			}
			if accept {
				if reason != "" || !strings.Contains(receipt.body, "Message-ID: <stable-id@apptown>") || !strings.Contains(receipt.body, "To: <admin@example.com>, <team@example.com>") || !strings.Contains(receipt.body, "Check the app.") {
					t.Fatalf("message: %s %s", reason, receipt.body)
				}
			} else if reason != "SMTP 535" {
				t.Fatalf("unsafe rejection: %q", reason)
			}
		})
	}
}
