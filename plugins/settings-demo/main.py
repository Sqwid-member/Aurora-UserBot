#!/usr/bin/env python3
"""Aurora settings-demo plugin.

Shows the graphical settings menu end to end:
  1. The schema lives in aurora.plugin.json ("settings").
  2. The panel renders it; values land in the private settings namespace.
  3. This plugin reads them with settings.get and re-reads on the
     settings.changed event (subscribed in the manifest).

Answers to "/demo <text>" in chat and to the "demo" panel command.
"""

import json
import sys

SETTINGS_KEYS = ("reply_prefix", "mode", "max_len", "private_only")

DEFAULTS = {
    "reply_prefix": "\U0001F916",
    "mode": "normal",
    "max_len": 200,
    "private_only": True,
}

_state = dict(DEFAULTS)
_seq = [0]
_pending = {}  # id -> settings key we asked for


def log(level, message):
    print(f"[{level.upper()}] {message}", file=sys.stderr, flush=True)


def send(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def next_id():
    _seq[0] += 1
    return _seq[0]


def call(method, params=None):
    _id = next_id()
    send({"jsonrpc": "2.0", "id": _id, "method": method, "params": params or {}})
    return _id


def respond(req, result=None, error=None):
    frame = {"jsonrpc": "2.0", "id": req.get("id")}
    if error:
        frame["error"] = {"code": -32001, "message": error}
    else:
        frame["result"] = result
    send(frame)


def refresh_settings():
    """Ask the host for every schema key; answers arrive as responses."""
    for key in SETTINGS_KEYS:
        _pending[call("settings.get", {"key": key})] = key


def coerce(key, value):
    if key == "max_len":
        try:
            return max(1, int(float(value)))
        except (TypeError, ValueError):
            return DEFAULTS[key]
    if key == "private_only":
        return value is True or str(value).lower() in ("1", "true", "yes", "on")
    if key == "mode" and value not in ("normal", "shout", "whisper"):
        return DEFAULTS[key]
    if value is None:
        return DEFAULTS[key]
    return value


def on_response(msg):
    key = _pending.pop(msg.get("id"), None)
    if key is None:
        return
    result = msg.get("result") or {}
    if "value" in result:
        _state[key] = coerce(key, result["value"])
    else:
        _state[key] = DEFAULTS[key]


def format_text(text):
    mode = _state.get("mode", "normal")
    if mode == "shout":
        text = text.upper()
    elif mode == "whisper":
        text = text.lower()
    try:
        max_len = int(_state.get("max_len", 200))
    except (TypeError, ValueError):
        max_len = 200
    if max_len > 0 and len(text) > max_len:
        text = text[:max_len] + "…"
    prefix = _state.get("reply_prefix", "")
    return f"{prefix} {text}".strip() if prefix else text


def on_message(payload):
    text = (payload.get("text") or "").strip()
    if not text or payload.get("out"):
        return
    if not text.lower().startswith("/demo"):
        return
    if _state.get("private_only") and not payload.get("is_private", True):
        return
    body = text[5:].strip() or "порожньо"
    peer_type = payload.get("peer_type", "user")
    peer_id = payload.get("peer_id", 0)
    peer = f"-{peer_id}" if peer_type == "channel" else str(peer_id)
    call("tg.send", {
        "peer": peer,
        "text": format_text(body),
        "reply_to": payload.get("message_id", 0),
    }, )


def main():
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
            respond(msg, {"ok": True})
        elif method == "plugin.load":
            respond(msg, {"ok": True})
            refresh_settings()
        elif method == "plugin.unload":
            respond(msg, {"ok": True})
            sys.exit(0)
        elif method == "event":
            ev = msg.get("params", {})
            name = ev.get("name")
            if name in ("core.start", "settings.changed"):
                refresh_settings()
            elif name == "message.new":
                try:
                    on_message(ev.get("data") or {})
                except Exception as exc:
                    log("error", f"handler failed: {exc}")
        elif method == "command":
            params = msg.get("params", {})
            if params.get("name") == "demo":
                respond(msg, format_text(params.get("text", "").strip() or "порожньо"))
            else:
                respond(msg, error=f"unknown command {params.get('name')}")
        elif method is None and msg.get("id") is not None:
            on_response(msg)


if __name__ == "__main__":
    main()
