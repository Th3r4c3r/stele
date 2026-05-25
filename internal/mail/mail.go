// Package mail sends transactional emails. SMTP-or-log strategy: if
// STELE_SMTP_HOST is empty, emails are logged to slog at Warn level
// (dev/local convenience). When configured, uses net/smtp + STARTTLS.
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Sender abstracts the email send target. Tests pass a stub.
type Sender interface {
	Send(to, subject, body string) error
}

// FromEnv builds a Sender from STELE_SMTP_* env. Returns a LogSender
// when the host is empty (dev mode).
func FromEnv() Sender {
	host := os.Getenv("STELE_SMTP_HOST")
	if host == "" {
		slog.Info("mail: STELE_SMTP_HOST empty, using log fallback")
		return &LogSender{}
	}
	cfg := SMTPConfig{
		Host:     host,
		Port:     envOr("STELE_SMTP_PORT", "587"),
		Username: os.Getenv("STELE_SMTP_USER"),
		Password: os.Getenv("STELE_SMTP_PASS"),
		From:     envOr("STELE_SMTP_FROM", "stele@stele.local"),
	}
	return &SMTPSender{cfg: cfg}
}

// LogSender prints emails to stderr via slog. Useful in dev.
type LogSender struct{}

func (LogSender) Send(to, subject, body string) error {
	slog.Warn("mail.LogSender",
		"to", to,
		"subject", subject,
		"body", body,
		"hint", "set STELE_SMTP_HOST to actually send",
	)
	return nil
}

// SMTPConfig is the SMTP server connection bundle.
type SMTPConfig struct {
	Host, Port, Username, Password, From string
}

// SMTPSender sends via net/smtp with STARTTLS.
type SMTPSender struct{ cfg SMTPConfig }

func (s *SMTPSender) Send(to, subject, body string) error {
	if to == "" {
		return errors.New("mail: empty recipient")
	}
	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = c.Quit() }()

	if err := c.Hello("stele"); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	msg := buildMessage(s.cfg.From, to, subject, body)
	if _, err := wc.Write([]byte(msg)); err != nil {
		_ = wc.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return nil
}

func buildMessage(from, to, subject, body string) string {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return sb.String()
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
