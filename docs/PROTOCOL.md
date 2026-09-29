# Протокол плагінів Aurora

Версія протоколу: **1**

Плагін Aurora — це звичайний виконуваний файл. Він говорить з ядром
newline-delimited JSON-RPC 2.0 через `stdin`/`stdout`. Ніяких спільних
бібліотек, ніякого `dlopen`, ніякого ABI.

Саме тому плагін можна написати на Go, Python, Node, Lua, Ruby, PHP, C чи
будь-чому, що вміє читати JSON зі стандартного вводу-виводу.

---

## 1. Транспорт

```
хост  --write-->  stdin плагіна      (запити та сповіщення)
хост  <--read---  stdout плагіна     (відповіді, запити та сповіщення плагіна)
хост  <--read---  stderr плагіна     (довільні логи, ядро перехоплює й показує)
```

* Один JSON-об'єкт на рядок. Без перенесень усередині, без pretty-print.
* Максимальний розмір рядка — 8 МБ.
* **Ніколи не пишіть у `stdout` нічого, кроме протоколу.** Логи — у `stderr`.
* Обрив `stdout` означає «хост закрився» — завершуйтеся.

## 2. Формат повідомлення

```json
{"jsonrpc":"2.0","method":"tg.send","params":{"peer":"me","text":"Привіт"},"id":7}
```

```json
{"jsonrpc":"2.0","id":7,"result":{"id":42,"peer_id":1234}}
```

```json
{"jsonrpc":"2.0","id":7,"error":{"code":-32001,"message":"plugin \"x\" lacks the \"net\" permission"}}
```

Повідомлення **без** поля `id` — це сповіщення (notification): відповіді на
нього не очікується.

### Коди помилок

| Код | Значення |
|-----|----------|
| `-32700` | не вдалося розібрати JSON |
| `-32600` | некоректний запит |
| `-32601` | невідомий метод |
| `-32602` | некоректні параметри |
| `-32603` | внутрішня помилка (зокрема й panic у обробнику) |
| `-32001` | бракує дозволу (`permissions`) |
| `-32002` | сервіс не готовий (сесія Telegram ще не авторизована) |

---

## 3. Життєвий цикл

```
        ┌──────────┐
        │ created  │
        └────┬─────┘
             │ Host.Start
        ┌────▼─────┐  plugin.hello  ──►  params: {protocol, version, dir, plugin, now}
        │ starting │  ◄── result: {}   (обов'язково!)
        └────┬─────┘
             │ plugin.load   ──►  params: {plugin, events[], commands[]}
        ┌────▼─────┐  ◄── result: {ok: true}
        │ running  │
        └────┬─────┘
             │ Host.Stop
        ┌────▼─────┐  plugin.unload ──►
        │ stopping │  ◄── result: {ok: true}   (потім SIGTERM, потім SIGKILL)
        └────┬─────┘
        ┌────▼─────┐
        │ stopped  │
        └──────────┘
```

Якщо `plugin.hello` не приходить за 15 секунд (налаштується в
`plugins.start_timeout_sec`), плагін вважається мертвим і його вбивають.

Після `plugin.load` ядро надсилає `event` зі `core.start`, якщо плагін
підписаний на нього.

---

## 4. Ядро → плагін

### `plugin.hello` (запит)

Обов'язковий перший запит. Параметри:

```json
{
  "protocol": 1,
  "core": "aurora",
  "version": "1.0.0",
  "dir": "/data/data/com.termux/files/home/.local/share/aurora/plugins/hello",
  "plugin": "hello",
  "now": 1730000000
}
```

Плагін має перевірити `protocol`. Якщо версія не збігається — повернути
помилку, а не мовчки працювати далі.

### `plugin.load` (запит)

```json
{"plugin":"hello","events":["core.start","message.new"],
 "commands":[{"name":"hello","usage":"hello [ім'я]","aliases":["hi"]}]}
```

### `plugin.unload` (запит)

Останній шанс вивантажити стан і закрити файли. Після відповіді —
`SIGTERM`, через 3 секунди — `SIGKILL`.

### `event` (сповіщення)

```json
{"name":"message.new","data":{
  "message_id":42,"peer_id":1234,"peer_type":"user","peer_title":"@durov",
  "from_id":1234,"from_name":"@durov","text":"привіт","date":1730000000,
  "out":false,"reply_to":0,"media":"","is_private":true,"mentions_me":false
}}
```

