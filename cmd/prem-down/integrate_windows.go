// Windows implementation of the "integrate" subcommand: an Explorer
// context-menu entry for .prproj files.
//
// The keys live under HKCU\Software\Classes so no elevation is needed, and
// are written by shelling out to reg.exe (always present) rather than pulling
// in a registry dependency. The MSI installer writes the same entry under
// HKLM for all users; when both exist Explorer shows the HKCU one. The
// command invokes prem-down with --gui so the console window the shell opens
// stays up long enough to read the result.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	contextMenuKey   = `HKCU\Software\Classes\SystemFileAssociations\.prproj\shell\prem-down`
	contextMenuTitle = "Downgrade for older Premiere"

	fileManagerName = "File Explorer"
	integrationKind = "a File Explorer context-menu entry"

	integrationInstalledMessage = `Installed the File Explorer context-menu entry: right-click a .prproj file and
pick "` + contextMenuTitle + `".`
	integrationRemovedMessage = "Removed the File Explorer context-menu entry."
)

// contextMenuRegAdds returns the reg.exe argument lists that create the
// context-menu entry launching exe. Split out from installIntegration so the
// exact keys and values are unit-testable without touching the registry.
func contextMenuRegAdds(exe string) [][]string {
	return [][]string{
		{"add", contextMenuKey, "/ve", "/t", "REG_SZ", "/d", contextMenuTitle, "/f"},
		{"add", contextMenuKey, "/v", "Icon", "/t", "REG_SZ", "/d", exe, "/f"},
		{
			"add", contextMenuKey + `\command`, "/ve", "/t", "REG_SZ",
			"/d", fmt.Sprintf(`"%s" --gui "%%1"`, exe), "/f",
		},
	}
}

func installIntegration() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate own executable: %w", err)
	}
	// Resolve symlinks (e.g. the winget Links shim) so the registry points at
	// a path that keeps working if the shim is recreated elsewhere... the
	// resolved target is also what stays valid longest for manual installs.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	for _, args := range contextMenuRegAdds(exe) {
		if out, err := exec.Command("reg", args...).CombinedOutput(); err != nil { //nolint:gosec // G204: "reg" is constant; args are built internally from the resolved own-executable path, not external input
			return fmt.Errorf("reg %s: %v: %s", args[0], err, out)
		}
	}
	return nil
}

func removeIntegration() error {
	// Missing key means already removed: reg query failing is success here,
	// so a double --remove stays quiet instead of erroring.
	if err := exec.Command("reg", "query", contextMenuKey).Run(); err != nil {
		return nil
	}
	if out, err := exec.Command("reg", "delete", contextMenuKey, "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg delete: %v: %s", err, out)
	}
	return nil
}
