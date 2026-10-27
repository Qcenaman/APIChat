package guard

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLastInput    = user32.NewProc("GetLastInputInfo")
	procGetTickCount = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// idleFor returns how long since the last mouse or keyboard input, or -1 if unknown.
func idleFor() time.Duration {
	var li lastInputInfo
	li.cbSize = uint32(unsafe.Sizeof(li))
	r, _, _ := procLastInput.Call(uintptr(unsafe.Pointer(&li)))
	if r == 0 {
		return -1
	}
	now, _, _ := procGetTickCount.Call()
	d := uint32(now) - li.dwTime
	return time.Duration(d) * time.Millisecond
}
