package config

import (
	"fmt"
	"net"
	"net/mail"
	"strings"
)

// SMTP security modes.
const (
	SMTPStartTLS = "starttls" // plain connect, then upgrade; the default, port 587
	SMTPTLS      = "tls"      // implicit TLS from the first byte, port 465
	SMTPNone     = "none"     // no encryption: a relay on loopback only
)

// NotificationsConfig is how the deployment sends mail. Admin-set here and in
// the environment, not in the settings UI: config is the default, and the UI is
// for a first-run deployment that would otherwise have a dead button.
type NotificationsConfig struct {
	SMTP SMTPConfig `yaml:"smtp,omitempty"`
}

// SMTPConfig is the notifications.smtp block. The password is deliberately not
// a field: it comes from DOMESTIQUE_SMTP_PASSWORD (mailer.EnvPassword), the same
// rule every other credential follows, so a config file meant to be readable
// never holds one.
type SMTPConfig struct {
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	Security string `yaml:"security,omitempty"`
	Username string `yaml:"username,omitempty"`
	// From is the sender, "Domestique <domestique@example.com>" or a bare address.
	From string `yaml:"from,omitempty"`
}

// Enabled is whether email is configured. No host means the feature is off:
// the profile card is hidden and nothing is sent.
func (s SMTPConfig) Enabled() bool { return s.Host != "" }

func (s *SMTPConfig) applyDefaults() {
	s.Host = strings.TrimSpace(s.Host)
	if s.Host == "" {
		return
	}
	if s.Security == "" {
		s.Security = SMTPStartTLS
	}
	if s.Port == 0 {
		switch s.Security {
		case SMTPTLS:
			s.Port = 465
		case SMTPNone:
			s.Port = 25
		default:
			s.Port = 587
		}
	}
}

// validate rejects a partial block rather than let it fail at 06:30: a host
// without a sender, an unknown security value, or mail with no public_url to
// put in its links.
func (s SMTPConfig) validate(publicURL string) error {
	if s.Host == "" {
		if s.Port != 0 || s.Security != "" || s.Username != "" || s.From != "" {
			return fmt.Errorf("notifications.smtp has settings but no host: set host, or remove the block")
		}
		return nil
	}
	if strings.TrimSpace(s.From) == "" {
		return fmt.Errorf("notifications.smtp.host is set but notifications.smtp.from is not: nobody to send as")
	}
	if _, err := mail.ParseAddress(s.From); err != nil {
		return fmt.Errorf("notifications.smtp.from %q is not an email address", s.From)
	}
	switch s.Security {
	case SMTPStartTLS, SMTPTLS, SMTPNone:
	default:
		return fmt.Errorf("notifications.smtp.security %q is not one of starttls, tls, none", s.Security)
	}
	if s.Security == SMTPNone && !isLoopbackHost(s.Host) {
		return fmt.Errorf("notifications.smtp.security is none but host %q is not loopback: an unencrypted hop is only for a relay on this machine; use starttls or tls", s.Host)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("notifications.smtp.port %d is not a valid port", s.Port)
	}
	if publicURL == "" {
		return fmt.Errorf("notifications.smtp is set but public_url is not: the email's links need the address riders reach this deployment at")
	}
	return nil
}

// isLoopbackHost is whether host is this machine: "localhost" or a loopback
// address, judged by its spelling (no DNS lookup).
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
