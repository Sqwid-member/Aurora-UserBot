#!/usr/bin/env python3
"""Aurora weather plugin — current weather via wttr.in, no API key needed.

    /weather             -> default city (setting)
    /weather Lviv        -> that city

Uses the host http.request gateway (needs the net permission), so the
plugin process itself never touches the network.
"""

import json
import sys
import time
import urllib.parse

_name = "weather"
_SEQ = [0]
_pending = {}
SETTINGS = {"default_city": "Kyiv"}


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


def call_wait(method: str, params: dict, timeout: float = 15.0):
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


def load_settings() -> None:
    res = call_wait("settings.get", {"key": "default_city"})
    if isinstance(res, dict) and res.get("found"):
        city = str(res.get("value", "") or "").strip()
        if city:
            SETTINGS["default_city"] = city


def fetch(city: str) -> str:
    url = "https://wttr.in/{}?format=%l:+%c+%t,+відчувається+як+%f,+вологість+%h,+вітер+%w".format(
        urllib.parse.quote(city, safe="")
    )
    res = call_wait("http.request", {"method": "GET", "url": url, "timeout": 10})
    if not isinstance(res, dict):
        return "🌦 Не вдалося спитати погоду — спробуй пізніше"
    if res.get("status") != 200:
        return f"🌦 Сервіс погоди відповів {res.get('status')} — спробуй пізніше"
    body = (res.get("body") or "").strip()
    if not body or "Unknown location" in body:
        return f"🌦 Не знаю такого міста: {city}"
    return "🌦 " + body


def handle(text: str) -> str:
    city = (text or "").strip() or SETTINGS["default_city"]
    if len(city) > 64:
        return "🌦 Задовга назва міста"
    return fetch(city)


def dispatch(msg: dict) -> None:
    global _name
    if msg.get("method") is None and msg.get("id") in _pending:
        _pending[msg["id"]].append(msg)
        return
    method = msg.get("method")
    if method == "plugin.hello":
        _name = msg.get("params", {}).get("plugin", "weather")
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
        if ev.get("name") == "settings.changed":
            load_settings()
    elif method == "command":
        params = msg.get("params", {})
        if params.get("name") in ("weather", _name):
            try:
                respond(msg, {"text": handle(params.get("text", ""))})
            except Exception as exc:
                log("error", f"handler failed: {exc}")
                respond(msg, {"text": "🌦 Щось пішло не так — дивись логи"})
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
