// Windows implementation of the "integrate" subcommand: a File Explorer
// context-menu entry for .prproj files, plus the COM handler that entry points
// at. This file has two halves: the installer (writes the registry keys) and
// the Drop Target handler (runs when Explorer invokes the entry).
//
// The keys live under HKCU\Software\Classes so no elevation is needed, and are
// written by shelling out to reg.exe (always present) rather than pulling in a
// registry dependency. The MSI installer writes the same entries under HKLM for
// all users; when both exist Explorer shows the HKCU one.
//
// The verb is implemented as a Drop Target, not a plain command: its CLSID
// resolves to prem-down's own COM LocalServer (this same exe), so selecting
// several .prproj files and invoking the entry hands the whole selection to a
// single prem-down process. A command verb ("exe" "%1"), by contrast, makes
// Explorer launch one process — one console window — per selected file.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	contextMenuKey   = `HKCU\Software\Classes\SystemFileAssociations\.prproj\shell\prem-down`
	contextMenuTitle = "Downgrade for older Premiere"

	// dropHandlerCLSID identifies prem-down's Drop Target COM handler. It is a
	// fixed, private class id generated once for this project: it must stay
	// constant so upgrades and "integrate --remove" locate the same
	// registration, and it must not be reused for anything else. The handler it
	// points at is the Drop Target COM server in the second half of this file.
	dropHandlerCLSID = "{4D9F2A18-7C3B-4E6A-B1F5-2A8C6D0E9F34}"
	dropHandlerName  = "prem-down Premiere downgrade handler"
	clsidKey         = `HKCU\Software\Classes\CLSID\` + dropHandlerCLSID

	fileManagerName = "File Explorer"
	integrationKind = "a File Explorer context-menu entry"

	integrationInstalledMessage = `Installed the File Explorer context-menu entry: right-click a .prproj file and
