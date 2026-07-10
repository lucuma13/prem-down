package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A VideoComponentParam as 2026 writes it when both bounds sit at their
// per-parameter defaults: the LowerBound/UpperBound children are dropped. 2025
// requires them present, so reconstruction must re-insert the false/true
// sentinels (which 2025 then repopulates with the real bounds on load).
const sparseVideoComponentParam = `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<ParameterID>1</ParameterID>
	<StartKeyframe>0,true,0,0,0,0,0,0</StartKeyframe>
</VideoComponentParam>`

func TestRebuildInsertsMissingBounds(t *testing.T) {
	out, stats := reconstructPositionalClasses(sparseVideoComponentParam)
	for _, field := range reconstructFieldsByTag["VideoComponentParam"] {
		want := "<" + field + ">" + reconstructDefaults[fieldKey{"VideoComponentParam", field}] + "</" + field + ">"
		if !strings.Contains(out, want) {
			t.Errorf("missing inserted field %s: output\n%s", field, out)
		}
		if stats[fieldKey{"VideoComponentParam", field}] != 1 {
			t.Errorf("stats for %s = %d, want 1", field, stats[fieldKey{"VideoComponentParam", field}])
		}
	}
	// Existing fields and indentation are untouched.
	if !strings.Contains(out, "<ParameterID>1</ParameterID>") {
		t.Errorf("existing fields disturbed:\n%s", out)
	}
	// Inserted fields reuse the instance's own separator (tab indentation).
	if !strings.Contains(out, "\n\t<LowerBound>") {
		t.Errorf("separator not reused for inserted fields:\n%s", out)
	}
}

// A param where 2026 KEPT real (non-default) bounds must be left exactly as
// Premiere wrote it — we must never clobber a real per-parameter value.
func TestRebuildLeavesCompleteInstanceByteIdentical(t *testing.T) {
	complete := `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<ParameterID>1</ParameterID>
	<LowerBound>-150</LowerBound>
	<UpperBound>150</UpperBound>
</VideoComponentParam>`
	out, stats := reconstructPositionalClasses(complete)
	if out != complete {
		t.Errorf("complete instance was modified:\n%s", out)
	}
	if len(stats) != 0 {
		t.Errorf("stats non-empty for complete instance: %v", stats)
	}
}

// When only one bound is missing, only that one is inserted; the present one is
// left untouched.
func TestRebuildInsertsOnlyMissingBound(t *testing.T) {
	partial := `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<LowerBound>-150</LowerBound>
</VideoComponentParam>`
	out, stats := reconstructPositionalClasses(partial)
	if !strings.Contains(out, "<LowerBound>-150</LowerBound>") {
		t.Errorf("present bound was disturbed:\n%s", out)
	}
	if !strings.Contains(out, "<UpperBound>true</UpperBound>") {
		t.Errorf("missing UpperBound not inserted:\n%s", out)
	}
	if stats[fieldKey{"VideoComponentParam", "LowerBound"}] != 0 {
		t.Errorf("present LowerBound should not be re-inserted: %v", stats)
	}
	if stats[fieldKey{"VideoComponentParam", "UpperBound"}] != 1 {
		t.Errorf("UpperBound stats = %d, want 1", stats[fieldKey{"VideoComponentParam", "UpperBound"}])
	}
}

// The Lumetri color-selector param class has an unbounded UpperBound that 2025
// won't recompute from the "true" sentinel, so we insert the literal marker for
// it (keyed by ClassID) while ordinary params still get "true".
func TestRebuildClassOverrideForColorUpperBound(t *testing.T) {
	const colorCID = "0fde4e9f-f895-4ba3-b0fe-9a6feafda583"
	color := `<VideoComponentParam ObjectID="10" ClassID="` + colorCID + `" Version="10">
	<Name>Set color</Name>
</VideoComponentParam>`
	out, _ := reconstructPositionalClasses(color)
	if !strings.Contains(out, "<UpperBound>18446744073709551615</UpperBound>") {
		t.Errorf("color class did not get the unbounded UpperBound override:\n%s", out)
	}
	// LowerBound is unaffected by the override — still the plain sentinel.
	if !strings.Contains(out, "<LowerBound>false</LowerBound>") {
		t.Errorf("color class LowerBound should still be the false sentinel:\n%s", out)
	}

	// An ordinary (non-color) ClassID still gets the "true" sentinel.
	ordinary := `<VideoComponentParam ObjectID="11" ClassID="cc12343e-f113-4d3b-ae05-b287db77d461" Version="10">
	<Name>Opacity</Name>
</VideoComponentParam>`
	out, _ = reconstructPositionalClasses(ordinary)
	if !strings.Contains(out, "<UpperBound>true</UpperBound>") {
		t.Errorf("ordinary class should get the true sentinel, not the override:\n%s", out)
	}
}

