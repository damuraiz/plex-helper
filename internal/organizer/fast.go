package organizer

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func sameDataIdentity(a, b Identity) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.Size == b.Size && a.ModTimeNS == b.ModTimeNS
}
func verifyIdentity(path string, expected Identity) error {
	actual, err := snapshot(path)
	if err != nil {
		return err
	}
	if expected.ChangeTimeNS == 0 || actual != expected {
		return fmt.Errorf("content changed or file attributes changed; refusing to alter: %s", path)
	}
	return nil
}
func sameFilesystem(entry Entry) (bool, error) {
	if entry.Identity.ChangeTimeNS == 0 {
		return false, nil
	}
	for parent := filepath.Dir(entry.Destination); ; parent = filepath.Dir(parent) {
		info, err := os.Stat(parent)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return false, nil
			}
			return uint64(stat.Dev) == entry.Identity.Device, nil
		}
		if !os.IsNotExist(err) || parent == filepath.Dir(parent) {
			return false, err
		}
	}
}

// Fast moves change directory entries only. Check ctime as well as inode,
// device, length and mtime before the move; persist the new ctime for Undo.
// If a crash loses that final fingerprint, recovery refuses to guess.
func fastMove(source, destination string, expected Identity) (Identity, error) {
	for _, path := range []string{source, destination} {
		if err := checkPath(path); err != nil {
			return Identity{}, err
		}
	}
	if err := checkAliases(destination); err != nil {
		return Identity{}, err
	}
	if err := durableMkdir(filepath.Dir(destination), 0755); err != nil {
		return Identity{}, err
	}
	if err := verifyIdentity(source, expected); err != nil {
		return Identity{}, err
	}
	if err := renameExclusive(source, destination); err != nil {
		return Identity{}, err
	}
	if err := syncDir(filepath.Dir(destination)); err != nil {
		return Identity{}, err
	}
	if filepath.Dir(source) != filepath.Dir(destination) {
		if err := syncDir(filepath.Dir(source)); err != nil {
			return Identity{}, err
		}
	}
	actual, err := snapshot(destination)
	if err != nil {
		return Identity{}, err
	}
	if !sameDataIdentity(actual, expected) {
		return Identity{}, fmt.Errorf("source replaced or modified during atomic move; preserved at %s", destination)
	}
	return actual, nil
}

func checkFastRestore(entry JournalEntry) error {
	if entry.Status == "planned" || entry.Status == "restored" {
		return nil
	}
	if entry.DestinationIdentity == nil {
		// A durable pending record may precede a move that never started.
		if err := verifyIdentity(entry.Source, entry.Identity); err == nil {
			return nil
		}
		return fmt.Errorf("interrupted fast move has no verified destination fingerprint; manual recovery required: %s", entry.Destination)
	}
	if err := verifyIdentity(entry.Destination, *entry.DestinationIdentity); err != nil {
		return err
	}
	if present, err := exists(entry.Source); err != nil {
		return err
	} else if present {
		return fmt.Errorf("original path occupied; refusing to overwrite: %s", entry.Source)
	}
	return nil
}
