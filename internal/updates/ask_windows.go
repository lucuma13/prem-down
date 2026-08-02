// The Windows first-run prompt.
//
// A run launched from Explorer's context menu has no console - the shell
// activates the handler as a background COM server - so the question is put in
// a message box, the same way the result is reported.

package updates

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	moduser32               = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW         = moduser32.NewProc("MessageBoxW")
	procMessageBoxIndirectW = moduser32.NewProc("MessageBoxIndirectW")
	modkernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandleW    = modkernel32.NewProc("GetModuleHandleW")
)

const (
	mbYesNo          = 0x00000004
	mbIconQuestion   = 0x00000020
	mbUserIcon       = 0x00000080
	mbSetForeground  = 0x00010000
	mbTopmost        = 0x00040000
	idYes            = 6
	promptBoxOptions = mbYesNo | mbSetForeground | mbTopmost
)

const iconResourceName = "APP"

// msgBoxParamsW is MSGBOXPARAMSW.
//
// The fields are declared in the C struct's order and nothing else: Go's
// natural alignment reproduces the C padding on every Windows architecture,
// where explicit padding fields would be right on amd64 and wrong on 386.
// TestMsgBoxParamsLayout pins the resulting size.
type msgBoxParamsW struct {
	cbSize             uint32
	hwndOwner          uintptr
	hInstance          uintptr
	lpszText           *uint16
	lpszCaption        *uint16
	dwStyle            uint32
	lpszIcon           *uint16
	dwContextHelpID    uintptr
	lpfnMsgBoxCallback uintptr
	dwLanguageID       uint32
}

// askWithLogo puts the question in a message box carrying the executable's own
// icon, reporting false if that box could not be shown at all.
//
// The icon can be missing - the resource is only linked into the build the
// .syso covers - and a message box that cannot be raised returns zero, which is
// indistinguishable from a decline. Neither may be read as an answer, so both
// send the caller to the plain box instead: a generic icon is worth far less
// than the question.
func askWithLogo(text, caption *uint16) (uintptr, bool) {
	icon, err := syscall.UTF16PtrFromString(iconResourceName)
	if err != nil {
		return 0, false
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0) // the running .exe
	if hInstance == 0 {
		return 0, false
	}
	p := msgBoxParamsW{
		hInstance:   hInstance,
		lpszText:    text,
		lpszCaption: caption,
		dwStyle:     promptBoxOptions | mbUserIcon,
		lpszIcon:    icon,
	}
	p.cbSize = uint32(unsafe.Sizeof(p))
	//nolint:gosec // G103: the uintptr conversion is the Win32 calling convention; the buffers are kept alive below.
	r, _, _ := procMessageBoxIndirectW.Call(uintptr(unsafe.Pointer(&p)))
	// Those conversions are invisible to the GC; hold everything the call read
	// through until it has returned.
	runtime.KeepAlive(&p)
	runtime.KeepAlive(text)
	runtime.KeepAlive(caption)
	runtime.KeepAlive(icon)
	if r == 0 {
		return 0, false
	}
	return r, true
}

// messageBox puts text to the user as a Yes/No box and returns the button they
// pressed.
//
// The logo box is tried first and the system-icon one is the fallback.
func messageBox(text, caption string) (button uintptr, ok bool) {
	t, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return 0, false
	}
	c, err := syscall.UTF16PtrFromString(caption)
	if err != nil {
		return 0, false
	}
	// A message box runs a modal message loop on the calling thread, so pin the
	// goroutine to one OS thread while one is up.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if r, ok := askWithLogo(t, c); ok {
		return r, true
	}
	//nolint:gosec // G103: the uintptr conversions are the Win32 calling convention; the buffers are kept alive below.
	r, _, _ := procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)),
		promptBoxOptions|mbIconQuestion)
	runtime.KeepAlive(t)
	runtime.KeepAlive(c)
	return r, r != 0
}

func (c *Checker) ask(question string, _ io.Reader, _ io.Writer) bool {
	r, ok := messageBox(question, c.Product)
	return ok && r == idYes
}

// announceDialog shows the upgrade and optionally starts it. It reports whether
// the box was shown, so a failure falls back to printing.
//
// The buttons read "Yes" and "No" rather than "Update now" and "Cancel":
// MessageBox offers fixed button sets, and relabelling needs
// TaskDialogIndirect, which needs a comctl32 v6 manifest this binary does not
// ship. The text is what carries the meaning.
func (c *Checker) announceDialog(u Upgrade) bool {
	r, ok := messageBox(c.dialogText(u), c.Product)
	if !ok {
		return false
	}
	if r == idYes {
		c.takeUpgrade(u)
	}
	return true
}

// dialogText is the notice for this platform's box: that there is something
// newer, the two versions to compare, and the question its buttons answer.
func (c *Checker) dialogText(u Upgrade) string {
	return fmt.Sprintf("A new version is available!\nLatest is %s (you have %s). Do you want to update now?",
		plainVersion(u.Version), plainVersion(c.Version))
}

// upgradeCommand is what taking the upgrade runs for this install channel: the
// channel's own command where there is one, and otherwise fetching the MSI and
// handing it to the shell, which is what double-clicking it would do.
//
// curl ships with Windows 10 and later. The paths are quoted for cmd.exe;
// Product is a program name set by the host, not user input.
func (c *Checker) upgradeCommand(u Upgrade) string {
	if u.Verb == verbRun {
		return u.Target
	}
	msi := fmt.Sprintf(`"%%TEMP%%\%s-installer.msi"`, c.Product)
	url := "https://github.com/" + c.Repo + "/releases/latest/download/" +
		c.Product + "_installer_windows.msi"
	return fmt.Sprintf(`curl -fL -o %s "%s" && start "" %s`, msi, url, msi)
}

// takeUpgrade runs the channel's upgrade in a console window the user can
// watch, because winget prints and prompts and a silent download looks like a
// button that did nothing. /K keeps the window open afterwards so its result
// can be read.
//
// Failure is ignored: this run is about to exit, and there is nothing useful
// left to say.
func (c *Checker) takeUpgrade(u Upgrade) {
	//nolint:gosec // G204: cmd.exe is fixed; the command is this package's own upgrade hint, not user input
	_ = exec.Command("cmd", "/C", "start", "", "cmd", "/K", c.upgradeCommand(u)).Start()
}
