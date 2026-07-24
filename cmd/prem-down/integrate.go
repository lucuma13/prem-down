// The "integrate" subcommand wires prem-down into the OS file manager so
// non-technical editors never need a terminal: right-click a .prproj and pick
// "Downgrade".
//
//   - macOS: installs a Finder Quick Action into ~/Library/Services
//     (integrate_darwin.go). The Homebrew cask runs this automatically after
//     install and removes it before uninstall; the .pkg installer's postinstall
//     runs it too, as the logged-in user (packaging/macos/scripts/postinstall).
//   - Windows: adds a context-menu entry for .prproj files under HKCU
//     (integrate_windows.go). The MSI installer ships equivalent HKLM keys, so
//     this is only needed for portable installs.
//
// "integrate --remove" undoes the wiring.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"fmt"
	"io"
)

func usageIntegrate(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: prem-down integrate [--remove]

Add a right-click "Downgrade" action for .prproj files (%s).

Options:
      --remove    remove the right-click action instead
  -h, --help      show this help
`, integrationKind)
}

func (c *cli) integrate(args []string) int {
	remove := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			usageIntegrate(c.out)
			return 0
		case "--remove":
			remove = true
		default:
			usageIntegrate(c.err)
			return c.fatal("error: unknown option %s", a)
		}
	}
	if remove {
		if err := removeIntegration(); err != nil {
			return c.fatal("error: %v", err)
		}
		_, _ = fmt.Fprintln(c.out, integrationRemovedMessage)
		return 0
	}
	if err := installIntegration(); err != nil {
		return c.fatal("error: %v", err)
	}
	_, _ = fmt.Fprintln(c.out, integrationInstalledMessage)
	return 0
}
