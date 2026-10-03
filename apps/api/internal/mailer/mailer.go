// Package mailer sends plain-text email over SMTP, driving the protocol itself.
//
// net/smtp is frozen and its SendMail has no context and no timeout, so a relay
// that accepts the connection and then goes quiet would hold a goroutine, and
// with it the morning pass, indefinitely. This package dials with a timeout,
// puts a deadline on the whole conversation, closes the socket when the context
// is cancelled, and always upgrades to TLS when asked to (it never falls back
// to sending a password in the clear).
//
// It is the first outbound client here that is not HTTP, so otelhttp does not
// apply: a send opens one manual span, "smtp send", carrying the relay's host
// and port and nothing else.
//
// Three things are never in a log, an error or a span: the password, the
// recipient, and anything the server said. SMTP servers echo the recipient in
// their refusals ("550 5.1.1 <name@host> unknown"), so a failure is reported
// as a SendError holding the stage that failed and the numeric reply code, and
// that is all.
package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// EnvPassword is where the SMTP password comes from. Never the config file.
// #nosec G101 -- the name of an environment variable, not a credential.
const EnvPassword = "DOMESTIQUE_SMTP_PASSWORD"

const (
	dialTimeout = 10 * time.Second
	// sendDeadline bounds the whole conversation, dial to QUIT.
	sendDeadline = 20 * time.Second
	// subjectLimit keeps the Subject header on one line (RFC 5322 asks for 78
	// octets including the field name).
	subjectLimit = 69
)

// Config is the connection: the notifications.smtp block, minus the password.
type Config struct {
	Host string
	Port int
	// Security is "starttls", "tls" or "none" (a relay on loopback).
	Security string
	Username string
	// From is the sender, "Domestique <domestique@example.com>" or bare.
	From string
}

// SendError says which stage failed and with what numeric reply code, and
// nothing a server wrote. Code is 0 when the failure was not a reply (a refused
// connection, a timeout). Unwrap yields only the context's error, so a
// cancelled or timed-out send still satisfies errors.Is.
type SendError struct {
	Stage  string
	Code   int
	ctxErr error
}

func (e *SendError) Error() string {
	s := "smtp " + e.Stage + " failed"
	if e.Code != 0 {
		s += " (code " + strconv.Itoa(e.Code) + ")"
	}
	if e.ctxErr != nil {
		s += ": " + e.ctxErr.Error()
	}
	return s
}

func (e *SendError) Unwrap() error { return e.ctxErr }

// Mailer sends mail through one relay. It holds the password for its lifetime
// and exposes it to nothing.
type Mailer struct {
	cfg      Config
	password string
	// Log, when set, gets one Debug line per send with the stage and code of a
	// failure. Callers log the outcome that matters at Warn.
	Log *slog.Logger

	// rootCAs and connDeadline exist for tests: a fake server's own CA, and a
	// deadline short enough to wait for.
	rootCAs      *x509.CertPool
	connDeadline time.Duration
}

// New returns a Mailer. The password is read from the environment by main and
// passed in; it is not retained anywhere but here.
func New(cfg Config, password string) *Mailer {
	return &Mailer{cfg: cfg, password: password, connDeadline: sendDeadline}
}

