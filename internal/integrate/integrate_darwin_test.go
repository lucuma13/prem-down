package integrate

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Services-database entry must be keyed exactly as pbs expects - a
// workflow service has no bundle id, so the key starts with "(null)" - and the
// enabled-status value must be well-formed JSON that turns both Finder menus
// on, or the auto-enable silently registers a disabled (or malformed) entry.
func TestServiceEnableStatus(t *testing.T) {
	key := serviceStatusKey()
	if want := "(null) - " + quickActionMenuTitle + " - runWorkflowAsService"; key != want {
		t.Errorf("serviceStatusKey() = %q, want %q", key, want)
	}

	var status struct {
		EnabledContextMenu  bool `json:"enabled_context_menu"`
		EnabledServicesMenu bool `json:"enabled_services_menu"`
		PresentationModes   struct {
			ContextMenu  bool `json:"ContextMenu"`
			ServicesMenu bool `json:"ServicesMenu"`
		} `json:"presentation_modes"`
	}
	if err := json.Unmarshal([]byte(serviceEnabledStatus), &status); err != nil {
		t.Fatalf("serviceEnabledStatus is not valid JSON: %v", err)
	}
	if !status.EnabledContextMenu || !status.EnabledServicesMenu ||
		!status.PresentationModes.ContextMenu || !status.PresentationModes.ServicesMenu {
		t.Errorf("serviceEnabledStatus does not enable both menus: %+v", status)
	}
}

// integrate.Run is the CLI glue for the subcommand: `integrate on` installs the
// Quick Action, `integrate off` takes it away, and a bare `integrate` reports
// status. Drive it end-to-end with HOME pointed at a temp dir so the real
// Services folder is never touched.
func TestIntegrateMainInstallAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origEnable := enableServiceMenu
	enableServiceMenu = func() error { return nil }
	t.Cleanup(func() { enableServiceMenu = origEnable })

	origDisable := disableServiceMenu
	disableServiceMenu = func() error { return nil }
	t.Cleanup(func() { disableServiceMenu = origDisable })

	bundle := filepath.Join(home, "Library", "Services", quickActionMenuTitle+".workflow")

	// --help is a clean no-op that installs nothing.
	Run(io.Discard, io.Discard, []string{"-h"})
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("integrate --help should not install anything (stat err: %v)", err)
	}

	// A bare invocation reports the status.
	status := func(want string) {
		t.Helper()
		var out strings.Builder
		if code := Run(&out, io.Discard, nil); code != 0 {
			t.Fatalf("a bare integrate should return 0, got %d", code)
		}
		if got := strings.TrimSpace(out.String()); got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
	}
	status(integrationAbsentMessage)
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("a bare integrate should not install anything (stat err: %v)", err)
	}

	// Each direction reports what actually changed.
	action := func(arg, want string) {
		t.Helper()
		var out strings.Builder
		if code := Run(&out, io.Discard, []string{arg}); code != 0 {
			t.Fatalf("integrate %s should return 0, got %d", arg, code)
		}
		if got := strings.TrimSpace(out.String()); got != want {
			t.Errorf("integrate %s said %q, want %q", arg, got, want)
		}
	}

	action("off", integrationAbsentMessage) // nothing installed yet

	action("on", integrationInstalledMessage)
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("integrate on did not create the bundle: %v", err)
	}
	status(integrationPresentMessage)

	action("on", integrationReinstalledMessage)
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("a repeated integrate on must leave the bundle in place: %v", err)
	}
	status(integrationPresentMessage)

	action("off", integrationRemovedMessage)
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("integrate off left the bundle behind (stat err: %v)", err)
	}
	status(integrationAbsentMessage)

	action("off", integrationAbsentMessage)
	status(integrationAbsentMessage)
}

