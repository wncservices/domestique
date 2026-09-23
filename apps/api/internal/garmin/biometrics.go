package garmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Connect's biometric endpoints: the numbers a rider's own watch and Garmin
// account already hold — max heart rate, running lactate threshold, cycling
// FTP — which is what lets a profile be filled in without asking. Paths are
// from github.com/cyberjunky/python-garminconnect, the same reverse-engineered
// reference this package cites for Activities and Profile.
//
// None of the three has been exercised against a live account, and the
// reference documents their response shapes only loosely (the lactate
// endpoint is a list of near-identical dicts with a "hearRate" typo the
// reference itself works around). So every decode below is deliberately
// tolerant — array or single object, several spellings — and every value is
// range-checked, failing closed to 0: no reading, never a bogus one.
const (
	heartRateZonesPath   = "/biometric-service/heartRateZones"
	lactateThresholdPath = "/biometric-service/biometric/latestLactateThreshold"
	ftpRangePath         = "/biometric-service/stats/functionalThresholdPower/range"
)

// ftpLookback is how far back the FTP range query looks for the most recent
// reading. Garmin only records a new value when it detects or is given one,
// so a quiet spell is normal — a season, not a week.
const ftpLookback = 365 * 24 * time.Hour

// Biometrics is what Connect holds about the rider's own physiology. Zero
// means Connect had no usable reading for that field.
type Biometrics struct {
	// MaxHR is the max heart rate Connect uses for the rider's zones. Often
	// Garmin's own age-based default until the rider sets or tests one, which
	// is why the caller treats it as an estimate, not a confirmed value.
	MaxHR int
	// ThresholdPaceSecPerKM is running lactate-threshold pace.
	ThresholdPaceSecPerKM float64
	// CyclingFTPWatts is the rider's functional threshold power as Connect
	// has it — auto-detected by the watch or entered by the rider.
	CyclingFTPWatts float64
}

// Biometrics reads all three, best-effort and independently: a field whose
// endpoint failed stays 0, and the returned error joins every failure so the
// caller can log it — but partial results are still returned and still
// usable. Deliberately not all-or-nothing: one undocumented endpoint moving
// must not cost the rider the other two.
func (c *Client) Biometrics(ctx context.Context, now time.Time) (Biometrics, error) {
	var out Biometrics
	var errs []error

	if hr, err := c.maxHeartRate(ctx); err != nil {
		errs = append(errs, fmt.Errorf("heart rate zones: %w", err))
	} else {
		out.MaxHR = hr
	}
	if pace, err := c.thresholdPace(ctx); err != nil {
		errs = append(errs, fmt.Errorf("lactate threshold: %w", err))
	} else {
		out.ThresholdPaceSecPerKM = pace
	}
	if ftp, err := c.cyclingFTP(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("functional threshold power: %w", err))
	} else {
		out.CyclingFTPWatts = ftp
	}
	return out, errors.Join(errs...)
}

// getBiometric is one authenticated GET of a biometric-service path.
func (c *Client) getBiometric(ctx context.Context, path string) ([]byte, error) {
	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	raw, status, err := c.do(ctx, http.MethodGet, c.APIBase+path, nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("returned %d: %s", status, snippet(raw))
	}
	return raw, nil
}

func (c *Client) maxHeartRate(ctx context.Context) (int, error) {
	raw, err := c.getBiometric(ctx, heartRateZonesPath)
	if err != nil {
		return 0, err
	}
	objs, err := decodeObjects(raw)
	if err != nil {
		return 0, err
	}
	// Prefer the account-wide entry over a sport-specific one: max HR is a
	// property of the rider, and Connect lists per-sport zone sets beside a
	// DEFAULT one.
	best := 0
	for _, o := range objs {
		hr := int(number(o, "maxHeartRateUsed", "maxHeartRate"))
		if hr < minPlausibleMaxHR || hr > maxPlausibleMaxHR {
			continue
		}
		if sport, _ := o["sport"].(string); sport == "DEFAULT" {
			return hr, nil
		}
		if best == 0 {
			best = hr
		}
	}
	return best, nil
}

const (
	minPlausibleMaxHR = 120
	maxPlausibleMaxHR = 230
)

func (c *Client) thresholdPace(ctx context.Context) (float64, error) {
	raw, err := c.getBiometric(ctx, lactateThresholdPath)
	if err != nil {
		return 0, err
	}
	objs, err := decodeObjects(raw)
	if err != nil {
		return 0, err
	}
	for _, o := range objs {
		if pace := paceFromLactateSpeed(number(o, "speed")); pace > 0 {
			return pace, nil
		}
	}
	return 0, nil
}

// paceFromLactateSpeed turns Connect's lactate-threshold "speed" into
// seconds per kilometre, or 0 when it is not credible.
//
// The unit is the one thing here nobody has confirmed: values seen in the
// wild look like 0.36, which only makes sense as decametres per second
// (3.6 m/s), while a plain m/s value would be ≥ 1 for any runner. Anything
// under 1 is therefore scaled by 10, and the result must then land inside a
// range a runner could actually hold (about 2:15 to 11:00 per km) or it is
// discarded — a wrong guess at the unit costs a missing suggestion, never a
// wrong pace on a profile.
func paceFromLactateSpeed(speed float64) float64 {
	if speed <= 0 {
		return 0
	}
	if speed < 1 {
		speed *= 10
	}
	if speed < minPlausibleRunSpeed || speed > maxPlausibleRunSpeed {
		return 0
	}
	return 1000 / speed
}

const (
	minPlausibleRunSpeed = 1.5 // m/s, ~11:07 per km
	maxPlausibleRunSpeed = 7.5 // m/s, ~2:13 per km
)

func (c *Client) cyclingFTP(ctx context.Context, now time.Time) (float64, error) {
	path := fmt.Sprintf("%s/%s/%s?sport=CYCLING&aggregation=daily",
		ftpRangePath, now.Add(-ftpLookback).Format("2006-01-02"), now.Format("2006-01-02"))
	raw, err := c.getBiometric(ctx, path)
	if err != nil {
		return 0, err
	}
	objs, err := decodeObjects(raw)
	if err != nil {
		return 0, err
	}
	// The most recent reading wins; calendarDate is ISO so it sorts as text.
	var latest string
	var watts float64
	for _, o := range objs {
		w := number(o, "value", "functionalThresholdPower", "ftp")
		if w < minPlausibleFTP || w > maxPlausibleFTP {
			continue
		}
		date, _ := o["calendarDate"].(string)
		if date >= latest {
			latest, watts = date, w
		}
	}
	return watts, nil
}

const (
	minPlausibleFTP = 40.0
	maxPlausibleFTP = 700.0
)

// decodeObjects accepts a JSON array of objects or one bare object — the
// biometric endpoints have been seen returning both.
func decodeObjects(raw []byte) ([]map[string]any, error) {
	var many []map[string]any
	if err := json.Unmarshal(raw, &many); err == nil {
		return many, nil
	}
	var one map[string]any
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("unreadable response: %s", snippet(raw))
	}
	return []map[string]any{one}, nil
}

// number returns the first of keys holding a positive JSON number, so a
// field Garmin has spelled two ways (its own "hearRate" typo) is read
// whichever way this account's response spells it.
func number(o map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := o[k].(float64); ok && v > 0 {
			return v
		}
	}
	return 0
}
