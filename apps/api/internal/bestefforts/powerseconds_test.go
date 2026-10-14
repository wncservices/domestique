package bestefforts

import "testing"

func TestPowerSecondsIsTheWindowOfAPowerMetric(t *testing.T) {
	for key, want := range map[string]int{"power_5": 5, "power_1200": 1200, "power_3600": 3600} {
		if got, ok := PowerSeconds(key); !ok || got != want {
			t.Errorf("%s = %d %v, want %d", key, got, ok, want)
		}
	}
	for _, key := range []string{"max_hr", "speed_1200", "power_", "power_x", "power_0", "power_-5", ""} {
		if got, ok := PowerSeconds(key); ok {
			t.Errorf("%q = %d, want not a power metric", key, got)
		}
	}
}
