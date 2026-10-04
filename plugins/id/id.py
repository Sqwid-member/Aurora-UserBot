#!/usr/bin/env python3
"""Aurora id plugin — shows chat/user/message identifiers.

Type /id in any chat and the host posts the IDs back. Works through the
command router, so no Telegram permissions are needed at all: the plugin
snoops its own trigger message (record-only, like remind) and answers
from that context.
"""

import json
import sys
import threading
import time

_name = "id"
_pending = {}
_SEQ = [0]

# Last owner's /id trigger still fresh enough to answer from.
_last: dict = {}
_last_lock = threading.Lock()
CTX_TTL = 180


def log(level: str, message: str) -> None:
    print(f"[{level.upper()}] {message}", file=sys.stderr, flush=True)


def send(obj: dict) -> None:
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def respond(req: dict, result=None, error: str | None = None) -> None:
    frame = {"jsonrpc": "2.0", "id": req.get("id")}
    if error:
        frame["error"] = {"code": -32000, "message": error}
    else:
        frame["result"] = result
    send(frame)


def remember(payload: dict) -> None:
    text = (payload.get("text") or "").strip()
    word = text.split()[0].lower() if text.split() else ""
    if word not in ("/id", "id"):
        return
    with _last_lock:
        _last["id"] = {
            "peer_type": payload.get("peer_type", "?"),
            "peer_id": payload.get("peer_id", 0),
            "peer_title": payload.get("peer_title", ""),
            "message_id": payload.get("message_id", 0),
            "from_id": payload.get("from_id", 0),
            "from_name": payload.get("from_name", ""),
            "at": time.time(),
        }


def lookup():
    with _last_lock:
        rec = _last.get("id")
        if not rec:
            return None
        if time.time() - rec["at"] > CTX_TTL:
            return None
        return rec


def format_ids(rec: dict) -> str:
    lines = ["🆔 Ідентифікатори:"]
    title = rec["peer_title"] or "—"
    lines.append(f"💬 Чат: {title} (`{rec['peer_type']} {rec['peer_id']}`)")
    lines.append(f"✉️ Повідомлення: `{rec['message_id']}`")
    if rec["from_id"]:
        who = rec["from_name"] or "—"
        lines.append(f"👤 Відправник: {who} (`{rec['from_id']}`)")
    return "\n".join(lines)


def dispatch(msg: dict) -> None:
    global _name
    if msg.get("method") is None and msg.get("id") in _pending:
        _pending[msg["id"]].append(msg)
        return
    method = msg.get("method")
    if method == "plugin.hello":
        _name = msg.get("params", {}).get("plugin", "id")
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
        if ev.get("name") == "message.new":
            data = ev.get("data") or {}
            # Record-only, and only the owner's own trigger: the host
            # delivers the answer itself, otherwise /id replies twice.
            if data.get("out"):
                try:
                    remember(data)
                except Exception as exc:
                    log("error", f"remember failed: {exc}")
    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") not in ("id", _name):
            respond(msg, error=f"unknown command {params.get('name')}")
            return
        rec = lookup()
        if rec is None:
            respond(msg, {"text": "🆔 Виклич /id прямо в чаті — звідти видно ідентифікатори"})
        else:
            respond(msg, {"text": format_ids(rec)})


def main() -> None:
    while True:
        line = sys.stdin.readline()
        if not line:
            break
        line = line.strip()
        if not line:
            continue
        try:
            dispatch(json.loads(line))
        except json.JSONDecodeError as exc:
            log("error", f"bad json: {exc}")
        except Exception as exc:
            log("error", f"dispatch failed: {exc}")


if __name__ == "__main__":
    main()
