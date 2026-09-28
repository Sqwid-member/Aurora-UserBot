#!/bin/bash
# install-local.sh — Локальне встановлення Aurora UserBot з поточного каталогу в Termux/Linux
set -euo pipefail

say()  { printf "\033[35m▚▚▚\033[0m %s\n" "$*"; }
warn() { printf "\033[33m⚠\033[0m  %s\n" "$*" >&2; }
die()  { printf "\033[31m✖\033[0m  %s\n" "$*" >&2; exit 1; }

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

if [ -n "${PREFIX:-}" ] && [ "${PREFIX}" = "/data/data/com.termux/files/usr" ]; then
  IS_TERMUX=1
  DEFAULT_BIN_DIR="${PREFIX}/bin"
else
  IS_TERMUX=0
  DEFAULT_BIN_DIR="$HOME/bin"
fi

BIN_DIR="${AURORA_BIN_DIR:-$DEFAULT_BIN_DIR}"
DATA_DIR="${AURORA_HOME:-$HOME/.local/share/aurora}"

say "Локальне встановлення Aurora UserBot"
printf "     каталог джерела:   %s\n" "$ROOT_DIR"
printf "     каталог бінарника: %s\n" "$BIN_DIR"
printf "     каталог даних:     %s\n" "$DATA_DIR"
[ "$IS_TERMUX" = "1" ] && printf "     оточення:          Termux\n"

mkdir -p "$BIN_DIR" "$DATA_DIR"/{etc,data,plugins,logs,cache,run}
chmod 700 "$DATA_DIR" "$DATA_DIR"/{etc,data,plugins,logs,cache,run} 2>/dev/null || true

# 1. Збирання бінарника
if [ -f "$BIN_DIR/aurora" ] && [ "${AURORA_REBUILD:-0}" != "1" ]; then
  say "Знайдено існуючий бінарник у $BIN_DIR/aurora"
else
  ARCH="$(uname -m)"
  PREBUILT=""
  if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
    if [ -f "$ROOT_DIR/dist/aurora-linux-arm64" ]; then
      PREBUILT="$ROOT_DIR/dist/aurora-linux-arm64"
    elif [ -f "$ROOT_DIR/aurora-linux-arm64" ]; then
      PREBUILT="$ROOT_DIR/aurora-linux-arm64"
    fi
  elif [ "$ARCH" = "x86_64" ] || [ "$ARCH" = "amd64" ]; then
    if [ -f "$ROOT_DIR/dist/aurora-linux-amd64" ]; then
      PREBUILT="$ROOT_DIR/dist/aurora-linux-amd64"
    elif [ -f "$ROOT_DIR/aurora-linux-amd64" ]; then
      PREBUILT="$ROOT_DIR/aurora-linux-amd64"
    fi
  fi

  if [ -n "$PREBUILT" ] && [ -f "$PREBUILT" ]; then
    say "Використовую готовий скомпільований бінарник ($ARCH): $PREBUILT"
    cp "$PREBUILT" "$BIN_DIR/aurora"
    chmod 755 "$BIN_DIR/aurora"
    say "Бінарник успішно встановлено в $BIN_DIR/aurora"
  else
    if ! command -v go >/dev/null 2>&1; then
      if [ "$IS_TERMUX" = "1" ]; then
        say "Встановлюю Go компілятор у Termux..."
        pkg install -y golang
      else
        die "Для збирання потрібен Go: встановіть Go або скористайтесь готовим бінарником"
      fi
    fi

    say "Компіляція статичного бінарника (CGO=0)..."
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "v0.2.0-termux")"
    COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo "local")"
    LDFLAGS="-s -w -X github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo.Version=${VERSION} -X github.com/Sqwid-member/Aurora-UserBot/internal/buildinfo.Commit=${COMMIT}"

    CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$BIN_DIR/aurora" ./cmd/aurora
    chmod 755 "$BIN_DIR/aurora"
    say "Бінарник успішно зібрано: $BIN_DIR/aurora"
  fi
fi

# 2. Копіювання прикладів плагінів
if [ -d "$ROOT_DIR/plugins" ]; then
  for d in echo pulse autoaway hello; do
    if [ -d "$ROOT_DIR/plugins/$d" ] && [ ! -d "$DATA_DIR/plugins/$d" ]; then
      cp -r "$ROOT_DIR/plugins/$d" "$DATA_DIR/plugins/$d" 2>/dev/null || true
    fi
  done
  say "Приклади плагінів скопійовано у $DATA_DIR/plugins"
fi

# 3. Перевірка PATH
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    warn "Додайте у ~/.bashrc:"
    printf "\n    export PATH=\"%s:\$PATH\"\n\n" "$BIN_DIR"
    ;;
esac

echo
say "Готово! $("$BIN_DIR/aurora" version 2>/dev/null || echo "Aurora UserBot")"
printf "\n  Швидке використання в Termux:\n\n"
printf "    1. Авторизація в Telegram (стандартні ключі за замовчуванням):\n"
printf "         aurora login       # вхід у терміналі (номер -> код -> пароль)\n"
printf "         aurora login web   # вхід через веб-панель у браузері\n\n"
printf "    2. Робота з юзерботом:\n"
printf "         aurora start       # запуск у фоні (не вимикається при згортанні)\n"
printf "         aurora run         # запуск у відкритому терміналі\n"
printf "         aurora status      # перевірка активності та пам'яті (RAM)\n"
printf "         aurora logs        # живий журнал логів\n"
printf "         aurora stop        # зупинка процесу\n"
printf "         aurora panel       # адреса веб-панелі\n\n"
printf "    3. Мультиакаунтинг:\n"
printf "         У веб-панелі натисніть на профіль вгорі справа -> \"+ Додати акаунт\".\n"
printf "         Для кожного акаунта можна окремо вмикати конкретні плагіни!\n\n"

if [ -t 0 ] && [ -t 1 ]; then
  if [ ! -f "$DATA_DIR/data/session.json" ]; then
    printf "\n\033[36m🌌 Оберіть спосіб входу в Telegram:\033[0m\n"
    printf "  1) \033[32mУ терміналі\033[0m прямо зараз (номер -> код -> пароль)\n"
    printf "  2) \033[33mУ веб-панелі\033[0m через браузер (відкриється автоматично)\n"
    printf "  3) Пропустити (увійти пізніше)\n"
    printf "Ваш вибір [1/2/3, за замовчуванням 1]: "
    read -r choice
    case "${choice:-1}" in
      1) "$BIN_DIR/aurora" login ;;
      2) "$BIN_DIR/aurora" login web ;;
      *)
        say "Ви можете увійти пізніше:"
        printf "    aurora login       # вхід у терміналі\n"
        printf "    aurora login web   # вхід через браузер\n"
        printf "    aurora start       # запуск фонової служби\n"
        ;;
    esac
  fi
fi
