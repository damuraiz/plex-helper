package organizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type event struct {
	Version  int       `json:"version,omitempty"`
	Event    string    `json:"event"`
	Journal  *Journal  `json:"journal,omitempty"`
	Index    int       `json:"index,omitempty"`
	Digest   string    `json:"sha256,omitempty"`
	Error    string    `json:"error,omitempty"`
	Identity *Identity `json:"identity,omitempty"`
}

func appendEvent(f *os.File, record event) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}
func (e *Engine) journalPath(id string) (string, error) {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return "", fmt.Errorf("invalid journal ID")
	}
	return filepath.Join(e.dataDir, id+".jsonl"), nil
}
func stages(journalID string, index int, entry Entry) (string, string) {
	base := fmt.Sprintf(".plex-helper-%s-%d-", journalID, index)
	return filepath.Join(filepath.Dir(entry.Source), base+"source"), filepath.Join(filepath.Dir(entry.Destination), base+"undo")
}

func (e *Engine) Apply(plan Plan, confirmed []string) (Journal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	selected := map[string]bool{}
	valid := map[string]bool{}
	for _, entry := range plan.Entries {
		if entry.Role == "video" {
			valid[entry.ID] = true
		}
	}
	for _, id := range confirmed {
		if !valid[id] {
			return Journal{}, fmt.Errorf("unknown confirmed video ID: %s", id)
		}
		selected[id] = true
	}
	filtered := plan
	filtered.Entries = []Entry{}
	for _, entry := range plan.Entries {
		if selected[entry.MediaID] {
			filtered.Entries = append(filtered.Entries, entry)
		}
	}
	if len(filtered.Entries) == 0 {
		return Journal{}, fmt.Errorf("explicitly confirm at least one video")
	}
	if err := validatePlan(filtered); err != nil {
		return Journal{}, err
	}
	j := Journal{ID: newID(), PlanID: plan.ID, Root: plan.Root, Library: plan.Library, CreatedAt: time.Now().UTC(), Status: "applying", Entries: []JournalEntry{}}
	for index, entry := range filtered.Entries {
		sourceStage, undoStage := stages(j.ID, index, entry)
		fast, err := sameFilesystem(entry)
		if err != nil {
			return Journal{}, err
		}
		verification := ""
		if fast {
			verification = "stat"
		}
		j.Entries = append(j.Entries, JournalEntry{Entry: entry, SourceStage: sourceStage, UndoStage: undoStage, Status: "planned", Verification: verification})
	}
	e.beginProgress(j, "apply")
	defer e.endProgress()
	path, _ := e.journalPath(j.ID)
	if err := checkPath(path); err != nil {
		return j, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return j, err
	}
	defer f.Close()
	if err = appendEvent(f, event{Version: 2, Event: "header", Journal: &j}); err != nil {
		return j, err
	}
	if err = syncDir(e.dataDir); err != nil {
		return j, err
	}
	for index := range j.Entries {
		entry := &j.Entries[index]
		e.publish(j, index, "Переименование")
		if err = e.fileProgress(entry.Source, 0, 0, "Переименование"); err != nil {
			return failedJournal(f, j, index, err)
		}
		digest := ""
		if entry.Verification != "stat" {
			digest, err = hashFile(entry.Source, &entry.Identity, e.fileProgress)
			if err != nil {
				return failedJournal(f, j, index, err)
			}
		}
		entry.Digest = digest
		entry.Status = "pending"
		if err = appendEvent(f, event{Event: "pending", Index: index, Digest: digest}); err != nil {
			return j, err
		}
		if entry.Verification == "stat" {
			actual, moveErr := fastMove(entry.Source, entry.Destination, entry.Identity)
			err = moveErr
			if err == nil {
				entry.DestinationIdentity = &actual
			}
		} else {
			err = transfer(entry.Source, entry.Destination, entry.SourceStage, digest, &entry.Identity, e.fileProgress)
		}
		if err != nil {
			return failedJournal(f, j, index, err)
		}
		entry.Status = "moved"
		if err = appendEvent(f, event{Event: "moved", Index: index, Identity: entry.DestinationIdentity}); err != nil {
			return j, err
		}
		e.publish(j, index, "Переименовано")
	}
	j.Status = "complete"
	if err = appendEvent(f, event{Event: "complete"}); err != nil {
		return j, err
	}
	return j, nil
}
func failedJournal(f *os.File, j Journal, index int, cause error) (Journal, error) {
	if cause == ErrStopRequested {
		j.Status = "stopped"
		if err := appendEvent(f, event{Event: "stopped"}); err != nil {
			return j, err
		}
		return j, cause
	}
	j.Status = "needs-attention"
	j.Error = cause.Error()
	j.Entries[index].Error = cause.Error()
	if err := appendEvent(f, event{Event: "failed", Index: index, Error: cause.Error()}); err != nil {
		return j, fmt.Errorf("%v; journal write also failed: %w", cause, err)
	}
	return j, fmt.Errorf("batch stopped; journal %s: %w", j.ID, cause)
}

