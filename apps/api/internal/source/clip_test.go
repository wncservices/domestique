package source

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// An importer (Garmin, Wahoo, Komoot, a share copy, a workout route) hands
// Create text it did not write. Refusing it would fail the whole import, so
// Create clips it to the same caps instead.
func TestCreateClipsImportedTextInsteadOfRefusing(t *testing.T) {
	db := openTestDB(t)

	tags := make([]string, 31)
	for i := range tags {
		tags[i] = fmt.Sprintf("t%d", i)
	}
	tags[0] = strings.Repeat("x", 60)

	route, err := db.Create(t.Context(), CreateRequest{
		Name:     strings.Repeat("é", 250),
		Descript: strings.Repeat("d", 5001),
		Tags:     tags,
		GPX:      exampleGPX(t),
	})
	if err != nil {
		t.Fatalf("create with over-long text: %v, want it clipped", err)
	}
	if n := utf8.RuneCountInString(route.Name); n != MaxNameChars {
		t.Errorf("name = %d runes, want %d", n, MaxNameChars)
	}
	if n := utf8.RuneCountInString(route.Description); n != MaxDescriptionChars {
		t.Errorf("description = %d runes, want %d", n, MaxDescriptionChars)
	}
	if len(route.Tags) != MaxTags {
		t.Errorf("tags = %d, want %d", len(route.Tags), MaxTags)
	}
	if n := utf8.RuneCountInString(route.Tags[0]); n != MaxTagChars {
		t.Errorf("first tag = %d runes, want %d", n, MaxTagChars)
	}
}

func TestClipTextCountsRunesAndKeepsWithinCaps(t *testing.T) {
	name, desc, tags := ClipText(strings.Repeat("é", 201), strings.Repeat("d", 10), []string{"ok", strings.Repeat("x", 51)})
	if n := utf8.RuneCountInString(name); n != 200 {
		t.Errorf("name = %d runes, want 200", n)
	}
	if desc != strings.Repeat("d", 10) {
		t.Errorf("description changed inside its cap: %q", desc)
	}
	if len(tags) != 2 || tags[0] != "ok" || utf8.RuneCountInString(tags[1]) != 50 {
		t.Errorf("tags = %q, want [ok and a 50-rune tag]", tags)
	}
	if err := ValidateText(name, desc, tags); err != nil {
		t.Errorf("clipped text still refused: %v", err)
	}
}

// A route stored before the caps can be over them. Editing its other fields
// must still work; only a field the rider sets is checked.
func TestUpdateChecksOnlyTheFieldsItSetOnALegacyRoute(t *testing.T) {
	db := openTestDB(t)
	route, err := db.Create(t.Context(), CreateRequest{Name: "Legacy", GPX: exampleGPX(t)})
	if err != nil {
		t.Fatal(err)
	}
	legacyName := strings.Repeat("a", 250)
	legacyTags := make([]string, 31)
	for i := range legacyTags {
		legacyTags[i] = fmt.Sprintf("t%d", i)
	}
	if _, err := db.db.ExecContext(t.Context(), db.query(`UPDATE routes SET name=?, tags=? WHERE slug=?`),
		legacyName, joinList(legacyTags), route.Slug); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Update(t.Context(), route.Slug, UpdateRequest{Tags: &[]string{"fresh"}}); err != nil {
		t.Errorf("retagging a legacy route: %v, want it to work", err)
	}
	if _, err := db.Update(t.Context(), route.Slug, UpdateRequest{Enabled: new(bool)}); err != nil {
		t.Errorf("disabling a legacy route: %v, want it to work", err)
	}

	long := strings.Repeat("a", 201)
	if _, err := db.Update(t.Context(), route.Slug, UpdateRequest{Name: &long}); !errors.Is(err, ErrInvalid) {
		t.Errorf("renaming to 201 characters: err = %v, want ErrInvalid", err)
	}
}
