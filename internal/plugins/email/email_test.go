package email

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
)

func initPlugin(t *testing.T, cfg map[string]any, password string) *EmailPlugin {
	t.Helper()
	p := &EmailPlugin{}
	lookup := func(name string) (string, error) {
		if name == "smtp_password" {
			return password, nil
		}
		return "", nil
	}
	if err := p.Init(context.Background(), cfg, lookup); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseCfg() map[string]any {
	return map[string]any{"smtp_host": "mail.example.com", "from": "fleet@example.com"}
}

func TestInit_Validation(t *testing.T) {
	p := &EmailPlugin{}
	lookup := func(string) (string, error) { return "", nil }
	if err := p.Init(context.Background(), map[string]any{"from": "a@b.c"}, lookup); err == nil {
		t.Error("missing smtp_host: expected Init error")
	}
	if err := p.Init(context.Background(), map[string]any{"smtp_host": "h"}, lookup); err == nil {
		t.Error("missing from: expected Init error")
	}
}

func TestInit_Defaults(t *testing.T) {
	p := initPlugin(t, baseCfg(), "")
	if p.port != "587" {
		t.Errorf("port = %q, want 587", p.port)
	}
	if p.username != "fleet@example.com" {
		t.Errorf("username = %q, want from", p.username)
	}
	if p.Name() != "email" || p.EventSources() != nil {
		t.Error("shape wrong")
	}
}

func TestSendEmail_SeamAndMessage(t *testing.T) {
	p := initPlugin(t, baseCfg(), "s3cret")
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	var gotAuth smtp.Auth
	p.send = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotAuth, gotFrom, gotTo, gotMsg = addr, a, from, to, msg
		return nil
	}

	_, err := p.Do(context.Background(), "send_email", map[string]any{
		"to":      "a@x.com, b@y.com",
		"subject": "Run report",
		"body":    "All good.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAddr != "mail.example.com:587" {
		t.Errorf("addr = %q", gotAddr)
	}
	if gotFrom != "fleet@example.com" {
		t.Errorf("from = %q", gotFrom)
	}
	if len(gotTo) != 2 || gotTo[0] != "a@x.com" || gotTo[1] != "b@y.com" {
		t.Errorf("to = %v", gotTo)
	}
	if gotAuth == nil {
		t.Error("expected PlainAuth when smtp_password set")
	}
	msg := string(gotMsg)
	for _, want := range []string{
		"From: fleet@example.com\r\n",
		"To: a@x.com, b@y.com\r\n",
		"Subject: Run report\r\n",
		"\r\n\r\nAll good.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestSendEmail_NoAuthWhenNoPassword(t *testing.T) {
	p := initPlugin(t, baseCfg(), "")
	var gotAuth smtp.Auth = smtp.PlainAuth("", "x", "y", "z") // sentinel non-nil
	p.send = func(_ string, a smtp.Auth, _ string, _ []string, _ []byte) error {
		gotAuth = a
		return nil
	}
	if _, err := p.Do(context.Background(), "send_email",
		map[string]any{"to": "a@x.com", "subject": "s", "body": "b"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != nil {
		t.Error("expected nil auth when smtp_password empty")
	}
}

func TestSendEmail_Validation(t *testing.T) {
	p := initPlugin(t, baseCfg(), "")
	p.send = func(string, smtp.Auth, string, []string, []byte) error { return nil }
	cases := []map[string]any{
		{"subject": "s", "body": "b"},               // no to
		{"to": "a@x.com", "body": "b"},              // no subject
		{"to": "a@x.com", "subject": "s"},           // no body
		{"to": " , ,", "subject": "s", "body": "b"}, // only empty recipients
	}
	for i, params := range cases {
		if _, err := p.Do(context.Background(), "send_email", params); err == nil {
			t.Errorf("case %d: expected error for %v", i, params)
		}
	}
	if _, err := p.Do(context.Background(), "nope", map[string]any{}); err == nil {
		t.Error("unknown action: expected error")
	}
}

// fakeSMTP is a minimal in-process SMTP server (plaintext, no auth, no
// STARTTLS advertised) capturing one message.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	from string
	to   []string
	data string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = w.WriteString(s + "\r\n"); _ = w.Flush() }
	say("220 fake ESMTP")
	inData := false
	var dataLines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.data = strings.Join(dataLines, "\n")
				f.mu.Unlock()
				say("250 ok")
				continue
			}
			dataLines = append(dataLines, line)
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			say("250 fake") // no extensions: no STARTTLS, no AUTH
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[len("MAIL FROM:"):]
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			f.mu.Lock()
			f.to = append(f.to, line[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case upper == "DATA":
			inData = true
			say("354 go ahead")
		case upper == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestSendEmail_RealSMTPPath(t *testing.T) {
	fake := newFakeSMTP(t)
	host, port, _ := net.SplitHostPort(fake.ln.Addr().String())

	p := initPlugin(t, map[string]any{
		"smtp_host": host,
		"smtp_port": port,
		"from":      "fleet@example.com",
	}, "") // no password -> no auth -> plaintext OK
	// NOTE: p.send keeps its default (real net/smtp.SendMail).

	_, err := p.Do(context.Background(), "send_email", map[string]any{
		"to": "dev@example.com", "subject": "smoke", "body": "hello smtp",
	})
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(fake.from, "fleet@example.com") {
		t.Errorf("MAIL FROM = %q", fake.from)
	}
	if len(fake.to) != 1 || !strings.Contains(fake.to[0], "dev@example.com") {
		t.Errorf("RCPT TO = %v", fake.to)
	}
	if !strings.Contains(fake.data, "hello smtp") {
		t.Errorf("DATA = %q", fake.data)
	}
}
