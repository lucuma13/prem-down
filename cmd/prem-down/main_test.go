package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf16"

	"github.com/lucuma13/prem-down/internal/premdown"
	"github.com/lucuma13/prem-down/internal/updates"
)

// testCLI is a cli whose streams are in-memory buffers, so a test can drive
// run/downgrade/integrate directly (the cli methods are promoted) and inspect
// exactly what was written - no pipe redirection, process-exit seam, or global
// save/restore. out and err shadow the embedded writers with their concrete
// buffer type so a test can read them back.
type testCLI struct {
	*cli
	out *bytes.Buffer // captured stdout
	err *bytes.Buffer // captured stderr
}

// newTestCLI builds a testCLI; stdin seeds anything that prompts (nothing does
// on macOS or Windows, where both prompts are dialogs, but the update checker
// takes a reader for hosts whose prompt is a console).
//
// The update check is wired to a settings file under t.TempDir and to an Ask
// that always declines, so a --gui test can never read or write the real
// settings file, reach the network, or raise a dialog mid-test. Tests that
// exercise the check itself override these fields.
func newTestCLI(t *testing.T, stdin string) *testCLI {
	t.Helper()
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	// A fixed release-shaped version, not the build's own: the real one is "dev"
	// unless ldflags stamp it, and the checker treats an uncomparable version as
	// a dev build and does nothing - which would silently neuter these tests.
	checker := updates.New(githubRepo, "prem-down", "1.0.0")
	checker.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	checker.Ask = func(string, io.Reader, io.Writer) bool { return false }
	checker.Announce = func(updates.Upgrade) bool { return false } // never raise the notice dialog in a test
	return &testCLI{
		cli: &cli{out: out, err: errBuf, in: strings.NewReader(stdin), checker: checker},
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
	for _, want := range []string{"Usage: prem-down", "--to", "--verbose", "--version", "integrate", "updates", premdown.ReleaseExamples()} {
		if !strings.Contains(got, want) {
			t.Errorf("usage() output missing %q:\n%s", want, got)
		}
	}
}

// --------------------------------------------------------------------------
// run() - the CLI arg parser and dispatch, driven over an in-memory cli.
//
// Because run/fatal write through the cli's injected streams and thread the
// exit code back to main (rather than calling os.Exit mid-stack), every fatal
// branch and the whole run() arg parser is reachable in-process: build a cli
// over buffers with newTestCLI, call run, and read the code and the captured
// output back directly - no pipe, panic seam, or global swapping.
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
// newline so the pause returns; this covers the gui branch of pauseIfGUI. --gui
// marks a run as coming from the file manager; it opens the door to the update
// check's question.
func TestRunGUIFlag(t *testing.T) {
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

// Naming the folder and its settings file together is one Production, not two -
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
	// The settings land in the encoding the target release reads - UTF-16LE for
	// anything before 2026 - so the stamped key is matched in that form.
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

// run dispatches "updates" the same way it dispatches "integrate" - before
// any flag parsing, so the word is never mistaken for an input file.
func TestRunDispatchesUpdates(t *testing.T) {
	c := newTestCLI(t, "")
	if code := c.run([]string{"updates"}); code != 0 {
		t.Fatalf("updates should return 0, got code=%d", code)
	}
	if !strings.Contains(c.out.String(), "Update checks are") {
		t.Errorf("status not printed:\n%s", c.out)
	}
	c = newTestCLI(t, "")
	if code := c.run([]string{"updates", "on"}); code != 0 {
		t.Fatalf("updates on should return 0, got code=%d", code)
	}
	saved, err := os.ReadFile(c.checker.ConfigPath) //nolint:gosec // G304: the path is this test's own t.TempDir file
	if err != nil {
		t.Fatalf("updates on did not write the settings file: %v", err)
	}
	if !strings.Contains(string(saved), `"updates": "on"`) {
		t.Errorf("setting not applied, settings file reads:\n%s", saved)
	}
	if c.out.Len() == 0 {
		t.Error("updates on printed nothing")
	}
}

// newCLI and dialogRun both wire a checker, so a nil one never reaches run in a
// shipped build; the guard is there so a missing checker is reported rather
// than dereferenced - a panic on the COM path surfaces as an empty message box.
// Reporting it also beats letting the word fall through to the flag parser,
// which would collect it as a positional and call it a missing project file.
func TestRunUpdatesWithoutAChecker(t *testing.T) {
	c := newTestCLI(t, "")
	c.checker = nil
	if code := c.run([]string{"updates"}); code != 1 {
		t.Fatalf("want exit 1, got code=%d", code)
	}
	if !strings.Contains(c.err.String(), "not available") {
		t.Errorf("missing diagnostic:\n%s", c.err)
	}
}

// An unknown --to release fails before any file is touched, so a typo cannot
// convert half a batch to something the user did not ask for.
func TestRunRejectsAnUnknownRelease(t *testing.T) {
	src := writeProject(t, t.TempDir(), "in.prproj")
	c := newTestCLI(t, "")
	if code := c.run([]string{"--to=2027", src}); code != 1 {
		t.Fatalf("an unknown release should fatal 1, got code=%d", code)
	}
	if !strings.Contains(c.err.String(), "unknown release") {
		t.Errorf("missing diagnostic:\n%s", c.err)
	}
	if _, err := os.Stat(strings.TrimSuffix(src, ".prproj") + "_downgraded.prproj"); err == nil {
		t.Error("nothing should have been converted")
	}
}

// newCLI is the only place the process streams are reached for, and the update
// checker has to be wired in there or the feature is silently absent from the
// real binary while every test still passes.
func TestNewCLIWiresTheProcessStreams(t *testing.T) {
	c := newCLI()
	if c.out != os.Stdout || c.err != os.Stderr || c.in != os.Stdin {
		t.Error("newCLI should wire the real process streams")
	}
	if c.gui {
		t.Error("a plain invocation is not a file-manager run")
	}
	if c.checker == nil {
		t.Fatal("newCLI should wire the update checker")
	}
	if c.checker.Version != version || c.checker.Repo != githubRepo {
		t.Errorf("the checker should carry this build's identity, got %+v", c.checker)
	}
}

// comDowngrade is the Windows COM handler's way in: it runs a selection through
// the same cli the tests drive and folds the result into dialog text, because
// that activation has no console to print to. An empty selection exercises the
// wiring without converting anything.
func TestComDowngradeFoldsARunIntoDialogText(t *testing.T) {
	summary, failed := comDowngrade(nil)
	if !failed {
		t.Error("a selection with nothing to convert is not a success")
	}
	if !strings.Contains(summary, "Usage: prem-down") {
		t.Errorf("want the run's own output as the dialog text, got %q", summary)
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
			c.checker.Ask = func(string, io.Reader, io.Writer) bool { asks++; return false }
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
	c.checker.Ask = func(string, io.Reader, io.Writer) bool {
		t.Error("a failed run must not raise the update question")
		return true
	}
	if code := c.run([]string{"--gui", src}); code != 1 {
		t.Fatalf("a failed downgrade should return 1, got code=%d", code)
	}
}

// releaseVersion decides what a `go install` build reports. Only a real tagged
// release may be adopted: a working-tree build records a module pseudo-version,
// which is not a release and has to keep reading as "dev".
func TestReleaseVersion(t *testing.T) {
	cases := []struct {
		mod    string
		want   string
		wantOK bool
	}{
		{"v0.2.0", "0.2.0", true}, // go install ...@v0.2.0
		{"0.2.0", "0.2.0", true},  // tolerate an unprefixed tag
		{"v1.10.3", "1.10.3", true},
		// Everything Go records for a build that is not from a release tag.
		{"v0.0.0-20260727225736-31d867967f98", "", false},
		{"v0.0.0-20260727225736-31d867967f98+dirty", "", false},
		{"(devel)", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := releaseVersion(tc.mod)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("releaseVersion(%q) = %q, %v; want %q, %v", tc.mod, got, ok, tc.want, tc.wantOK)
		}
	}
}

// The default build reports "dev": this test binary is built from the working
// tree, so init must have found a pseudo-version and declined it.
func TestVersionDefaultsToDevInAWorkingTreeBuild(t *testing.T) {
	if version != "dev" {
		t.Errorf("a working-tree build should report dev, got %q", version)
	}
}

// --------------------------------------------------------------------------
// The Windows context-menu path
// --------------------------------------------------------------------------

func TestComSummary(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr, want string
	}{
		{"success only", "wrote a_downgraded.prproj\n", "", "wrote a_downgraded.prproj"},
		// Failures come first: they are what the user has to act on.
		{
			"both", "wrote a_downgraded.prproj\n", "error: b.prproj: broken\n",
			"error: b.prproj: broken\n\nwrote a_downgraded.prproj",
		},
		{"failure only", "", "error: b.prproj: broken\n", "error: b.prproj: broken"},
		// A run that produced no output at all still needs something to show.
		{"neither", "", "", "Nothing to downgrade."},
		{"whitespace only", "  \n", "\n", "Nothing to downgrade."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := comSummary(tc.stdout, tc.stderr); got != tc.want {
				t.Errorf("comSummary(%q, %q) = %q, want %q", tc.stdout, tc.stderr, got, tc.want)
			}
		})
	}
}

// testChecker is an update checker that cannot touch the real settings, reach
// the network, or put a dialog on screen.
func testChecker(t *testing.T) *updates.Checker {
	t.Helper()
	u := updates.New(githubRepo, "prem-down", "1.0.0")
	u.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	u.Ask = func(string, io.Reader, io.Writer) bool { return false }
	u.Announce = func(updates.Upgrade) bool { return false } // never raise the notice dialog in a test
	return u
}

// The context-menu round trip.
func TestDialogRunSaysNothingOnSuccess(t *testing.T) {
	dir := t.TempDir()
	src := writeProject(t, dir, "in.prproj")

	summary, failed := dialogRun(testChecker(t), []string{src})
	if failed {
		t.Errorf("a good project should not report failure: %q", summary)
	}
	if summary != "" {
		t.Errorf("a clean run should show nothing, got %q", summary)
	}
	if _, err := os.Stat(filepath.Join(dir, "in_downgraded.prproj")); err != nil {
		t.Errorf("the conversion should have happened in-process: %v", err)
	}
}

// A mixed selection must still convert what it can, and the dialog has to lead
// with the failure while still crediting the file that worked.
func TestDialogRunReportsPartialFailure(t *testing.T) {
	dir := t.TempDir()
	good := writeProject(t, dir, "good.prproj")
	bad := filepath.Join(dir, "bad.prproj")
	if err := os.WriteFile(bad, []byte("not a premiere project"), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}

	summary, failed := dialogRun(testChecker(t), []string{bad, good})
	if !failed {
		t.Error("a failed file should make the dialog report failure")
	}
	if !strings.HasPrefix(summary, "error:") {
		t.Errorf("the error should lead the summary, got %q", summary)
	}
	if !strings.Contains(summary, "good") {
		t.Errorf("the file that converted should still be reported, got %q", summary)
	}
}

// --------------------------------------------------------------------------
// Auto-targeting across a mixed batch. The engine's per-source resolution is
// covered in internal/premdown; what matters here is that run() keeps it
// per-file - hoisting the resolution out of the loop would silently give every
// project in a selection the same target.
// --------------------------------------------------------------------------

// sharedFixture copies one of the conversion fixtures into dir. These two tests
// need projects at genuinely different releases, which the inline XML elsewhere
// in this file cannot provide.
func sharedFixture(t *testing.T, dir, name, as string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "premdown", "testdata", name)) //nolint:gosec // G304: name is a fixture filename this test file supplies, not external input
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, as)
	if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec // G306: test fixture copy, perms irrelevant
		t.Fatal(err)
	}
	return dst
}