Ядро надсилає лише ті події, які перелічені в `events` маніфесту.
`"*"` означає усі.

### `command` (запит)

```json
{"name":"hello","text":"Вася","args":["Вася"]}
```

Відповідь:

```json
{"text":"Привіт, Вася! 👋"}
```

### `method` (запит)

Будь-який метод, перелічений у `rpc_methods` маніфесту.

### `ping` (запит)

```json
{"jsonrpc":"2.0","id":9,"result":{"ok":true,"ts":1730000000123}}
```

---

## 5. Плагін → ядро

| Метод | Параметри | Результат | Дозвіл |
|-------|-----------|-----------|--------|
| `log` | `{level, msg}` | `{}` | — |
| `kv.get` | `{key}` | `{value, found}` | — |
| `kv.set` | `{key, value}` | `{}` | — |
| `kv.delete` | `{key}` | `{}` | — |
| `kv.keys` | `{key}` (префікс) | `{keys:[...]}` | — |
| `settings.get` | `{key}` | `{value, found}` | — |
| `settings.set` | `{key, value}` | `{}` | — |
| `settings.schema` | `{fields[], mode?}` | `{ok, fields}` | — |
| `config.get` | `{key}` ( dotted path ) | `{value, found}` | `config` |
| `tg.get_me` | — | обліковий запис | — |
| `tg.send` | `{peer, text, reply_to?, silent?, no_preview?, schedule?, parse_mode?}` | `{id, peer_id, date, text}` | `tg: ["send"]` |
| `tg.history` | `{peer, limit}` | `{messages:[...]}` | `tg: ["read"]` |
| `tg.resolve` | `{key: peer}` | `{id, type, title, username}` | `tg: ["resolve"]` |
| `ui.notify` | `{title, text, level}` | `{}` | — |
| `http.request` | `{method?, url, headers?, body?, timeout?}` | `{status, headers, body, truncated}` | `net: true` |
| `event.subscribe` | `{names:[...]}` | `{ok, events}` | — |
| `core.info` | — | `{version, protocol, events[], methods[]}` | — |

`kv.*` — **спільне** сховище ядра: бачите й те, що пишуть інші плагіни.
`settings.*` — приватний простір імені плагіна (`plugin:<ім'я>:<ключ>`).

`parse_mode` підтримує `""` (звичайний текст) та `"html"`.

Обмеження `http.request`: лише `http`/`https`, максимум 30 секунд, тіло
відповіді обрізається до 1 МБ.

---

## 6. Події

| Подія | Коли |
|-------|------|
| `core.start` | ядро піднялося, плагіни стартують |
| `core.stop` | штатне вимикання |
| `session.started` | Telegram-сесія авторизована |
| `session.ended` | сесія закрита або з'єднання впало |
| `message.new` | нове повідомлення |
| `message.edited` | редагування |
| `message.deleted` | `{peer_id, message_ids[], channel}` |
| `user.typing` | `{user_id, peer_id, action}` |
| `settings.changed` | `{plugin, keys[]}` — налаштування змінено з панелі |
| `chat.action` | набір, прочитання, зміна назви, статус |
| `command.received` | команда з панелі або CLI |

---

## 7. Маніфест

```jsonc
{
  "name": "hello",              // ^[a-z0-9][a-z0-9_.-]{0,31}$, має збігатися з каталогом
  "version": "1.0.0",
  "description": "…",
  "author": "…",
  "license": "MIT",
  "language": "go",             // підказка для панелі
  "tags": ["example"],

  "runtime": {
    "command": "./hello",       // або ім'я з PATH, як-от "python3"
    "args": ["hello.py"],       // відносні шляхи від каталогу плагіна
    "env": {"TZ": "Europe/Kyiv"},
    "wrap": "proot"             // необов'язково: обгортка (див. розділ 8)
  },

  "protocol": 1,
  "events": ["message.new", "core.start"],
  "commands": [
    {"name": "hello", "usage": "hello [ім'я]", "aliases": ["hi"], "in_chat": true}
  ],
  "rpc_methods": [],

  "permissions": {
    "tg": ["send", "read", "resolve"],  // або ["*"]
    "net": false,
    "config": false,
    "env": ["SOME_VAR"],
    "fs": ["data"]
  },

  "limits": {
    "memory_mb": 128,      // RLIMIT_DATA; максимум 2048, мінімум 64
    "cpu_seconds": 0,      // 0 = без ліміту
    "file_mb": 16,         // RLIMIT_FSIZE
    "output_kb": 1024,     // скільки логів зберігати
    "idle_timeout_sec": 0  // 0 = вимкнено
  }
}
```

