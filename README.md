# Aurora 🌌

Модульний Telegram **юзербот** на Go. Один статичний бінарник, який працює
скрізь, де працює Go — зокрема **прямо в Termux на телефоні**. Плагіни
пишуться будь-якою мовою. Керується з локального сайту.

```
┌──────────────────────────────────────────────────────────────┐
│  aurora (один бінарник, ~12 МБ, CGO вимкнено)                │
│                                                              │
│   ┌──────────┐  JSON-RPC  ┌──────────────┐  stdio  ┌───────┐ │
│   │   MTProto│──(stdio)──▶│ плагін-менеджер│◀────────│ плагін│ │
│   │  (gotd)  │◀──────────│  + ліміти     │────────▶│  .go  │ │
│   └──────────┘  події     └──────────────┘         │  .py  │ │
│        │                                             │  .lua │ │
│        │                                             │  .mjs │ │
│   ┌────▼─────┐   HTTP/SSE   ┌────────────────────┐  └───────┘ │
│   │   панель │◀─────────────│ 127.0.0.1:8420     │            │
│   └──────────┘              └────────────────────┘            │
└──────────────────────────────────────────────────────────────┘
```

---

## Що саме тут важливо

| Вимога | Рішення |
|--------|---------|
| **Максимальна швидкодія** | Go, `CGO_ENABLED=0`, без фреймворків. Розбір апдейтів не робить жодних мережевих викликів — тільки кеш імен і map-и. Черга подій обмежена, доставка неблокуюча. |
| **Мінімум ОП** | `debug.SetMemoryLimit` за замовчуванням 96 МБ, трохи goroutine, лог у кільцевий буфер, `kv` — один JSON у пам'яті, панель — один HTML без CDN. `RLIMIT_AS` на кожен плагін. |
| **Модульність** | Ядро не знає про конкретні плагіни. Плагін — звичайний виконуваний файл, спілкується через JSON-RPC, має маніфест, дозволи, ліміти та життєвий цикл. |
| **Плагіни на кількох мовах** | Go SDK (без залежностей), Python, Node.js, Lua — приклади в репозиторії. Протокол — JSON у рядках, тож підходить будь-що. |
| **Termux** | `scripts/install.sh` ставить все одним рядком, бінарник статичний, `PDEATHSIG` і `prlimit` працюють на ядрі Android, панель відкривається через `termux-open-url`. |
| **Просте встановлення** | Один curl-рядок або `make install`. Немає зовнішніх залежностей, баз даних, демонів. |
| **Локальний сайт** | Вбудований SPA: статус, плагіни, вхід, чат, логи, налаштування. Токен у cookie, слухає лише loopback. |

---

## Встановлення на Termux

```console
$ pkg install curl git
$ curl -fsSL https://raw.githubusercontent.com/aurora/aurora/main/scripts/install.sh | bash
```

Скрипт сам:

1. перевірить оточення і поставить лише те, чого бракує;
2. спробує завантажити готовий бінарник, а якщо немає — збере з вихідного коду;
3. покладе `~/bin/aurora`, створить `~/.local/share/aurora/`;
4. скопіює приклади плагінів.

Якщо `~/bin` не в `PATH`, скрипт скаже який рядок додати в `~/.bashrc`.

### Звичайний Linux / macOS

```console
$ git clone https://github.com/aurora/aurora && cd aurora
$ make install        # або: go build -o ~/bin/aurora ./cmd/aurora
```

---

## Перший запуск

Aurora — **юзербот**, а не бот, тому потрібні власні `app_id` і `app_hash`.
Це обов'язково: Telegram не приймає реєстрації з пустими ключами, і жоден
проєкт не має права їх вигадувати замість вас.

1. Відкрийте <https://my.telegram.org> → **API development tools** →
   заповніть форму → отримайте `App api_id` і `App api_hash`.

2. Запишіть їх у конфіг:

```console
$ aurora config > ~/.local/share/aurora/etc/config.json
$ nano ~/.local/share/aurora/etc/config.json
```

```jsonc
{
  "telegram": {
    "app_id": 1234567,              // ← ваш
    "app_hash": "0123456789abcdef", // ← ваш
    "phone": "+380501234567"       // можна залишити порожнім — спитає при старті
  }
}
```

3. Запустіть:

```console
$ aurora run
```

