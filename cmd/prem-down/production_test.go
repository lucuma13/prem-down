// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
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

// newProduction lays out a Production: settings file at the top, a project
// beside it, a project nested a folder down, and a non-project file that must
// survive the trip byte-for-byte.
func newProduction(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	writeFile(t, filepath.Join(dir, name+prodsetExt), prodset2026)
	writeFile(t, filepath.Join(dir, "Untitled"+prprojExt), prodProject)
	writeFile(t, filepath.Join(dir, "subfolder", "nested"+prprojExt), prodProject)
	writeFile(t, filepath.Join(dir, "media", "clip.mov"), "\x00\x01binary media\xff")
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

// The whole point of rewriting the .prodset textually rather than re-encoding
// it: both version keys move and NOTHING else does, so the embedded XML blob
// and Adobe's exact formatting survive untouched.
func TestDowngradeProdsetChangesOnlyTheVersionKeys(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "p"+prodsetExt), prodset2026)
	dst := filepath.Join(dir, "out"+prodsetExt)
	if err := dcli().downgradeProdset(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dst)
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

	dst := filepath.Join(t.TempDir(), "out"+prodsetExt)
	if err := dcli().downgradeProdset(fixture, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dst)
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
	src := writeFile(t, filepath.Join(dir, "p"+prodsetExt),
		`{"mMinCompatibleProjectVersion":40,"mProjectVersion":45}`)
	dst := filepath.Join(dir, "out"+prodsetExt)
	if err := dcli().downgradeProdset(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, dst), `{"mMinCompatibleProjectVersion":40,"mProjectVersion":43}`; got != want {
		t.Errorf("floor should stay at 40:\ngot  %s\nwant %s", got, want)
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
	src := writeFile(t, filepath.Join(dir, "p"+prodsetExt),
		`{"mProjectVersion":45,"mProjectVersion":45}`)
	dst := filepath.Join(dir, "out"+prodsetExt)
	err := dcli().downgradeProdset(src, dst, 43, false)
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

// Verbose mode narrates both stamps, since a user checking why 2025 still
// refuses a Production needs to see that the compatibility floor moved too.
func TestDowngradeProdsetVerboseNarratesBothStamps(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "p"+prodsetExt), prodset2026)
	c := newTestCLI("")
	if err := c.downgradeProdset(src, filepath.Join(dir, "out"+prodsetExt), 0, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auto target", "mProjectVersion -> 43", "mMinCompatibleProjectVersion -> 43"} {
		if !strings.Contains(c.out.String(), want) {
			t.Errorf("verbose output missing %q:\n%s", want, c.out)
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
	c := newTestCLI("")
	if err := c.downgradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}

	// The settings file is renamed to match the output folder (see the dedicated
	// rename test); read it back under that name.
	if got := readFile(t, filepath.Join(dst, filepath.Base(dst)+prodsetExt)); !strings.Contains(got, `"mProjectVersion":43`) {
		t.Errorf("settings not downgraded: %s", got)
	}
	for _, rel := range []string{"Untitled" + prprojExt, filepath.Join("subfolder", "nested"+prprojExt)} {
		xml := string(gunzipFile(t, filepath.Join(dst, rel)))
		if v := mustGetProjectVersion(t, xml); v != 43 {
			t.Errorf("%s: version = %d, want 43", rel, v)
		}
	}
	// Non-project files are the reason the copy is worth making: non-project files inside a
	// Production must arrive bit-identical or the copy is not usable.
	if got, want := readFile(t, filepath.Join(dst, "media", "clip.mov")), "\x00\x01binary media\xff"; got != want {
		t.Errorf("media not copied verbatim: %q", got)
	}
	// The source is an input, never an output.
	if got := readFile(t, filepath.Join(src, "MyProduction"+prodsetExt)); got != prodset2026 {
		t.Error("the original Production was modified")
	}
	if _, err := os.Stat(filepath.Join(src, "MyProduction_downgraded"+prodsetExt)); err == nil {
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
	if err := dcli().downgradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dst, filepath.Base(dst)+prodsetExt) // MyProduction_downgraded.prodset
	if _, err := os.Stat(want); err != nil {
		t.Errorf("settings file should be renamed to match the folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "MyProduction"+prodsetExt)); err == nil {
		t.Error("the original settings-file name should not remain in the output")
	}
	// The unique-suffix case: a taken output name means the folder becomes
	// "..._downgraded-1", and the settings file must track that exact basename.
	if err := os.Mkdir(src+"_downgraded", 0o755); err == nil { //nolint:gosec // test setup
		_ = err
	}
	dst2 := uniqueDir(src + "_downgraded")
	if err := dcli().downgradeProduction(src, dst2, 43, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst2, filepath.Base(dst2)+prodsetExt)); err != nil {
		t.Errorf("settings file should track the suffixed folder name %s: %v", filepath.Base(dst2), err)
	}
}

