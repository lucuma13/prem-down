package updates

import (
	"strings"
	"testing"
	"unsafe"
)

// MSGBOXPARAMSW is passed to Windows by address, and the first thing the API
// reads is cbSize: a struct laid out differently from the C one is either
// rejected or read past its own fields. Nothing about that shows up at compile
// time, and ask cannot be driven in a test at all so the layout is pinned here
// instead.
//
// The expected sizes come from the C declaration: four DWORD-sized fields
// (cbSize, dwStyle, dwLanguageId being UINT/DWORD, plus alignment) around six
// pointer-sized ones (hwndOwner, hInstance, lpszText, lpszCaption, lpszIcon,
// dwContextHelpId, lpfnMsgBoxCallback). On amd64 the two DWORDs that precede a
// pointer each carry four bytes of padding, and the trailing one is padded to
// the struct's alignment; on 386 nothing is padded.
func TestMsgBoxParamsLayout(t *testing.T) {
	var p msgBoxParamsW

	want := map[uintptr]uintptr{
		4: 40, // 386: no padding anywhere
		8: 80, // amd64/arm64: cbSize, dwStyle and dwLanguageID each padded to 8
	}[unsafe.Sizeof(uintptr(0))]
	if want == 0 {
		t.Skipf("no expected size recorded for a %d-byte pointer", unsafe.Sizeof(uintptr(0)))
	}
	if got := unsafe.Sizeof(p); got != want {
		t.Errorf("sizeof(MSGBOXPARAMSW) = %d, want %d", got, want)
	}

	// cbSize must be first: Windows reads it before anything else.
	if off := unsafe.Offsetof(p.cbSize); off != 0 {
		t.Errorf("cbSize is at offset %d, want 0", off)
	}
	// And every pointer field must sit on a pointer boundary, which is what the
	// natural-alignment argument in ask_windows.go rests on.
	for name, off := range map[string]uintptr{
		"hwndOwner":          unsafe.Offsetof(p.hwndOwner),
		"hInstance":          unsafe.Offsetof(p.hInstance),
		"lpszText":           unsafe.Offsetof(p.lpszText),
		"lpszCaption":        unsafe.Offsetof(p.lpszCaption),
		"lpszIcon":           unsafe.Offsetof(p.lpszIcon),
		"dwContextHelpID":    unsafe.Offsetof(p.dwContextHelpID),
		"lpfnMsgBoxCallback": unsafe.Offsetof(p.lpfnMsgBoxCallback),
	} {
		if off%unsafe.Sizeof(uintptr(0)) != 0 {
			t.Errorf("%s is at offset %d, which is not pointer-aligned", name, off)
		}
	}
}

// What "Yes" runs cannot be exercised here - it opens a console window - so the
// command it would run is pinned instead. A wrong one is silent: the window
// opens, something fails inside it, and the user is left with a prompt.
func TestUpgradeCommand(t *testing.T) {
	c := New("lucuma13/prem-down", "prem-down", "0.0.9")

	// A channel with its own upgrade command runs exactly that.
	const winget = "winget upgrade -e --id lucuma13.prem-down"
	if got := c.upgradeCommand(Upgrade{Verb: verbRun, Target: winget}); got != winget {
		t.Errorf("upgradeCommand = %q, want %q", got, winget)
	}

	// Anything else fetches the MSI and opens it. The URL has to be the
	// latest-release redirect and the asset name publish.yml uploads, or the
	// download 404s.
	got := c.upgradeCommand(Upgrade{Verb: verbDownload, Target: c.releasesPage()})
	for _, want := range []string{
		"curl -fL",
		"https://github.com/lucuma13/prem-down/releases/latest/download/prem-down_installer_windows.msi",
		`start ""`,
		`"%TEMP%\prem-down-installer.msi"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("installer command %q missing %q", got, want)
		}
	}
}

// The prompt's button behaviour is carried entirely by these flags, and getting
// them wrong is silent: a box with the wrong default opts the user in or out on
// a keystroke they did not read.
func TestPromptBoxOptions(t *testing.T) {
	if promptBoxOptions&mbYesNo == 0 {
		t.Error("the prompt must offer Yes/No")
	}
	// MB_DEFBUTTON2 would make "No" the default. Its absence is what puts Enter
	// on "Yes", matching the macOS dialog's default button.
	const mbDefButton2 = 0x00000100
	if promptBoxOptions&mbDefButton2 != 0 {
		t.Error("MB_DEFBUTTON2 is set, so Enter would decline rather than accept")
	}
	// The icon comes from the executable's own resources, so the style passed to
	// MessageBoxIndirectW must ask for a user icon and not a system one.
	if style := promptBoxOptions | mbUserIcon; style&mbIconQuestion != 0 {
		t.Error("the logo box must not also carry the system question-mark icon")
	}
}
