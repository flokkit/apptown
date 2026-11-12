package notification

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type smtpDial func(context.Context, string, string) (net.Conn, error)

type smtpTransport struct {
	dial smtpDial
	tls  *tls.Config
}

func (s *Service) sendSMTP(ctx context.Context, c channelConfig, values map[string]string, m Message) string {
	dial := s.smtp.dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	return smtpSend(ctx, c, values, m, dial, s.smtp.tls)
}

// smtpSend accepts a transport for an in-memory SMTP peer in tests. It never
// returns the server's text, which may echo credentials or addresses.
func smtpSend(ctx context.Context, c channelConfig, values map[string]string, m Message, dial smtpDial, tlsConfig *tls.Config) string {
	if c.Security != "tls" && c.Security != "starttls" && c.Security != "none" {
		return "invalid channel settings"
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return "invalid email addresses"
	}
	tos, err := mail.ParseAddressList(c.To)
	if err != nil || len(tos) == 0 {
		return "invalid email addresses"
	}
	password := ""
	if c.Username != "" {
		var ok bool
		password, ok = oneSecret(values, c.PasswordFrom)
		if !ok {
			return "could not read channel secret"
		}
		if c.Security == "none" {
			return "SMTP authentication requires TLS"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return safeConnectionReason(err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return "could not connect"
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	}
	if c.Security == "tls" {
		conn = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return smtpReason(err)
	}
	defer client.Close()
	if c.Security == "starttls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return smtpReason(err)
		}
	}
	if c.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", c.Username, password, c.Host)); err != nil {
			return smtpReason(err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return smtpReason(err)
	}
	for _, to := range tos {
		if err := client.Rcpt(to.Address); err != nil {
			return smtpReason(err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return smtpReason(err)
	}
	addresses := make([]string, 0, len(tos))
	for _, to := range tos {
		addresses = append(addresses, to.String())
	}
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s@apptown>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n", from.String(), strings.Join(addresses, ", "), mime.QEncoding.Encode("UTF-8", headerText(m.Title)), m.ID)
	if _, err := w.Write([]byte(headers + strings.ReplaceAll(strings.Join(m.Lines, "\n"), "\n", "\r\n") + "\r\n")); err != nil {
		return smtpReason(err)
	}
	if err := w.Close(); err != nil {
		return smtpReason(err)
	}
	// DATA acceptance is the send receipt; a failed QUIT cannot undo it.
	_ = client.Quit()
	return ""
}
func smtpReason(err error) string {
	var e *textproto.Error
	if errors.As(err, &e) {
		return fmt.Sprintf("SMTP %d", e.Code)
	}
	return safeConnectionReason(err)
}
