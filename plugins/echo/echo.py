#!/usr/bin/env python3
"""Aurora echo plugin — a complete, dependency-free example.

Aurora talks to plugins over newline-delimited JSON-RPC on stdin/stdout.
Logs go to stderr; the host picks them up and routes them into the Aurora
log with this plugin's scope. That is the whole contract.
"""

import json
import sys
import threading
import time

# JSON-RPC 2.0 error codes Aurora returns.
E_FORBIDDEN = -32001
E_UNAVAILABLE = -32002

_stdin_lock = threading.Lock()
_plugin_name = "echo"
_SEQ = [0]
_pending = {}

SETTINGS = {"enabled": True, "reply_prefix": ""}


def log(level: str, message: str) -> None:
    """Write to stderr — never stdout, which carries the protocol."""
    print(f"[{level.upper()}] {message}", file=sys.stderr, flush=True)


def next_id() -> int:
    _SEQ[0] += 1
    return _SEQ[0]


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


def dispatch(msg: dict) -> None:
    # A response to one of our own calls: stash it for the waiter.
    if msg.get("method") is None and msg.get("id") in _pending:
        _pending[msg["id"]].append(msg)
        return

    method = msg.get("method")

    if method == "plugin.hello":
        global _plugin_name
        _plugin_name = msg.get("params", {}).get("plugin", "echo")
        log("info", f"handshake with Aurora {msg.get('params', {}).get('version')}")
        respond(msg, {"ok": True})

    elif method == "plugin.load":
        load_settings()
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
        try:
            if name == "message.new":
                on_message(data)
            elif name == "core.start":
                log("info", "core started")
            elif name == "settings.changed":
                load_settings()
                log("info", f"settings reloaded: {SETTINGS}")
        except Exception as exc:  # never let one bad message kill the plugin
            log("error", f"handler failed: {exc}")

    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") == "echo":
            if not SETTINGS["enabled"]:
                respond(msg, {"text": "echo вимкнено в налаштуваннях"})
                return
            text = params.get("text", "").strip()
            respond(msg, {"text": SETTINGS["reply_prefix"] + (text or "бек")})
        else:
            respond(msg, error=f"unknown command {params.get('name')}")


def call_wait(method: str, params: dict, timeout: float = 10.0):
    """Synchronous call: waits for the response with our id, dispatching
    anything else that arrives meanwhile."""
    rid = next_id()
    box: list = []
    _pending[rid] = box
    try:
        send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        deadline = time.time() + timeout
        while time.time() < deadline:
            if box:
                resp = box.pop(0)
                if resp.get("error"):
                    return None
                return (resp.get("result") or {})
            line = sys.stdin.readline()
            if not line:
                return None
            line = line.strip()
            if not line:
                continue
            try:
                dispatch(json.loads(line))
            except json.JSONDecodeError as exc:
                log("error", f"bad json: {exc}")
        return None
    finally:
        _pending.pop(rid, None)


def settings_get(key: str, default):
    res = call_wait("settings.get", {"key": key})
    if not isinstance(res, dict):
        return default
    if not res.get("found"):
        return default
    return res.get("value", default)


def load_settings():
    enabled = settings_get("enabled", True)
    prefix = settings_get("reply_prefix", "")
    SETTINGS["enabled"] = bool(enabled) if isinstance(enabled, bool) else True
    SETTINGS["reply_prefix"] = str(prefix) if isinstance(prefix, str) else ""


def on_message(payload: dict) -> None:
    if not SETTINGS["enabled"]:
        return
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
        "text": SETTINGS["reply_prefix"] + body,
        "reply_to": payload.get("message_id", 0),
    }, _id=next_id())


def main() -> None:
    while True:
        line = sys.stdin.readline()
        if not line:
            break
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError as exc:
            log("error", f"bad json: {exc}")
            continue
        try:
            dispatch(msg)
        except Exception as exc:
            log("error", f"dispatch failed: {exc}")


if __name__ == "__main__":
    main()
