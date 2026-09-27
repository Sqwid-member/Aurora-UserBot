package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// pipePair returns two Conns wired to each other through in-memory pipes,
// which is exactly the topology of a plugin process.
func pipePair(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	aIn, bIn := newDuplex(t)
	bOut, aOut := newDuplex(t)

	a := NewConn(aOut, aIn)
	b := NewConn(bOut, bIn)
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

func TestCallAndResult(t *testing.T) {
	a, b := pipePair(t)

	b.Handle("add", func(_ context.Context, raw json.RawMessage) (any, *Error) {
		var p struct{ A, B int }
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, NewError(CodeInvalidParams, "%v", err)
		}
		return map[string]any{"sum": p.A + p.B}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = b.Serve(ctx) }()

	var res struct {
		Sum int `json:"sum"`
	}
	if err := a.Call(ctx, "add", map[string]int{"a": 2, "b": 40}, &res); err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Sum != 42 {
		t.Fatalf("sum = %d, want 42", res.Sum)
	}
}

func TestUnknownMethodReturnsError(t *testing.T) {
	a, b := pipePair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = b.Serve(ctx) }()

	err := a.Call(ctx, "nope", nil, nil)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("code = %d, want %d", rpcErr.Code, CodeMethodNotFound)
	}
}

func TestNotifyIsFireAndForget(t *testing.T) {
	a, b := pipePair(t)

	got := make(chan string, 1)
	b.HandleFunc("ping", func(_ context.Context, raw json.RawMessage) {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &p)
		got <- p.Text
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = b.Serve(ctx) }()

	if err := a.Notify("ping", map[string]string{"text": "hello"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	select {
	case v := <-got:
		if v != "hello" {
			t.Fatalf("text = %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("notification never arrived")
	}
}

func TestConcurrentCallsDoNotInterleave(t *testing.T) {
	a, b := pipePair(t)

	b.Handle("echo", func(_ context.Context, raw json.RawMessage) (any, *Error) {
		var p struct{ N int }
		_ = json.Unmarshal(raw, &p)
		// Vary the latency so responses would arrive out of order if the
		// pending map were keyed wrongly.
		time.Sleep(time.Duration(20-p.N%5) * time.Millisecond)
		return map[string]any{"n": p.N}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = b.Serve(ctx) }()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var res struct {
				N int `json:"n"`
			}
			if err := a.Call(ctx, "echo", map[string]int{"n": n}, &res); err != nil {
				t.Errorf("call %d: %v", n, err)
				return
			}
			if res.N != n {
				t.Errorf("mismatched response: sent %d, got %d", n, res.N)
			}
		}(i)
	}
	wg.Wait()
}

func TestCallTimeout(t *testing.T) {
	a, b := pipePair(t)

	b.Handle("slow", func(ctx context.Context, _ json.RawMessage) (any, *Error) {
		time.Sleep(2 * time.Second)
		return nil, nil
	})

	sctx, cancelServe := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelServe()
	go func() { _ = b.Serve(sctx) }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := a.Call(ctx, "slow", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestMalformedFrameDoesNotKillConnection(t *testing.T) {
	clientSide, serverSide := pipePair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = serverSide.Serve(ctx) }()

	// Simulate garbage arriving on the wire.
	if _, err := clientSide.w.Write([]byte("{not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The connection must still answer valid requests.
	serverSide.Handle("ok", func(context.Context, json.RawMessage) (any, *Error) {
		return true, nil
	})
	if err := clientSide.Call(ctx, "ok", nil, nil); err != nil {
		t.Fatalf("connection died after a bad frame: %v", err)
	}
}

func TestHandlerPanicBecomesError(t *testing.T) {
	a, b := pipePair(t)

	b.Handle("boom", func(context.Context, json.RawMessage) (any, *Error) {
		panic("kaboom")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = b.Serve(ctx) }()

	err := a.Call(ctx, "boom", nil, nil)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if rpcErr.Code != CodeInternalError {
		t.Fatalf("code = %d, want %d", rpcErr.Code, CodeInternalError)
	}
}