Валідація: `command` не може бути абсолютним шляхом чи містити `..`; ліміти
обрізаються до стелі; команди й аліаси нижній регістр, дублікати
викидаються.

---

## 8. Ізоляція: чого можна і чого не можна очікувати

Ядро реально робить:

* **обмежує пам'ять** через `RLIMIT_DATA` (НЕ `RLIMIT_AS` — див. нижче),
  час CPU через `RLIMIT_CPU`, розмір файлу через `RLIMIT_FSIZE`, кількість
  дескрипторів через `RLIMIT_NOFILE`;
* **відсікає ядро** (`RLIMIT_CORE = 0`) — плагін не завалить сховище телефона дампами;
* **очищає оточення**: плагін отримує тільки `PATH`, `HOME` (каталог плагіна!),
  `TMPDIR`, `LANG`, `PREFIX` і явно дозволені змінні;
* **ставить `cwd` у каталог плагіна**, `HOME` теж туди ж;
* **створює окрему process group**, тож один `kill` гасить і нащадків;
* **ставить `PDEATHSIG`**, тож плагін не переживе ядро.

### Чому `RLIMIT_DATA`, а не `RLIMIT_AS`

`RLIMIT_AS` обмежує **віртуальний адресний простір**. Go, Python і Node
резервують сотні мегабайт адресного простору під служби рантайму ще до
виконання першого рядка коду: під лімітом у 512 МБ Go-процес помирає з
`failed to reserve page summary memory`, CPython і V8 — теж.

Тобто `RLIMIT_AS` не обмежує реальне споживання пам'яті плагіном; він просто
робить плагін непридатним до запуску. Інтеграційний тест цього зачепив, і
це добре, що помітив.

`RLIMIT_DATA` (з Linux 4.7) діє також на анонімний `mmap`, а саме там
керований рантайм тримає купу. Плагін стартує нормально, а померти з
справжнім OOM уже тоді, коли реально переріс ліміт. На телефоні це саме те,
що потрібно: розбіжний плагін помирає, ядро й решта плагінів працюють далі.

Перевірено експериментально: Go-плагін нормально працює під `RLIMIT_DATA`,
і при спробі алокувати понад ліміт помирає.

Ядро **не** робить і не може cheaply робити на Android:

* справжньої файлової ізоляції (простори імен і `seccomp` недоступні для
  звичайного застосунку в Android);
* мережевої ізоляції, окрім дозволу `permissions.net` на рівні API ядра.

Якщо потрібна справжня ізоляція — обгортка. На Termux:

```json
{
  "runtime": {
    "command": "./main.py",
    "wrap": "proot",
    "args": ["-r", "/data/data/com.termux/files/usr", "-b", "/tmp:/tmp", "-w", "."]
  }
}
```

Ядро знайде `proot` у `PATH` і запустить
`proot -r … -b … -w … ./main.py` у каталозі плагіна. Це дає реальний chroot
всередині Termux (`pkg install proot`).

Чесна порада: `RLIMIT_DATA` + порожнє оточення + `proot` за потреби — це вже
значно більше, ніж дає будь-який популярний юзербот, і при цьому плагін
залишається звичайною програмою, яку легко налагодити.

---

## 9. Готові реалізації

| Мова | SDK | Файл |
|------|-----|------|
| Go | `github.com/Sqwid-member/Aurora-UserBot/sdk/go/aurora` | [`sdk/go/aurora/sdk.go`](../sdk/go/aurora/sdk.go) |
| Python | вбудований у стандартну бібліотеку | [`plugins/echo/echo.py`](../plugins/echo/echo.py) |
| Node.js | вбудований у стандартну бібліотеку | [`plugins/autoaway/autoaway.mjs`](../plugins/autoaway/autoaway.mjs) |
| Lua | ~40 рядків encode/decode у прикладі | [`plugins/pulse/pulse.lua`](../plugins/pulse/pulse.lua) |

Будь-яка інша мова — це 100 рядків: прочитати рядок, розпарсити JSON,
відповісти на `plugin.hello`, читати далі.
