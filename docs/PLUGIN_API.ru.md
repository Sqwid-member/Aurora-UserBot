> **Language / Мова / Язык:** [English](PLUGIN_API.md) • [Українська](PLUGIN_API.uk.md) • **Русский**

# API плагинов Aurora — Практическое руководство

Подробное описание протокола находится в [`PROTOCOL.ru.md`](PROTOCOL.ru.md).
Здесь описано, как быстро начать разработку плагина.

## Минимальный плагин (Python)

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

Манифест рядом `aurora.plugin.json`:

```json
{
  "name": "minimal",
  "runtime": {"command": "python3", "args": ["main.py"]},
  "events": ["core.start"],
  "permissions": {"tg": ["send"]}
}
```

Ядро Aurora возьмет на себя запуск процесса, IPC и доставку событий.

## Go SDK

В Aurora входит официальный легковесный Go SDK в директории `sdk/go/aurora`:

```go
package main

import (
	"context"
	"strings"

	"github.com/Sqwid-member/Aurora-UserBot/sdk/go/aurora"
)

func main() {
	p := aurora.New()

	p.OnCommand("ping", func(ctx context.Context, cmd aurora.Command) error {
		return cmd.Reply(ctx, "pong 🏓")
	})

	p.OnEvent("message.new", func(ctx context.Context, ev aurora.Event) error {
		if strings.HasPrefix(ev.Text(), "!echo ") {
			text := strings.TrimPrefix(ev.Text(), "!echo ")
			return ev.Reply(ctx, text)
		}
		return nil
	})

	if err := p.Run(); err != nil {
		panic(err)
	}
}
```

## Node.js

```javascript
#!/usr/bin/env node
import readline from 'node:readline';

const rl = readline.createInterface({ input: process.stdin });

rl.on('line', (line) => {
  if (!line.trim()) return;
  const msg = JSON.parse(line);
  
  if (msg.method === 'plugin.hello') {
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: msg.id, result: { ok: true } }) + '\n');
  } else if (msg.method === 'plugin.load') {
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: msg.id, result: { ok: true } }) + '\n');
  }
});
```

## Настройки плагинов

Плагины могут описывать схему настроек, отображаемую в веб-панели Aurora:

```json
{
  "settings": [
    {
      "key": "enabled",
      "type": "bool",
      "title": "Включено",
      "default": true
    },
    {
      "key": "prefix",
      "type": "text",
      "title": "Префикс команд",
      "default": "."
    }
  ]
}
```

При сохранении настроек в веб-панели плагин получает уведомление `settings.changed`.
