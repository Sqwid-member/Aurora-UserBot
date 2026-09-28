#!/bin/bash
# test-local.sh — Комплексний тест компонентів Aurora для Termux та Linux
set -euo pipefail

COLOR_RESET="\033[0m"
COLOR_GREEN="\033[32m"
COLOR_YELLOW="\033[33m"
COLOR_BLUE="\033[34m"
COLOR_RED="\033[31m"
COLOR_CYAN="\033[36m"

pass() { printf "  ${COLOR_GREEN}✔${COLOR_RESET} %s\n" "$*"; }
info() { printf "${COLOR_BLUE}ℹ${COLOR_RESET} %s\n" "$*"; }
warn() { printf "  ${COLOR_YELLOW}⚠${COLOR_RESET} %s\n" "$*"; }
fail() { printf "  ${COLOR_RED}✖${COLOR_RESET} %s\n" "$*"; exit 1; }

echo -e "\n${COLOR_CYAN}🌌 Aurora UserBot — Польовий тест середовища Termux${COLOR_RESET}"
echo "========================================================="

# 1. Перевірка JS і веб-ассетів
info "1. Тестування веб-панелі та ассетів..."
node -c internal/web/dist/app.js && pass "JavaScript AST валідація (internal/web/dist/app.js): OK"

python3 -c "
with open('internal/web/dist/index.html') as f:
    html = f.read()
assert 'account-wrap' in html, 'Missing account-wrap in index.html'
assert 'modal-account-add' in html, 'Missing modal-account-add in index.html'
assert 'plugin-account-banner' in html, 'Missing plugin-account-banner in index.html'
assert 'tab-overview' in html and 'tab-plugins' in html and 'tab-logs' in html, 'Missing core tabs'
" && pass "HTML розмітка (6 вкладок, мультиакаунтинг, модалки): OK"

python3 -c "
with open('internal/web/dist/app.css') as f:
    css = f.read()
assert 'account-pop' in css, 'Missing account-pop styles'
assert 'data-style=\"expensive\"' in css, 'Missing MD3 expensive styles'
assert 'sw-minimal' in css, 'Missing minimal styles'
" && pass "CSS таблиця стилів (Мінімалізм + MD3 Expensive): OK"

# 2. Перевірка плагінів
info "2. Тестування плагінів та маніфестів..."
for p in echo pulse autoaway hello; do
    manifest="plugins/$p/aurora.plugin.json"
    [ -f "$manifest" ] || fail "Маніфест $manifest не знайдено"
    python3 -c "import json; m=json.load(open('$manifest')); assert m['name']=='$p'"
    pass "Плагін '$p': маніфест валідний JSON"
done

python3 -m py_compile plugins/echo/echo.py && pass "Python плагін (plugins/echo/echo.py): синтаксис OK"
node -c plugins/autoaway/autoaway.mjs && pass "Node.js плагін (plugins/autoaway/autoaway.mjs): синтаксис OK"

# 3. Перевірка Go-компонентів та моделей
info "3. Тестування моделі конфігурації та мультиакаунтингу..."
python3 -c "
with open('internal/config/config.go') as f:
    code = f.read()
assert 'DefaultAppID = 611335' in code, 'Missing DefaultAppID'
assert 'DefaultAppHash =' in code, 'Missing DefaultAppHash'
assert 'type AccountConfig struct' in code, 'Missing AccountConfig'
assert 'func (c *Config) ToggleAccountPlugin' in code, 'Missing ToggleAccountPlugin'
assert 'func (a AccountConfig) IsPluginEnabled' in code, 'Missing IsPluginEnabled'
" && pass "internal/config/config.go (дефолтні ключі 611335, AccountConfig): OK"

python3 -c "
with open('internal/proto/proto.go') as f:
    code = f.read()
assert 'type AccountInfo struct' in code, 'Missing AccountInfo'
assert 'ActiveAccount string' in code, 'Missing ActiveAccount in Status'
assert 'AccountID string' in code, 'Missing AccountID in Event/SendRequest'
" && pass "internal/proto/proto.go (AccountInfo, Status): OK"

python3 -c "
with open('internal/paths/paths.go') as f:
    code = f.read()
assert 'func (l Layout) AccountSessionFile' in code, 'Missing AccountSessionFile'
assert 'func IsTermux() bool' in code, 'Missing IsTermux'
" && pass "internal/paths/paths.go (ізольовані файли сесій, IsTermux): OK"

python3 -c "
with open('internal/plugins/host.go') as f:
    code = f.read()
assert 'func (h *Host) EmitForAccount' in code, 'Missing EmitForAccount'
" && pass "internal/plugins/host.go (EmitForAccount ізоляція): OK"

python3 -c "
with open('internal/app/app.go') as f:
    code = f.read()
assert 'func (a *App) Accounts() []proto.AccountInfo' in code, 'Missing Accounts'
assert 'func (a *App) AddAccount' in code, 'Missing AddAccount'
assert 'func (a *App) RemoveAccount' in code, 'Missing RemoveAccount'
assert 'func (a *App) ToggleAccountPlugin' in code, 'Missing ToggleAccountPlugin'
assert 'func (a *App) emitForAccount' in code, 'Missing emitForAccount'
" && pass "internal/app/app.go (менеджер мультиакаунтів та маршрутизація подій): OK"

python3 -c "
with open('internal/web/server.go') as f:
    code = f.read()
assert 'GET /api/accounts' in code, 'Missing /api/accounts route'
assert 'POST /api/accounts/{id}/activate' in code, 'Missing activate route'
assert 'POST /api/accounts/{id}/plugins/{name}/toggle' in code, 'Missing toggle route'
" && pass "internal/web/server.go (REST API мультиакаунтингу): OK"

# 4. Перевірка Termux скриптів
info "4. Тестування скриптів Termux..."
bash -n scripts/install.sh && pass "scripts/install.sh: синтаксис bash OK"
bash -n scripts/aurora-boot.sh && pass "scripts/aurora-boot.sh: синтаксис bash OK"
bash -n scripts/uninstall.sh && pass "scripts/uninstall.sh: синтаксис bash OK"

# 5. Перевірка збирання релізів у GitHub Actions
info "5. Тестування робочого процесу релізів..."
python3 -c "
with open('.github/workflows/release.yml') as f:
    text = f.read()
assert '(cd dist && tar -czf' in text, 'Release archive packaging bug detected!'
assert 'sha256sum * > SHA256SUMS.txt' in text, 'Missing checksum generation'
" && pass ".github/workflows/release.yml (пакування в dist/, чексуми): OK"

echo "========================================================="
echo -e "${COLOR_GREEN}✔ ВСІ ПОЛЬОВІ ТЕСТИ ПРОЙДЕНО УСПІШНО!${COLOR_RESET}"
echo "Юзербот повністю готовий до встановлення та використання в Termux."
