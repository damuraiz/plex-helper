package organizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damuraiz/plex-helper/internal/media"
)

func fixture(t *testing.T) (*Engine, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "input")
	library := filepath.Join(base, "library")
	if err = os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	return e, source, library
}
func put(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}
func content(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func moviePlan(t *testing.T, e *Engine, source, library string) Plan {
	t.Helper()
	inv, err := e.Scan(source, "auto")
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]media.Metadata{}
	for _, item := range inv.Items {
		selected[item.ID] = media.Metadata{Kind: "movie", Title: "Example", Year: 2024}
	}
	plan, err := e.Plan(inv, library, selected)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestApplyUndoPreservesVideosAndExactSidecars(t *testing.T) {
	e, source, library := fixture(t)
	original := filepath.Join(source, "Example.2024.1080p.mkv")
	put(t, original, "video bytes")
	put(t, filepath.Join(source, "Example.2024.1080p.ru.forced.srt"), "subtitle bytes")
	put(t, filepath.Join(source, "Example.2024.1080p.unrelated.srt"), "unrelated")
	plan := moviePlan(t, e, source, library)
	if len(plan.Entries) != 2 {
		t.Fatalf("want video + exact subtitle, got %+v", plan.Entries)
	}
	if _, err := e.Apply(plan, nil); err == nil {
		t.Fatal("apply without confirmation succeeded")
	}
	j, err := e.Apply(plan, []string{plan.Entries[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "complete" {
		t.Fatalf("%+v", j)
	}
	if content(t, plan.Entries[0].Destination) != "video bytes" {
		t.Fatal("content changed")
	}
	if _, err = os.Stat(original); !os.IsNotExist(err) {
		t.Fatal("source still exists")
	}
	j, err = e.Undo(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "undone" {
		t.Fatal(j.Status)
	}
	if content(t, original) != "video bytes" {
		t.Fatal("undo lost bytes")
	}
	if content(t, plan.Entries[1].Source) != "subtitle bytes" {
		t.Fatal("subtitle undo changed")
	}
	if _, err = e.Undo(j.ID); err != nil {
		t.Fatal("second undo must be idempotent:", err)
	}
	journals, err := e.Journals()
	if err != nil || len(journals) != 1 || journals[0].Status != "undone" {
		t.Fatalf("journals=%+v err=%v", journals, err)
	}
}
func TestApplyRefusesExistingDestinationAndChangedSource(t *testing.T) {
	e, source, library := fixture(t)
	file := filepath.Join(source, "Example.2024.mkv")
	put(t, file, "original")
	plan := moviePlan(t, e, source, library)
	if err := os.MkdirAll(filepath.Dir(plan.Entries[0].Destination), 0755); err != nil {
		t.Fatal(err)
	}
	put(t, plan.Entries[0].Destination, "existing library")
	if _, err := e.Apply(plan, []string{plan.Entries[0].ID}); err == nil {
		t.Fatal("existing destination overwritten")
	}
	if content(t, file) != "original" {
		t.Fatal("source altered")
	}
	if content(t, plan.Entries[0].Destination) != "existing library" {
		t.Fatal("library altered")
	}
	if err := os.Remove(plan.Entries[0].Destination); err != nil {
		t.Fatal(err)
	}
	put(t, file, "changed-size")
	if _, err := e.Apply(plan, []string{plan.Entries[0].ID}); err == nil {
		t.Fatal("changed source accepted")
	}
}
func TestExclusiveRenameNeverOverwrites(t *testing.T) {
	_, source, _ := fixture(t)
	first := filepath.Join(source, "first")
	second := filepath.Join(source, "second")
	put(t, first, "first")
	put(t, second, "second")
	if err := renameExclusive(first, second); err == nil {
		t.Fatal("exclusive rename overwrote destination")
	}
	if content(t, first) != "first" || content(t, second) != "second" {
		t.Fatal("existing data changed")
	}
}
func TestUndoUsesContentDigestNotOnlyStat(t *testing.T) {
	e, source, library := fixture(t)
	file := filepath.Join(source, "Example.2024.mkv")
	put(t, file, "original")
	plan := moviePlan(t, e, source, library)
	j, err := e.Apply(plan, []string{plan.Entries[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	dest := plan.Entries[0].Destination
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	put(t, dest, "modified")
	if err = os.Chtimes(dest, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Undo(j.ID); err == nil || !strings.Contains(err.Error(), "content changed") {
		t.Fatalf("want digest refusal, got %v", err)
	}
	if content(t, dest) != "modified" {
		t.Fatal("changed file lost")
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("unexpected restore")
	}
}
func TestPlannedAncestorAliasesAndMissingSeasons(t *testing.T) {
	e, source, library := fixture(t)
	put(t, filepath.Join(source, "one.2024.1080p.mkv"), "one")
	put(t, filepath.Join(source, "two.2024.720p.mkv"), "two")
	inv, err := e.Scan(source, "auto")
	if err != nil {
		t.Fatal(err)
	}
	selections := map[string]media.Metadata{inv.Items[0].ID: {Kind: "movie", Title: "Title", Year: 2024}, inv.Items[1].ID: {Kind: "movie", Title: "TITLE", Year: 2024}}
	if _, err = e.Plan(inv, library, selections); err == nil {
		t.Fatal("ancestor case alias accepted")
	}
	e, source, library = fixture(t)
	put(t, filepath.Join(source, "Show.S02E99.mkv"), "episode")
	inv, err = e.Scan(source, "auto")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := e.Plan(inv, library, map[string]media.Metadata{inv.Items[0].ID: {Kind: "tv", Title: "Show", Year: 2020, Seasons: map[string]map[string]string{"1": {"1": "Pilot"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 0 || len(plan.Issues) != 1 {
		t.Fatalf("missing season accepted: %+v", plan)
	}
}
func TestSymlinkAndAmbiguousSidecars(t *testing.T) {
	e, source, library := fixture(t)
	put(t, filepath.Join(source, "Example.2024.mkv"), "video")
	put(t, filepath.Join(source, "Example.2024.mp4"), "video2")
	put(t, filepath.Join(source, "Example.2024.srt"), "shared subtitle")
	if err := os.Symlink(filepath.Join(source, "Example.2024.mkv"), filepath.Join(source, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	inv, err := e.Scan(source, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Items) != 2 || len(inv.Issues) != 1 {
		t.Fatalf("scan=%+v", inv)
	}
	plan := moviePlan(t, e, source, library)
	if len(plan.Entries) != 2 {
		t.Fatal("ambiguous subtitle must stay put")
	}
	if _, err = e.Scan(filepath.Join(source, "link.mkv"), "auto"); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func interruptedJournal(t *testing.T, e *Engine, plan Plan) (Journal, string) {
	t.Helper()
	id := newID()
	entry := plan.Entries[0]
	sha, err := hashFile(entry.Source, &entry.Identity)
	if err != nil {
		t.Fatal(err)
	}
	sourceStage, undoStage := stages(id, 0, entry)
	j := Journal{ID: id, PlanID: plan.ID, Root: plan.Root, Library: plan.Library, CreatedAt: time.Now(), Status: "applying", Entries: []JournalEntry{{Entry: entry, SourceStage: sourceStage, UndoStage: undoStage, Status: "planned"}}}
	path, _ := e.journalPath(id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = appendEvent(f, event{Version: 1, Event: "header", Journal: &j}); err != nil {
		t.Fatal(err)
	}
	if err = appendEvent(f, event{Event: "pending", Index: 0, Digest: sha}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	j.Entries[0].Digest = sha
	j.Entries[0].Status = "pending"
	return j, path
}
func TestRecoveryPendingMoveWithTornTail(t *testing.T) {
	e, source, library := fixture(t)
	file := filepath.Join(source, "Example.2024.mkv")
	put(t, file, "recoverable")
	plan := moviePlan(t, e, source, library)
	j, path := interruptedJournal(t, e, plan)
	if err := transfer(j.Entries[0].Source, j.Entries[0].Destination, j.Entries[0].SourceStage, j.Entries[0].Digest, &j.Entries[0].Identity); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"event":"mov`)
	f.Close()
	if _, err = e.Undo(j.ID); err != nil {
		t.Fatal(err)
	}
	if content(t, file) != "recoverable" {
		t.Fatal("recovery lost bytes")
	}
	backups, err := filepath.Glob(path + ".partial-*")
	if err != nil || len(backups) != 1 {
		t.Fatal("torn journal was not backed up")
	}
}
func TestRecoveryCrossVolumeCopyAndCapturedOriginal(t *testing.T) {
	e, source, library := fixture(t)
	file := filepath.Join(source, "Example.2024.mkv")
	put(t, file, "cross-volume bytes")
	plan := moviePlan(t, e, source, library)
	j, _ := interruptedJournal(t, e, plan)
	entry := j.Entries[0]
	if err := durableMkdir(filepath.Dir(entry.Destination), 0755); err != nil {
		t.Fatal(err)
	}
	// Execute the staged cross-volume implementation even on a single disk.
	if err := copyAcross(entry.Source, entry.Destination, entry.SourceStage, entry.Digest, &entry.Identity); err != nil {
		t.Fatal(err)
	}
	if content(t, entry.Destination) != "cross-volume bytes" {
		t.Fatal("copy corrupted")
	}
	if _, err := e.Undo(j.ID); err != nil {
		t.Fatal(err)
	}
	if content(t, file) != "cross-volume bytes" {
		t.Fatal("undo corrupted")
	}
	// Simulate power loss after destination publication and source capture.
	e, source, library = fixture(t)
	file = filepath.Join(source, "Example.2024.mkv")
	put(t, file, "captured")
	plan = moviePlan(t, e, source, library)
	j, _ = interruptedJournal(t, e, plan)
	entry = j.Entries[0]
	if err := durableMkdir(filepath.Dir(entry.Destination), 0755); err != nil {
		t.Fatal(err)
	}
	put(t, entry.Destination, "captured")
	if err := renameExclusive(file, entry.SourceStage); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Undo(j.ID); err != nil {
		t.Fatal(err)
	}
	if content(t, file) != "captured" {
		t.Fatal("captured source lost")
	}
	if _, err := os.Stat(entry.Destination); !os.IsNotExist(err) {
		t.Fatal("duplicate destination left")
	}
}
func TestJournalRejectsTraversalAndCompleteCorruption(t *testing.T) {
	e, source, library := fixture(t)
	put(t, filepath.Join(source, "Example.2024.mkv"), "data")
	plan := moviePlan(t, e, source, library)
	j, path := interruptedJournal(t, e, plan)
	if _, err := e.Undo("../../outside"); err == nil {
		t.Fatal("journal ID traversal accepted")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("bad complete record\n")
	f.Close()
	if _, err = e.Undo(j.ID); err == nil {
		t.Fatal("complete corruption accepted")
	}
	// Header paths are validated independently from mutable media state.
	j.Entries[0].Destination = filepath.Join(library, "..", "outside")
	b, _ := json.Marshal(event{Version: 1, Event: "header", Journal: &j})
	if err = os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Undo(j.ID); err == nil {
		t.Fatal("journal escape accepted")
	}
}