// installIntegration must produce a complete Quick Action bundle under
// $HOME/Library/Services, and removeIntegration must take it away again.
// HOME points into a temp dir so the test never touches the real Services.
func TestInstallAndRemoveIntegration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// installIntegration switches the Quick Action on via the per-user `pbs`
	// preference domain, which cfprefsd resolves by UID (ignoring $HOME); stub
	// it so the test never flips the setting on the developer's real machine.
	origEnable := enableServiceMenu
	enableServiceMenu = func() error { return nil }
	t.Cleanup(func() { enableServiceMenu = origEnable })

	origDisable := disableServiceMenu
	disableServiceMenu = func() error { return nil }
	t.Cleanup(func() { disableServiceMenu = origDisable })

	if err := installIntegration(); err != nil {
		t.Fatalf("installIntegration: %v", err)
	}
	bundle := filepath.Join(home, "Library", "Services", quickActionMenuTitle+".workflow")

	info, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")) //nolint:gosec // G304: path is built from test-controlled constants
	if err != nil {
		t.Fatalf("Info.plist not written: %v", err)
	}
	for _, want := range []string{quickActionMenuTitle, "runWorkflowAsService", "NSSendFileTypes", quickActionIconName} {
		if !strings.Contains(string(info), want) {
			t.Errorf("Info.plist missing %q", want)
		}
	}

	// The custom menu icon must be written into Resources under the exact name
	// NSIconName resolves, with the embedded bytes intact.
	icon, err := os.ReadFile(filepath.Join(bundle, "Contents", "Resources", quickActionIconName+".tiff")) //nolint:gosec // G304: path is built from test-controlled constants
	if err != nil {
		t.Fatalf("icon TIFF not written: %v", err)
	}
	if len(icon) == 0 || !bytes.Equal(icon, quickActionIcon) {
		t.Errorf("icon TIFF mismatch: wrote %d bytes, embedded %d bytes", len(icon), len(quickActionIcon))
	}

	doc, err := os.ReadFile(filepath.Join(bundle, "Contents", "document.wflow")) //nolint:gosec // G304: path is built from test-controlled constants
	if err != nil {
		t.Fatalf("document.wflow not written: %v", err)
	}
	// The shell script is spliced into XML: its redirections must arrive
	// escaped ("2>&1" -> "2&gt;&amp;1") or the plist would be malformed.
	// "--gui" is the only thing telling prem-down this run came from Finder,
	// and so the only thing that lets the update check ever ask its question on
	// macOS.
	for _, want := range []string{"com.apple.RunShellScript", "2&gt;&amp;1", "prem-down --gui"} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("document.wflow missing %q", want)
		}
	}
	if strings.Contains(string(doc), "2>&1") {
		t.Error("document.wflow contains unescaped shell script")
	}

	// Idempotent: a second install (e.g. every brew upgrade) must succeed.
	if err := installIntegration(); err != nil {
		t.Fatalf("second installIntegration: %v", err)
	}

	if err := removeIntegration(); err != nil {
		t.Fatalf("removeIntegration: %v", err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("bundle still present after remove (stat err: %v)", err)
	}

	// Removing what is already gone stays quiet (uninstall hook re-runs).
	if err := removeIntegration(); err != nil {
		t.Fatalf("second removeIntegration: %v", err)
	}
}

// The Quick Action lives under the user's home directory, so a run that cannot
// work out where that is - or cannot create the bundle there - has to say so
// and exit non-zero. Both directions of the subcommand report it; neither
// claims to have installed or removed anything.
func TestIntegrateReportsAFailureToWriteTheBundle(t *testing.T) {
	// Stubbed for the same reason as everywhere else in this file: the real
	// implementations write the per-user `pbs` domain, which cfprefsd resolves
	// by UID and so ignores the $HOME this test sets.
	origEnable, origDisable := enableServiceMenu, disableServiceMenu
	enableServiceMenu = func() error { return nil }
	disableServiceMenu = func() error { return nil }
	t.Cleanup(func() { enableServiceMenu, disableServiceMenu = origEnable, origDisable })

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, home string }{
		{"no home directory", ""},
		{"home is not a directory", blocker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			for _, args := range [][]string{nil, {"on"}, {"off"}} {
				var out, errw strings.Builder
				if code := Run(&out, &errw, args); code != 1 {
					t.Errorf("%v: want exit 1, got %d (out %q)", args, code, out.String())
				}
				if !strings.Contains(errw.String(), "error:") {
					t.Errorf("%v: failure not reported:\n%s", args, errw.String())
				}
				if out.String() != "" {
					t.Errorf("%v: nothing should be announced on failure: %q", args, out.String())
				}
			}
		})
	}
}

