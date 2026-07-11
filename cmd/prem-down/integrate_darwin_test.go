// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"bytes"
	"encoding/json"
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

// integrateMain is the CLI glue for the subcommand: `integrate` installs the
// Quick Action and `integrate --remove` takes it away. Drive it end-to-end with
// HOME pointed at a temp dir so the real Services folder is never touched.
func TestIntegrateMainInstallAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origEnable := enableServiceMenu
	enableServiceMenu = func() error { return nil }
	t.Cleanup(func() { enableServiceMenu = origEnable })

	bundle := filepath.Join(home, "Library", "Services", quickActionMenuTitle+".workflow")

	// --help is a clean no-op that installs nothing.
	integrateMain([]string{"-h"})
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("integrate --help should not install anything (stat err: %v)", err)
	}

	integrateMain(nil)
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("integrate did not create the bundle: %v", err)
	}

	integrateMain([]string{"--remove"})
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Errorf("integrate --remove left the bundle behind (stat err: %v)", err)
	}
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
	for _, want := range []string{"com.apple.RunShellScript", "2&gt;&amp;1", "prem-down"} {
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
