//go:build !darwin && !(linux && amd64)

package organizer

import "fmt"

func renameExclusive(source, destination string) error {
	return fmt.Errorf("exclusive moves require Synology linux/amd64 or macOS")
}
func directorySyncUnsupported(err error) error { return err }
