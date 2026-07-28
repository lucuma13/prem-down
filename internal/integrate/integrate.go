// Package integrate wires prem-down into the OS file manager: right-click a
// .prproj (or a Production's .prodset) and pick "Downgrade for older Premiere".
//
//   - macOS: installs a Finder Quick Action into ~/Library/Services
//     (integrate_darwin.go). The Homebrew cask runs this automatically after
//     install and removes it before uninstall; the .pkg installer's postinstall
//     runs it too, as the logged-in user (packaging/macos/scripts/postinstall).
//   - Windows: adds context-menu entries for .prproj and .prodset files under HKCU
//     (integrate_windows.go). The MSI installer ships equivalent HKLM keys, so
//     this is only needed for portable installs.
//
// "integrate off" undoes the wiring.
package integrate

import (
	"fmt"
	"io"
)

// Downgrader converts one file-manager selection and returns what to tell the
// user: a summary to show, and whether anything failed. Only the Windows COM
// handler calls one — it does the work in-process and reports in a message box
// — but the type is declared here so MaybeRunCOMServer has the same signature
// on every platform.
type Downgrader func(files []string) (summary string, failed bool)

func usageIntegrate(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Add a right-click "Downgrade for older Premiere" action on %s.

Usage: prem-down integrate [on|off]

Actions:
  on          add the action to the context menu
  off         remove it

With no action, the current status is shown.

Options:
  -h, --help  show this help
`, FileManagerName)
}

// Run executes the "integrate" subcommand and returns the process exit code for
// the caller to return, writing through the injected streams rather than
// os.Stdout/os.Stderr.
//
// It never pauses for the --gui prompt the way the downgrade path does: run
// dispatches "integrate" before parsing any flags, so --gui cannot be set when
// this is reached.
func Run(out, errw io.Writer, args []string) int {
	fatal := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(errw, format+"\n", a...)
		return 1
	}
	// The two directions are tracked separately so asking for both is an error.
	// Bare command reports the status.
	add, remove := false, false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			usageIntegrate(out)
			return 0
		case "on":
			add = true
		case "off":
			remove = true
		default:
			usageIntegrate(errw)
			return fatal("error: unknown action %s", a)
		}
	}
	if add && remove {
		usageIntegrate(errw)
		return fatal("error: on and off cannot be combined")
	}
	switch {
	case remove:
		if err := removeIntegration(); err != nil {
			return fatal("error: %v", err)
		}
		_, _ = fmt.Fprintln(out, integrationRemovedMessage)
	case add:
		if err := installIntegration(); err != nil {
			return fatal("error: %v", err)
		}
		_, _ = fmt.Fprintln(out, integrationInstalledMessage)
	default:
		installed, err := integrationInstalled()
		if err != nil {
			return fatal("error: %v", err)
		}
		state := "off"
		if installed {
			state = "on"
		}
		_, _ = fmt.Fprintf(out, "integrate: %s\n", state)
	}
	return 0
}
