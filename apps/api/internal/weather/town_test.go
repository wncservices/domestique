package weather

import (
	"strings"
	"testing"
	"unicode"
)

func TestTownLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Gent", "Gent"},
		{"Gent, België", "Gent, België"},
		{"  Ghent  ", "Ghent"},
		// The geocoder's own town results: settlement and country.
		{"Ghent, East Flanders, Flanders, Belgium", "Ghent, Belgium"},
		// Street addresses: everything below the settlement goes.
		{"12 Kerkstraat, Gent, Oost-Vlaanderen, België", "Gent, België"},
		{"Kerkstraat 12, 9000, Gent, Oost-Vlaanderen, België", "Gent, België"},
		{"Kerkstraat, Gent, Oost-Vlaanderen, België", "Gent, België"},
		{"221B, Baker Street, Marylebone, London, England, NW1 6XE, United Kingdom", "Marylebone, United Kingdom"},
		{"Rue de la Loi 16, Bruxelles, 1000, Belgique", "Bruxelles, Belgique"},
	}
	for _, tc := range cases {
		if got := TownLabel(tc.in); got != tc.want {
			t.Errorf("TownLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTownLabelNeverKeepsADigitOrStreet(t *testing.T) {
	inputs := []string{
		"12 Kerkstraat, Gent, Oost-Vlaanderen, België",
		"Kerkstraat 12, Gent",
		"5 Main Street, Springfield, USA",
		"9000",
		"12",
		"1600 Pennsylvania Avenue NW, Washington, DC 20500, United States",
	}
	for _, in := range inputs {
		got := TownLabel(in)
		if strings.IndexFunc(got, unicode.IsDigit) >= 0 {
			t.Errorf("TownLabel(%q) = %q keeps a digit", in, got)
		}
		if strings.Contains(strings.ToLower(got), "straat") || strings.Contains(strings.ToLower(got), "street") || strings.Contains(strings.ToLower(got), "avenue") {
			t.Errorf("TownLabel(%q) = %q keeps a street", in, got)
		}
	}
}

func TestTownLabelOfNothingUsableIsEmpty(t *testing.T) {
	for _, in := range []string{"", "  ", ",,", "12", "12, 9000"} {
		if got := TownLabel(in); got != "" {
			t.Errorf("TownLabel(%q) = %q, want empty", in, got)
		}
	}
}
