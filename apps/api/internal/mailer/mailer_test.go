package mailer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	testUser     = "domestique"
	testPassword = "s3cret-pw-do-not-log"
	recipient    = "rider.private@example.org"
)

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake smtp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// fakeSMTP is a tiny in-process SMTP server: enough of the protocol for the
// client to be exercised, nothing ever leaves the machine.
type fakeSMTP struct {
	ln   net.Listener
	cert tls.Certificate

	implicitTLS   bool
	offerStartTLS bool
	requireAuth   bool
	rcptReply     string // overrides the RCPT reply; %s is the address
	stall         bool   // greet, then never answer another command

	mu       sync.Mutex
	messages []string
	rcpts    []string
	authed   bool
	tlsUsed  bool
}

func startFake(t *testing.T, cert tls.Certificate, configure func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, cert: cert}
	if configure != nil {
		configure(f)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	cfg := &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12}
	active := false
	if f.implicitTLS {
		conn = tls.Server(conn, cfg)
		active = true
		f.setTLS()
	}
	r := bufio.NewReader(conn)
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(conn, format+"\r\n", args...) }
	say("220 fake ESMTP ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if f.stall {
			continue
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-fake")
			if f.offerStartTLS && !active {
				say("250-STARTTLS")
			}
			if f.requireAuth {
				say("250-AUTH PLAIN")
			}
			say("250 8BITMIME")
		case cmd == "STARTTLS":
			say("220 go ahead")
			conn = tls.Server(conn, cfg)
			r = bufio.NewReader(conn)
			active = true
			f.setTLS()
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line)[len("AUTH PLAIN "):])
			if string(raw) == "\x00"+testUser+"\x00"+testPassword {
				f.mu.Lock()
				f.authed = true
				f.mu.Unlock()
				say("235 2.7.0 accepted")
			} else {
				say("535 5.7.8 authentication failed")
			}
		case strings.HasPrefix(cmd, "MAIL FROM"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			addr := strings.Trim(strings.TrimSpace(line)[len("RCPT TO:"):], "<> ")
			f.mu.Lock()
			f.rcpts = append(f.rcpts, addr)
			f.mu.Unlock()
			if f.rcptReply != "" {
				say(f.rcptReply, addr)
			} else {
				say("250 ok")
			}
		case cmd == "DATA":
			say("354 go on")
			var msg strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				msg.WriteString(l)
			}
			f.mu.Lock()
			f.messages = append(f.messages, msg.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

func (f *fakeSMTP) setTLS() {
	f.mu.Lock()
	f.tlsUsed = true
	f.mu.Unlock()
}

func (f *fakeSMTP) snapshot() (msgs, rcpts []string, authed, tlsUsed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages...), append([]string(nil), f.rcpts...), f.authed, f.tlsUsed
}

type logs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newMailer(t *testing.T, f *fakeSMTP, pool *x509.CertPool, security, username string) (*Mailer, *logs) {
	t.Helper()
	l := &logs{}
	m := New(Config{Host: "127.0.0.1", Port: f.port(), Security: security, Username: username, From: "Domestique <domestique@example.com>"}, testPassword)
	m.rootCAs = pool
	m.Log = slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return m, l
}

// assertNoSecrets is the contract of this package: an error or a log line may
// carry a stage and a code, never the address or the password.
func assertNoSecrets(t *testing.T, what string, text string) {
	t.Helper()
	for _, secret := range []string{recipient, "rider.private", testPassword} {
		if strings.Contains(text, secret) {
			t.Errorf("%s contains %q:\n%s", what, secret, text)
		}
	}
}

