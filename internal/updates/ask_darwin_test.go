package updates

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The dialog is the one part of this package that cannot be exercised in
// process - running it puts a window on the screen and waits for a human. What
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
	// Both variants. The iconless one is only reached when staging the logo
	// fails, rare but costly enough to get wrong: ask treats every osascript
	// failure as "no", and Notify persists that answer forever.
	for name, script := range map[string]string{
		"withIcon": dialogScript,
		"noIcon":   dialogScriptNoIcon,
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "dialog.scpt")
			cmd := exec.Command(osacompile, "-o", out, //nolint:gosec // G204: osacompile and the script are package constants; out is this test's own t.TempDir path
				"-e", "on run argv", "-e", script, "-e", "end run")
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("dialog script does not compile: %v\n%s", err, b)
			}
		})
	}
}

// The affirmative test runs against osascript's whole reply, so it has to match
// the field that reply actually carries.
func TestAffirmativeReplyMatchesOsascriptFormat(t *testing.T) {
	if got, want := affirmativeReply, "button returned:OK"; got != want {
		t.Errorf("affirmativeReply = %q, want %q", got, want)
	}
	// The declining replies must not satisfy the same test.
	for _, reply := range []string{"button returned:Not now, gave up:false", "gave up:true"} {
		if strings.Contains(reply, affirmativeReply) {
			t.Errorf("%q should not read as consent", reply)
		}
	}
}

// stageIcon has to produce a file osascript can actually render, which means
// real .icns bytes on disk.
func TestStageIconWritesTheLogo(t *testing.T) {
	if len(dialogIcon) == 0 {
		t.Fatal("no icon embedded")
	}
	if magic := string(dialogIcon[:4]); magic != "icns" {
		t.Errorf("embedded icon is not an .icns (magic %q)", magic)
	}

	path, cleanup := stageIcon()
	if path == "" {
		t.Fatal("stageIcon returned no path")
	}
	written, err := os.ReadFile(path) //nolint:gosec // G304: path is stageIcon's own temp file
	if err != nil {
		t.Fatalf("staged icon unreadable: %v", err)
	}
	if !bytes.Equal(written, dialogIcon) {
		t.Errorf("staged icon differs from the embedded bytes (%d vs %d)", len(written), len(dialogIcon))
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup left the icon behind (stat err: %v)", err)
	}
}
