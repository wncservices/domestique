package weather

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// Thresholds for "bad weather to ride outside", defaults for a road rider.
// Each has its reasoning beside it; they are constants, easy to retune.
const (
	// Rain needs likelihood and amount together. Probability alone flags
	// dry-but-uncertain cloud; amount alone ignores how likely it is.
	rainProbBad  = 60.0 // percent, window maximum
	rainTotalBad = 0.2  // mm, window total
	// 1 mm an hour is steady rain (the UK Met Office calls 0.5 to 4 mm/h
	// moderate), bad whatever the probability says.
	rainHourBad = 1.0 // mm in a single hour

	// Beaufort 5 (fresh breeze, 29 to 38 km/h) is where a long ride is a grind.
	windBad = 30.0 // km/h sustained at 10 m
	// Gusts of 50 km/h and up (Beaufort 7) are a handling risk.
	gustBad = 50.0 // km/h

	// Ice on wet roads and numb hands start around here.
	coldBad = 3.0 // deg C, strictly below
	// Heat stress on a long effort; the trainer is not cool either, but a fan is.
	heatBad = 33.0 // deg C, strictly above

	// A ride is never worth planning past six hours of forecast, and the day
	// ends at 24.
	maxWindowHours = 6
)

// Verdict codes, by rule. Reasons and Codes are parallel and in table order.
const (
	CodeRain    = "rain"
	CodeWind    = "wind"
	CodeCold    = "cold"
	CodeHeat    = "heat"
	CodeThunder = "thunder"
	CodeWintry  = "wintry"
)

// Window is the hours of one local date a ride covers: StartHour inclusive,
// EndHour exclusive.
type Window struct {
	Date               string
	StartHour, EndHour int
}

// Verdict is Assess's answer. The four numbers summarise the window for a chip.
type Verdict struct {
	Bad                                    bool
	Reasons                                []string
	Codes                                  []string
	TempMin, TempMax, RainProbMax, GustMax float64
}

func isThunder(code int) bool { return code == 95 || code == 96 || code == 99 }

// isWintry: freezing drizzle and rain (56, 57, 66, 67), snowfall and snow
// grains (71 to 77), snow showers (85, 86).
func isWintry(code int) bool {
	switch {
	case code == 56 || code == 57 || code == 66 || code == 67:
		return true
	case code >= 71 && code <= 77:
		return true
	case code == 85 || code == 86:
		return true
	}
	return false
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func num(v float64) string { return strconv.FormatFloat(round1(v), 'f', -1, 64) }

// Assess judges the window against the thresholds. Pure: no clock, no zone.
// An empty window, or one whose date the forecast does not cover, is not bad.
func Assess(f Forecast, w Window) Verdict {
	var v Verdict
	first := true
	var totalRain, maxHourRain, maxWind float64
	var thunder, wintry bool
	for _, h := range f.Hours {
		if h.Date != w.Date || h.Hour < w.StartHour || h.Hour >= w.EndHour {
			continue
		}
		if first {
			v.TempMin, v.TempMax = h.Temp, h.Temp
			first = false
		}
		v.TempMin = math.Min(v.TempMin, h.Temp)
		v.TempMax = math.Max(v.TempMax, h.Temp)
		v.RainProbMax = math.Max(v.RainProbMax, h.RainProb)
		v.GustMax = math.Max(v.GustMax, h.Gust)
		maxWind = math.Max(maxWind, h.Wind)
		maxHourRain = math.Max(maxHourRain, h.Rain)
		totalRain += h.Rain
		thunder = thunder || isThunder(h.Code)
		wintry = wintry || isWintry(h.Code)
	}
	if first {
		return v
	}
	// Open-Meteo reports tenths of a millimetre; rounding the sum stops float
	// drift (0.1 + 0.1 + 0.1) from deciding a boundary.
	totalRain = math.Round(totalRain*100) / 100

	add := func(code, reason string) {
		v.Bad = true
		v.Codes = append(v.Codes, code)
		v.Reasons = append(v.Reasons, reason)
	}
	switch {
	case v.RainProbMax >= rainProbBad && totalRain >= rainTotalBad:
		add(CodeRain, fmt.Sprintf("Rain likely, %s%% and %s mm between %d and %d",
			num(v.RainProbMax), num(totalRain), w.StartHour, w.EndHour))
	case maxHourRain >= rainHourBad:
		add(CodeRain, fmt.Sprintf("Heavy rain, up to %s mm an hour between %d and %d",
			num(maxHourRain), w.StartHour, w.EndHour))
	}
	switch {
	case v.GustMax >= gustBad:
		add(CodeWind, fmt.Sprintf("Gusts up to %s km/h", num(v.GustMax)))
	case maxWind >= windBad:
		add(CodeWind, fmt.Sprintf("Wind up to %s km/h", num(maxWind)))
	}
	if v.TempMin < coldBad {
		add(CodeCold, fmt.Sprintf("Cold, down to %s °C", num(v.TempMin)))
	}
	if v.TempMax > heatBad {
		add(CodeHeat, fmt.Sprintf("Hot, up to %s °C", num(v.TempMax)))
	}
	if thunder {
		add(CodeThunder, "Thunderstorms forecast")
	}
	if wintry {
		add(CodeWintry, "Snow or ice forecast")
	}
	return v
}

// WindowFor returns the end hour of a ride window: the configured end, or the
// start plus the planned duration rounded up to whole hours when that is later
// (a 4-hour ride is judged on four hours, not a 3-hour slice), never more than
// six hours from the start nor past the end of the day.
func WindowFor(start, end int, plannedSeconds float64) int {
	if hours := int(math.Ceil(plannedSeconds / 3600)); start+hours > end {
		end = start + hours
	}
	if end > start+maxWindowHours {
		end = start + maxWindowHours
	}
	if end > 24 {
		end = 24
	}
	return end
}

// ClipToNow drops hours that have already passed from a window on today's date,
// by the forecast's own local time (now shifted by its UTC offset), so a
// morning window is not judged on rain that already fell. A window on an
// earlier date comes back empty; other dates are untouched.
func ClipToNow(w Window, f Forecast, now time.Time) Window {
	local := now.UTC().Add(time.Duration(f.UTCOffsetSeconds) * time.Second)
	today := local.Format("2006-01-02")
	switch {
	case w.Date < today:
		w.StartHour = w.EndHour
	case w.Date == today && local.Hour() > w.StartHour:
		w.StartHour = local.Hour()
	}
	return w
}

// severity orders the codes worst first, for choosing a chip icon.
var severity = []string{CodeThunder, CodeWintry, CodeRain, CodeWind, CodeCold, CodeHeat}

// WorstCode returns the most severe of the codes, or "" for none.
func WorstCode(codes []string) string {
	for _, s := range severity {
		for _, c := range codes {
			if c == s {
				return s
			}
		}
	}
	return ""
}
