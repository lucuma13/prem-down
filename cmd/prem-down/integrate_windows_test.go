// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"strings"
	"testing"
)

// The reg.exe invocations are what the MSI mirrors under HKLM and what
// Explorer parses; pin their shape without touching the real registry.
func TestContextMenuRegAdds(t *testing.T) {
	exe := `C:\Tools\prem-down.exe`
	adds := contextMenuRegAdds(exe)
	if len(adds) != 3 {
		t.Fatalf("expected 3 reg add commands, got %d", len(adds))
	}
	for _, args := range adds {
		if args[0] != "add" || !strings.HasPrefix(args[1], contextMenuKey) {
			t.Errorf("unexpected reg command: %v", args)
		}
		if args[len(args)-1] != "/f" {
			t.Errorf("reg add not forced (would prompt): %v", args)
		}
	}
	command := adds[2]
	wantCmd := `"C:\Tools\prem-down.exe" --gui "%1"`
	if got := command[len(command)-2]; got != wantCmd {
		t.Errorf("command value = %q, want %q", got, wantCmd)
	}
}
