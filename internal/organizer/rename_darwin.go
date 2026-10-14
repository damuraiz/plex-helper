//go:build darwin

package organizer

import (
	"syscall"
	"unsafe"
)

// Apple SDK: SYS_renameatx_np=488; RENAME_EXCL=4; AT_FDCWD=-2.
func renameExclusive(source, destination string) error {
	from, err := syscall.BytePtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.BytePtrFromString(destination)
	if err != nil {
		return err
	}
	atFDCWD := int64(-2)
	_, _, errno := syscall.Syscall6(488, uintptr(atFDCWD), uintptr(unsafe.Pointer(from)), uintptr(atFDCWD), uintptr(unsafe.Pointer(to)), 4, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
func directorySyncUnsupported(err error) error { return nil }
func changeTime(stat *syscall.Stat_t) int64    { return stat.Ctimespec.Sec*1e9 + stat.Ctimespec.Nsec }
