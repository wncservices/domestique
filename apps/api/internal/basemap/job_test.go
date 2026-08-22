package basemap

import "testing"

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
