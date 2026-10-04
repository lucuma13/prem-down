// Tells File Explorer about each output prem-down writes.
//
// An open Explorer window normally notices new files by watching its folder,
// but that watch does not work for folders whose path exceeds MAX_PATH (260): a
// downgraded project written there stays invisible until the user refreshes the
// window. SHChangeNotify reaches the shell directly instead, and does update
// such a window. It is sent for every output because it is harmless where the
// watch already works.

package integrate

import (
	"runtime"
	"syscall"
)

var procSHChangeNotify = modshell32.NewProc("SHChangeNotify")

const (
	shcneCreate = 0x00000002 // SHCNE_CREATE
	shcneMkdir  = 0x00000008 // SHCNE_MKDIR
	shcnfPathW  = 0x0005     // SHCNF_PATHW: the item is a UTF-16 path

	// SHCNF_FLUSHNOWAIT starts delivery before returning, so the notification
	// is not lost when prem-down exits straight afterwards, without blocking on
	// a busy Explorer the way SHCNF_FLUSH would.
	shcnfFlushNoWait = 0x3000
)

// NotifyCreated tells File Explorer that path, a file or (dir) a folder, was
// just created, so an open window showing its parent lists it without F5.
func NotifyCreated(path string, dir bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	event := uintptr(shcneCreate)
	if dir {
		event = shcneMkdir
	}
	call(procSHChangeNotify, event, shcnfPathW|shcnfFlushNoWait, ptr(p), 0)
	runtime.KeepAlive(p)
}
