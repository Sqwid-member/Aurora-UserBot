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

func main() {
	var sent []string
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for in.Scan() {
		var f frame
		if json.Unmarshal(in.Bytes(), &f) != nil {
			continue
		}
		switch f.Method {
		case "plugin.hello":
			emit(map[string]any{
				"jsonrpc": "2.0", "id": f.ID,
				"result": map[string]any{"ok": true, "sdk": "stdlib"},
			})
		case "plugin.load":
			emit(map[string]any{
				"jsonrpc": "2.0", "id": f.ID,
				"result": map[string]any{"ok": true},
			})
		case "plugin.unload":
			emit(map[string]any{
				"jsonrpc": "2.0", "id": f.ID,
				"result": map[string]any{"ok": true},
			})
			os.Exit(0)
		case "event":
			var ev struct {
				Name string `json:"name"`
				Data any    `json:"data"`
			}
			_ = json.Unmarshal(f.Params, &ev)
			sent = append(sent, ev.Name)
			// Exercise a host API call so the reverse channel is covered.
			call("kv.set", map[string]any{"key": "testplugin:last", "value": ev.Name})
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
			emit(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": map[string]any{"text": text}})
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

var seq int

func call(method string, params map[string]any) {
	seq++
	emit(map[string]any{
		"jsonrpc": "2.0", "id": seq, "method": method, "params": params,
	})
}

func emit(v any) {
	buf, _ := json.Marshal(v)
	fmt.Fprintln(os.Stdout, string(buf))
}
