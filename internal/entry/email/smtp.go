package email

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/agentruntime/coding-agent/internal/entry"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// SMTPConfig is operator-owned configuration, never returned by an HTTP API.
// ImplicitTLS uses TLS from connection start (usually port 465). Otherwise
// STARTTLS is mandatory. Plaintext credential or message transmission is refused.
type SMTPConfig struct {
	Addr        string
	Username    string
	Password    string
	From        string
	ImplicitTLS bool
	Timeout     time.Duration
}
type Sink struct{ Config SMTPConfig }

func (Sink) Name() string { return "email" }
func (Sink) Phases() []entry.Phase {
	return []entry.Phase{entry.Accepted, entry.Question, entry.Result, entry.Failed}
}
func (Sink) Render(o entry.Outcome, p entry.Phase, lang string) entry.Message {
	labels := map[entry.Phase]string{entry.Accepted: "已受理", entry.Question: "需要确认", entry.Result: "任务结果", entry.Failed: "任务结束"}
	if lang == "en" {
		labels = map[entry.Phase]string{entry.Accepted: "Task accepted", entry.Question: "Confirmation needed", entry.Result: "Task result", entry.Failed: "Task ended"}
	}
	body := o.Summary
	if o.URL != "" {
		body += "\n\n" + o.URL
	}
	return entry.Message{Subject: "Coding Agent — " + labels[p], Body: body}
}
func (c SMTPConfig) Validate() error {
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil || host == "" || port == "" {
		return errors.New("invalid SMTP address")
	}
	if _, err = NormalizeAddress(c.From); err != nil {
		return errors.New("invalid SMTP sender")
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("SMTP username and password must be configured together")
	}
	return nil
}
func (s Sink) Deliver(ctx context.Context, c entry.Connection, a entry.Address, m entry.Message, key string) error {
	if err := s.Config.Validate(); err != nil {
		return err
	}
	from, err := mail.ParseAddress(s.Config.From)
	if err != nil {
		return errors.New("invalid SMTP sender")
	}
	to, err := NormalizeAddress(a.Email)
	if err != nil {
		return errors.New("invalid email destination")
	}
	// The application validates the destination on connection writes or derives it
	// from a verified inbound sender. This adapter sends to exactly one address.
	payload, err := BuildMessage(from.Address, to, a, m, key)
	if err != nil {
		return err
	}
	cfg := s.Config
	timeout := cfg.Timeout
	if timeout <= 0 || timeout > time.Minute {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	host, _, _ := net.SplitHostPort(cfg.Addr)
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if cfg.ImplicitTLS {
		secure := tls.Client(conn, tlsConfig)
		if secure.HandshakeContext(ctx) != nil {
			return errors.New("SMTP TLS negotiation failed")
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if !cfg.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support required STARTTLS")
		}
		if client.StartTLS(tlsConfig) != nil {
			return errors.New("SMTP TLS negotiation failed")
		}
	}
	if cfg.Username != "" {
		if client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, host)) != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if client.Mail(from.Address) != nil {
		return errors.New("SMTP sender rejected")
	}
	if client.Rcpt(to) != nil {
		return errors.New("SMTP recipient rejected")
	}
	w, err := client.Data()
	if err != nil {
		return errors.New("SMTP DATA rejected")
	}
	if _, err = io.WriteString(w, payload); err != nil {
		return fmt.Errorf("%w: SMTP interrupted during DATA", entry.ErrUnknownDelivery)
	}
	if err = w.Close(); err != nil {
		var response *textproto.Error
		// A complete server rejection is a definite failure. Losing the reply after
		// the final dot may mean accepted mail: the outbox must not automatically retry.
		if errors.As(err, &response) && response.Code >= 400 && response.Code <= 599 {
			return errors.New("SMTP message rejected")
		}
		return fmt.Errorf("%w: SMTP acknowledgement unavailable", entry.ErrUnknownDelivery)
	}
	// The 250 acknowledgement is the commit point. QUIT failure cannot undo it.
	_ = client.Quit()
	return nil
}
func BuildMessage(from, to string, a entry.Address, m entry.Message, key string) (string, error) {
	fromAddr, err := NormalizeAddress(from)
	if err != nil {
		return "", err
	}
	toAddr, err := NormalizeAddress(to)
	if err != nil {
		return "", err
	}
	if key == "" || len(m.Body) > 8<<20 {
		return "", errors.New("invalid outgoing email")
	}
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Subject)
	if len(subject) > 998 {
		subject = subject[:998]
	}
	domain := strings.Split(fromAddr, "@")[1]
	h := sha256.Sum256([]byte(key))
	messageID := "<entry-" + hex.EncodeToString(h[:]) + "@" + domain + ">"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: %s\r\nDate: %s\r\nAuto-Submitted: auto-replied\r\nX-Auto-Response-Suppress: All\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n", (&mail.Address{Address: fromAddr}).String(), (&mail.Address{Address: toAddr}).String(), mime.QEncoding.Encode("utf-8", subject), messageID, time.Now().UTC().Format(time.RFC1123Z))
	if a.ThreadID != "" {
		if !ValidMessageID(a.ThreadID) {
			return "", ErrMessage
		}
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", a.ThreadID)
		refs := []string{}
		for _, id := range strings.Fields(a.References) {
			if !ValidMessageID(id) {
				return "", ErrMessage
			}
			if id != a.ThreadID {
				refs = append(refs, id)
			}
		}
		if len(refs) > 8 {
			refs = refs[len(refs)-8:]
		}
		refs = append(refs, a.ThreadID)
		for i, r := range refs {
			if i == 0 {
				b.WriteString("References: ")
			} else {
				b.WriteString("\r\n ")
			}
			b.WriteString(r)
		}
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	writer := quotedprintable.NewWriter(&b)
	if _, err = io.WriteString(writer, strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}
