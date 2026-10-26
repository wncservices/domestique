package llm

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNumbersInFindsDutchDecimals(t *testing.T) {
	got := numbersIn("1,5 uur en 20 min")
	if !slices.Equal(got, []string{"1.5", "20"}) {
		t.Errorf("numbersIn = %q, want [1.5 20]", got)
	}
}

func TestNumbersInFindsPointDecimalsAndDates(t *testing.T) {
	got := numbersIn("Ride 1.5 h on 2026-10-27.")
	if !slices.Equal(got, []string{"1.5", "2026", "10", "27"}) {
		t.Errorf("numbersIn = %q", got)
	}
}

func TestNumbersInIgnoresASentenceEndingAfterANumber(t *testing.T) {
	got := numbersIn("It was 20. Then rest.")
	if !slices.Equal(got, []string{"20"}) {
		t.Errorf("numbersIn = %q, want [20]", got)
	}
}

func TestCheckProseAcceptsNumbersFromTheInput(t *testing.T) {
	input := "Target 320 TSS, ridden 290 TSS."
	if err := checkProse("You rode 290 TSS against 320.", input, 5, 700); err != nil {
		t.Errorf("checkProse: %v", err)
	}
}

func TestCheckProseRejectsAnInventedNumber(t *testing.T) {
	err := checkProse("You rode 25 minutes.", "Ride of 20 minutes.", 5, 700)
	if !errors.Is(err, errInventedNumber) {
		t.Errorf("err = %v, want errInventedNumber", err)
	}
}

func TestCheckProseRejectsEmptyText(t *testing.T) {
	if err := checkProse("   ", "", 3, 300); err == nil {
		t.Error("expected an error for blank text")
	}
}

func TestCheckProseLimitsSentencesAndRunes(t *testing.T) {
	if err := checkProse("One. Two. Three.", "", 3, 300); err != nil {
		t.Errorf("three sentences: %v", err)
	}
	if err := checkProse("One. Two. Three. Four.", "", 3, 300); err == nil {
		t.Error("four sentences: expected an error")
	}
	if err := checkProse(strings.Repeat("é", 301), "", 3, 300); err == nil {
		t.Error("301 runes: expected an error")
	}
	if err := checkProse(strings.Repeat("é", 300), "", 3, 300); err != nil {
		t.Errorf("300 runes: %v", err)
	}
}

func TestCountSentencesKeepsDecimalsWhole(t *testing.T) {
	if n := countSentences("Ride 1.5 hours. Rest after."); n != 2 {
		t.Errorf("countSentences = %d, want 2", n)
	}
}
