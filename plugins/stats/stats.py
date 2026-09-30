#!/usr/bin/env python3
"""Aurora stats plugin — статус і статистика юзербота.

Підписується на події ядра, рахує повідомлення/команди/сесії і віддає
зведену статистику командою /stats. Лічильники зберігаються в kv ядра,
тому переживають перезапуски.
"""

import json
import sys
import threading
import time

KV_KEY = "stats:counters"
SAVE_EVERY = 25

SETTINGS = {"title": "Статус Aurora", "show_today": True}

_lock = threading.Lock()
_seq = [0]
_plugin_name = "stats"
_started_at = time.time()
_session_user = ""
_session_online = False
_sessions_total = 0
_since_save = [0]

_counters = {
    "msg_in": 0,
    "msg_out": 0,
    "commands": 0,
    "day": "",
    "day_in": 0,
    "day_out": 0,
}


def log(level, message):
    print(f"[{level.upper()}] {message}", file=sys.stderr, flush=True)


def _next_id():
    _seq[0] += 1
    return _seq[0]


def send(obj):
    with _lock:
        sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
        sys.stdout.flush()


def call(method, params=None):
    """Пожежа-і-забудь: відповідь ядра не чекаємо."""
    send({"jsonrpc": "2.0", "id": _next_id(),
          "method": method, "params": params or {}})


def respond(req, result=None, error=None):
    frame = {"jsonrpc": "2.0", "id": req.get("id")}
    if error:
        frame["error"] = {"code": -32000, "message": str(error)}
    else:
        frame["result"] = result if result is not None else {}
    send(frame)


def kv_get(key, timeout=10):
    """Синхронний kv.get: чекаємо відповідь із нашим id."""
    rid = _next_id()
    send({"jsonrpc": "2.0", "id": rid,
          "method": "kv.get", "params": {"key": key}})
    deadline = time.time() + timeout
    while time.time() < deadline:
        line = sys.stdin.readline()
        if not line:
            return None
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue
        if msg.get("id") == rid:
            res = msg.get("result") or {}
            if res.get("found"):
                return res.get("value")
            return None
        # Чужий фрейм під час очікування — обробити по-простому.
        dispatch(msg)
    return None


def kv_save():
    call("kv.set", {"key": KV_KEY, "value": _counters})
    _since_save[0] = 0


def settings_get(key, default, timeout=10):
    rid = _next_id()
    send({"jsonrpc": "2.0", "id": rid,
          "method": "settings.get", "params": {"key": key}})
    deadline = time.time() + timeout
    while time.time() < deadline:
        line = sys.stdin.readline()
        if not line:
            return default
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue
        if msg.get("id") == rid:
            res = msg.get("result") or {}
            if res.get("found"):
                return res.get("value", default)
            return default
        dispatch(msg)
    return default


def load_settings():
    title = settings_get("title", "Статус Aurora")
    today = settings_get("show_today", True)
    SETTINGS["title"] = str(title) if isinstance(title, str) and title.strip() else "Статус Aurora"
    SETTINGS["show_today"] = bool(today) if isinstance(today, bool) else True
    log("info", f"settings: {SETTINGS}")


def _today():
    return time.strftime("%Y-%m-%d")


def _roll_day():
    today = _today()
    if _counters.get("day") != today:
        _counters["day"] = today
        _counters["day_in"] = 0
        _counters["day_out"] = 0


def _touch():
    _since_save[0] += 1
    if _since_save[0] >= SAVE_EVERY:
        kv_save()


def fmt_uptime(seconds):
    seconds = int(seconds)
    d, seconds = divmod(seconds, 86400)
    h, seconds = divmod(seconds, 3600)
    m, s = divmod(seconds, 60)
    if d:
        return f"{d} д {h} год"
    if h:
        return f"{h} год {m} хв"
    if m:
        return f"{m} хв {s} с"
    return f"{s} с"


def snapshot():
    global _session_user, _session_online, _sessions_total
    _roll_day()
    uptime = fmt_uptime(time.time() - _started_at)
    if _session_online and _session_user:
        session = f"онлайн ({_session_user})"
    elif _session_user:
        session = f"офлайн ({_session_user})"
    else:
        session = "невідомо"
    lines = [
        f"{SETTINGS['title']}",
        f"• плагін працює: {uptime}",
        f"• сесія: {session} (сесій: {_sessions_total})",
        f"• повідомлень: вхідних {_counters['msg_in']} / вихідних {_counters['msg_out']}",
    ]
    if SETTINGS["show_today"]:
        lines.append(f"• сьогодні: вхідних {_counters['day_in']} / вихідних {_counters['day_out']}")
    lines.append(f"• команд виконано: {_counters['commands']}")
    return "\n".join(lines)


def on_event(ev):
    global _session_user, _session_online, _sessions_total
    name = ev.get("name")
    data = ev.get("data") or {}
    if name == "message.new":
        _roll_day()
        if data.get("out"):
            _counters["msg_out"] += 1
            _counters["day_out"] += 1
        else:
            _counters["msg_in"] += 1
            _counters["day_in"] += 1
        _touch()
    elif name == "session.started":
        _sessions_total += 1
        _session_online = True
        user = data.get("username") or data.get("first_name") or str(data.get("id", ""))
        if user:
            _session_user = f"@{user}" if not str(user).startswith("@") and data.get("username") else str(user)
        kv_save()
    elif name == "session.ended":
        _session_online = False
        kv_save()
    elif name == "command.received":
        _counters["commands"] += 1
        _touch()
    elif name == "settings.changed":
        load_settings()


def dispatch(msg):
    method = msg.get("method")
    if method == "plugin.hello":
        log("info", f"handshake with Aurora {msg.get('params', {}).get('version')}")
        respond(msg, {"ok": True})
    elif method == "plugin.load":
        restored = kv_get(KV_KEY)
        if isinstance(restored, dict):
            for k in ("msg_in", "msg_out", "commands", "day", "day_in", "day_out"):
                if k in restored:
                    _counters[k] = restored[k]
            _roll_day()
            log("info", f"counters restored: {restored}")
        load_settings()
        log("info", "loaded")
        respond(msg, {"ok": True})
    elif method == "plugin.unload":
        kv_save()
        log("info", "unloading")
        respond(msg, {"ok": True})
        sys.exit(0)
    elif method == "event":
        try:
            on_event(msg.get("params", {}))
        except Exception as exc:
            log("error", f"handler failed: {exc}")
    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") == "stats":
            _counters["commands"] += 1
            _touch()
            respond(msg, {"text": snapshot()})
        else:
            respond(msg, error=f"unknown command {params.get('name')}")
    elif method is None:
        pass  # відповідь на наш fire-and-forget виклик


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
        try:
            dispatch(msg)
        except Exception as exc:
            log("error", f"dispatch failed: {exc}")


if __name__ == "__main__":
    main()
