//go:build linux && amd64

package organizer

import (
	"fmt"
	"syscall"
	"unsafe"
)

func renameExclusive(source, destination string) error {
	from, err := syscall.BytePtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.BytePtrFromString(destination)
	if err != nil {
		return err
	}
	atFDCWD := int64(-100)
	_, _, errno := syscall.Syscall6(316, uintptr(atFDCWD), uintptr(unsafe.Pointer(from)), uintptr(atFDCWD), uintptr(unsafe.Pointer(to)), 1, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
func directorySyncUnsupported(err error) error {
	return fmt.Errorf("filesystem does not support durable directory sync: %w", err)
}
func changeTime(stat *syscall.Stat_t) int64 { return stat.Ctim.Sec*1e9 + stat.Ctim.Nsec }
