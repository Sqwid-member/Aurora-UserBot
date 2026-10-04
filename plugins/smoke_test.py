#!/usr/bin/env python3
"""Smoke harness: speaks Aurora plugin protocol to a plugin process.

Feeds plugin.hello + plugin.load, then drives scripted scenarios,
answering host-side calls (kv.*, http.request, tg.send, settings.get)
with canned behavior. Fails loudly on protocol violations.
"""

import json
import subprocess
import sys
import threading
import time

FAILURES = []


def check(name, cond, detail=""):
    print(("PASS " if cond else "FAIL ") + name + (f" — {detail}" if detail and not cond else ""))
    if not cond:
        FAILURES.append(name)


class Plugin:
    def __init__(self, path, pname):
        self.p = subprocess.Popen(
            ["python3", path], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True, bufsize=1)
        self.pname = pname
        self.lock = threading.Lock()
        self.seq = [1000]
        self.inbox = {}   # id -> [responses]
        self.calls = []   # host calls received: (method, params)
        self.kv = {}
        self.reader = threading.Thread(target=self._pump, daemon=True)
        self.reader.start()
        # handshake
        r = self.request("plugin.hello", {"plugin": pname, "version": "dev"})
        check(f"{pname}:hello", r == {"ok": True}, str(r))
        r = self.request("plugin.load", {})
        check(f"{pname}:load", r == {"ok": True}, str(r))

    def _pump(self):
        for line in self.p.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except Exception:
                continue
            mid = msg.get("id")
            if msg.get("method") is None and mid in self.inbox:
                self.inbox[mid].append(msg)
                continue
            if msg.get("method"):
                self.answer_host(msg)

    def answer_host(self, msg):
        method, params, mid = msg.get("method"), msg.get("params", {}), msg.get("id")
        with self.lock:
            self.calls.append((method, params))
        if method == "settings.get":
            defaults = {"precision": 6, "default_city": "Kyiv", "enabled": True}
            key = params.get("key")
            if key in self.kv_settings:
                result = {"found": True, "value": self.kv_settings[key]}
            elif key in defaults:
                result = {"found": False}
            else:
                result = {"found": False}
            self._send({"jsonrpc": "2.0", "id": mid, "result": result})
        elif method == "kv.get":
            k = params.get("key")
            if k in self.kv:
                self._send({"jsonrpc": "2.0", "id": mid, "result": {"found": True, "value": self.kv[k]}})
            else:
                self._send({"jsonrpc": "2.0", "id": mid, "result": {"found": False}})
        elif method == "kv.set":
            self.kv[params.get("key")] = params.get("value")
            self._send({"jsonrpc": "2.0", "id": mid, "result": {"ok": True}})
        elif method == "kv.delete":
            self.kv.pop(params.get("key"), None)
            self._send({"jsonrpc": "2.0", "id": mid, "result": {"ok": True}})
        elif method == "kv.keys":
            prefix = params.get("key", "")
            self._send({"jsonrpc": "2.0", "id": mid,
                        "result": {"keys": sorted(k for k in self.kv if k.startswith(prefix))}})
        elif method == "http.request":
            body = self.http_body or ""
            self._send({"jsonrpc": "2.0", "id": mid,
                        "result": {"status": 200, "body": body}})
        elif method == "tg.send":
            self._send({"jsonrpc": "2.0", "id": mid, "result": {"ok": True}})
        else:
            self._send({"jsonrpc": "2.0", "id": mid,
                        "error": {"code": -32601, "message": "no such method"}})

    kv_settings = {}
    http_body = ""

    def _send(self, obj):
        with self.lock:
            self.p.stdin.write(json.dumps(obj, ensure_ascii=False) + "\n")
            self.p.stdin.flush()

    def request(self, method, params, timeout=10.0):
        with self.lock:
            self.seq[0] += 1
            rid = self.seq[0]
            self.inbox[rid] = []
        self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        deadline = time.time() + timeout
        while time.time() < deadline:
            with self.lock:
                box = self.inbox.get(rid, [])
                if box:
                    return box.pop(0).get("result")
            time.sleep(0.02)
        return "<TIMEOUT>"

    def event(self, name, data):
        self._send({"jsonrpc": "2.0", "method": "event",
                    "params": {"name": name, "data": data}})
        time.sleep(0.3)

    def stop(self):
        try:
            self.request("plugin.unload", {})
        except Exception:
            pass
        self.p.terminate()


