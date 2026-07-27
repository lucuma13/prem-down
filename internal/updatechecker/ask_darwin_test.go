// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package updatechecker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The dialog is the one part of this package that cannot be exercised in
// process — running it puts a window on the screen and waits for a human. What
// can be checked without that is that the AppleScript is valid, which is where
// the real risk is: a typo in "cancel button" or "giving up after" would only
// show up as a silently declined prompt on a user's machine, since ask treats
// every osascript failure as no.
//
// osacompile parses and compiles the same source osascript would run, without
// executing it.
func TestDialogScriptCompiles(t *testing.T) {
	const osacompile = "/usr/bin/osacompile"
	if _, err := os.Stat(osacompile); err != nil {
		t.Skipf("%s unavailable: %v", osacompile, err)
	}
	out := filepath.Join(t.TempDir(), "dialog.scpt")
	cmd := exec.Command(osacompile, "-o", out, //nolint:gosec // G204: osacompile and the script are package constants; out is this test's own t.TempDir path
		"-e", "on run argv", "-e", dialogScript, "-e", "end run")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dialog script does not compile: %v\n%s", err, b)
	}
}
