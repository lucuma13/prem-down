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

// The Services-database entry must be keyed exactly as pbs expects — a
// workflow service has no bundle id, so the key starts with "(null)" — and the
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
	status("integrate: off")
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("a bare integrate should not install anything (stat err: %v)", err)
	}

	Run(io.Discard, io.Discard, []string{"on"})
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("integrate on did not create the bundle: %v", err)
	}
	status("integrate: on")

	Run(io.Discard, io.Discard, []string{"off"})
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("integrate off left the bundle behind (stat err: %v)", err)
	}
	status("integrate: off")
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
// work out where that is — or cannot create the bundle there — has to say so
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

// A bundle that already exists but cannot be written into — the shape an
// install left behind by a privileged installer takes for the user's own later
// run — fails on the file it could not write instead of leaving a half-built
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

// MaybeRunCOMServer is the Windows Drop Target activation hook. On macOS the
// Quick Action invokes prem-down directly, so it must never claim a run —
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