// projectVersionOf reads the <Project> version back out of a produced file.
func projectVersionOf(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a path the test just produced under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s is not gzipped: %v", path, err)
	}
	defer func() { _ = zr.Close() }()
	xml, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`<Project [^>]*Version="(\d+)"`).FindSubmatch(xml)
	if m == nil {
		t.Fatalf("no <Project> version found in %s", path)
	}
	v, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Two projects at different releases, downgraded in one invocation, must each
// land one release below their own source.
func TestRunAutoTargetsEachProjectIndependently(t *testing.T) {
	dir := t.TempDir()
	src25 := sharedFixture(t, dir, "fixture_ppro25.prproj", "a2025.prproj")
	src26 := sharedFixture(t, dir, "fixture_ppro26.prproj", "b2026.prproj")

	c := newTestCLI(t, "")
	if code := c.run([]string{src25, src26}); code != 0 {
		t.Fatalf("mixed batch should succeed, got code=%d:\n%s", code, c.err)
	}
	for _, tc := range []struct {
		out  string
		want int
	}{
		{"a2025_downgraded.prproj", 42},
		{"b2026_downgraded.prproj", 43},
	} {
		if got := projectVersionOf(t, filepath.Join(dir, tc.out)); got != tc.want {
			t.Errorf("%s: got Project version %d, want %d", tc.out, got, tc.want)
		}
	}
}