// readJournal accepts a torn, non-newline-terminated final record. completeEnd
// identifies the durable JSONL prefix used for backup/repair before appending.
func readJournal(path string) (Journal, int64, error) {
	var j Journal
	if err := checkPath(path); err != nil {
		return j, 0, err
	}
	if _, err := snapshot(path); err != nil {
		return j, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return j, 0, err
	}
	defer f.Close()
	// Journals are local operator artifacts. Bound malformed input before parsing.
	data, err := io.ReadAll(io.LimitReader(f, 64*1024*1024+1))
	if err != nil {
		return j, 0, err
	}
	if len(data) > 64*1024*1024 {
		return j, 0, fmt.Errorf("journal exceeds 64 MiB")
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	offset := int64(0)
	header := false
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var record event
		if err = json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
			if !bytes.HasSuffix(line, []byte{'\n'}) && header {
				j.Status = "interrupted"
				break
			}
			return j, offset, fmt.Errorf("corrupt journal: %w", err)
		}
		if !header {
			if record.Event != "header" || (record.Version != 1 && record.Version != 2) || record.Journal == nil {
				return j, offset, fmt.Errorf("invalid journal header")
			}
			j = *record.Journal
			header = true
			if err = validateJournal(j); err != nil {
				return j, offset, err
			}
		} else {
			switch record.Event {
			case "pending", "moved", "failed", "undo_pending", "restored":
				if record.Index < 0 || record.Index >= len(j.Entries) {
					return j, offset, fmt.Errorf("invalid journal index")
				}
				entry := &j.Entries[record.Index]
				switch record.Event {
				case "pending":
					if entry.Verification != "stat" && len(record.Digest) != 64 {
						return j, offset, fmt.Errorf("invalid checksum")
					}
					entry.Digest = record.Digest
					entry.Status = "pending"
					j.Status = "interrupted"
				case "moved":
					if entry.Verification == "stat" {
						if record.Identity == nil || record.Identity.ChangeTimeNS == 0 || !sameDataIdentity(*record.Identity, entry.Identity) {
							return j, offset, fmt.Errorf("invalid moved fingerprint")
						}
						entry.DestinationIdentity = record.Identity
					}
					entry.Status = "moved"
				case "failed":
					entry.Error = record.Error
					j.Error = record.Error
					j.Status = "needs-attention"
				case "undo_pending":
					entry.Status = "undo_pending"
					j.Status = "interrupted"
				case "restored":
					entry.Status = "restored"
				}
			case "complete":
				j.Status = "complete"
			case "undone":
				j.Status = "undone"
				j.Error = ""
			case "stopped":
				j.Status = "stopped"
			default:
				return j, offset, fmt.Errorf("unknown journal event")
			}
		}
		offset += int64(len(line))
	}
	if !header {
		return j, offset, fmt.Errorf("empty journal")
	}
	if strings.TrimSuffix(filepath.Base(path), ".jsonl") != j.ID {
		return j, offset, fmt.Errorf("journal ID mismatch")
	}
	return j, offset, nil
}
func validateJournal(j Journal) error {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(j.ID) || !filepath.IsAbs(j.Root) || !filepath.IsAbs(j.Library) || filepath.Clean(j.Root) != j.Root || filepath.Clean(j.Library) != j.Library {
		return fmt.Errorf("invalid journal roots or ID")
	}
	seen := map[string]bool{}
	for index, entry := range j.Entries {
		if entry.Verification != "" && entry.Verification != "stat" {
			return fmt.Errorf("unknown verification method")
		}
		if entry.Verification == "stat" && entry.Identity.ChangeTimeNS == 0 {
			return fmt.Errorf("fast entry has no change time")
		}
		if !filepath.IsAbs(entry.Source) || !filepath.IsAbs(entry.Destination) || filepath.Clean(entry.Source) != entry.Source || filepath.Clean(entry.Destination) != entry.Destination || !inside(entry.Source, j.Root) || !inside(entry.Destination, j.Library) {
			return fmt.Errorf("journal paths escape roots")
		}
		if seen[entry.Source] || seen[entry.Destination] {
			return fmt.Errorf("duplicate journal path")
		}
		seen[entry.Source] = true
		seen[entry.Destination] = true
		sourceStage, undoStage := stages(j.ID, index, entry.Entry)
		if entry.SourceStage != sourceStage || entry.UndoStage != undoStage {
			return fmt.Errorf("invalid journal staging path")
		}
	}
	return nil
}

