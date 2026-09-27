package kv

import (
	"path/filepath"
	"testing"
)

func TestSetGetDelete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Set("plugin:echo:prefix", "привіт"); err != nil {
		t.Fatal(err)
	}
	var got string
	found, err := store.Get("plugin:echo:prefix", &got)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != "привіт" {
		t.Fatalf("found=%v got=%q", found, got)
	}

	keys := store.Keys("plugin:echo:")
	if len(keys) != 1 || keys[0] != "plugin:echo:prefix" {
		t.Fatalf("keys = %v", keys)
	}

	store.Delete("plugin:echo:prefix")
	if found, _ := store.Get("plugin:echo:prefix", &got); found {
		t.Fatal("key still present after delete")
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("a", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	var n int
	found, err := reopened.Get("a", &n)
	if err != nil || !found || n != 1 {
		t.Fatalf("found=%v n=%d err=%v", found, n, err)
	}
}

func TestCorruptStoreIsQuarantinedNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	if err := writeFile(path, []byte("{{{ not json")); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("a corrupt store must not brick the bot: %v", err)
	}
	defer func() { _ = store.Close() }()
	if store.Len() != 0 {
		t.Fatalf("expected an empty store, got %d keys", store.Len())
	}
}

func TestValueSizeIsCapped(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	huge := make([]byte, MaxValueSize+1)
	if err := store.SetRaw("big", huge); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}
