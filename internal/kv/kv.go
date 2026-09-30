// Package kv is Aurora's micro key-value store.
//
// It is a single JSON document held in memory and flushed atomically in the
// background. That is deliberately boring: on a phone the whole dataset is a
// few kilobytes, a debounced rewrite is faster than any embedded database,
// and there is no mmap/page-cache overhead to speak of.
package kv

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	flushInterval = 5 * time.Second
	// MaxValueSize guards against a runaway plugin writing a huge blob.
	MaxValueSize = 1 << 20 // 1 MiB
)

// ErrTooLarge is returned when a value exceeds MaxValueSize.
var ErrTooLarge = errors.New("kv: value too large")

// ErrNotFound is returned by Get when the key is absent.
var ErrNotFound = errors.New("kv: key not found")

// Store is a namespaced JSON key-value store persisted to one file.
type Store struct {
	path string

	flushMu sync.Mutex
	mu      sync.RWMutex
	data    map[string]json.RawMessage

	dirty   bool
	closed  bool
	flushCh chan struct{}
	done    chan struct{}
	wg      sync.WaitGroup
	err     error
}

// Open loads (or creates) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{
		path:    path,
		data:    make(map[string]json.RawMessage, 128),
		flushCh: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Fresh store; nothing to load.
	case err != nil:
		return nil, err
	default:
		var data map[string]json.RawMessage
		if err := json.Unmarshal(raw, &data); err != nil {
			// A corrupt store must not brick the bot. Keep a forensic copy.
			_ = os.Rename(path, path+".corrupt")
			data = nil
		}
		if data != nil {
			s.data = data
		}
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop()
	}()
	return s, nil
}

// loop is the background flusher. It exits when Close closes s.done, which
// Close owns exclusively — closing it here too would panic on a double close.
func (s *Store) loop() {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = s.Flush()
		case <-s.flushCh:
			_ = s.Flush()
		case <-s.done:
			return
		}
	}
}

// Close flushes and stops the background writer. It waits for the loop
// goroutine to exit first: otherwise a racing Flush could recreate files
// while the caller is removing the store directory.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.done)
	s.wg.Wait()
	return s.Flush()
}

// Set stores an arbitrary JSON-encodable value.
func (s *Store) Set(key string, value any) error {
	buf, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(buf) > MaxValueSize {
		return ErrTooLarge
	}
	return s.SetRaw(key, buf)
}

// SetRaw stores pre-encoded JSON.
func (s *Store) SetRaw(key string, raw []byte) error {
	if len(raw) > MaxValueSize {
		return ErrTooLarge
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("kv: store closed")
	}
	cp := make(json.RawMessage, len(raw))
	copy(cp, raw)
	s.data[key] = cp
	s.dirty = true
	s.mu.Unlock()
	s.wake()
	return nil
}

// Get decodes the value at key into dst. Returns false when absent.
func (s *Store) Get(key string, dst any) (bool, error) {
	raw, ok := s.GetRaw(key)
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return true, err
	}
	return true, nil
}

// GetRaw returns the raw JSON stored at key.
func (s *Store) GetRaw(key string) (json.RawMessage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, ok := s.data[key]
	if !ok {
		return nil, false
	}
	cp := make(json.RawMessage, len(raw))
	copy(cp, raw)
	return cp, true
}

// GetString is a convenience wrapper returning a string value.
func (s *Store) GetString(key string) (string, bool) {
	var v string
	ok, err := s.Get(key, &v)
	if err != nil {
		return "", false
	}
	return v, ok
}

// Delete removes a key.
func (s *Store) Delete(key string) {
	s.mu.Lock()
	if _, ok := s.data[key]; ok {
		delete(s.data, key)
		s.dirty = true
	}
	s.mu.Unlock()
	s.wake()
}

// Keys returns all keys under prefix, sorted.
func (s *Store) Keys(prefix string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, 16)
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// List returns every value under prefix.
func (s *Store) List(prefix string) map[string]json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]json.RawMessage)
	for k, v := range s.data {
		if strings.HasPrefix(k, prefix) {
			cp := make(json.RawMessage, len(v))
			copy(cp, v)
			out[k] = cp
		}
	}
	return out
}

// Len returns the number of stored keys.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// Flush writes the store to disk atomically.
func (s *Store) Flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	snapshot := make(map[string]json.RawMessage, len(s.data))
	for k, v := range s.data {
		cp := make(json.RawMessage, len(v))
		copy(cp, v)
		snapshot[k] = cp
	}
	s.dirty = false
	s.mu.Unlock()

	buf, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) wake() {
	select {
	case s.flushCh <- struct{}{}:
	default:
	}
}