// A bundle that already exists but cannot be written into - the shape an
// install left behind by a privileged installer takes for the user's own later
// run - fails on the file it could not write instead of leaving a half-built
// Quick Action that Finder would try to load.
func TestInstallIntegrationReportsAWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failures cannot be provoked")
	}
	for _, locked := range []string{"Contents", filepath.Join("Contents", "Resources")} {
		t.Run(locked, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			origEnable := enableServiceMenu
			enableServiceMenu = func() error { return nil }
			t.Cleanup(func() { enableServiceMenu = origEnable })

			bundle := filepath.Join(home, "Library", "Services", quickActionMenuTitle+".workflow")
			if err := os.MkdirAll(filepath.Join(bundle, "Contents", "Resources"), 0o750); err != nil {
				t.Fatal(err)
			}
			// r-x: the directories are still there to be found, but nothing new
			// can be created in them.
			dir := filepath.Join(bundle, locked)
			if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: the point of the test is a directory that can be traversed but not written
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restoring the directory so the temp dir can be removed

			if err := installIntegration(); err == nil {
				t.Fatal("expected an error when the bundle cannot be written")
			}
		})
	}
}

// The subcommand's own report of the same failure. TestIntegrateReportsAFailureToWriteTheBundle
// covers a home directory Run cannot even locate, which fails before either
// direction does any work; this is the later failure, where the bundle was
// found but the install or removal itself could not be carried out. Run must
// still exit non-zero and must not announce a change that did not happen -
// "Right-click integration installed" over a bundle that was never written
// sends the user looking for a menu entry that is not there.
func TestIntegrateReportsAFailedInstallOrRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission failures cannot be provoked")
	}
	for _, tc := range []struct {
		name, action string
		// locked is the directory made read-only, relative to ~/Library/Services.
		locked string
	}{
		// The bundle is there, but its Contents cannot be written into, so
		// installIntegration fails partway.
		{"on", "on", quickActionMenuTitle + ".workflow/Contents"},
		// The bundle is there and Services itself cannot be written, so the
		// bundle cannot be unlinked.
		{"off", "off", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			origEnable, origDisable := enableServiceMenu, disableServiceMenu
			enableServiceMenu = func() error { return nil }
			disableServiceMenu = func() error { return nil }
			t.Cleanup(func() { enableServiceMenu, disableServiceMenu = origEnable, origDisable })

			services := filepath.Join(home, "Library", "Services")
			bundle := filepath.Join(services, quickActionMenuTitle+".workflow")
			if err := os.MkdirAll(filepath.Join(bundle, "Contents", "Resources"), 0o750); err != nil {
				t.Fatal(err)
			}
			// r-x: still found by integrationInstalled's stat, but nothing in it
			// can be created or unlinked.
			dir := filepath.Join(services, tc.locked)
			if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: the point of the test is a directory that can be traversed but not written
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restoring the directory so the temp dir can be removed

			var out, errw strings.Builder
			if code := Run(&out, &errw, []string{tc.action}); code != 1 {
				t.Errorf("want exit 1, got %d (out %q)", code, out.String())
			}
			if !strings.Contains(errw.String(), "error:") {
				t.Errorf("failure not reported:\n%s", errw.String())
			}
			if out.String() != "" {
				t.Errorf("nothing should be announced on failure: %q", out.String())
			}
		})
	}
}

// MaybeRunCOMServer is the Windows Drop Target activation hook. On macOS the
// Quick Action invokes prem-down directly, so it must never claim a run -
// whatever the arguments look like.
func TestMaybeRunCOMServerNeverClaimsARunOnMacOS(t *testing.T) {
	for _, args := range [][]string{nil, {"-Embedding"}, {"a.prproj"}} {
		if MaybeRunCOMServer(args, func([]string) (string, bool) {
			t.Error("the COM downgrader must not be called on macOS")
			return "", false
		}) {
			t.Errorf("MaybeRunCOMServer(%v) should be a no-op on macOS", args)
		}
	}
}

// removeIntegration must reverse the Services-database entry, not just delete
// the bundle: it has to invoke the disable seam so no stale NSServicesStatus
// record lingers after uninstall.
func TestRemoveIntegrationDisablesService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origEnable := enableServiceMenu
	enableServiceMenu = func() error { return nil }
	t.Cleanup(func() { enableServiceMenu = origEnable })

	called := false
	origDisable := disableServiceMenu
	disableServiceMenu = func() error { called = true; return nil }
	t.Cleanup(func() { disableServiceMenu = origDisable })

	if err := installIntegration(); err != nil {
		t.Fatalf("installIntegration: %v", err)
	}
	if err := removeIntegration(); err != nil {
		t.Fatalf("removeIntegration: %v", err)
	}
	if !called {
		t.Error("removeIntegration did not invoke disableServiceMenu")
	}
}

