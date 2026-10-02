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
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');

  // Єдиний форматер тривалості (короткий для карток, довгий для деталей).
  // relTime() лишається окремо — у нього інша семантика («N хв тому»).
  function humanUptime(sec, long = false) {
    if (!sec && sec !== 0) return long ? '0 с' : '0s';
    sec = Math.max(0, Math.round(sec));
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600),
          m = Math.floor((sec % 3600) / 60), s = sec % 60;
    if (long) {
      if (d) return `${d} д ${h} год`;
      if (h) return `${h} год ${m} хв`;
      if (m) return `${m} хв ${s} с`;
      return `${s} с`;
    }
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
    if (tabId === 'settings') loadSettings();
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
        if (opening) {
          const btnRect = btnTheme.getBoundingClientRect();
          if (btnRect.left < 280) {
            pop.style.right = 'auto';
            pop.style.left = '0';
            pop.style.transformOrigin = 'top left';
          } else {
            pop.style.right = '0';
            pop.style.left = 'auto';
            pop.style.transformOrigin = 'top right';
          }
          whenReady(() => { renderList(); sync(); });
        } else sync();
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

  
  let LAST_STATUS = null;
  function updateRamDisplay() {
    if (!LAST_STATUS) return;
    const sysMB = parseFloat((LAST_STATUS.memory_mb || 0).toFixed(1));
    const pluginsMB = parseFloat(((ALL_PLUGINS || []).reduce((acc, p) => acc + ((p.memory_kb || 0) / 1024), 0)).toFixed(1));
    const totalMB = parseFloat((sysMB + pluginsMB).toFixed(1));
    const baseMB = LAST_STATUS.mem_limit_mb || 96;

    const valEl = $("#ram-quick-val");
    if (valEl) valEl.textContent = totalMB;

    const pill = $("#ram-pill");
    if (pill) {
      pill.title = `Використання RAM: ${totalMB} MB загалом (система: ${sysMB} MB, плагіни: ${pluginsMB} MB)`;
    }

    const detailEl = $("#ram-quick-detail");
    if (detailEl) {
      detailEl.textContent = `(ядро ${sysMB} + плаг. ${pluginsMB})`;
    }

    const ramPct = Math.min(100, Math.round((totalMB / baseMB) * 100));
    const quickBar = $("#ram-quick-bar");
    if (quickBar) {
      quickBar.style.width = `${ramPct}%`;
      quickBar.className = `ram-pill-bar-fill ${totalMB > baseMB ? "burst" : totalMB > baseMB * 0.85 ? "warning" : ""}`;
    }
  }

  function renderStatus(st) {
    const setText = (sel, val) => { const el = $(sel); if (el) el.textContent = val; };
    // 1. Top status pill
    const state = st.session || 'offline';
    const pill = $('#tg-status-pill');
    const dot = $('#tg-status-dot');
    const text = $('#tg-status-text');

    if (dot) {
      dot.className = `status-dot ${state === 'authorized' ? 'active' : state === 'connecting' ? 'warning' : state === 'unauthorized' ? 'warning' : 'error'}`;
    }
    if (text) text.textContent = stateLabels[state] || state;
    if (pill) pill.onclick = () => { if (state !== 'authorized') openAddAccountModal(); };
    if ((state === 'unauthorized' || location.hash === '#auth') && !window._authModalShown) {
      window._authModalShown = true;
      openAddAccountModal();
    }

    // 2. User profile pill & Multi-account update
    if (st.accounts && Array.isArray(st.accounts)) {
      ALL_ACCOUNTS = st.accounts;
      if (st.active_account) ACTIVE_ACCOUNT_ID = st.active_account;
      renderAccounts();
    } else {
      const userPill = $('#user-pill');
      if (st.user && st.user.id) {
        if (userPill) userPill.style.display = 'inline-flex';
        const name = [st.user.first_name, st.user.last_name].filter(Boolean).join(' ') || (st.user.username ? '@' + st.user.username : 'Користувач');
        setText('#user-display-name', name);
        const av = $('#user-avatar-char');
        if (av) av.textContent = (st.user.first_name || st.user.username || '?')[0].toUpperCase();
      }
    }

    // 3. RAM Pills & Gauges (System + Plugins Total)
    LAST_STATUS = st;
    updateRamDisplay();

    // Card values stay honest: backend exposes only runtime.mem_limit_mb —
    // there is no burst ceiling, so burstMB mirrors baseMB (this also fixes
    // the ReferenceError where the cards read undefined curMB/burstMB).
    const curMB = parseFloat((st.memory_mb || 0).toFixed(1));
    const baseMB = st.mem_limit_mb || 96;
    const burstMB = baseMB;
    const overLimit = curMB > baseMB;
    const ramPct = Math.min(100, Math.round((curMB / baseMB) * 100));
    const burstBadge = $('#ram-burst-badge');
    if (burstBadge) {
      burstBadge.style.display = overLimit ? '' : 'none';
      burstBadge.textContent = 'ПЕРЕВИЩЕННЯ ЛІМІТУ';
    }

    // Overview cards (all writes guarded — one missing node must not
    // leave the rest half-updated; №45)
    setText('#card-tg-state', stateLabels[state] || state);
    const cardTgDot = $('#card-tg-dot');
    // Same 4-state mapping as the header dot (№47): error is reachable here too.
    if (cardTgDot) cardTgDot.className = `status-dot ${state === 'authorized' ? 'active' : state === 'connecting' ? 'warning' : state === 'unauthorized' ? 'warning' : 'error'}`;
    // Multi-account aware (№48): prefer the active account from st.accounts,
    // fall back to legacy st.user.
    const accList = Array.isArray(st.accounts) ? st.accounts : ALL_ACCOUNTS;
    const acc = accList.find((a) => a.id === (st.active_account || ACTIVE_ACCOUNT_ID)) || accList[0];
    const accUser = acc?.user || st.user;
    setText('#card-tg-info', accUser && accUser.username ? `@${accUser.username} • ID: ${accUser.id}`
      : accUser && accUser.phone ? accUser.phone
      : acc && acc.phone ? acc.phone
      : acc && acc.title ? acc.title
      : 'Сесія очікує входу');

    setText('#card-ram-val', `${curMB} MB`);
    const ramMeter = $('#card-ram-meter');
    if (ramMeter) ramMeter.style.width = `${ramPct}%`;
    setText('#card-ram-base', baseMB);
    setText('#card-ram-burst', burstMB);
    setText('#card-ram-percent', `${ramPct}%`);

    setText('#card-plugins-up', `${st.plugins_running ?? st.plugins_up ?? 0} / ${st.plugin_count || 0}`);
    setText('#card-uptime', humanUptime(st.uptime_sec));
    setText('#card-sys-info', `${st.go_version || 'Go'} • ${st.goroutines || 0} goroutines`);
    setText('#badge-plugins-count', st.plugin_count || 0);

    // Footer sync state
    const d = new Date();
    const timeStr = [d.getHours(), d.getMinutes(), d.getSeconds()].map(n => String(n).padStart(2, '0')).join(':');
    $('#sync-dot').className = 'status-dot active';
    $('#sync-text').textContent = `Синхронізовано о ${timeStr}`;
  }

  // ---------- Sliders with CSS Variables ----------
  function setupSlider(input, chip, suffix = ' MB') {
    if (!input || !chip) return;
    // №11: loadSettings() викликається при кожному відкритті вкладки —
    // без guard слухачі input накопичуються і чіп оновлюється N разів.
    if (input.dataset.sliderBound === '1') {
      const val = parseFloat(input.value);
      chip.textContent = `${val}${suffix}`;
      return;
    }
    input.dataset.sliderBound = '1';
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
    setupSlider($('#setting-base-input'), $('#setting-base-chip'));
    // Burst sliders are intentionally not wired: backend has only mem_limit_mb.
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
      out.textContent = typeof res === 'string' ? res : (res.text || res.result || res.message || JSON.stringify(res, null, 2));
      toast('Команда', `«${name}» успішно виконано`, 'ok');
    } catch (err) {
      out.textContent = `Помилка: ${err.message}`;
      toast('Помилка', err.message, 'error');
    }
  });

  $('#btn-copy-cmd')?.addEventListener('click', async () => {
    const text = $('#cmd-result')?.textContent || '';
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      toast('Команда', 'Результат скопійовано', 'ok');
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text; document.body.appendChild(ta); ta.select();
      try { document.execCommand('copy'); toast('Команда', 'Результат скопійовано', 'ok'); }
      catch { toast('Команда', 'Не вдалося скопіювати', 'error'); }
      ta.remove();
    }
  });

  // Показати/сховати секрети (Web Token, App Hash).
  $$('[data-pw-toggle]').forEach((btn) => {
    btn.onclick = () => {
      const input = document.getElementById(btn.dataset.pwToggle);
      if (!input) return;
      const show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      btn.textContent = show ? '🙈' : '👁';
      btn.setAttribute('aria-pressed', String(show));
    };
  });

  // ---------- Plugins Management ----------
  async function loadPlugins() {
    try {
      const data = await api('/api/plugins');
      ALL_PLUGINS = data.plugins || [];
      renderPlugins();
      updateRamDisplay();
    } catch (e) {
      console.warn('Cannot load plugins:', e);
    }
  }

  let lastPluginSig = '';
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
      list = list.filter(p => {
        const perms = p.permissions || {};
        const hay = [
          p.name, p.description || '', p.language || '',
          (perms.tg || []).join(' '), perms.net ? 'net http мережа' : '',
          (p.events || []).join(' '),
        ].join(' ').toLowerCase();
        return query.split(/\s+/).every((tok => hay.includes(tok)));
      });
    }

    const emptyNote = $('#plugins-empty');
    if (emptyNote) emptyNote.hidden = list.length > 0;

    const activeAcc = ALL_ACCOUNTS.find(a => a.id === ACTIVE_ACCOUNT_ID) || ALL_ACCOUNTS[0];

    // Anti-flicker: background refresh every 12s must not replay the
    // rise-animation, steal focus or reset scroll while the user reads.
    const sig = filter + '|' + query + '|' + (activeAcc?.id || '') + '|'
      + list.map((p) => `${p.name}:${p.state}:${p.pid || 0}:${p.memory_kb || 0}:${p.events_delivered || 0}:${p.version || ''}`).join(',');
    if (sig === lastPluginSig) return;
    lastPluginSig = sig;

    box.innerHTML = list.map((p) => {
      const perms = p.permissions || {};
      const isEnabledForAcc = !activeAcc || !activeAcc.enabled_plugins || activeAcc.enabled_plugins.includes(p.name);
      const tgCaps = (perms.tg || []).map(c => `<span class="cap-chip">tg:${esc(c)}</span>`).join('');
      const netCap = perms.net ? '<span class="cap-chip">net:http</span>' : '';
      const wildCap = (p.events || []).includes('*')
        ? '<span class="cap-chip danger" title="Плагін отримує ВСІ події, включно з текстом усіх повідомлень">читає все</span>'
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
              ${p.has_settings ? `<button class="btn btn-sm btn-icon" data-settings="${esc(p.name)}" title="Налаштування плагіна" aria-label="Налаштування"><svg class="md-icon sm" viewBox="0 0 24 24"><path fill="currentColor" d="M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58c.18-.14.23-.41.12-.61l-1.92-3.32c-.12-.22-.37-.29-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54c-.04-.24-.24-.41-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.09.63-.09.94s.02.64.07.94l-2.03 1.58c-.18.14-.23.41-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z"/></svg></button>` : ''}
              <button class="btn btn-sm btn-icon" data-info="${esc(p.name)}" title="Інформація про плагін" aria-label="Інфо"><svg class="md-icon sm" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z"/></svg></button>
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
    const p = (ALL_PLUGINS || []).find((x) => x.name === name);
    if (!p || !p.has_settings) {
      toast("Налаштування", `У плагіна «${name}» немає параметрів налаштування`, "info");
      return;
    }
    PS_NAME = name;
    $("#ps-title").textContent = `Налаштування: ${name}`;
    const box = $("#ps-fields");
    if (box) box.innerHTML = "<p class=\"auth-lead\">Завантаження…</p>";
    const btnSave = $("#btn-ps-save");
    const btnReset = $("#btn-ps-reset");
    if (btnSave) btnSave.style.display = "inline-flex";
    if (btnReset) btnReset.style.display = "inline-flex";
    openModal("#modal-plugin-settings");
    try {
      const data = await Promise.race([
        api(`/api/plugins/${encodeURIComponent(name)}/settings`),
        new Promise((_, reject) => setTimeout(() => reject(new Error("Час очікування вичерпано")), 5000))
      ]);
      const fields = (data && data.fields) || [];
      if (!fields.length) {
        if (box) box.innerHTML = "<p class=\"auth-lead\">У цього плагіна немає доступних параметрів налаштування.</p>";
        if (btnSave) btnSave.style.display = "none";
        if (btnReset) btnReset.style.display = "none";
      } else {
        renderSettingsForm(fields);
      }
    } catch (err) {
      if (box) box.innerHTML = `<div class="auth-status err">${esc(err.message || "Не вдалося завантажити")}</div>`;
      if (btnSave) btnSave.style.display = "none";
      if (btnReset) btnReset.style.display = "none";
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
          + `<input type="password" class="form-input" ${attrs} value="${esc(val ?? '')}" placeholder="${esc(fld.placeholder || '')}" autocomplete="off">${hint}</div>`;
      default:
        return `<div class="form-group"><label class="form-label">${esc(fld.title || fld.key)}${badge}</label>`
          + `<input type="text" class="form-input" ${attrs} value="${esc(val ?? '')}" placeholder="${esc(fld.placeholder || '')}">${hint}</div>`;
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
        // Порожнє поле = скинути до дефолту (null → бекенд видаляє ключ).
        // Раніше порожні поля мовчки викидались і очистити значення було неможливо.
        if (v === '') values[k] = null;
        else if (!Number.isNaN(Number(v))) values[k] = Number(v);
      } else values[k] = el.value;
    });
    return values;
  }

  $('#plugins-container')?.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-settings]');
    if (btn) openPluginSettings(btn.dataset.settings);
  });

  // ---------- Plugin Info Card ----------
  const fmtUptime = (sec) => humanUptime(sec, true);

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

  // ---------- Security Audit (client-side, based on loaded plugins) ----------
  function auditLevel(p) {
    const perms = p.permissions || {};
    if (p.state === 'failed' || p.last_error) return 'DANGEROUS';
    if (perms.net || (perms.tg || []).length > 3 || (p.restarts || 0) > 5) return 'WARNING';
    return 'SAFE';
  }
  function openAdvisor() {
    const box = $('#advisor-modal-body');
    const fixBtn = $('#btn-advisor-autofix');
    if (fixBtn) fixBtn.style.display = 'none'; // no auto-RAM backend: honest audit only
    if (!box) { openModal('#modal-advisor'); return; }
    const list = ALL_PLUGINS || [];
    if (!list.length) {
      box.innerHTML = '<p class="auth-lead">Немає встановлених плагінів — аудитити нічого.</p>';
    } else {
      const rows = list.map((p) => {
        const lvl = auditLevel(p);
        const perms = p.permissions || {};
        const tg = (perms.tg || []).join(', ') || '—';
        const notes = [];
        if (p.state === 'failed' || p.last_error) notes.push(`Помилка: ${p.last_error || p.state}`);
        if (perms.net) notes.push('Має доступ до мережі (net:http)');
        if ((p.restarts || 0) > 5) notes.push(`Багато рестартів: ${p.restarts}`);
        if (!notes.length) notes.push('Ризикових дозволів не знайдено.');
        return `<div class="advisor-card"><span class="advisor-status-badge ${lvl}">${lvl}</span>`
          + `<div><b>${esc(p.name)}</b> · ${esc(p.state || '?')} · ${esc(p.language || 'go')}</div>`
          + `<div class="form-help">tg: ${esc(tg)}${perms.net ? ' · net:http' : ''}</div>`
          + `<div class="form-help">${esc(notes.join(' ') )}</div></div>`;
      }).join('');
      const danger = list.filter((p) => auditLevel(p) === 'DANGEROUS').length;
      const warn = list.filter((p) => auditLevel(p) === 'WARNING').length;
      box.innerHTML = `<p class="auth-lead">Перевірено плагінів: ${list.length} — DANGEROUS: ${danger}, WARNING: ${warn}.</p>` + rows;
    }
    openModal('#modal-advisor');
  }
  $('#btn-open-advisor')?.addEventListener('click', openAdvisor);

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
      // Always sync with the server (№51): never keep a stale value that
      // would overwrite a phone-side change on the next Save — except the
      // field the user is editing right now.
      const set = (sel, val) => {
        const el = $(sel);
        if (el && document.activeElement !== el) el.value = val;
      };
      set('#profile-first-name', u.first_name || '');
      set('#profile-last-name', u.last_name || '');
      set('#profile-username', u.username || '');
      const about = $('#profile-about');
      if (about && document.activeElement !== about) {
        about.value = p.about || '';
        about.dispatchEvent(new Event('input'));
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

      const tg = t;
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
    let fields = '';
    if (rec.fields && typeof rec.fields === 'object' && Object.keys(rec.fields).length) {
      try {
        fields = `<span class="log-fields">${esc(JSON.stringify(rec.fields))}</span>`;
      } catch { fields = ''; }
    }

    const el = document.createElement('div');
    el.className = 'log-entry';
    el.title = 'Клік — копіювати рядок';
    el.innerHTML = `<span class="log-time">${esc(time)}</span> <span class="log-level ${esc(lvl)}">${esc(lvl)}</span> <span class="log-scope">${esc(scope)}</span> <span class="log-msg">${esc(rec.msg || '')}</span>${fields}`;

    term.appendChild(el);
    while (term.children.length > 500) term.removeChild(term.firstChild);
    if (AUTOSCROLL) term.scrollTop = term.scrollHeight;
    updateLogJump();
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
    this.setAttribute('aria-pressed', String(AUTOSCROLL));
    if (AUTOSCROLL) {
      const term = $('#logs-terminal');
      if (term) term.scrollTop = term.scrollHeight;
    }
    toast('Логи', `Автопрокрутка ${AUTOSCROLL ? 'увімкнена' : 'вимкнена'}`, 'ok');
  });

  // Кнопка «До кінця»: видима лише коли користувач відмотав вгору.
  function updateLogJump() {
    const term = $('#logs-terminal'), btn = $('#btn-log-jump');
    if (!term || !btn) return;
    const away = term.scrollHeight - term.scrollTop - term.clientHeight > 120;
    btn.classList.toggle('visible', away);
  }
  $('#logs-terminal')?.addEventListener('scroll', updateLogJump);
  $('#btn-log-jump')?.addEventListener('click', () => {
    const term = $('#logs-terminal');
    if (term) { term.scrollTop = term.scrollHeight; updateLogJump(); }
  });

  // Клік по рядку логу — копіювати в буфер.
  $('#logs-terminal')?.addEventListener('click', async (e) => {
    const entry = e.target.closest('.log-entry');
    if (!entry) return;
    const text = entry.innerText || entry.textContent || '';
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text; document.body.appendChild(ta); ta.select();
      try { document.execCommand('copy'); } catch {}
      ta.remove();
    }
    entry.classList.add('copied');
    setTimeout(() => entry.classList.remove('copied'), 600);
  });

  $('#btn-clear-logs')?.addEventListener('click', () => {
    LOG_LINES = [];
    $('#logs-terminal').innerHTML = '';
    $('#log-count').textContent = '0 рядків';
    toast('Логи', 'Вікно консолі очищено', 'ok');
  });

  $('#btn-download-logs')?.addEventListener('click', () => {
    const header = `# Aurora UserBot logs — export ${new Date().toISOString()} — ${LOG_LINES.length} records`;
    const text = [header].concat(LOG_LINES.map(r => {
      const base = `[${r.time}] [${r.level}] [${r.scope || 'core'}] ${r.msg}`;
      if (r.fields && typeof r.fields === 'object' && Object.keys(r.fields).length) {
        try { return base + ' ' + JSON.stringify(r.fields); } catch { return base; }
      }
      return base;
    })).join('\n');
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

  // Search in Control Center (filters cards AND nav chips, shows empty state)
  function controlSearch(q) {
    q = (q || '').toLowerCase().trim();
    let visibleCards = 0;
    $$('.control-action-card').forEach((card) => {
      const kw = (card.dataset.keywords || '') + ' ' + card.innerText.toLowerCase();
      const show = !q || q.split(/\s+/).every((tok => kw.includes(tok)));
      card.style.display = show ? 'flex' : 'none';
      if (show) visibleCards++;
    });
    let visibleChips = 0;
    $$('.quick-jump-chip').forEach((chip) => {
      const kw = (chip.dataset.jump || '') + ' ' + chip.innerText.toLowerCase();
      const show = !q || q.split(/\s+/).every((tok => kw.includes(tok)));
      chip.style.display = show ? '' : 'none';
      if (show) visibleChips++;
    });
    let empty = $('#control-empty');
    if (!empty) {
      const grid = $('#control-actions-grid');
      if (grid) {
        empty = document.createElement('p');
        empty.id = 'control-empty';
        empty.className = 'empty-note';
        empty.textContent = 'Нічого не знайдено — спробуйте інший запит.';
        grid.after(empty);
      }
    }
    if (empty) empty.hidden = (visibleCards + visibleChips) > 0;
    clearKbdFocus();
  }
  $('#control-search-input')?.addEventListener('input', (e) => controlSearch(e.target.value));

  // Arrow-key navigation across visible cards (pairs with .kbd-focus CSS).
  function visibleCards() {
    return $$('.control-action-card').filter((c) => c.style.display !== 'none');
  }
  function clearKbdFocus() {
    $$('.control-action-card.kbd-focus').forEach((c) => c.classList.remove('kbd-focus'));
  }
  function moveKbdFocus(dir) {
    const list = visibleCards();
    if (!list.length) return;
    let idx = list.findIndex((c) => c.classList.contains('kbd-focus'));
    idx = idx < 0 ? (dir > 0 ? 0 : list.length - 1) : (idx + dir + list.length) % list.length;
    clearKbdFocus();
    list[idx].classList.add('kbd-focus');
    list[idx].scrollIntoView({ block: 'nearest' });
  }
  $('#control-search-input')?.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowRight') { e.preventDefault(); moveKbdFocus(1); }
    else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') { e.preventDefault(); moveKbdFocus(-1); }
    else if (e.key === 'Enter') {
      const cur = $('.control-action-card.kbd-focus');
      if (cur && cur.style.display !== 'none') { e.preventDefault(); cur.click(); }
    }
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
      case 'stop-all-plugins': {
        if (!confirm('Зупинити всі запущені плагіни?')) return;
        let ok = 0, fail = 0;
        for (const p of ALL_PLUGINS.filter(x => x.state === 'running')) {
          try { await api(`/api/plugins/${encodeURIComponent(p.name)}/stop`, { method: 'POST' }); ok++; }
          catch { fail++; }
        }
        toast('Плагіни', fail ? `Зупинено: ${ok}, помилок: ${fail}` : `Зупинено плагінів: ${ok}`, fail ? 'warn' : 'warn');
        await loadPlugins();
        break;
      }
      case 'start-all-plugins': {
        let ok = 0, fail = 0;
        for (const p of ALL_PLUGINS.filter(x => x.state !== 'running')) {
          try { await api(`/api/plugins/${encodeURIComponent(p.name)}/start`, { method: 'POST' }); ok++; }
          catch { fail++; }
        }
        toast('Плагіни', fail ? `Запущено: ${ok}, помилок: ${fail}` : `Запущено плагінів: ${ok}`, fail ? 'error' : 'ok');
        await loadPlugins();
        break;
      }
      case 'clear-cache': {
        // No dedicated cache API on the backend — run a real GC cycle and
        // drop the in-memory log buffer so the button does something honest.
        try {
          const st = await api('/api/gc', { method: 'POST' });
          LOG_LINES = [];
          rerenderLogs();
          toast('Кеш', `GC виконано, купа: ${(st.memory_mb || 0).toFixed(1)} MB. Серверного кеша окремо немає.`, 'ok');
        } catch (err) {
          toast('Кеш', `Не вдалося: ${err.message}`, 'error');
        }
        break;
      }
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
    // Escape — закриває всі модалки і ГАРАНТОВАНО зупиняє QR-полінг
    if (e.key === 'Escape') {
      const hadAuth = !!document.querySelector('#modal-auth.active, #modal-account-add.active');
      const hadAccAdd = !!document.querySelector('#modal-account-add.active');
      $$('.modal-overlay.active').forEach(m => m.classList.remove('active'));
      if (hadAuth) stopQRPolling();
      if (hadAccAdd) maybeCleanupAccountModal('modal-account-add');
      return;
    }
    // Numbers 1-6 / T / / — лише без модифікаторів, поза полями вводу
    // і коли жодна модалка не відкрита (інакше ламаємо Ctrl+T, пошук, форми).
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (document.querySelector('.modal-overlay.active')) return;
    if (!['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement.tagName)) {
      if (e.key >= '1' && e.key <= '6') {
        const tabs = ['overview', 'plugins', 'console', 'profile', 'settings', 'logs'];
        const target = tabs[parseInt(e.key, 10) - 1];
        if (target) switchTab(target);
      } else if (e.key === 't' || e.key === 'T' || e.key === 'е' || e.key === 'Е') {
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
  // Central place to stop QR polling: any close path for the auth
  // modals must halt /api/auth/qr + /api/auth every-2s polling
  // (Eco-Mode would otherwise poll forever after ✖ / Escape).
  function stopQRPolling() {
    try { if (typeof qrStopPoll === 'function') qrStopPoll(); } catch {}
    try { if (typeof accStopQR === 'function') accStopQR(); } catch {}
  }
  function maybeCleanupAccountModal(overlayId) {
    if (overlayId === 'modal-account-add') {
      try { if (typeof accCleanupOrphan === 'function') accCleanupOrphan(); } catch {}
    }
  }
  function closeModal(sel) {
    const el = $(sel);
    if (el) el.classList.remove('active');
    if (sel === '#modal-auth' || sel === '#modal-account-add') stopQRPolling();
    if (sel === '#modal-account-add') maybeCleanupAccountModal('modal-account-add');
  }

  $$('[data-close-modal]').forEach((btn) => {
    btn.onclick = () => {
      const overlay = btn.closest('.modal-overlay');
      if (overlay) {
        overlay.classList.remove('active');
        if (overlay.id === 'modal-auth' || overlay.id === 'modal-account-add') stopQRPolling();
        maybeCleanupAccountModal(overlay.id);
      }
    };
  });

  $$('.modal-overlay').forEach((overlay) => {
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay) {
        overlay.classList.remove('active');
        if (overlay.id === 'modal-auth' || overlay.id === 'modal-account-add') stopQRPolling();
        maybeCleanupAccountModal(overlay.id);
      }
    });
  });

  // ---------- Auth Login Modal v2 (QR на цьому телефоні / код / сесія) ----------
  function normalizePhone(val) {
    let p = (val || '').replace(/[^\d+]/g, '');
    if (!p) return '';
    if (p.startsWith('00')) p = '+' + p.slice(2);
    // Trunk-zero → +38 лише для 10-значних номерів з нулем попереду
    // (український формат 0XX XXX XX XX). Інші країни лишаємо як є,
    // щоб не ламати їхні номери вшитою українізацією.
    else if (/^0\d{9}$/.test(p)) p = '+38' + p;
    else if (!p.startsWith('+')) p = '+' + p;
    return p;
  }

  // ---------- Streams & Events ----------
  // EventSource can't send Authorization headers, so the token travels
  // in the query string here (api() uses the header + same-origin cookie).
  function openStream(path, onMessage) {
    let es;
    const connect = () => {
      const tok = localStorage.getItem("aurora_token") || window.__AURORA_TOKEN__;
      const url = tok ? (path + (path.includes("?") ? "&" : "?") + "token=" + encodeURIComponent(tok)) : path;
      es = new EventSource(url);
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
      return true;
    } catch (err) {
      // Offline: never leave a stale green "Синхронізовано".
      const dot = $('#sync-dot'), txt = $('#sync-text');
      if (dot) dot.className = 'status-dot error';
      if (txt) txt.textContent = 'Ядро недоступне — офлайн';
      const tgDot = $('#tg-status-dot');
      if (tgDot) tgDot.className = 'status-dot error';
      const tgText = $('#tg-status-text');
      if (tgText) tgText.textContent = 'Офлайн';
      return false;
    }
  }

  $('#btn-refresh')?.addEventListener('click', async function () {
    this.classList.add('spin');
    const ok = await refreshStatus();
    await loadPlugins();
    setTimeout(() => this.classList.remove('spin'), 700);
    if (ok) toast('Оновлено', 'Дані успішно актуалізовано', 'ok');
    else toast('Офлайн', 'Ядро недоступне — показано останні дані', 'error');
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

  function syncAccountPopAria() {
    const open = $('#account-pop')?.classList.contains('open') || false;
    $('#user-pill')?.setAttribute('aria-expanded', String(open));
  }
  $('#user-pill')?.addEventListener('click', (e) => {
    e.stopPropagation();
    $('#account-pop')?.classList.toggle('open');
    syncAccountPopAria();
  });

  document.addEventListener('click', (e) => {
    if (!e.target.closest('#account-wrap')) {
      $('#account-pop')?.classList.remove('open');
      syncAccountPopAria();
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
      syncAccountPopAria();
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
    $$('#add-acc-tabs [data-add-acc-tab]').forEach((b) => {
      const on = b.dataset.addAccTab === name;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', String(on));
    });
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
    // Mark completed BEFORE closing so the orphan-cleanup on close skips us.
    window._pendingAccId = '';
    toast('Успіх', msg, 'ok');
    closeModal('#modal-account-add');
    accStopQR();
    await loadAccounts();
    await refreshStatus();
  }

  // Best-effort cleanup of an unfinished wizard account (№9): closing the
  // modal mid-wizard must not leave a title/phone-less orphan in the list.
  let accCleaning = false;
  async function accCleanupOrphan() {
    const id = window._pendingAccId;
    if (!id || accCleaning) return;
    accCleaning = true;
    try {
      // Never delete an account that managed to authorize mid-close.
      const list = await api('/api/accounts');
      const me = Array.isArray(list) ? list.find((a) => a.id === id) : null;
      if (!me || me.session !== 'authorized') {
        try { await api(`/api/accounts/${encodeURIComponent(id)}`, { method: 'DELETE' }); } catch {}
      }
    } catch {
      try { await api(`/api/accounts/${encodeURIComponent(id)}`, { method: 'DELETE' }); } catch {}
    }
    try { await loadAccounts(); } catch {}
    window._pendingAccId = '';
    accCleaning = false;
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

  function openAddAccountModal() {
    $('#account-pop')?.classList.remove('open');
    window._pendingAccId = '';
    accNote('', false);
    accShowStep('phone');
    accTab('code');
    accSetQRLink('', '');
    const st = $('#add-acc-qr-status');
    if (st) st.textContent = 'Посилання ще не запитано.';
    openModal('#modal-account-add');
  }

  $('#btn-open-add-account')?.addEventListener('click', openAddAccountModal);

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
  function qrImgURL(path) {
    const tok = localStorage.getItem("aurora_token") || window.__AURORA_TOKEN__ || '';
    // Single cache-buster param; token only when present (no more "?t=&t=").
    return path + (tok ? '?token=' + encodeURIComponent(tok) + '&t=' : '?t=') + Date.now();
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
    // Decorative icons must not flood screen readers (№41).
    try {
      document.querySelectorAll('svg.md-icon').forEach((svg) => {
        if (!svg.hasAttribute('aria-hidden')) svg.setAttribute('aria-hidden', 'true');
      });
    } catch {}
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


  // Prevent accidental value changes when scrolling past range sliders
  document.addEventListener("wheel", (e) => {
    if (e.target && e.target.type === "range") {
      e.preventDefault();
    }
  }, { passive: false });

  boot();
})();