// Every file in a Production is stamped with one version, taken from the
// settings file — not resolved per project. A project that is already at or
// below that version has nothing to downgrade and must still be copied through,
// or the mirrored Production would be missing a project.
func TestDowngradeProductionCopiesAlreadyOldProjects(t *testing.T) {
	src := newProduction(t, "Mixed")
	writeFile(t, filepath.Join(src, "old"+prprojExt),
		strings.Replace(prodProject, `Version="45"`, `Version="41"`, 1))
	dst := src + "_downgraded"
	if err := dcli().downgradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	xml := readFile(t, filepath.Join(dst, "old"+prprojExt))
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
	writeFile(t, filepath.Join(dir, "AtFloor"+prodsetExt),
		`{"mMinCompatibleProjectVersion":38,"mProjectVersion":38}`)
	writeFile(t, filepath.Join(dir, "a"+prprojExt),
		strings.Replace(prodProject, `Version="45"`, `Version="38"`, 1))
	dst := dir + "_downgraded"
	err := dcli().downgradeProduction(dir, dst, 0, false) // 0 => auto
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
	err := dcli().downgradeProduction(src, dst, 35, false)
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
	if err := dcli().downgradeProduction(src, dst, firstProductionProjectVersion, false); err != nil {
		t.Errorf("version %d is the first release with Productions and must be allowed: %v",
			firstProductionProjectVersion, err)
	}
}

// A lone .prproj is not a Production and keeps the full release range: the
// pre-2020 guard must not leak into the plain-file path.
func TestPlainProjectStillAllowsPreProductionTargets(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, filepath.Join(dir, "a"+prprojExt), prodProject)
	if err := dcli().downgrade(src, filepath.Join(dir, "out"+prprojExt), 35, false); err != nil {
		t.Errorf("a lone project should still downgrade to a pre-Productions release: %v", err)
	}
}

func TestFindProdsetRequiresExactlyOne(t *testing.T) {
	dir := t.TempDir()
	if _, err := findProdset(dir); err == nil {
		t.Error("a folder with no settings file is not a Production")
	}
	writeFile(t, filepath.Join(dir, "a"+prodsetExt), prodset2026)
	if got, err := findProdset(dir); err != nil || filepath.Base(got) != "a"+prodsetExt {
		t.Errorf("findProdset = %q, %v", got, err)
	}
	writeFile(t, filepath.Join(dir, "b"+prodsetExt), prodset2026)
	if _, err := findProdset(dir); err == nil {
		t.Error("two settings files are ambiguous and must be refused")
	}
}

// The context menu is keyed on the .prodset file, so the natural gesture is to
// select it together with the projects. Those projects are already inside the
// Production being mirrored; downgrading them again would scatter stray
// _downgraded.prproj files through the user's original folder.
func TestPlanSkipsProjectsCoveredByAProduction(t *testing.T) {
	src := newProduction(t, "Sel")
	c := newTestCLI("")
	jobs, failed := c.plan([]string{
		filepath.Join(src, "Sel"+prodsetExt),
		filepath.Join(src, "Untitled"+prprojExt),
		filepath.Join(src, "subfolder", "nested"+prprojExt),
	})
	if failed {
		t.Error("no input was missing; nothing should have failed")
	}
	if len(jobs) != 1 || !jobs[0].production || jobs[0].path != src {
		t.Fatalf("expected one Production job for %s, got %+v", src, jobs)
	}
	if !strings.Contains(c.out.String(), "already part of the Production") {
		t.Errorf("the skip should be explained to the user:\n%s", c.out)
	}
}

// Selecting two Productions at once downgrades both.
func TestPlanAcceptsMultipleProductions(t *testing.T) {
	a := newProduction(t, "A")
	b := newProduction(t, "B")
	jobs, failed := newTestCLI("").plan([]string{
		filepath.Join(a, "A"+prodsetExt),
		filepath.Join(b, "B"+prodsetExt),
	})
	if failed {
		t.Error("nothing was missing; the plan should not have failed")
	}
	if len(jobs) != 2 {
		t.Fatalf("expected one job per Production, got %d: %+v", len(jobs), jobs)
	}
	for _, j := range jobs {
		if !j.production {
			t.Errorf("job %q should be a Production", j.path)
		}
	}
}

// Naming the folder and its settings file together is one Production, not two —
// otherwise the second pass would write a redundant "_downgraded-1" copy.
func TestPlanDeduplicatesFolderAndProdset(t *testing.T) {
	src := newProduction(t, "Dup")
	jobs, _ := newTestCLI("").plan([]string{src, filepath.Join(src, "Dup"+prodsetExt)})
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d: %+v", len(jobs), jobs)
	}
}

