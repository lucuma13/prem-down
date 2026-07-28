// Package main implements prem-down, which downgrades an Adobe Premiere Pro
// project file so an older version of Premiere can open it.
//
// Operation runs completely **offline and local** to your machine – no data is
// ever uploaded to the internet.
//
// It fully supports the breaking changes introduced with **Premiere Pro 2026**.
// The well-known method (gunzip the `.prproj`, lower the top-level project
// version, re-gzip) no longer works reliably on Premiere 2026 files. The cause
// is that 2026 uses sparser serialisation — it drops fields that older releases
// expect present (and report the project as damaged if they are absent). So the
// fix is bifold: re-insert those required fields, and set the project version
// to the target release.
//
// Premiere Pro Productions are supported too: passing its .prodset mirrors the
// whole thing into a sibling "<name>_downgraded" folder. See production.go.
//
// Usage example:
//
//	```
//	prem-down myproject.prproj
//	prem-down a.prproj b.prproj c.prproj          # batch
//	prem-down MyProduction/MyProduction.prodset   # whole Production
//	```
//
// This file is the CLI shell only: stream plumbing, argument parsing, and
// turning the positional arguments into jobs. The conversion itself lives in
// downgrade.go (one project) and production.go (a whole Production); the field
// re-insertion 2026 sources need lives in reconstruct.go.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/lucuma13/prem-down/internal/integrate"
	"github.com/lucuma13/prem-down/internal/premdown"
	"github.com/lucuma13/prem-down/internal/updatechecker"
)

// githubRepo where releases are published, the opt-in update check reads it.
const githubRepo = "lucuma13/prem-down"

// prem-down version, overridden at build time via -ldflags "-X
// main.version=1.2.3"
//
// "dev" is what an unstamped `go build` gets (deliberately not a number).
var version = "dev"

// init recovers the version for `go install <module>/cmd/prem-down@vX.Y.Z`,
// which applies no ldflags and would otherwise leave the binary reporting
// "dev". Go records the module version it resolved in the binary's build info,
// so that install path can report the release it actually is — and take part in
// the update check, which ignores versions it cannot compare.
func init() {
	if version != "dev" {
		return // an explicit -ldflags stamp always wins
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v, ok := releaseVersion(info.Main.Version); ok {
			version = v
		}
	}
}

// releaseVersion normalises a module version into the form prem-down reports,
// reporting false for anything that is not a real release. A build from a
// working tree records a pseudo-version ("v0.0.0-20260727225736-31d8679+dirty")
// rather than a tag, and that has to keep reading as "dev" — it is not a
// release, and claiming otherwise would both misreport --version and invite the
// update check to compare against it.
//
// The test is updatechecker's own, so the version this reports and the version
// the checker is willing to compare can never disagree. Split out from init to
// be testable without a module-installed binary.
func releaseVersion(modVersion string) (string, bool) {
	if !updatechecker.Comparable(modVersion) {
		return "", false
	}
	return strings.TrimPrefix(modVersion, "v"), true
}

// cli carries the process's IO streams and the --gui flag so the command logic
// writes through injected streams instead of the os.Stdout/os.Stderr/os.Stdin
// globals. Tests construct a cli over bytes.Buffers and call run/downgrade
// directly — no pipe redirection, no process-exit seam, no global save/restore.
type cli struct {
	out io.Writer // normal output (progress, "wrote ...", help)
	err io.Writer // diagnostics
	in  io.Reader // stdin; only ever read by an update-check prompt that wants one

	// gui means "this run came from the file manager, not a terminal", and marks
	// it as a surface that can put the update check's one-time question to the
	// user. It is set by --gui (passed by the Finder Quick Action) and by the
	// Windows COM handler, which calls run directly rather than through a flag.
	gui bool

	// updates is the opt-in update check
	updates *updatechecker.Checker
}

// newCLI wires a cli to the real process streams; used by main.
func newCLI() *cli {
	return &cli{
		out:     os.Stdout,
		err:     os.Stderr,
		in:      os.Stdin,
		updates: updatechecker.New(githubRepo, "prem-down", version),
	}
}

// downgrader hands the engine this cli's streams, so engine progress and
// per-file diagnostics land wherever the CLI's output is pointed — the real
// process streams in main, in-memory buffers under test.
func (c *cli) downgrader() *premdown.Downgrader {
	return &premdown.Downgrader{Out: c.out, Err: c.err}
}

