package premdown

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// silent is a Downgrader that discards all output, for exercising Downgrade and
// friends where the test asserts on the written file rather than on what was
// printed. The zero value already discards; this just names the intent.
func silent() *Downgrader { return &Downgrader{} }

// A VideoComponentParam as 2026 writes it when both bounds sit at their
// per-parameter defaults: the LowerBound/UpperBound children are dropped. 2025
// requires them present, so reconstruction must re-insert the false/true
// sentinels (which 2025 then repopulates with the real bounds on load).
const sparseVideoComponentParam = `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<ParameterID>1</ParameterID>
	<StartKeyframe>0,true,0,0,0,0,0,0</StartKeyframe>
</VideoComponentParam>`

// mustReconstruct, mustGetProjectVersion and mustSetProjectVersion unwrap the
// error returns for tests that exercise well-formed inputs; the error paths
// have their own dedicated tests below.
func mustReconstruct(t *testing.T, xml string) (string, map[fieldKey]int) {
	t.Helper()
	out, stats, err := reconstructPositionalClasses(xml)
	if err != nil {
		t.Fatal(err)
	}
	return out, stats
}

func mustGetProjectVersion(t *testing.T, xml string) int {
	t.Helper()
	v, err := getProjectVersion(xml)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustSetProjectVersion(t *testing.T, xml string, version int) string {
	t.Helper()
	out, err := setProjectVersion(xml, version)
	if err != nil {
		t.Fatal(err)
	}
	return out
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

// --------------------------------------------------------------------------
// reconstruct.go - the mini-DOM and the field re-insertion pass.
// --------------------------------------------------------------------------

func TestRebuildInsertsMissingBounds(t *testing.T) {
	out, stats := mustReconstruct(t, sparseVideoComponentParam)
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
// Premiere wrote it - we must never clobber a real per-parameter value.
func TestRebuildLeavesCompleteInstanceByteIdentical(t *testing.T) {
	complete := `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<ParameterID>1</ParameterID>
	<LowerBound>-150</LowerBound>
	<UpperBound>150</UpperBound>
</VideoComponentParam>`
	out, stats := mustReconstruct(t, complete)
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
	out, stats := mustReconstruct(t, partial)
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
	out, _ := mustReconstruct(t, color)
	if !strings.Contains(out, "<UpperBound>18446744073709551615</UpperBound>") {
		t.Errorf("color class did not get the unbounded UpperBound override:\n%s", out)
	}
	// LowerBound is unaffected by the override - still the plain sentinel.
	if !strings.Contains(out, "<LowerBound>false</LowerBound>") {
		t.Errorf("color class LowerBound should still be the false sentinel:\n%s", out)
	}

	// An ordinary (non-color) ClassID still gets the "true" sentinel.
	ordinary := `<VideoComponentParam ObjectID="11" ClassID="cc12343e-f113-4d3b-ae05-b287db77d461" Version="10">
	<Name>Opacity</Name>
</VideoComponentParam>`
	out, _ = mustReconstruct(t, ordinary)
	if !strings.Contains(out, "<UpperBound>true</UpperBound>") {
		t.Errorf("ordinary class should get the true sentinel, not the override:\n%s", out)
	}
}

func TestRebuildSkipsObjectRefs(t *testing.T) {
	// A reference (no Version attribute, self-closing) must not be touched.
	ref := `<VideoComponentParam ObjectRef="10"/>`
	out, stats := mustReconstruct(t, ref)
	if out != ref || len(stats) != 0 {
		t.Errorf("reference was modified: %q, stats %v", out, stats)
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
		roots, err := parseXML(in)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, r := range roots {
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

// A corrupt/unrecognised document surfaces as a returned error (never a hard
// exit), so a batch caller can report the one bad file and keep going.

func TestParseXMLErrors(t *testing.T) {
	cases := map[string]string{
		"unbalanced close": `</Orphan>`,
		"mismatched close": `<A Version="1"></B>`,
		"never closed":     `<A Version="1">text`,
	}
	for name, in := range cases {
		_, err := parseXML(in)
		if err == nil {
			t.Errorf("%s: expected an error, got nil", name)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), "xml") {
			t.Errorf("%s: missing XML diagnostic: %q", name, err)
		}
	}
}

func TestReconstructPositionalClassesPropagatesParseError(t *testing.T) {
	// The instance regex matches, but the region's XML is malformed (the inner
	// mismatched close). The error must reach the caller instead of exiting.
	bad := `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<LowerBound>1</Wrong>
</VideoComponentParam>`
	if _, _, err := reconstructPositionalClasses(bad); err == nil {
		t.Fatal("expected a parse error for a malformed instance, got nil")
	}
}

// Once one instance is malformed the whole file is refused, so the instances
// after it are left exactly as they were rather than half-rebuilt.
func TestReconstructPositionalClassesStopsAtTheFirstBadInstance(t *testing.T) {
	bad := `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<LowerBound>1</Wrong>
</VideoComponentParam>
` + sparseVideoComponentParam
	out, stats, err := reconstructPositionalClasses(bad)
	if err == nil {
		t.Fatal("expected a parse error for a malformed instance, got nil")
	}
	if out != "" || stats != nil {
		t.Errorf("a refused file must yield no document: %q, %v", out, stats)
	}
}

// Self-closing elements carry no content to complete, so they are stepped over
// rather than rebuilt - and the instance around them is still byte-identical
// apart from the fields actually inserted.
func TestRebuildSkipsSelfClosingChildren(t *testing.T) {
	in := `<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<ParameterID/>
	<StartKeyframe>0,true,0,0,0,0,0,0</StartKeyframe>
</VideoComponentParam>`
	out, stats := mustReconstruct(t, in)
	if !strings.Contains(out, "<ParameterID/>") {
		t.Errorf("the self-closing child should be reproduced as it was:\n%s", out)
	}
	if len(stats) != 2 {
		t.Errorf("both bounds should still be inserted, got %v", stats)
	}
}

// --------------------------------------------------------------------------
// downgrade.go - <Project> version stamping and path helpers.
// --------------------------------------------------------------------------

func TestSetAndGetProjectVersion(t *testing.T) {
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="45">
</Project>
</PremiereData>`
	if v := mustGetProjectVersion(t, xml); v != 45 {
		t.Fatalf("getProjectVersion = %d, want 45", v)
	}
	out := mustSetProjectVersion(t, xml, 43)
	if !strings.Contains(out, `<Project ObjectID="1" ClassID="y" Version="43">`) {
		t.Errorf("version not rewritten:\n%s", out)
	}
	// The PremiereData Version and everything else stay untouched.
	if !strings.Contains(out, `<PremiereData Version="3">`) {
		t.Errorf("PremiereData version disturbed:\n%s", out)
	}
}

func TestSetProjectVersionWrongCount(t *testing.T) {
	cases := map[string]string{
		"zero matches": `<PremiereData Version="3"></PremiereData>`,
		"two matches":  `<Project ObjectID="1" Version="45"><Project ObjectID="1" Version="45">`,
	}
	for name, xml := range cases {
		_, err := setProjectVersion(xml, 43)
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("%s: expected an 'exactly one' error, got %v", name, err)
		}
	}
}

func TestGetProjectVersionErrors(t *testing.T) {
	// No <Project ObjectID="1"> tag at all -> no regex match.
	if _, err := getProjectVersion("<PremiereData/>"); err == nil || !strings.Contains(err.Error(), "could not find") {
		t.Errorf("absent project tag: expected a 'could not find' error, got %v", err)
	}

	// The regex only captures digits, so the only way to reach the Atoi error is
	// a version with more digits than an int can hold (a range error).
	huge := `<Project ObjectID="1" ClassID="y" Version="` + strings.Repeat("9", 40) + `">`
	if _, err := getProjectVersion(huge); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("over-long version: expected an 'invalid' error, got %v", err)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "out.prproj")

	// A free path is returned unchanged.
	if got := UniquePath(base); got != base {
		t.Errorf("UniquePath(free) = %q, want %q", got, base)
	}

	// Once taken, a -1 suffix is added before the extension.
	if err := os.WriteFile(base, nil, 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	want1 := filepath.Join(dir, "out-1.prproj")
	if got := UniquePath(base); got != want1 {
		t.Errorf("UniquePath(taken) = %q, want %q", got, want1)
	}

	// With -1 also taken, it climbs to -2.
	if err := os.WriteFile(want1, nil, 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	want2 := filepath.Join(dir, "out-2.prproj")
	if got := UniquePath(base); got != want2 {
		t.Errorf("UniquePath(taken twice) = %q, want %q", got, want2)
	}
}

// --------------------------------------------------------------------------
// downgrade() end to end.
// --------------------------------------------------------------------------

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
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	// verbose=true also exercises the "already compatible, only re-gating" report
	// path taken for sources at/below the dense-serialisation floor.
	if err := silent().Downgrade(src, out, 41, true); err != nil {
		t.Fatal(err)
	}

	outXML := string(gunzipFile(t, out))
	if v := mustGetProjectVersion(t, outXML); v != 41 {
		t.Fatalf("output version = %d, want 41", v)
	}
	if outXML != mustSetProjectVersion(t, xml, 41) {
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
	if err := silent().Downgrade(fixture, out, 0, true); err != nil {
		t.Fatal(err)
	}

	outXML := string(gunzipFile(t, out))
	if got := mustGetProjectVersion(t, outXML); got != 43 {
		t.Fatalf("auto-target of v45 source = %d, want 43", got)
	}
}

// downgrade returns an operational error for a file that isn't a Premiere
// project, so a batch caller can report it and keep going. No output file must
// be written for the failed input.
func TestDowngradeReturnsErrorForNonPremiereFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(src, []byte("just some text"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	if err := silent().Downgrade(src, out, 43, false); err == nil {
		t.Fatal("expected an error for a non-Premiere file, got nil")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written when the input is rejected")
	}
}

// downgrade refuses a --to at or above the source release. The error is
// returned (not fatal) so a batch caller can keep going, and no output file is
// written.
func TestDowngradeRejectsTargetNotBelowSource(t *testing.T) {
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	// Equal to the source is refused just like above it.
	err := silent().Downgrade(src, out, 42, false)
	if err == nil {
		t.Fatal("expected an error for a target not below the source, got nil")
	}
	// On a lone file this is user error, so the message has to name both
	// versions and point at the flag that caused it.
	for _, want := range []string{"42", "--to"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
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
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	err := silent().Downgrade(src, out, 0, false)
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
// as an operational error (from the decompressor).
func TestDowngradeCorruptGzip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	// gzip magic (0x1f 0x8b) followed by a truncated/invalid stream.
	if err := os.WriteFile(src, []byte{0x1f, 0x8b, 0x08, 0x00}, 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	if err := silent().Downgrade(src, out, 43, false); err == nil {
		t.Fatal("expected an error for a corrupt gzip source, got nil")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written for a corrupt gzip source")
	}
}

// downgrade surfaces a read failure so a batch caller can report it and
// continue. A directory passed as the source makes os.ReadFile fail.
func TestDowngradeReadError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.prproj")
	if err := silent().Downgrade(dir, out, 43, false); err == nil {
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
	if err := os.WriteFile(src, raw, 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.prproj")
	if err := silent().Downgrade(src, out, 43, false); err == nil {
		t.Fatal("expected a decompression error for a corrupt gzip body, got nil")
	}
}

// Every file this package creates is written through fillNew, and the contract
// it carries is that a failure leaves nothing behind: the file was opened with
// O_EXCL, so it holds only our own partial output, and a truncated project left
// next to the original is one the user could open by mistake.
//
// Driven directly because neither a failing write nor a failing close can be
// provoked through writeNew or copyFile on a filesystem that works.
func TestFillNewRemovesPartialOutput(t *testing.T) {
	// A real file, created exactly as the callers create theirs.
	newFile := func(t *testing.T) (*os.File, string) {
		t.Helper()
		dst := filepath.Join(t.TempDir(), "out"+PrprojExt)
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302,G304: this test's own t.TempDir path, opened the way writeNew opens its output
		if err != nil {
			t.Fatal(err)
		}
		return f, dst
	}

	t.Run("a failed write", func(t *testing.T) {
		f, dst := newFile(t)
		boom := errors.New("boom")
		err := fillNew(f, dst, func(w io.Writer) error {
			// Some bytes land before the failure: this is the truncated-output
			// case, not a file that was never touched.
			if _, err := w.Write([]byte("half a project")); err != nil {
				t.Fatalf("setting up a partial write: %v", err)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Errorf("fillNew = %v, want the write's own error", err)
		}
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Errorf("the partial file was left behind (stat err: %v)", err)
		}
	})

	t.Run("a failed close", func(t *testing.T) {
		f, dst := newFile(t)
		// Closed early so fillNew's own Close fails, which is the shape of a
		// write whose error only surfaces when the last buffer is flushed - the
		// reason closing is checked at all rather than deferred.
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		err := fillNew(f, dst, func(io.Writer) error { return nil })
		if err == nil {
			t.Fatal("a failed close must be reported, not swallowed")
		}
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Errorf("the file was left behind after a failed close (stat err: %v)", err)
		}
	})

	// The other half of the contract: nothing failed, so nothing is removed.
	t.Run("a successful write keeps the file", func(t *testing.T) {
		f, dst := newFile(t)
		if err := fillNew(f, dst, func(w io.Writer) error {
			_, err := w.Write([]byte("a whole project"))
			return err
		}); err != nil {
			t.Fatalf("fillNew: %v", err)
		}
		got, err := os.ReadFile(dst) //nolint:gosec // G304: this test's own t.TempDir path
		if err != nil {
			t.Fatalf("output unreadable: %v", err)
		}
		if string(got) != "a whole project" {
			t.Errorf("output = %q, want %q", got, "a whole project")
		}
	})
}

// downgrade returns the write failure when the output path is unwritable - here
// its parent directory does not exist.
func TestDowngradeWriteError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "no-such-dir", "out.prproj")
	if err := silent().Downgrade(src, dst, 41, false); err == nil {
		t.Fatal("expected a write error for an unwritable destination, got nil")
	}
}

// Everything that can go wrong between reading a project and writing it is
// returned rather than fatal, and leaves no output behind: a document that
// carries the Premiere marker but no project tag, one whose classes cannot be
// parsed, and one carrying two project tags (where stamping a version would
// have to guess which one Premiere reads).
func TestDowngradeRefusesMalformedProjects(t *testing.T) {
	cases := map[string]string{
		"no project tag": `<PremiereData Version="3">
</PremiereData>`,
		"malformed class instance": `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="45"></Project>
<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<LowerBound>1</Wrong>
</VideoComponentParam>
</PremiereData>`,
		"two project tags": `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="45"></Project>
<Project ObjectID="1" ClassID="y" Version="45"></Project>
</PremiereData>`,
	}
	for name, xml := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := writeFile(t, filepath.Join(dir, "in"+PrprojExt), xml)
			out := filepath.Join(dir, "out"+PrprojExt)
			if err := silent().Downgrade(src, out, 43, false); err == nil {
				t.Fatal("expected an error, got nil")
			}
			if _, err := os.Stat(out); err == nil {
				t.Error("no output file should be written for a refused project")
			}
		})
	}
}

// The same corrupt class instance as above, reaching the refusal by the other
// route. From a source above 43 the conversion reconstructs every class, so a
// malformed one is refused on the way in - which is what the case above covers.
// At 43 or below there is nothing to re-insert, the document is never parsed on
// the way in, and verifyDowngraded's re-parse is the only thing between a
// corrupt source and a corrupt file written next to it. Downgrade has to pass
// that refusal on rather than write the gzip it has already built.
func TestDowngradeVerifiesAPassThroughConversion(t *testing.T) {
	dir := t.TempDir()
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="43"></Project>
<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<LowerBound>1</Wrong>
</VideoComponentParam>
</PremiereData>`
	src := writeFile(t, filepath.Join(dir, "in"+PrprojExt), xml)
	out := filepath.Join(dir, "out"+PrprojExt)

	// 43 -> 42 is the pass-through shape: both sides are dense, so
	// needsFieldReinsertion is false and no class is touched going in.
	err := silent().Downgrade(src, out, 42, false)
	if err == nil {
		t.Fatal("expected a refusal for a corrupt pass-through, got nil")
	}
	if !strings.Contains(err.Error(), "verify:") {
		t.Errorf("error %q should come from the verify step", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("no output file should be written for a refused project")
	}
}

// verifyDowngraded is the gate between the finished document and the disk. It
// is driven directly here because a document that fails it is by construction
// one the conversion above it was supposed to make impossible - the point of
// the check is that a future change which breaks that is refused, not written.
func TestVerifyDowngradedRefusesABadConversion(t *testing.T) {
	const stamped = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="43">
</Project>
</PremiereData>`

	cases := []struct {
		name       string
		xml        string
		reinserted bool
		want       string
	}{
		{"no version to read back", "<PremiereData/>", false, "verify:"},
		{
			"stamped with the wrong version",
			strings.Replace(stamped, `Version="43"`, `Version="41"`, 1), false, "want 43",
		},
		{
			"output no longer parses",
			stamped + "\n<VideoComponentParam ObjectID=\"1\" ClassID=\"x\" Version=\"10\">\n\t<LowerBound>1</Wrong>\n</VideoComponentParam>",
			false, "re-parse",
		},
		// A required field still missing after a conversion that was supposed to
		// re-insert them. A second pass would insert them, which is also what
		// makes the document change, so the refusal names the fields rather than
		// only reporting that something moved.
		{
			"fields still missing", stamped + "\n" + sparseVideoComponentParam, true,
			"VideoComponentParam/LowerBound (1x), VideoComponentParam/UpperBound (1x)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyDowngraded(tc.xml, 43, tc.reinserted)
			if err == nil {
				t.Fatal("expected a refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
	// The shape it exists to let through: stamped at the target, and every
	// required field already present.
	complete := stamped + `
<VideoComponentParam ObjectID="10" ClassID="x" Version="10">
	<LowerBound>false</LowerBound>
	<UpperBound>true</UpperBound>
</VideoComponentParam>`
	if err := verifyDowngraded(complete, 43, true); err != nil {
		t.Errorf("a correct conversion must pass: %v", err)
	}
}

// TestPre2026PassThrough: a v43 source must only have its <Project> version
// re-gated and be otherwise byte-identical.
func TestPre2026PassThrough(t *testing.T) {
	fixture := filepath.Join("testdata", "fixture_ppro25.prproj")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture %s not present", fixture)
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out.prproj")
	if err := silent().Downgrade(fixture, out, 42, false); err != nil {
		t.Fatal(err)
	}

	inXML := string(gunzipFile(t, fixture))
	outXML := string(gunzipFile(t, out))
	expected := mustSetProjectVersion(t, inXML, 42)
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
	if err := silent().Downgrade(fixture, out, 43, false); err != nil {
		t.Fatal(err)
	}
	outXML := string(gunzipFile(t, out))

	if v := mustGetProjectVersion(t, outXML); v != 43 {
		t.Fatalf("output project version = %d, want 43", v)
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

// unrecognisedProject writes a project stamped with a <Project> version above
// anything in the release map - the shape a Premiere release newer than this
// build's map takes on disk.
func unrecognisedProject(t *testing.T, dir string, version int) string {
	t.Helper()
	xml := fmt.Sprintf(`<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="%d">
</Project>
</PremiereData>`, version)
	path := filepath.Join(dir, "future.prproj")
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	return path
}

// A source from a release newer than the version map is converted, not refused:
// the working assumption is that such a release bumps the version number
// without changing the serialisation. It must say so on Err, and flag it for
// the CLI, which turns that into an upgrade check.
func TestDowngradeWarnsAndProceedsOnUnrecognisedRelease(t *testing.T) {
	newestVersion := newestKnownProjectVersion()
	dir := t.TempDir()
	src := unrecognisedProject(t, dir, newestVersion+2)
	out := filepath.Join(dir, "out.prproj")

	var errBuf bytes.Buffer
	d := &Downgrader{Err: &errBuf}
	if err := d.Downgrade(src, out, 0, false); err != nil {
		t.Fatalf("an unrecognised release must warn and proceed, got error: %v", err)
	}

	// Auto-target resolves positionally, so the newest known release is the target.
	if got := mustGetProjectVersion(t, string(gunzipFile(t, out))); got != newestVersion {
		t.Errorf("auto-target of an unrecognised source = %d, want %d", got, newestVersion)
	}
	if !d.SawUnrecognisedRelease() {
		t.Error("want the unrecognised release flagged for the CLI")
	}
	for _, want := range []string{"warning:", "unrecognised Premiere release (too new)", "Attempting to convert anyway"} {
		if !strings.Contains(errBuf.String(), want) {
			t.Errorf("warning missing %q:\n%s", want, errBuf.String())
		}
	}
}

// The warning is about the source, so an explicit --to does not suppress it.
func TestDowngradeWarnsOnUnrecognisedReleaseWithExplicitTarget(t *testing.T) {
	newestVersion := newestKnownProjectVersion()
	dir := t.TempDir()
	src := unrecognisedProject(t, dir, newestVersion+2)

	var errBuf bytes.Buffer
	d := &Downgrader{Err: &errBuf}
	if err := d.Downgrade(src, filepath.Join(dir, "out.prproj"), 41, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "unrecognised") {
		t.Errorf("--to must not suppress the source warning:\n%s", errBuf.String())
	}
}

// One Downgrader is one warning.
func TestUnrecognisedReleaseWarnsOnce(t *testing.T) {
	newestVersion := newestKnownProjectVersion()
	dir := t.TempDir()
	src := unrecognisedProject(t, dir, newestVersion+2)

	var errBuf bytes.Buffer
	d := &Downgrader{Err: &errBuf}
	for i := range 3 {
		if err := d.Downgrade(src, filepath.Join(dir, fmt.Sprintf("out%d.prproj", i)), 0, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(errBuf.String(), "warning:"); got != 1 {
		t.Errorf("want exactly one warning across three files, got %d:\n%s", got, errBuf.String())
	}
	if !d.SawUnrecognisedRelease() {
		t.Error("the flag must stay set after the warning is suppressed")
	}
}

// The known releases are the quiet path: nothing on Err, nothing flagged.
func TestKnownReleaseIsNotFlagged(t *testing.T) {
	newestVersion := newestKnownProjectVersion()
	dir := t.TempDir()
	src := unrecognisedProject(t, dir, newestVersion)

	var errBuf bytes.Buffer
	d := &Downgrader{Err: &errBuf}
	if err := d.Downgrade(src, filepath.Join(dir, "out.prproj"), 0, false); err != nil {
		t.Fatal(err)
	}
	if d.SawUnrecognisedRelease() || errBuf.Len() > 0 {
		t.Errorf("the newest known release must be quiet, got flag=%v err=%q",
			d.SawUnrecognisedRelease(), errBuf.String())
	}
}

// Field re-insertion is decided by the target.
func TestNeedsFieldReinsertion(t *testing.T) {
	const dense = lastDenseSerialisationProjectVersion
	cases := []struct {
		name           string
		source, target int
		want           bool
	}{
		{"2026 -> 2025: sparse source, dense target", 45, dense, true},
		{"2026 -> 2022: sparse source, older dense target", 45, 40, true},
		{"2027 -> 2026: both sparse, nothing to insert", 47, 45, false},
		{"2027 -> 2025: sparse source jumping the boundary", 47, dense, true},
		{"2024 -> 2023: both dense, already complete", 42, 41, false},
		{"2025 -> 2024: source is the boundary itself", dense, 42, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsFieldReinsertion(tc.source, tc.target); got != tc.want {
				t.Errorf("needsFieldReinsertion(%d, %d) = %v, want %v", tc.source, tc.target, got, tc.want)
			}
		})
	}
}

// End-to-end for the row that matters: a sparse source converted to a sparse
// target must come out with the fields still absent, exactly as the target
// release writes them itself. Re-inserting here would write fields that release
// never writes, and would also have to survive a verify pass built for the
// dense-target case.
func TestSparseToSparseInsertsNothing(t *testing.T) {
	newest := newestKnownProjectVersion()
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="` + strconv.Itoa(newest+2) + `">
` + sparseVideoComponentParam + `
</Project>
</PremiereData>`
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.prproj")
	var outBuf bytes.Buffer
	d := &Downgrader{Out: &outBuf}
	if err := d.Downgrade(src, out, 0, true); err != nil {
		t.Fatalf("sparse -> sparse must convert, got: %v", err)
	}

	got := string(gunzipFile(t, out))
	for _, field := range []string{"LowerBound", "UpperBound"} {
		if strings.Contains(got, "<"+field+">") {
			t.Errorf("%s was re-inserted for a target that omits it natively:\n%s", field, got)
		}
	}
	// The version stamp is the entire change.
	if want := mustSetProjectVersion(t, xml, newest); got != want {
		t.Errorf("sparse -> sparse should only re-gate the version, got:\n%s", got)
	}
	if !strings.Contains(outBuf.String(), "nothing to re-insert") {
		t.Errorf("verbose output should explain why nothing was inserted:\n%s", outBuf.String())
	}
}

// The same source with an explicit --to below the boundary still gets the full
// repair: the target is a release that requires the fields.
func TestSparseSourceJumpingTheBoundaryStillReinserts(t *testing.T) {
	newest := newestKnownProjectVersion()
	xml := `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="` + strconv.Itoa(newest+2) + `">
` + sparseVideoComponentParam + `
</Project>
</PremiereData>`
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.prproj")
	if err := silent().Downgrade(src, out, lastDenseSerialisationProjectVersion, false); err != nil {
		t.Fatalf("sparse -> dense must convert, got: %v", err)
	}
	got := string(gunzipFile(t, out))
	for _, field := range []string{"LowerBound", "UpperBound"} {
		if !strings.Contains(got, "<"+field+">") {
			t.Errorf("%s must be re-inserted for a dense target:\n%s", field, got)
		}
	}
}