func repairTail(path string, end int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if end < info.Size() {
		// Preserve the original torn journal before truncating its invalid tail.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		backup := path + ".partial-" + newID()
		out, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = syncDir(filepath.Dir(path)); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		if err = f.Truncate(end); err != nil {
			return err
		}
		return f.Sync()
	}
	// A complete final JSON object without a newline is accepted. Add the
	// separator so later records do not concatenate with it.
	if end > 0 {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		last := []byte{0}
		if _, err = f.ReadAt(last, end-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			if _, err = f.WriteAt([]byte{'\n'}, end); err != nil {
				return err
			}
			return f.Sync()
		}
	}
	return nil
}

func (e *Engine) Undo(journalID string) (Journal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path, err := e.journalPath(journalID)
	if err != nil {
		return Journal{}, err
	}
	j, end, err := readJournal(path)
	if err != nil {
		return j, err
	}
	if j.Status == "undone" {
		return j, nil
	}
	j.Status = "undoing"
	e.beginProgress(j, "undo")
	defer e.endProgress()
	// Verify the entire recovery set before changing any media.
	for index, entry := range j.Entries {
		e.publish(j, index, "Проверка перед отменой")
		if err = e.fileProgress(entry.Destination, 0, 0, "Проверка перед отменой"); err != nil {
			return j, err
		}
		if entry.Verification == "stat" {
			if err = checkFastRestore(entry); err != nil {
				return j, err
			}
			continue
		}
		if entry.Digest == "" || entry.Status == "planned" || entry.Status == "restored" {
			continue
		}
		found := false
		for _, candidate := range []string{entry.Source, entry.Destination, entry.SourceStage, entry.UndoStage} {
			present, err := exists(candidate)
			if err != nil {
				return j, err
			}
			if present {
				found = true
				if err = verifyDigest(candidate, entry.Digest, e.fileProgress); err != nil {
					return j, err
				}
			}
		}
		if !found {
			return j, fmt.Errorf("all recorded paths missing: %s", entry.Source)
		}
	}
	if err = repairTail(path, end); err != nil {
		return j, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return j, err
	}
	defer f.Close()
	j.Status = "undoing"
	for index := len(j.Entries) - 1; index >= 0; index-- {
		entry := &j.Entries[index]
		if entry.Verification == "stat" && entry.DestinationIdentity == nil {
			continue
		}
		if entry.Verification != "stat" && entry.Digest == "" || entry.Status == "planned" || entry.Status == "restored" {
			continue
		}
		e.publish(j, index, "Возвращаю исходное имя")
		if err = e.fileProgress(entry.Destination, 0, 0, "Возвращаю исходное имя"); err != nil {
			return failedJournal(f, j, index, err)
		}
		if err = appendEvent(f, event{Event: "undo_pending", Index: index}); err != nil {
			return j, err
		}
		entry.Status = "undo_pending"
		if entry.Verification == "stat" {
			_, err = fastMove(entry.Destination, entry.Source, *entry.DestinationIdentity)
		} else {
			err = restoreOne(*entry, e.fileProgress)
		}
		if err != nil {
			return failedJournal(f, j, index, err)
		}
		entry.Status = "restored"
		entry.Error = ""
		if err = appendEvent(f, event{Event: "restored", Index: index}); err != nil {
			return j, err
		}
		e.publish(j, index, "Возвращено")
	}
	j.Status = "undone"
	j.Error = ""
	if err = appendEvent(f, event{Event: "undone"}); err != nil {
		return j, err
	}
	return j, nil
}