// fatal reports a user error and returns the process exit code (1) for the
// caller to return. It replaces the old os.Exit-from-anywhere: run and its
// helpers thread the code back to main, which is the only place the process
// actually exits.
func (c *cli) fatal(format string, args ...any) int {
	_, _ = fmt.Fprintf(c.err, format+"\n", args...)
	return 1
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: prem-down input.prproj [input2.prproj ...] [--to RELEASE]
       prem-down production.prodset [--to RELEASE]
       prem-down integrate [--remove]
       prem-down auto-update [on|off|status]

Downgrade one or more Premiere Pro projects next to the original project.

Given a Production's .prodset file (or the Production folder), downgrades the
whole Production into a sibling "<name>_downgraded" folder: its settings, every
project inside, and a verbatim copy of everything else.

Options:
  --to RELEASE    target Premiere release (e.g. %s default: one version older).
  -v, --verbose   print detailed logs
      --version   show version
  -h, --help      show this help menu

Subcommands:
  integrate       add a right-click downgrade action to %s (--remove undoes it)
  auto-update     check GitHub for new releases: on, off or status (default off)
`, premdown.ReleaseExamples(), integrate.FileManagerName)
}

func main() {
	os.Exit(newCLI().run(os.Args[1:]))
}

// comDowngrade converts one File Explorer selection for the Windows COM handler
// and returns the text its message box should show. That activation has no
// console, so the run's output is collected into buffers — the same injected-
// stream seam the tests use — and displayed rather than printed.
//
// gui is set because a context-menu run is the surface the update check may
// raise its question on.
func comDowngrade(files []string) (string, bool) {
	return dialogRun(updatechecker.New(githubRepo, "prem-down", version), files)
}

// dialogRun runs files through a cli whose streams are buffers and folds the
// result into dialog text.
func dialogRun(updates *updatechecker.Checker, files []string) (string, bool) {
	var out, errOut bytes.Buffer
	c := &cli{
		out:     &out,
		err:     &errOut,
		in:      strings.NewReader(""),
		gui:     true,
		updates: updates,
	}
	failed := c.run(files) != 0
	return comSummary(out.String(), errOut.String()), failed
}

// comSummary folds a run's two streams into one block of dialog text: failures
// first, then what was written.
func comSummary(stdout, stderr string) string {
	var b strings.Builder
	for _, s := range []string{strings.TrimSpace(stderr), strings.TrimSpace(stdout)} {
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s)
	}
	if b.Len() == 0 {
		return "Nothing to downgrade."
	}
	return b.String()
}

// run holds main's logic, split out so it can be tested: it returns the process
// exit code instead of calling os.Exit, and user-error paths return c.fatal's
// code rather than exiting mid-stack. main is then a one-line shim.
func (c *cli) run(args []string) int {
	// When Explorer activates prem-down as the Drop Target COM server (Windows
	// only; "-Embedding"), it takes over completely: it collects the selected
	// files and relaunches prem-down --gui on them. See multi_selection_windows.go.
	if integrate.MaybeRunCOMServer(args, comDowngrade) {
		return 0
	}
	if len(args) > 0 && args[0] == "integrate" {
		return integrate.Run(c.out, c.err, args[1:])
	}
	// Dispatched on the word alone, like "integrate": falling through to the flag
	// parser would leave "auto-update" looking like an input file and report it
	// as a missing project.
	if len(args) > 0 && args[0] == "auto-update" {
		if c.updates == nil {
			return c.fatal("error: update checking is not available in this build")
		}
		return c.updates.Command(c.out, c.err, args[1:])
	}

	var positionals []string
	to := "" // empty => auto: one release below the source
	verbose := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			usage(c.out)
			return 0
		case a == "--version":
			_, _ = fmt.Fprintf(c.out, "prem-down %s\n", version)
			return 0
		case a == "--to":
			i++
			if i >= len(args) {
				return c.fatal("error: --to requires a value")
			}
			to = args[i] //nolint:gosec // G602: the i >= len(args) guard above returns first, so i is in range here
		case strings.HasPrefix(a, "--to="):
			to = strings.TrimPrefix(a, "--to=")
			if to == "" {
				return c.fatal("error: --to requires a value")
			}
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "--gui":
			c.gui = true
		case strings.HasPrefix(a, "-") && a != "-":
			usage(c.err)
			return c.fatal("error: unknown option %s", a)
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) == 0 {
		usage(c.err)
		return 2
	}

	// Explicit --to is resolved and validated up front; auto (empty) is deferred
	// to downgrade, which needs each source's version to pick the previous
	// release. Resolving once here also means a bad --to fails before any file is
	// touched.
	targetVersion := 0
	if to != "" {
		v, err := premdown.ResolveRelease(to)
		if err != nil {
			return c.fatal("error: %v", err)
		}
		targetVersion = v
	}

	jobs, failed := c.plan(positionals)

	// Each job is converted independently: a failure on one is reported and the
	// rest still run, so a batch (a multi-file selection from the context menu, or
	// a shell glob) isn't aborted by a single bad input. Exit non-zero if any
	// failed.
	for _, j := range jobs {
		if j.production {
			dst := premdown.UniqueDir(j.path + "_downgraded")
			if err := c.downgrader().DowngradeProduction(j.path, dst, targetVersion, verbose); err != nil {
				_, _ = fmt.Fprintf(c.err, "error: %s: %v\n", j.path, err)
				failed = true
			}
			continue
		}
		ext := filepath.Ext(j.path)
		dst := premdown.UniquePath(strings.TrimSuffix(j.path, ext) + "_downgraded" + premdown.PrprojExt)
		if err := c.downgrader().Downgrade(j.path, dst, targetVersion, verbose); err != nil {
			_, _ = fmt.Fprintf(c.err, "error: %s: %v\n", j.path, err)
			failed = true
			continue
		}
		_, _ = fmt.Fprintf(c.out, "wrote %s\n", dst)
	}
	// Only after a clean batch: a run that already reported an error is not a
	// place to ask the user about update checks.
	if !failed && c.updates != nil {
		c.updates.Notify(c.out, c.in, c.gui)
	}
	if failed {
		return 1
	}
	return 0
}

// job is one unit of work: either a lone .prproj (production false, path is the
// file) or a whole Production (production true, path is its folder).
type job struct {
	path       string
	production bool
}

// plan turns the raw positional arguments into the jobs to run, reporting
// unusable inputs as it goes.
//
// Three things reach this function as "a Production": the folder itself, and
// the .prodset inside it — which is what the file manager's right-click entry
// is wired to, since there is no way to put a menu entry on Production folders
// alone without putting it on every folder on the machine.
//
// The third is the habit the context menu encourages: selecting the .prodset
// *and* the .prproj files together. Those projects are already inside the
// Production being mirrored, so downgrading them again would scatter stray
// _downgraded.prproj files through the user's original folder. They are dropped
// from the plan instead, with a note saying where they were handled.
func (c *cli) plan(positionals []string) (jobs []job, failed bool) {
	var files []string
	roots := map[string]bool{} // Production folder -> already planned

	addProduction := func(dir string) {
		if roots[dir] {
			return // named twice, e.g. as both the folder and its .prodset
		}
		roots[dir] = true
		jobs = append(jobs, job{path: dir, production: true})
	}

	for _, input := range positionals {
		info, err := os.Stat(input) //nolint:gosec // G703: input is the user-supplied CLI path; stat-ing it is the tool's purpose
		if err != nil {
			_, _ = fmt.Fprintf(c.err, "error: %s not found\n", input)
			failed = true
			continue
		}
		switch {
		case info.IsDir():
			addProduction(filepath.Clean(input))
		case strings.EqualFold(filepath.Ext(input), premdown.ProdsetExt):
			// The Production is the folder the settings file sits in.
			addProduction(filepath.Dir(filepath.Clean(input)))
		default:
			files = append(files, input)
		}
	}

	for _, f := range files {
		if root, ok := coveredBy(f, roots); ok {
			_, _ = fmt.Fprintf(c.out, "skipping %s: already part of the Production %s\n", f, root)
			continue
		}
		jobs = append(jobs, job{path: f})
	}
	return jobs, failed
}

// coveredBy reports whether file lives inside one of the Production folders
// already planned. Comparison is on absolute, cleaned paths so "./x/a.prproj"
// and "x" are recognised as the same tree; if a path cannot be made absolute
// the file is simply treated as uncovered and processed on its own, which is
// the harmless outcome.
func coveredBy(file string, roots map[string]bool) (string, bool) {
	absFile, err := filepath.Abs(file)
	if err != nil {
		return "", false
	}
	for root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absFile)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return root, true
	}
	return "", false
}
