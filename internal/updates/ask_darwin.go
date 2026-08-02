// The macOS first-run prompt.
//
// A run launched from Finder has no terminal to ask in - a Quick Action's
// output is captured and shown as a notification once the process has already
// exited - so the question is raised as an AppleScript dialog.

package updates

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dialogIcon is the prem-down logo, shown in the dialog so the question is
// visibly ours. Without it the dialog inherits osascript's own icon, which
// tells the user nothing about who is asking.
//
// It is committed, and buildable with `make render`.
//
//go:embed prem-down.icns
var dialogIcon []byte

// "Not now" is both the plain button and the cancel button, so Escape and the
// close box land on the same answer as clicking it. "giving up after" keeps an
// ignored dialog from pinning a background launch open indefinitely; giving up
// returns no button, which reads as no.
const dialogScript = `display dialog (item 1 of argv) ` +
	`buttons {"Not now", "OK"} ` +
	`default button "OK" cancel button "Not now" ` +
	`with title (item 2 of argv) with icon (POSIX file (item 3 of argv)) giving up after 120`

// dialogScriptNoIcon is the same dialog for the case where the icon could not
// be staged on disk. A missing logo is not a reason to skip the question.
const dialogScriptNoIcon = `display dialog (item 1 of argv) ` +
	`buttons {"Not now", "OK"} ` +
	`default button "OK" cancel button "Not now" ` +
	`with title (item 2 of argv) with icon note giving up after 120`

// updateButton labels the action button and is matched against the reply
const updateButton = "Update now"

// noticeScript announces the available upgrade and offers to take it.
const noticeScript = `display dialog (item 1 of argv) ` +
	`buttons {"Cancel", "` + updateButton + `"} ` +
	`default button "` + updateButton + `" cancel button "Cancel" ` +
	`with title (item 2 of argv) with icon (POSIX file (item 3 of argv)) giving up after 120`

// noticeScriptNoIcon is noticeScript for a failed staging.
const noticeScriptNoIcon = `display dialog (item 1 of argv) ` +
	`buttons {"Cancel", "` + updateButton + `"} ` +
	`default button "` + updateButton + `" cancel button "Cancel" ` +
	`with title (item 2 of argv) with icon note giving up after 120`

// runInTerminalScript opens Terminal on the upgrade command, so taking the
// upgrade needs no typing. The command travels in argv, never spliced into the
// source. "do script" returns as soon as the command starts.
const runInTerminalScript = `tell application "Terminal"
	activate
	do script (item 1 of argv)
end tell`

const affirmativeButton = "OK"

// affirmativeReply is the exact osascript result for the affirmative button.
// Matching the whole "button returned:" field rather than searching for the
// label alone matters now the label is two characters: a bare substring test
// would be satisfied by any future button, title or reply text containing it.
const affirmativeReply = "button returned:" + affirmativeButton

// stageIcon writes the embedded logo somewhere osascript can read it, and
// returns the path and a function to remove it. The dialog is synchronous, so
// the file only has to outlive the call.
//
// An empty path means staging failed, and the caller falls back to the
// iconless script.
func stageIcon() (path string, cleanup func()) {
	dir, err := os.MkdirTemp("", "prem-down-icon")
	if err != nil {
		return "", func() {}
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, "prem-down.icns")
	if err := os.WriteFile(path, dialogIcon, 0o600); err != nil {
		cleanup()
		return "", func() {}
	}
	return path, cleanup
}

// runDialog stages the logo, runs one of this file's scripts with args in argv,
// and returns osascript's reply. The staged icon's path is appended as the last
// argv item when there is one, which is why each script reads it from a
// different index.
//
// ok is false when the dialog could not be put up at all - a cancelled dialog
// raises AppleScript error -128, and a missing or blocked osascript fails the
// same way, so the two are not distinguishable here and each caller decides
// what that means.
func runDialog(withIcon, withoutIcon string, args ...string) (reply string, ok bool) {
	icon, cleanup := stageIcon()
	defer cleanup()

	script := withIcon
	if icon == "" {
		script = withoutIcon
	} else {
		args = append(args, icon)
	}
	cmd := exec.Command("/usr/bin/osascript", //nolint:gosec // G204: fixed binary and fixed script; the text travels in argv
		append([]string{
			"-e", "on run argv",
			"-e", script,
			"-e", "end run",
		}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (c *Checker) ask(question string, _ io.Reader, _ io.Writer) bool {
	// A dialog that could not be raised, and one that was dismissed, both mean
	// no.
	reply, ok := runDialog(dialogScript, dialogScriptNoIcon, question, c.Product)
	return ok && strings.Contains(reply, affirmativeReply)
}

// announceDialog shows the upgrade and, if the user takes it, starts it.
// It reports whether the dialog was shown at all, so a failure falls back to
// printing rather than losing the notice.
//
// Cancelling reports false too: AppleScript raises error -128 for the cancel
// button, which is indistinguishable here from a dialog that never appeared.
func (c *Checker) announceDialog(u Upgrade) bool {
	reply, ok := runDialog(noticeScript, noticeScriptNoIcon, c.dialogText(u), c.Product)
	if !ok {
		return false
	}
	if strings.Contains(reply, "button returned:"+updateButton) {
		c.takeUpgrade(u)
	}
	return true
}

// dialogText is the notice for this platform's dialog: that there is something
// newer, and the two versions to compare.
//
// The macOS box asks no question because the button says what it does; the
// Windows box, whose buttons are a fixed "Yes" and "No", has to ask.
func (c *Checker) dialogText(u Upgrade) string {
	return fmt.Sprintf("A new version is available!\nLatest version is %s (you have %s).",
		plainVersion(u.Version), plainVersion(c.Version))
}

// installerURL is the latest release's macOS installer. GitHub resolves
// /releases/latest/download/<asset> to whatever the newest release carries, so
// the version never has to be pasted in - and the name is the one publish.yml
// uploads.
func (c *Checker) installerURL() string {
	return "https://github.com/" + c.Repo + "/releases/latest/download/" +
		c.Product + "_installer_macos.pkg"
}

// upgradeCommand is the channel's own upgrade command where there is one, and
// otherwise downloading the installer and executing it.
func (c *Checker) upgradeCommand(u Upgrade) string {
	if u.Verb == verbRun {
		return u.Target
	}
	pkg := fmt.Sprintf(`"$TMPDIR/%s-installer.pkg"`, c.Product)
	return fmt.Sprintf(`curl -fL --progress-bar -o %s '%s' && open %s`, pkg, c.installerURL(), pkg)
}

// takeUpgrade acts on the pressed action button by running the channel's
// upgrade in a Terminal window.
func (c *Checker) takeUpgrade(u Upgrade) {
	//nolint:gosec // G204: fixed binary and fixed script; the command travels in argv
	_ = exec.Command("/usr/bin/osascript",
		"-e", "on run argv", "-e", runInTerminalScript, "-e", "end run", c.upgradeCommand(u)).Run()
}
