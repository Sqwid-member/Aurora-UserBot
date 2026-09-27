/* Aurora control panel — vanilla JS, no dependencies, ~0 KB framework tax.
   Written for a phone in Termux: large tap targets, no hover-only affordances. */
(() => {
  'use strict';

  // Authentication rides on an HttpOnly cookie the server set on the first
  // visit, so the token never appears in this file, in the page source or in
  // the URL bar. We only learn it back, on demand, from /api/token.
  let TOKEN = '';
  const $ = (sel) => document.querySelector(sel);
  const $$ = (sel) => Array.from(document.querySelectorAll(sel));

  // ---------- api ----------
  async function api(path, opts = {}) {
    const headers = Object.assign({}, opts.headers || {});
    if (opts.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
    const res = await fetch(path, Object.assign({ credentials: 'same-origin' }, opts, { headers }));
    if (res.status === 401) { location.reload(); throw new Error('unauthorized'); }
    const text = await res.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch { data = { raw: text }; }
    if (!res.ok) throw new Error(data.error || ('HTTP ' + res.status));
    return data;
  }

  // ---------- utils ----------
  const esc = (s) => String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

  function humanUptime(sec) {
    if (!sec && sec !== 0) return '—';
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600),
          m = Math.floor((sec % 3600) / 60), s = sec % 60;
    if (d) return `${d}д ${h}г`;
    if (h) return `${h}г ${m}хв`;
    if (m) return `${m}хв ${s}с`;
    return `${s}с`;
  }

  function toast(title, text, level = 'info') {
    const el = document.createElement('div');
    el.className = 'toast' + (level && level !== 'info' ? ' ' + level : '');
    el.innerHTML = `<div class="t">${esc(title)}</div><div>${esc(text || '')}</div>`;
    $('#toasts').appendChild(el);
    setTimeout(() => { el.style.opacity = '0'; setTimeout(() => el.remove(), 250); }, 4200);
  }

  // ---------- tabs ----------
  $$('.tab').forEach((btn) => {
    btn.onclick = () => {
      $$('.tab').forEach((b) => b.classList.toggle('active', b === btn));
      $$('.panel').forEach((p) => p.classList.toggle('active', p.id === 'panel-' + btn.dataset.tab));
      window.scrollTo({ top: 0, behavior: 'instant' });
    };
  });

  // ---------- status ----------
  const stateNames = {
    offline: 'offline', connecting: 'підключення…',
    unauthorized: 'не авторизовано', authorized: 'на зв’язку', error: 'помилка',
  };

  function renderStatus(st) {
    $('#state-pill').dataset.state = st.session;
    $('#state-text').textContent = stateNames[st.session] || st.session;

    const stats = [
      ['Сесія', stateNames[st.session] || st.session, st.session === 'authorized' ? 'ok' : 'warn'],
      ['Час роботи', humanUptime(st.uptime_sec), ''],
      ['ОП у пам’яті', (st.memory_mb || 0).toFixed(1) + ' МБ', ''],
      ['Ліміт пам’яті', st.mem_limit_mb ? st.mem_limit_mb + ' МБ' : '∞', ''],
      ['Горутини', st.goroutines, ''],
      ['Плагіни', `${st.plugins_running} / ${st.plugin_count}`, st.plugins_running ? 'ok' : 'warn'],
      ['Версія', st.version, ''],
      ['Go', st.go_version, ''],
    ];
    $('#stat-grid').innerHTML = stats.map(([k, v, cls]) =>
      `<div class="stat"><div class="k">${esc(k)}</div><div class="v ${cls}">${esc(v)}</div></div>`
    ).join('');

    const card = $('#account-card');
    if (st.user) {
      const initial = (st.user.first_name || st.user.username || '?').charAt(0).toUpperCase();
      card.innerHTML = `
        <div class="acct">
          <div class="avatar">${esc(initial)}</div>
          <div>
            <div style="font-weight:700">${esc(st.user.first_name || '')} ${esc(st.user.last_name || '')}</div>
            <div class="muted small">${st.user.username ? '@' + esc(st.user.username) + ' · ' : ''}ID ${st.user.id}</div>
            <div class="muted small">${esc(st.user.phone || 'номер приховано')}</div>
          </div>
        </div>`;
    }
  }

  // ---------- event feed ----------
  const events = [];
  function pushEvent(ev) {
    if (ev.name === 'message.new' && ev.data && ev.data.text) {
      events.unshift({ name: ev.name, text: ev.data.peer_title, body: ev.data.text, at: ev.data.date });
    } else {
      events.unshift({ name: ev.name, at: Math.floor(Date.now() / 1000) });
    }
    events.length = Math.min(events.length, 50);
    const feed = $('#event-feed');
    feed.innerHTML = events.map((e) => `
      <div class="row-item">
        <div class="meta">${esc(new Date((e.at || 0) * 1000).toLocaleTimeString())} · ${esc(e.name)}</div>
        <div>${esc(e.text || '')}</div>
        ${e.body ? `<div class="muted small">${esc(e.body.slice(0, 200))}</div>` : ''}
      </div>`).join('');
  }

  // ---------- plugins ----------
  let pluginCache = [];
  async function loadPlugins() {
    const { plugins: list } = await api('/api/plugins');
    pluginCache = list || [];
    renderPlugins();
  }

  function renderPlugins() {
    const filter = $('#plugin-filter').value.trim().toLowerCase();
    const list = pluginCache.filter((p) =>
      !filter || p.name.includes(filter) || (p.description || '').toLowerCase().includes(filter));
    const box = $('#plugin-list');

    if (!list.length) {
      box.innerHTML = '<div class="card muted">Плагінів не знайдено. Скопіюйте каталог плагіна у <code>~/&#8202;.local/share/aurora/plugins/</code> або натисніть «Встановити».</div>';
      return;
    }

    box.innerHTML = list.map((p) => {
      const running = p.state === 'running';
      const acts = [
        running
          ? `<button class="ghost" data-act="restart" data-n="${esc(p.name)}">Перезапустити</button>
             <button class="ghost" data-act="stop" data-n="${esc(p.name)}">Зупинити</button>`
          : `<button class="primary" data-act="start" data-n="${esc(p.name)}">Запустити</button>`,
        `<button class="danger" data-act="uninstall" data-n="${esc(p.name)}">Видалити</button>`,
      ].join('');
      const stats = [
        `мов: ${esc(p.language)}`,
        running ? `pid ${p.pid}` : p.state,
        `подій: ${p.events_delivered}`,
        p.events_dropped ? `втрачено: ${p.events_dropped}` : '',
        p.uptime_sec ? `час: ${humanUptime(p.uptime_sec)}` : '',
        p.restarts ? `рестартів: ${p.restarts}` : '',
      ].filter(Boolean).map((s) => `<span>${s}</span>`).join('');
      const perms = p.permissions || {};
      const plist = [].concat(perms.tg || [], perms.net ? ['net'] : []).join(', ');

      return `<div class="plugin" data-state="${esc(p.state)}">
        <div class="head">
          <span class="name">${esc(p.name)}</span>
          <span class="tag">v${esc(p.version || '0')}</span>
          <span class="tag lang">${esc(p.language || '?')}</span>
          ${plist ? `<span class="tag">${esc(plist)}</span>` : ''}
        </div>
        <div class="desc">${esc(p.description || '—')}</div>
        <div class="stats">${stats}</div>
        ${p.last_error ? `<div class="err">⚠ ${esc(p.last_error)}</div>` : ''}
        <div class="acts">${acts}</div>
      </div>`;
    }).join('');
  }

  $('#plugin-filter').oninput = renderPlugins;

  $('#plugin-list').addEventListener('click', async (ev) => {
    const btn = ev.target.closest('button[data-act]');
    if (!btn) return;
    const { act, n } = btn.dataset;
    btn.disabled = true;
    try {
      if (act === 'uninstall') {
        if (!confirm(`Видалити плагін «${n}» разом з файлами?`)) return;
        await api('/api/plugins/' + encodeURIComponent(n) + '/uninstall', { method: 'POST' });
        toast('Плагіни', `«${n}» видалено`, 'warn');
      } else {
        const { message } = await api(`/api/plugins/${encodeURIComponent(n)}/${act}`, { method: 'POST' });
        toast('Плагіни', message || `${n}: ${act}`, 'info');
      }
      await loadPlugins();
    } catch (e) {
      toast('Помилка', e.message, 'error');
    } finally {
      btn.disabled = false;
    }
  });

  // ---------- install dialog ----------
  $('#plugin-install').onclick = () => $('#dlg-install').showModal();
  $('#dlg-install').addEventListener('close', async (ev) => {
    if (ev.target.returnValue !== 'ok') return;
    const source = $('#install-url').value.trim();
    if (!source) return;
    try {
      const { message } = await api('/api/plugins/install', {
        method: 'POST', body: JSON.stringify({ source, name: $('#install-name').value.trim() }),
      });
      toast('Плагіни', message, 'info');
      await loadPlugins();
    } catch (e) {
      toast('Помилка', e.message, 'error');
    }
  });

  // ---------- commands ----------
  async function loadCommands() {
    const cmds = await api('/api/commands');
    const box = $('#command-list');
    if (!cmds || !cmds.length) { box.innerHTML = '<div class="muted">Активних команд немає.</div>'; return; }
    box.innerHTML = cmds.map((c) => `
      <div class="row-item" style="padding:8px 0;border-bottom:1px solid var(--line)">
        <code>${esc(c.name)}</code>
        <span class="muted small">${esc(c.usage || c.description || '')}</span>
        <div class="muted small">${esc(c.description || '')}</div>
      </div>`).join('');
  }

  // ---------- auth ----------
  async function loadAuth() {
    const [auth, sess] = await Promise.all([api('/api/auth'), api('/api/session')]);
    const body = $('#auth-body');
    const cfg = window.__cfg || {};

    if (auth.step === 'code') {
      body.innerHTML = `
        <p>Код надіслано на номер <b>${esc(auth.phone || '')}</b>.</p>
        <input id="auth-code" inputmode="numeric" placeholder="5-значний код" autocomplete="one-time-code">
        <button class="primary" id="auth-code-go">Підтвердити</button>`;
      $('#auth-code-go').onclick = async () => {
        try {
          await api('/api/auth/code', { method: 'POST', body: JSON.stringify({ code: $('#auth-code').value.trim() }) });
          toast('Вхід', 'Код прийнято', 'info');
          setTimeout(refresh, 1200);
        } catch (e) { toast('Помилка', e.message, 'error'); }
      };
    } else if (auth.step === 'password') {
      body.innerHTML = `
        <p>Потрібен пароль двофакторної автентифікації.</p>
        <input id="auth-pass" type="password" placeholder="пароль 2FA" autocomplete="current-password">
        <button class="primary" id="auth-pass-go">Увійти</button>`;
      $('#auth-pass-go').onclick = async () => {
        try {
          await api('/api/auth/password', { method: 'POST', body: JSON.stringify({ password: $('#auth-pass').value }) });
          toast('Вхід', 'Пароль прийнято', 'info');
          setTimeout(refresh, 1200);
        } catch (e) { toast('Помилка', e.message, 'error'); }
      };
    } else if (auth.step === 'phone') {
      body.innerHTML = `
        <p>Введіть номер телефону для входу.</p>
        <input id="auth-phone" placeholder="+380…" value="${esc((window.__cfg || {}).telegram?.phone || '')}" autocomplete="tel">
        <button class="primary" id="auth-phone-go">Надіслати код</button>`;
      $('#auth-phone-go').onclick = async () => {
        try {
          await api('/api/auth/code-request', { method: 'POST', body: JSON.stringify({ phone: $('#auth-phone').value.trim() }) });
          toast('Вхід', 'Код надіслано', 'info');
          setTimeout(loadAuth, 1200);
        } catch (e) { toast('Помилка', e.message, 'error'); }
      };
    } else if (auth.step === 'signed_in' || (cfg && cfg.telegram && cfg.telegram.app_id)) {
      body.innerHTML = `
        <p class="muted">Ядро авторизоване або очікує авторизації під час старту.</p>
        <div class="row">
          <button class="ghost" id="auth-import">Імпортувати StringSession</button>
          <button class="ghost" id="auth-logout">Вийти з Telegram</button>
        </div>`;
      $('#auth-import').onclick = () => $('#dlg-session').showModal();
      $('#auth-logout').onclick = async () => {
        try { await api('/api/logout', { method: 'POST' }); toast('Вхід', 'Сесію закрито', 'warn'); }
        catch (e) { toast('Помилка', e.message, 'error'); }
      };
    } else {
      body.innerHTML = '<p class="muted">Ядро не налаштоване. Заповніть app_id і app_hash у вкладці «Налаштування».</p>';
    }
    void sess;

    const sbody = $('#session-body');
    sbody.innerHTML = sess.exists
      ? `<p>Сесія збережена локально.</p>
         <div class="muted small">DC: ${sess.dc} · ${esc(sess.address || '')}</div>
         <div class="muted small">auth_key: ${esc(sess.auth_key_id || '')}</div>`
      : '<p class="muted">Активної сесії немає.</p>';
  }

  $('#dlg-session').addEventListener('close', async (ev) => {
    if (ev.target.returnValue !== 'ok') return;
    try {
      await api('/api/session/import', { method: 'POST', body: JSON.stringify({ session: $('#session-str').value.trim() }) });
      toast('Сесія', 'Імпортовано — перезапустіть ядро', 'info');
    } catch (e) { toast('Помилка', e.message, 'error'); }
  });

  // ---------- chat ----------
  $('#chat-send').onclick = async () => {
    const peer = $('#chat-peer').value.trim();
    const text = $('#chat-text').value;
    if (!peer || !text) return;
    const btn = $('#chat-send');
    btn.disabled = true;
    try {
      const res = await api('/api/send', {
        method: 'POST',
        body: JSON.stringify({
          peer, text,
          silent: $('#chat-silent').checked,
          no_preview: $('#chat-nopreview').checked,
        }),
      });
      $('#chat-out').innerHTML = `<p class="muted small">Надіслано · msg_id ${res.id}</p><div>${esc(text)}</div>`;
      $('#chat-text').value = '';
    } catch (e) {
      $('#chat-out').innerHTML = `<p style="color:var(--err)">${esc(e.message)}</p>`;
    } finally { btn.disabled = false; }
  };

  // ---------- logs ----------
  const logBody = () => $('#log-body');
  function appendLog(rec) {
    const el = document.createElement('div');
    el.innerHTML = `<span class="t">${esc((rec.time || '').slice(11, 23))}</span> ` +
      `<span class="${esc(rec.level)}">${esc(rec.level.padEnd(5))}</span> ` +
      (rec.scope ? `<span class="s">${esc(rec.scope)}</span> ` : '') + esc(rec.msg);
    const body = logBody();
    body.appendChild(el);
    if ($('#log-follow').checked) body.scrollTop = body.scrollHeight;
    while (body.childElementCount > 1500) body.removeChild(body.firstChild);
  }

  $('#log-clear').onclick = () => { logBody().innerHTML = ''; };

  // ---------- settings ----------
  let cfgDraft = null;
  async function loadSettings() {
    const cfg = await api('/api/config');
    window.__cfg = cfg;
    cfgDraft = cfg;
    const t = cfg.telegram || {}, w = cfg.web || {}, r = cfg.runtime || {}, p = cfg.plugins || {};
    $('#settings-body').innerHTML = `
      <label>app_id</label><input id="c-appid" inputmode="numeric" value="${esc(t.app_id || '')}">
      <label>app_hash</label><input id="c-apphash" value="${esc(t.app_hash || '')}" placeholder="• • • •">
      <label>номер телефону</label><input id="c-phone" value="${esc(t.phone || '')}" placeholder="+380…">
      <label>MTProxy (host:port:hexsecret)</label><input id="c-mtproxy" value="${esc(t.mtproxy || '')}">
      <label>SOCKS5 (socks5://host:port)</label><input id="c-socks5" value="${esc(t.socks5 || '')}">
      <div class="row">
        <div style="flex:1"><label>порт панелі</label><input id="c-port" inputmode="numeric" value="${esc(w.port || 8420)}"></div>
        <div style="flex:1"><label>ліміт пам’яті, МБ</label><input id="c-mem" inputmode="numeric" value="${esc(r.mem_limit_mb ?? 0)}"></div>
        <div style="flex:1"><label>рівень логів</label>
          <select id="c-loglevel">
            ${['trace', 'debug', 'info', 'warn', 'error'].map((l) =>
              `<option ${r.log_level === l ? 'selected' : ''}>${l}</option>`).join('')}
          </select>
        </div>
      </div>
      <div class="row">
        <label class="chk"><input type="checkbox" id="c-web" ${w.enabled ? 'checked' : ''}> панель увімкнена</label>
        <label class="chk"><input type="checkbox" id="c-open" ${w.open_browser ? 'checked' : ''}> відкривати браузер</label>
        <label class="chk"><input type="checkbox" id="c-pfs" ${t.pfs ? 'checked' : ''}> PFS</label>
        <label class="chk"><input type="checkbox" id="c-noupd" ${t.disable_updates ? 'checked' : ''}> без апдейтів</label>
        <label class="chk"><input type="checkbox" id="c-sandbox" ${p.sandbox ? 'checked' : ''}> пісочниця</label>
        <label class="chk"><input type="checkbox" id="c-ro" ${r.read_only ? 'checked' : ''}> read-only</label>
      </div>`;
  }

  $('#settings-save').onclick = async () => {
    const num = (sel) => parseInt($(sel).value, 10) || 0;
    const payload = Object.assign({}, cfgDraft, {
      telegram: Object.assign({}, cfgDraft.telegram, {
        app_id: num('#c-appid'),
        app_hash: $('#c-apphash').value.trim(),
        phone: $('#c-phone').value.trim(),
        mtproxy: $('#c-mtproxy').value.trim(),
        socks5: $('#c-socks5').value.trim(),
        pfs: $('#c-pfs').checked,
        disable_updates: $('#c-noupd').checked,
      }),
      web: Object.assign({}, cfgDraft.web, {
        enabled: $('#c-web').checked,
        open_browser: $('#c-open').checked,
        port: num('#c-port') || 8420,
      }),
      runtime: Object.assign({}, cfgDraft.runtime, {
        mem_limit_mb: num('#c-mem'),
        log_level: $('#c-loglevel').value,
        read_only: $('#c-ro').checked,
      }),
      plugins: Object.assign({}, cfgDraft.plugins, { sandbox: $('#c-sandbox').checked }),
    });
    try {
      await api('/api/config', { method: 'PUT', body: JSON.stringify(payload) });
      toast('Налаштування', 'Збережено. Частина змін потребує перезапуску.', 'info');
    } catch (e) { toast('Помилка', e.message, 'error'); }
  };

  $('#settings-restart').onclick = async () => {
    if (!confirm('Перезапустити ядро Aurora?')) return;
    try { await api('/api/restart', { method: 'POST' }); toast('Ядро', 'Перезапуск…', 'warn'); }
    catch (e) { toast('Помилка', e.message, 'error'); }
  };

  $('#foot-token').onclick = async () => {
    if (!TOKEN) {
      try { TOKEN = (await api('/api/token')).token || ''; } catch { /* ignore */ }
    }
    if (!TOKEN) { toast('Помилка', 'токен недоступний', 'error'); return; }
    navigator.clipboard?.writeText(TOKEN).then(
      () => toast('Токен', 'Скопійовано в буфер обміну', 'info'),
      () => prompt('Ваш токен:', TOKEN));
  };

  // ---------- streams ----------
  function openStream(path, onMessage) {
    let es;
    const connect = () => {
      es = new EventSource(path, { withCredentials: true });
      es.onmessage = (e) => { try { onMessage(JSON.parse(e.data)); } catch {} };
      es.onerror = () => { es.close(); setTimeout(connect, 4000); };
    };
    connect();
  }

  // ---------- boot ----------
  async function boot() {
    try {
      const st = await api('/api/status');
      renderStatus(st);
    } catch {
      return; // 401 already triggers a reload onto the server-rendered gate
    }
    // Settings first: the auth panel reads the phone number from them.
    await Promise.allSettled([loadSettings()]);
    await Promise.allSettled([loadPlugins(), loadCommands(), loadAuth()]);
    openStream('/api/logs/stream', appendLog);
    openStream('/api/events', pushEvent);
  }

  setInterval(async () => {
    try { renderStatus(await api('/api/status')); } catch { /* ignore */ }
  }, 4000);

  setInterval(() => {
    if (!$('#panel-plugins').classList.contains('active')) return;
    loadPlugins().catch(() => {});
  }, 12000);

  boot();
})();
