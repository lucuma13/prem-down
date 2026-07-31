package integrate

import (
	"strings"
	"testing"

	"github.com/lucuma13/prem-down/internal/premdown"
)

// The reg.exe invocations are what the MSI mirrors under HKLM and what Explorer
// parses; pin their shape without touching the real registry. The verb is a
// Drop Target: it needs the DropTarget\CLSID pointer plus the CLSID's own
// LocalServer32 registration for COM to activate prem-down.
func TestContextMenuRegAdds(t *testing.T) {
	exe := `C:\Tools\prem-down.exe`
	adds := contextMenuRegAdds(exe)
	// Three adds per file type (title, icon, DropTarget), plus the two that
	// register the shared COM handler once.
	if want := 3*len(contextMenuKeys) + 2; len(adds) != want {
		t.Fatalf("expected %d reg add commands, got %d", want, len(adds))
	}
	for _, args := range adds {
		if args[0] != "add" {
			t.Errorf("not a reg add: %v", args)
		}
		if args[len(args)-1] != "/f" {
			t.Errorf("reg add not forced (would prompt): %v", args)
		}
	}

	dropTargets, sawLocalServer := 0, false
	for _, args := range adds {
		key, value := args[1], args[len(args)-2]
		switch {
		case strings.HasSuffix(key, `\DropTarget`):
			dropTargets++
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
	// Every file type needs its own verb, and all of them must resolve to the
	// single handler - otherwise one of the two menu entries does nothing.
	if dropTargets != len(contextMenuKeys) {
		t.Errorf("got %d DropTarget\\CLSID reg adds, want %d", dropTargets, len(contextMenuKeys))
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

// Productions are reached through the .prodset entry. If that key were ever
// dropped, right-clicking a Production's settings file would silently offer
// nothing, so pin both file types explicitly.
func TestContextMenuCoversProjectsAndProductions(t *testing.T) {
	// Pinned against the engine's own extensions: the menu must cover exactly the
	// file types prem-down knows how to convert.
	for _, ext := range []string{premdown.PrprojExt, premdown.ProdsetExt} {
		found := false
		for _, key := range contextMenuKeys {
			if strings.Contains(key, ext+`\shell\`) {
				found = true
			}
		}
		if !found {
			t.Errorf("no context-menu key for %s in %v", ext, contextMenuKeys)
		}
	}
	// Folders are deliberately excluded: a Directory verb would appear on every
	// folder on the machine.
	for _, key := range contextMenuKeys {
		if strings.Contains(key, `\Directory\`) {
			t.Errorf("context menu registered on all folders: %q", key)
		}
	}
}
