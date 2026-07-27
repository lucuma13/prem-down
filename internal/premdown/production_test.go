// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package premdown

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A .prodset as Premiere 2026 writes one: minified JSON, keys sorted, with an
// XML document embedded as a JSON string. The embedded blob is the part a
// re-encode through encoding/json would silently mangle (Go escapes < and > as
// < / >), so it is in every fixture on purpose.
const prodset2026 = `{"mAcceleratedRendererID":"6ed1497e-17ad-4a5b-846f-52bb81e20104",` +
	`"mIngestSettingsStr":"<?xml version=\"1.0\" encoding=\"UTF-8\" ?>\n<PremiereData Version=\"3\">\n\t<IngestSettings ObjectRef=\"1\"/>\n</PremiereData>\n",` +
	`"mMinCompatibleProjectVersion":45,"mPreviousProductionPaths":[],` +
	`"mProjectVersion":45}`

// A minimal project, in the sparse form that needs no field reconstruction, so
// Production tests exercise the mirroring rather than the 2026 XML repair
// (which main_test.go covers against the real fixtures).
const prodProject = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="45">
</Project>
</PremiereData>`

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	return path
}

// A .prin is Premiere's gzip-compressed sidecar that sits next to a .prproj. It
// is not a project, so a Production downgrade must copy it verbatim — and the
// gzip magic in its first bytes must NOT trip the .prproj gzip path (that path
// is reached by extension, never by sniffing). The leading 1f 8b here guards
// exactly that: a gzip-magic sidecar copied byte-for-byte, not decompressed.
const prinSidecar = "\x1f\x8b\x08\x00binary sidecar\xff"

// newProduction lays out a Production: settings file at the top, a project
// beside it, a project nested a folder down, and a .prin sidecar next to that
// nested project (as Premiere writes them) that must survive byte-for-byte.
func newProduction(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	writeFile(t, filepath.Join(dir, name+ProdsetExt), prodset2026)
	writeFile(t, filepath.Join(dir, "Untitled"+PrprojExt), prodProject)
	writeFile(t, filepath.Join(dir, "subfolder", "nested"+PrprojExt), prodProject)
	writeFile(t, filepath.Join(dir, "subfolder", "nested.prin"), prinSidecar)
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is built by the test itself
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readProdsetFile reads a settings file as text whatever encoding it is in, so
// the assertions below can stay written in readable JSON. Which encoding was
// actually used is asserted separately, by the tests that exist to check it.
func readProdsetFile(t *testing.T, path string) string {
	t.Helper()
	js, err := decodeProdset([]byte(readFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	return js
}

// utf16le renders text the way every Premiere release before 2026 writes a
// .prodset: UTF-16LE, no BOM.
func utf16le(s string) string {
	return string(encodeProdset(s, firstUTF8ProdsetProjectVersion-1))
}

// The whole point of rewriting the .prodset textually rather than re-encoding
// it: both version keys move and NOTHING else does, so the embedded XML blob
// and Adobe's exact formatting survive untouched.
func TestDowngradeProdsetChangesOnlyTheVersionKeys(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "p"+ProdsetExt), prodset2026)
	dst := filepath.Join(dir, "out"+ProdsetExt)
	if err := silent().downgradeProdset(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	got := readProdsetFile(t, dst)
	want := strings.NewReplacer(
		`"mMinCompatibleProjectVersion":45`, `"mMinCompatibleProjectVersion":43`,
		`"mProjectVersion":45`, `"mProjectVersion":43`,
	).Replace(prodset2026)
	if got != want {
		t.Errorf("prodset rewrite touched more than the version keys:\ngot  %s\nwant %s", got, want)
	}
}

// The inline prodset2026 is a reduced sample; this runs the same
// "only-the-version-keys-move" guarantee against a real 2026 .prodset saved by
// Premiere (paths sanitised), which carries the large embedded settings blobs
// the sample omits — the exact bytes a JSON re-encode would mangle.
func TestDowngradeProdsetRealFixtureChangesOnlyVersionKeys(t *testing.T) {
	fixture := filepath.Join("testdata", "fixture_prproduction.prodset")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture %s not present", fixture)
	}
	original := readFile(t, fixture)

	dst := filepath.Join(t.TempDir(), "out"+ProdsetExt)
	if err := silent().downgradeProdset(fixture, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	got := readProdsetFile(t, dst)
	want := strings.NewReplacer(
		`"mMinCompatibleProjectVersion":45`, `"mMinCompatibleProjectVersion":43`,
		`"mProjectVersion":45`, `"mProjectVersion":43`,
	).Replace(original)
	if got != want {
		t.Errorf("real prodset rewrite touched more than the version keys")
	}
	// The fixture itself is an input and must be left exactly as committed.
	if readFile(t, fixture) != original {
		t.Error("the source fixture was modified")
	}
}

// The compatibility floor is lowered to let the target release in, but a
// Production that already declares a wider range keeps it — raising the floor
// would lock out releases that could previously open it.
func TestDowngradeProdsetNeverRaisesCompatibilityFloor(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "p"+ProdsetExt),
		`{"mMinCompatibleProjectVersion":40,"mProjectVersion":45}`)
	dst := filepath.Join(dir, "out"+ProdsetExt)
	if err := silent().downgradeProdset(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	if got, want := readProdsetFile(t, dst), `{"mMinCompatibleProjectVersion":40,"mProjectVersion":43}`; got != want {
		t.Errorf("floor should stay at 40:\ngot  %s\nwant %s", got, want)
	}
}

// 2025 writes the .prodset as UTF-16LE and it has to be readable.
func TestDowngradeProdsetReadsUTF16Input(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "p"+ProdsetExt),
		utf16le(`{"mMinCompatibleProjectVersion":43,"mProjectVersion":43}`))
	dst := filepath.Join(dir, "out"+ProdsetExt)
	if err := silent().downgradeProdset(src, dst, 42, false); err != nil {
		t.Fatal(err)
	}
	if got, want := readProdsetFile(t, dst),
		`{"mMinCompatibleProjectVersion":42,"mProjectVersion":42}`; got != want {
		t.Errorf("UTF-16LE input should downgrade like any other:\ngot  %s\nwant %s", got, want)
	}
}

// The encoding is chosen by the target.
func TestDowngradeProdsetEncodesForTheTargetRelease(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   int
		wantUTF8 bool
	}{
		{"2026 target keeps UTF-8", firstUTF8ProdsetProjectVersion, true},
		{"2025 target re-encodes to UTF-16LE", 43, false},
		{"2024 target re-encodes to UTF-16LE", 42, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Source one release above the target, so resolveTarget accepts it.
			src := writeFile(t, filepath.Join(dir, "p"+ProdsetExt),
				`{"mMinCompatibleProjectVersion":46,"mProjectVersion":46}`)
			dst := filepath.Join(dir, "out"+ProdsetExt)
			if err := silent().downgradeProdset(src, dst, tc.target, false); err != nil {
				t.Fatal(err)
			}
			raw := readFile(t, dst)
			if gotUTF8 := !strings.HasPrefix(raw, "{\x00"); gotUTF8 != tc.wantUTF8 {
				t.Errorf("target %d: got UTF-8=%v, want UTF-8=%v (%q)",
					tc.target, gotUTF8, tc.wantUTF8, raw[:min(12, len(raw))])
			}
			// Whatever the encoding, the document must still read back
			// correctly.
			if got := readProdsetFile(t, dst); !strings.Contains(got,
				fmt.Sprintf(`"mProjectVersion":%d`, tc.target)) {
				t.Errorf("target %d not stamped: %s", tc.target, got)
			}
		})
	}
}

// A UTF-16LE document that ends mid-character means the file is truncated.
func TestDecodeProdsetRefusesTruncatedUTF16(t *testing.T) {
	raw := []byte(utf16le(`{"mProjectVersion":45}`))
	if _, err := decodeProdset(raw[:len(raw)-1]); err == nil {
		t.Error("expected a refusal for a UTF-16 document ending mid-character")
	}
}

// The BOM Adobe does not write, honoured anyway: a settings file that picked
// one up from some other tool should still be read rather than rejected as
// garbage.
func TestDecodeProdsetHonoursBOM(t *testing.T) {
	js := `{"mProjectVersion":43}`
	got, err := decodeProdset(append([]byte{0xff, 0xfe}, []byte(utf16le(js))...))
	if err != nil {
		t.Fatal(err)
	}
	if got != js {
		t.Errorf("BOM should be consumed, not decoded:\ngot  %q\nwant %q", got, js)
	}
}

// "mProjectVersion" is a suffix of "mMinCompatibleProjectVersion", so a pattern
// without the leading quote would match inside it and stamp the wrong key. The
// escaped \" of an embedded XML string is the other near-miss.
func TestProdsetKeyPatternsDoNotOverlap(t *testing.T) {
	js := `{"mMinCompatibleProjectVersion":45,"mProjectVersion":45,` +
		`"blob":"<x a=\"mProjectVersion\">45</x>"}`
	if n := len(prodsetVersionRe.FindAllString(js, -1)); n != 1 {
		t.Errorf("mProjectVersion pattern matched %d times, want 1", n)
	}
	if n := len(prodsetMinCompatRe.FindAllString(js, -1)); n != 1 {
		t.Errorf("mMinCompatibleProjectVersion pattern matched %d times, want 1", n)
	}
}

// A duplicated key is accepted by encoding/json (last one wins) but leaves the
// textual rewrite with two places to stamp and no way to know which Premiere
// reads. Refuse rather than half-stamp the file.
func TestDowngradeProdsetRefusesDuplicateVersionKey(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "p"+ProdsetExt),
		`{"mProjectVersion":45,"mProjectVersion":45}`)
	dst := filepath.Join(dir, "out"+ProdsetExt)
	err := silent().downgradeProdset(src, dst, 43, false)
	if err == nil {
		t.Fatal("expected a refusal for a duplicated version key")
	}
	if !strings.Contains(err.Error(), "found 2") {
		t.Errorf("the error should say how many keys were found: %v", err)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("nothing should be written when the rewrite is refused")
	}
}

// Verbose mode narrates both stamps.
func TestDowngradeProdsetVerboseNarratesBothStamps(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "p"+ProdsetExt), prodset2026)
	var out bytes.Buffer
	d := &Downgrader{Out: &out}
	if err := d.downgradeProdset(src, filepath.Join(dir, "out"+ProdsetExt), 0, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auto target", "mProjectVersion -> 43", "mMinCompatibleProjectVersion -> 43"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("verbose output missing %q:\n%s", want, out.String())
		}
	}
}

func TestParseProdsetRejectsNonProdset(t *testing.T) {
	for name, content := range map[string]string{
		"not JSON":              "<PremiereData Version=\"3\">",
		"JSON without the key":  `{"mAcceleratedRendererID":"x"}`,
		"JSON of the wrong sha": `[]`,
	} {
		if _, err := parseProdset([]byte(content)); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

// A Production downgrade must produce a complete, coherent copy: settings and
// every project stamped to the target, everything else reproduced verbatim, and
// the original left entirely alone.
func TestDowngradeProductionMirrorsWholeFolder(t *testing.T) {
	src := newProduction(t, "MyProduction")
	dst := src + "_downgraded"
	if err := silent().DowngradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}

	// The settings file is renamed to match the output folder (see the dedicated
	// rename test); read it back under that name.
	if got := readProdsetFile(t, filepath.Join(dst, filepath.Base(dst)+ProdsetExt)); !strings.Contains(got, `"mProjectVersion":43`) {
		t.Errorf("settings not downgraded: %s", got)
	}
	for _, rel := range []string{"Untitled" + PrprojExt, filepath.Join("subfolder", "nested"+PrprojExt)} {
		xml := string(gunzipFile(t, filepath.Join(dst, rel)))
		if v := mustGetProjectVersion(t, xml); v != 43 {
			t.Errorf("%s: version = %d, want 43", rel, v)
		}
	}
	// A .prin next to a project must arrive bit-identical
	if got, want := readFile(t, filepath.Join(dst, "subfolder", "nested.prin")), prinSidecar; got != want {
		t.Errorf("sidecar not copied verbatim: %q", got)
	}
	// The source is an input, never an output.
	if got := readFile(t, filepath.Join(src, "MyProduction"+ProdsetExt)); got != prodset2026 {
		t.Error("the original Production was modified")
	}
	if _, err := os.Stat(filepath.Join(src, "MyProduction_downgraded"+ProdsetExt)); err == nil {
		t.Error("a stray downgraded settings file was left in the original folder")
	}
}

// Premiere identifies a Production by the .prodset whose basename matches the
// folder, so the mirrored settings file must be renamed to track the renamed
// output folder — otherwise Premiere does not recognise the copy as a
// Production. The original name must not survive alongside it.
func TestDowngradeProductionRenamesProdsetToFolder(t *testing.T) {
	src := newProduction(t, "MyProduction")
	dst := src + "_downgraded"
	if err := silent().DowngradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dst, filepath.Base(dst)+ProdsetExt) // MyProduction_downgraded.prodset
	if _, err := os.Stat(want); err != nil {
		t.Errorf("settings file should be renamed to match the folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "MyProduction"+ProdsetExt)); err == nil {
		t.Error("the original settings-file name should not remain in the output")
	}
	// The unique-suffix case: a taken output name means the folder becomes
	// "..._downgraded-1", and the settings file must track that exact basename.
	if err := os.Mkdir(src+"_downgraded", 0o755); err == nil { //nolint:gosec // test setup
		_ = err
	}
	dst2 := UniqueDir(src + "_downgraded")
	if err := silent().DowngradeProduction(src, dst2, 43, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst2, filepath.Base(dst2)+ProdsetExt)); err != nil {
		t.Errorf("settings file should track the suffixed folder name %s: %v", filepath.Base(dst2), err)
	}
}

// Every file in a Production is stamped with one version, taken from the
// settings file — not resolved per project. A project that is already at or
// below that version has nothing to downgrade and must still be copied through,
// or the mirrored Production would be missing a project.
func TestDowngradeProductionCopiesAlreadyOldProjects(t *testing.T) {
	src := newProduction(t, "Mixed")
	writeFile(t, filepath.Join(src, "old"+PrprojExt),
		strings.Replace(prodProject, `Version="45"`, `Version="41"`, 1))
	dst := src + "_downgraded"
	if err := silent().DowngradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	xml := readFile(t, filepath.Join(dst, "old"+PrprojExt))
	if v := mustGetProjectVersion(t, xml); v != 41 {
		t.Errorf("an already-older project should be copied unchanged, got version %d", v)
	}
}

// A Production already at the floor (2020.1) has no earlier Production-capable
// release to land on: with no --to, its auto target is one step below the
// floor, so the same guard refuses it. This is the auto-path route into
// checkProductionTarget, distinct from an explicit pre-Productions --to.
func TestProductionAtFloorRefusesAutoTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "AtFloor")
	writeFile(t, filepath.Join(dir, "AtFloor"+ProdsetExt),
		`{"mMinCompatibleProjectVersion":38,"mProjectVersion":38}`)
	writeFile(t, filepath.Join(dir, "a"+PrprojExt),
		strings.Replace(prodProject, `Version="45"`, `Version="38"`, 1))
	dst := dir + "_downgraded"
	err := silent().DowngradeProduction(dir, dst, 0, false) // 0 => auto
	if err == nil {
		t.Fatal("a Production already at the oldest Production release cannot be downgraded")
	}
	if !strings.Contains(err.Error(), "Productions") {
		t.Errorf("unhelpful error: %v", err)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("nothing should have been written for a Production that cannot be downgraded")
	}
}

// Productions arrived in Premiere Pro 14.1 (April 2020) = version 38. Older
// releases cannot open one at all, so stamping down to them would produce a
// folder no Premiere will ever read — refused (before the output folder is
// created).
func TestProductionRefusesTargetsPredatingProductions(t *testing.T) {
	src := newProduction(t, "Old")
	dst := src + "_downgraded"
	err := silent().DowngradeProduction(src, dst, 35, false)
	if err == nil {
		t.Fatal("expected a refusal for a pre-Productions target")
	}
	if !strings.Contains(err.Error(), "Productions") {
		t.Errorf("unhelpful error: %v", err)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Error("nothing should have been written for a refused target")
	}
	// The floor itself is allowed.
	if err := silent().DowngradeProduction(src, dst, firstProductionProjectVersion, false); err != nil {
		t.Errorf("version %d is the first release with Productions and must be allowed: %v",
			firstProductionProjectVersion, err)
	}
}

// A lone .prproj is not a Production and keeps the full release range: the
// pre-2020 guard must not leak into the plain-file path.
func TestPlainProjectStillAllowsPreProductionTargets(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "a"+PrprojExt), prodProject)
	if err := silent().Downgrade(src, filepath.Join(dir, "out"+PrprojExt), 35, false); err != nil {
		t.Errorf("a lone project should still downgrade to a pre-Productions release: %v", err)
	}
}

func TestFindProdsetRequiresExactlyOne(t *testing.T) {
	dir := t.TempDir()
	if _, err := findProdset(dir); err == nil {
		t.Error("a folder with no settings file is not a Production")
	}
	writeFile(t, filepath.Join(dir, "a"+ProdsetExt), prodset2026)
	if got, err := findProdset(dir); err != nil || filepath.Base(got) != "a"+ProdsetExt {
		t.Errorf("findProdset = %q, %v", got, err)
	}
	writeFile(t, filepath.Join(dir, "b"+ProdsetExt), prodset2026)
	if _, err := findProdset(dir); err == nil {
		t.Error("two settings files are ambiguous and must be refused")
	}
}

// A Production whose settings file is unreadable as JSON fails before the
// output folder is created, so a corrupt input never leaves a half-built
// Production behind.
func TestDowngradeProductionRefusesCorruptProdset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Broken")
	writeFile(t, filepath.Join(dir, "Broken"+ProdsetExt), "{not json")
	writeFile(t, filepath.Join(dir, "a"+PrprojExt), prodProject)
	dst := dir + "_downgraded"
	if err := silent().DowngradeProduction(dir, dst, 43, false); err == nil {
		t.Fatal("expected an error for a corrupt settings file")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("no output folder should be created when the settings file is unreadable")
	}
}

// One bad project must not cost the user the rest of the Production, but must
// be reported as a failure.
func TestDowngradeProductionReportsPartialFailure(t *testing.T) {
	src := newProduction(t, "Partial")
	writeFile(t, filepath.Join(src, "corrupt"+PrprojExt), "not a premiere project at all")
	dst := src + "_downgraded"
	err := silent().DowngradeProduction(src, dst, 43, false)
	if err == nil {
		t.Fatal("a Production with an unconvertible project should report failure")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("the error should say the output is incomplete: %v", err)
	}
	// The good files still made it, so the user can salvage the run.
	if _, statErr := os.Stat(filepath.Join(dst, "Untitled"+PrprojExt)); statErr != nil {
		t.Errorf("the other projects should still have been written: %v", statErr)
	}
}

// Symlinks are recreated as links rather than followed, so a link pointing
// outside the Production is not silently inlined into the copy.
func TestDowngradeProductionPreservesSymlinks(t *testing.T) {
	src := newProduction(t, "Links")
	linkTarget := filepath.Join("subfolder", "nested.prin")
	if err := os.Symlink(linkTarget, filepath.Join(src, "link.prin")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dst := src + "_downgraded"
	if err := silent().DowngradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dst, "link.prin"))
	if err != nil {
		t.Fatalf("link.prin should still be a symlink: %v", err)
	}
	if target != linkTarget {
		t.Errorf("symlink target = %q, want %q", target, linkTarget)
	}
}

// UniqueDir must not split a directory name on ".", or a Production folder with
// a dot in its name would get the suffix wedged into the middle.
func TestUniqueDirDoesNotSplitOnDots(t *testing.T) {
	dir := t.TempDir()
	taken := filepath.Join(dir, "my.big.production")
	if err := os.Mkdir(taken, 0o750); err != nil {
		t.Fatal(err)
	}
	if got, want := UniqueDir(taken), taken+"-1"; got != want {
		t.Errorf("UniqueDir = %q, want %q", got, want)
	}
}
