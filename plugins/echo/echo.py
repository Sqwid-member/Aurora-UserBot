#!/usr/bin/env python3
"""Aurora echo plugin — a complete, dependency-free example.

Aurora talks to plugins over newline-delimited JSON-RPC on stdin/stdout.
Logs go to stderr; the host picks them up and routes them into the Aurora
log with this plugin's scope. That is the whole contract.
"""

import json
import sys
import threading

# JSON-RPC 2.0 error codes Aurora returns.
E_FORBIDDEN = -32001
E_UNAVAILABLE = -32002

_stdin_lock = threading.Lock()
_plugin_name = "echo"


def log(level: str, message: str) -> None:
    """Write to stderr — never stdout, which carries the protocol."""
    print(f"[{level.upper()}] {message}", file=sys.stderr, flush=True)


def send(obj: dict) -> None:
    with _stdin_lock:
        sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
        sys.stdout.flush()


def call(method: str, params: dict | None = None, _id: int = 0) -> None:
    send({"jsonrpc": "2.0", "id": _id, "method": method, "params": params or {}})


def respond(req: dict, result=None, error: str | None = None) -> None:
    frame = {"jsonrpc": "2.0", "id": req.get("id")}
    if error:
        frame["error"] = {"code": E_FORBIDDEN, "message": error}
    else:
        frame["result"] = result
    send(frame)


def on_message(payload: dict) -> None:
    text = (payload.get("text") or "").strip()
    if not text or payload.get("out"):
        return
    if not text.lower().startswith("/echo"):
        return

    body = text[5:].strip()
    if not body:
        body = "бек"

    peer_type = payload.get("peer_type", "user")
    peer_id = payload.get("peer_id", 0)
    peer = f"-{peer_id}" if peer_type == "channel" else str(peer_id)

    call("tg.send", {
        "peer": peer,
        "text": body,
        "reply_to": payload.get("message_id", 0),
    }, _id=next_id())


_SEQ = [0]


def next_id() -> int:
    _SEQ[0] += 1
    return _SEQ[0]


def main() -> None:
    global _plugin_name
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError as exc:
            log("error", f"bad json: {exc}")
            continue

        method = msg.get("method")

        if method == "plugin.hello":
            _plugin_name = msg.get("params", {}).get("plugin", "echo")
            log("info", f"handshake with Aurora {msg.get('params', {}).get('version')}")
            respond(msg, {"ok": True})

        elif method == "plugin.load":
            log("info", "loaded")
            respond(msg, {"ok": True})

        elif method == "plugin.unload":
            log("info", "unloading")
            respond(msg, {"ok": True})
            sys.exit(0)

        elif method == "event":
            ev = msg.get("params", {})
            name = ev.get("name")
            data = ev.get("data") or {}
            if name == "message.new":
                try:
                    on_message(data)
                except Exception as exc:  # never let one bad message kill the plugin
                    log("error", f"handler failed: {exc}")
            elif name == "core.start":
                log("info", "core started")

        elif method == "command":
            params = msg.get("params", {})
            if params.get("name") == "echo":
                text = params.get("text", "").strip()
                respond(msg, text or "бек")
            else:
                respond(msg, error=f"unknown command {params.get('name')}")


if __name__ == "__main__":
    main()
