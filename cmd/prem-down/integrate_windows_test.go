// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"strings"
	"testing"
)

// The reg.exe invocations are what the MSI mirrors under HKLM and what Explorer
// parses; pin their shape without touching the real registry. The verb is a
// Drop Target: it needs the DropTarget\CLSID pointer plus the CLSID's own
// LocalServer32 registration for COM to activate prem-down.
func TestContextMenuRegAdds(t *testing.T) {
	exe := `C:\Tools\prem-down.exe`
	adds := contextMenuRegAdds(exe)
	if len(adds) != 5 {
		t.Fatalf("expected 5 reg add commands, got %d", len(adds))
	}
	for _, args := range adds {
		if args[0] != "add" {
			t.Errorf("not a reg add: %v", args)
		}
		if args[len(args)-1] != "/f" {
			t.Errorf("reg add not forced (would prompt): %v", args)
		}
	}

	var sawDropTarget, sawLocalServer bool
	for _, args := range adds {
		key, value := args[1], args[len(args)-2]
		switch {
		case strings.HasSuffix(key, `\DropTarget`):
			sawDropTarget = true
			if value != dropHandlerCLSID {
				t.Errorf("DropTarget CLSID = %q, want %q", value, dropHandlerCLSID)
			}
		case strings.HasSuffix(key, `\LocalServer32`):
			sawLocalServer = true
			if want := `"` + exe + `"`; value != want {
				t.Errorf("LocalServer32 = %q, want %q", value, want)
			}
		}
	}
	if !sawDropTarget {
		t.Error("no DropTarget\\CLSID reg add")
	}
	if !sawLocalServer {
		t.Error("no CLSID\\LocalServer32 reg add")
	}

	// The CLSID key the handler is registered under must carry the same class id
	// the verb points at, or Explorer's activation would find nothing.
	if !strings.Contains(clsidKey, dropHandlerCLSID) {
		t.Errorf("clsidKey %q does not reference CLSID %q", clsidKey, dropHandlerCLSID)
	}
}

// hasEmbeddingArg gates whether prem-down becomes the COM server, so it must
// recognise exactly COM's activation flag and nothing a normal user would type.
func TestHasEmbeddingArg(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-Embedding"}, true},
		{[]string{"/Embedding"}, true},
		{[]string{"-embedding"}, true},
		{[]string{"a.prproj", "-Embedding"}, true},
		{nil, false},
		{[]string{"a.prproj", "b.prproj"}, false},
		{[]string{"--gui", "a.prproj"}, false},
		{[]string{"integrate"}, false},
	} {
		if got := hasEmbeddingArg(tc.args); got != tc.want {
			t.Errorf("hasEmbeddingArg(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// makeCmdLine must quote each argument so paths with spaces survive as one
// argument through CreateProcess -> CommandLineToArgvW.
func TestMakeCmdLine(t *testing.T) {
	got := makeCmdLine([]string{`C:\Program Files\prem-down\prem-down.exe`, "--gui", `C:\My Clips\a b.prproj`})
	want := `"C:\Program Files\prem-down\prem-down.exe" --gui "C:\My Clips\a b.prproj"`
	if got != want {
		t.Errorf("makeCmdLine =\n  %q\nwant\n  %q", got, want)
	}
}
