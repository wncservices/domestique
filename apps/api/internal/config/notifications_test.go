package config

import (
	"strings"
	"testing"
)

const smtpBase = "public_url: https://domestique.example.com\nnotifications:\n  smtp:\n"

func TestSMTPConfigFullBlock(t *testing.T) {
	cfg, err := Load(writeConfig(t, smtpBase+`    host: smtp.example.com
    port: 587
    security: starttls
    username: domestique
    from: "Domestique <domestique@example.com>"
`))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Notifications.SMTP
	if !s.Enabled() || s.Host != "smtp.example.com" || s.Port != 587 || s.Security != "starttls" || s.Username != "domestique" {
		t.Errorf("smtp = %+v", s)
	}
}

func TestSMTPConfigDefaults(t *testing.T) {
	cases := map[string]int{"starttls": 587, "tls": 465, "none": 25, "": 587}
	for security, port := range cases {
		host := "smtp.example.com"
		if security == "none" {
			host = "localhost" // an unencrypted hop is only allowed to this machine
		}
		body := smtpBase + "    host: " + host + "\n    from: d@example.com\n"
		if security != "" {
			body += "    security: " + security + "\n"
		}
		cfg, err := Load(writeConfig(t, body))
		if err != nil {
			t.Fatalf("%q: %v", security, err)
		}
		want := security
		if want == "" {
			want = "starttls"
		}
		if cfg.Notifications.SMTP.Security != want || cfg.Notifications.SMTP.Port != port {
			t.Errorf("%q: security %q port %d, want %s %d", security, cfg.Notifications.SMTP.Security, cfg.Notifications.SMTP.Port, want, port)
		}
	}
}

func TestSMTPConfigAbsentMeansOff(t *testing.T) {
	cfg, err := Load(writeConfig(t, "public_url: https://domestique.example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notifications.SMTP.Enabled() {
		t.Error("an absent block must leave email off")
	}
}

func TestSMTPConfigRejectsAPartialOrBadBlock(t *testing.T) {
	cases := map[string]string{
		"host without from":       smtpBase + "    host: smtp.example.com\n",
		"from not an address":     smtpBase + "    host: smtp.example.com\n    from: not-an-address\n",
		"unknown security":        smtpBase + "    host: smtp.example.com\n    from: d@example.com\n    security: ssl\n",
		"none to a remote host":   smtpBase + "    host: smtp.example.com\n    from: d@example.com\n    security: none\n",
		"none to a lookalike":     smtpBase + "    host: localhost.example.com\n    from: d@example.com\n    security: none\n",
		"none to a private IP":    smtpBase + "    host: 10.0.0.5\n    from: d@example.com\n    security: none\n",
		"bad port":                smtpBase + "    host: smtp.example.com\n    from: d@example.com\n    port: 70000\n",
		"fields without a host":   smtpBase + "    from: d@example.com\n    username: u\n",
		"smtp without public_url": "notifications:\n  smtp:\n    host: smtp.example.com\n    from: d@example.com\n",
	}
	for name, body := range cases {
		_, err := Load(writeConfig(t, body))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "smtp") && !strings.Contains(err.Error(), "public_url") {
			t.Errorf("%s: error does not say what is wrong: %v", name, err)
		}
	}
}

// The password never lives in the file: a key by that name is not read.
func TestSMTPConfigHasNoPasswordField(t *testing.T) {
	cfg, err := Load(writeConfig(t, smtpBase+"    host: smtp.example.com\n    from: d@example.com\n    password: hunter2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{cfg.Notifications.SMTP.Username, cfg.Notifications.SMTP.From, cfg.Notifications.SMTP.Host}, " ")), "hunter2") {
		t.Error("a password in the file was picked up")
	}
}

// An unencrypted hop is fine to a relay on this machine, whichever way
// loopback is spelled.
func TestSMTPConfigAcceptsSecurityNoneOnLoopback(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "127.0.0.2", `"::1"`} {
		body := smtpBase + "    host: " + host + "\n    from: d@example.com\n    security: none\n"
		if _, err := Load(writeConfig(t, body)); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
}
