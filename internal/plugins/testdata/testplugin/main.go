// Command testplugin is a real, minimal Aurora plugin used by the host's
// integration tests. It speaks the production protocol over stdio and is
// deliberately stdlib-only so the test needs nothing but the Go toolchain.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	ID json.RawMessage `json:"id,omitempty"`
}

var (
	seq     int
	callTag = map[string]string{} // id -> method, so responses can be labelled
)

func main() {
	var seen []string

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for in.Scan() {
		var f frame
		if json.Unmarshal(in.Bytes(), &f) != nil {
			continue
		}

		// A response to one of our own calls.
		if f.Method == "" && f.ID != nil {
			report(string(f.ID), f)
			continue
		}

		switch f.Method {
		case "plugin.hello":
			emit(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": map[string]any{"ok": true}})
		case "plugin.load":
			emit(map[string]any{"id": f.ID, "result": map[string]any{"ok": true}})
		case "plugin.unload":
			emit(map[string]any{"id": f.ID, "result": map[string]any{"ok": true}})
			os.Exit(0)
		case "event":
			var ev struct {
				Name string `json:"name"`
				Data any    `json:"data"`
			}
			_ = json.Unmarshal(f.Params, &ev)
			seen = append(seen, ev.Name)
			// Exercise the reverse channel: the plugin calls host methods and
			// the test inspects what the host answered. The test manifest
			// grants tg:send/read and config but NOT net, so http.request
			// must come back forbidden.
			call("kv.set", map[string]any{"key": "testplugin:last", "value": ev.Name})
			call("http.request", map[string]any{"url": "http://example.com"})
			call("tg.get_me", nil)
		case "command":
			var p struct {
				Name string `json:"name"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(f.Params, &p)
			text := "pong"
			if strings.TrimSpace(p.Text) != "" {
				text = strings.ToUpper(p.Text)
			}
			emit(map[string]any{"id": f.ID, "result": map[string]any{"text": text}})
		default:
			if f.ID != nil {
				emit(map[string]any{
					"jsonrpc": "2.0", "id": f.ID,
					"error": map[string]any{"code": -32601, "message": "unknown method " + f.Method},
				})
			}
		}
	}
}

// report stores the outcome of a host call so the test can assert on it.
func report(id string, f frame) {
	method, ok := callTag[id]
	if !ok {
		return
	}
	delete(callTag, id)

	var value string
	switch {
	case f.Error != nil:
		value = "error:" + f.Error.Message
	case method == "http.request":
		var res struct {
			Status int `json:"status"`
		}
		if json.Unmarshal(f.Result, &res) == nil {
			value = "ok:status=" + itoa(res.Status)
		} else {
			value = "ok"
		}
	default:
		value = "ok"
	}
	call("kv.set", map[string]any{"key": "testplugin:" + method, "value": value})
}

func call(method string, params map[string]any) {
	seq++
	id := itoa(seq)
	callTag[id] = method
	emit(map[string]any{
		"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params,
	})
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func emit(v any) {
	buf, _ := json.Marshal(v)
	fmt.Fprintln(os.Stdout, string(buf))
}
