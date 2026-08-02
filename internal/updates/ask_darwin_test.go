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
	// Every script this file can run, including both iconless variants. Those
	// are only reached when staging the logo fails, rare but costly enough to
	// get wrong: ask treats every osascript failure as "no", and Notify
	// persists that answer forever. The notice pair fails the same way in the
	// other direction - announceDialog reads a broken script as a dialog that
	// could not be raised, so the upgrade is quietly printed instead of
	// offered, for every user, forever.
	for name, script := range map[string]string{
		"prompt":        dialogScript,
		"promptNoIcon":  dialogScriptNoIcon,
		"notice":        noticeScript,
		"noticeNoIcon":  noticeScriptNoIcon,
		"runInTerminal": runInTerminalScript,
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

// What "Update now" runs cannot be exercised here - it opens a Terminal window
// - so the command it would run is pinned instead. A wrong one is silent: the
// window opens, something fails inside it, and the user is left with a prompt.
func TestUpgradeCommand(t *testing.T) {
	c := New("lucuma13/prem-down", "prem-down", "0.0.9")

	// A channel with its own upgrade command runs exactly that.
	const brew = "brew upgrade prem-down"
	if got := c.upgradeCommand(Upgrade{Verb: verbRun, Target: brew}); got != brew {
		t.Errorf("upgradeCommand = %q, want %q", got, brew)
	}

	// Anything else fetches the .pkg and opens it. The URL has to be the
	// latest-release redirect and the asset name publish.yml uploads, or the
	// download 404s.
	got := c.upgradeCommand(Upgrade{Verb: verbDownload, Target: c.releasesPage()})
	for _, want := range []string{
		"curl -fL",
		"https://github.com/lucuma13/prem-down/releases/latest/download/prem-down_installer_macos.pkg",
		"open ",
		`"$TMPDIR/prem-down-installer.pkg"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("installer command %q missing %q", got, want)
		}
	}
	// The command is handed to Terminal's "do script" as one shell line, so the
	// download has to gate the open: a 404 that still ran `open` would hand the
	// user an error dialog about a corrupt package.
	if !strings.Contains(got, "&&") {
		t.Errorf("installer command %q should only open the .pkg if the download succeeded", got)
	}
}

// installerURL is the one string in this file that cannot be wrong quietly:
// c.Product is spliced into an asset name, so a Product that is not the release
// asset's prefix 404s at the worst moment.
func TestInstallerURL(t *testing.T) {
	c := New("lucuma13/prem-down", "prem-down", "0.0.9")
	const want = "https://github.com/lucuma13/prem-down/releases/latest/download/prem-down_installer_macos.pkg"
	if got := c.installerURL(); got != want {
		t.Errorf("installerURL = %q, want %q", got, want)
	}
}

// The dialog carries both versions, and the whole point of the notice is that
// the user can compare them. They are rendered bare: the tag GitHub returns
// carries a "v" and the version the binary reports does not, so passing them
// through unchanged would show "1.2.0 (you have 1.1.0)" one release and
// "v1.2.0 (you have 1.1.0)" the next.
func TestDialogTextComparesBareVersions(t *testing.T) {
	c := New("lucuma13/prem-down", "prem-down", "1.1.0")
	got := c.dialogText(Upgrade{Version: "v1.2.0", Verb: verbDownload, Target: c.releasesPage()})

	if !strings.Contains(got, "1.2.0") || !strings.Contains(got, "1.1.0") {
		t.Errorf("dialog text %q should name both versions", got)
	}
	if strings.Contains(got, "v1.2.0") {
		t.Errorf("dialog text %q still carries the tag's leading v", got)
	}
	// The macOS buttons say what they do ("Update now"), so unlike the Windows
	// box the text must not also ask - two questions, one pair of buttons.
	if strings.Contains(got, "?") {
		t.Errorf("dialog text %q asks a question the buttons already answer", got)
	}
}