func TestSendPlainToLoopback(t *testing.T) {
	cert, _ := testCert(t)
	f := startFake(t, cert, nil)
	m, _ := newMailer(t, f, nil, "none", "")
	if err := m.Send(t.Context(), recipient, "Hello", "Body line"); err != nil {
		t.Fatal(err)
	}
	msgs, rcpts, authed, tlsUsed := f.snapshot()
	if len(msgs) != 1 || len(rcpts) != 1 || rcpts[0] != recipient {
		t.Fatalf("messages %d, rcpts %v", len(msgs), rcpts)
	}
	if authed || tlsUsed {
		t.Errorf("authed %v tls %v on a plain loopback relay with no username", authed, tlsUsed)
	}
}

func TestSendStartTLSWithAuth(t *testing.T) {
	cert, pool := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.offerStartTLS, f.requireAuth = true, true })
	m, _ := newMailer(t, f, pool, "starttls", testUser)
	if err := m.Send(t.Context(), recipient, "Hello", "Body"); err != nil {
		t.Fatal(err)
	}
	msgs, _, authed, tlsUsed := f.snapshot()
	if len(msgs) != 1 || !authed || !tlsUsed {
		t.Fatalf("messages %d authed %v tls %v", len(msgs), authed, tlsUsed)
	}
}

func TestSendImplicitTLS(t *testing.T) {
	cert, pool := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.implicitTLS, f.requireAuth = true, true })
	m, _ := newMailer(t, f, pool, "tls", testUser)
	if err := m.Send(t.Context(), recipient, "Hello", "Body"); err != nil {
		t.Fatal(err)
	}
	if msgs, _, authed, tlsUsed := f.snapshot(); len(msgs) != 1 || !authed || !tlsUsed {
		t.Fatalf("messages %d authed %v tls %v", len(msgs), authed, tlsUsed)
	}
}

// A server that does not offer STARTTLS must never be sent a password in the
// clear: the send fails rather than downgrade.
func TestStartTLSIsNeverSkipped(t *testing.T) {
	cert, pool := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.requireAuth = true }) // no STARTTLS offered
	m, _ := newMailer(t, f, pool, "starttls", testUser)
	err := m.Send(t.Context(), recipient, "Hello", "Body")
	var se *SendError
	if !errors.As(err, &se) || se.Stage != "starttls" {
		t.Fatalf("err = %v, want a starttls SendError", err)
	}
	if msgs, _, authed, _ := f.snapshot(); len(msgs) != 0 || authed {
		t.Error("something was sent without TLS")
	}
}

func TestAuthRefusedIsAStageAndACode(t *testing.T) {
	cert, pool := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.offerStartTLS, f.requireAuth = true, true })
	l := &logs{}
	m := New(Config{Host: "127.0.0.1", Port: f.port(), Security: "starttls", Username: testUser, From: "d@example.com"}, "the-wrong-password")
	m.rootCAs, m.Log = pool, slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
	err := m.Send(t.Context(), recipient, "Hello", "Body")
	var se *SendError
	if !errors.As(err, &se) || se.Stage != "auth" || se.Code != 535 {
		t.Fatalf("err = %#v, want auth 535", err)
	}
	if strings.Contains(err.Error(), "the-wrong-password") || strings.Contains(l.String(), "the-wrong-password") {
		t.Error("the password reached an error or a log")
	}
	if msgs, _, _, _ := f.snapshot(); len(msgs) != 0 {
		t.Error("a message was delivered after a refused login")
	}
}

// Servers echo the recipient in their errors ("550 5.1.1 <name@host> unknown"),
// so the error carries the stage and the numeric code and nothing the server said.
func TestRecipientRefusalNeverEchoesTheAddress(t *testing.T) {
	cert, _ := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.rcptReply = "550 5.1.1 <%s> user unknown" })
	m, l := newMailer(t, f, nil, "none", "")
	err := m.Send(t.Context(), recipient, "Hello", "Body")
	var se *SendError
	if !errors.As(err, &se) || se.Stage != "rcpt" || se.Code != 550 {
		t.Fatalf("err = %#v, want rcpt 550", err)
	}
	assertNoSecrets(t, "the error", err.Error())
	assertNoSecrets(t, "the logs", l.String())
	if !strings.Contains(l.String(), "rcpt") || !strings.Contains(l.String(), "550") {
		t.Errorf("the log should say what stage and code failed:\n%s", l.String())
	}
}

