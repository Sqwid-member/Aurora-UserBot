package plugins_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurora/aurora/internal/kv"
	"github.com/aurora/aurora/internal/logx"
	"github.com/aurora/aurora/internal/plugins"
	"github.com/aurora/aurora/internal/proto"
)

// fakeServices records what the host asks the "Telegram runtime" to do.
type fakeServices struct {
	ready   bool
	sent    []proto.SendRequest
	history []proto.Message
}

func (f *fakeServices) Ready() bool { return f.ready }

func (f *fakeServices) Send(_ context.Context, req proto.SendRequest) (proto.SendResult, error) {
	f.sent = append(f.sent, req)
	return proto.SendResult{ID: 42, PeerID: 1234, Text: req.Text}, nil
}

func (f *fakeServices) GetMe(context.Context) (proto.User, error) {
	return proto.User{ID: 1, Username: "aurora", First: "Aurora"}, nil
}

func (f *fakeServices) History(context.Context, string, int) ([]proto.Message, error) {
	return f.history, nil
}

func (f *fakeServices) Resolve(context.Context, string) (proto.PeerInfo, error) {
	return proto.PeerInfo{ID: 7, Type: "user", Title: "Test"}, nil
}

func (f *fakeServices) Notify(string, string, string) {}

func (f *fakeServices) ConfigValue(key string) (any, bool) {
	if key == "runtime.log_level" {
		return "info", true
	}
	return nil, false
}

const testManifest = `{
  "name": "testplugin",
  "version": "0.9.0",
  "description": "integration test plugin",
  "language": "go",
  "runtime": {"command": "./testplugin"},
  "events": ["message.new", "core.start"],
  "commands": [{"name": "ping", "aliases": ["p"]}],
  "rpc_methods": [],
  "permissions": {"tg": ["send", "read"], "config": true}
}`

// buildTestPlugin compiles testdata/testplugin into a temp plugin directory.
func buildTestPlugin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "testplugin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aurora.plugin.json"), []byte(testManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "testplugin")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "testdata/testplugin"
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test plugin: %v\n%s", err, out)
	}
	return root
}

