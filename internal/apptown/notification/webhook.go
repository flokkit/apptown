package notification

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func oneSecret(values map[string]string, refs []string) (string, bool) {
	if len(refs) != 1 {
		return "", false
	}
	v := values[refs[0]]
	return v, v != ""
}
func sendWebhook(ctx context.Context, c channelConfig, values map[string]string, m Message) string {
	address, ok := oneSecret(values, c.URLFrom)
	if !ok {
		return "could not read channel secret"
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "invalid webhook address"
	}
	contentType, body := render(m, c.Format)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return "invalid webhook address"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-AppTown-Delivery", m.ID)
	if c.Format == "ntfy" {
		req.Header.Set("Title", headerText(m.Title))
		priority := "default"
		if m.Urgent {
			priority = "high"
		}
		req.Header.Set("Priority", priority)
		if m.Link != "" {
			req.Header.Set("Click", headerText(m.Link))
		}
	}
	client := http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return safeConnectionReason(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Sprintf("HTTP %d", res.StatusCode)
	}
	return ""
}
func headerText(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }
func safeConnectionReason(err error) string {
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return "timeout"
	}
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var record tls.RecordHeaderError
	if errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &record) {
		return "TLS error"
	}
	return "could not connect"
}