func TestAStalledServerTimesOut(t *testing.T) {
	cert, _ := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.stall = true })
	m, _ := newMailer(t, f, nil, "none", "")
	m.connDeadline = 300 * time.Millisecond
	start := time.Now()
	err := m.Send(t.Context(), recipient, "Hello", "Body")
	if err == nil {
		t.Fatal("a server that never answers was reported as success")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("took %v: the deadline did not apply", took)
	}
	var se *SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a SendError", err)
	}
	assertNoSecrets(t, "the error", err.Error())
}

func TestCancellingTheContextClosesTheSocket(t *testing.T) {
	cert, _ := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.stall = true })
	m, _ := newMailer(t, f, nil, "none", "")
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(150*time.Millisecond, cancel)
	start := time.Now()
	err := m.Send(ctx, recipient, "Hello", "Body")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("took %v after cancel; the 20s deadline was left to run", took)
	}
}

func TestDialFailureIsAStage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // nothing listens here now
	m := New(Config{Host: "127.0.0.1", Port: port, Security: "none", From: "d@example.com"}, "")
	err = m.Send(t.Context(), recipient, "Hello", "Body")
	var se *SendError
	if !errors.As(err, &se) || se.Stage != "dial" {
		t.Fatalf("err = %v, want a dial SendError", err)
	}
	assertNoSecrets(t, "the error", err.Error())
}

func TestMessageShape(t *testing.T) {
	cert, _ := testCert(t)
	f := startFake(t, cert, nil)
	m, _ := newMailer(t, f, nil, "none", "")
	subject := "Today's ride, é Threshold\r\nBcc: evil@example.net"
	body := "First line\nSecond line with é and a long tail " + strings.Repeat("x", 120) + "\n"
	if err := m.Send(t.Context(), recipient, subject, body); err != nil {
		t.Fatal(err)
	}
	msgs, _, _, _ := f.snapshot()
	msg := msgs[0]

	if strings.Contains(strings.ReplaceAll(msg, "\r\n", ""), "\n") || strings.Contains(strings.ReplaceAll(msg, "\r\n", ""), "\r") {
		t.Error("the message has a bare CR or LF")
	}
	head, _, _ := strings.Cut(msg, "\r\n\r\n")
	for _, want := range []string{
		"From: \"Domestique\" <domestique@example.com>", "To: <" + recipient + ">",
		"MIME-Version: 1.0", "Content-Type: text/plain; charset=utf-8", "Content-Transfer-Encoding: quoted-printable",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("headers lack %q:\n%s", want, head)
		}
	}
	for _, prefix := range []string{"Date: ", "Message-ID: <", "Subject: =?utf-8?q?"} {
		if !strings.Contains(head, prefix) {
			t.Errorf("headers lack %q:\n%s", prefix, head)
		}
	}
	if strings.Contains(head, "\r\nBcc:") || strings.Contains(strings.ToLower(head), "evil@example.net\r\n") {
		t.Errorf("a header was injected through the subject:\n%s", head)
	}
	for _, line := range strings.Split(msg, "\r\n") {
		if len(line) > 78 {
			t.Errorf("a line is %d octets; quoted-printable must keep lines short: %q", len(line), line)
		}
	}
	if !strings.Contains(msg, "=C3=A9") {
		t.Error("the body is not quoted-printable encoded UTF-8")
	}
	if _, err := time.Parse(time.RFC1123Z, strings.TrimPrefix(headerLine(head, "Date"), "Date: ")); err != nil {
		t.Errorf("Date is not RFC 5322: %v", err)
	}
}

func headerLine(head, name string) string {
	for _, l := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(l, name+": ") {
			return l
		}
	}
	return ""
}

