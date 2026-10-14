package organizer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	err = f.Sync()
	// Directory fsync is not implemented by some developer filesystems.
	// Production DSM volumes are required to support directory sync.
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		return directorySyncUnsupported(err)
	}
	return err
}
func durableMkdir(path string, mode os.FileMode) error {
	if err := checkPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("not a directory: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err = durableMkdir(filepath.Dir(path), mode); err != nil {
		return err
	}
	if err = os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
		return err
	}
	if err = checkPath(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func hashFile(path string, expected *Identity, callbacks ...fileProgress) (string, error) {
	if err := checkPath(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	id, err := identityInfo(before)
	if err != nil {
		return "", err
	}
	if expected != nil && id != *expected {
		return "", fmt.Errorf("source changed: %s", path)
	}
	h := sha256.New()
	if err = notify(callbacks, path, 0, id.Size, "Проверка содержимого SHA256"); err != nil {
		return "", err
	}
	writer := &progressWriter{writer: h, callbacks: callbacks, path: path, total: id.Size, phase: "Проверка содержимого SHA256"}
	if _, err = io.Copy(writer, f); err != nil {
		return "", err
	}
	if err = notify(callbacks, path, id.Size, id.Size, "Синхронизация файла"); err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	afterID, err := identityInfo(after)
	if err != nil {
		return "", err
	}
	if afterID != id {
		return "", fmt.Errorf("file changed while hashing: %s", path)
	}
	current, err := snapshot(path)
	if err != nil {
		return "", err
	}
	if current != id {
		return "", fmt.Errorf("file replaced while hashing: %s", path)
	}
	// Persist data as well as namespace changes. A completed downloader may
	// have closed the file without flushing its final blocks to stable storage.
	if err = f.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func verifyDigest(path, digest string, callbacks ...fileProgress) error {
	if len(digest) != 64 {
		return fmt.Errorf("missing recovery checksum")
	}
	actual, err := hashFile(path, nil, callbacks...)
	if err != nil {
		return err
	}
	if actual != digest {
		return fmt.Errorf("content changed; refusing to alter: %s", path)
	}
	return nil
}
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// transfer preserves either the original or a durable verified destination at
// every step. A journal records holding before transfer starts. Replacement of
// the original pathname is captured in holding, never silently unlinked.
func transfer(source, destination, holding, digest string, expected *Identity, callbacks ...fileProgress) error {
	for _, path := range []string{source, destination, holding} {
		if err := checkPath(path); err != nil {
			return err
		}
	}
	if err := checkAliases(destination); err != nil {
		return err
	}
	if err := verifyDigest(source, digest, callbacks...); err != nil {
		return err
	}
	if expected != nil {
		current, err := snapshot(source)
		if err != nil {
			return err
		}
		if current != *expected {
			return fmt.Errorf("source replaced: %s", source)
		}
	}
	if err := durableMkdir(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	if err := checkPath(destination); err != nil {
		return err
	}
	err := renameExclusive(source, destination)
	if err == nil {
		if err = syncDir(filepath.Dir(destination)); err != nil {
			return err
		}
		if filepath.Dir(destination) != filepath.Dir(source) {
			if err = syncDir(filepath.Dir(source)); err != nil {
				return err
			}
		}
		if expected != nil {
			current, checkErr := snapshot(destination)
			if checkErr != nil {
				return checkErr
			}
			if !sameDataIdentity(current, *expected) {
				return fmt.Errorf("source replaced during atomic move; preserved at %s", destination)
			}
		}
		return verifyDigest(destination, digest, callbacks...)
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyAcross(source, destination, holding, digest, expected, callbacks...)
}
func copyAcross(source, destination, holding, digest string, expected *Identity, callbacks ...fileProgress) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	id, err := identityInfo(info)
	if err != nil {
		return err
	}
	if expected != nil && id != *expected {
		return fmt.Errorf("source changed before copy")
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".plex-helper-copy-*")
	if err != nil {
		return err
	}
	temporary := tmp.Name()
	defer func() { tmp.Close(); os.Remove(temporary) }()
	h := sha256.New()
	if err = notify(callbacks, source, 0, id.Size, "Копирование между томами"); err != nil {
		return err
	}
	writer := &progressWriter{writer: io.MultiWriter(tmp, h), callbacks: callbacks, path: source, total: id.Size, phase: "Копирование между томами"}
	if _, err = io.Copy(writer, input); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return fmt.Errorf("source changed while copying")
	}
	if err = tmp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = verifyDigest(temporary, digest, callbacks...); err != nil {
		return err
	}
	if err = checkPath(destination); err != nil {
		return err
	}
	if err = checkAliases(destination); err != nil {
		return err
	}
	if err = renameExclusive(temporary, destination); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(destination)); err != nil {
		return err
	}
	// Holding is on the source filesystem. An atomic capture prevents deleting
	// a newly replaced source pathname after validation.
	if err = renameExclusive(source, holding); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(source)); err != nil {
		return err
	}
	current, err := snapshot(holding)
	if err != nil {
		return err
	}
	if !sameDataIdentity(current, id) {
		return fmt.Errorf("source replaced during copy; preserved at %s", holding)
	}
	if err = verifyDigest(holding, digest, callbacks...); err != nil {
		return err
	}
	if err = os.Remove(holding); err != nil {
		return err
	}
	return syncDir(filepath.Dir(source))
}

// restoreOne reconciles every recorded crash window. Existing originals are
// kept; verified duplicate destinations are removed only after a source exists.
func restoreOne(entry JournalEntry, callbacks ...fileProgress) error {
	for _, path := range []string{entry.Source, entry.Destination, entry.SourceStage, entry.UndoStage} {
		if path != "" {
			if err := checkPath(path); err != nil {
				return err
			}
		}
	}
	source, err := exists(entry.Source)
	if err != nil {
		return err
	}
	dest, err := exists(entry.Destination)
	if err != nil {
		return err
	}
	holding := false
	if entry.SourceStage != "" {
		holding, err = exists(entry.SourceStage)
		if err != nil {
			return err
		}
	}
	undoHolding := false
	if entry.UndoStage != "" {
		undoHolding, err = exists(entry.UndoStage)
		if err != nil {
			return err
		}
	}
	if source {
		if err = verifyDigest(entry.Source, entry.Digest, callbacks...); err != nil {
			return err
		}
	}
	if dest {
		if err = verifyDigest(entry.Destination, entry.Digest, callbacks...); err != nil {
			return err
		}
	}
	if holding {
		if err = verifyDigest(entry.SourceStage, entry.Digest, callbacks...); err != nil {
			return err
		}
	}
	if undoHolding {
		if err = verifyDigest(entry.UndoStage, entry.Digest, callbacks...); err != nil {
			return err
		}
	}
	if !source {
		switch {
		case holding:
			if err = durableMkdir(filepath.Dir(entry.Source), 0755); err != nil {
				return err
			}
			if err = renameExclusive(entry.SourceStage, entry.Source); err != nil {
				return err
			}
			if err = syncDir(filepath.Dir(entry.Source)); err != nil {
				return err
			}
			source = true
			holding = false
		case dest:
			if err = transfer(entry.Destination, entry.Source, entry.UndoStage, entry.Digest, nil, callbacks...); err != nil {
				return err
			}
			source = true
			dest = false
		case undoHolding:
			// Return the captured library file to its recorded destination first;
			// every subsequent crash path remains represented in the journal.
			if err = renameExclusive(entry.UndoStage, entry.Destination); err != nil {
				return err
			}
			if err = syncDir(filepath.Dir(entry.Destination)); err != nil {
				return err
			}
			if err = transfer(entry.Destination, entry.Source, entry.UndoStage, entry.Digest, nil, callbacks...); err != nil {
				return err
			}
			source = true
			undoHolding = false
		default:
			return fmt.Errorf("both source and destination are missing: %s", entry.Source)
		}
	}
	if source {
		for _, path := range []string{entry.Destination, entry.SourceStage, entry.UndoStage} {
			if path == "" {
				continue
			}
			present, err := exists(path)
			if err != nil {
				return err
			}
			if !present {
				continue
			}
			if err = verifyDigest(path, entry.Digest, callbacks...); err != nil {
				return err
			}
			if path == entry.Destination {
				// Capture the pathname atomically before deleting a duplicate.
				// A concurrent replacement is preserved for manual recovery.
				if err = renameExclusive(path, entry.UndoStage); err != nil {
					return err
				}
				if err = syncDir(filepath.Dir(path)); err != nil {
					return err
				}
				path = entry.UndoStage
				if err = verifyDigest(path, entry.Digest, callbacks...); err != nil {
					return err
				}
			}
			if err = os.Remove(path); err != nil {
				return err
			}
			if err = syncDir(filepath.Dir(path)); err != nil {
				return err
			}
		}
	}
	return nil
}
