package organizer

import "fmt"

// This proof is scoped to one Undo call. The checksum establishes that the
// legacy file is unchanged; the complete stat fingerprint protects the gap
// between preflight and the exclusive rename. Nothing is inferred from size
// and mtime alone, and no old journal is silently converted to stat mode.
type legacyUndoProof struct {
	path     string
	identity Identity
}

func needsUndo(entry JournalEntry) bool {
	return entry.Status != "planned" && entry.Status != "restored" &&
		(entry.Verification == "stat" || entry.Digest != "")
}

func verifyLegacyUndo(entry JournalEntry, callbacks ...fileProgress) (legacyUndoProof, bool, error) {
	verified := make(map[string]Identity)
	for _, candidate := range []string{entry.Source, entry.Destination, entry.SourceStage, entry.UndoStage} {
		present, err := exists(candidate)
		if err != nil {
			return legacyUndoProof{}, false, err
		}
		if !present {
			continue
		}
		identity, err := snapshot(candidate)
		if err != nil {
			return legacyUndoProof{}, false, err
		}
		digest, err := hashFile(candidate, &identity, callbacks...)
		if err != nil {
			return legacyUndoProof{}, false, err
		}
		if digest != entry.Digest {
			return legacyUndoProof{}, false, fmt.Errorf("content changed; refusing to alter: %s", candidate)
		}
		if identity.ChangeTimeNS != 0 {
			if err = verifyIdentity(candidate, identity); err != nil {
				return legacyUndoProof{}, false, err
			}
		}
		verified[candidate] = identity
	}
	if len(verified) == 0 {
		return legacyUndoProof{}, false, fmt.Errorf("all recorded paths missing: %s", entry.Source)
	}
	return simpleLegacyUndo(entry, verified)
}

func simpleLegacyUndo(entry JournalEntry, verified map[string]Identity) (legacyUndoProof, bool, error) {
	if len(verified) != 1 {
		return legacyUndoProof{}, false, nil
	}
	for path, identity := range verified {
		if identity.ChangeTimeNS == 0 || (path != entry.Source && path != entry.Destination) {
			return legacyUndoProof{}, false, nil
		}
		if path == entry.Destination {
			same, err := sameFilesystem(Entry{Source: path, Destination: entry.Source, Identity: identity})
			if err != nil || !same {
				return legacyUndoProof{}, false, err
			}
		}
		return legacyUndoProof{path: path, identity: identity}, true, nil
	}
	return legacyUndoProof{}, false, nil
}

func (proof legacyUndoProof) restore(entry JournalEntry) error {
	for _, path := range []string{entry.Source, entry.Destination, entry.SourceStage, entry.UndoStage} {
		if path == proof.path {
			continue
		}
		if present, err := exists(path); err != nil {
			return err
		} else if present {
			return fmt.Errorf("recovery path appeared after verification; refusing to alter: %s", path)
		}
	}
	if proof.path == entry.Source {
		// The previous attempt (or a manual recovery) already returned the file.
		// Verify the proof before recording success, without rereading contents.
		return verifyIdentity(entry.Source, proof.identity)
	}
	_, err := fastMove(entry.Destination, entry.Source, proof.identity)
	return err
}
