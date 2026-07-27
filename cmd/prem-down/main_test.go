package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Lucuma13/prem-down/internal/premdown"
	"github.com/Lucuma13/prem-down/internal/updatechecker"
)

// testCLI is a cli whose streams are in-memory buffers, so a test can drive
// run/downgrade/integrate directly (the cli methods are promoted) and inspect
// exactly what was written — no pipe redirection, process-exit seam, or global
// save/restore. out and err shadow the embedded writers with their concrete
// buffer type so a test can read them back.
type testCLI struct {
	*cli
	out *bytes.Buffer // captured stdout
	err *bytes.Buffer // captured stderr
}

// newTestCLI builds a testCLI; stdin seeds the reader the --gui pause consumes.
//
// The update check is wired to a settings file under t.TempDir and to an Ask
// that always declines, so a --gui test can never read or write the real
// settings file, reach the network, or raise an osascript dialog mid-test.
// Tests that exercise the check itself override these fields.
func newTestCLI(t *testing.T, stdin string) *testCLI {
	t.Helper()
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	// A fixed release-shaped version, not the build's own: the real one is "dev"
	// unless ldflags stamp it, and the checker treats an uncomparable version as
	// a dev build and does nothing — which would silently neuter these tests.
	updates := updatechecker.New(githubRepo, "prem-down", "1.0.0")
	updates.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	updates.Ask = func(string, io.Reader, io.Writer) bool { return false }
	return &testCLI{
		cli: &cli{out: out, err: errBuf, in: strings.NewReader(stdin), updates: updates},
		out: out,
		err: errBuf,
	}
}

func TestUsage(t *testing.T) {
	var b strings.Builder
	usage(&b)
	got := b.String()
	// The help must name the tool, the --to option, and give live release
	// examples (so a stale hard-coded list can't silently drift from releases).
	for _, want := range []string{"Usage: prem-down", "--to", "--verbose", "--version", "integrate", "auto-update", premdown.ReleaseExamples()} {
		if !strings.Contains(got, want) {
			t.Errorf("usage() output missing %q:\n%s", want, got)
		}
	}
}

// --------------------------------------------------------------------------
// run() — the CLI arg parser and dispatch, driven over an in-memory cli.
//
// Because run/fatal write through the cli's injected streams and thread the
// exit code back to main (rather than calling os.Exit mid-stack), every fatal
// branch and the whole run() arg parser is reachable in-process: build a cli
// over buffers with newTestCLI, call run, and read the code and the captured
// output back directly — no pipe, panic seam, or global swapping.
// --------------------------------------------------------------------------

func TestRunHelpAndVersion(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		c := newTestCLI(t, "")
		if code := c.run([]string{arg}); code != 0 {
			t.Errorf("%s: want clean exit 0, got code=%d", arg, code)
		}
		if !strings.Contains(c.out.String(), "Usage: prem-down") {
			t.Errorf("%s: help not printed:\n%s", arg, c.out)
		}
	}
	c := newTestCLI(t, "")
	if code := c.run([]string{"--version"}); code != 0 {
		t.Errorf("--version: want 0, got code=%d", code)
	}
	if !strings.Contains(c.out.String(), "prem-down "+version) {
		t.Errorf("--version not printed: %q", c.out)
	}
}

func TestRunNoPositionalsReturns2(t *testing.T) {
	// A flag but no input file: usage to c.err, exit code 2.
	c := newTestCLI(t, "")
	if code := c.run([]string{"-v"}); code != 2 {
		t.Errorf("no input files should return 2, got code=%d", code)
	}
	if !strings.Contains(c.err.String(), "Usage:") {
		t.Errorf("usage not printed to c.err:\n%s", c.err)
	}
}

func TestRunUnknownOptionExits(t *testing.T) {
	c := newTestCLI(t, "")
	if code := c.run([]string{"--nope"}); code != 1 {
		t.Errorf("unknown option should fatal 1, got code=%d", code)
	}
	if !strings.Contains(c.err.String(), "unknown option") {
		t.Errorf("missing diagnostic:\n%s", c.err)
	}
}

func TestRunToRequiresValueExits(t *testing.T) {
	// Both the space form with nothing after it and an explicit-but-empty
	// "--to=" must be rejected ("--to=" would otherwise silently mean auto).
	for _, args := range [][]string{{"--to"}, {"--to=", "in.prproj"}} {
		c := newTestCLI(t, "")
		if code := c.run(args); code != 1 {
			t.Errorf("%v: --to without a value should fatal 1, got code=%d", args, code)
		}
		if !strings.Contains(c.err.String(), "--to requires a value") {
			t.Errorf("%v: missing diagnostic:\n%s", args, c.err)
		}
	}
}