// writeFutureProject writes a project stamped with a <Project> version far above
// anything in the release map - a Premiere newer than this build knows about.
// A literal is used rather than the map's own newest entry so the test keeps
// meaning the same thing after the map gains a release.
func writeFutureProject(t *testing.T, dir, name string) string {
	t.Helper()
	const xml = `<PremiereData Version="3">
<Project ObjectID="1" ClassID="y" Version="99">
</Project>
</PremiereData>`
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	return path
}

// stubReleases points a testCLI's update check at a server answering with tag,
// counting requests, so no test ever reaches GitHub.
func stubReleases(t *testing.T, c *testCLI, tag string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"tag_name":"`+tag+`"}`)
	}))
	t.Cleanup(srv.Close)
	c.checker.Endpoint = srv.URL
	return &hits
}

// optIn records a prior "yes" to update checks, as a user who answered the
// first-run question would have.
func optIn(t *testing.T, c *testCLI) {
	t.Helper()
	if code := c.checker.Command(io.Discard, io.Discard, []string{"on"}); code != 0 {
		t.Fatalf("failed to opt in, code=%d", code)
	}
}

// Meeting a Premiere release this build does not know is the one moment an
// update check earns its own message: the file was converted on an assumption,
// and a newer prem-down may simply know the release. Opted in, the run warns,
// converts, and points at the upgrade.
func TestRunOffersAnUpgradeForAnUnrecognisedRelease(t *testing.T) {
	c := newTestCLI(t, "")
	optIn(t, c)
	hits := stubReleases(t, c, "v2.0.0")
	src := writeFutureProject(t, t.TempDir(), "in.prproj")

	// Converted, but on an assumption: its own exit code, not a plain success.
	if code := c.run([]string{src}); code != exitUnrecognisedRelease {
		t.Fatalf("want code=%d, got code=%d err=%s", exitUnrecognisedRelease, code, c.err)
	}
	if _, err := os.Stat(strings.TrimSuffix(src, ".prproj") + "_downgraded.prproj"); err != nil {
		t.Errorf("want the file converted: %v (stdout: %q)", err, c.out)
	}
	for _, want := range []string{"unrecognised", "newer prem-down", "2.0.0"} {
		if !strings.Contains(c.err.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, c.err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("want one update request, got %d", hits.Load())
	}
}

// No upgrade to offer - already current, or offline - leaves the warning to
// stand on its own. The conversion is unaffected either way.
func TestRunStillWarnsWhenNoUpgradeIsAvailable(t *testing.T) {
	c := newTestCLI(t, "")
	optIn(t, c)
	stubReleases(t, c, "v1.0.0") // same as the test CLI's version
	src := writeFutureProject(t, t.TempDir(), "in.prproj")

	// Nothing to offer does not make it a clean run: the assumption still stands.
	if code := c.run([]string{src}); code != exitUnrecognisedRelease {
		t.Fatalf("want code=%d, got code=%d err=%s", exitUnrecognisedRelease, code, c.err)
	}
	if _, err := os.Stat(strings.TrimSuffix(src, ".prproj") + "_downgraded.prproj"); err != nil {
		t.Errorf("want the file converted: %v (stdout: %q)", err, c.out)
	}
	if !strings.Contains(c.err.String(), "unrecognised") {
		t.Errorf("the warning must stand alone:\n%s", c.err)
	}
	if strings.Contains(c.err.String(), "newer prem-down") {
		t.Errorf("nothing to upgrade to, so nothing should be offered:\n%s", c.err)
	}
}

// Without opt-in the check is silent, but the run must still fall through to the
// ordinary notice - otherwise a user who has never been asked would miss the
// first-run question on the very surface built to put it.
func TestRunUnrecognisedReleaseWithoutOptInStillAsks(t *testing.T) {
	c := newTestCLI(t, "")
	asks := 0
	c.checker.Ask = func(string, io.Reader, io.Writer) bool { asks++; return false }
	c.checker.Endpoint = "http://127.0.0.1:1/dead" // opting out means it is never reached
	src := writeFutureProject(t, t.TempDir(), "in.prproj")

	if code := c.run([]string{"--gui", src}); code != exitUnrecognisedRelease {
		t.Fatalf("want code=%d, got code=%d err=%s", exitUnrecognisedRelease, code, c.err)
	}
	if asks != 1 {
		t.Errorf("want the first-run question still asked once, got %d", asks)
	}
	if !strings.Contains(c.err.String(), "unrecognised") {
		t.Errorf("want the warning regardless of the update setting:\n%s", c.err)
	}
	if strings.Contains(c.err.String(), "newer prem-down") {
		t.Errorf("no opt-in means no upgrade offer:\n%s", c.err)
	}
}

// A recognised release takes the routine path: the ordinary weekly notice on
// stdout, and none of the unrecognised-release wording.
func TestRunKnownReleaseUsesTheOrdinaryNotice(t *testing.T) {
	c := newTestCLI(t, "")
	optIn(t, c)
	stubReleases(t, c, "v2.0.0")
	src := writeProject(t, t.TempDir(), "in.prproj")

	if code := c.run([]string{src}); code != 0 {
		t.Fatalf("want a clean run, got code=%d err=%s", code, c.err)
	}
	if !strings.Contains(c.out.String(), "is available") {
		t.Errorf("want the routine notice on stdout:\n%s", c.out)
	}
	if strings.Contains(c.err.String(), "unrecognised") || strings.Contains(c.err.String(), "newer prem-down") {
		t.Errorf("a known release must not mention an unrecognised one:\n%s", c.err)
	}
}

// The exit code says "look at this", but the Windows message box must not call a
// successful conversion an error: every file was written, and the caution is
// already in the text. Only a real failure gets the error icon.
func TestDialogRunDoesNotFlagAnUnrecognisedReleaseAsFailed(t *testing.T) {
	checker := updates.New(githubRepo, "prem-down", "1.0.0")
	checker.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	checker.Ask = func(string, io.Reader, io.Writer) bool { return false }
	checker.Announce = func(updates.Upgrade) bool { return false } // never raise the notice dialog in a test
	src := writeFutureProject(t, t.TempDir(), "in.prproj")

	summary, failed := dialogRun(checker, []string{src})
	if failed {
		t.Error("an unrecognised release converted every file; it is not a failure")
	}
	for _, want := range []string{"unrecognised Premiere release (too new)", "wrote "} {
		if !strings.Contains(summary, want) {
			t.Errorf("dialog text missing %q:\n%s", want, summary)
		}
	}
}
