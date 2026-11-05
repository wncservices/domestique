package basemap

import (
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestParseProgress(t *testing.T) {
	cases := []struct {
		name        string
		log         string
		wantPercent int
		wantOK      bool
	}{
		{
			name:   "no progress line yet",
			log:    "",
			wantOK: false,
		},
		{
			name:   "unrelated log content",
			log:    "2026/08/22 21:42:13 INFO starting up\n",
			wantOK: false,
		},
		{
			name:        "a single update",
			log:         "fetching chunks  20% |████                | (1.8/8.7 GB, 14 MB/s) [1m25s:8m24s]",
			wantPercent: 20,
			wantOK:      true,
		},
		{
			// go-pmtiles overwrites the same terminal line with carriage
			// returns between updates, not newlines — this is what a raw
			// log dump of several updates in a row actually looks like,
			// captured from the real binary.
			name: "multiple carriage-return-separated updates, takes the last",
			log: "fetching chunks   0% |                                  | (0 B/8.7 GB) [0s:0s]" +
				"\rfetching chunks  46% |█████████           | (4.1/8.7 GB, 25 MB/s) [3m22s:3m10s]" +
				"\rfetching chunks  99% |███████████████████ | (26/27 MB, 8.1 MB/s) [3s:0s]" +
				"\r                                                                               " +
				"\rfetching chunks 100% |████████████████████| (27/27 MB, 7.9 MB/s)\n" +
				"2026/08/22 20:08:38 extract.go:606: Completed in 6.2s with 4 download threads.\n",
			wantPercent: 100,
			wantOK:      true,
		},
		{
			// The final completion line has no percentage in it at all —
			// confirms the parse doesn't get confused by log lines after
			// the last real progress update and somehow return something
			// stale or wrong.
			name: "trailing non-progress lines after completion",
			log: "fetching chunks  75% |███████████████     | (6.5/8.7 GB, 20 MB/s) [4m0s:1m0s]\n" +
				"2026/08/22 20:08:38 extract.go:611: Extract required 28 total requests.\n",
			wantPercent: 75,
			wantOK:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			percent, ok := parseProgress([]byte(tc.log))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && percent != tc.wantPercent {
				t.Errorf("percent = %d, want %d", percent, tc.wantPercent)
			}
		})
	}
}

// A bad quantity in the config must come back as an error from Trigger, before
// any Job is created. It used to be a resource.MustParse panic, which took the
// request down after the caller had already recorded the update as pending, so
// every later update was refused with 409 until a restart.
func TestTriggerRefusesABadQuantityWithoutCreatingAJob(t *testing.T) {
	for name, cfg := range map[string]JobConfig{
		"memory limit": {MemLimit: "lots", MemRequest: "256Mi", CPURequest: "250m"},
		"cpu request":  {MemLimit: "1Gi", MemRequest: "256Mi", CPURequest: "250 millicores"},
		"empty":        {MemLimit: "", MemRequest: "256Mi", CPURequest: "250m"},
	} {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			c := &Client{clientset: cs, cfg: cfg}
			job, err := c.Trigger(t.Context(), BBox{West: 1, South: 1, East: 2, North: 2}, 14, "20260101")
			if err == nil {
				t.Fatalf("Trigger returned %q with no error for a bad quantity", job)
			}
			if !strings.Contains(err.Error(), "basemap") {
				t.Errorf("error %q does not say which setting is wrong", err)
			}
			if n := len(cs.Actions()); n != 0 {
				t.Errorf("%d cluster call(s) made for a job that cannot be built", n)
			}
		})
	}
}
