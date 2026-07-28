// The "updates" subcommand: the way to set the check from a terminal, for
// users who never see the first-run question because they do not use the
// file-manager integration, and for anyone changing their mind later.

package updates

import (
	"fmt"
	"io"
	"time"
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

	switch action {
	case "on":
		s.Updates = stateOn
	case "off":
		s.Updates = stateOff
		s.LatestSeen = "" // drop any pending notice along with the setting
	}
	if action != "" {
		if err := c.save(s); err != nil {
			return fatal("error: %v", err)
		}
	}

	path, err := c.configPath()
	if err != nil {
		return fatal("error: %v", err)
	}
	switch s.Updates {
	case stateOn:
		_, _ = fmt.Fprintf(out, "%s: on\n", c.commandName())
	case stateOff:
		_, _ = fmt.Fprintf(out, "%s: off\n", c.commandName())
	default:
		_, _ = fmt.Fprintf(out, "%s: not set (nothing is checked until you are asked, or set it here)\n", c.commandName())
	}
	_, _ = fmt.Fprintf(out, "settings file: %s\n", path)
	if s.Updates == stateOn && !s.LastChecked.IsZero() {
		_, _ = fmt.Fprintf(out, "last checked: %s (latest release: %s)\n",
			s.LastChecked.Local().Format(time.RFC1123), s.LatestSeen)
	}
	return 0
}
