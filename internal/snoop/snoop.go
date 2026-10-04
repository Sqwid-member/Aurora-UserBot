// Package snoop implements the deleted/edited message watcher.
//
// Every incoming message is recorded (text + who + where + when) in the
// shared KV store. When Telegram reports a deletion or an edit, the
// watcher looks the original up and produces a Catch describing what
// vanished or changed. Delivery (e.g. forwarding the catch to Saved
// Messages) is the caller's job via OnCatch.
//
// Two Telegram quirks shape the design:
//
//  1. UpdateDeleteMessages for private chats and basic groups carries no
//     peer — only bare message IDs, which are merely per-dialog unique.
//     So a delete is attributed by scanning for records with that ID:
//     exactly one hit wins, several hits are all reported as candidates.
//  2. The update path must never block: all KV access here is local and
//     bounded, and OnCatch is invoked synchronously so the caller decides
//     whether to fan out to a goroutine.
package snoop

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/kv"
)

// Limits bound memory and disk usage.
const (
	// MaxMessages caps stored messages; older ones are pruned.
	MaxMessages = 2000
	// MaxCatches caps the recent-catch log read by `aurora snoop`.
	MaxCatches = 200
	// KeepDays drops messages older than this during pruning.
	KeepDays = 7
	// CtxTTL bounds how far back an edit/delete may reference a message
	// we never recorded (e.g. sent before the last restart).
	CtxTTL = 30 * 24 * time.Hour
)

const (
	msgPrefix   = "snoop:msg:"
	catchPrefix = "snoop:caught:"
)

// Kind describes what happened to a message.
type Kind string

const (
	KindDeleted Kind = "deleted"
	KindEdited  Kind = "edited"
)

// Record is one remembered message.
type Record struct {
	Peer  string `json:"peer"` // "user:123", "chat:456", "channel:789"
	ID    int    `json:"id"`
	Title string `json:"title,omitempty"`
	From  string `json:"from,omitempty"`
	Text  string `json:"text,omitempty"`
	Date  int64  `json:"date,omitempty"`
}

// Catch is a detected deletion or edit, ready to render.
type Catch struct {
	Kind  Kind   `json:"kind"`
	Peer  string `json:"peer"`
	Title string `json:"title,omitempty"`
	From  string `json:"from,omitempty"`
	Old   string `json:"old,omitempty"`
	New   string `json:"new,omitempty"`
	Date  int64  `json:"date,omitempty"`
	At    int64  `json:"at"`
}

// Text renders the catch the way it is posted to the owner.
func (c Catch) Text() string {
	title := c.Title
	if title == "" {
		title = c.Peer
	}
	who := c.From
	if who == "" {
		who = "невідомий відправник"
	}
	old := c.Old
	if old == "" {
		old = "[без тексту]"
	}
	switch c.Kind {
	case KindEdited:
		current := c.New
		if current == "" {
			current = "[без тексту]"
		}
		return fmt.Sprintf("✏️ Змінено в %s:\n— %s\n— було: %s\n— стало: %s",
			title, who, old, current)
	default:
		return fmt.Sprintf("🗑 Видалено в %s:\n— %s\n%s", title, who, old)
	}
}

// Options tunes a Watcher.
type Options struct {
	MaxMessages int
	KeepDays    int
	// Now is substituted in tests.
	Now func() time.Time
}

// Watcher records messages and matches deletions/edits against them.
type Watcher struct {
	kv   *kv.Store
	max  int
	keep time.Duration
	now  func() time.Time

	writes uint64

	// OnCatch fires for every detected deletion/edit, synchronously.
	OnCatch func(Catch)
}

