package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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
		// A "<...>" token whose first char isn't a name char (leading space) is
		// not a tag: parseXML passes it through as literal text, and it must
		// still round-trip. Exercises the tagNameRe no-match branch.
		`before < not a tag > after`,
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
	if err := downgrade(src, out, 41, true); err != nil {
		t.Fatal(err)
	}

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
	if err := downgrade(fixture, out, 0, true); err != nil {
		t.Fatal(err)
	}

	outXML := string(gunzipFile(t, out))
	if got := getProjectVersion(outXML); got != 43 {
		t.Fatalf("auto-target of v45 source = %d, want 43", got)
	}
}

// downgrade returns an operational error (rather than exiting) for a file that
// isn't a Premiere project, so a batch caller can report it and keep going. No
// output file must be written for the failed input.
func TestDowngradeReturnsErrorForNonPremiereFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(src, []byte("just some text"), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	if err := downgrade(src, out, 43, false); err == nil {
		t.Fatal("expected an error for a non-Premiere file, got nil")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written when the input is rejected")
	}
}

// downgrade refuses a --to at or above the source release rather than stamping
// a higher version — it is a downgrader, so target >= source is user error.
// The error is returned (not fatal) so a batch caller can keep going, and no
// output file is written.
func TestDowngradeRejectsTargetNotBelowSource(t *testing.T) {
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
	// Equal to the source is refused just like above it.
	if err := downgrade(src, out, 42, false); err == nil {
		t.Fatal("expected an error for a target not below the source, got nil")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written when the target is rejected")
	}
}

