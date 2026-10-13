package config

import "testing"

// Metrics are served on their own internal port, configured only through
// DOMESTIQUE_METRICS_ADDR: unset means :9090, set but empty means off.
func TestMetricsAddr(t *testing.T) {
	cfg := &Config{}

	t.Run("unset defaults to :9090", func(t *testing.T) {
		// t.Setenv registers the restore; Unsetenv then makes it genuinely unset.
		t.Setenv(EnvMetricsAddr, "")
		unset(t, EnvMetricsAddr)
		if got := cfg.MetricsAddr(); got != ":9090" {
			t.Fatalf("MetricsAddr = %q, want :9090", got)
		}
	})
	t.Run("set but empty turns it off", func(t *testing.T) {
		t.Setenv(EnvMetricsAddr, "")
		if got := cfg.MetricsAddr(); got != "" {
			t.Fatalf("MetricsAddr = %q, want off", got)
		}
	})
	t.Run("a value is used", func(t *testing.T) {
		t.Setenv(EnvMetricsAddr, "127.0.0.1:9100")
		if got := cfg.MetricsAddr(); got != "127.0.0.1:9100" {
			t.Fatalf("MetricsAddr = %q", got)
		}
	})
	t.Run("there is no yaml key", func(t *testing.T) {
		if _, err := Load(writeConfig(t, "metrics:\n  listen: \":9191\"\n")); err == nil {
			t.Log("an unknown key is tolerated by Load; it must still not change the address")
		}
		t.Setenv(EnvMetricsAddr, "")
		unset(t, EnvMetricsAddr)
		cfg, err := Load(writeConfig(t, "metrics:\n  listen: \":9191\"\n"))
		if err == nil && cfg.MetricsAddr() != ":9090" {
			t.Fatalf("a yaml key changed the address to %q", cfg.MetricsAddr())
		}
	})
}

func unset(t *testing.T, key string) {
	t.Helper()
	if err := osUnsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// `domestique validate` has to catch a bad address before a pod starts with
// metrics silently absent.
func TestValidateRejectsABadMetricsAddress(t *testing.T) {
	for _, bad := range []string{"9090", "nonsense", ":", ":99999", "host:port"} {
		t.Run("bad "+bad, func(t *testing.T) {
			t.Setenv(EnvMetricsAddr, bad)
			cfg, err := Load(writeConfig(t, "source:\n  dsn: x.db\n"))
			if err == nil {
				err = cfg.Validate()
			}
			if err == nil {
				t.Errorf("%s=%q was accepted", EnvMetricsAddr, bad)
			}
		})
	}
	for _, good := range []string{":9090", "127.0.0.1:9090", "[::1]:9090", ""} {
		t.Run("good "+good, func(t *testing.T) {
			t.Setenv(EnvMetricsAddr, good)
			cfg, err := Load(writeConfig(t, "source:\n  dsn: x.db\n"))
			if err != nil {
				t.Fatalf("%s=%q refused: %v", EnvMetricsAddr, good, err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("%s=%q refused: %v", EnvMetricsAddr, good, err)
			}
		})
	}
}