Ядро надрукує адресу панелі з токеном і відкриє її (`termux-open-url`).
Там можна і увійти, і керувати плагінами.

> **Лайфхак для повторного входу.** Якщо вже користуєтеся Telethon або
> Pyrogram — не вводьте SMS знову. Експортуйте `StringSession` звідти і
> імпортуйте сюди: `aurora session import "1BVts…"`. Сумісний формат
> використовується в обидва боки, `aurora session export` поверне такий рядок.

### Автозапуск після перезавантаження

```console
$ pkg install termux-boot
$ mkdir -p ~/.termux/boot
$ ln -sf ~/aurora/scripts/aurora-boot.sh ~/.termux/boot/aurora
```

`termux-boot` запускає скрипт лише після реального перезавантаження
пристрою, а не після закриття застосунку.

---

## Панель керування

`http://127.0.0.1:8420` — один вбудований HTML без зовнішніх ресурсів, тому
працює офлайн.

| Вкладка | Що робить |
|---------|-----------|
| **Статус** | сесія, час роботи, реальна ОП (`VmRSS`), ліміт пам'яті, goroutine, лічильники плагінів, стрічка подій |
| **Плагіни** | встановлення з git, старт/стоп/рестарт/видалення, pid, доставлені й **втрачені** події, помилки, підписки |
| **Вхід** | телефон → код → пароль 2FA, імпорт/експорт StringSession, вихід |
| **Чат** | надіслати повідомлення будь-кому за `@username`, номером, id або посиланням |
| **Логи** | живий SSE-потік, рівні, підписки, фільтр, автоскрол |
| **Налаштування** | ключі, проксі, ліміт пам'яті, рівень логів, read-only, пісочниця |

Токен обов'язковий і передається як `?token=` (одразу стає cookie) або
`Authorization: Bearer`. За замовчуванням панель слухає **лише loopback** —
щоб не виставити свій юзербот у локальну мережу.

---

## Плагіни

### Структура

```
~/.local/share/aurora/plugins/
└── myplug/
    ├── aurora.plugin.json   # маніфест: дозволи, ліміти, події, команди
    ├── main.py              # або main.go + зібраний бінарник
    └── ...
```

### Встановлення

```console
$ aurora plugin install https://github.com/me/aurora-plugin-myplug
```

Або просто скопіюйте каталог у `plugins/` і перезапустіть ядро.

### Швидкий приклад (Python, ~40 рядків)

```json
{
  "name": "ping",
  "runtime": {"command": "python3", "args": ["main.py"]},
  "events": ["message.new"],
  "commands": [{"name": "ping"}],
  "permissions": {"tg": ["send"]},
  "limits": {"memory_mb": 64}
}
```

```python
import json, sys

for line in sys.stdin:
    if not line.strip(): continue
    m = json.loads(line)
    method = m.get("method")

    if method in ("plugin.hello", "plugin.load"):
        print(json.dumps({"jsonrpc": "2.0", "id": m["id"], "result": {"ok": True}}), flush=True)

    elif method == "event":
        ev = m.get("params", {})
        if ev["name"] == "message.new" and ev["data"]["text"] == "/ping":
            print(json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tg.send",
                              "params": {"peer": "me", "text": "pong"}}), flush=True)

    elif method == "command":
        print(json.dumps({"jsonrpc": "2.0", "id": m["id"],
                          "result": {"text": "pong"}}), flush=True)
```

Детальніше — **[docs/PLUGIN_API.md](docs/PLUGIN_API.md)** та
**[docs/PROTOCOL.md](docs/PROTOCOL.md)**.

Готові приклади:

| Плагін | Мова | Що робить |
|--------|------|-----------|
| [`plugins/hello`](plugins/hello) | Go + офіційний SDK | шаблон із життєвим циклом, командою й лічильником |
| [`plugins/echo`](plugins/echo) | Python | відповідає на `/echo` у приватних чатах |
| [`plugins/pulse`](plugins/pulse) | Lua | моніторинг + тост у панель, з власним JSON-кодеком |
| [`plugins/autoaway`](plugins/autoaway) | Node.js | автостатус після тиші |

### Дозволи й ізоляція

Маніфест оголошує можливості, а ядро їх перевіряє **на кожен виклик** —
бо плагін це недовірений процес, і тільки поведінка ядра є справжньою
межею:

