# API плагінів Aurora — практичний довідник

Детальний протокол — у [`PROTOCOL.md`](PROTOCOL.md). Тут — як швидко
почати писати плагін.

## Найкоротший можливий плагін (Python)

```python
#!/usr/bin/env python3
import json, sys

for line in sys.stdin:
    if not line.strip():
        continue
    m = json.loads(line)
    if m.get("method") == "plugin.hello":
        print(json.dumps({"jsonrpc":"2.0","id":m["id"],"result":{"ok":True}}), flush=True)
    elif m.get("method") == "plugin.load":
        print(json.dumps({"jsonrpc":"2.0","id":m["id"],"result":{"ok":True}}), flush=True)
```

Маніфест поруч:

```json
{
  "name": "minimal",
  "runtime": {"command": "python3", "args": ["main.py"]},
  "events": ["core.start"],
  "permissions": {"tg": ["send"]}
}
```

Ось і все. Ядро зробить решту.

## Go

```go
package main

import (
    "context"
    "os"

    "github.com/Sqwid-member/Aurora-UserBot/sdk/go/aurora"
)

func main() {
    p := aurora.New()
    p.OnEvent("message.new", func(ctx context.Context, e aurora.Event) error {
        var m aurora.Message
        if err := e.Unmarshal(&m); err != nil { return err }
        if m.Text == "/ping" {
            _, _ = p.Send("me", "pong", aurora.SendOptions{})
        }
        return nil
    })
    p.OnCommand("ping", func(ctx context.Context, c aurora.Command) (string, error) {
        return "pong", nil
    })
    _ = p.Run(os.Args[1:])
}
```

```console
$ cd plugins/hello
$ go build -o hello .     # або make examples з кореня репозиторію
```

SDK не має жодних залежностей — `go.sum` порожній, збірка — миттєва,
бінарник статичний.

## Node.js

```javascript
import { createInterface } from 'node:readline';

const rl = createInterface({ input: process.stdin });
rl.on('line', (line) => {
  if (!line) return;
  const m = JSON.parse(line);
  if (m.method === 'plugin.hello' || m.method === 'plugin.load') {
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: m.id, result: { ok: true } }) + '\n');
  }
});
```

## Lua

Lua не має JSON у стандартній бібліотеці, але в [`plugins/pulse/pulse.lua`](../plugins/pulse/pulse.lua)
лежить самописний кодувальник/декодувальник на ~80 рядків — достатньо для
протоколу. Для більшого JSON підійде будь-яка з чистих Lua-бібліотек.

## Ruby, PHP, C, Rust, Nim, Bash…

Протокол — це JSON у рядках. Напишіть:

1. цикл читання `stdin` по рядках,
2. `json.loads` / `JSON.parse`,
3. відповідь на `plugin.hello` і `plugin.load`,
4. обробку `event` і `command`.

Сто рядків. Більше нічого знати не потрібно.

---

## Каталог host-API

### `tg.send`

```python
call("tg.send", {"peer": "@durov", "text": "Привіт", "reply_to": 42})
```

`peer` приймає: `@username`, `username`, `+380…`, `-1001234567890` (канал),
`12345` (юзер або чат за кешем), `me`, будь-яке посилання `t.me/...`.

Потрібен дозвіл `permissions.tg: ["send"]`.

### `tg.history`

```python
call("tg.history", {"peer": "@durov", "limit": 20})
```

Повертає масив об'єктів того ж виду, що й `message.new`, у хронологічному
порядку (старі → нові). Потрібен `tg: ["read"]`.

### `tg.resolve`

```python
call("tg.resolve", {"key": "@durov"})
# → {"id": 1234, "type": "user", "title": "Дурів", "username": "durov"}
```

### `ui.notify`

```python
call("ui.notify", {"title": "Готово", "text": "Текст оновлено", "level": "info"})
```

Тост з'явиться в панелі. `level`: `info` | `warn` | `error`.

### `http.request`

```python
call("http.request", {"url": "https://api.example.com/ping"})
```

Потрібен `permissions.net: true`. Лише `http`/`https`, до 30 с, тіло до 1 МБ.

### `settings.*` і `kv.*`

`settings` — приватний простір плагіна, зберігається між перезапусками.
`kv` — спільний: інші плагіни теж його бачать.

```python
call("settings.set", {"key": "interval", "value": 300})
call("settings.get", {"key": "interval"})
```

---

## Події та підписка

Маніфест визначає, що саме вам надсилатимуть:

```json
"events": ["message.new", "message.edited"]
```

`"*"` — усе. Додатково можна попросити додаткові події з коду:

```python
call("event.subscribe", {"names": ["user.typing"]})
```

### `message.new`

```json
{
  "message_id": 42,
  "peer_id": 1234,
  "peer_type": "user",
  "peer_title": "@durov",
  "from_id": 1234,
  "from_name": "@durov",
  "from_bot": false,
  "text": "привіт",
  "date": 1730000000,
  "out": false,
  "reply_to": 0,
  "media": "photo",
  "is_private": true,
  "mentions_me": false
}
```

`out: true` означає, що це ви відправили — майже завжди варто ігнорувати
такі повідомлення, щоб плагін не реагував на власні репліки.

---

## Дозволи — чекліст

Перед публікацією плагіна пройдіться:

* [ ] `permissions.tg` містить **тільки** потрібне (`send`, `read`, `resolve`).
* [ ] `permissions.net` — лише якщо плагін ходить в зовнішні API.
* [ ] `permissions.config` — зазвичай не потрібно; `config.set` все одно
      заблоковано, зміни робіть у панелі.
* [ ] `permissions.env` — перелік змінних, які справді треба.
* [ ] `limits.memory_mb` — реалістична оцінка; ядро обрізає до 2048.
* [ ] `limits.idle_timeout_sec` — страховка від завислих плагінів.
* [ ] Події в `events` — ті, що реально потрібні. Кожна подія — це
      повідомлення в кожному запущеному екземплярі плагіна.

## Швидкий контроль якості

```console
$ aurora doctor              # середовище
$ aurora plugins             # що встановлено
$ aurora plugin restart <ім'я>
```

У панелі: вкладка «Плагіни» показує pid, кількість доставлених і
**втрачених** подій, помилки та час роботи. Якщо `events_dropped` росте —
плагін не встигає; або полегшіть логіку, або зніміть підписку на зайві події.