// Send delivers one plain-text message to a single recipient. One send is one
// connection: nothing is pooled and nothing is retried.
func (m *Mailer) Send(ctx context.Context, to, subject, body string) (err error) {
	rcpt, ok := singleAddress(to)
	if !ok {
		return &SendError{Stage: "recipient"}
	}
	from, perr := mail.ParseAddress(m.cfg.From)
	if perr != nil {
		return &SendError{Stage: "sender"}
	}

	ctx, span := otel.Tracer("github.com/wncservices/domestique/internal/mailer").Start(ctx, "smtp send",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("smtp.host", m.cfg.Host), attribute.Int("smtp.port", m.cfg.Port)))
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, m.connDeadline)
	defer cancel()

	defer func() {
		var se *SendError
		if errors.As(err, &se) {
			span.SetStatus(codes.Error, se.Stage)
			if m.Log != nil {
				m.Log.Debug("smtp send failed", "stage", se.Stage, "code", se.Code)
			}
		}
	}()

	msg := buildMessage(from, rcpt, subject, body)
	fail := func(stage string, cause error) error {
		se := &SendError{Stage: stage}
		var te *textproto.Error
		if errors.As(cause, &te) {
			se.Code = te.Code
		}
		if ce := ctx.Err(); ce != nil {
			se.ctxErr = ce
		}
		return se
	}

	conn, derr := m.dial(ctx)
	if derr != nil {
		return fail("dial", derr)
	}
	// Cancelling the context, or the deadline, closes the socket, which is
	// what unblocks a read on a server that has gone quiet.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	c, cerr := smtp.NewClient(conn, m.cfg.Host)
	if cerr != nil {
		return fail("greeting", cerr)
	}
	if herr := c.Hello("domestique"); herr != nil {
		return fail("hello", herr)
	}

	if m.cfg.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			// Never carry on in the clear: a password or a recipient sent
			// over an unencrypted hop is the thing this refuses to do.
			return fail("starttls", errors.New("not offered"))
		}
		if serr := c.StartTLS(m.tlsConfig()); serr != nil {
			return fail("starttls", serr)
		}
	}
	if m.cfg.Username != "" {
		// PlainAuth refuses to send a password over an unencrypted
		// connection, except to localhost.
		if aerr := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.password, m.cfg.Host)); aerr != nil {
			return fail("auth", aerr)
		}
	}
	if merr := c.Mail(from.Address); merr != nil {
		return fail("mail", merr)
	}
	if rerr := c.Rcpt(rcpt); rerr != nil {
		return fail("rcpt", rerr)
	}
	w, derr := c.Data()
	if derr != nil {
		return fail("data", derr)
	}
	if _, werr := io.WriteString(w, msg); werr != nil {
		return fail("body", werr)
	}
	if cerr := w.Close(); cerr != nil {
		return fail("data", cerr)
	}
	// The message is accepted; a failing QUIT changes nothing.
	_ = c.Quit()
	if m.Log != nil {
		m.Log.Debug("smtp send ok")
	}
	return nil
}

func (m *Mailer) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: m.rootCAs}
}

func (m *Mailer) dial(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	nd := &net.Dialer{Timeout: dialTimeout}
	if m.cfg.Security == "tls" {
		return (&tls.Dialer{NetDialer: nd, Config: m.tlsConfig()}).DialContext(ctx, "tcp", addr)
	}
	return nd.DialContext(ctx, "tcp", addr)
}

// singleAddress accepts exactly one bare address: no display name, no list, no
// control characters. The address comes from the signed-in identity, but a
// header is built from it, so it is checked like input.
func singleAddress(to string) (string, bool) {
	if to == "" || strings.ContainsAny(to, "\r\n\x00,;<> \t") {
		return "", false
	}
	a, err := mail.ParseAddress(to)
	if err != nil || a.Name != "" || a.Address != to {
		return "", false
	}
	return a.Address, true
}

// buildMessage is a plain-text message: CRLF line ends, a quoted-printable
// UTF-8 body, and a Subject with CR and LF removed so it cannot start a header.
func buildMessage(from *mail.Address, to, subject, body string) string {
	var b strings.Builder
	h := func(name, value string) { b.WriteString(name + ": " + value + "\r\n") }

	h("From", from.String())
	h("To", "<"+to+">")
	h("Subject", encodeSubject(subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+randomID()+"@"+domainOf(from.Address)+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")

	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	qp := quotedprintable.NewWriter(&b)
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()
	if !strings.HasSuffix(b.String(), "\r\n") {
		b.WriteString("\r\n")
	}
	return b.String()
}

// encodeSubject strips CR and LF and encodes the rest as an RFC 2047 word when
// it needs one, shortening it until the header fits one line.
func encodeSubject(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	s = strings.ToValidUTF8(s, "")
	for {
		enc := mime.QEncoding.Encode("utf-8", s)
		if len(enc) <= subjectLimit || s == "" {
			return enc
		}
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
}

func randomID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return "localhost"
}
