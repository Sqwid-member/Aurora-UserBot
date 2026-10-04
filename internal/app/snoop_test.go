package app

import (
	"path/filepath"
	"testing"

	"github.com/Sqwid-member/Aurora-UserBot/internal/config"
	"github.com/Sqwid-member/Aurora-UserBot/internal/kv"
	"github.com/Sqwid-member/Aurora-UserBot/internal/logx"
	"github.com/Sqwid-member/Aurora-UserBot/internal/proto"
	"github.com/Sqwid-member/Aurora-UserBot/internal/snoop"
)

func testSnoopApp(t *testing.T, mutate func(*config.Config)) *App {
	t.Helper()
	dir := t.TempDir()
	cs, err := config.Open(filepath.Join(dir, "config.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		if err := cs.Update(func(c *config.Config) { mutate(c) }); err != nil {
			t.Fatal(err)
		}
	}
	ks, err := kv.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ks.Close() })
	return &App{
		Cfg: cs,
		KV:  ks,
		Log: logx.New(logx.Options{}, "test"),
	}
}

func TestSnoopObserveRecords(t *testing.T) {
	a := testSnoopApp(t, func(c *config.Config) {
		c.Snoop.Enabled = true
		c.Snoop.NotifyDelete = false
		c.Snoop.NotifyEdit = false
	})
	m := proto.Message{ID: 11, PeerID: 5, PeerType: "user", PeerTitle: "Bob",
		FromName: "Bob", Text: "hello", Date: 1700000000}
	a.snoopObserve("acc1", proto.EventMessageNew, m)
	// Own outgoing messages are never recorded.
	m.Out = true
	a.snoopObserve("acc1", proto.EventMessageNew, m)
	keys := a.KV.Keys("snoop:msg:")
	if len(keys) != 1 {
		t.Fatalf("expected 1 record, got %v", keys)
	}
}

func TestSnoopObserveDisabled(t *testing.T) {
	a := testSnoopApp(t, func(c *config.Config) {
		c.Snoop.Enabled = false
	})
	a.snoopObserve("acc1", proto.EventMessageNew,
		proto.Message{ID: 11, PeerID: 5, PeerType: "user", Text: "hello"})
	if got := a.KV.Keys("snoop:msg:"); len(got) != 0 {
		t.Fatalf("disabled watcher must not record: %v", got)
	}
}

func TestSnoopEditRefreshSilent(t *testing.T) {
	a := testSnoopApp(t, func(c *config.Config) {
		c.Snoop.Enabled = true
		c.Snoop.NotifyDelete = false
		c.Snoop.NotifyEdit = false
	})
	a.snoopObserve("acc1", proto.EventMessageNew,
		proto.Message{ID: 11, PeerID: 5, PeerType: "user", Text: "v1", Date: 1700000000})
	a.snoopObserve("acc1", proto.EventMessageEdit,
		proto.Message{ID: 11, PeerID: 5, PeerType: "user", Text: "v2", Date: 1700000001})
	// Silent refresh: no catch persisted, stored copy updated.
	if got := snoop.Recent(a.KV, 10); len(got) != 0 {
		t.Fatalf("expected no catches, got %v", got)
	}
}