// pbsShim puts fake `defaults` and `plutil` executables at the front of PATH,
// which is what lets the two functions behind those seams be driven for real.
// They are stubbed out everywhere else in this file for a reason: the genuine
// ones write the per-user `pbs` domain, which cfprefsd resolves by UID and so
// ignores the $HOME a test sets - running them unshimmed would flip the Quick
// Action on the developer's own machine during `go test`. Both are invoked by
// bare name, so PATH is the whole interception.
//
// Every invocation appends its command line to a log, and any invocation whose
// command line contains a pattern written to the fail file exits non-zero.
type pbsShim struct {
	log      string
	failFile string
}

func newPBSShim(t *testing.T) *pbsShim {
	t.Helper()
	dir := t.TempDir()
	s := &pbsShim{log: filepath.Join(dir, "calls.log"), failFile: filepath.Join(dir, "fail")}

	// ${0##*/} rather than basename: PATH holds nothing but this directory
	// while the shim is installed, so no other tool can be relied on.
	const script = `#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >> "$PBS_LOG"
if [ -f "$PBS_FAIL" ]; then
  while IFS= read -r pat; do
    [ -n "$pat" ] || continue
    case "${0##*/} $*" in *"$pat"*) exit 1 ;; esac
  done < "$PBS_FAIL"
fi
exit 0
`
	for _, tool := range []string{"defaults", "plutil"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil { //nolint:gosec // G306: a shim this test is about to execute has to be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("PBS_LOG", s.log)
	t.Setenv("PBS_FAIL", s.failFile)
	return s
}

// fail makes every later invocation containing any of pats exit non-zero.
//
// The trailing newline is required, not tidiness: `read` reports failure on a
// last line without one, and the shim's loop would skip the pattern.
func (s *pbsShim) fail(t *testing.T, pats ...string) {
	t.Helper()
	if err := os.WriteFile(s.failFile, []byte(strings.Join(pats, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// calls is the command lines run so far, in order.
func (s *pbsShim) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(s.log) //nolint:gosec // G304: the shim's own log, under this test's t.TempDir
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// wantCalls checks the command lines ran in order, each matching its want as a
// prefix, and reports the whole log on a mismatch.
func (s *pbsShim) wantCalls(t *testing.T, want ...string) []string {
	t.Helper()
	got := s.calls(t)
	if len(got) != len(want) {
		t.Fatalf("ran %d commands, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("command %d = %q, want it to start with %q", i+1, got[i], w)
		}
	}
	return got
}

// tmpPlist is the scratch file the export/import dance goes through: the last
// field of any of these command lines.
func tmpPlist(t *testing.T, call string) string {
	t.Helper()
	fields := strings.Fields(call)
	if len(fields) == 0 {
		t.Fatalf("no arguments in %q", call)
	}
	return fields[len(fields)-1]
}

// The enable path is the fiddliest thing in this file, and every part of it is
// load-bearing: `defaults` cannot write a key beginning with "(" at all, so the
// edit has to go out to a temp plist, through plutil, and back in - and it has
// to go back in through `defaults import` rather than a direct file write, or
// cfprefsd's cache would overwrite it. Nothing about getting this wrong is
// visible: the workflow installs, and the Quick Action simply never appears
// until the user finds it under "Quick Actions > Customise".
func TestEnableQuickActionEditsThroughATempPlist(t *testing.T) {
	t.Run("existing container", func(t *testing.T) {
		s := newPBSShim(t)
		if err := enableQuickAction(); err != nil {
			t.Fatalf("enableQuickAction: %v", err)
		}
		// -extract succeeds, so NSServicesStatus is already there and the
		// -insert that would create it must be skipped.
		calls := s.wantCalls(t,
			"defaults export pbs ",
			"plutil -extract NSServicesStatus raw ",
			"plutil -replace NSServicesStatus.",
			"defaults import pbs ",
		)

		// The entry written is the one pbs reads, with the value that turns both
		// menus on.
		for _, want := range []string{serviceStatusKey(), "-json", serviceEnabledStatus} {
			if !strings.Contains(calls[2], want) {
				t.Errorf("-replace call %q missing %q", calls[2], want)
			}
		}

		// Export, edit and import must all address the same scratch file, or the
		// edit is imported from a plist that never received it.
		tmp := tmpPlist(t, calls[0])
		if !strings.HasSuffix(tmp, ".plist") {
			t.Errorf("scratch file %q is not a .plist", tmp)
		}
		for _, c := range calls {
			if got := tmpPlist(t, c); got != tmp {
				t.Errorf("command %q used %q, want the exported %q", c, got, tmp)
			}
		}
		// And it must not be left behind.
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Errorf("scratch plist %s was not removed (stat err: %v)", tmp, err)
		}
	})

	t.Run("missing container is created first", func(t *testing.T) {
		s := newPBSShim(t)
		// A domain with no NSServicesStatus yet - the first-ever install. plutil
		// -replace does not create intermediate dictionaries, so the -insert has
		// to run before the key can be addressed.
		s.fail(t, "-extract")
		if err := enableQuickAction(); err != nil {
			t.Fatalf("enableQuickAction: %v", err)
		}
		s.wantCalls(t,
			"defaults export pbs ",
			"plutil -extract NSServicesStatus raw ",
			"plutil -insert NSServicesStatus -dictionary ",
			"plutil -replace NSServicesStatus.",
			"defaults import pbs ",
		)
	})
}

// Any step failing has to stop the sequence rather than carry on with a plist
// that is not what the next step assumes - importing an export that never
// happened would wipe the user's whole pbs domain.
func TestEnableQuickActionStopsAtTheFirstFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail []string
		want []string
	}{
		{"export", []string{"export"}, []string{"defaults export pbs "}},
		{"insert", []string{"-extract", "-insert"}, []string{
			"defaults export pbs ", "plutil -extract ", "plutil -insert ",
		}},
		{"replace", []string{"-replace"}, []string{
			"defaults export pbs ", "plutil -extract ", "plutil -replace ",
		}},
		{"import", []string{"import"}, []string{
			"defaults export pbs ", "plutil -extract ", "plutil -replace ", "defaults import pbs ",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newPBSShim(t)
			s.fail(t, tc.fail...)
			if err := enableQuickAction(); err == nil {
				t.Fatal("a failed step must be reported, not swallowed")
			}
			calls := s.wantCalls(t, tc.want...)
			// The scratch file goes either way; a failure must not litter TMPDIR.
			if tmp := tmpPlist(t, calls[0]); tmp != "" {
				if _, err := os.Stat(tmp); !os.IsNotExist(err) {
					t.Errorf("scratch plist %s survived the failure (stat err: %v)", tmp, err)
				}
			}
		})
	}
}

// Disabling is the mirror, with one deliberate asymmetry: an entry that is not
// there is not a failure. Uninstall runs it best-effort over a Services
// database that may never have had our key - a Quick Action the user ticked off
// themselves, or an install from before the auto-enable existed.
func TestDisableQuickAction(t *testing.T) {
	t.Run("removes the entry and imports the result", func(t *testing.T) {
		s := newPBSShim(t)
		if err := disableQuickAction(); err != nil {
			t.Fatalf("disableQuickAction: %v", err)
		}
		calls := s.wantCalls(t,
			"defaults export pbs ",
			"plutil -remove NSServicesStatus.",
			"defaults import pbs ",
		)
		if !strings.Contains(calls[1], serviceStatusKey()) {
			t.Errorf("-remove call %q does not name our entry", calls[1])
		}
		tmp := tmpPlist(t, calls[0])
		for _, c := range calls {
			if got := tmpPlist(t, c); got != tmp {
				t.Errorf("command %q used %q, want the exported %q", c, got, tmp)
			}
		}
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Errorf("scratch plist %s was not removed (stat err: %v)", tmp, err)
		}
	})

	t.Run("an absent entry is not a failure", func(t *testing.T) {
		s := newPBSShim(t)
		s.fail(t, "-remove")
		if err := disableQuickAction(); err != nil {
			t.Fatalf("nothing to remove must succeed, got %v", err)
		}
		// And nothing is imported: re-importing an unchanged export is pointless
		// work on a domain we have no business rewriting.
		s.wantCalls(t, "defaults export pbs ", "plutil -remove NSServicesStatus.")
	})

	for _, tc := range []struct {
		name string
		fail string
		want []string
	}{
		{"export", "export", []string{"defaults export pbs "}},
		{"import", "import", []string{
			"defaults export pbs ", "plutil -remove ", "defaults import pbs ",
		}},
	} {
		t.Run("reports a failed "+tc.name, func(t *testing.T) {
			s := newPBSShim(t)
			s.fail(t, tc.fail)
			if err := disableQuickAction(); err == nil {
				t.Fatal("a failed step must be reported, not swallowed")
			}
			s.wantCalls(t, tc.want...)
		})
	}
}
