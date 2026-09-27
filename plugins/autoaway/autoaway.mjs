#!/usr/bin/env node
// Aurora "autoaway" plugin — a zero-dependency Node.js example.
//
// Node ships JSON, so the protocol layer is just readline + JSON.stringify.
// Everything the plugin needs is in the standard library.

import { createInterface } from 'node:readline';

let protocol = 1;
let pluginName = 'autoaway';
let requestId = 0;

const state = {
  enabled: true,
  minutes: 15,
  lastActivity: Date.now(),
  awaySet: false,
};

const log = (level, msg) => process.stderr.write(`[${level.toUpperCase()}] ${msg}\n`);

const write = (frame) => process.stdout.write(JSON.stringify(frame) + '\n');

const call = (method, params) => {
  requestId += 1;
  write({ jsonrpc: '2.0', id: requestId, method, params: params ?? {} });
};

const reply = (msg, result, err) => {
  const frame = { jsonrpc: '2.0', id: msg.id };
  if (err) frame.error = { code: -32000, message: err };
  else frame.result = result ?? {};
  write(frame);
};

const peerRef = (id, type) => (type === 'channel' ? `-${id}` : String(id));

async function setStatus(text) {
  // A minimal MTProto-free way to show presence: the host exposes
  // tg.send, so "presence" here is a Saved Messages note. Swap this for
  // account.updateStatus when you extend the host API.
  call('tg.send', { peer: 'me', text: `⚡ ${text}` });
}

async function maybeSetAway() {
  if (!state.enabled || state.awaySet) return;
  const idleMinutes = (Date.now() - state.lastActivity) / 60000;
  if (idleMinutes < state.minutes) return;
  state.awaySet = true;
  log('info', `idle for ${Math.round(idleMinutes)} min — going away`);
  await setStatus(`незаймай — ${Math.round(idleMinutes)} хв без активності`);
  call('ui.notify', { title: 'Autoaway', text: 'встановлено «Незаймай»', level: 'info' });
}

async function handle(msg) {
  switch (msg.method) {
    case 'plugin.hello': {
      protocol = msg.params?.protocol ?? 1;
      pluginName = msg.params?.plugin ?? pluginName;
      log('info', `handshake with Aurora ${msg.params?.version} (protocol ${protocol})`);
      reply(msg, { ok: true });
      break;
    }
    case 'plugin.load':
      log('info', `loaded; away after ${state.minutes} min of silence`);
      reply(msg, { ok: true });
      break;
    case 'plugin.unload':
      log('info', 'unloading');
      reply(msg, { ok: true });
      process.exit(0);
      break;
    case 'event': {
      const ev = msg.params ?? {};
      if (ev.name === 'message.new' || ev.name === 'message.edited') {
        if (ev.data?.out) {
          state.lastActivity = Date.now();
          if (state.awaySet) {
            state.awaySet = false;
            await setStatus('на зв’язку');
          }
        }
      }
      break;
    }
    case 'command': {
      const p = msg.params ?? {};
      if (p.name === 'away') {
        const arg = (p.text || '').trim();
        if (arg === 'off') state.enabled = false;
        else if (arg === 'on') state.enabled = true;
        else if (/^\d+$/.test(arg)) {
          state.minutes = Number(arg);
          state.enabled = true;
        }
        reply(msg, state.enabled
          ? `Авто-«Незаймай» увімкнено: ${state.minutes} хв тиші`
          : 'Авто-«Незаймай» вимкнено');
      } else {
        reply(msg, null, `unknown command ${p.name}`);
      }
      break;
    }
    default:
      break;
  }
}

const rl = createInterface({ input: process.stdin });
rl.on('line', (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    log('error', `bad json: ${err.message}`);
    return;
  }
  Promise.resolve(handle(msg)).catch((err) => log('error', err.stack || String(err)));
});

setInterval(maybeSetAway, 30_000).unref?.();
setInterval(() => {}, 1 << 30); // stay alive even when idle