// New wires a Watcher over store. A nil store disables persistence
// (memory-only operation is not supported — pass a real store).
func New(store *kv.Store, opts Options) *Watcher {
	max := opts.MaxMessages
	if max <= 0 {
		max = MaxMessages
	}
	keepDays := opts.KeepDays
	if keepDays <= 0 {
		keepDays = KeepDays
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Watcher{kv: store, max: max, keep: time.Duration(keepDays) * 24 * time.Hour, now: now}
}

func msgKey(peer string, id int) string {
	return fmt.Sprintf("%s%s:%d", msgPrefix, peer, id)
}

// NoteNew remembers an incoming message. Own outgoing messages must be
// filtered by the caller (they would otherwise report the owner's own
// deletions, e.g. chat-command trigger cleanup).
func (w *Watcher) NoteNew(peer string, rec Record) {
	if w == nil || w.kv == nil || rec.ID == 0 {
		return
	}
	rec.Peer = peer
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = w.kv.SetRaw(msgKey(peer, rec.ID), raw)
	if atomic.AddUint64(&w.writes, 1)%64 == 0 {
		w.prune()
	}
}

// NoteEdit compares the edited message against the remembered original.
// It returns nil when nothing was recorded or the text is identical.
func (w *Watcher) NoteEdit(peer string, id int, newText, title, from string, date int64) *Catch {
	if w == nil || w.kv == nil || id == 0 {
		return nil
	}
	raw, ok := w.kv.GetRaw(msgKey(peer, id))
	if !ok {
		return nil
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil
	}
	oldText := rec.Text
	if oldText == newText {
		return nil
	}
	if title == "" {
		title = rec.Title
	}
	if from == "" {
		from = rec.From
	}
	// Refresh the stored copy so a second edit diffs against the latest.
	rec.Text = newText
	rec.Title = title
	rec.From = from
	if next, err := json.Marshal(rec); err == nil {
		_ = w.kv.SetRaw(msgKey(peer, id), next)
	}
	c := Catch{Kind: KindEdited, Peer: peer, Title: title, From: from,
		Old: oldText, New: newText, Date: rec.Date, At: w.now().Unix()}
	w.remember(c)
	return &c
}

// Refresh silently updates the stored copy (used when edit notifications
// are off: the next edit must still diff against the latest text, but no
// catch is produced and OnCatch stays quiet).
func (w *Watcher) Refresh(peer string, id int, newText, title, from string) {
	if w == nil || w.kv == nil || id == 0 {
		return
	}
	raw, ok := w.kv.GetRaw(msgKey(peer, id))
	if !ok {
		return
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return
	}
	if rec.Text == newText {
		return
	}
	rec.Text = newText
	if title != "" {
		rec.Title = title
	}
	if from != "" {
		rec.From = from
	}
	if next, err := json.Marshal(rec); err == nil {
		_ = w.kv.SetRaw(msgKey(peer, id), next)
	}
}

// NoteDelete resolves bare message IDs against remembered records.
// For channels the peer is known and attribution is exact; otherwise
// every record sharing the ID is reported as a candidate.
func (w *Watcher) NoteDelete(peer string, channel bool, ids []int) []Catch {
	if w == nil || w.kv == nil || len(ids) == 0 {
		return nil
	}
	var out []Catch
	now := w.now()
	for _, id := range ids {
		if id == 0 {
			continue
		}
		var recs []Record
		if channel {
			if raw, ok := w.kv.GetRaw(msgKey(peer, id)); ok {
				var rec Record
				if err := json.Unmarshal(raw, &rec); err == nil {
					recs = append(recs, rec)
				}
			}
		} else {
			recs = w.byID(id)
		}
		for _, rec := range recs {
			if now.Sub(time.Unix(rec.Date, 0)) > CtxTTL && rec.Date > 0 {
				continue
			}
			c := Catch{Kind: KindDeleted, Peer: rec.Peer, Title: rec.Title,
				From: rec.From, Old: rec.Text, Date: rec.Date, At: now.Unix()}
			w.remember(c)
			out = append(out, c)
			// Consume the record: a message can only be deleted once, and
			// this keeps ambiguous IDs from matching forever.
			w.kv.Delete(msgKey(rec.Peer, rec.ID))
		}
	}
	return out
}

// byID finds every remembered record sharing a bare message ID
// (IDs are only per-dialog unique outside channels).
func (w *Watcher) byID(id int) []Record {
	suffix := fmt.Sprintf(":%d", id)
	var out []Record
	for _, k := range w.kv.Keys(msgPrefix) {
		if !strings.HasSuffix(k, suffix) {
			continue
		}
		raw, ok := w.kv.GetRaw(k)
		if !ok {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err == nil && rec.ID == id {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// remember persists a catch to the recent-catch log, capped.
func (w *Watcher) remember(c Catch) {
	if w.OnCatch != nil {
		w.OnCatch(c)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	key := fmt.Sprintf("%s%019d:%06d", catchPrefix, w.now().UnixNano(),
		atomic.AddUint64(&w.writes, 1)%1000000)
	_ = w.kv.SetRaw(key, raw)
	keys := w.kv.Keys(catchPrefix)
	if len(keys) > MaxCatches {
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-MaxCatches] {
			w.kv.Delete(k)
		}
	}
}

// prune drops messages older than KeepDays and anything beyond MaxMessages.
func (w *Watcher) prune() {
	now := w.now()
	type entry struct {
		key  string
		date int64
	}
	var entries []entry
	for _, k := range w.kv.Keys(msgPrefix) {
		raw, ok := w.kv.GetRaw(k)
		if !ok {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			w.kv.Delete(k)
			continue
		}
		if rec.Date > 0 && now.Sub(time.Unix(rec.Date, 0)) > w.keep {
			w.kv.Delete(k)
			continue
		}
		entries = append(entries, entry{k, rec.Date})
	}
	if len(entries) <= w.max {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].date < entries[j].date })
	for _, e := range entries[:len(entries)-w.max] {
		w.kv.Delete(e.key)
	}
}

// Recent returns the newest catches, newest first, for `aurora snoop`.
func Recent(store *kv.Store, n int) []Catch {
	if store == nil {
		return nil
	}
	if n <= 0 {
		n = 20
	}
	keys := store.Keys(catchPrefix)
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	if len(keys) > n {
		keys = keys[:n]
	}
	out := make([]Catch, 0, len(keys))
	for _, k := range keys {
		raw, ok := store.GetRaw(k)
		if !ok {
			continue
		}
		var c Catch
		if err := json.Unmarshal(raw, &c); err == nil {
			out = append(out, c)
		}
	}
	return out
}
