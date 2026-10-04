package snoop

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/kv"
)

func testStore(t *testing.T) *kv.Store {
	t.Helper()
	st, err := kv.Open(filepath.Join(t.TempDir(), "snoop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestNoteDeleteExact(t *testing.T) {
	st := testStore(t)
	var got []Catch
	w := New(st, Options{Now: time.Now})
	w.OnCatch = func(c Catch) { got = append(got, c) }

	w.NoteNew("user:1", Record{ID: 10, Title: "Bob", From: "Bob", Text: "secret", Date: time.Now().Unix()})
	out := w.NoteDelete("user:1", true, []int{10})
	if len(out) != 1 {
		t.Fatalf("got %d catches", len(out))
	}
	if out[0].Old != "secret" || out[0].Kind != KindDeleted {
		t.Fatalf("bad catch: %+v", out[0])
	}
	if len(got) != 1 {
		t.Fatalf("OnCatch not fired")
	}
	// Second delete finds nothing — the record was consumed.
	if out := w.NoteDelete("user:1", true, []int{10}); len(out) != 0 {
		t.Fatalf("expected no second catch, got %v", out)
	}
}

func TestNoteDeleteAmbiguous(t *testing.T) {
	st := testStore(t)
	w := New(st, Options{Now: time.Now})
	now := time.Now().Unix()
	w.NoteNew("user:1", Record{ID: 7, Title: "A", Text: "from-a", Date: now})
	w.NoteNew("user:2", Record{ID: 7, Title: "B", Text: "from-b", Date: now})
	out := w.NoteDelete("", false, []int{7})
	if len(out) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(out))
	}
}

func TestNoteEdit(t *testing.T) {
	st := testStore(t)
	var got []Catch
	w := New(st, Options{Now: time.Now})
	w.OnCatch = func(c Catch) { got = append(got, c) }

	w.NoteNew("chat:5", Record{ID: 3, Title: "Group", Text: "hello", Date: time.Now().Unix()})
	if c := w.NoteEdit("chat:5", 3, "hello", "", "", 0); c != nil {
		t.Fatal("identical text must not report")
	}
	c := w.NoteEdit("chat:5", 3, "hello world", "", "", 0)
	if c == nil || c.Old != "hello" || c.New != "hello world" {
		t.Fatalf("bad edit catch: %+v", c)
	}
	// Second edit diffs against the latest text.
	c = w.NoteEdit("chat:5", 3, "hello world!", "", "", 0)
	if c == nil || c.Old != "hello world" {
		t.Fatalf("bad second edit: %+v", c)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 catches, got %d", len(got))
	}
	// Unknown message: silence.
	if c := w.NoteEdit("chat:5", 999, "x", "", "", 0); c != nil {
		t.Fatal("unknown message must not report")
	}
}

func TestRefreshIsSilent(t *testing.T) {
	st := testStore(t)
	fired := 0
	w := New(st, Options{Now: time.Now})
	w.OnCatch = func(Catch) { fired++ }
	w.NoteNew("chat:5", Record{ID: 3, Title: "G", Text: "v1", Date: time.Now().Unix()})
	w.Refresh("chat:5", 3, "v2", "", "")
	if fired != 0 {
		t.Fatal("Refresh must not fire OnCatch")
	}
	// The next edit diffs against v2, not v1.
	c := w.NoteEdit("chat:5", 3, "v3", "", "", 0)
	if c == nil || c.Old != "v2" || fired != 1 {
		t.Fatalf("bad refresh+edit: %+v fired=%d", c, fired)
	}
}

func TestCatchText(t *testing.T) {
	c := Catch{Kind: KindDeleted, Title: "Group", From: "Bob", Old: "secret"}
	if s := c.Text(); !strings.Contains(s, "Видалено") || !strings.Contains(s, "secret") {
		t.Fatalf("bad text: %s", s)
	}
	c = Catch{Kind: KindEdited, Title: "Group", From: "Bob", Old: "a", New: "b"}
	if s := c.Text(); !strings.Contains(s, "було: a") || !strings.Contains(s, "стало: b") {
		t.Fatalf("bad text: %s", s)
	}
}

func TestPruneAndRecent(t *testing.T) {
	st := testStore(t)
	now := time.Now()
	w := New(st, Options{MaxMessages: 5, KeepDays: 30, Now: func() time.Time { return now }})
	for i := 1; i <= 8; i++ {
		w.NoteNew("user:1", Record{ID: i, Text: "x", Date: now.Unix()})
	}
	// Prune runs every 64 writes; force it via Recent-independent path:
	// emulate by shrinking through repeated cycles is slow, so call directly.
	w.prune()
	if got := len(st.Keys(msgPrefix)); got > 5 {
		t.Fatalf("expected <=5 records, got %d", got)
	}
	// Old records are pruned by age.
	w.NoteNew("user:1", Record{ID: 100, Text: "old", Date: now.Add(-31 * 24 * time.Hour).Unix()})
	w.prune()
	if _, ok := st.GetRaw(msgKey("user:1", 100)); ok {
		t.Fatal("old record survived pruning")
	}
	// Recent log caps.
	for i := 0; i < MaxCatches+10; i++ {
		w.remember(Catch{Kind: KindDeleted, Old: "x", At: now.Unix()})
	}
	if got := len(Recent(st, 10000)); got != MaxCatches {
		t.Fatalf("expected %d catches, got %d", MaxCatches, got)
	}
	if got := len(Recent(st, 3)); got != 3 {
		t.Fatalf("expected 3 catches, got %d", got)
	}
}