func TestRebuildSkipsObjectRefs(t *testing.T) {
	// A reference (no Version attribute, self-closing) must not be touched.
	ref := `<VideoComponentParam ObjectRef="10"/>`
	out, stats := reconstructPositionalClasses(ref)
	if out != ref || len(stats) != 0 {
		t.Errorf("reference was modified: %q, stats %v", out, stats)
	}
}

func TestSetAndGetProjectVersion(t *testing.T) {
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="45">
</Project>
</PremiereData>`
	if v := getProjectVersion(xml); v != 45 {
		t.Fatalf("getProjectVersion = %d, want 45", v)
	}
	out := setProjectVersion(xml, 43)
	if !strings.Contains(out, `<Project ObjectID="1" ClassID="y" Version="43">`) {
		t.Errorf("version not rewritten:\n%s", out)
	}
	// The PremiereData Version and everything else stay untouched.
	if !strings.Contains(out, `<PremiereData Version="3">`) {
		t.Errorf("PremiereData version disturbed:\n%s", out)
	}
}

func TestUsage(t *testing.T) {
	var b strings.Builder
	usage(&b)
	got := b.String()
	// The help must name the tool, the --to option, and give live release
	// examples (so a stale hard-coded list can't silently drift from releases).
	for _, want := range []string{"Usage: prem-down", "--to", "--verbose", "--version", "integrate", releaseExamples()} {
		if !strings.Contains(got, want) {
			t.Errorf("usage() output missing %q:\n%s", want, got)
		}
	}
}

// parseXML followed by render must reproduce the input byte-for-byte: that
// round-trip invariant is what lets reconstructPositionalClasses leave
// untouched instances exactly as Premiere wrote them. This also exercises the
// self-closing, comment and declaration branches that the fixture path doesn't.
func TestParseXMLRenderRoundTrip(t *testing.T) {
	inputs := []string{
		`<?xml version="1.0"?>
<Root>
	<Child Version="1">text</Child>
	<SelfClosing Ref="7"/>
	<!-- a comment -->
	<Nested Version="2">
		<Leaf>v</Leaf>
	</Nested>
</Root>`,
		`plain text with no tags`,
		`<Solo Version="1"/>`,
	}
	for _, in := range inputs {
		var b strings.Builder
		for _, r := range parseXML(in) {
			switch v := r.(type) {
			case string:
				b.WriteString(v)
			case *el:
				v.render(&b)
			}
		}
		if got := b.String(); got != in {
			t.Errorf("round-trip changed the input:\n--- in ---\n%s\n--- out ---\n%s", in, got)
		}
	}
}

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
	got := releaseExamples()
	// The three releases just below the newest (2026), single-quoted, "..."-trailed.
	if !strings.HasSuffix(got, "...") {
		t.Errorf("releaseExamples should end with ..., got %q", got)
	}
	for _, want := range []string{"'2025'", "'2024'", "'2023'"} {
		if !strings.Contains(got, want) {
			t.Errorf("releaseExamples = %q, missing %s", got, want)
		}
	}
	// The newest release itself is skipped (examples are for downgrade targets).
	if strings.Contains(got, "'2026'") {
		t.Errorf("releaseExamples should skip the newest release, got %q", got)
	}
}

func TestResolveRelease(t *testing.T) {
	cases := map[string]int{
		"2026":    45,
		"2025":    43,
		"CS4":     22,
		"cs4":     22, // case-insensitive
		"  2025 ": 43, //nolint:gocritic // mapKey: intentional whitespace, exercises resolveRelease's trimming
		"cc2014":  27, // alias, case-insensitive
		"CC2014":  27,
	}
	for name, want := range cases {
		if got := resolveRelease(name); got != want {
			t.Errorf("resolveRelease(%q) = %d, want %d", name, got, want)
		}
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

func TestWarnTarget(t *testing.T) {
	capture := func(version int) string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := os.Stderr
		os.Stderr = w
		warnTarget(version)
		_ = w.Close()
		os.Stderr = orig
		out, _ := io.ReadAll(r)
		return string(out)
	}
	if got := capture(minConvertibleProjectVersion - 1); !strings.Contains(got, "warning") {
		t.Errorf("below-floor target should warn, got %q", got)
	}
	if got := capture(minConvertibleProjectVersion); got != "" {
		t.Errorf("at-floor target should not warn, got %q", got)
	}
	if got := capture(minConvertibleProjectVersion + 5); got != "" {
		t.Errorf("above-floor target should not warn, got %q", got)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "out.prproj")

	// A free path is returned unchanged.
	if got := uniquePath(base); got != base {
		t.Errorf("uniquePath(free) = %q, want %q", got, base)
	}

	// Once taken, a -1 suffix is added before the extension.
	if err := os.WriteFile(base, nil, 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	want1 := filepath.Join(dir, "out-1.prproj")
	if got := uniquePath(base); got != want1 {
		t.Errorf("uniquePath(taken) = %q, want %q", got, want1)
	}

	// With -1 also taken, it climbs to -2.
	if err := os.WriteFile(want1, nil, 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	want2 := filepath.Join(dir, "out-2.prproj")
	if got := uniquePath(base); got != want2 {
		t.Errorf("uniquePath(taken twice) = %q, want %q", got, want2)
	}
}

// A downgrade of a plain (un-gzipped) source that is already at or below the
// dense-serialisation floor: only the <Project> version is re-gated, the output
// is still written gzipped, and the rest of the XML is byte-identical.
func TestDowngradePlainXMLInput(t *testing.T) {
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	// verbose=true also exercises the "already compatible, only re-gating" report
	// path taken for sources at/below the dense-serialisation floor.
	downgrade(src, out, 41, true)

	outXML := string(gunzipFile(t, out))
	if getProjectVersion(outXML) != 41 {
		t.Fatalf("output version = %d, want 41", getProjectVersion(outXML))
	}
	if outXML != setProjectVersion(xml, 41) {
		t.Errorf("pre-2026 plain input should only be re-gated, got:\n%s", outXML)
	}
}

// downgrade with projectVersion == 0 auto-targets the release one step below the
// source. The 2026 fixture (v45) must resolve to v43 (2025), skipping the absent
// v44. Exercised with verbose=true to cover the reporting branch too.
func TestDowngradeAutoTargetVerbose(t *testing.T) {
	fixture := filepath.Join("testdata", "fixture_ppro26.prproj")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture %s not present", fixture)
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out.prproj")
	downgrade(fixture, out, 0, true)

	outXML := string(gunzipFile(t, out))
	if got := getProjectVersion(outXML); got != 43 {
		t.Fatalf("auto-target of v45 source = %d, want 43", got)
	}
}

func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is a test-controlled temp file
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestPre2026PassThrough: a v43 source must only have its <Project> version
// re-gated — no field insertion — and be otherwise byte-identical.
func TestPre2026PassThrough(t *testing.T) {
	fixture := filepath.Join("testdata", "fixture_ppro25.prproj")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture %s not present", fixture)
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out.prproj")
	downgrade(fixture, out, 42, false)

	inXML := string(gunzipFile(t, fixture))
	outXML := string(gunzipFile(t, out))
	expected := setProjectVersion(inXML, 42)
	if outXML != expected {
		t.Fatal("v43 pass-through output is not input-with-regated-version")
	}
}

// TestDowngrade2026Fixture round-trips the real 2026 fixture (v45 -> v43) and
// asserts the invariant 2025 requires: every VideoComponentParam definition
// ends up with both LowerBound and UpperBound present. Also checks the Lumetri
// color-class override and that the version is re-gated.
func TestDowngrade2026Fixture(t *testing.T) {
	fixture := filepath.Join("testdata", "fixture_ppro26.prproj")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture %s not present", fixture)
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out.prproj")
	downgrade(fixture, out, 43, false)
	outXML := string(gunzipFile(t, out))

	if getProjectVersion(outXML) != 43 {
		t.Fatalf("output project version = %d, want 43", getProjectVersion(outXML))
	}

	// Every VideoComponentParam definition (has a Version attr; not an
	// ObjectRef) must carry both bounds, or 2025 reports the project damaged.
	defRe := regexp.MustCompile(`(?s)<VideoComponentParam[ \t\r\n][^>]*\bVersion="\d+"[^>]*>.*?</VideoComponentParam>`)
	defs := defRe.FindAllString(outXML, -1)
	if len(defs) == 0 {
		t.Fatal("no VideoComponentParam definitions found in fixture output")
	}
	colorOverrides := 0
	for _, d := range defs {
		if !strings.Contains(d, "<LowerBound>") || !strings.Contains(d, "<UpperBound>") {
			t.Fatalf("VideoComponentParam missing a bound after downgrade:\n%s", d)
		}
		if strings.Contains(d, `ClassID="0fde4e9f-f895-4ba3-b0fe-9a6feafda583"`) {
			colorOverrides++
			if !strings.Contains(d, "<UpperBound>18446744073709551615</UpperBound>") {
				t.Errorf("color-class param did not get the unbounded UpperBound:\n%s", d)
			}
		}
	}
	if colorOverrides == 0 {
		t.Error("expected at least one Lumetri color-class param in the fixture")
	}
}