func newHost(t *testing.T, services plugins.Services) (*plugins.Host, *kv.Store) {
	t.Helper()
	root := buildTestPlugin(t)
	store, err := kv.Open(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	log := logx.New(logx.Options{Level: logx.LevelError, Sink: os.Stderr}, "test")
	h := plugins.New(services, root, store, log, plugins.Options{
		Version:      "test",
		StartTimeout: 30 * time.Second,
		StopGrace:    2 * time.Second,
		MemoryMB:     128,
		MaxRestarts:  1,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		h.StopAll(ctx)
		cancel()
	})
	return h, store
}

func TestPluginLifecycle(t *testing.T) {
	svc := &fakeServices{ready: true}
	h, _ := newHost(t, svc)

	manifests, broken := h.Discover()
	if len(manifests) != 1 || len(broken) != 0 {
		t.Fatalf("discovery: %d manifests, %d broken (%v)", len(manifests), len(broken), broken)
	}
	if manifests[0].Name != "testplugin" {
		t.Fatalf("unexpected plugin: %s", manifests[0].Name)
	}

	ctx := context.Background()
	if _, err := h.Install(filepath.Join(h.Root(), "testplugin")); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := h.Start(ctx, "testplugin"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if inst, _ := h.Get("testplugin"); !inst.Running() {
		t.Fatal("plugin should be running")
	}

	stats := h.Stats()
	if len(stats) != 1 || stats[0].State != "running" {
		t.Fatalf("stats = %+v", stats)
	}
	if stats[0].PID == 0 {
		t.Error("stats should expose the plugin pid")
	}

	if err := h.Stop(ctx, "testplugin"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if inst, _ := h.Get("testplugin"); inst.Running() {
		t.Fatal("plugin should be stopped")
	}
}

func TestCommandRouting(t *testing.T) {
	h, _ := newHost(t, &fakeServices{ready: true})
	ctx := context.Background()

	if _, err := h.Install(filepath.Join(h.Root(), "testplugin")); err != nil {
		t.Fatal(err)
	}
	if err := h.Start(ctx, "testplugin"); err != nil {
		t.Fatal(err)
	}

	out, err := h.Command(ctx, "ping", "hello")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if out != "HELLO" {
		t.Errorf("out = %q, want HELLO", out)
	}

	// Alias.
	if _, err := h.Command(ctx, "p", ""); err != nil {
		t.Errorf("alias: %v", err)
	}
	// Scoped form.
	if _, err := h.Command(ctx, "testplugin.ping", "x"); err != nil {
		t.Errorf("scoped: %v", err)
	}
	// Unknown.
	if _, err := h.Command(ctx, "nope", ""); err == nil {
		t.Error("unknown command must error")
	}
}

func TestEventDeliveryAndHostAPIFromPlugin(t *testing.T) {
	svc := &fakeServices{ready: true}
	h, store := newHost(t, svc)
	ctx := context.Background()

	if _, err := h.Install(filepath.Join(h.Root(), "testplugin")); err != nil {
		t.Fatal(err)
	}
	if err := h.Start(ctx, "testplugin"); err != nil {
		t.Fatal(err)
	}

	h.Emit("message.new", proto.Message{Text: "hi", PeerID: 5, ID: 9})
	h.Emit("not.subscribed", map[string]string{"ignored": "yes"})
	h.Emit("core.start", map[string]any{})

	// The plugin calls kv.set on every event it receives; wait for the round trip.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := store.GetString("testplugin:last"); ok && v == "core.start" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v, _ := store.GetString("testplugin:last"); v != "core.start" {
		t.Fatalf("the last event the plugin saw was %q, want core.start", v)
	}

	inst, _ := h.Get("testplugin")
	if got := inst.Stats(); got.Events == 0 {
		t.Error("delivered event counter did not move")
	}
}

func TestPermissionEnforcement(t *testing.T) {
	svc := &fakeServices{ready: true}
	// The test manifest grants tg:send/read and config, but not net.
	h, store := newHost(t, svc)
	ctx := context.Background()

	if _, err := h.Install(filepath.Join(h.Root(), "testplugin")); err != nil {
		t.Fatal(err)
	}
	if err := h.Start(ctx, "testplugin"); err != nil {
		t.Fatal(err)
	}

	h.Emit("core.start", map[string]any{})

	// The plugin calls http.request on every event; the host must refuse it
	// and say why. Permissions are enforced on the plugin's own call, so the
	// proof has to come from the plugin's side of the wire.
	verdict := waitForString(t, store, "testplugin:http.request")
	if !strings.Contains(verdict, "error:") || !strings.Contains(verdict, "net") {
		t.Fatalf("http.request verdict = %q, want a forbidden error naming the net permission", verdict)
	}

	// tg.get_me needs no capability, so it must succeed.
	if v := waitForString(t, store, "testplugin:tg.get_me"); !strings.HasPrefix(v, "ok") {
		t.Fatalf("tg.get_me verdict = %q, want ok", v)
	}
}

// waitForString polls the shared KV until key holds a non-empty value.
func waitForString(t *testing.T, store *kv.Store, key string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := store.GetString(key); ok && v != "" {
			return v
		}
		time.Sleep(50 * time.Millisecond)
	}
	v, _ := store.GetString(key)
	return v
}

func TestStartAllSkipsDisabled(t *testing.T) {
	svc := &fakeServices{ready: true}
	h, _ := newHost(t, svc)

	started, failed := h.StartAll(context.Background(), map[string]bool{"testplugin": true})
	if len(started) != 0 {
		t.Fatalf("started = %v, want nothing", started)
	}
	if len(failed) != 0 {
		t.Fatalf("a skipped plugin must not be reported as failed: %v", failed)
	}
}

func TestBrokenPluginDoesNotStopTheRest(t *testing.T) {
	svc := &fakeServices{ready: true}
	root := buildTestPlugin(t)
	brokenDir := filepath.Join(root, "broken")
	if err := os.MkdirAll(brokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "aurora.plugin.json"),
		[]byte(`{"name":"broken","runtime":{"command":"/etc/passwd"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := kv.Open(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	h := plugins.New(svc, root, store,
		logx.New(logx.Options{Level: logx.LevelError, Sink: os.Stderr}, "test"),
		plugins.Options{StartTimeout: 20 * time.Second, StopGrace: time.Second})

	started, failed := h.StartAll(context.Background(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.StopAll(ctx)

	if len(started) != 1 || started[0] != "testplugin" {
		t.Fatalf("started = %v, want the healthy plugin", started)
	}
	if _, ok := failed["broken"]; !ok {
		t.Fatalf("failed = %v, want the broken plugin reported", failed)
	}
}
