package weather

import (
	"strings"
	"unicode"
)

// TownLabel reduces whatever a client sent as a place name to a town-level
// label, so a street address a rider happened to search for is never stored.
//
// The geocoder (Nominatim's display_name) returns one comma-separated string,
// most specific part first, and no address components, so this is a heuristic
// on that string:
//
//  1. Drop every segment that contains a digit: house numbers, postcodes and
//     "12 Kerkstraat" or "Rue de la Loi 16" alike.
//  2. Drop segments that look like a street (a Dutch or German street suffix
//     such as -straat, -laan, -weg, or a word such as street, avenue, rue).
//  3. Of what is left, keep the first (the settlement) and the last (the
//     country), which turns "Ghent, East Flanders, Flanders, Belgium" into
//     "Ghent, Belgium". One or two segments are kept as they are.
//
// Known limit: a venue or suburb name with no digit and no street word (a
// cafe, a district) can survive as the first segment. The coordinates are
// rounded to about 1 km regardless; this is about the text. The result is empty
// when nothing usable is left. The client's value is untrusted, so the store
// applies this on every write.
func TownLabel(place string) string {
	var kept []string
	for _, seg := range strings.Split(place, ",") {
		seg = strings.TrimSpace(seg)
		if seg == "" || strings.IndexFunc(seg, unicode.IsDigit) >= 0 || looksLikeStreet(seg) {
			continue
		}
		kept = append(kept, seg)
	}
	switch {
	case len(kept) == 0:
		return ""
	case len(kept) <= 2:
		return strings.Join(kept, ", ")
	}
	return kept[0] + ", " + kept[len(kept)-1]
}

var streetSuffixes = []string{"straat", "laan", "weg", "dreef", "plein", "gracht", "straße", "strasse"}

var streetWords = map[string]bool{
	"street": true, "road": true, "avenue": true, "lane": true, "drive": true,
	"boulevard": true, "rue": true, "via": true, "calle": true, "avenida": true,
	"rua": true, "platz": true, "chaussée": true,
}

func looksLikeStreet(seg string) bool {
	lower := strings.ToLower(seg)
	for _, w := range strings.FieldsFunc(lower, func(r rune) bool { return unicode.IsSpace(r) || r == '.' }) {
		if streetWords[w] {
			return true
		}
		for _, s := range streetSuffixes {
			if strings.HasSuffix(w, s) {
				return true
			}
		}
	}
	return false
}