// A project outside every named Production is its own job, even when a
// Production is also selected.
func TestPlanKeepsProjectsOutsideTheProduction(t *testing.T) {
	src := newProduction(t, "Inside")
	lone := writeFile(t, filepath.Join(t.TempDir(), "lone"+prprojExt), prodProject)
	jobs, _ := newTestCLI("").plan([]string{src, lone})
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d: %+v", len(jobs), jobs)
	}
}

// End to end through run: the .prodset argument the context menu passes, with
// no --to, must land a downgraded Production in the sibling folder.
func TestRunDowngradesProductionFromProdsetArgument(t *testing.T) {
	src := newProduction(t, "E2E")
	c := newTestCLI("")
	if code := c.run([]string{filepath.Join(src, "E2E"+prodsetExt)}); code != 0 {
		t.Fatalf("run should succeed, got code=%d\n%s", code, c.err)
	}
	dst := src + "_downgraded"
	if got := readFile(t, filepath.Join(dst, filepath.Base(dst)+prodsetExt)); !strings.Contains(got, `"mProjectVersion":43`) {
		t.Errorf("auto target should be 2025 (43): %s", got)
	}
	if !strings.Contains(c.out.String(), "wrote "+dst) {
		t.Errorf("the output folder should be reported:\n%s", c.out)
	}
}

// A folder that holds no settings file is not a Production; run reports it and
// exits non-zero rather than inventing one.
func TestRunRejectsNonProductionFolder(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI("")
	if code := c.run([]string{dir}); code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(c.err.String(), "not a Premiere Production") {
		t.Errorf("unhelpful diagnostic:\n%s", c.err)
	}
}

// The output folder must be one we create. If the name is taken, run picks a
// free one rather than merging into or overwriting whatever is there.
func TestRunPicksAFreeOutputFolder(t *testing.T) {
	src := newProduction(t, "Twice")
	prodset := filepath.Join(src, "Twice"+prodsetExt)
	c := newTestCLI("")
	if code := c.run([]string{prodset}); code != 0 {
		t.Fatalf("first run failed: %s", c.err)
	}
	if code := c.run([]string{prodset}); code != 0 {
		t.Fatalf("second run failed: %s", c.err)
	}
	for _, dir := range []string{src + "_downgraded", src + "_downgraded-1"} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("expected %s to exist: %v", dir, err)
		}
	}
}

// A Production whose settings file is unreadable as JSON fails before the
// output folder is created, so a corrupt input never leaves a half-built
// Production behind.
func TestDowngradeProductionRefusesCorruptProdset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Broken")
	writeFile(t, filepath.Join(dir, "Broken"+prodsetExt), "{not json")
	writeFile(t, filepath.Join(dir, "a"+prprojExt), prodProject)
	dst := dir + "_downgraded"
	if err := dcli().downgradeProduction(dir, dst, 43, false); err == nil {
		t.Fatal("expected an error for a corrupt settings file")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("no output folder should be created when the settings file is unreadable")
	}
}

// One bad project must not cost the user the rest of the Production, but the
// result is an incomplete Production and must be reported as a failure rather
// than presented as a finished copy.
func TestDowngradeProductionReportsPartialFailure(t *testing.T) {
	src := newProduction(t, "Partial")
	writeFile(t, filepath.Join(src, "corrupt"+prprojExt), "not a premiere project at all")
	dst := src + "_downgraded"
	c := newTestCLI("")
	err := c.downgradeProduction(src, dst, 43, false)
	if err == nil {
		t.Fatal("a Production with an unconvertible project should report failure")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("the error should say the output is incomplete: %v", err)
	}
	// The good files still made it, so the user can salvage the run.
	if _, statErr := os.Stat(filepath.Join(dst, "Untitled"+prprojExt)); statErr != nil {
		t.Errorf("the other projects should still have been written: %v", statErr)
	}
}

// Symlinks are recreated as links rather than followed, so a link pointing
// outside the Production is not silently inlined into the copy.
func TestDowngradeProductionPreservesSymlinks(t *testing.T) {
	src := newProduction(t, "Links")
	if err := os.Symlink(filepath.Join("media", "clip.mov"), filepath.Join(src, "link.mov")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dst := src + "_downgraded"
	if err := dcli().downgradeProduction(src, dst, 43, false); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dst, "link.mov"))
	if err != nil {
		t.Fatalf("link.mov should still be a symlink: %v", err)
	}
	if want := filepath.Join("media", "clip.mov"); target != want {
		t.Errorf("symlink target = %q, want %q", target, want)
	}
}

// uniqueDir must not split a directory name on ".", or a Production folder with
// a dot in its name would get the suffix wedged into the middle.
func TestUniqueDirDoesNotSplitOnDots(t *testing.T) {
	dir := t.TempDir()
	taken := filepath.Join(dir, "my.big.production")
	if err := os.Mkdir(taken, 0o750); err != nil {
		t.Fatal(err)
	}
	if got, want := uniqueDir(taken), taken+"-1"; got != want {
		t.Errorf("uniqueDir = %q, want %q", got, want)
	}
}
