#!/usr/bin/env python3
"""Aurora notes plugin — tiny personal KV notebook.

    /note save ssh user@host -p 2222
    /note get ssh
    /note list
    /note del ssh

Notes live in the host KV store (survive restarts), plain text, no
encryption — don't store passwords here.
"""

import json
import re
import sys
import time

_name = "notes"
_SEQ = [0]
_pending = {}

PREFIX = "notes:"
KEY_RE = re.compile(r"^[a-z0-9][a-z0-9_.-]{0,31}$")
MAX_TEXT = 2000

HELP = (
    "📝 Нотатки: /note save <ключ> <текст> · /note get <ключ> · "
    "/note list · /note del <ключ>"
)


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


def call_wait(method: str, params: dict, timeout: float = 10.0):
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
                return resp.get("result") or {}
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


def kv_get(key: str):
    res = call_wait("kv.get", {"key": PREFIX + key})
    if not isinstance(res, dict) or not res.get("found"):
        return None
    return res.get("value")


def kv_set(key: str, text: str) -> bool:
    res = call_wait("kv.set", {"key": PREFIX + key, "value": text})
    return isinstance(res, dict)


def kv_delete(key: str) -> bool:
    res = call_wait("kv.delete", {"key": PREFIX + key})
    return isinstance(res, dict)


def kv_list() -> list:
    res = call_wait("kv.keys", {"key": PREFIX})
    if not isinstance(res, dict):
        return []
    keys = res.get("keys") or []
    return sorted(k[len(PREFIX):] for k in keys if isinstance(k, str) and k.startswith(PREFIX))


def handle(text: str) -> str:
    parts = (text or "").strip().split(None, 2)
    if not parts:
        return HELP
    sub = parts[0].lower()
    if sub == "list":
        keys = kv_list()
        if not keys:
            return "📝 Нотаток поки немає — /note save <ключ> <текст>"
        return "📝 Нотатки:\n" + "\n".join(f"• `{k}`" for k in keys)
    if sub in ("save", "set", "add"):
        if len(parts) < 3:
            return "📝 Формат: /note save <ключ> <текст>"
        key = parts[1].lower()
        if not KEY_RE.match(key):
            return "📝 Ключ: латиниця, цифри, _ . - (до 32 символів)"
        body = parts[2].strip()
        if not body:
            return "📝 Порожню нотатку не збережу"
        if len(body) > MAX_TEXT:
            return f"📝 Задовга нотатка (максимум {MAX_TEXT} символів)"
        if not kv_set(key, body):
            return "📝 Не вдалося зберегти — дивись логи ядра"
        return f"📝 Збережено: `{key}`"
    if sub == "get":
        if len(parts) < 2:
            return "📝 Формат: /note get <ключ>"
        key = parts[1].lower()
        val = kv_get(key)
        if val is None:
            return f"📝 Немає нотатки `{key}`"
        return f"📝 `{key}`:\n{val}"
    if sub in ("del", "delete", "rm"):
        if len(parts) < 2:
            return "📝 Формат: /note del <ключ>"
        key = parts[1].lower()
        if kv_get(key) is None:
            return f"📝 Немає нотатки `{key}`"
        kv_delete(key)
        return f"📝 Видалено: `{key}`"
    if sub in ("help", "допомога"):
        return HELP
    return HELP


def dispatch(msg: dict) -> None:
    global _name
    if msg.get("method") is None and msg.get("id") in _pending:
        _pending[msg["id"]].append(msg)
        return
    method = msg.get("method")
    if method == "plugin.hello":
        _name = msg.get("params", {}).get("plugin", "notes")
        respond(msg, {"ok": True})
    elif method == "plugin.load":
        log("info", "loaded")
        respond(msg, {"ok": True})
    elif method == "plugin.unload":
        log("info", "unloading")
        respond(msg, {"ok": True})
        sys.exit(0)
    elif method == "event":
        pass  # stateless: every call reads KV fresh
    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") in ("note", "notes", _name):
            try:
                respond(msg, {"text": handle(params.get("text", ""))})
            except Exception as exc:
                log("error", f"handler failed: {exc}")
                respond(msg, {"text": "📝 Щось пішло не так — дивись логи"})
        else:
            respond(msg, error=f"unknown command {params.get('name')}")


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