def main():
    root = "plugins"

    # ---- ping ----
    p = Plugin(f"{root}/ping/ping.py", "ping")
    r = p.request("command", {"name": "ping", "text": ""})
    check("ping:answer", isinstance(r, dict) and "Понг" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "nope", "text": ""})
    check("ping:unknown-rejected", r is None, str(r))
    p.stop()

    # ---- calc ----
    p = Plugin(f"{root}/calc/calc.py", "calc")
    r = p.request("command", {"name": "calc", "text": "2 + 2 * 2"})
    check("calc:precedence", r.get("text", "").endswith("= **6**"), str(r))
    r = p.request("command", {"name": "calc", "text": "sqrt(16) + pi"})
    check("calc:funcs", "4.0" in r.get("text", "") or "= **7.14" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "calc", "text": "1/0"})
    check("calc:divzero", "нуль" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "calc", "text": "__import__('os').system('x')"})
    check("calc:no-eval", "Помилка" in r.get("text", "") or "не " in r.get("text", ""), str(r))
    r = p.request("command", {"name": "calc", "text": ""})
    check("calc:empty", "Вкажіть" in r.get("text", ""), str(r))
    p.stop()

    # ---- notes ----
    p = Plugin(f"{root}/notes/notes.py", "notes")
    r = p.request("command", {"name": "note", "text": "save ssh user@host -p 2222"})
    check("notes:save", "Збережено" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "note", "text": "get ssh"})
    check("notes:get", "user@host" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "note", "text": "list"})
    check("notes:list", "ssh" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "note", "text": "save BAD!KEY x"})
    check("notes:badkey", "латиниця" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "note", "text": "del ssh"})
    check("notes:del", "Видалено" in r.get("text", ""), str(r))
    r = p.request("command", {"name": "note", "text": "get ssh"})
    check("notes:gone", "Немає" in r.get("text", ""), str(r))
    p.stop()

    # ---- weather (stubbed http) ----
    p = Plugin(f"{root}/weather/weather.py", "weather")
    Plugin.http_body = "Kyiv: ⛅ +18°C, відчувається як +17°C"
    r = p.request("command", {"name": "weather", "text": "Lviv"})
    check("weather:body", "⛅" in r.get("text", ""), str(r))
    called = [c for c in p.calls if c[0] == "http.request"]
    check("weather:via-gateway", bool(called) and "wttr.in/Lviv" in called[-1][1].get("url", ""), str(called))
    p.stop()

    # ---- id (context flow) ----
    p = Plugin(f"{root}/id/id.py", "id")
    r = p.request("command", {"name": "id", "text": ""})
    check("id:no-context", "прямо в чаті" in r.get("text", ""), str(r))
    p.event("message.new", {"text": "/id", "out": True, "peer_type": "chat",
                            "peer_id": 123, "peer_title": "Test", "message_id": 7,
                            "from_id": 42, "from_name": "Me"})
    r = p.request("command", {"name": "id", "text": ""})
    check("id:ids", all(s in r.get("text", "") for s in ("123", "`7`", "42")), str(r))
    p.stop()

    # ---- remind (context + timer delivery) ----
    p = Plugin(f"{root}/remind/remind.py", "remind")
    r = p.request("command", {"name": "remind", "text": "1s тест"})
    check("remind:no-context", "Не бачу чату" in r.get("text", ""), str(r))
    p.event("message.new", {"text": "/remind 1s тест", "out": True,
                            "peer_type": "user", "peer_id": 777, "message_id": 9})
    r = p.request("command", {"name": "remind", "text": "1s тест"})
    check("remind:scheduled", "Нагадаю через" in r.get("text", ""), str(r))
    time.sleep(2.0)
    sent = [c for c in p.calls if c[0] == "tg.send"]
    check("remind:delivered", any(c[1].get("peer") == "777" and "тест" in c[1].get("text", "") for c in sent), str(sent))
    r = p.request("command", {"name": "remind", "text": "блабла"})
    check("remind:badformat", "Формат" in r.get("text", ""), str(r))
    p.stop()

    print()
    if FAILURES:
        print(f"{len(FAILURES)} FAILURES: {FAILURES}")
        sys.exit(1)
    print("ALL PLUGIN SMOKE TESTS PASSED")


if __name__ == "__main__":
    main()
