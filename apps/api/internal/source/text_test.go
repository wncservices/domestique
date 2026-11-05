package source

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateTextCountsCharactersNotBytes(t *testing.T) {
	// 200 two-byte runes is 400 bytes: the cap is on characters.
	if err := ValidateText(strings.Repeat("é", 200), "", nil); err != nil {
		t.Errorf("200 characters refused: %v", err)
	}
	if err := ValidateText(strings.Repeat("é", 201), "", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("201 characters: err = %v, want ErrInvalid", err)
	}
}

func TestValidateTextRefusesEachCapWithItsMessage(t *testing.T) {
	cases := map[string]struct {
		name, desc string
		tags       []string
		want       string
	}{
		"name":        {name: strings.Repeat("a", 201), want: "Name is too long (max 200 characters)."},
		"description": {desc: strings.Repeat("d", 5001), want: "Description is too long (max 5000 characters)."},
		"tag count":   {tags: make([]string, 31), want: "Too many tags (max 30)."},
		"tag length":  {tags: []string{strings.Repeat("x", 51)}, want: "A tag is too long (max 50 characters)."},
	}
	for label, c := range cases {
		err := ValidateText(c.name, c.desc, c.tags)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", label, err)
			continue
		}
		if err.Error() != c.want {
			t.Errorf("%s: message = %q, want %q", label, err.Error(), c.want)
		}
	}
}