pick "` + contextMenuTitle + `".`
	integrationRemovedMessage = "Removed the File Explorer context-menu entry."
)

// contextMenuRegAdds returns the reg.exe argument lists that create the
// context-menu entry. The verb is implemented as a Drop Target rather than a
// plain command: its DropTarget\CLSID points at prem-down's own COM handler,
// registered as a LocalServer32 on the same exe, so Explorer packages an entire
// multi-file selection into one Drop call and a single prem-down process
// downgrades them all. COM appends "-Embedding" to the LocalServer32 command
// when it activates the handler; maybeRunCOMServer (below) detects that and
// enters server mode. Split out from installIntegration so the exact keys and
// values are unit-testable without touching the registry.
func contextMenuRegAdds(exe string) [][]string {
	return [][]string{
		{"add", contextMenuKey, "/ve", "/t", "REG_SZ", "/d", contextMenuTitle, "/f"},
		{"add", contextMenuKey, "/v", "Icon", "/t", "REG_SZ", "/d", exe, "/f"},
		{
			"add", contextMenuKey + `\DropTarget`, "/v", "CLSID", "/t", "REG_SZ",
			"/d", dropHandlerCLSID, "/f",
		},
		{"add", clsidKey, "/ve", "/t", "REG_SZ", "/d", dropHandlerName, "/f"},
		{
			"add", clsidKey + `\LocalServer32`, "/ve", "/t", "REG_SZ",
			"/d", fmt.Sprintf(`"%s"`, exe), "/f",
		},
	}
}

func installIntegration() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate own executable: %w", err)
	}
	// Resolve symlinks (e.g. the winget Links shim) so the registry points at
	// a path that keeps working if the shim is recreated elsewhere... the
	// resolved target is also what stays valid longest for manual installs.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	for _, args := range contextMenuRegAdds(exe) {
		if out, err := exec.Command("reg", args...).CombinedOutput(); err != nil { //nolint:gosec // G204: "reg" is constant; args are built internally from the resolved own-executable path, not external input
			return fmt.Errorf("reg %s: %v: %s", args[0], err, out)
		}
	}
	return nil
}

func removeIntegration() error {
	// Missing key means already removed: a failing reg query (key absent) is
	// treated as success and skipped, so a double --remove stays quiet. Both the
	// verb key and the CLSID registration are removed.
	for _, key := range []string{contextMenuKey, clsidKey} {
		if err := exec.Command("reg", "query", key).Run(); err != nil { //nolint:gosec // G204: key is one of the two package constants above, not external input
			continue
		}
		if out, err := exec.Command("reg", "delete", key, "/f").CombinedOutput(); err != nil { //nolint:gosec // G204: key is one of the two package constants above, not external input
			return fmt.Errorf("reg delete %s: %v: %s", key, err, out)
		}
	}
	return nil
}

// ===========================================================================
// Drop Target COM handler
//
// When COM activates the handler registered above (relaunching this exe with
// "-Embedding"), the code below registers a class factory, waits for Explorer's
// Drop, pulls the selected paths out of the CF_HDROP, and relaunches
// "prem-down --gui <files...>" in one fresh console window. The actual
// downgrade and the "press Enter" pause then run through the normal CLI path in
// main(), which already batches any number of files.
//
// The COM plumbing is hand-rolled on the syscall package (vtables built with
// syscall.NewCallback) so prem-down stays a single dependency-free binary. Only
// the two interfaces Explorer needs are implemented: IClassFactory (to vend the
// handler) and IDropTarget (to receive the selection).
// ===========================================================================

// ole32, shell32 and user32 are all on Windows' KnownDLLs list, so they are
// always resolved from System32 and NewLazyDLL cannot be DLL-planted here.
var (
	modole32    = syscall.NewLazyDLL("ole32.dll")
	modshell32  = syscall.NewLazyDLL("shell32.dll")
	moduser32   = syscall.NewLazyDLL("user32.dll")
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCoInitializeEx        = modole32.NewProc("CoInitializeEx")
	procCoUninitialize        = modole32.NewProc("CoUninitialize")
	procCoRegisterClassObject = modole32.NewProc("CoRegisterClassObject")
	procCoRevokeClassObject   = modole32.NewProc("CoRevokeClassObject")
	procCLSIDFromString       = modole32.NewProc("CLSIDFromString")
	procReleaseStgMedium      = modole32.NewProc("ReleaseStgMedium")

	procDragQueryFileW = modshell32.NewProc("DragQueryFileW")

	procGetMessageW        = moduser32.NewProc("GetMessageW")
	procTranslateMessage   = moduser32.NewProc("TranslateMessage")
	procDispatchMessageW   = moduser32.NewProc("DispatchMessageW")
	procPostThreadMessageW = moduser32.NewProc("PostThreadMessageW")
	procGetCurrentThreadID = moduser32.NewProc("GetCurrentThreadId")
	procShowWindow         = moduser32.NewProc("ShowWindow")

	procGetConsoleWindow = modkernel32.NewProc("GetConsoleWindow")
)

// ptr converts a Go pointer to the uintptr a syscall argument list wants. It is
// the single audited use of unsafe.Pointer for argument passing: routing every
// call through it keeps the rest of the file free of raw conversions (so go
// vet's unsafeptr and gosec's G103 have one place to look), and taking a
// pointer's address through it escapes the pointee to the heap, where Go's
// non-moving GC leaves it put for the duration of the call.
//
//nolint:gosec // G103: Win32/COM interop is inherently unsafe; audited here.
func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

// call invokes a Win32/COM entry point and returns its primary result. These
// APIs report failure through that return value (checked at the call site where
// it matters) or, for the void UI calls, not at all; LazyProc.Call's error is
// the always-non-nil GetLastError shim, so it is intentionally discarded.
func call(p *syscall.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

// failed reports whether an HRESULT indicates failure (its sign bit is set),
// without narrowing the uintptr — the FAILED() macro, spelled to keep gosec's
// integer-overflow check (G115) quiet.
func failed(hr uintptr) bool { return hr&0x80000000 != 0 }

const (
	coinitApartmentThreaded = 0x2
	clsctxLocalServer       = 0x4
	regclsSingleUse         = 0 // REGCLS_SINGLEUSE

	sOK               = 0
	eNoInterface      = uintptr(0x80004002)
	classENoAggregate = uintptr(0x80040110) // CLASS_E_NOAGGREGATION

	cfHDROP         = 15
	dvaspectContent = 1
	tymedHGlobal    = 1
	dropeffectCopy  = 1

	wmQuit           = 0x0012
	createNewConsole = 0x00000010
	swHide           = 0 // ShowWindow: hide the window

	// serverTimeout bounds how long an activated server lingers if Explorer
	// never drives it to a Drop (e.g. the activation is abandoned), so it can
	// never hang around as an orphaned background process.
	serverTimeout = 60 * time.Second
)

// serverThreadID is the STA thread running the message pump; Drop (and the
// safety timer) post WM_QUIT to it to end the pump. Kept as uintptr (a thread
// id fits) so it feeds PostThreadMessageW without a narrowing conversion.
var serverThreadID uintptr

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// The two interface ids Explorer asks our objects for.
var (
	iidIUnknown      = guid{0x00000000, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIClassFactory = guid{0x00000001, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIDropTarget   = guid{0x00000122, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

func guidEqual(a, b *guid) bool {
	return a.Data1 == b.Data1 && a.Data2 == b.Data2 && a.Data3 == b.Data3 && a.Data4 == b.Data4
}

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type iClassFactoryVtbl struct {
	iUnknownVtbl
	CreateInstance uintptr
	LockServer     uintptr
}

type iDropTargetVtbl struct {
	iUnknownVtbl
	DragEnter uintptr
	DragOver  uintptr
	DragLeave uintptr
	Drop      uintptr
}

// iDataObjectVtbl mirrors just enough of the IDataObject vtable to call GetData
// (the fourth entry). Walking Explorer's object through a typed struct avoids a
// uintptr->unsafe.Pointer vtable computation; the trailing methods are unused.
type iDataObjectVtbl struct {
	iUnknownVtbl
	GetData uintptr
}

// A COM object is just a struct whose first field is a pointer to its vtable;
// the pointer to the struct is the interface pointer Explorer holds. A single
// static instance of each suffices (this is a single-use, single-thread server).
type (
	classFactory struct{ vtbl *iClassFactoryVtbl }
	dropTarget   struct{ vtbl *iDropTargetVtbl }
)

var (
	factoryVtbl        iClassFactoryVtbl
	dropVtbl           iDropTargetVtbl
	factoryInstance    classFactory
	dropTargetInstance dropTarget
)

// formatEtc / stgMedium mirror the Win32 structs; Go inserts the same alignment
// padding the C layouts have on amd64 (ptd and hGlobal land on 8-byte offsets).
// Fields we neither set nor read — the target-device pointer and the fields the
// shell fills in and ReleaseStgMedium consumes — are blank so they hold their
// place in the layout without tripping the unused-field check.
type formatEtc struct {
	cfFormat uint16
	_        uintptr // ptd (DVTARGETDEVICE*, left NULL)
	dwAspect uint32
	lindex   int32
	tymed    uint32
}

type stgMedium struct {
	_       uint32  // tymed (set by the shell, unread)
	_       uint32  // padding, aligns hGlobal to 8
	hGlobal uintptr // the HDROP we read
	_       uintptr // pUnkForRelease (consumed by ReleaseStgMedium)
}

// setupCOMObjects wires the vtables to Go callbacks. Refcounting is deliberately
// trivial — AddRef/Release return a constant nonzero so the static objects are
// never considered freed; the process controls its own lifetime and exits right
// after the Drop.
func setupCOMObjects() {
	factoryVtbl = iClassFactoryVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: syscall.NewCallback(factoryQueryInterface),
			AddRef:         syscall.NewCallback(comAddRefRelease),
			Release:        syscall.NewCallback(comAddRefRelease),
		},
		CreateInstance: syscall.NewCallback(factoryCreateInstance),
		LockServer:     syscall.NewCallback(comLockServer),
	}
	factoryInstance.vtbl = &factoryVtbl

	dropVtbl = iDropTargetVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: syscall.NewCallback(dropQueryInterface),
			AddRef:         syscall.NewCallback(comAddRefRelease),
			Release:        syscall.NewCallback(comAddRefRelease),
		},
		DragEnter: syscall.NewCallback(dropDragEnter),
		DragOver:  syscall.NewCallback(dropDragOver),
		DragLeave: syscall.NewCallback(dropDragLeave),
		Drop:      syscall.NewCallback(dropDrop),
	}
	dropTargetInstance.vtbl = &dropVtbl
}

// The COM methods take their pointer arguments as typed pointers rather than
// uintptr so their bodies never convert a uintptr back into a pointer — that
// keeps them clean under go vet's unsafeptr check. Scalar-by-value slots (a
// DWORD grfKeyState, a POINTL pt — 8 bytes, one register slot on amd64) that we
// ignore stay as uintptr.
func comAddRefRelease(_ unsafe.Pointer) uintptr { return 1 }

func comLockServer(_ unsafe.Pointer, _ uintptr) uintptr { return sOK }

func factoryQueryInterface(this unsafe.Pointer, riid *guid, ppv *unsafe.Pointer) uintptr {
	if guidEqual(riid, &iidIUnknown) || guidEqual(riid, &iidIClassFactory) {
		*ppv = this
		return sOK
	}
	*ppv = nil
	return eNoInterface
}

func factoryCreateInstance(_ unsafe.Pointer, pUnkOuter unsafe.Pointer, riid *guid, ppv *unsafe.Pointer) uintptr {
	if pUnkOuter != nil { // aggregation is not supported
		*ppv = nil
		return classENoAggregate
	}
	if guidEqual(riid, &iidIUnknown) || guidEqual(riid, &iidIDropTarget) {
		*ppv = unsafe.Pointer(&dropTargetInstance) //nolint:gosec // G103: hand back our static handler as the new instance.
		return sOK
	}
	*ppv = nil
	return eNoInterface
}

func dropQueryInterface(this unsafe.Pointer, riid *guid, ppv *unsafe.Pointer) uintptr {
	if guidEqual(riid, &iidIUnknown) || guidEqual(riid, &iidIDropTarget) {
		*ppv = this
		return sOK
	}
	*ppv = nil
	return eNoInterface
}

// DragEnter/DragOver report that a copy would be accepted so Explorer proceeds
// to Drop. DragLeave is a no-op.
func dropDragEnter(_ unsafe.Pointer, _ unsafe.Pointer, _ uintptr, _ uintptr, pdwEffect *uint32) uintptr {
	*pdwEffect = dropeffectCopy
	return sOK
}

func dropDragOver(_ unsafe.Pointer, _ uintptr, _ uintptr, pdwEffect *uint32) uintptr {
	*pdwEffect = dropeffectCopy
	return sOK
}

func dropDragLeave(_ unsafe.Pointer) uintptr { return sOK }

func dropDrop(_ unsafe.Pointer, pDataObj unsafe.Pointer, _ uintptr, _ uintptr, pdwEffect *uint32) uintptr {
	*pdwEffect = dropeffectCopy
	if files := extractHDROPFiles(pDataObj); len(files) > 0 {
		_ = launchDowngradeConsole(files)
	}
	// End the message pump: the selection has been handed off (or there was
	// nothing to hand off). Runs on the STA thread, so posting to it is safe.
	call(procPostThreadMessageW, serverThreadID, wmQuit, 0, 0)
	return sOK
}

// extractHDROPFiles pulls the selected paths out of the data object Explorer
// passed to Drop. It asks for CF_HDROP as an HGLOBAL, enumerates the paths with
// DragQueryFileW, then releases the medium.
func extractHDROPFiles(pDataObj unsafe.Pointer) []string {
	fe := formatEtc{cfFormat: cfHDROP, dwAspect: dvaspectContent, lindex: -1, tymed: tymedHGlobal}
	var med stgMedium
	dataObj := (*struct{ vtbl *iDataObjectVtbl })(pDataObj)
	hr, _, _ := syscall.SyscallN(dataObj.vtbl.GetData, uintptr(pDataObj), ptr(&fe), ptr(&med))
	if failed(hr) {
		return nil
	}
	defer call(procReleaseStgMedium, ptr(&med))

	hDrop := med.hGlobal
	count := call(procDragQueryFileW, hDrop, 0xFFFFFFFF, 0, 0)
	files := make([]string, 0, int(count))
	for i := 0; i < int(count); i++ {
		// A first call with a nil buffer returns the length (excluding the NUL),
		// so paths longer than MAX_PATH are handled without a fixed buffer.
		length := call(procDragQueryFileW, hDrop, uintptr(i), 0, 0)
		if length == 0 {
			continue
		}
		buf := make([]uint16, int(length)+1)
		call(procDragQueryFileW, hDrop, uintptr(i), ptr(&buf[0]), uintptr(len(buf)))
		files = append(files, syscall.UTF16ToString(buf))
	}
	return files
}

// launchDowngradeConsole relaunches prem-down in a brand-new console window to
// downgrade every selected file. CREATE_NEW_CONSOLE (and leaving the standard
// handles unset) gives the child a real interactive console, which the "--gui"
// pause in main() needs to wait on Enter — the activated server itself has no
// console. The existing batch path in main() then handles per-file success and
// failure.
func launchDowngradeConsole(files []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	argv := append([]string{exe, "--gui"}, files...)

	appPtr, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	cmdPtr, err := syscall.UTF16PtrFromString(makeCmdLine(argv))
	if err != nil {
		return err
	}
	var si syscall.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si)) //nolint:gosec // G103/G115: STARTUPINFO.cb is its own byte size, which fits a DWORD.
	var pi syscall.ProcessInformation
	if err := syscall.CreateProcess(appPtr, cmdPtr, nil, nil, false, createNewConsole, nil, nil, &si, &pi); err != nil {
		return err
	}
	_ = syscall.CloseHandle(pi.Thread)
	_ = syscall.CloseHandle(pi.Process)
	return nil
}

// makeCmdLine joins argv into a command line with each element quoted per the
// Windows CommandLineToArgvW rules, so paths with spaces survive intact.
func makeCmdLine(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(syscall.EscapeArg(a))
	}
	return b.String()
}

// messagePump runs the STA message loop. In a single-threaded apartment, COM
// delivers incoming interface calls (DragEnter, Drop, ...) through the message
// queue, so this pump is what actually drives our handler. It returns when
// GetMessage yields WM_QUIT (0) or an error (-1).
func messagePump() {
	var msg struct {
		hwnd     uintptr
		message  uint32
		wParam   uintptr
		lParam   uintptr
		time     uint32
		pt       struct{ x, y int32 }
		lPrivate uint32
	}
	for {
		r := call(procGetMessageW, ptr(&msg), 0, 0, 0)
		if int32(r) <= 0 { //nolint:gosec // G115: GetMessageW returns a 32-bit BOOL; 0 is WM_QUIT and -1 is an error, both end the pump.
			return
		}
		call(procTranslateMessage, ptr(&msg))
		call(procDispatchMessageW, ptr(&msg))
	}
}

// runDropTargetServer runs prem-down as the COM LocalServer for the Drop Target
// class: it registers the class factory, pumps messages until the Drop hands
// off the selection (or the safety timeout fires), then tears down.
func runDropTargetServer() {
	// COM launches this LocalServer as a console-subsystem process, so Windows
	// hands it a console window it never uses. Hide it up front so the only
	// window the user sees is the fresh console the downgrade runs in; a brief
	// flash before this line is unavoidable since the OS creates the console at
	// process start.
	if hwnd := call(procGetConsoleWindow); hwnd != 0 {
		call(procShowWindow, hwnd, swHide)
	}

	// COM apartment, the message pump, and the class object must all live on one
	// OS thread for the STA to work.
	runtime.LockOSThread()

	call(procCoInitializeEx, 0, coinitApartmentThreaded)
	defer call(procCoUninitialize)

	serverThreadID = call(procGetCurrentThreadID)

	setupCOMObjects()

	var clsid guid
	clsidStr, err := syscall.UTF16PtrFromString(dropHandlerCLSID)
	if err != nil {
		return
	}
	call(procCLSIDFromString, ptr(clsidStr), ptr(&clsid))

	var token uint32
	hr := call(procCoRegisterClassObject,
		ptr(&clsid), ptr(&factoryInstance), clsctxLocalServer, regclsSingleUse, ptr(&token))
	if failed(hr) {
		return
	}
	defer call(procCoRevokeClassObject, uintptr(token))

	timer := time.AfterFunc(serverTimeout, func() {
		call(procPostThreadMessageW, serverThreadID, wmQuit, 0, 0)
	})
	defer timer.Stop()

	messagePump()
}

// maybeRunCOMServer runs the Drop Target server and reports true when prem-down
// was activated by COM (launched with "-Embedding"); main() then returns
// without doing any normal CLI parsing.
func maybeRunCOMServer(args []string) bool {
	if !hasEmbeddingArg(args) {
		return false
	}
	runDropTargetServer()
	return true
}

// hasEmbeddingArg reports whether COM launched us for activation: it appends
// "-Embedding" (older shells use "/Embedding") to the LocalServer32 command.
func hasEmbeddingArg(args []string) bool {
	for _, a := range args {
		if strings.EqualFold(strings.TrimLeft(a, "-/"), "Embedding") {
			return true
		}
	}
	return false
}
