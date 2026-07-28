// Tests for the release map and everything that reads it: name resolution,
// the "one release below" step, and the samples the CLI help prints. Adding a
// Premiere release to releases.go is expected to move the values these assert.

package premdown

import (
	"strings"
	"testing"
)

func TestReleaseNamesNewestFirst(t *testing.T) {
	got := releaseNames()
	names := strings.Split(got, ", ")
	if names[0] != "2026" {
		t.Errorf("releaseNames should list newest first, got first = %q", names[0])
	}
	if names[len(names)-1] != "CS4" {
		t.Errorf("releaseNames should list oldest last, got last = %q", names[len(names)-1])
	}
	if len(names) != len(releases) {
		t.Errorf("releaseNames listed %d names, want %d", len(names), len(releases))
	}
}

func TestReleaseExamples(t *testing.T) {
	got := ReleaseExamples()
	// The two releases just below the newest (2026), single-quoted, "..."-trailed.
	if !strings.HasSuffix(got, "...") {
		t.Errorf("ReleaseExamples should end with ..., got %q", got)
	}
	for _, want := range []string{"'2025'", "'2024'"} {
		if !strings.Contains(got, want) {
			t.Errorf("ReleaseExamples = %q, missing %s", got, want)
		}
	}
	// The newest release itself is skipped (examples are for downgrade targets).
	if strings.Contains(got, "'2026'") {
		t.Errorf("ReleaseExamples should skip the newest release, got %q", got)
	}
}

func TestResolveRelease(t *testing.T) {
	cases := map[string]int{
		"2026":    45,
		"2025":    43,
		"CS4":     22,
		"cs4":     22, // case-insensitive
		"  2025 ": 43, //nolint:gocritic // mapKey: intentional whitespace, exercises ResolveRelease's trimming
		"cc2014":  27, // alias, case-insensitive
		"CC2014":  27,
	}
	for name, want := range cases {
		if got, err := ResolveRelease(name); err != nil || got != want {
			t.Errorf("ResolveRelease(%q) = %d, %v; want %d, nil", name, got, err, want)
		}
	}
}

func TestResolveReleaseUnknownErrors(t *testing.T) {
	_, err := ResolveRelease("NoSuchRelease")
	if err == nil {
		t.Fatal("unknown release should return an error")
	}
	if !strings.Contains(err.Error(), "unknown release") {
		t.Errorf("missing diagnostic: %v", err)
	}
}

func TestPreviousRelease(t *testing.T) {
	cases := []struct {
		v        int
		wantXML  int
		wantName string
		wantOK   bool
	}{
		{45, 43, "2025", true},   // 2026 -> 2025 (skips the absent v44)
		{32, 30, "2015.1", true}, // 2017 -> 2015.1 (skips the absent v31)
		{23, 22, "CS4", true},    // one step down lands on the oldest
		{22, 0, "", false},       // nothing below the oldest known release
	}
	for _, c := range cases {
		gotXML, gotName, gotOK := previousRelease(c.v)
		if gotXML != c.wantXML || gotName != c.wantName || gotOK != c.wantOK {
			t.Errorf("previousRelease(%d) = (%d, %q, %v), want (%d, %q, %v)",
				c.v, gotXML, gotName, gotOK, c.wantXML, c.wantName, c.wantOK)
		}
	}
}
