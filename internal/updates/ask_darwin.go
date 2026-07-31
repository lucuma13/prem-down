// The macOS first-run prompt.
//
// A run launched from Finder has no terminal to ask in - a Quick Action's
// output is captured and shown as a notification once the process has already
// exited - so the question is raised as an AppleScript dialog.

package updates

import (
	"io"
	"os/exec"
	"strings"
)

// dialogScript is the AppleScript body. The question and the title arrive
// through argv and are never spliced into the source, so nothing in them can
// break quoting.
//
// "Not now" is both the plain button and the cancel button, so Escape and the
// close box land on the same answer as clicking it. "giving up after" keeps an
// ignored dialog from pinning a background launch open indefinitely; giving up
// returns no button, which reads as no.
const dialogScript = `display dialog (item 1 of argv) ` +
	`buttons {"Not now", "Check for updates"} ` +
	`default button "Check for updates" cancel button "Not now" ` +
	`with title (item 2 of argv) with icon note giving up after 120`

const affirmativeButton = "Check for updates"

func (c *Checker) ask(question string, _ io.Reader, _ io.Writer) bool {
	cmd := exec.Command("/usr/bin/osascript", //nolint:gosec // G204: fixed binary and fixed script; the text travels in argv
		"-e", "on run argv",
		"-e", dialogScript,
		"-e", "end run",
		question, c.Product)
	out, err := cmd.Output()
	if err != nil {
		// Cancel raises AppleScript error -128, which exits non-zero; so does a
		// missing or blocked osascript. Every one of them means "no".
		return false
	}
	return strings.Contains(string(out), affirmativeButton)
}
