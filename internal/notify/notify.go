// Package notify sends operator email over SMTP (plain, STARTTLS or implicit TLS).
package notify

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	Host, Security, User, Pass, From, To string
	Port                                 int
}

func (c Config) Ready() bool { return c.Host != "" && c.From != "" && c.To != "" }

func Send(c Config, subject, body string) error {
	if !c.Ready() {
		return fmt.Errorf("SMTP not configured (host, from and recipients required)")
	}
	if c.Port == 0 {
		c.Port = 587
	}
	addr := net.JoinHostPort(c.Host, fmt.Sprint(c.Port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	tlsCfg := &tls.Config{ServerName: c.Host}

	var cl *smtp.Client
	var err error
	if c.Security == "tls" {
		conn, derr := tls.DialWithDialer(d, "tcp", addr, tlsCfg)
		if derr != nil {
			return derr
		}
		cl, err = smtp.NewClient(conn, c.Host)
	} else {
		conn, derr := d.Dial("tcp", addr)
		if derr != nil {
			return derr
		}
		cl, err = smtp.NewClient(conn, c.Host)
	}
	if err != nil {
		return err
	}
	defer cl.Close()
	_ = cl.Hello("vaultkeeper")
	if c.Security == "starttls" {
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, c.Host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := cl.Mail(addrOnly(c.From)); err != nil {
		return err
	}
	var rcpts []string
	for _, r := range strings.FieldsFunc(c.To, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		rcpts = append(rcpts, r)
		if err := cl.Rcpt(addrOnly(r)); err != nil {
			return fmt.Errorf("recipient %s: %w", r, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		c.From, strings.Join(rcpts, ", "), sanitize(subject), time.Now().Format(time.RFC1123Z), body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

func sanitize(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

func addrOnly(s string) string {
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return s[i+1 : i+j]
		}
	}
	return strings.TrimSpace(s)
}
