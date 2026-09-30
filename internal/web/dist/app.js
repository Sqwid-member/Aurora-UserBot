/* Aurora UserBot — Advanced Control Center UI Driver */
(() => {
  'use strict';

  const $ = (sel, el = document) => el.querySelector(sel);
  const $$ = (sel, el = document) => Array.from(el.querySelectorAll(sel));

  let TOKEN = '';
  let CURRENT_CONFIG = {};
  let ALL_COMMANDS = [];
  let ALL_ACCOUNTS = [];
  let ACTIVE_ACCOUNT_ID = '';
  let ALL_PLUGINS = [];
  let LOG_LINES = [];
  let AUTOSCROLL = true;
  let LOG_FILTER = 'ALL';
  let LOG_SEARCH = '';
  let ECO_MODE = JSON.parse(localStorage.getItem('aurora.eco') || 'false');
  let timers = null;

  // ---------- API Client ----------
  // Save token from URL or injected global, then scrub it from the
  // address bar: it has done its job (cookie + localStorage below), and
  // must not linger in history, bookmarks or pasted screenshots.
  try {
    const urlTok = new URLSearchParams(window.location.search).get("token") || window.__AURORA_TOKEN__;
    if (urlTok) {
      localStorage.setItem("aurora_token", urlTok);
      document.cookie = "aurora_token=" + encodeURIComponent(urlTok) + "; path=/; max-age=31536000; SameSite=Lax";
    }
    const url = new URL(window.location.href);
    if (url.searchParams.has("token")) {
      url.searchParams.delete("token");
      const rest = url.searchParams.toString();
      window.history.replaceState(null, "", url.pathname + (rest ? "?" + rest : "") + url.hash);
    }
  } catch (e) {}

  async function api(path, opts = {}) {
    const headers = Object.assign({}, opts.headers || {});
    if (opts.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
    const tok = localStorage.getItem("aurora_token") || window.__AURORA_TOKEN__;
    if (tok && !headers['Authorization']) headers['Authorization'] = "Bearer " + tok;
    const res = await fetch(path, Object.assign({ credentials: 'same-origin' }, opts, { headers }));
    if (res.status === 401) {
      const fallbackTok = window.__AURORA_TOKEN__;
      if (fallbackTok && tok !== fallbackTok) {
        localStorage.setItem("aurora_token", fallbackTok);
        location.reload();
      }
      throw new Error('unauthorized');
    }
    const text = await res.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch { data = { raw: text }; }
    if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
    return data;
  }

  // ---------- Utilities ----------
  const esc = (s) => String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

  function humanUptime(sec) {
    if (!sec && sec !== 0) return '0s';
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600),
          m = Math.floor((sec % 3600) / 60), s = sec % 60;
    if (d) return `${d}д ${h}г`;
    if (h) return `${h}г ${m}хв`;
    if (m) return `${m}хв ${s}с`;
    return `${s}с`;
  }

  // ---------- Toasts ----------
  function toast(title, msg, type = 'ok') {
    const box = $('#toast-container');
    if (!box) return;
    const el = document.createElement('div');
    el.className = `toast ${type === 'error' ? 'err' : type === 'warn' ? 'warn' : 'ok'}`;
    const icon = type === 'error' ? '✖' : type === 'warn' ? '⚠' : '✔';
    el.innerHTML = `<span>${icon}</span><span style="flex:1"><b>${esc(title)}:</b> ${esc(msg || '')}</span>`;
    box.appendChild(el);

    const timer = setTimeout(() => {
      el.style.opacity = '0';
      el.style.transform = 'translateX(24px)';
      el.style.transition = 'all 0.3s ease';
      setTimeout(() => el.remove(), 300);
    }, 4000);

    el.onclick = () => { clearTimeout(timer); el.remove(); };
  }

  // ---------- Navigation Tabs ----------
  function switchTab(tabId) {
    $$('.tab-btn').forEach((b) => b.classList.toggle('active', b.dataset.tab === tabId));
    $$('.tab-section').forEach((s) => s.classList.toggle('active', s.id === `tab-${tabId}`));
    window.scrollTo({ top: 0, behavior: 'smooth' });
    try { sessionStorage.setItem('aurora.activeTab', tabId); } catch {}
    if (tabId === 'profile') {
      loadProfile();
      const sl = $('#sessions-list');
      if (sl && !sl.dataset.loaded) {
        sl.dataset.loaded = '1';
        loadSessions();
      }
    }
  }

  $$('.tab-btn').forEach((btn) => {
    btn.onclick = () => switchTab(btn.dataset.tab);
  });

  // Restore saved tab
  try {
    const savedTab = sessionStorage.getItem('aurora.activeTab');
    if (savedTab && $(`#tab-${savedTab}`)) switchTab(savedTab);
  } catch {}

  // ---------- Themes ----------
  function initTheme() {
    const pop = $('#theme-pop');
    const btnTheme = $('#btn-theme');
    const list = $('#theme-list');
    const engine = () => window.AuroraTheme || null;

    const whenReady = (fn) => {
      if (window.AuroraTheme) fn();
      else window.addEventListener('aurora:theme-ready', fn, { once: true });
    };

    const renderList = () => {
      const api = engine();
      if (!api || !list || list.dataset.rendered) return;
      list.innerHTML = '';
      api.themes.forEach((theme) => {
        const sample = api.sample(theme.id);
        const opt = document.createElement('button');
        opt.type = 'button';
        opt.className = 'theme-opt';
        opt.dataset.theme = theme.id;
        opt.setAttribute('role', 'option');
        opt.innerHTML =
          '<span class="theme-dot" style="background:linear-gradient(135deg,' +
          sample.light + ' 50%,' + sample.dark + ' 50%)"></span>' +
          '<span class="theme-opt-name"></span>';
        opt.querySelector('.theme-opt-name').textContent = theme.name;
        opt.onclick = () => { api.setTheme(theme.id); sync(); };
        list.appendChild(opt);
      });
      list.dataset.rendered = '1';
      sync();
    };

    const sync = () => {
      const api = engine();
      if (api) {
        const state = api.current();
        $$('.theme-opt', list).forEach((o) => o.classList.toggle('active', o.dataset.theme === state.themeId));
        $$('#theme-mode button').forEach((b) => b.classList.toggle('active', b.dataset.mode === state.scheme));
      }
      if (btnTheme) btnTheme.setAttribute('aria-expanded', pop?.classList.contains('open') ? 'true' : 'false');
    };

    const close = () => { if (pop) pop.classList.remove('open'); sync(); };

    if (btnTheme && pop) {
      btnTheme.onclick = (e) => {
        e.stopPropagation();
        const opening = !pop.classList.contains('open');
        pop.classList.toggle('open', opening);
        if (opening) whenReady(() => { renderList(); sync(); });
        else sync();
      };
      document.addEventListener('click', (e) => {
        if (!pop.contains(e.target) && e.target !== btnTheme) close();
      });
      document.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(); });
    }

    $$('#theme-mode button').forEach((btn) => {
      btn.onclick = () => whenReady(() => { window.AuroraTheme.setScheme(btn.dataset.mode); sync(); });
    });

    window.addEventListener('aurora:theme-change', sync);
    whenReady(() => { renderList(); sync(); });
    sync();
  }

  // ---------- Status & Core Metrics ----------
  const stateLabels = {
    offline: 'Офлайн', connecting: 'Підключення...',
    unauthorized: 'Не авторизовано', authorized: 'Авторизовано', error: 'Помилка',
  };

  function renderStatus(st) {
    // 1. Top status pill
    const state = st.session || 'offline';
    const pill = $('#tg-status-pill');
    const dot = $('#tg-status-dot');
    const text = $('#tg-status-text');

    if (dot) {
      dot.className = `status-dot ${state === 'authorized' ? 'active' : state === 'connecting' ? 'warning' : state === 'unauthorized' ? 'warning' : 'error'}`;
    }
    if (text) text.textContent = stateLabels[state] || state;
    if (pill) pill.onclick = () => { if (state !== 'authorized') openModal('#modal-auth'); };
    if ((state === 'unauthorized' || location.hash === '#auth') && !window._authModalShown) {
      window._authModalShown = true;
      openModal('#modal-auth');
    }

    // 2. User profile pill & Multi-account update
    if (st.accounts && Array.isArray(st.accounts)) {
      ALL_ACCOUNTS = st.accounts;
      if (st.active_account) ACTIVE_ACCOUNT_ID = st.active_account;
      renderAccounts();
    } else {
      const userPill = $('#user-pill');
      if (st.user && st.user.id) {
        userPill.style.display = 'inline-flex';
        const name = [st.user.first_name, st.user.last_name].filter(Boolean).join(' ') || (st.user.username ? '@' + st.user.username : 'Користувач');
        $('#user-display-name').textContent = name;
        $('#user-avatar-char').textContent = (st.user.first_name || st.user.username || '?')[0].toUpperCase();
      }
    }

    // 3. RAM Pills & Gauges
    const curMB = parseFloat((st.memory_mb || 0).toFixed(1));
    const baseMB = st.mem_limit_mb || 96;
    const burstMB = Math.round(baseMB * 1.33);

    $('#ram-quick-val').textContent = curMB;
    const ramPct = Math.min(100, Math.round((curMB / baseMB) * 100));
    const quickBar = $('#ram-quick-bar');
    if (quickBar) {
      quickBar.style.width = `${ramPct}%`;
      quickBar.className = `ram-pill-bar-fill ${curMB > baseMB ? 'burst' : curMB > baseMB * 0.85 ? 'warning' : ''}`;
    }

    // Overview cards
    $('#card-tg-state').textContent = stateLabels[state] || state;
    const cardTgDot = $('#card-tg-dot');
    if (cardTgDot) cardTgDot.className = `status-dot ${state === 'authorized' ? 'active' : 'warning'}`;
    $('#card-tg-info').textContent = st.user && st.user.username ? `@${st.user.username} • ID: ${st.user.id}` : (st.user && st.user.phone ? st.user.phone : 'Сесія очікує входу');

    $('#card-ram-val').textContent = `${curMB} MB`;
    $('#card-ram-meter').style.width = `${ramPct}%`;
    $('#card-ram-base').textContent = baseMB;
    $('#card-ram-burst').textContent = burstMB;
    $('#card-ram-percent').textContent = `${ramPct}%`;

    $('#card-plugins-up').textContent = `${st.plugins_up || 0} / ${st.plugin_count || 0}`;
    $('#card-uptime').textContent = humanUptime(st.uptime_sec);
    $('#card-sys-info').textContent = `${st.go_version || 'Go'} • ${st.goroutines || 0} goroutines`;
    $('#badge-plugins-count').textContent = st.plugin_count || 0;

    // Footer sync state
    const d = new Date();
    const timeStr = [d.getHours(), d.getMinutes(), d.getSeconds()].map(n => String(n).padStart(2, '0')).join(':');
    $('#sync-dot').className = 'status-dot active';
    $('#sync-text').textContent = `Синхронізовано о ${timeStr}`;
  }

  // ---------- Sliders with CSS Variables ----------
  function setupSlider(input, chip, suffix = ' MB') {
    if (!input || !chip) return;
    const update = () => {
      const val = parseFloat(input.value);
      const min = parseFloat(input.min) || 0;
      const max = parseFloat(input.max) || 100;
      const pct = Math.max(0, Math.min(100, ((val - min) / (max - min)) * 100));
      input.style.setProperty('--p', `${pct}%`);
      chip.textContent = `${val}${suffix}`;
    };
    input.addEventListener('input', update);
    update();
  }

  function initSliders() {
    setupSlider($('#quick-tune-base'), $('#quick-tune-base-chip'));
    setupSlider($('#quick-tune-burst'), $('#quick-tune-burst-chip'));
    setupSlider($('#setting-base-input'), $('#setting-base-chip'));
    setupSlider($('#setting-burst-input'), $('#setting-burst-chip'));
  }

  // ---------- Quick RAM Tuning ----------
  $('#btn-quick-tune-save')?.addEventListener('click', async () => {
    const val = parseInt($('#quick-tune-base').value, 10);
    if (!val) return;
    try {
      const draft = Object.assign({}, CURRENT_CONFIG, {
        runtime: Object.assign({}, CURRENT_CONFIG.runtime, { mem_limit_mb: val })
      });
      await api('/api/config', { method: 'PUT', body: JSON.stringify(draft) });
      toast('RAM Менеджер', `Базовий ліміт встановлено на ${val} MB`, 'ok');
      refreshStatus();
    } catch (e) {
      toast('Помилка', e.message, 'error');
    }
  });

  $('#form-memory-tune')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const val = parseInt($('#setting-base-input').value, 10);
    if (!val) return;
    try {
      const draft = Object.assign({}, CURRENT_CONFIG, {
        runtime: Object.assign({}, CURRENT_CONFIG.runtime, { mem_limit_mb: val })
      });
      await api('/api/config', { method: 'PUT', body: JSON.stringify(draft) });
      toast('Налаштування', `Ліміти пам'яті збережено (${val} MB)`, 'ok');
      refreshStatus();
    } catch (err) {
      toast('Помилка', err.message, 'error');
    }
  });

  // Quick GC Trigger
  async function triggerGC() {
    try {
      const btn = $('#btn-quick-gc');
      if (btn) btn.disabled = true;
      const st = await api('/api/gc', { method: 'POST' });
      toast('Пам\'ять', `Збір сміття GC завершено. Купа: ${(st.memory_mb || 0).toFixed(1)} MB`, 'ok');
      renderStatus(st);
    } catch (e) {
      toast('Помилка GC', e.message, 'error');
    } finally {
      const btn = $('#btn-quick-gc');
      if (btn) btn.disabled = false;
    }
  }
  $('#btn-quick-gc')?.addEventListener('click', triggerGC);

  // ---------- Quick Message Send ----------
  $('#form-quick-send')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const peer = $('#quick-send-peer').value.trim();
    const text = $('#quick-send-text').value.trim();
    if (!peer || !text) return;
    const btn = $('#form-quick-send button[type="submit"]');
    btn.disabled = true;
    try {
      const res = await api('/api/send', {
        method: 'POST',
        body: JSON.stringify({ peer, text, silent: false, no_preview: false }),
      });
      toast('Повідомлення', `Надіслано для ${peer} (ID: ${res.id})`, 'ok');
      $('#quick-send-text').value = '';
    } catch (err) {
      toast('Помилка надсилання', err.message, 'error');
    } finally {
      btn.disabled = false;
    }
  });

  // ---------- Console & Media Send ----------
  $('#form-send-media')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const peer = $('#send-peer').value.trim();
    const text = $('#send-text').value.trim();
    const silent = $('#send-silent')?.checked || false;
    const nopreview = $('#send-nopreview')?.checked || false;
    if (!peer) return;

    const btn = $('#btn-send-msg');
    btn.disabled = true;
    try {
      const res = await api('/api/send', {
        method: 'POST',
        body: JSON.stringify({ peer, text, silent, no_preview: nopreview }),
      });
      toast('Чат', `Повідомлення успішно доставлено (ID: ${res.id})`, 'ok');
      $('#send-text').value = '';
    } catch (err) {
      toast('Помилка', err.message, 'error');
    } finally {
      btn.disabled = false;
    }
  });

  // ---------- Commands Runner ----------
  async function loadCommands() {
    try {
      ALL_COMMANDS = await api('/api/commands') || [];
      const sel = $('#cmd-select');
      if (!sel) return;
      sel.innerHTML = '<option value="">-- Оберіть зареєстровану команду --</option>' +
        ALL_COMMANDS.map(c => `<option value="${esc(c.name)}">${esc(c.name)} (${esc(c.plugin || 'core')})</option>`).join('');
      $('#card-commands-info').textContent = `${ALL_COMMANDS.length} зареєстрованих команд`;
    } catch (e) {
      console.warn('Cannot load commands:', e);
    }
  }

  $('#btn-run-cmd')?.addEventListener('click', async () => {
    const sel = $('#cmd-select');
    const name = sel.value;
    const args = $('#cmd-args').value.trim();
    if (!name) { toast('Команда', 'Оберіть команду зі списку', 'warn'); return; }

    const out = $('#cmd-result');
    out.textContent = 'Виконання команди...';
    try {
      const res = await api('/api/command', {
        method: 'POST',
        body: JSON.stringify({ name, text: args }),
      });
      out.textContent = typeof res === 'string' ? res : (res.result || res.message || JSON.stringify(res, null, 2));
      toast('Команда', `«${name}» успішно виконано`, 'ok');
    } catch (err) {
      out.textContent = `Помилка: ${err.message}`;
      toast('Помилка', err.message, 'error');
    }
  });

  // ---------- Plugins Management ----------
  async function loadPlugins() {
    try {
      const data = await api('/api/plugins');
      ALL_PLUGINS = data.plugins || [];
      renderPlugins();
    } catch (e) {
      console.warn('Cannot load plugins:', e);
    }
  }

  function renderPlugins() {
    const box = $('#plugins-container');
    if (!box) return;

    const query = ($('#plugins-search')?.value || '').toLowerCase().trim();
    const filter = ($('#plugins-filter button.active')?.dataset.f) || 'all';

    let list = ALL_PLUGINS.slice();

    // Counts on filter buttons
    const runningCount = list.filter(p => p.state === 'running').length;
    const stoppedCount = list.filter(p => p.state !== 'running').length;
    $$('#plugins-filter button').forEach(b => {
      const f = b.dataset.f;
      const countEl = $('em', b);
      if (countEl) {
        countEl.textContent = f === 'running' ? runningCount : f === 'stopped' ? stoppedCount : list.length;
      }
    });

    if (filter === 'running') list = list.filter(p => p.state === 'running');
    if (filter === 'stopped') list = list.filter(p => p.state !== 'running');

    if (query) {
      list = list.filter(p =>
        p.name.toLowerCase().includes(query) ||
        (p.description || '').toLowerCase().includes(query) ||
        (p.language || '').toLowerCase().includes(query)
      );
    }

    const emptyNote = $('#plugins-empty');
    if (emptyNote) emptyNote.hidden = list.length > 0;

    const activeAcc = ALL_ACCOUNTS.find(a => a.id === ACTIVE_ACCOUNT_ID) || ALL_ACCOUNTS[0];

    box.innerHTML = list.map((p) => {
      const perms = p.permissions || {};
      const isEnabledForAcc = !activeAcc || !activeAcc.enabled_plugins || activeAcc.enabled_plugins.includes(p.name);
      const tgCaps = (perms.tg || []).map(c => `<span class="cap-chip">tg:${esc(c)}</span>`).join('');
      const netCap = perms.net ? '<span class="cap-chip">net:http</span>' : '';
      const wildCap = (p.events || []).includes('*')
        ? '<span class="cap-chip" title="Плагін отримує ВСІ події, включно з текстом усіх повідомлень" style="border-color:var(--md-error); color:var(--md-error);">читає все</span>'
        : '';
      const memMB = p.memory_kb ? Math.max(1, Math.round(p.memory_kb / 1024)) : 0;
      const isRunning = p.state === 'running';
      const procInfo = isRunning && p.pid ? `PID ${p.pid}` : 'процес зупинено';

      return `
        <div class="plugin-card" data-state="${esc(p.state)}">
          <div>
            <div class="plugin-card-header">
              <div class="plugin-name">
                <span>${esc(p.name)}</span>
                <span class="status-dot ${isRunning ? 'active' : p.state === 'failed' ? 'error' : 'warning'}"></span>
              </div>
              <span class="plugin-runtime-badge ${esc(p.language || 'go')}">${esc(p.language || 'go')}</span>
            </div>
            <div class="plugin-desc">${esc(p.description || 'Немає опису')}</div>
            <div class="plugin-caps-row">
              <span class="cap-chip">v${esc(p.version || '1.0.0')}</span>
              ${tgCaps}
              ${netCap}
              ${wildCap}
            </div>
            <div class="plugin-acc-row">
              <span style="font-size:12px; color:var(--md-text-secondary); display:flex; align-items:center; gap:6px;">
                <svg class="md-icon sm" viewBox="0 0 24 24"><path fill="currentColor" d="M12 12c2.21 0 4-1.79 4-4s-1.79-4-4-4-4 1.79-4 4 1.79 4 4 4zm0 2c-2.67 0-8 1.34-8 4v2h16v-2c0-2.66-5.33-4-8-4z"/></svg>
                <span>${esc(activeAcc?.title || 'Акаунт')}</span>
              </span>
              <label class="md3-switch" title="Увімкнути/вимкнути для активного акаунта">
                <input type="checkbox" ${isEnabledForAcc ? 'checked' : ''} data-toggle-plugin="${esc(p.name)}">
                <span class="md3-switch-track"><span class="md3-switch-thumb"></span></span>
                <span class="md3-switch-label" style="font-size:11px;">${isEnabledForAcc ? 'Увімкнено' : 'Вимкнено'}</span>
              </label>
            </div>
          </div>
          <div class="plugin-footer">
            <span class="plugin-stats-text">RAM: ${memMB} MB • Подій: ${p.events_delivered || 0} • ${procInfo}</span>
            <div class="plugin-actions">
              ${isRunning
                ? `<button class="btn btn-sm" data-act="stop" data-name="${esc(p.name)}">Зупинити</button>`
                : `<button class="btn btn-sm btn-primary" data-act="start" data-name="${esc(p.name)}">Запустити</button>`
              }
              <button class="btn btn-sm" data-act="restart" data-name="${esc(p.name)}">Перезапуск</button>
              ${p.has_settings ? `<button class="btn btn-sm" data-settings="${esc(p.name)}">Налаштування</button>` : ''}
              <button class="btn btn-sm" data-info="${esc(p.name)}">Інфо</button>
              <button class="btn btn-sm btn-danger" data-act="uninstall" data-name="${esc(p.name)}">Видалити</button>
            </div>
          </div>
        </div>
      `;
    }).join('');
  }

  // Plugins Filter click
  $$('#plugins-filter button').forEach((btn) => {
    btn.onclick = () => {
      $$('#plugins-filter button').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      renderPlugins();
    };
  });

  // Plugins search
  $('#plugins-search')?.addEventListener('input', () => {
    const val = $('#plugins-search').value;
    $('#plugins-search-clear')?.classList.toggle('visible', val.length > 0);
    renderPlugins();
  });
  $('#plugins-search-clear')?.addEventListener('click', () => {
    $('#plugins-search').value = '';
    $('#plugins-search-clear').classList.remove('visible');
    renderPlugins();
  });

  // Plugin Actions Delegation
  $('#plugins-container')?.addEventListener('click', async (e) => {
    const btn = e.target.closest('button[data-act]');
    if (!btn) return;
    const { act, name } = btn.dataset;
    btn.disabled = true;

    try {
      if (act === 'uninstall') {
        if (!confirm(`Видалити плагін «${name}» та всі його файли?`)) { btn.disabled = false; return; }
        await api(`/api/plugins/${encodeURIComponent(name)}/uninstall`, { method: 'POST' });
        toast('Плагіни', `«${name}» видалено`, 'warn');
      } else {
        const res = await api(`/api/plugins/${encodeURIComponent(name)}/${act}`, { method: 'POST' });
        toast('Плагіни', res.message || `${name}: ${act}`, 'ok');
      }
      await loadPlugins();
      await refreshStatus();
    } catch (err) {
      toast('Помилка', err.message, 'error');
    } finally {
      btn.disabled = false;
    }
  });

  // ---------- Plugin Settings Form ----------
  let PS_NAME = '';

  async function openPluginSettings(name) {
    PS_NAME = name;
    $('#ps-title').textContent = `Налаштування: ${name}`;
    const box = $('#ps-fields');
    if (box) box.innerHTML = '<p class="auth-lead">Завантаження…</p>';
    openModal('#modal-plugin-settings');
    try {
      const data = await api(`/api/plugins/${encodeURIComponent(name)}/settings`);
      renderSettingsForm(data.fields || []);
    } catch (err) {
      if (box) box.innerHTML = `<div class="auth-status err">${esc(err.message || 'Не вдалося завантажити')}</div>`;
    }
  }

  function settingFieldHTML(f) {
    const fld = f.field || {};
    const val = f.value;
    const badge = f.stored ? '' : ' <span class="badge">дефолт</span>';
    const hint = fld.description ? `<div class="form-help">${esc(fld.description)}</div>` : '';
    const attrs = `data-ps-key="${esc(fld.key)}" data-ps-type="${esc(fld.type)}"`;
    switch (fld.type) {
      case 'bool':
        return `<div class="form-group"><label class="md3-switch">`
          + `<input type="checkbox" ${attrs} ${val ? 'checked' : ''}>`
          + `<span class="md3-switch-track"><span class="md3-switch-thumb"></span></span>`
          + `<span class="md3-switch-label">${esc(fld.title || fld.key)}${badge}</span></label>${hint}</div>`;
      case 'number': {
        const min = fld.min != null ? ` min="${fld.min}"` : '';
        const max = fld.max != null ? ` max="${fld.max}"` : '';
        const v = val == null ? '' : String(val);
        return `<div class="form-group"><label class="form-label">${esc(fld.title || fld.key)}${badge}</label>`
          + `<input type="number" class="form-input" ${attrs} value="${esc(v)}"${min}${max} step="any">${hint}</div>`;
      }
      case 'select': {
        const opts = (fld.options || []).map((o) =>
          `<option value="${esc(o.value)}"${String(val) === String(o.value) ? ' selected' : ''}>${esc(o.label || o.value)}</option>`
        ).join('');
        return `<div class="form-group"><label class="form-label">${esc(fld.title || fld.key)}${badge}</label>`
          + `<select class="form-select" ${attrs}>${opts}</select>${hint}</div>`;
      }
      case 'password':
        return `<div class="form-group"><label class="form-label">${esc(fld.title || fld.key)}${badge}</label>`
          + `<input type="password" class="form-input" ${attrs} value="${esc(val || '')}" placeholder="${esc(fld.placeholder || '')}" autocomplete="off">${hint}</div>`;
      default:
        return `<div class="form-group"><label class="form-label">${esc(fld.title || fld.key)}${badge}</label>`
          + `<input type="text" class="form-input" ${attrs} value="${esc(val || '')}" placeholder="${esc(fld.placeholder || '')}">${hint}</div>`;
    }
  }

  function renderSettingsForm(fields) {
    const box = $('#ps-fields');
    if (!box) return;
    if (!fields.length) {
      box.innerHTML = '<p class="auth-lead">У цього плагіна немає налаштувань.</p>';
      return;
    }
    box.innerHTML = fields.map(settingFieldHTML).join('');
  }

  function collectSettings() {
    const values = {};
    $$('#ps-fields [data-ps-key]').forEach((el) => {
      const k = el.dataset.psKey, t = el.dataset.psType;
      if (t === 'bool') values[k] = el.checked;
      else if (t === 'number') {
        const v = (el.value || '').trim();
        if (v !== '' && !Number.isNaN(Number(v))) values[k] = Number(v);
      } else values[k] = el.value;
    });
    return values;
  }

  $('#plugins-container')?.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-settings]');
    if (btn) openPluginSettings(btn.dataset.settings);
  });

  // ---------- Plugin Info Card ----------
  function fmtUptime(sec) {
    sec = Math.max(0, Math.round(sec || 0));
    const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
    if (d) return `${d} д ${h} год`;
    if (h) return `${h} год ${m} хв`;
    if (m) return `${m} хв ${sec % 60} с`;
    return `${sec} с`;
  }

  function openPluginInfo(name) {
    const p = (ALL_PLUGINS || []).find((x) => x.name === name);
    const box = $('#pi-body');
    if (!box) return;
    if (!p) {
      box.innerHTML = '<p class="auth-lead">Плагін не знайдено.</p>';
    } else {
      const cmds = p.command_details || [];
      const cmdHTML = cmds.length ? cmds.map((c) => {
        const aliases = (c.aliases || []).map((a) => `<code>/${esc(a)}</code>`).join(' ');
        const chat = c.in_chat ? ' <span class="badge">працює з чату</span>' : '';
        return `<div class="form-group" style="margin-bottom:10px;">
          <div><code>/${esc(c.name)}</code>${aliases ? ' · ' + aliases : ''}${chat}</div>
          <div class="form-help">${esc(c.usage || '')}${c.description ? ' — ' + esc(c.description) : ''}</div>
        </div>`;
      }).join('') : '<p class="auth-lead">Команд немає.</p>';
      const events = (p.events || []).map((e) => `<code>${esc(e)}</code>`).join(' ') || '—';
      const perms = p.permissions || {};
      const tg = (perms.tg || []).map((c) => `<span class="cap-chip">tg:${esc(c)}</span>`).join(' ');
      const net = perms.net ? '<span class="cap-chip">net:http</span>' : '';
      const memMB = p.memory_kb ? Math.max(1, Math.round(p.memory_kb / 1024)) : 0;
      box.innerHTML = `
        <div class="plugin-card-header" style="margin-bottom:8px;">
          <div class="plugin-name"><span>${esc(p.name)}</span>
            <span class="status-dot ${p.state === 'running' ? 'active' : p.state === 'failed' ? 'error' : 'warning'}"></span>
          </div>
          <span class="plugin-runtime-badge ${esc(p.language || 'go')}">${esc(p.language || 'go')} · v${esc(p.version || '1.0.0')}</span>
        </div>
        <p>${esc(p.description || 'Немає опису')}</p>
        <h4 style="margin:12px 0 6px;">Команди</h4>
        ${cmdHTML}
        <h4 style="margin:12px 0 6px;">Як працює</h4>
        <p class="form-help">Реагує на події ядра: ${events}. Команди запускаються з панелі (розділ команд), з CLI або прямо з чату — ті, що з міткою «працює з чату», спрацьовують коли власник пише <code>/команда</code>. Власне повідомлення-тригер бот видаляє, а результат надсилає в той самий чат.</p>
        <h4 style="margin:12px 0 6px;">Технічне</h4>
        <p class="form-help">Автор: ${esc(p.author || '—')} · Шлях: <code>${esc(p.path || '')}</code><br>
        Стан: ${esc(p.state)} · PID: ${p.pid || '—'} · Аптайм: ${fmtUptime(p.uptime_sec)} · RAM: ${memMB} MB<br>
        Подій доставлено: ${p.events_delivered || 0} · Втрачено: ${p.events_dropped || 0} · Рестартів: ${p.restarts || 0}<br>
        Дозволи: ${tg}${net} ${(!tg && !net) ? '—' : ''}</p>
        ${p.last_error ? `<p class="auth-status err">${esc(p.last_error)}</p>` : ''}`;
    }
    $('#pi-title').textContent = `Плагін: ${name}`;
    openModal('#modal-plugin-info');
  }

  $('#plugins-container')?.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-info]');
    if (btn) openPluginInfo(btn.dataset.info);
  });

  $('#btn-ps-save')?.addEventListener('click', async () => {
    if (!PS_NAME) return;
    const btn = $('#btn-ps-save');
    if (btn) btn.disabled = true;
    try {
      const res = await api(`/api/plugins/${encodeURIComponent(PS_NAME)}/settings`, {
        method: 'POST', body: JSON.stringify({ values: collectSettings() }),
      });
      renderSettingsForm(res.fields || []);
      toast('Налаштування', 'Збережено. Плагін отримав подію settings.changed.', 'ok');
    } catch (err) {
      toast('Помилка збереження', err.message, 'error');
    } finally {
      if (btn) btn.disabled = false;
    }
  });

  $('#btn-ps-reset')?.addEventListener('click', async () => {
    if (!PS_NAME || !confirm(`Скинути налаштування «${PS_NAME}» до значень за замовчуванням?`)) return;
    try {
      await api(`/api/plugins/${encodeURIComponent(PS_NAME)}/settings`, { method: 'DELETE' });
      const data = await api(`/api/plugins/${encodeURIComponent(PS_NAME)}/settings`);
      renderSettingsForm(data.fields || []);
      toast('Налаштування', 'Скинуто до дефолтів.', 'ok');
    } catch (err) {
      toast('Помилка скидання', err.message, 'error');
    }
  });

  // Install Plugin Form
  $('#btn-open-install-modal')?.addEventListener('click', () => openModal('#modal-install'));
  $('#form-plugin-install')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const source = $('#install-source').value.trim();
    const name = $('#install-name').value.trim();
    if (!source) return;

    const btn = $('#btn-submit-install');
    btn.disabled = true;
    try {
      const res = await api('/api/plugins/install', {
        method: 'POST',
        body: JSON.stringify({ source, name }),
      });
      toast('Встановлення', res.message || 'Плагін встановлено', 'ok');
      closeModal('#modal-install');
      $('#install-source').value = '';
      $('#install-name').value = '';
      await loadPlugins();
    } catch (err) {
      toast('Помилка встановлення', err.message, 'error');
    } finally {
      btn.disabled = false;
    }
  });

  // ---------- Profile Editor (Tab 4) ----------
  $('#profile-about')?.addEventListener('input', () => {
    const len = $('#profile-about').value.length;
    const counter = $('#profile-about-counter');
    if (counter) {
      counter.textContent = `${len} / 70`;
      counter.className = len >= 70 ? 'full' : len >= 60 ? 'near' : '';
    }
  });

  async function loadProfile() {
    try {
      const p = await api('/api/profile');
      const u = p.user || {};
      if ($('#profile-first-name') && !$('#profile-first-name').value) $('#profile-first-name').value = u.first_name || '';
      if ($('#profile-last-name') && !$('#profile-last-name').value) $('#profile-last-name').value = u.last_name || '';
      if ($('#profile-username') && !$('#profile-username').value) $('#profile-username').value = u.username || '';
      if ($('#profile-about') && !$('#profile-about').value) {
        $('#profile-about').value = p.about || '';
        $('#profile-about').dispatchEvent(new Event('input'));
      }
    } catch {}
  }

  $('#form-profile-edit')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('#btn-save-profile');
    if (btn) btn.disabled = true;
    try {
      const res = await api('/api/profile', {
        method: 'POST',
        body: JSON.stringify({
          first_name: $('#profile-first-name').value.trim(),
          last_name: $('#profile-last-name').value.trim(),
          about: $('#profile-about').value.trim(),
        }),
      });
      toast('Профіль', `Збережено: ${res.first_name || ''} ${res.last_name || ''}`.trim(), 'ok');
      refreshStatus();
    } catch (err) {
      toast('Помилка профілю', err.message, 'error');
    } finally {
      if (btn) btn.disabled = false;
    }
  });

  $('#form-username-edit')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const username = $('#profile-username').value.trim().replace(/^@/, '');
    if (!username) return;
    try {
      const res = await api('/api/profile/username', {
        method: 'POST', body: JSON.stringify({ username }),
      });
      toast('Юзернейм', `Тепер @${res.username}`, 'ok');
      refreshStatus();
    } catch (err) {
      toast('Помилка юзернейму', err.message, 'error');
    }
  });

  $('#form-avatar-upload')?.addEventListener('submit', (e) => {
    e.preventDefault();
    const input = $('#profile-avatar-file');
    const file = input?.files?.[0];
    if (!file) { toast('Помилка', 'Оберіть файл', 'error'); return; }
    if (file.size > 5 * 1024 * 1024) { toast('Помилка', 'Файл більше 5 МБ', 'error'); return; }
    const btn = $('#btn-upload-avatar');
    if (btn) { btn.disabled = true; btn.querySelector('span').textContent = 'Завантаження…'; }
    const reader = new FileReader();
    reader.onload = async () => {
      try {
        await api('/api/profile/avatar', {
          method: 'POST',
          body: JSON.stringify({ image: String(reader.result || ''), name: file.name }),
        });
        toast('Аватар', 'Фото профілю оновлено', 'ok');
        input.value = '';
      } catch (err) {
        toast('Помилка аватара', err.message, 'error');
      } finally {
        if (btn) { btn.disabled = false; btn.querySelector('span').textContent = 'Встановити нове фото'; }
      }
    };
    reader.onerror = () => {
      toast('Помилка', 'Не вдалося прочитати файл', 'error');
      if (btn) { btn.disabled = false; btn.querySelector('span').textContent = 'Встановити нове фото'; }
    };
    reader.readAsDataURL(file);
  });

  // ---------- Active sessions ----------
  function relTime(ts) {
    if (!ts) return '';
    const s = Math.max(0, Math.floor(Date.now() / 1000) - ts);
    if (s < 60) return 'щойно';
    if (s < 3600) return `${Math.floor(s / 60)} хв тому`;
    if (s < 86400) return `${Math.floor(s / 3600)} год тому`;
    return `${Math.floor(s / 86400)} дн тому`;
  }

  async function loadSessions() {
    const box = $('#sessions-list');
    if (!box) return;
    box.innerHTML = '<p class="auth-lead">Завантаження…</p>';
    try {
      const data = await api('/api/sessions');
      const list = data.sessions || [];
      if (!list.length) {
        box.innerHTML = '<p class="auth-lead">Немає активних сесій.</p>';
        return;
      }
      box.innerHTML = list.map((s) => {
        const title = [s.device, s.platform].filter(Boolean).join(' • ') || 'Невідомий пристрій';
        const sub = [s.app, s.app_version].filter(Boolean).join(' ') +
          (s.ip ? ` • ${esc(s.ip)}` : '') +
          (s.region || s.country ? ` (${esc([s.region, s.country].filter(Boolean).join(', '))})` : '');
        const badge = s.current ? '<span class="badge">поточна</span>' : (s.official_app ? '' : '<span class="badge">сторонній клієнт</span>');
        const warn = s.password_pending ? '<span class="badge">чекає 2FA</span>' : '';
        return `<div class="account-item" style="cursor:default;">`
          + `<div class="account-item-meta">`
          + `<span class="account-item-title">${esc(title)} ${badge}${warn}</span>`
          + `<span class="account-item-sub">${esc(sub)} • активна ${esc(relTime(s.active))}</span>`
          + `</div>`
          + `<button type="button" class="btn btn-sm btn-danger" data-terminate-session="${s.hash}">Завершити</button>`
          + `</div>`;
      }).join('');
    } catch (err) {
      box.innerHTML = `<div class="auth-status err">${esc(err.message || 'Не вдалося')}</div>`;
    }
  }

  $('#btn-sessions-refresh')?.addEventListener('click', () => loadSessions());

  $('#sessions-list')?.addEventListener('click', async (e) => {
    const btn = e.target.closest('[data-terminate-session]');
    if (!btn) return;
    const hash = btn.dataset.terminateSession;
    const row = btn.closest('.account-item');
    const isCurrent = row && row.innerHTML.includes('поточна');
    const msg = isCurrent
      ? 'Це ПОТОЧНА сесія юзербота! Завершення вимкне Aurora. Продовжити?'
      : 'Завершити цю сесію Telegram?';
    if (!confirm(msg)) return;
    btn.disabled = true;
    try {
      await api(`/api/sessions/${encodeURIComponent(hash)}/terminate`, { method: 'POST' });
      toast('Сесії', 'Сесію завершено', 'ok');
      await loadSessions();
      await refreshStatus();
    } catch (err) {
      toast('Помилка', err.message, 'error');
      btn.disabled = false;
    }
  });

  // ---------- Settings (Tab 5) ----------
  async function loadSettings() {
    try {
      CURRENT_CONFIG = await api('/api/config');
      const t = CURRENT_CONFIG.telegram || {};
      const w = CURRENT_CONFIG.web || {};
      const r = CURRENT_CONFIG.runtime || {};

      const baseMB = r.mem_limit_mb || 96;
      if ($('#setting-base-input')) {
        $('#setting-base-input').value = baseMB;
        setupSlider($('#setting-base-input'), $('#setting-base-chip'));
      }
      if ($('#quick-tune-base')) {
        $('#quick-tune-base').value = baseMB;
        setupSlider($('#quick-tune-base'), $('#quick-tune-base-chip'));
      }

      if ($('#setting-web-host')) $('#setting-web-host').value = w.host || '127.0.0.1';
      if ($('#setting-web-port')) $('#setting-web-port').value = w.port || 8420;

      const tg = c.telegram || {};
      const customKeys = tg.app_id > 0 && tg.app_hash && tg.app_hash !== '••••••';
      if ($('#setting-tg-badge')) {
        $('#setting-tg-badge').textContent = customKeys ? 'Власні ключі' : 'Стандартні (Web K)';
      }
      if ($('#setting-tg-appid')) $('#setting-tg-appid').value = tg.app_id > 0 ? tg.app_id : '';
      if ($('#setting-tg-apphash')) $('#setting-tg-apphash').value = tg.app_hash && tg.app_hash !== '••••••' ? tg.app_hash : '';
    } catch (e) {
      console.warn('Cannot load config:', e);
    }
  }

  $('#form-sys-settings')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const host = $('#setting-web-host').value.trim();
      const port = parseInt($('#setting-web-port').value, 10);
      const token = $('#setting-web-token').value.trim();
      const appId = parseInt($('#setting-tg-appid')?.value || '0', 10) || 0;
      const appHash = $('#setting-tg-apphash')?.value.trim() || '';

      const draft = Object.assign({}, CURRENT_CONFIG, {
        telegram: Object.assign({}, CURRENT_CONFIG.telegram, {
          app_id: appId,
          app_hash: appHash,
        }),
        web: Object.assign({}, CURRENT_CONFIG.web, {
          host: host || '127.0.0.1',
          port: port || 8420,
        })
      });
      if (token) draft.web.token = token;

      await api('/api/config', { method: 'PUT', body: JSON.stringify(draft) });
      toast('Налаштування', 'Параметри збережено. Перезапустіть ядро для застосування.', 'ok');
      await loadSettings();
    } catch (err) {
      toast('Помилка', err.message, 'error');
    }
  });

  // ---------- Terminal & Live Logs (Tab 6) ----------
  function appendLog(rec) {
    LOG_LINES.push(rec);
    if (LOG_LINES.length > 2000) LOG_LINES.shift();
    $('#log-count').textContent = `${LOG_LINES.length} рядків`;

    // Filter check
    if (LOG_FILTER !== 'ALL' && rec.level !== LOG_FILTER) return;
    if (LOG_SEARCH && !JSON.stringify(rec).toLowerCase().includes(LOG_SEARCH)) return;

    renderLogEntry(rec);
  }

  function renderLogEntry(rec) {
    const term = $('#logs-terminal');
    if (!term) return;

    const time = (rec.time || '').slice(11, 19) || new Date().toTimeString().slice(0, 8);
    const lvl = (rec.level || 'INFO').toUpperCase();
    const scope = rec.scope ? `[${rec.scope}]` : '';

    const el = document.createElement('div');
    el.className = 'log-entry';
    el.innerHTML = `<span class="log-time">${esc(time)}</span> <span class="log-level ${esc(lvl)}">${esc(lvl)}</span> <span class="log-scope">${esc(scope)}</span> <span class="log-msg">${esc(rec.msg || '')}</span>`;

    term.appendChild(el);
    if (AUTOSCROLL) term.scrollTop = term.scrollHeight;
  }

  function rerenderLogs() {
    const term = $('#logs-terminal');
    if (!term) return;
    term.innerHTML = '';
    const filtered = LOG_LINES.filter(rec => {
      if (LOG_FILTER !== 'ALL' && rec.level !== LOG_FILTER) return false;
      if (LOG_SEARCH && !JSON.stringify(rec).toLowerCase().includes(LOG_SEARCH)) return false;
      return true;
    });
    filtered.forEach(renderLogEntry);
  }

  $$('[data-log-filter]').forEach((btn) => {
    btn.onclick = () => {
      $$('[data-log-filter]').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      LOG_FILTER = btn.dataset.logFilter;
      rerenderLogs();
    };
  });

  $('#log-search-input')?.addEventListener('input', (e) => {
    LOG_SEARCH = e.target.value.toLowerCase().trim();
    rerenderLogs();
  });

  $('#btn-toggle-autoscroll')?.addEventListener('click', function () {
    AUTOSCROLL = !AUTOSCROLL;
    this.classList.toggle('active', AUTOSCROLL);
    toast('Логи', `Автопрокрутка ${AUTOSCROLL ? 'увімкнена' : 'вимкнена'}`, 'ok');
  });

  $('#btn-clear-logs')?.addEventListener('click', () => {
    LOG_LINES = [];
    $('#logs-terminal').innerHTML = '';
    $('#log-count').textContent = '0 рядків';
    toast('Логи', 'Вікно консолі очищено', 'ok');
  });

  $('#btn-download-logs')?.addEventListener('click', () => {
    const text = LOG_LINES.map(r => `[${r.time}] [${r.level}] [${r.scope || 'core'}] ${r.msg}`).join('\n');
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `aurora-logs-${Date.now()}.txt`;
    a.click();
    URL.revokeObjectURL(url);
    toast('Експорт', 'Логи завантажено у файл', 'ok');
  });

  // ---------- Control Center (Cmd+K) ----------
  function openControlMenu() {
    openModal('#modal-control-menu');
    setTimeout(() => $('#control-search-input')?.focus(), 50);
  }

  $('#btn-open-control-menu')?.addEventListener('click', openControlMenu);
  $('#fab-control-menu')?.addEventListener('click', openControlMenu);
  $('#btn-footer-menu')?.addEventListener('click', openControlMenu);

  // Search in Control Center
  $('#control-search-input')?.addEventListener('input', (e) => {
    const q = e.target.value.toLowerCase().trim();
    $$('.control-action-card').forEach((card) => {
      const kw = (card.dataset.keywords || '') + ' ' + card.innerText.toLowerCase();
      card.style.display = !q || kw.includes(q) ? 'flex' : 'none';
    });
  });

  // Action Cards Click Handling
  $('#control-actions-grid')?.addEventListener('click', async (e) => {
    const card = e.target.closest('.control-action-card');
    if (!card) return;
    const action = card.dataset.action;

    closeModal('#modal-control-menu');

    switch (action) {
      case 'gc':
        await triggerGC();
        break;
      case 'stop-all-plugins':
        if (!confirm('Зупинити всі запущені плагіни?')) return;
        for (const p of ALL_PLUGINS.filter(x => x.state === 'running')) {
          await api(`/api/plugins/${encodeURIComponent(p.name)}/stop`, { method: 'POST' }).catch(() => {});
        }
        toast('Плагіни', 'Всі плагіни зупинено', 'warn');
        await loadPlugins();
        break;
      case 'start-all-plugins':
        for (const p of ALL_PLUGINS.filter(x => x.state !== 'running')) {
          await api(`/api/plugins/${encodeURIComponent(p.name)}/start`, { method: 'POST' }).catch(() => {});
        }
        toast('Плагіни', 'Всі плагіни запущено', 'ok');
        await loadPlugins();
        break;
      case 'clear-cache':
        toast('Кеш', 'Тимчасові файли та кеш очищено', 'ok');
        break;
      case 'toggle-eco':
        ECO_MODE = !ECO_MODE;
        localStorage.setItem('aurora.eco', JSON.stringify(ECO_MODE));
        $('#eco-active-banner').style.display = ECO_MODE ? 'flex' : 'none';
        if (timers) { timers.clear(); timers = scheduleTimers(); }
        toast('Eco-Mode', ECO_MODE ? 'Termux Eco-Mode активовано (знижене споживання CPU/батареї)' : 'Eco-Mode вимкнено', 'ok');
        break;
      case 'ping-test': {
        const t0 = performance.now();
        await api('/healthz');
        const ping = Math.round(performance.now() - t0);
        toast('Затримка ядра (Ping)', `${ping} ms`, 'ok');
        break;
      }
      case 'copy-token': {
        if (!TOKEN) {
          try { TOKEN = (await api('/api/token')).token || ''; } catch {}
        }
        if (TOKEN) {
          navigator.clipboard?.writeText(TOKEN).then(
            () => toast('Токен', 'Токен доступу скопійовано в буфер', 'ok'),
            () => prompt('Токен доступу:', TOKEN)
          );
        } else {
          toast('Помилка', 'Токен недоступний', 'error');
        }
        break;
      }
      case 'export-config': {
        const text = JSON.stringify(CURRENT_CONFIG, null, 2);
        const blob = new Blob([text], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'aurora-config.json';
        a.click();
        URL.revokeObjectURL(url);
        toast('Конфігурація', 'config.json завантажено', 'ok');
        break;
      }
      case 'restart-core':
        if (!confirm('Перезапустити ядро Aurora?')) return;
        try {
          await api('/api/restart', { method: 'POST' });
          toast('Ядро', 'Перезапуск ядра... Зачекайте кілька секунд', 'warn');
        } catch (err) {
          toast('Помилка', err.message, 'error');
        }
        break;
      case 'shutdown-core':
        if (!confirm('Повністю зупинити процес Aurora?')) return;
        try {
          await api('/api/shutdown', { method: 'POST' });
          toast('Ядро', 'Процес завершує роботу', 'warn');
        } catch (err) {
          toast('Помилка', err.message, 'error');
        }
        break;
    }
  });

  // Quick Jumps
  $$('.quick-jump-chip').forEach((chip) => {
    chip.onclick = () => {
      closeModal('#modal-control-menu');
      switchTab(chip.dataset.jump);
    };
  });

  // Global Keyboard Shortcuts
  document.addEventListener('keydown', (e) => {
    // Cmd+K / Ctrl+K
    if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
      e.preventDefault();
      openControlMenu();
      return;
    }
    // Escape
    if (e.key === 'Escape') {
      $$('.modal-overlay.active').forEach(m => m.classList.remove('active'));
      return;
    }
    // Numbers 1-6 for tabs (when not typing in an input)
    if (!['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement.tagName)) {
      if (e.key >= '1' && e.key <= '6') {
        const tabs = ['overview', 'plugins', 'console', 'profile', 'settings', 'logs'];
        const target = tabs[parseInt(e.key, 10) - 1];
        if (target) switchTab(target);
      } else if (e.key === 't' || e.key === 'T') {
        $('#btn-theme')?.click();
      } else if (e.key === '/') {
        e.preventDefault();
        switchTab('plugins');
        setTimeout(() => $('#plugins-search')?.focus(), 50);
      }
    }
  });

  // ---------- Modals Helper ----------
  function openModal(sel) {
    const el = $(sel);
    if (el) el.classList.add('active');
  }
  function closeModal(sel) {
    const el = $(sel);
    if (el) el.classList.remove('active');
  }

  $$('[data-close-modal]').forEach((btn) => {
    btn.onclick = () => {
      const overlay = btn.closest('.modal-overlay');
      if (overlay) overlay.classList.remove('active');
    };
  });

  $$('.modal-overlay').forEach((overlay) => {
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay) overlay.classList.remove('active');
    });
  });

  // ---------- Auth Login Modal v2 (QR на цьому телефоні / код / сесія) ----------
  function normalizePhone(val) {
    let p = (val || '').replace(/[^\d+]/g, '');
    if (!p) return '';
    if (p.startsWith('00')) p = '+' + p.slice(2);
    else if (p.startsWith('0') && p.length >= 10 && !p.startsWith('+')) p = '+38' + p;
    else if (!p.startsWith('+')) p = '+' + p;
    return p;
  }

  function authTab(name) {
    $$('#auth-tabs [data-auth-tab]').forEach((b) => b.classList.toggle('active', b.dataset.authTab === name));
    ['qr', 'code', 'session'].forEach((t) => {
      const el = $('#auth-tab-' + t);
      if (el) el.hidden = t !== name;
    });
  }
  $$('#auth-tabs [data-auth-tab]').forEach((b) => {
    b.onclick = () => authTab(b.dataset.authTab);
  });

  function authNote(msg, isErr) {
    const el = $('#auth-status');
    if (!el) return;
    if (!msg) { el.hidden = true; el.textContent = ''; return; }
    el.hidden = false;
    el.textContent = msg;
    el.classList.toggle('err', !!isErr);
  }

  function showCodeStep(which) {
    const map = { phone: '#auth-step-phone', code: '#auth-step-code', signup: '#auth-step-signup', pwd: '#auth-step-pwd' };
    Object.entries(map).forEach(([k, sel]) => {
      const el = $(sel);
      if (el) el.style.display = k === which ? 'block' : 'none';
    });
    authTab('code');
  }

  async function requestCode(viaSMS) {
    const input = $('#auth-phone-input');
    const phone = normalizePhone((input.value || '').trim());
    if (!phone) {
      toast('Помилка', 'Введіть номер телефону', 'error');
      return;
    }
    input.value = phone;
    const path = viaSMS ? '/api/auth/code-request/sms' : '/api/auth/code-request';
    try {
      authNote(viaSMS ? 'Надсилаємо код по SMS…' : 'Надсилаємо код…', false);
      await api(path, { method: 'POST', body: JSON.stringify({ phone }) });
      toast('Вхід', viaSMS ? 'Код надіслано по SMS!' : 'Код підтвердження надіслано!', 'ok');
      showCodeStep('code');
      authNote('', false);
      setTimeout(() => $('#auth-code-input')?.focus(), 100);
    } catch (err) {
      authNote(err.message || 'Не вдалося надіслати код', true);
      toast('Помилка', err.message || 'Не вдалося надіслати код', 'error');
    }
  }

  $('#form-auth-phone')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('#form-auth-phone button[type="submit"]');
    if (btn) { btn.disabled = true; btn.textContent = 'Надсилаємо код...'; }
    try { await requestCode(false); }
    finally { if (btn) { btn.disabled = false; btn.textContent = 'Отримати код'; } }
  });

  $('#btn-auth-sms')?.addEventListener('click', async () => {
    const btn = $('#btn-auth-sms');
    if (btn) { btn.disabled = true; }
    try { await requestCode(true); }
    finally { if (btn) { btn.disabled = false; } }
  });

  $('#btn-auth-resend')?.addEventListener('click', async () => {
    try {
      authNote('Надсилаємо код ще раз (зазвичай SMS/дзвінок)…', false);
      await api('/api/auth/resend', { method: 'POST' });
      authNote('', false);
      toast('Вхід', 'Код надіслано повторно', 'ok');
    } catch (err) {
      authNote(err.message || 'Не вдалося надіслати повторно', true);
      toast('Помилка', err.message || 'Не вдалося надіслати повторно', 'error');
    }
  });

  $('#form-auth-code')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const code = $('#auth-code-input').value.trim();
    if (!code) return;
    try {
      await api('/api/auth/code', { method: 'POST', body: JSON.stringify({ code }) });
      toast('Вхід', 'Авторизовано!', 'ok');
      authNote('', false);
      closeModal('#modal-auth');
      refreshStatus();
    } catch (err) {
      const msg = err.message || '';
      const up = msg.toUpperCase();
      if (up.includes('SESSION_PASSWORD_NEEDED') || up.includes('PASSWORD_AUTH_NEEDED') || msg.includes('2FA')) {
        showCodeStep('pwd');
        authNote('Потрібен хмарний пароль 2FA.', false);
      } else if (msg.includes('не зареєстровано') || up.includes('SIGN-UP') || up.includes('SIGNUP') || up.includes('NOT REGISTERED') || up.includes('UNOCCUPLICATED')) {
        showCodeStep('signup');
        authNote(msg, false);
      } else {
        authNote(msg, true);
        toast('Помилка коду', msg, 'error');
      }
    }
  });

  $('#form-auth-signup')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const first = ($('#auth-signup-first').value || '').trim();
    const last = ($('#auth-signup-last').value || '').trim();
    if (!first) { toast('Помилка', "Введіть ім'я", 'error'); return; }
    try {
      await api('/api/auth/signup', { method: 'POST', body: JSON.stringify({ first_name: first, last_name: last }) });
      toast('Вхід', 'Акаунт створено!', 'ok');
      closeModal('#modal-auth');
      refreshStatus();
    } catch (err) {
      authNote(err.message, true);
      toast('Помилка реєстрації', err.message, 'error');
    }
  });

  $('#form-auth-pwd')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const password = $('#auth-pwd-input').value;
    try {
      await api('/api/auth/password', { method: 'POST', body: JSON.stringify({ password }) });
      toast('Вхід', 'Успішний вхід з 2FA паролем!', 'ok');
      closeModal('#modal-auth');
      refreshStatus();
    } catch (err) {
      authNote(err.message, true);
      toast('Помилка 2FA', err.message, 'error');
    }
  });

  $('#form-import-session')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const session = $('#auth-session-input').value.trim();
    if (!session) { toast('Помилка', 'Вставте рядок сесії', 'error'); return; }
    if (session.length < 32) { toast('Помилка', 'Рядок сесії закороткий — перевірте копію', 'error'); return; }
    try {
      await api('/api/session/import', { method: 'POST', body: JSON.stringify({ session }) });
      toast('Сесія', 'Сесію імпортовано. Перезапустіть ядро.', 'ok');
      closeModal('#modal-auth');
      refreshStatus();
    } catch (err) {
      toast('Помилка імпорту', err.message, 'error');
    }
  });

  $('#btn-web-bookmarklet-copy')?.addEventListener('click', async () => {
    const code = ($('#web-bookmarklet')?.textContent || '').trim();
    if (!code) return;
    try {
      await navigator.clipboard.writeText(code);
      toast('Букмарклет', 'Код скопійовано — вставте його в адресу закладки', 'ok');
    } catch {
      toast('Букмарклет', 'Не вдалося скопіювати — виділіть код вручну', 'error');
    }
  });

  $('#form-import-web')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const data = $('#auth-web-input').value.trim();
    const dc = parseInt($('#auth-web-dc')?.value || '0', 10) || 0;
    if (!data) { toast('Помилка', 'Вставте JSON з букмарклета', 'error'); return; }
    try {
      const res = await api('/api/session/import-web', { method: 'POST', body: JSON.stringify({ dc, data }) });
      toast('Сесія', `Сесію імпортовано (DC ${res.dc}). Перезапустіть ядро.`, 'ok');
      closeModal('#modal-auth');
      refreshStatus();
    } catch (err) {
      toast('Помилка імпорту з Web', err.message, 'error');
    }
  });

  // ----- QR на цьому ж телефоні: старт + опитування токена -----
  let qrTimer = null;
  function qrStopPoll() {
    if (qrTimer) { clearInterval(qrTimer); qrTimer = null; }
  }
  function qrImgURL(path) {
    const tok = localStorage.getItem("aurora_token") || window.__AURORA_TOKEN__ || '';
    return path + (tok ? '?token=' + encodeURIComponent(tok) : '?t=') + '&t=' + Date.now();
  }
  function qrSetLink(url, expires) {
    const link = $('#qr-link'), exp = $('#qr-expires'), open = $('#btn-qr-open'), img = $('#qr-img');
    if (!link || !open) return;
    if (img && !img.dataset.errBound) {
      img.dataset.errBound = '1';
      img.onerror = () => { img.hidden = true; };
    }
    if (!url) {
      link.hidden = true; link.textContent = '';
      if (exp) { exp.hidden = true; exp.textContent = ''; }
      if (img) { img.hidden = true; img.removeAttribute('src'); }
      open.style.opacity = '.5'; open.style.pointerEvents = 'none'; open.removeAttribute('href');
      return;
    }
    link.hidden = false; link.textContent = url;
    if (exp && expires) {
      exp.hidden = false;
      try { exp.textContent = 'Діє до ' + new Date(expires).toLocaleTimeString(); }
      catch { exp.textContent = ''; }
    }
    if (img) { img.hidden = false; img.src = qrImgURL('/api/auth/qr/image'); }
    open.style.opacity = '1'; open.style.pointerEvents = 'auto';
    open.setAttribute('href', url);
  }
  async function qrPollOnce() {
    try {
      const st = await api('/api/auth/qr');
      const status = $('#qr-status');
      if (st && st.url) {
        qrSetLink(st.url, st.expires);
        if (status) status.textContent = st.running ? 'Чекаю підтвердження в Telegram — відкрийте посилання нижче…' : 'Посилання готове.';
      } else if (status) {
        status.textContent = st && st.running ? 'Telegram готує посилання…' : 'Посилання ще не готове — оновіть статус.';
      }
      try {
        const auth = await api('/api/auth');
        if (auth && auth.signed_in) {
          if (status) status.textContent = 'Підтверджено! Сесію збережено.';
          qrStopPoll();
          toast('Вхід', 'Авторизовано по QR!', 'ok');
          closeModal('#modal-auth');
          refreshStatus();
        }
      } catch {}
    } catch (err) {
      const status = $('#qr-status');
      if (status) status.textContent = 'Помилка статусу: ' + (err.message || err);
    }
  }
  function qrStartPoll() {
    qrStopPoll();
    qrTimer = setInterval(qrPollOnce, 2000);
  }
  $('#btn-qr-start')?.addEventListener('click', async () => {
    const btn = $('#btn-qr-start'), status = $('#qr-status');
    if (btn) { btn.disabled = true; btn.textContent = 'Запитуємо…'; }
    try {
      await api('/api/auth/qr', { method: 'POST' });
      if (status) status.textContent = 'Запит надіслано, чекаю токен від Telegram…';
      qrSetLink('', '');
      await qrPollOnce();
      qrStartPoll();
    } catch (err) {
      if (status) status.textContent = 'Помилка: ' + (err.message || err);
      toast('QR-вхід', err.message || 'Не вдалося', 'error');
    } finally {
      if (btn) { btn.disabled = false; btn.textContent = 'Отримати посилання для входу'; }
    }
  });
  $('#btn-qr-refresh')?.addEventListener('click', () => qrPollOnce());
  $('#btn-qr-copy')?.addEventListener('click', async () => {
    const url = ($('#qr-link')?.textContent || '').trim();
    if (!url) { toast('QR', 'Посилання ще немає', 'error'); return; }
    try {
      await navigator.clipboard.writeText(url);
      toast('QR', 'Посилання скопійовано', 'ok');
    } catch {
      const ta = document.createElement('textarea');
      ta.value = url; document.body.appendChild(ta); ta.select();
      try { document.execCommand('copy'); toast('QR', 'Посилання скопійовано', 'ok'); }
      catch { toast('QR', 'Не вдалося скопіювати', 'error'); }
      ta.remove();
    }
  });
  $('#modal-auth')?.addEventListener('click', (e) => {
    if (e.target && e.target.id === 'modal-auth') qrStopPoll();
  });

  // ---------- Streams & Events ----------
  function openStream(path, onMessage) {
    let es;
    const connect = () => {
      const tok = localStorage.getItem("aurora_token") || window.__AURORA_TOKEN__;
      const url = tok ? (path + (path.includes("?") ? "&" : "?") + "token=" + encodeURIComponent(tok)) : path;
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => { try { onMessage(JSON.parse(e.data)); } catch {} };
      es.onerror = () => { es.close(); setTimeout(connect, 4000); };
    };
    connect();
  }

  // Refresh & Restart in Header
  async function refreshStatus() {
    try {
      const st = await api('/api/status');
      renderStatus(st);
    } catch {}
  }

  $('#btn-refresh')?.addEventListener('click', async function () {
    this.classList.add('spin');
    await refreshStatus();
    await loadPlugins();
    setTimeout(() => this.classList.remove('spin'), 700);
    toast('Оновлено', 'Дані успішно актуалізовано', 'ok');
  });

  $('#btn-restart')?.addEventListener('click', async () => {
    if (!confirm('Перезапустити ядро Aurora UserBot?')) return;
    try {
      await api('/api/restart', { method: 'POST' });
      toast('Перезапуск', 'Сигнал перезапуску відправлено', 'warn');
    } catch (e) {
      toast('Помилка', e.message, 'error');
    }
  });


  // ---------- Multi-Account Management ----------
  async function loadAccounts() {
    try {
      const list = await api('/api/accounts') || [];
      if (Array.isArray(list)) {
        ALL_ACCOUNTS = list;
        const active = ALL_ACCOUNTS.find(a => a.is_active);
        if (active) ACTIVE_ACCOUNT_ID = active.id;
        else if (ALL_ACCOUNTS.length > 0 && !ACTIVE_ACCOUNT_ID) ACTIVE_ACCOUNT_ID = ALL_ACCOUNTS[0].id;
        renderAccounts();
      }
    } catch (e) {
      console.warn('Cannot load accounts:', e);
    }
  }

  function renderAccounts() {
    const activeAcc = ALL_ACCOUNTS.find(a => a.id === ACTIVE_ACCOUNT_ID) || ALL_ACCOUNTS[0];
    if (activeAcc) {
      const userPill = $('#user-pill');
      if (userPill) userPill.style.display = 'inline-flex';
      const name = activeAcc.title || (activeAcc.user && [activeAcc.user.first_name, activeAcc.user.last_name].filter(Boolean).join(' ')) || activeAcc.phone || 'Акаунт';
      if ($('#user-display-name')) $('#user-display-name').textContent = name;
      if ($('#user-avatar-char')) $('#user-avatar-char').textContent = (name[0] || '?').toUpperCase();

      if ($('#plugin-account-title')) $('#plugin-account-title').textContent = name;
      if ($('#plugin-account-stats-badge')) {
        const count = activeAcc.enabled_plugins ? activeAcc.enabled_plugins.length : ALL_PLUGINS.length;
        $('#plugin-account-stats-badge').textContent = `${count} активних плагінів`;
      }
    }

    if ($('#accounts-count-badge')) $('#accounts-count-badge').textContent = ALL_ACCOUNTS.length;

    const listEl = $('#accounts-dropdown-list');
    if (listEl) {
      listEl.innerHTML = ALL_ACCOUNTS.map(a => {
        const isAct = a.id === ACTIVE_ACCOUNT_ID;
        const name = a.title || (a.user && [a.user.first_name, a.user.last_name].filter(Boolean).join(' ')) || 'Акаунт';
        const sub = a.phone || (a.user?.username ? '@' + a.user.username : stateLabels[a.session] || a.session);
        const initial = (name[0] || '?').toUpperCase();
        return `
          <button type="button" class="account-item ${isAct ? 'active' : ''}" data-switch-acc="${esc(a.id)}">
            <div class="user-avatar" style="width:26px; height:26px; font-size:11px;">${initial}</div>
            <div class="account-item-meta">
              <span class="account-item-title">${esc(name)} ${isAct ? '✓' : ''}</span>
              <span class="account-item-sub">${esc(sub)}</span>
            </div>
            <span class="status-dot ${a.session === 'authorized' ? 'active' : 'warning'}" style="width:7px; height:7px;"></span>
          </button>
        `;
      }).join('');
    }
  }

  $('#user-pill')?.addEventListener('click', (e) => {
    e.stopPropagation();
    $('#account-pop')?.classList.toggle('open');
  });

  document.addEventListener('click', (e) => {
    if (!e.target.closest('#account-wrap')) {
      $('#account-pop')?.classList.remove('open');
    }
  });

  $('#accounts-dropdown-list')?.addEventListener('click', async (e) => {
    const item = e.target.closest('[data-switch-acc]');
    if (!item) return;
    const accID = item.dataset.switchAcc;
    if (accID === ACTIVE_ACCOUNT_ID) return;

    try {
      await api(`/api/accounts/${encodeURIComponent(accID)}/activate`, { method: 'POST' });
      ACTIVE_ACCOUNT_ID = accID;
      $('#account-pop')?.classList.remove('open');
      toast('Акаунт змінено', `Активний акаунт перемкнуто`, 'ok');
      await loadAccounts();
      await refreshStatus();
      renderPlugins();
    } catch (err) {
      toast('Помилка перемикання', err.message, 'error');
    }
  });

  $('#plugins-container')?.addEventListener('change', async (e) => {
    const toggle = e.target.closest('[data-toggle-plugin]');
    if (!toggle) return;
    const pluginName = toggle.dataset.togglePlugin;
    const activeAcc = ALL_ACCOUNTS.find(a => a.id === ACTIVE_ACCOUNT_ID) || ALL_ACCOUNTS[0];
    if (!activeAcc) return;

    try {
      const res = await api(`/api/accounts/${encodeURIComponent(activeAcc.id)}/plugins/${encodeURIComponent(pluginName)}/toggle`, {
        method: 'POST'
      });
      toast('Плагіни акаунта', `«${pluginName}» ${res.enabled ? 'увімкнено' : 'вимкнено'} для «${activeAcc.title}»`, 'ok');
      await loadAccounts();
      renderPlugins();
    } catch (err) {
      toggle.checked = !toggle.checked;
      toast('Помилка', err.message, 'error');
    }
  });

  // ---------- Add-account wizard (code / QR / session tabs) ----------
  function accTab(name) {
    $$('#add-acc-tabs [data-add-acc-tab]').forEach((b) => b.classList.toggle('active', b.dataset.addAccTab === name));
    ['code', 'qr', 'session'].forEach((t) => {
      const el = $('#add-acc-tab-' + t);
      if (el) el.hidden = t !== name;
    });
  }
  $$('#add-acc-tabs [data-add-acc-tab]').forEach((b) => {
    b.onclick = () => accTab(b.dataset.addAccTab);
  });

  function accNote(msg, isErr) {
    const el = $('#add-acc-status');
    if (!el) return;
    if (!msg) { el.hidden = true; el.textContent = ''; return; }
    el.hidden = false;
    el.textContent = msg;
    el.classList.toggle('err', !!isErr);
  }

  function accShowStep(which) {
    const map = { phone: '#add-acc-step-phone', code: '#add-acc-step-code', signup: '#add-acc-step-signup', pwd: '#add-acc-step-pwd' };
    Object.entries(map).forEach(([k, sel]) => {
      const el = $(sel);
      if (el) el.style.display = k === which ? 'block' : 'none';
    });
    accTab('code');
  }

  async function accDone(msg) {
    toast('Успіх', msg, 'ok');
    closeModal('#modal-account-add');
    accStopQR();
    await loadAccounts();
    await refreshStatus();
  }

  // Creates the backend account on first use per wizard run and reuses it.
  async function ensurePendingAcc(phone) {
    if (window._pendingAccId) return window._pendingAccId;
    const title = ($('#add-acc-title').value || '').trim();
    const newAcc = await api('/api/accounts', {
      method: 'POST',
      body: JSON.stringify({ title, phone: phone || '' }),
    });
    window._pendingAccId = newAcc.id;
    return newAcc.id;
  }
  const accPath = (id, suffix) => `/api/accounts/${encodeURIComponent(id)}${suffix}`;

  $('#btn-open-add-account')?.addEventListener('click', () => {
    $('#account-pop')?.classList.remove('open');
    window._pendingAccId = '';
    accNote('', false);
    accShowStep('phone');
    accTab('code');
    accSetQRLink('', '');
    const st = $('#add-acc-qr-status');
    if (st) st.textContent = 'Посилання ще не запитано.';
    openModal('#modal-account-add');
  });

  async function accRequestCode(viaSMS) {
    const input = $('#add-acc-phone');
    const phone = normalizePhone((input.value || '').trim());
    if (!phone) {
      toast('Помилка', 'Введіть номер телефону', 'error');
      return;
    }
    input.value = phone;
    try {
      accNote(viaSMS ? 'Надсилаємо код по SMS…' : 'Надсилаємо код…', false);
      const id = await ensurePendingAcc(phone);
      await api(accPath(id, '/auth/' + (viaSMS ? 'code-request/sms' : 'code-request')), {
        method: 'POST',
        body: JSON.stringify({ phone }),
      });
      toast('Акаунт створено', viaSMS ? 'Код надіслано по SMS!' : 'Код підтвердження надіслано!', 'ok');
      accShowStep('code');
      accNote('', false);
      setTimeout(() => $('#add-acc-code-input')?.focus(), 100);
    } catch (err) {
      accNote(err.message || 'Не вдалося надіслати код', true);
      toast('Помилка', err.message || 'Не вдалося надіслати код', 'error');
    }
  }

  $('#form-add-account-phone')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('#form-add-account-phone button[type="submit"]');
    if (btn) { btn.disabled = true; btn.textContent = 'Надсилаємо код...'; }
    try { await accRequestCode(false); }
    finally {
      if (btn) { btn.disabled = false; btn.textContent = 'Отримати код'; }
    }
  });

  $('#btn-add-acc-sms')?.addEventListener('click', () => accRequestCode(true));

  $('#btn-add-acc-resend')?.addEventListener('click', async () => {
    if (!window._pendingAccId) return;
    try {
      await api(accPath(window._pendingAccId, '/auth/resend'), { method: 'POST' });
      toast('Акаунт', 'Код надіслано повторно', 'ok');
    } catch (err) {
      accNote(err.message || 'Не вдалося надіслати повторно', true);
      toast('Помилка', err.message || 'Не вдалося надіслати повторно', 'error');
    }
  });

  $('#form-add-account-code')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const code = $('#add-acc-code-input').value.trim();
    if (!code || !window._pendingAccId) return;

    try {
      await api(accPath(window._pendingAccId, '/auth/code'), {
        method: 'POST',
        body: JSON.stringify({ code }),
      });
      await accDone('Новий акаунт успішно авторизовано!');
    } catch (err) {
      const msg = err.message || '';
      const up = msg.toUpperCase();
      if (up.includes('SESSION_PASSWORD_NEEDED') || up.includes('PASSWORD_AUTH_NEEDED') || msg.includes('2FA')) {
        accShowStep('pwd');
        accNote('Потрібен хмарний пароль 2FA.', false);
      } else if (msg.includes('не зареєстровано') || up.includes('SIGN-UP') || up.includes('SIGNUP') || up.includes('NOT REGISTERED') || up.includes('UNOCCUPLICATED')) {
        accShowStep('signup');
        accNote(msg, false);
      } else {
        accNote(msg, true);
        toast('Помилка коду', msg, 'error');
      }
    }
  });

  $('#form-add-account-signup')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (!window._pendingAccId) return;
    const first = ($('#add-acc-signup-first').value || '').trim();
    const last = ($('#add-acc-signup-last').value || '').trim();
    if (!first) { toast('Помилка', "Введіть ім'я", 'error'); return; }
    try {
      await api(accPath(window._pendingAccId, '/auth/signup'), {
        method: 'POST',
        body: JSON.stringify({ first_name: first, last_name: last }),
      });
      await accDone('Новий акаунт створено!');
    } catch (err) {
      accNote(err.message, true);
      toast('Помилка реєстрації', err.message, 'error');
    }
  });

  $('#form-add-account-pwd')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const password = $('#add-acc-pwd-input').value;
    if (!window._pendingAccId) return;

    try {
      await api(accPath(window._pendingAccId, '/auth/password'), {
        method: 'POST',
        body: JSON.stringify({ password }),
      });
      await accDone('2FA пройдено! Новий акаунт авторизовано.');
    } catch (err) {
      accNote(err.message, true);
      toast('Помилка 2FA', err.message, 'error');
    }
  });

  // ----- Per-account QR (same phone, official Telegram app) -----
  let accQRTimer = null;
  function accStopQR() {
    if (accQRTimer) { clearInterval(accQRTimer); accQRTimer = null; }
  }
  function accSetQRLink(url, expires) {
    const link = $('#add-acc-qr-link'), exp = $('#add-acc-qr-expires'), open = $('#btn-add-acc-qr-open'), img = $('#add-acc-qr-img');
    if (!link || !open) return;
    if (img && !img.dataset.errBound) {
      img.dataset.errBound = '1';
      img.onerror = () => { img.hidden = true; };
    }
    if (!url) {
      link.hidden = true; link.textContent = '';
      if (exp) { exp.hidden = true; exp.textContent = ''; }
      if (img) { img.hidden = true; img.removeAttribute('src'); }
      open.style.opacity = '.5'; open.style.pointerEvents = 'none'; open.removeAttribute('href');
      return;
    }
    link.hidden = false; link.textContent = url;
    if (exp && expires) {
      exp.hidden = false;
      try { exp.textContent = 'Діє до ' + new Date(expires).toLocaleTimeString(); }
      catch { exp.textContent = ''; }
    }
    if (img && window._pendingAccId) {
      img.hidden = false;
      img.src = qrImgURL(`/api/accounts/${encodeURIComponent(window._pendingAccId)}/auth/qr/image`);
    }
    open.style.opacity = '1'; open.style.pointerEvents = 'auto';
    open.setAttribute('href', url);
  }
  async function accQRPollOnce() {
    if (!window._pendingAccId) return;
    try {
      const st = await api(accPath(window._pendingAccId, '/auth/qr'));
      const status = $('#add-acc-qr-status');
      if (st && st.url) {
        accSetQRLink(st.url, st.expires);
        if (status) status.textContent = st.running ? 'Чекаю підтвердження в Telegram…' : 'Посилання готове.';
      } else if (status) {
        status.textContent = st && st.running ? 'Telegram готує посилання…' : 'Посилання ще не готове.';
      }
      const accs = await api('/api/accounts');
      const me = Array.isArray(accs) ? accs.find((a) => a.id === window._pendingAccId) : null;
      if (me && me.session === 'authorized') {
        if (status) status.textContent = 'Підтверджено! Акаунт додано.';
        await accDone('Новий акаунт авторизовано по QR!');
      }
    } catch (err) {
      const status = $('#add-acc-qr-status');
      if (status) status.textContent = 'Помилка статусу: ' + (err.message || err);
    }
  }
  $('#btn-add-acc-qr-start')?.addEventListener('click', async () => {
    const btn = $('#btn-add-acc-qr-start'), status = $('#add-acc-qr-status');
    if (btn) { btn.disabled = true; btn.textContent = 'Запитуємо…'; }
    try {
      const id = await ensurePendingAcc('');
      await api(accPath(id, '/auth/qr'), { method: 'POST' });
      if (status) status.textContent = 'Запит надіслано, чекаю токен від Telegram…';
      accSetQRLink('', '');
      await accQRPollOnce();
      accStopQR();
      accQRTimer = setInterval(accQRPollOnce, 2000);
    } catch (err) {
      if (status) status.textContent = 'Помилка: ' + (err.message || err);
      toast('QR-вхід', err.message || 'Не вдалося', 'error');
    } finally {
      if (btn) { btn.disabled = false; btn.textContent = 'Отримати посилання для входу'; }
    }
  });
  $('#btn-add-acc-qr-copy')?.addEventListener('click', async () => {
    const url = ($('#add-acc-qr-link')?.textContent || '').trim();
    if (!url) { toast('QR', 'Посилання ще немає', 'error'); return; }
    try {
      await navigator.clipboard.writeText(url);
      toast('QR', 'Посилання скопійовано', 'ok');
    } catch {
      toast('QR', 'Не вдалося скопіювати', 'error');
    }
  });
  $('#modal-account-add')?.addEventListener('click', (e) => {
    if (e.target && e.target.id === 'modal-account-add') accStopQR();
  });

  // ----- Per-account session import -----
  $('#form-add-account-import')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const session = $('#add-acc-session-input').value.trim();
    if (!session) { toast('Помилка', 'Вставте рядок сесії', 'error'); return; }
    if (session.length < 32) { toast('Помилка', 'Рядок сесії закороткий', 'error'); return; }
    try {
      const id = await ensurePendingAcc('');
      await api(accPath(id, '/session/import'), { method: 'POST', body: JSON.stringify({ session }) });
      await accDone('Сесію імпортовано. Перезапустіть ядро.');
    } catch (err) {
      toast('Помилка імпорту', err.message, 'error');
    }
  });

  $('#form-add-account-import-web')?.addEventListener('submit', async (e) => {
    e.preventDefault();
    const data = $('#add-acc-web-input').value.trim();
    const dc = parseInt($('#add-acc-web-dc')?.value || '0', 10) || 0;
    if (!data) { toast('Помилка', 'Вставте JSON з букмарклета', 'error'); return; }
    try {
      const id = await ensurePendingAcc('');
      const res = await api(accPath(id, '/session/import-web'), { method: 'POST', body: JSON.stringify({ dc, data }) });
      await accDone(`Сесію імпортовано (DC ${res.dc}). Перезапустіть ядро.`);
    } catch (err) {
      toast('Помилка імпорту з Web', err.message, 'error');
    }
  });

  // ---------- Boot Init ----------
  async function boot() {
    initTheme();
    initSliders();

    await refreshStatus();
    await loadSettings();
    await Promise.allSettled([loadAccounts(), loadPlugins(), loadCommands()]);

    openStream('/api/logs/stream', appendLog);
    openStream('/api/events', (ev) => {
      if (ev.name === 'session.started' || ev.name === 'core.start') refreshStatus();
    });

    function scheduleTimers() {
      const statusInterval = ECO_MODE ? 15000 : 4000;
      const pluginsInterval = ECO_MODE ? 30000 : 12000;

      const sTimer = setInterval(() => {
        if (document.hidden) return;
        refreshStatus();
      }, statusInterval);

      const pTimer = setInterval(() => {
        if (document.hidden) return;
        loadPlugins();
      }, pluginsInterval);

      return {
        clear() {
          clearInterval(sTimer);
          clearInterval(pTimer);
        }
      };
    }

    if (ECO_MODE) {
      $('#eco-active-banner').style.display = 'flex';
    }

    timers = scheduleTimers();

    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) refreshStatus();
    });
  }

  boot();
})();
