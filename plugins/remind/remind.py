#!/usr/bin/env python3
"""Aurora remind plugin — delayed reminders delivered back to the chat.

Usage in any chat: /remind 10m politivat kvity
Units: s (seconds), m (minutes, default), h (hours), d (days).

How delivery works: the host routes /remind through the `command` method
but never passes the chat peer, so the plugin snoops message.new for the
owner's own /remind message and remembers (peer, text, time). When the
timer fires it sends via tg.send. A panel/CLI call without a recent chat
context gets an honest error instead of a reminder to nowhere.
"""

import json
import re
import sys
import threading
import time

_name = "remind"
_SEQ = [0]
_pending = {}

# command word -> {peer, text, at, message_id}; only filled from message.new
_context: dict = {}
_context_lock = threading.Lock()

UNITS = {"s": 1, "m": 60, "h": 3600, "d": 86400}
MAX_DELAY = 30 * 86400  # 30 days
CTX_TTL = 180  # seconds a chat context stays valid


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


def next_id() -> int:
    _SEQ[0] += 1
    return _SEQ[0]


def human_delay(sec: int) -> str:
    d, sec = divmod(sec, 86400)
    h, sec = divmod(sec, 3600)
    m, sec = divmod(sec, 60)
    parts = []
    if d:
        parts.append(f"{d} д")
    if h:
        parts.append(f"{h} год")
    if m:
        parts.append(f"{m} хв")
    if sec or not parts:
        parts.append(f"{sec} с")
    return " ".join(parts)


def parse_plan(text: str):
    """Returns (delay_seconds, body) or an error string."""
    m = re.match(r"^\s*(\d+)\s*([smhdSMHD])?\s+([\s\S]+?)\s*$", text or "")
    if not m:
        return None, "⏰ Формат: /remind 10m текст (одиниці: s/m/h/d, без одиниці — хвилини)"
    amount = int(m.group(1))
    unit = (m.group(2) or "m").lower()
    body = m.group(3).strip()
    if not body:
        return None, "⏰ А що нагадати? /remind 10m текст"
    delay = amount * UNITS[unit]
    if delay <= 0:
        return None, "⏰ Час має бути в майбутньому"
    if delay > MAX_DELAY:
        return None, "⏰ Максимум — 30 днів"
    return (delay, body), None


def peer_of(payload: dict) -> str:
    if payload.get("peer_type") == "channel":
        return f"-{payload.get('peer_id', 0)}"
    return str(payload.get("peer_id", 0))


def remember(payload: dict) -> None:
    text = (payload.get("text") or "").strip()
    word = text.split()[0].lower() if text.split() else ""
    if word not in ("/remind", "remind"):
        return
    with _context_lock:
        _context["remind"] = {
            "peer": peer_of(payload),
            "text": text,
            "at": time.time(),
            "message_id": payload.get("message_id", 0),
        }


def lookup(text: str):
    want = " ".join((text or "").split()).lower()
    with _context_lock:
        rec = _context.get("remind")
        if not rec:
            return None
        if time.time() - rec["at"] > CTX_TTL:
            return None
        got = " ".join(rec["text"].split()).lower()
        # The chat trigger carries the "/remind " prefix, the command text doesn't.
        if got == "/remind " + want or got == "remind " + want:
            return rec
        return None


def deliver(peer: str, body: str, reply_to: int) -> None:
    params = {"peer": peer, "text": f"⏰ Нагадую: {body}"}
    if reply_to:
        params["reply_to"] = reply_to
    send({"jsonrpc": "2.0", "id": next_id(), "method": "tg.send", "params": params})


def dispatch(msg: dict) -> None:
    global _name
    if msg.get("method") is None and msg.get("id") in _pending:
        _pending[msg["id"]].append(msg)
        return
    method = msg.get("method")
    if method == "plugin.hello":
        _name = msg.get("params", {}).get("plugin", "remind")
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
            # Record-only: only the owner's own /remind carries a peer we
            # may later deliver to. Never reply here — the host delivers the
            # command result itself, otherwise every reminder fires twice.
            if data.get("out"):
                try:
                    remember(data)
                except Exception as exc:
                    log("error", f"remember failed: {exc}")
    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") not in ("remind", _name):
            respond(msg, error=f"unknown command {params.get('name')}")
            return
        plan, err = parse_plan(params.get("text", ""))
        if err:
            respond(msg, {"text": err})
            return
        delay, body = plan
        rec = lookup(params.get("text", ""))
        if rec is None:
            respond(msg, {"text": "⏰ Не бачу чату для доставки — виклич /remind прямо в Telegram"})
            return
        timer = threading.Timer(delay, deliver, args=(rec["peer"], body, rec["message_id"]))
        timer.daemon = True
        timer.start()
        log("info", f"scheduled in {delay}s for peer {rec['peer']}")
        respond(msg, {"text": f"⏰ Нагадаю через {human_delay(delay)}"})


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