func TestARecipientThatIsNotOneAddressIsRefusedBeforeConnecting(t *testing.T) {
	m := New(Config{Host: "127.0.0.1", Port: 1, Security: "none", From: "d@example.com"}, "")
	for _, bad := range []string{"", "a@b.example\r\nBcc: x@y.example", "two@a.example, three@b.example", "Name <n@a.example>", "not-an-address"} {
		err := m.Send(t.Context(), bad, "s", "b")
		var se *SendError
		if !errors.As(err, &se) || se.Stage != "recipient" {
			t.Errorf("%q: err = %v, want a recipient SendError", bad, err)
		}
	}
}

func TestSendSpanCarriesHostAndPortOnly(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	cert, _ := testCert(t)
	f := startFake(t, cert, func(f *fakeSMTP) { f.rcptReply = "550 5.1.1 <%s> user unknown" })
	m, _ := newMailer(t, f, nil, "none", "")
	_ = m.Send(t.Context(), recipient, "A subject nobody should trace", "A body nobody should trace")

	var found bool
	for _, sp := range rec.Ended() {
		if sp.Name() != "smtp send" {
			continue
		}
		found = true
		attrs := map[string]string{}
		for _, kv := range sp.Attributes() {
			attrs[string(kv.Key)] = kv.Value.String()
		}
		if attrs["smtp.host"] != "127.0.0.1" || attrs["smtp.port"] != strconv.Itoa(f.port()) {
			t.Errorf("attributes = %v", attrs)
		}
		if len(attrs) != 2 {
			t.Errorf("a span carries more than host and port: %v", attrs)
		}
		dump := fmt.Sprint(sp.Name(), attrs, sp.Status(), sp.Events())
		assertNoSecrets(t, "the span", dump)
		for _, banned := range []string{"nobody should trace", "domestique@example.com"} {
			if strings.Contains(dump, banned) {
				t.Errorf("span carries %q: %s", banned, dump)
			}
		}
	}
	if !found {
		t.Fatal("no `smtp send` span was recorded")
	}
}

// The span descends from the caller's, not from a fresh root.
func TestSendSpanIsAChildOfTheCallers(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	cert, _ := testCert(t)
	f := startFake(t, cert, nil)
	m, _ := newMailer(t, f, nil, "none", "")
	ctx, parent := tp.Tracer("test").Start(t.Context(), "request")
	if err := m.Send(ctx, recipient, "s", "b"); err != nil {
		t.Fatal(err)
	}
	parent.End()
	for _, sp := range rec.Ended() {
		if sp.Name() == "smtp send" && sp.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Error("`smtp send` is not a child of the caller's span")
		}
	}
}

// An unknown or empty security value must never fall through to plaintext, and
// "none" is only for a relay on this machine. Nothing is dialled in any case.
func TestSecurityFailsClosed(t *testing.T) {
	cases := map[string]struct{ security, host string }{
		"empty":                 {"", "127.0.0.1"},
		"wrong case":            {"STARTTLS", "smtp.example.com"},
		"typo":                  {"ssl", "smtp.example.com"},
		"whitespace":            {" tls", "smtp.example.com"},
		"none to a remote host": {"none", "smtp.example.com"},
		"none to a lookalike":   {"none", "localhost.example.com"},
		"none to a private IP":  {"none", "10.0.0.5"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := New(Config{Host: c.host, Port: 1, Security: c.security, From: "d@example.com"}, "")
			err := m.Send(t.Context(), "r@example.com", "s", "b")
			var se *SendError
			if !errors.As(err, &se) || se.Stage != "security" {
				t.Fatalf("Send = %v, want a security-stage SendError before any connection", err)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST", "localhost.", "127.0.0.1", "127.9.9.9", "::1", "[::1]"} {
		if !IsLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"", "smtp.example.com", "localhost.example.com", "10.0.0.5", "0.0.0.0", "192.168.1.1"} {
		if IsLoopbackHost(h) {
			t.Errorf("%q must not be loopback", h)
		}
	}
}
