package config

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/weather"
)

// Weather is on by default (it only ever does anything for a rider who has
// opted in with a town) and points at the public Open-Meteo unless told
// otherwise; an operator can switch it off or point it at their own instance.
func TestWeatherConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Weather.On() {
		t.Error("weather should default to on")
	}
	if cfg.Weather.BaseURL != weather.DefaultBaseURL {
		t.Errorf("base_url default = %q", cfg.Weather.BaseURL)
	}

	cfg, err = Load(writeConfig(t, "weather:\n  enabled: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Weather.On() {
		t.Error("enabled: false was ignored")
	}

	cfg, err = Load(writeConfig(t, "weather:\n  base_url: https://weather.example.org\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Weather.BaseURL != "https://weather.example.org" || !cfg.Weather.On() {
		t.Errorf("base_url override = %+v", cfg.Weather)
	}
}

func TestValidateRejectsAMalformedWeatherBaseURL(t *testing.T) {
	base := func() *Config {
		return &Config{
			Source:  SourceConfig{DSN: "data/domestique.db"},
			Weather: WeatherConfig{BaseURL: "https://weather.example.org"},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("a valid weather.base_url was rejected: %v", err)
	}
	for _, bad := range []string{"not a url at all", "weather.example.org", "https://"} {
		cfg := base()
		cfg.Weather.BaseURL = bad
		if err := cfg.Validate(); err == nil {
			t.Errorf("%q: expected an error, got none", bad)
		}
	}
	off := false
	cfg := base()
	cfg.Weather.Enabled = &off
	cfg.Weather.BaseURL = "not a url at all"
	if err := cfg.Validate(); err != nil {
		t.Errorf("a bad url while disabled should be ignored: %v", err)
	}
}
