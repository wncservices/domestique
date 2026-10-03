package config

import "testing"

func TestPublicURL(t *testing.T) {
	cfg, err := Load(writeConfig(t, "public_url: https://domestique.example.com/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://domestique.example.com" {
		t.Errorf("PublicURL = %q, want the trailing slash trimmed", cfg.PublicURL)
	}
	if cfg, err = Load(writeConfig(t, "")); err != nil || cfg.PublicURL != "" {
		t.Errorf("an absent public_url is fine and empty: %q, %v", cfg.PublicURL, err)
	}
	for _, bad := range []string{"domestique.example.com", "ftp://x.example.com", "https://", "https://x.example.com/?a=b"} {
		if _, err := Load(writeConfig(t, "public_url: "+bad+"\n")); err == nil {
			t.Errorf("public_url %q was accepted", bad)
		}
	}
}
