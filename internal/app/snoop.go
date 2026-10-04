package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/snoop"
)

// snoopWatcher is built lazily on first use: the KV store must be open,
// which is only guaranteed once the core is running.
func (a *App) snoopWatcher() *snoop.Watcher {
	a.snoopMu.Lock()
	defer a.snoopMu.Unlock()
	if a.snoopW == nil {
		cfg := a.Cfg.Get().Snoop
		a.snoopW = snoop.New(a.KV, snoop.Options{
			MaxMessages: cfg.MaxMessages,
			KeepDays:    cfg.KeepDays,
		})
		a.snoopW.OnCatch = func(c snoop.Catch) { a.snoopNotify(c) }
	}
	return a.snoopW
}

// snoopObserve feeds one event into the deleted/edited watcher.
// It runs on the update path: local KV only, never blocks on I/O.
func (a *App) snoopObserve(accID, name string, data any) {
	cfg := a.Cfg.Get().Snoop
	if !cfg.Enabled {
		return
	}
	w := a.snoopWatcher()
	switch name {
	case proto.EventMessageNew:
		m, ok := data.(proto.Message)
		if !ok || m.Out || m.ID == 0 {
			return
		}
		w.NoteNew(snoopPeerKey(m.PeerType, m.PeerID), snoop.Record{
			ID:    m.ID,
			Title: m.PeerTitle,
			From:  senderName(m),
			Text:  m.Text,
			Date:  m.Date,
		})
	case proto.EventMessageEdit:
		m, ok := data.(proto.Message)
		if !ok || m.Out || m.ID == 0 {
			return
		}
		if !cfg.NotifyEdit {
			// Still refresh the stored copy so a later edit diffs right,
			// but stay silent: no catch, no notification.
			w.Refresh(snoopPeerKey(m.PeerType, m.PeerID), m.ID, m.Text, m.PeerTitle, senderName(m))
			return
		}
		w.NoteEdit(snoopPeerKey(m.PeerType, m.PeerID), m.ID, m.Text, m.PeerTitle, senderName(m), m.Date)
	case proto.EventMessageDelete:
		if !cfg.NotifyDelete {
			return
		}
		peer, channel, ids := snoopDeleteTarget(data)
		if len(ids) == 0 {
			return
		}
		w.NoteDelete(peer, channel, ids)
	}
}

// snoopNotify posts one catch to the configured target, off the update path.
func (a *App) snoopNotify(c snoop.Catch) {
	target := a.Cfg.Get().Snoop.Target
	if target == "" {
		target = "me"
	}
	accID := a.Cfg.Get().ActiveAccount
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := a.Send(ctx, proto.SendRequest{
			AccountID: accID,
			Peer:      target,
			Text:      c.Text(),
			Silent:    true,
		}); err != nil {
			a.Log.Warn("snoop notify failed", logx.F("error", err.Error()))
		}
	}()
}

// snoopPeerKey renders a storage key for a dialog.
func snoopPeerKey(peerType string, peerID int64) string {
	if peerType == "" {
		peerType = "user"
	}
	return fmt.Sprintf("%s:%d", peerType, peerID)
}

// senderName renders "Name (@user)"-ish attribution from a message.
func senderName(m proto.Message) string {
	return m.FromName
}

// snoopDeleteTarget unpacks the message.deleted payload the tgc layer emits:
// {"peer_id":…, "message_ids":[…], "channel":…}. Numbers arrive as float64
// through the generic event bus.
func snoopDeleteTarget(data any) (peer string, channel bool, ids []int) {
	m, ok := data.(map[string]any)
	if !ok {
		return "", false, nil
	}
	if ch, _ := m["channel"].(bool); ch {
		channel = true
		if id, _ := m["peer_id"].(float64); id != 0 {
			peer = fmt.Sprintf("channel:%d", int64(id))
		}
	}
	for _, v := range toIntSlice(m["message_ids"]) {
		if v != 0 {
			ids = append(ids, v)
		}
	}
	return peer, channel, ids
}

func toIntSlice(v any) []int {
	switch t := v.(type) {
	case []int:
		return t
	case []any:
		out := make([]int, 0, len(t))
		for _, e := range t {
			switch n := e.(type) {
			case float64:
				out = append(out, int(n))
			case int:
				out = append(out, n)
			case int64:
				out = append(out, int(n))
			}
		}
		return out
	default:
		return nil
	}
}
