// The "updates" subcommand: the way to set the check from a terminal, for
// users who never see the first-run question because they do not use the
// file-manager integration, and for anyone changing their mind later.

package updates

import (
	"fmt"
	"io"
)

func (c *Checker) commandName() string {
	if c.CommandName != "" {
		return c.CommandName
	}
	return DefaultCommandName
}

func (c *Checker) usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Check for new %s versions.

Usage: %s %s [on|off]

Actions:
  on          check for new versions automatically
  off         never check

With no action, the current status is shown.

Options:
  -h, --help  show this help
`, c.Product, c.Product, c.commandName())
}

// reportAction confirms the setting the user just applied.
func reportAction(out io.Writer, action, was string) {
	switch {
	case action == "on" && was == stateOn:
		_, _ = fmt.Fprintln(out, "Update checks are already on.")
	case action == "on":
		_, _ = fmt.Fprintln(out, "Update checks are on.")
	case was == stateOff:
		_, _ = fmt.Fprintln(out, "Update checks are already off.")
	default:
		_, _ = fmt.Fprintln(out, "Update checks are off.")
	}
}

// Command executes the updates subcommand and returns the process exit code
// for the caller to return, writing through the injected streams rather than
// os.Stdout/os.Stderr.
func (c *Checker) Command(out, errw io.Writer, args []string) int {
	fatal := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(errw, format+"\n", a...)
		return 1
	}

	action := "" // no action: report, and change nothing
	for _, a := range args {
		switch a {
		case "-h", "--help":
			c.usage(out)
			return 0
		case "on", "off":
			action = a
		default:
			c.usage(errw)
			return fatal("error: unknown action %s", a)
		}
	}

	s, err := c.load()
	if err != nil {
		return fatal("error: %v", err)
	}

	// An action reports what it changed rather than restating the resulting
	// state. The status report is left to a bare invocation.
	if action != "" {
		was := s.Updates
		switch action {
		case "on":
			s.Updates = stateOn
		case "off":
			s.Updates = stateOff
			s.LatestSeen = "" // drop any pending notice along with the setting
		}
		if err := c.save(s); err != nil {
			return fatal("error: %v", err)
		}
		reportAction(out, action, was)
		return 0
	}

	switch s.Updates {
	case stateOn:
		_, _ = fmt.Fprintln(out, "Update checks are on.")
	case stateOff:
		_, _ = fmt.Fprintln(out, "Update checks are off.")
	default:
		_, _ = fmt.Fprintf(out, "Update checks are not set.\n"+
			"Set them with %s %s on/off\n", c.Product, c.commandName())
	}
	return 0
}