```jsonc
"permissions": {
  "tg": ["send", "read"],   // або ["*"]
  "net": false,             // без цього http.request → -32001
  "config": false,          // доступ до конфігурації
  "env": []                 // дозволені змінні оточення
},
"limits": {
  "memory_mb": 64,          // RLIMIT_AS
  "cpu_seconds": 0,
  "file_mb": 16,            // RLIMIT_FSIZE
  "idle_timeout_sec": 900
}
```

Плюс до того ядро обрізає оточення (`HOME` плагіна — його власний каталог),
ставить окрему process group, `PDEATHSIG=SIGKILL`, `RLIMIT_CORE=0`.
Справжню файлову ізоляцію на Android дає лише `proot` — і маніфест це
підтримує через `runtime.wrap`.

---

## Команди CLI

```console
$ aurora run                          # ядро: Telegram + плагіни + панель
$ aurora login                        # інтерактивний вхід
$ aurora panel                        # надрукувати посилання з токеном
$ aurora send @durov "привіт"         # надіслати
$ aurora plugins                      # список
$ aurora plugin start|stop|restart <ім'я>
$ aurora plugin install <git-url>
$ aurora plugin remove <ім'я>
$ aurora session                      # інформація про сесію
$ aurora session export               # StringSession для Telethon/Pyrogram
$ aurora session import "1BVts…"      # імпорт без SMS
$ aurora config                       # конфіг + перевірка
$ aurora doctor                       # перевірка оточення
```

---

## Як влаштовано ядро

```
cmd/aurora           CLI
internal/
  tgc/               MTProto: з'єднання, вхід, peer-резолв, конвертація апдейтів
  plugins/           маніфести, process-менеджер, ліміти, host-API, пермішенни
  ipc/               newline-delimited JSON-RPC 2.0
  web/               HTTP-панель + SSE + вбудований SPA
  kv/                мікро-сховище (JSON у пам'яті + debounced flush)
  config/            конфіг із суворою валідацією
  logx/              кільцевий логгер, ніколи не блокує
  paths/             розкладка каталогів, Termux-aware
  proto/             спільні типи для плагінів і панелі
sdk/go/              офіційний Go SDK (stdlib-only, окремий модуль)
plugins/             приклади на Go / Python / Lua / Node
```

Кілька рішень, які варто знати:

* **Апдейти не роблять IO.** Під час обробки `tg.Updates` ми лише
  оновлюємо кеш імен і конвертуємо в `proto.Message`. Жодного
  `ResolvePeer` у гарячому шляху — інакше бот задихався б від власних
  повідомлень.
* **Черга подій не блокує.** Кожен плагін має обмежену чергу (512) і
  ліміт 200 подій/сек. Переповнення не зупиняє прийом апдейтів, а
  рахується в `events_dropped` — видно в панелі.
* **Авторестарт із backoff.** Плагін, що впав, піднімається до 5 разів
  з експоненційною паузою, потім паркується. Ядро не перезапускає сам
  себе — Telegram сам прийде в норму.
* **Секретність.** `app_hash` і токен панелі ніколи не віддаються в API
  (маскуються), сесія лежить у `~/.local/share/aurora/data/session.json`
  з правами `0600`, а `RLIMIT_CORE=0` не дає плагіну завалити сховище
  дампом пам'яті.

---

## Розробка

```console
$ make build        # бінарник
$ make test         # тести, включно з інтеграційним тестом плагіна
$ make lint         # gofmt + go vet
$ make cross        # крос-збірка android/arm64, android/arm, linux/*
$ make release      # крос-збірка + архіви
$ make help         # список цілей
```

Тести перевіряють не тільки юніти: `internal/plugins/host_test.go`
**компілює справжній плагін-виконуваний файл і піднімає його**, потім
перевіряє handshake, маршрутизацію команд, доставку подій, round-trip
host-API та відмову за відсутності дозволу. Тобто протокол тестується
наскрізь, а не в папері.

## Ліцензія

MIT. Див. [LICENSE](LICENSE).

## Застереження

Юзербот — це неофіційний клієнт Telegram. Автоматизація, спам і масові
розсилки порушують умови користування. Тримайте свої плагіни в межах
особистого використання, не масового, і не зловживайте чужими акаунтами.
