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
// "integrate --remove" undoes the wiring.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.
package integrate

import (
	"fmt"
	"io"
)

func usageIntegrate(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: prem-down integrate [--remove]

Add a right-click "Downgrade for older Premiere" action for .prproj project
files and .prodset Production settings files (%s).

Options:
      --remove    remove the right-click action instead
  -h, --help      show this help
`, integrationKind)
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
	remove := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			usageIntegrate(out)
			return 0
		case "--remove":
			remove = true
		default:
			usageIntegrate(errw)
			return fatal("error: unknown option %s", a)
		}
	}
	if remove {
		if err := removeIntegration(); err != nil {
			return fatal("error: %v", err)
		}
		_, _ = fmt.Fprintln(out, integrationRemovedMessage)
		return 0
	}
	if err := installIntegration(); err != nil {
		return fatal("error: %v", err)
	}
	_, _ = fmt.Fprintln(out, integrationInstalledMessage)
	return 0
}
