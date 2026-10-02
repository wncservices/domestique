package secrets

import (
	"strings"
	"testing"
)

// MAC is how a table can hold a findable-but-not-readable stand-in for a
// value: the same input gives the same output under one key, a different
// output under another, and a different purpose is a different key, so two
// tables cannot be joined on it.
func TestMACIsKeyedAndPurposeBound(t *testing.T) {
	a, b := testBox(t), testBox(t)

	first, again := a.MAC("p", "wilant"), a.MAC("p", "wilant")
	if first != again {
		t.Error("MAC is not deterministic")
	}
	if a.MAC("p", "wilant") == a.MAC("p", "friend") {
		t.Error("different values share a MAC")
	}
	if a.MAC("p", "wilant") == a.MAC("q", "wilant") {
		t.Error("different purposes share a MAC")
	}
	if a.MAC("p", "wilant") == b.MAC("p", "wilant") {
		t.Error("different keys share a MAC")
	}
	if strings.Contains(a.MAC("p", "wilant"), "wilant") {
		t.Error("MAC contains the value")
	}
	var nilBox *Box
	if nilBox.MAC("p", "wilant") != "" {
		t.Error("nil box should yield an empty MAC")
	}
}
