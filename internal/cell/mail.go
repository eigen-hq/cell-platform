package cell

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// MailSettings is the Secret in SystemNamespace with the SMTP server the
// cell sends through (E-79), the one truth (E-82): the API writes it, the
// operator sets the identity's mail stage from it and sends warnings with it.
const MailSettings = "cell-mail"

// Mail is the SMTP server the cell sends through. TLS means implicit TLS
// (usually port 465); otherwise the connection must offer STARTTLS.
type Mail struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	TLS      bool
}

// Data is the Mail as Secret data.
func (m Mail) Data() map[string][]byte {
	return map[string][]byte{"host": []byte(m.Host), "port": []byte(strconv.Itoa(m.Port)), "username": []byte(m.Username),
		"password": []byte(m.Password), "from": []byte(m.From), "tls": []byte(strconv.FormatBool(m.TLS))}
}

// ReadMail reads MailSettings' data.
func ReadMail(data map[string][]byte) (Mail, error) {
	port, err := strconv.Atoi(string(data["port"]))
	if err != nil || string(data["host"]) == "" || string(data["from"]) == "" {
		return Mail{}, fmt.Errorf("secret %s: host, port and from are needed", MailSettings)
	}
	return Mail{Host: string(data["host"]), Port: port, Username: string(data["username"]), Password: string(data["password"]),
		From: string(data["from"]), TLS: string(data["tls"]) == "true"}, nil
}

// Dial connects with TLS and logs in. The caller quits the client; the
// connection ends with ctx's deadline at the latest.
func (m Mail) Dial(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	tlsConfig := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if m.TLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	fail := func(err error) (*smtp.Client, error) {
		_ = c.Close()
		return nil, err
	}
	if !m.TLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fail(errors.New("the server offers no STARTTLS; turn on TLS if it speaks TLS from the start (port 465)"))
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return fail(fmt.Errorf("STARTTLS: %w", err))
		}
	}
	if m.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
			return fail(fmt.Errorf("login: %w", err))
		}
	}
	return c, nil
}

// Send mails a plain text to the given addresses, all in To.
func (m Mail) Send(ctx context.Context, to []string, subject, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	_, domain, _ := strings.Cut(m.From, "@")
	var msg strings.Builder
	for _, h := range [][2]string{
		{"From", m.From}, {"To", strings.Join(to, ", ")}, {"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", time.Now().UTC().Format(time.RFC1123Z)}, {"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"}, {"Content-Type", "text/plain; charset=utf-8"}, {"Content-Transfer-Encoding", "8bit"},
		{"Auto-Submitted", "auto-generated"},
	} {
		if strings.ContainsAny(h[1], "\r\n") {
			return fmt.Errorf("mail header %s has a line break", h[0])
		}
		fmt.Fprintf(&msg, "%s: %s\r\n", h[0], h[1])
	}
	msg.WriteString("\r\n")
	msg.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	c, err := m.Dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("sender %s: %w", m.From, err)
	}
	for _, a := range to {
		if err := c.Rcpt(a); err != nil {
			return fmt.Errorf("recipient %s: %w", a, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg.String())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