// One corrupt project in a multi-file batch must fail only that file: the rest
// still convert, the process does not exit, and the corrupt one is named.
func TestRunBatchContinuesPastCorruptFile(t *testing.T) {
	dir := t.TempDir()
	// Recognisably a Premiere file, but structurally corrupt (no <Project> tag).
	bad := filepath.Join(dir, "bad.prproj")
	if err := os.WriteFile(bad, []byte(`<PremiereData Version="3"></PremiereData>`), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.prproj")
	const xml = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	if err := os.WriteFile(good, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}

	// The corrupt file comes first, so aborting there would skip the good one.
	c := newTestCLI(t, "")
	if code := c.run([]string{"--to=2023", bad, good}); code != 1 {
		t.Errorf("batch with a failure should return 1, got %d", code)
	}
	if !strings.Contains(c.err.String(), "bad.prproj") {
		t.Errorf("the corrupt file is not named in the diagnostic:\n%s", c.err)
	}
	if _, err := os.Stat(strings.TrimSuffix(good, ".prproj") + "_downgraded.prproj"); err != nil {
		t.Errorf("the good file was not converted after the corrupt one failed: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(bad, ".prproj") + "_downgraded.prproj"); err == nil {
		t.Error("no output should be written for the corrupt file")
	}
}

func TestRunMissingFileReturns1(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.prproj")
	c := newTestCLI(t, "")
	if code := c.run([]string{missing}); code != 1 {
		t.Errorf("a missing input should return 1, got code=%d", code)
	}
	if !strings.Contains(c.err.String(), "not found") {
		t.Errorf("missing diagnostic:\n%s", c.err)
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
	c := newTestCLI(t, "")
	if code := c.run(args); code != 0 {
		t.Fatalf("batch should succeed with 0, got code=%d", code)
	}
	if !strings.Contains(c.out.String(), "wrote ") {
		t.Errorf("no downgrade output:\n%s", c.out)
	}
	for _, in := range inputs {
		out := strings.TrimSuffix(in, ".prproj") + "_downgraded.prproj"
		if _, err := os.Stat(out); err != nil {
			t.Errorf("expected output %s to be written: %v", out, err)
		}
	}
}

// --gui makes run wait for Enter before returning (the OS context menu opens a
// console that would otherwise vanish). The injected stdin already holds a
// newline so the pause returns; this covers the gui branch of pauseIfGUI.
func TestRunGUIPauses(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.prproj")
	const xml = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	if err := os.WriteFile(src, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	c := newTestCLI(t, "\n")
	if code := c.run([]string{"--gui", "--to=2023", src}); code != 0 {
		t.Fatalf("gui run should return 0, got code=%d", code)
	}
	if !c.gui {
		t.Error("--gui should have set the gui flag")
	}
}

// The space-separated "--to RELEASE" form (distinct from "--to="), combined with
// an input that exists but isn't a Premiere project: downgrade fails, run reports
// it and returns 1.
func TestRunToSpaceFormAndDowngradeError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.prproj")
	if err := os.WriteFile(src, []byte("not a premiere project"), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	c := newTestCLI(t, "")
	if code := c.run([]string{"--to", "2023", src}); code != 1 {
		t.Fatalf("a failed downgrade should return 1, got code=%d", code)
	}
	if !strings.Contains(c.err.String(), "error:") {
		t.Errorf("downgrade failure not reported:\n%s", c.err)
	}
}

// run dispatches the "integrate" subcommand; --help there is a clean no-op.
// HOME points at a temp dir so nothing touches the real Services folder.
func TestRunIntegrateDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := newTestCLI(t, "")
	if code := c.run([]string{"integrate", "-h"}); code != 0 {
		t.Fatalf("integrate -h should return 0, got code=%d", code)
	}
	if !strings.Contains(c.out.String(), "integrate") {
		t.Errorf("integrate help not printed:\n%s", c.out)
	}
}

// --------------------------------------------------------------------------
// Production fixtures for the plan/run tests. Deliberately leaner than the
// engine package's equivalents: these exercise how inputs become jobs, not the
// byte-fidelity of a mirrored Production, so no sidecar or nested tree is
// needed here.
// --------------------------------------------------------------------------

const prodsetFixture = `{"mMinCompatibleProjectVersion":45,"mProjectVersion":45}`

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

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is built by the test itself
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// utf16le renders text the way Premiere writes a .prodset before 2026: UTF-16LE,
// no BOM.
func utf16le(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return string(b)
}

// newProduction lays out a minimal Production: the settings file, a project
// beside it, and one nested a folder down so "covered by a Production" has a
// subtree to match against.
func newProduction(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	writeFile(t, filepath.Join(dir, name+premdown.ProdsetExt), prodsetFixture)
	writeFile(t, filepath.Join(dir, "Untitled"+premdown.PrprojExt), prodProject)
	writeFile(t, filepath.Join(dir, "subfolder", "nested"+premdown.PrprojExt), prodProject)
	return dir
}

// The context menu is keyed on the .prodset file, so the natural gesture is to
// select it together with the projects. Those projects are already inside the
// Production being mirrored; downgrading them again would scatter stray
// _downgraded.prproj files through the user's original folder.
func TestPlanSkipsProjectsCoveredByAProduction(t *testing.T) {
	src := newProduction(t, "Sel")
	c := newTestCLI(t, "")
	jobs, failed := c.plan([]string{
		filepath.Join(src, "Sel"+premdown.ProdsetExt),
		filepath.Join(src, "Untitled"+premdown.PrprojExt),
		filepath.Join(src, "subfolder", "nested"+premdown.PrprojExt),
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
	jobs, failed := newTestCLI(t, "").plan([]string{
		filepath.Join(a, "A"+premdown.ProdsetExt),
		filepath.Join(b, "B"+premdown.ProdsetExt),
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
	jobs, _ := newTestCLI(t, "").plan([]string{src, filepath.Join(src, "Dup"+premdown.ProdsetExt)})
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d: %+v", len(jobs), jobs)
	}
}

// A project outside every named Production is its own job, even when a
// Production is also selected.
func TestPlanKeepsProjectsOutsideTheProduction(t *testing.T) {
	src := newProduction(t, "Inside")
	lone := writeFile(t, filepath.Join(t.TempDir(), "lone"+premdown.PrprojExt), prodProject)
	jobs, _ := newTestCLI(t, "").plan([]string{src, lone})
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d: %+v", len(jobs), jobs)
	}
}

// End to end through run: the .prodset argument the context menu passes, with
// no --to, must land a downgraded Production in the sibling folder.
func TestRunDowngradesProductionFromProdsetArgument(t *testing.T) {
	src := newProduction(t, "E2E")
	c := newTestCLI(t, "")
	if code := c.run([]string{filepath.Join(src, "E2E"+premdown.ProdsetExt)}); code != 0 {
		t.Fatalf("run should succeed, got code=%d\n%s", code, c.err)
	}
	dst := src + "_downgraded"
	// The settings land in the encoding the target release reads — UTF-16LE for
	// anything before 2026 — so the stamped key is matched in that form.
	settings := readFile(t, filepath.Join(dst, filepath.Base(dst)+premdown.ProdsetExt))
	if want := utf16le(`"mProjectVersion":43`); !strings.Contains(settings, want) {
		t.Errorf("auto target should be 2025 (43), UTF-16LE encoded: %q", settings)
	}
	if !strings.Contains(c.out.String(), "wrote "+dst) {
		t.Errorf("the output folder should be reported:\n%s", c.out)
	}
}

// A folder that holds no settings file is not a Production; run reports it and
// exits non-zero rather than inventing one.
func TestRunRejectsNonProductionFolder(t *testing.T) {
	dir := t.TempDir()
	c := newTestCLI(t, "")
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
	prodset := filepath.Join(src, "Twice"+premdown.ProdsetExt)
	c := newTestCLI(t, "")
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

// --------------------------------------------------------------------------
// Opt-in update check, as wired into run()
// --------------------------------------------------------------------------

// writeProject drops a minimal but valid project at dir/name.
func writeProject(t *testing.T, dir, name string) string {
	t.Helper()
	const xml = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="42">
</Project>
</PremiereData>`
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	return path
}

// run dispatches "auto-update" the same way it dispatches "integrate" — before
// any flag parsing, so the word is never mistaken for an input file.
func TestRunDispatchesAutoUpdate(t *testing.T) {
	c := newTestCLI(t, "")
	if code := c.run([]string{"auto-update"}); code != 0 {
		t.Fatalf("auto-update should return 0, got code=%d", code)
	}
	if !strings.Contains(c.out.String(), "auto-update:") {
		t.Errorf("status not printed:\n%s", c.out)
	}
	c = newTestCLI(t, "")
	if code := c.run([]string{"auto-update", "on"}); code != 0 {
		t.Fatalf("auto-update on should return 0, got code=%d", code)
	}
	if !strings.Contains(c.out.String(), "auto-update: on") {
		t.Errorf("setting not applied:\n%s", c.out)
	}
}

// The question belongs to the context-menu surfaces, which is what --gui marks.
// A terminal run converts and says nothing: that user has the subcommand.
func TestRunAsksAboutUpdatesOnlyFromTheFileManager(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantAsks int
	}{
		{"terminal", []string{"--to=2023"}, 0},
		{"file manager", []string{"--gui", "--to=2023"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCLI(t, "")
			asks := 0
			c.updates.Ask = func(string, io.Reader, io.Writer) bool { asks++; return false }
			src := writeProject(t, t.TempDir(), "in.prproj")
			if code := c.run(append(tc.args, src)); code != 0 {
				t.Fatalf("want a clean run, got code=%d err=%s", code, c.err)
			}
			if asks != tc.wantAsks {
				t.Errorf("want %d questions, got %d", tc.wantAsks, asks)
			}
		})
	}
}

// A run that reported an error is no place to ask about update checks: the
// result is already a caution dialog on macOS and a red console on Windows.
func TestRunSkipsTheUpdateCheckAfterAFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.prproj")
	if err := os.WriteFile(src, []byte("not a premiere project"), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	c := newTestCLI(t, "")
	c.updates.Ask = func(string, io.Reader, io.Writer) bool {
		t.Error("a failed run must not raise the update question")
		return true
	}
	if code := c.run([]string{"--gui", src}); code != 1 {
		t.Fatalf("a failed downgrade should return 1, got code=%d", code)
	}
}
