// Windows implementation of the "integrate" subcommand: the File Explorer
// context-menu entry for .prproj and .prodset files. This file is the installer
// — it writes and removes the registry keys; the COM handler those keys point
// at lives in multi_selection_windows.go.
//
// The keys live under HKCU\Software\Classes so no elevation is needed, and are
// written by shelling out to reg.exe (always present) rather than pulling in a
// registry dependency. The MSI installer writes the same entries under HKLM for
// all users; when both exist Explorer shows the HKCU one.
//
// The verb is implemented as a Drop Target, not a plain command: its CLSID
// resolves to prem-down's own COM LocalServer (this same exe), so selecting
// several .prproj files and invoking the entry hands the whole selection to a
// single prem-down process, which reports on all of them in one message box. A
// command verb ("exe" "%1"), by contrast, is invoked once per selected file, so
// a selection would become one process and one message box each.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package integrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	contextMenuKeyRoot = `HKCU\Software\Classes\SystemFileAssociations\`
	contextMenuTitle   = "Downgrade for older Premiere"

	// dropHandlerCLSID identifies prem-down's Drop Target COM handler. It is a
	// fixed, private class id generated once for this project: it must stay
	// constant so upgrades and "integrate --remove" locate the same
	// registration, and it must not be reused for anything else. The handler it
	// points at is the Drop Target COM server in multi_selection_windows.go.
	dropHandlerCLSID = "{4D9F2A18-7C3B-4E6A-B1F5-2A8C6D0E9F34}"
	dropHandlerName  = "prem-down Premiere downgrade handler"
	clsidKey         = `HKCU\Software\Classes\CLSID\` + dropHandlerCLSID

	// FileManagerName is this platform's file manager (named in the CLI help).
	FileManagerName = "File Explorer"
	integrationKind = "a File Explorer context-menu entry"

	integrationInstalledMessage = `Installed the File Explorer context-menu entries: right-click a .prproj file,
(or a .prodset file), and pick "` + contextMenuTitle + `".`
	integrationRemovedMessage = "Removed the File Explorer context-menu entries."
)

// contextMenuKeys are the verb keys, one per file type the entry appears on.
//
// A Production is a folder, but the entry is registered on its .prodset file
// rather than on folders (a Directory verb would put the entry on every
// folder on the machine) A .prodset only ever exists inside a Production, so
// keying on it is both precise and discoverable — and prem-down maps the file
// back to its folder (see plan in main.go).
var contextMenuKeys = []string{
	contextMenuKeyRoot + `.prproj\shell\prem-down`,
	contextMenuKeyRoot + `.prodset\shell\prem-down`,
}

// contextMenuRegAdds returns the reg.exe argument lists that create the
// context-menu entry. The verb is implemented as a Drop Target rather than a
// plain command: its DropTarget\CLSID points at prem-down's own COM handler,
// registered as a LocalServer32 on the same exe, so Explorer packages an entire
// multi-file selection into one Drop call and a single prem-down process
// downgrades them all. COM appends "-Embedding" to the LocalServer32 command
// when it activates the handler; MaybeRunCOMServer (multi_selection_windows.go)
// detects that and enters server mode. Split out from installIntegration so the
// exact keys and values are unit-testable without touching the registry.
func contextMenuRegAdds(exe string) [][]string {
	var adds [][]string
	// Every file type gets its own verb key, all pointing at the one handler.
	for _, key := range contextMenuKeys {
		adds = append(adds,
			[]string{"add", key, "/ve", "/t", "REG_SZ", "/d", contextMenuTitle, "/f"},
			[]string{"add", key, "/v", "Icon", "/t", "REG_SZ", "/d", exe, "/f"},
			[]string{
				"add", key + `\DropTarget`, "/v", "CLSID", "/t", "REG_SZ",
				"/d", dropHandlerCLSID, "/f",
			},
		)
	}
	return append(adds,
		[]string{"add", clsidKey, "/ve", "/t", "REG_SZ", "/d", dropHandlerName, "/f"},
		[]string{
			"add", clsidKey + `\LocalServer32`, "/ve", "/t", "REG_SZ",
			"/d", fmt.Sprintf(`"%s"`, exe), "/f",
		},
	)
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
	// Missing key means already removed: a failing reg query (key absent) is
	// treated as success and skipped, so a double --remove stays quiet — as
	// does removing an install that predates the .prodset entry. Every verb key
	// and the CLSID registration are removed.
	for _, key := range append(append([]string{}, contextMenuKeys...), clsidKey) {
		if err := exec.Command("reg", "query", key).Run(); err != nil { //nolint:gosec // G204: key is one of the two package constants above, not external input
			continue
		}
		if out, err := exec.Command("reg", "delete", key, "/f").CombinedOutput(); err != nil { //nolint:gosec // G204: key is one of the two package constants above, not external input
			return fmt.Errorf("reg delete %s: %v: %s", key, err, out)
		}
	}
	return nil
}
