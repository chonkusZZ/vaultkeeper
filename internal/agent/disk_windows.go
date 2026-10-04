//go:build windows

package agent

import (
	"syscall"
	"unsafe"
)

func diskUsage(path string) (total, free uint64) {
	k := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	var avail, tot, totFree uint64
	r, _, _ := k.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&tot)), uintptr(unsafe.Pointer(&totFree)))
	if r == 0 {
		return 0, 0
	}
	return tot, avail
}
