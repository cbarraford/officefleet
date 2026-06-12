// Package email provides the Email integration plugin: the send_email action
// over stdlib net/smtp (STARTTLS negotiated automatically when the server
// advertises it). Inbound email (IMAP) is deferred (SP3b spec §9).
package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strconv"
	"strings"

	"github.com/cbarraford/office-fleet/internal/plugin"
)

func init() {
	plugin.Register(&EmailPlugin{})
}

// EmailPlugin sends plain-text email via SMTP.
type EmailPlugin struct {
	host     string
	port     string
	from     string
	username string
	password string
	// send is a test seam; defaults to smtp.SendMail.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

func (e *EmailPlugin) Name() string { return "email" }

func (e *EmailPlugin) EventSources() []plugin.EventSource { return nil }

func (e *EmailPlugin) Actions() []plugin.Action {
	return []plugin.Action{
		{Name: "send_email", Description: "Send a plain-text email (to is a comma-separated list)"},
	}
}

func (e *EmailPlugin) ConfigSchema() plugin.Schema {
	return plugin.Schema{
		"type": "object",
		"properties": map[string]any{
			"smtp_host":     map[string]any{"type": "string"},
			"smtp_port":     map[string]any{"type": "string", "default": "587"},
			"from":          map[string]any{"type": "string"},
			"smtp_username": map[string]any{"type": "string", "description": "defaults to from"},
		},
		"required": []string{"smtp_host", "from"},
	}
}

func (e *EmailPlugin) Init(_ context.Context, cfg map[string]any, secrets plugin.SecretLookup) error {
	host, _ := cfg["smtp_host"].(string)
	if host == "" {
		return fmt.Errorf("email: smtp_host is required")
	}
	from, _ := cfg["from"].(string)
	if from == "" {
		return fmt.Errorf("email: from is required")
	}
	if strings.ContainsAny(from, "\r\n") {
		return fmt.Errorf("email: from must not contain CR/LF")
	}
	e.host = host
	e.from = from
	e.port = "587"
	switch v := cfg["smtp_port"].(type) {
	case string:
		if v != "" {
			e.port = v
		}
	case int:
		if v > 0 {
			e.port = strconv.Itoa(v)
		}
	case float64:
		if v > 0 {
			e.port = strconv.Itoa(int(v))
		}
	}
	e.username = from
	if u, ok := cfg["smtp_username"].(string); ok && u != "" {
		e.username = u
	}
	pw, err := secrets("smtp_password")
	if err != nil && !plugin.IsSecretNotFound(err) {
		return fmt.Errorf("email: resolve secret smtp_password: %w", err)
	}
	e.password = pw // empty = unauthenticated relay
	if e.send == nil {
		e.send = smtp.SendMail
	}
	return nil
}

func (e *EmailPlugin) Do(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	switch action {
	case "send_email":
		return e.sendEmail(ctx, params)
	default:
		return nil, fmt.Errorf("email: unknown action %q", action)
	}
}

func (e *EmailPlugin) sendEmail(_ context.Context, params map[string]any) (map[string]any, error) {
	if e.send == nil {
		return nil, fmt.Errorf("email: plugin not initialized (check smtp_host/from in the email plugin config)")
	}
	toRaw, _ := params["to"].(string)
	subject, _ := params["subject"].(string)
	body, _ := params["body"].(string)
	if toRaw == "" || subject == "" || body == "" {
		return nil, fmt.Errorf("email send_email: to, subject, and body are required")
	}
	var recipients []string
	for _, addr := range strings.Split(toRaw, ",") {
		if a := strings.TrimSpace(addr); a != "" {
			recipients = append(recipients, a)
		}
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("email send_email: no valid recipients in %q", toRaw)
	}

	// Header-injection guard: subject and addresses are template-rendered
	// from event payloads (attacker-influenced); CR/LF would inject headers.
	if strings.ContainsAny(subject, "\r\n") {
		return nil, fmt.Errorf("email send_email: subject must not contain CR/LF")
	}
	for _, r := range recipients {
		if strings.ContainsAny(r, "\r\n") {
			return nil, fmt.Errorf("email send_email: recipient %q must not contain CR/LF", r)
		}
	}

	var auth smtp.Auth
	if e.password != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}
	msg := []byte("From: " + e.from + "\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" + body)

	if err := e.send(e.host+":"+e.port, auth, e.from, recipients, msg); err != nil {
		return nil, fmt.Errorf("email: send: %w", err)
	}
	return map[string]any{"recipients": len(recipients)}, nil
}
