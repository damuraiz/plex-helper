//go:build !darwin && !(linux && amd64)

package organizer

import (
	"fmt"
	"syscall"
)

func renameExclusive(source, destination string) error {
	return fmt.Errorf("exclusive moves require Synology linux/amd64 or macOS")
}
func directorySyncUnsupported(err error) error { return err }
func changeTime(stat *syscall.Stat_t) int64    { return 0 }
