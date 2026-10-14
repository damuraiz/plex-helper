package organizer

import (
	"errors"
	"io"
	"time"
)

var ErrStopRequested = errors.New("операция остановлена по запросу пользователя")

type Progress struct {
	Active        bool   `json:"active"`
	Kind          string `json:"kind,omitempty"`
	JournalID     string `json:"journalId,omitempty"`
	Phase         string `json:"phase,omitempty"`
	FileIndex     int    `json:"fileIndex,omitempty"`
	FileCount     int    `json:"fileCount,omitempty"`
	Completed     int    `json:"completed,omitempty"`
	Path          string `json:"path,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	Total         int64  `json:"total,omitempty"`
	StopRequested bool   `json:"stopRequested,omitempty"`
}

func cloneJournal(j Journal) Journal {
	j.Entries = append([]JournalEntry(nil), j.Entries...)
	return j
}

func (e *Engine) Progress() Progress {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	return e.progress
}
func (e *Engine) RequestStop() bool {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	if !e.progress.Active {
		return false
	}
	e.stopRequested = true
	e.progress.StopRequested = true
	return true
}
func (e *Engine) beginProgress(j Journal, kind string) {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	e.stopRequested = false
	e.liveJournal = new(Journal)
	*e.liveJournal = cloneJournal(j)
	e.progress = Progress{Active: true, Kind: kind, JournalID: j.ID, FileCount: len(j.Entries), Phase: "Подготовка"}
}
func (e *Engine) endProgress() {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	e.liveJournal = nil
	e.progress = Progress{}
	e.stopRequested = false
}
func (e *Engine) publish(j Journal, index int, phase string) {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	*e.liveJournal = cloneJournal(j)
	e.progress.FileIndex = index + 1
	e.progress.Phase = phase
	e.progress.Bytes = 0
	e.progress.Total = 0
	if index >= 0 && index < len(j.Entries) {
		e.progress.Path = j.Entries[index].Source
	}
	e.progress.Completed = 0
	for _, entry := range j.Entries {
		if e.progress.Kind == "apply" && entry.Status == "moved" || e.progress.Kind == "undo" && entry.Status == "restored" {
			e.progress.Completed++
		}
	}
}

type fileProgress func(path string, bytes, total int64, phase string) error

func (e *Engine) fileProgress(path string, bytes, total int64, phase string) error {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	if e.stopRequested {
		return ErrStopRequested
	}
	e.progress.Path = path
	e.progress.Bytes = bytes
	e.progress.Total = total
	e.progress.Phase = phase
	return nil
}
func notify(callbacks []fileProgress, path string, bytes, total int64, phase string) error {
	for _, callback := range callbacks {
		if err := callback(path, bytes, total, phase); err != nil {
			return err
		}
	}
	return nil
}

// Only a small counting writer is allocated. Cancellation is checked for each
// chunk; snapshots are updated at most a few times per second.
type progressWriter struct {
	writer       io.Writer
	callbacks    []fileProgress
	path, phase  string
	count, total int64
	last         time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	if time.Since(w.last) >= 200*time.Millisecond {
		if err := notify(w.callbacks, w.path, w.count, w.total, w.phase); err != nil {
			return 0, err
		}
		w.last = time.Now()
	}
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}