// With auto-target (projectVersion == 0) on a source already at the oldest known
// release, there is no earlier release to pick, so downgrade returns an error
// rather than exiting.
func TestDowngradeAutoTargetNoEarlierRelease(t *testing.T) {
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="22">
</Project>
</PremiereData>`
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	err := downgrade(src, out, 0, false)
	if err == nil {
		t.Fatal("expected an error when the source has no earlier release, got nil")
	}
	if !strings.Contains(err.Error(), "no known earlier release") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written when there is no target release")
	}
}

// A file that starts with the gzip magic bytes but isn't valid gzip is reported
// as an operational error (from the decompressor), not a hard exit.
func TestDowngradeCorruptGzip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	// gzip magic (0x1f 0x8b) followed by a truncated/invalid stream.
	if err := os.WriteFile(src, []byte{0x1f, 0x8b, 0x08, 0x00}, 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	if err := downgrade(src, out, 43, false); err == nil {
		t.Fatal("expected an error for a corrupt gzip source, got nil")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written for a corrupt gzip source")
	}
}

// downgrade surfaces a read failure (rather than exiting) so a batch caller can
// report it and continue. A directory passed as the source makes os.ReadFile
// fail.
func TestDowngradeReadError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.prproj")
	if err := downgrade(dir, out, 43, false); err == nil {
		t.Fatal("expected a read error when the source is a directory, got nil")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written when the source can't be read")
	}
}

// A gzip stream with an intact header but a corrupted body passes gzip.NewReader
// and fails later in io.ReadAll (a distinct branch from the bad-header case).
func TestDowngradeCorruptGzipBody(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`<PremiereData Version="3"><Project ObjectID="1" ClassID="y" Version="45"></Project></PremiereData>`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[len(raw)/2] ^= 0xFF // corrupt the deflate body, leaving the header intact

	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	if err := os.WriteFile(src, raw, 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	if err := downgrade(src, out, 43, false); err == nil {
		t.Fatal("expected a decompression error for a corrupt gzip body, got nil")
	}
}

// downgrade returns the write failure (rather than exiting) when the output path
// is unwritable — here its parent directory does not exist.
func TestDowngradeWriteError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "no-such-dir", "out.prproj")
	if err := downgrade(src, dst, 41, false); err == nil {
		t.Fatal("expected a write error for an unwritable destination, got nil")
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
	if err := downgrade(fixture, out, 42, false); err != nil {
		t.Fatal(err)
	}

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
	if err := downgrade(fixture, out, 43, false); err != nil {
		t.Fatal(err)
	}
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

// --------------------------------------------------------------------------
// Exit-path coverage. fatal() and main() end the process via os.Exit, so they
// are routed through the osExit seam; runCaptured stubs it (aborting via panic,
// as a real exit would abort the process) and redirects stdout/stderr, making
// every fatal branch and the whole run() arg parser reachable in-process.
// --------------------------------------------------------------------------

// exitPanic is what the stubbed osExit panics with, so a fatal() path unwinds
// back to runCaptured instead of continuing past the point the real os.Exit
// would have ended the process.
type exitPanic struct{ code int }

// runCaptured invokes fn with osExit stubbed and os.Stdout/os.Stderr redirected.
// It returns fn's own return value (or the code passed to a fatal exit), whether
// fn exited via fatal, and the captured stdout and stderr.
func runCaptured(t *testing.T, fn func() int) (code int, exited bool, stdout, stderr string) {
	t.Helper()
	origExit, origOut, origErr := osExit, os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	osExit = func(c int) { panic(exitPanic{c}) }

	// Drain both pipes concurrently. macOS pipes start with a small buffer, so a
	// usage/fatal-sized write would block the writer if we only read after fn
	// returned — deadlocking the test.
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()

	func() {
		defer func() {
			if r := recover(); r != nil {
				ep, ok := r.(exitPanic)
				if !ok {
					panic(r) // a real bug, not a stubbed exit — re-raise
				}
				code, exited = ep.code, true
			}
		}()
		code = fn()
	}()

	os.Stdout, os.Stderr, osExit = origOut, origErr, origExit
	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	return code, exited, outBuf.String(), errBuf.String()
}

// Every helper that reports a corrupt/unrecognised document does so via fatal
// (a hard exit), since it means a malformed input rather than an ordinary skip.
// Each must exit 1 and name the problem.

func TestResolveReleaseUnknownExits(t *testing.T) {
	code, exited, _, stderr := runCaptured(t, func() int { return resolveRelease("NoSuchRelease") })
	if !exited || code != 1 {
		t.Fatalf("unknown release should fatal with code 1, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "unknown release") {
		t.Errorf("missing diagnostic: %q", stderr)
	}
}

func TestParseXMLFatalCases(t *testing.T) {
	cases := map[string]string{
		"unbalanced close": `</Orphan>`,
		"mismatched close": `<A Version="1"></B>`,
		"never closed":     `<A Version="1">text`,
	}
	for name, in := range cases {
		code, exited, _, stderr := runCaptured(t, func() int { parseXML(in); return 0 })
		if !exited || code != 1 {
			t.Errorf("%s: expected fatal exit 1, got exited=%v code=%d", name, exited, code)
		}
		if !strings.Contains(strings.ToLower(stderr), "xml") {
			t.Errorf("%s: missing XML diagnostic: %q", name, stderr)
		}
	}
}

func TestSetProjectVersionWrongCountExits(t *testing.T) {
	cases := map[string]string{
		"zero matches": `<PremiereData Version="3"></PremiereData>`,
		"two matches":  `<Project ObjectID="1" Version="45"><Project ObjectID="1" Version="45">`,
	}
	for name, xml := range cases {
		code, exited, _, stderr := runCaptured(t, func() int { setProjectVersion(xml, 43); return 0 })
		if !exited || code != 1 {
			t.Errorf("%s: expected fatal exit 1, got exited=%v code=%d", name, exited, code)
		}
		if !strings.Contains(stderr, "exactly one") {
			t.Errorf("%s: missing diagnostic: %q", name, stderr)
		}
	}
}

func TestGetProjectVersionExits(t *testing.T) {
	// No <Project ObjectID="1"> tag at all -> no regex match.
	code, exited, _, stderr := runCaptured(t, func() int { getProjectVersion("<PremiereData/>"); return 0 })
	if !exited || code != 1 {
		t.Errorf("absent project tag should fatal 1, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "could not find") {
		t.Errorf("missing diagnostic: %q", stderr)
	}

	// The regex only captures digits, so the only way to reach the Atoi error is
	// a version with more digits than an int can hold (a range error).
	huge := `<Project ObjectID="1" ClassID="y" Version="` + strings.Repeat("9", 40) + `">`
	code, exited, _, stderr = runCaptured(t, func() int { getProjectVersion(huge); return 0 })
	if !exited || code != 1 {
		t.Errorf("over-long version should fatal 1, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "invalid") {
		t.Errorf("missing diagnostic: %q", stderr)
	}
}

// --------------------------------------------------------------------------
// run() — the CLI arg parser and dispatch, exercised through the exit seam.
// --------------------------------------------------------------------------

func TestRunHelpAndVersion(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		code, exited, stdout, _ := runCaptured(t, func() int { return run([]string{arg}) })
		if exited || code != 0 {
			t.Errorf("%s: want clean exit 0, got exited=%v code=%d", arg, exited, code)
		}
		if !strings.Contains(stdout, "Usage: prem-down") {
			t.Errorf("%s: help not printed:\n%s", arg, stdout)
		}
	}
	code, exited, stdout, _ := runCaptured(t, func() int { return run([]string{"--version"}) })
	if exited || code != 0 {
		t.Errorf("--version: want 0, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stdout, "prem-down "+version) {
		t.Errorf("--version not printed: %q", stdout)
	}
}

func TestRunNoPositionalsReturns2(t *testing.T) {
	// A flag but no input file: usage to stderr, exit code 2.
	code, exited, _, stderr := runCaptured(t, func() int { return run([]string{"-v"}) })
	if exited || code != 2 {
		t.Errorf("no input files should return 2, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("usage not printed to stderr:\n%s", stderr)
	}
}

func TestRunUnknownOptionExits(t *testing.T) {
	code, exited, _, stderr := runCaptured(t, func() int { return run([]string{"--nope"}) })
	if !exited || code != 1 {
		t.Errorf("unknown option should fatal 1, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "unknown option") {
		t.Errorf("missing diagnostic:\n%s", stderr)
	}
}

func TestRunToRequiresValueExits(t *testing.T) {
	code, exited, _, stderr := runCaptured(t, func() int { return run([]string{"--to"}) })
	if !exited || code != 1 {
		t.Errorf("--to without a value should fatal 1, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "--to requires a value") {
		t.Errorf("missing diagnostic:\n%s", stderr)
	}
}

func TestRunMissingFileReturns1(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.prproj")
	code, exited, _, stderr := runCaptured(t, func() int { return run([]string{missing}) })
	if exited || code != 1 {
		t.Errorf("a missing input should return 1, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("missing diagnostic:\n%s", stderr)
	}
}

// The success path: --to= form, verbose, and a two-file batch each written next
// to its original. Covers the parse loop, resolveRelease, the per-file Stat +
// downgrade loop, and the return-0 tail.
func TestRunBatchSuccess(t *testing.T) {
	dir := t.TempDir()
	const xml = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	var inputs []string
	for _, n := range []string{"a.prproj", "b.prproj"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
			t.Fatal(err)
		}
		inputs = append(inputs, p)
	}
	args := append([]string{"--to=2023", "-v"}, inputs...)
	code, exited, stdout, _ := runCaptured(t, func() int { return run(args) })
	if exited || code != 0 {
		t.Fatalf("batch should succeed with 0, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stdout, "wrote ") {
		t.Errorf("no downgrade output:\n%s", stdout)
	}
	for _, in := range inputs {
		out := strings.TrimSuffix(in, ".prproj") + "_downgraded.prproj"
		if _, err := os.Stat(out); err != nil {
			t.Errorf("expected output %s to be written: %v", out, err)
		}
	}
}

// --gui makes run wait for Enter before returning (the OS context menu opens a
// console that would otherwise vanish). Feed a newline via stdin so the pause
// returns; this covers the guiMode branch of pauseIfGUI.
func TestRunGUIPauses(t *testing.T) {
	origStdin, origGUI := os.Stdin, guiMode
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	_, _ = w.WriteString("\n")
	_ = w.Close()
	t.Cleanup(func() { os.Stdin = origStdin; guiMode = origGUI })

	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	const xml = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	code, exited, _, _ := runCaptured(t, func() int { return run([]string{"--gui", "--to=2023", src}) })
	if exited || code != 0 {
		t.Fatalf("gui run should return 0, got exited=%v code=%d", exited, code)
	}
	if !guiMode {
		t.Error("--gui should have set guiMode")
	}
}

// The space-separated "--to RELEASE" form (distinct from "--to="), combined with
// an input that exists but isn't a Premiere project: downgrade fails, run reports
// it and returns 1 without exiting.
func TestRunToSpaceFormAndDowngradeError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.prproj")
	if err := os.WriteFile(src, []byte("not a premiere project"), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	code, exited, _, stderr := runCaptured(t, func() int { return run([]string{"--to", "2023", src}) })
	if exited || code != 1 {
		t.Fatalf("a failed downgrade should return 1, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("downgrade failure not reported:\n%s", stderr)
	}
}

// run dispatches the "integrate" subcommand; --help there is a clean no-op.
// HOME points at a temp dir so nothing touches the real Services folder.
func TestRunIntegrateDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	code, exited, stdout, _ := runCaptured(t, func() int { return run([]string{"integrate", "-h"}) })
	if exited || code != 0 {
		t.Fatalf("integrate -h should return 0, got exited=%v code=%d", exited, code)
	}
	if !strings.Contains(stdout, "integrate") {
		t.Errorf("integrate help not printed:\n%s", stdout)
	}
}
