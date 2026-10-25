package llm

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// This file holds the checks every free-text job shares: the text is not
// empty, is within its sentence and rune limits, and invents no number. A
// number is any digit run with an optional . or , decimal, so the Dutch "1,5"
// and the English "1.5" are the same figure.

var (
	numberPattern     = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
	sentenceBreak     = regexp.MustCompile(`[.!?]+(?:\s+|$)`)
	errInventedNumber = errors.New("llm: the text states a number the input does not contain")
)

// numbersIn returns every number in text, decimals normalised to ".".
func numbersIn(text string) []string {
	found := numberPattern.FindAllString(text, -1)
	for i, n := range found {
		found[i] = strings.Replace(n, ",", ".", 1)
	}
	return found
}

// countSentences counts sentences as the runs of text between a . ! or ?
// that is followed by a space or the end. A decimal point is not a break.
func countSentences(text string) int {
	n := 0
	for _, part := range sentenceBreak.Split(text, -1) {
		if strings.TrimSpace(part) != "" {
			n++
		}
	}
	return n
}

// checkProse reports whether text may be shown: non-empty, at most maxSentences
// sentences and maxRunes runes, and every number it states appears in input,
// the prompt the model was given.
func checkProse(text, input string, maxSentences, maxRunes int) error {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return errors.New("llm: the text is empty")
	case utf8.RuneCountInString(text) > maxRunes:
		return fmt.Errorf("llm: the text is over %d runes", maxRunes)
	case countSentences(text) > maxSentences:
		return fmt.Errorf("llm: the text is over %d sentences", maxSentences)
	}
	given := numbersIn(input)
	for _, n := range numbersIn(text) {
		if !slices.Contains(given, n) {
			return errInventedNumber
		}
	}
	return nil
}

// proseSchema is the reply shape of a text job: one string, capped in runes.
func proseSchema(maxRunes int) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "maxLength": maxRunes},
		},
		"required":             []string{"text"},
		"additionalProperties": false,
	}
}
