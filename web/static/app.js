(() => {
'use strict';

/* ---------- tiny DOM helper ---------- */
function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'html') el.innerHTML = v; // only used for static SVG icons below
    else if (k in el && k !== 'list') { try { el[k] = v; } catch { el.setAttribute(k, v); } }
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

/* replaceChildren that tolerates null/false/arrays */
function put(el, ...kids) {
  el.replaceChildren(...kids.flat(Infinity).filter((k) => k != null && k !== false));
}

const ICON = {
  dash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/></svg>',
  jobs: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12m0 0l-4-4m4 4l4-4"/><path d="M4 17v2a2 2 0 002 2h12a2 2 0 002-2v-2"/></svg>',
  copy: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V6a2 2 0 012-2h9"/></svg>',
  mirror: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v18"/><path d="M8 7l-5 5 5 5V7z"/><path d="M16 7l5 5-5 5V7z"/></svg>',
  infra: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="6" rx="1.5"/><rect x="3" y="14" width="18" height="6" rx="1.5"/><path d="M7 7h.01M7 17h.01"/></svg>',
  explore: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"/><circle cx="12" cy="13" r="2.5"/><path d="M14 15l2.5 2.5"/></svg>',
  logs: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/></svg>',
  cog: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 00.3 1.8l.1.1a2 2 0 11-2.8 2.8l-.1-.1a1.7 1.7 0 00-1.8-.3 1.7 1.7 0 00-1 1.5V21a2 2 0 11-4 0v-.1a1.7 1.7 0 00-1.1-1.5 1.7 1.7 0 00-1.8.3l-.1.1a2 2 0 11-2.8-2.8l.1-.1a1.7 1.7 0 00.3-1.8 1.7 1.7 0 00-1.5-1H3a2 2 0 110-4h.1a1.7 1.7 0 001.5-1.1 1.7 1.7 0 00-.3-1.8l-.1-.1a2 2 0 112.8-2.8l.1.1a1.7 1.7 0 001.8.3H9a1.7 1.7 0 001-1.5V3a2 2 0 114 0v.1a1.7 1.7 0 001 1.5 1.7 1.7 0 001.8-.3l.1-.1a2 2 0 112.8 2.8l-.1.1a1.7 1.7 0 00-.3 1.8V9a1.7 1.7 0 001.5 1H21a2 2 0 110 4h-.1a1.7 1.7 0 00-1.5 1z"/></svg>',
  logo: '<svg viewBox="0 0 32 32"><rect width="32" height="32" rx="7" fill="#4f46e5"/><path d="M16 6l9 3.5v6.2c0 5-3.6 8.6-9 10.3-5.4-1.7-9-5.3-9-10.3V9.5z" fill="none" stroke="#fff" stroke-width="2.2" stroke-linejoin="round"/><path d="M12 16l3 3 5-6" fill="none" stroke="#fff" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
};
const icon = (n) => h('span', { html: ICON[n], style: 'display:contents' });

/* ---------- api ---------- */
let onUnauth = () => {};
async function api(path, method = 'GET', body) {
  const opt = { method, headers: {}, credentials: 'same-origin' };
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  else if (method !== 'GET' && method !== 'DELETE') opt.headers['Content-Type'] = 'application/json', opt.body = '{}';
  const r = await fetch('/api' + path, opt);
  let data = null;
  try { data = await r.json(); } catch {}
  if (r.status === 401 && path !== '/login') { onUnauth(); throw new Error('Session expired'); }
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
}

function toast(msg, err) {
  const t = h('div', { class: 'toast' + (err ? ' err' : '') }, msg);
  document.getElementById('toasts').append(t);
  setTimeout(() => t.remove(), err ? 7000 : 3500);
}
const guard = (fn) => async (...a) => { try { return await fn(...a); } catch (e) { toast(e.message, true); } };

/* ---------- formatting ---------- */
const pad = (n) => String(n).padStart(2, '0');
function ago(ts) {
  if (!ts) return '—';
  const s = Math.floor(Date.now() / 1000 - ts);
  if (s < 0) return 'in ' + dur(-s);
  if (s < 45) return 'just now';
  return dur(s) + ' ago';
}
function dur(s) {
  s = Math.round(s);
  if (s < 90) return s + 's';
  if (s < 5400) return Math.round(s / 60) + 'm';
  if (s < 172800) return Math.round(s / 3600) + 'h';
  return Math.round(s / 86400) + 'd';
}
const fmtTime = (ts) => ts ? new Date(ts * 1000).toLocaleString() : '—';
function fmtBytes(n) {
  if (n == null || isNaN(n)) return '—';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 2 : 1)) + ' ' + u[i];
}
const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
function cronHuman(c) {
  if (!c) return 'Manual only';
  let m;
  if ((m = /^(\d+) \* \* \* \*$/.exec(c))) return `Hourly at :${pad(m[1])}`;
  if ((m = /^(\d+) \*\/(\d+) \* \* \*$/.exec(c))) return `Every ${m[2]} hours`;
  if ((m = /^(\d+) (\d+) \* \* \*$/.exec(c))) return `Daily at ${pad(m[2])}:${pad(m[1])}`;
  if ((m = /^(\d+) (\d+) \* \* (\d)$/.exec(c))) return `${DAYS[m[3]]}s at ${pad(m[2])}:${pad(m[1])}`;
  return c;
}
const STATUS_LABEL = { success: '✓ Success', warning: '! Warning', failed: '✕ Failed', running: 'Running', queued: 'Queued', unknown: 'No runs yet', off: 'Off', info: 'Info', error: 'Error', debug: 'Debug' };
const chip = (s, label) => h('span', { class: 'chip ' + (s || 'unknown') }, label || STATUS_LABEL[s || 'unknown'] || s);
const hist = (arr) => h('span', { class: 'hist', title: 'Last runs (oldest → newest)' },
  Array.from({ length: 14 }, (_, i) => { const v = arr && arr[arr.length - 14 + i]; return h('i', { class: v || '' }); }));
const nameOf = (list, id) => (list.find((x) => x.id === id) || {}).name || '?';

/* ---------- modal ---------- */
function modal(title, content, buttons) {
  const bg = h('div', { class: 'modal-bg', onclick: (e) => { if (e.target === bg) bg.remove(); } });
  const close = () => bg.remove();
  bg.append(h('div', { class: 'modal' + (content.classList && content.classList.contains('wide') ? ' wide' : ''), role: 'dialog', 'aria-label': title },
    h('h2', {}, title), content,
    h('div', { class: 'actions', style: 'margin-top:16px;justify-content:flex-end' },
      (buttons || []).map((b) => h('button', { class: 'btn ' + (b.cls || ''), onclick: () => b.fn(close) }, b.label)),
      h('button', { class: 'btn', onclick: close }, buttons ? 'Cancel' : 'Close'))));
  const esc = (e) => { if (e.key === 'Escape') { bg.remove(); document.removeEventListener('keydown', esc); } };
  document.addEventListener('keydown', esc);
  document.body.append(bg);
  const first = bg.querySelector('input,select,textarea'); if (first) first.focus();
  return close;
}

/* ---------- form helpers ---------- */
function field(label, input, hint) {
  return h('label', { class: 'f' }, label, hint ? h('span', { class: 'hint' }, hint) : null, input);
}
const select = (opts, val) => {
  const s = h('select', {}, opts.map(([v, l]) => h('option', { value: v, selected: v === val }, l)));
  s.value = val; return s;
};

/* ---------- schedule picker ---------- */
function schedulePicker(initial, { allowManual = true } = {}) {
  let type = 'manual', min = 0, time = '02:00', dow = '0', every = '6', custom = initial || '';
  let m;
  if (!initial) type = 'manual';
  else if ((m = /^(\d+) \* \* \* \*$/.exec(initial))) { type = 'hourly'; min = +m[1]; }
  else if ((m = /^(\d+) \*\/(\d+) \* \* \*$/.exec(initial))) { type = 'every'; every = m[2]; min = +m[1]; }
  else if ((m = /^(\d+) (\d+) \* \* \*$/.exec(initial))) { type = 'daily'; time = `${pad(m[2])}:${pad(m[1])}`; }
  else if ((m = /^(\d+) (\d+) \* \* (\d)$/.exec(initial))) { type = 'weekly'; time = `${pad(m[2])}:${pad(m[1])}`; dow = m[3]; }
  else type = 'custom';
  const types = [['manual', 'Manual only'], ['hourly', 'Hourly'], ['every', 'Every N hours'], ['daily', 'Daily'], ['weekly', 'Weekly'], ['custom', 'Custom cron']];
  const typeSel = select(allowManual ? types : types.slice(1), type);
  const timeIn = h('input', { type: 'time', value: time });
  const minIn = h('input', { type: 'number', min: 0, max: 59, value: min });
  const dowSel = select(DAYS.map((d, i) => [String(i), d]), dow);
  const everySel = select(['2', '3', '4', '6', '8', '12'].map((v) => [v, v + ' hours']), every);
  const customIn = h('input', { type: 'text', value: custom, placeholder: '*/15 * * * *' });
  const wrap = h('div', { style: 'display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin-top:6px' });
  const preview = h('div', { class: 'hint small muted', style: 'margin-top:4px;font-weight:400' });
  const get = () => {
    const [hh, mm] = (timeIn.value || '02:00').split(':').map(Number);
    switch (typeSel.value) {
      case 'manual': return '';
      case 'hourly': return `${+minIn.value || 0} * * * *`;
      case 'every': return `${+minIn.value || 0} */${everySel.value} * * *`;
      case 'daily': return `${mm} ${hh} * * *`;
      case 'weekly': return `${mm} ${hh} * * ${dowSel.value}`;
      default: return customIn.value.trim();
    }
  };
  const draw = () => {
    const t = typeSel.value;
    put(wrap, typeSel,
      (t === 'daily' || t === 'weekly') && [t === 'weekly' && dowSel, h('span', {}, 'at'), timeIn],
      (t === 'hourly' || t === 'every') && [t === 'every' && everySel, h('span', {}, 'at minute'), minIn],
      t === 'custom' && customIn);
    preview.textContent = t === 'manual' ? 'Runs only when you start it.' : `Cron: ${get()}  ·  ${cronHuman(get())}`;
  };
  [typeSel, timeIn, minIn, dowSel, everySel, customIn].forEach((e) => { e.style.marginTop = '0'; e.style.width = 'auto'; e.addEventListener('input', draw); });
  customIn.style.width = '180px';
  draw();
  return { el: h('div', {}, wrap, preview), get };
}

/* ---------- routing ---------- */
const state = { timers: [], agents: [] };
function clearTimers() { state.timers.forEach(clearInterval); state.timers = []; }
function every(fn, ms) { state.timers.push(setInterval(fn, ms)); }

const NAV = [['#/', 'Dashboard', 'dash'], ['#/jobs', 'Backup jobs', 'jobs'], ['#/explorer', 'Backup explorer', 'explore'], ['#/copies', 'Copy jobs', 'copy'], ['#/mirrors', 'Mirror jobs', 'mirror'], ['#/infra', 'Infrastructure', 'infra'], ['#/logs', 'Logs', 'logs'], ['#/settings', 'Settings', 'cog']];

function shell(main, active) {
  return h('div', { class: 'shell' },
    h('aside', { class: 'side' },
      h('div', { class: 'brand' }, h('span', { html: ICON.logo, style: 'display:contents' }), 'Vaultkeeper'),
      h('nav', { class: 'nav' }, NAV.map(([href, label, ic]) =>
        h('a', { href, class: active === href ? 'active' : '' }, icon(ic), label))),
      h('div', { class: 'foot' }, h('button', { class: 'btn sm', onclick: guard(async () => { await api('/logout', 'POST'); showLogin(); }) }, 'Sign out'))),
    h('main', { class: 'main' }, main));
}

let renderToken = 0;
async function route() {
  clearTimers();
  const token = ++renderToken;
  const hash = location.hash || '#/';
  const parts = hash.slice(2).split('/');
  const top = parts[0] || '';
  const activeMap = { '': '#/', jobs: '#/jobs', explorer: '#/explorer', copies: '#/copies', mirrors: '#/mirrors', infra: '#/infra', logs: '#/logs', settings: '#/settings', runs: '#/logs' };
  const container = h('div', {});
  put($app, shell(container, activeMap[top] || '#/'));
  try {
    state.agents = await api('/agents');
    const pages = { '': pageDashboard, jobs: pageJobs, explorer: pageExplorer, copies: pageCopies, mirrors: pageMirrors, infra: pageInfra, logs: pageLogs, settings: pageSettings, runs: pageRun };
    const fn = pages[top] || pageDashboard;
    if (token === renderToken) await fn(container, parts.slice(1), token);
  } catch (e) {
    if (e.message !== 'Session expired') put(container, h('div', { class: 'note bad' }, e.message));
  }
}

/* ---------- login ---------- */
function showLogin() {
  clearTimers();
  const err = h('div', { class: 'note bad hidden' });
  const pw = h('input', { type: 'password', autocomplete: 'current-password', required: true });
  const form = h('form', { onsubmit: async (e) => {
    e.preventDefault();
    try { await api('/login', 'POST', { password: pw.value }); route(); }
    catch (x) { err.textContent = x.message; err.classList.remove('hidden'); pw.select(); }
  } }, err, field('Admin password', pw), h('button', { class: 'btn primary', style: 'width:100%;justify-content:center;margin-top:16px' }, 'Sign in'));
  put($app, h('div', { class: 'login' }, h('div', { class: 'card' },
    h('div', { class: 'brand' }, h('span', { html: ICON.logo, style: 'display:contents' }), 'Vaultkeeper'), form)));
  pw.focus();
}

/* ---------- dashboard ---------- */
async function pageDashboard(c, _, token) {
  const draw = async () => {
    const d = await api('/dashboard');
    if (token !== renderToken) return;
    const cnt = d.counts || {};
    const tile = (n, l, cls) => h('div', { class: 'tile' }, h('div', { class: 'n', style: cls ? `color:var(--${cls})` : '' }, n), h('div', { class: 'l' }, l));
    const a = d.agents;
    put(c, 
      h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Dashboard'), h('div', { class: 'sub' }, 'Status of every backup, backup test and copy job')), h('div', { class: 'right actions' },
        d.active ? chip('running', d.active + ' active') : null,
        h('a', { class: 'btn primary', href: '#/jobs/new' }, '+ New backup job'))),
      h('div', { class: 'grid tiles' },
        tile(cnt.success || 0, 'Healthy', 'ok'),
        tile(cnt.warning || 0, 'Warnings', cnt.warning ? 'warn' : ''),
        tile(cnt.failed || 0, 'Failing', cnt.failed ? 'bad' : ''),
        tile(`${a.online}/${a.total}`, 'Agents online', a.online < a.total ? 'warn' : ''),
        tile(a.dest_total ? fmtBytes(a.dest_free) : '—', a.dest_total ? `Free of ${fmtBytes(a.dest_total)} on destinations` : 'Destination space')),
      jobsTable(d.jobs),
      d.copies.length ? copiesCard(d.copies) : null,
      d.mirrors && d.mirrors.length ? mirrorsTable(d.mirrors) : null,
      h('div', { class: 'card' }, h('h2', {}, 'Recent warnings & errors'),
        d.problems.length ? h('div', { class: 'log' }, d.problems.map(logRow)) : h('div', { class: 'muted' }, 'Nothing to report. 🎉')));
  };
  await draw();
  every(guard(draw), 5000);
}

function runCell(r, test) {
  if (!r) return h('span', { class: 'muted' }, test ? '—' : 'Never run');
  const d = r.finished && r.started ? ` · ${dur(r.finished - r.started)}` : '';
  return h('a', { href: '#/runs/' + r.id, title: fmtTime(r.created) + (r.message ? '\n' + r.message : ''), style: 'color:inherit' },
    chip(r.status), h('div', { class: 'small muted' }, ago(r.finished || r.started || r.created) + d));
}

function jobsTable(jobs) {
  if (!jobs.length) return h('div', { class: 'card' }, h('div', { class: 'empty' }, h('p', {}, 'No backup jobs yet.'), h('a', { class: 'btn primary', href: '#/jobs/new' }, 'Create your first job')));
  return h('div', { class: 'card flush' },
    h('div', { class: 'card-head' }, h('h2', {}, 'Backup jobs')),
    h('div', { class: 'tablewrap' }, h('table', {},
      h('thead', {}, h('tr', {}, ['Job', 'Schedule', 'Last backup', 'Backup test', 'History', ''].map((t) => h('th', {}, t)))),
      h('tbody', {}, jobs.map((j) => h('tr', { class: 'click', onclick: (e) => { if (!e.target.closest('a,button')) location.hash = '#/jobs/' + j.id; } },
        h('td', {}, h('div', { class: 'name' }, j.name, !j.enabled && h('span', { class: 'muted small' }, ' (paused)')),
          h('div', { class: 'route' }, h('span', { class: 'dot ' + (j.source_online ? 'on' : 'off'), title: j.source_online ? 'online' : 'offline' }), j.source_name, '→',
            h('span', { class: 'dot ' + (j.dest_online ? 'on' : 'off'), title: j.dest_online ? 'online' : 'offline' }), j.dest_name)),
        h('td', {}, h('div', {}, cronHuman(j.schedule)), h('div', { class: 'small muted' }, j.enabled && j.next_backup ? 'next ' + ago(j.next_backup) : '')),
        h('td', {}, runCell(j.last_backup)),
        h('td', {}, j.test_mode === 'off' ? chip('off', 'Disabled') : runCell(j.last_test, true), j.test_mode !== 'off' && j.next_test ? h('div', { class: 'small muted' }, 'next ' + ago(j.next_test)) : null),
        h('td', {}, hist(j.history)),
        h('td', {}, h('button', { class: 'btn sm', onclick: guard(async () => { await api(`/jobs/${j.id}/run`, 'POST', { kind: 'backup' }); toast('Backup started'); route(); }) }, 'Run now'))))))));
}

function copiesCard(cs) {
  return h('div', { class: 'card flush' }, h('div', { class: 'card-head' }, h('h2', {}, 'Copy jobs')),
    h('div', { class: 'tablewrap' }, h('table', {}, h('thead', {}, h('tr', {}, ['Copy', 'Source job → destination', 'Schedule', 'Last copy', 'History'].map((t) => h('th', {}, t)))),
      h('tbody', {}, cs.map((x) => h('tr', {}, h('td', { class: 'name' }, x.name), h('td', {}, x.source_job + ' → ' + x.dest_name),
        h('td', {}, cronHuman(x.schedule), x.next_run ? h('div', { class: 'small muted' }, 'next ' + ago(x.next_run)) : null),
        h('td', {}, runCell(x.last_run)), h('td', {}, hist(x.history))))))));
}

function logRow(l) {
  return h('div', {}, h('span', { class: 't', title: new Date(l.ts).toLocaleString() }, new Date(l.ts).toLocaleTimeString()), h('span', { class: 'lv-' + l.level }, l.level),
    h('span', { class: 'm' }, l.job_name ? h('b', {}, l.job_name + ': ') : null, l.run_id ? h('a', { href: '#/runs/' + l.run_id }, l.message) : l.message));
}

/* ---------- wake target & post-completion script ---------- */
function hooksFieldset(initial, { defaultAgent }) {
  const agents = state.agents.map((x) => x.agent);
  const w = (initial && initial.wake) || {}, po = (initial && initial.post) || {};
  const agentSel = (val) => select(agents.map((a) => [a.id, a.name]), val || defaultAgent || (agents[0] || {}).id);
  const scriptsOff = (id) => { const a = agents.find((x) => x.id === id); return a && !a.allow_scripts; };

  const f = {
    wakeOn: h('input', { type: 'checkbox', checked: !!w.enabled }),
    wakeAgent: agentSel(w.agent),
    method: select([['wol', 'Wake-on-LAN (magic packet)'], ['command', 'Custom command']], w.method || 'wol'),
    mac: h('input', { type: 'text', value: w.mac || '', placeholder: 'aa:bb:cc:dd:ee:ff' }),
    bcast: h('input', { type: 'text', value: w.broadcast || '', placeholder: 'optional, e.g. 192.168.1.255' }),
    cmd: h('textarea', { placeholder: 'curl -s http://smartplug.lan/relay/0?turn=on', value: w.command || '' }),
    ready: select([['browse', 'It is browseable — a folder on the agent lists (recommended)'], ['ping', 'It answers ping'], ['tcp', 'A TCP port is open (e.g. 445 SMB, 2049 NFS, 22 SSH)'], ['command', 'A command succeeds']], w.ready || 'browse'),
    path: h('input', { type: 'text', value: w.path || '', placeholder: '/mnt/nas/backups' }),
    marker: h('input', { type: 'text', value: w.marker || '', placeholder: 'optional, e.g. .vk-ready' }),
    tryMount: h('input', { type: 'checkbox', checked: !!w.try_mount }),
    host: h('input', { type: 'text', value: w.host || '', placeholder: 'nas.lan or 192.168.1.20' }),
    port: h('input', { type: 'number', min: 1, max: 65535, value: w.port || 445 }),
    rcmd: h('textarea', { placeholder: 'test -d /mnt/nas/backups', value: w.ready_command || '' }),
    settle: h('input', { type: 'number', min: 0, max: 3600, value: w.settle_sec || 0 }),
    timeout: h('input', { type: 'number', min: 1, max: 240, value: w.timeout_min || 10 }),
    postOn: h('input', { type: 'checkbox', checked: !!po.enabled }),
    postAgent: agentSel(po.agent || w.agent),
    when: select([['success', 'After a successful run (warnings count as success)'], ['always', 'After every run, even failed ones']], po.on || 'success'),
    ptimeout: h('input', { type: 'number', min: 5, max: 3600, value: po.timeout_sec || 120 }),
    script: h('textarea', { style: 'min-height:110px', placeholder: '# example: power the NAS off over SSH\nssh -o BatchMode=yes admin@nas.lan poweroff', value: po.script || '' }),
    target: h('input', { type: 'text', value: (initial && initial.target) || '', placeholder: 'optional, e.g. Main NAS' }),
  };
  const wakeBox = h('div', { style: 'margin-top:12px' }), postBox = h('div', { style: 'margin-top:12px' });
  const scriptWarn = h('div', { class: 'note warn hidden' });
  const draw = () => {
    wakeBox.classList.toggle('hidden', !f.wakeOn.checked);
    postBox.classList.toggle('hidden', !f.postOn.checked);
    const m = f.method.value, r = f.ready.value;
    put(wakeBox,
      h('div', { class: 'row' }, field('Send the wake-up from agent', f.wakeAgent, 'It must be online and on the same network segment as the target (Docker bridge networking blocks broadcasts).'), field('How to wake it', f.method)),
      m === 'wol' ? h('div', { class: 'row' }, field('MAC address of the target', f.mac, 'Wake-on-LAN must be enabled on the target.'), field('Broadcast address', f.bcast, 'Leave empty to broadcast on every network interface.')) : field('Wake command', f.cmd, 'Runs on that agent (e.g. a smart-plug or IPMI call).'),
      h('div', { class: 'row' }, field('Start the job when…', f.ready, 'If the target already responds nothing is sent and the job starts immediately.')),
      r === 'browse' ? h('div', { class: 'row' }, field('Folder that must list', f.path, 'Absolute path on that agent, e.g. where the NAS share is mounted. An empty, unmounted folder does not count.'), field('Marker file', f.marker, 'Optionally require this file inside the folder.'),
        h('label', { class: 'check', style: 'align-self:end;margin-bottom:8px' }, f.tryMount, 'Try mounting it while waiting (needs an /etc/fstab entry)')) : null,
      r === 'ping' ? h('div', { class: 'row' }, field('Host to ping', f.host)) : null,
      r === 'tcp' ? h('div', { class: 'row' }, field('Host', f.host), field('Port', f.port)) : null,
      r === 'command' ? h('div', { class: 'row' }, field('Readiness command', f.rcmd, 'Exit status 0 means ready. Runs on that agent.')) : null,
      h('div', { class: 'row' }, field('…then wait this many extra seconds', f.settle, 'Usually 0 — the checks above already wait for the real thing.'), field('Give up after (minutes)', f.timeout, 'A safety net only; the job fails with a clear message if the target never comes up.')));
    put(postBox,
      h('div', { class: 'row' }, field('Run on agent', f.postAgent), field('Run', f.when), field('Script timeout (seconds)', f.ptimeout)),
      field('Script', f.script, 'Runs with /bin/sh (Linux/macOS) or PowerShell (Windows). Variables: VK_JOB_NAME, VK_RUN_KIND, VK_STATUS, VK_MESSAGE, VK_TARGET, VK_RUN_ID. Output is added to the run log. Skipped while another job that shares this target is still running or queued.'));
    const needs = [];
    if (f.wakeOn.checked && (f.method.value === 'command' || f.ready.value === 'command') && scriptsOff(f.wakeAgent.value)) needs.push(agents.find((a) => a.id === f.wakeAgent.value).name);
    if (f.postOn.checked && scriptsOff(f.postAgent.value)) needs.push(agents.find((a) => a.id === f.postAgent.value).name);
    scriptWarn.classList.toggle('hidden', !needs.length);
    scriptWarn.textContent = needs.length ? `Scripts are switched off on ${[...new Set(needs)].join(', ')}. Restart that agent with --allow-scripts (or VK_ALLOW_SCRIPTS=1) first — it's a deliberate opt-in because a script gives the manager command execution on that machine. It can't be enabled remotely.` : '';
  };
  Object.values(f).forEach((e) => e.addEventListener && e.addEventListener('input', draw));
  draw();
  const el = h('fieldset', {}, h('legend', {}, 'Wake target & post-completion script'),
    h('div', { class: 'note' }, 'For a target that sleeps or is switched off (a NAS, a backup PC): wake it before the job and shut it down afterwards. Applies to every task that needs the target (backups, tests, restores…).'),
    h('label', { class: 'check' }, f.wakeOn, 'Wake the backup target before running'), wakeBox,
    h('label', { class: 'check', style: 'margin-top:16px' }, f.postOn, 'Run a script when the job has finished (e.g. shut the NAS down)'), postBox,
    scriptWarn,
    h('div', { class: 'row', style: 'margin-top:12px' }, field('Target name', f.target, 'Optional. Jobs using the same MAC/host — or the same name — share a target, so the script only runs once the last of them has finished.')));
  const num = (e, d) => { const n = +e.value; return Number.isFinite(n) ? n : d; };
  return { el, get: () => {
    if (!f.wakeOn.checked && !f.postOn.checked) return null;
    return { target: f.target.value.trim(),
      wake: f.wakeOn.checked ? { enabled: true, agent: f.wakeAgent.value, method: f.method.value, mac: f.mac.value, broadcast: f.bcast.value, command: f.cmd.value, ready: f.ready.value,
        host: f.host.value.trim(), port: num(f.port, 0), path: f.path.value.trim(), marker: f.marker.value.trim(), try_mount: f.tryMount.checked, ready_command: f.rcmd.value,
        settle_sec: num(f.settle, 0), timeout_min: num(f.timeout, 10) } : { enabled: false },
      post: f.postOn.checked ? { enabled: true, agent: f.postAgent.value, script: f.script.value, on: f.when.value, timeout_sec: num(f.ptimeout, 120) } : { enabled: false } };
  } };
}

// Read-only summary rows for detail pages.
function hooksRows(hk) {
  if (!hk || !((hk.wake && hk.wake.enabled) || (hk.post && hk.post.enabled))) return null;
  const rows = [];
  const ag = (id) => (state.agents.map((x) => x.agent).find((a) => a.id === id) || {}).name || '?';
  if (hk.wake && hk.wake.enabled) {
    const w = hk.wake;
    const ready = { browse: `${w.path} lists${w.marker ? ' (marker ' + w.marker + ')' : ''}`, ping: `${w.host} answers ping`, tcp: `${w.host}:${w.port} is open`, command: 'readiness command succeeds' }[w.ready] || w.ready;
    rows.push(h('dt', {}, 'Wake target'), h('dd', {}, `${w.method === 'wol' ? 'Wake-on-LAN ' + w.mac : 'custom command'} from ${ag(w.agent)}; starts when ${ready}`, w.settle_sec ? `, +${w.settle_sec}s` : '', h('span', { class: 'muted' }, ` · gives up after ${w.timeout_min} min`)));
  }
  if (hk.post && hk.post.enabled) {
    rows.push(h('dt', {}, 'After completion'), h('dd', {}, `Script on ${ag(hk.post.agent)} (${hk.post.on === 'always' ? 'after every run' : 'after success'})`, h('pre', { class: 'mono', style: 'margin:6px 0 0;white-space:pre-wrap' }, hk.post.script)));
  }
  if (hk.target) rows.push(h('dt', {}, 'Target name'), h('dd', {}, hk.target));
  return rows;
}

/* ---------- jobs ---------- */
async function pageJobs(c, p, token) {
  if (p[0] === 'new') return jobForm(c, null);
  if (p[0] && p[1] === 'edit') return jobForm(c, await api('/jobs/' + p[0]));
  if (p[0]) return jobDetail(c, p[0], p[1] || 'overview', token);
  const jobs = await api('/jobs');
  put(c, 
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Backup jobs'), h('div', { class: 'sub' }, 'Source → destination file backups, encrypted with restic')),
      h('a', { class: 'btn primary right', href: '#/jobs/new' }, '+ New backup job')),
    jobs.length ? jobsTable(jobs) : jobsTable([]));
}

async function jobDetail(c, id, tab, token) {
  const draw = async () => {
    const j = await api('/jobs/' + id);
    if (token !== renderToken) return;
    const tabs = [['overview', 'Overview'], ['restore', 'Restore points'], ['history', 'Run history']];
    const body = h('div', {});
    put(c, 
      h('div', { class: 'crumb' }, h('a', { href: '#/jobs' }, 'Backup jobs'), ' / ', j.name),
      h('div', { class: 'page-head' }, h('h1', {}, j.name), chip(j.health), !j.enabled && chip('off', 'Paused'),
        h('div', { class: 'right actions' },
          h('button', { class: 'btn primary', onclick: guard(async () => { await api(`/jobs/${id}/run`, 'POST', { kind: 'backup' }); toast('Backup queued'); draw(); }) }, 'Run backup'),
          h('button', { class: 'btn', disabled: !j.initialized || j.test_mode === 'off', title: !j.initialized ? 'Run a backup first' : '', onclick: guard(async () => { await api(`/jobs/${id}/run`, 'POST', { kind: 'test' }); toast('Backup test queued'); draw(); }) }, 'Run backup test'),
          j.initialized ? h('a', { class: 'btn', href: `#/explorer/${id}` }, 'Browse & restore') : null,
          h('button', { class: 'btn', disabled: !j.initialized || !j.keep_last, title: !j.initialized ? 'Run a backup first' : !j.keep_last ? 'No restore-point limit is set (unlimited)' : `Keep only the newest ${j.keep_last} restore points`, onclick: guard(async () => {
            if (!confirm(`Remove restore points beyond the newest ${j.keep_last} and free their space? This cannot be undone.`)) return;
            const r = await api(`/jobs/${id}/run`, 'POST', { kind: 'prune' }); toast('Prune started'); location.hash = '#/runs/' + r.run_id;
          }) }, 'Prune now'),
          h('a', { class: 'btn', href: `#/jobs/${id}/edit` }, 'Edit'))),
      h('div', { class: 'tabs' }, tabs.map(([k, l]) => h('a', { href: `#/jobs/${id}` + (k === 'overview' ? '' : '/' + k), class: tab === k ? 'on' : '' }, l))),
      body);
    if (tab === 'restore') await restorePoints(body, j);
    else if (tab === 'history') await runHistory(body, id);
    else overview(body, j);
  };
  await draw();
  if (tab === 'overview') every(guard(draw), 6000);
}

function overview(body, j) {
  const lr = (r) => r ? h('div', {}, chip(r.status), ' ', h('a', { href: '#/runs/' + r.id }, fmtTime(r.finished || r.created)), r.message ? h('div', { class: 'small muted' }, r.message) : null) : h('span', { class: 'muted' }, 'Never');
  const s = (j.last_backup && j.last_backup.summary) || {};
  const t = (j.last_test && j.last_test.summary) || {};
  body.append(h('div', { class: 'cols' },
    h('div', { class: 'card' }, h('h2', {}, 'Configuration'), h('dl', { class: 'kv' },
      h('dt', {}, 'Source'), h('dd', {}, j.source_name, ' ', h('span', { class: 'dot ' + (j.source_online ? 'on' : 'off') })),
      h('dt', {}, 'Destination'), h('dd', {}, j.dest_name, ' ', h('span', { class: 'dot ' + (j.dest_online ? 'on' : 'off') })),
      h('dt', {}, 'Paths'), h('dd', {}, j.paths.map((x) => h('div', {}, h('code', {}, x)))),
      j.excludes.length ? [h('dt', {}, 'Exclusions'), h('dd', {}, j.excludes.map((x) => h('div', {}, h('code', {}, x))))] : null,
      j.mount ? [h('dt', {}, 'Network share'), h('dd', {}, `${j.mount.type.toUpperCase()} ${j.mount.remote}`)] : null,
      h('dt', {}, 'Schedule'), h('dd', {}, cronHuman(j.schedule), j.enabled && j.next_backup ? h('span', { class: 'muted' }, ` · next ${fmtTime(j.next_backup)}`) : null),
      h('dt', {}, 'Restore points kept'), h('dd', {}, j.keep_last ? `Last ${j.keep_last}` : 'Unlimited'),
      h('dt', {}, 'Compression'), h('dd', {}, j.compression),
      h('dt', {}, 'Bandwidth limit'), h('dd', {}, j.bandwidth_kb ? j.bandwidth_kb + ' KB/s' : 'None'),
      hooksRows(j.hooks),
      h('dt', {}, 'Repository size'), h('dd', {}, j.repo_size ? fmtBytes(j.repo_size) : '—'),
      h('dt', {}, 'Encryption'), h('dd', {}, 'AES-256 (restic) ', h('button', { class: 'btn sm', onclick: guard(async () => showKey(await api(`/jobs/${j.id}/key`), j.name)) }, 'Show key')))),
    h('div', {},
      h('div', { class: 'card' }, h('h2', {}, 'Last backup'), lr(j.last_backup), h('div', { style: 'margin-top:10px' }, hist(j.history)),
        s.snapshot_id ? h('dl', { class: 'kv', style: 'margin-top:12px' },
          h('dt', {}, 'Snapshot'), h('dd', {}, h('code', {}, String(s.snapshot_id).slice(0, 8))),
          h('dt', {}, 'Files'), h('dd', {}, `${s.total_files} (${s.files_new} new, ${s.files_changed} changed)`),
          h('dt', {}, 'Source size'), h('dd', {}, fmtBytes(s.total_bytes)),
          h('dt', {}, 'Data added'), h('dd', {}, fmtBytes(s.data_added)),
          h('dt', {}, 'Duration'), h('dd', {}, (s.duration_s || 0).toFixed(1) + 's')) : null),
      h('div', { class: 'card' }, h('h2', {}, 'Backup test'),
        j.test_mode === 'off' ? h('div', { class: 'muted' }, 'Automatic backup tests are disabled for this job.') : [
          lr(j.last_test), h('div', { style: 'margin:10px 0' }, hist(j.test_history)),
          h('dl', { class: 'kv' },
            h('dt', {}, 'Mode'), h('dd', {}, j.test_mode === 'random' ? 'Random file (chosen at first backup)' : 'You chose the file'),
            h('dt', {}, 'Test file'), h('dd', {}, j.test_path ? h('code', {}, j.test_path) : h('span', { class: 'muted' }, 'chosen after the first backup')),
            h('dt', {}, 'Schedule'), h('dd', {}, cronHuman(j.test_schedule), j.next_test ? h('span', { class: 'muted' }, ` · next ${fmtTime(j.next_test)}`) : null),
            h('dt', {}, 'Repo data check'), h('dd', {}, j.check_data_pct ? `${j.check_data_pct}% of pack data read & verified` : 'Structure & index only'),
            t.sha256 ? [h('dt', {}, 'Last restored'), h('dd', {}, `${fmtBytes(t.size)}, sha256 `, h('code', {}, String(t.sha256).slice(0, 16) + '…'))] : null)]))));
}

function showKey(k, name) {
  modal(`Encryption key — ${name}`, h('div', {},
    h('div', { class: 'note warn' }, 'Store this somewhere safe (password manager). Without it the backups cannot be restored, and it cannot be recovered from the repository.'),
    h('dl', { class: 'kv' }, h('dt', {}, 'Repository'), h('dd', {}, h('code', {}, k.repo)), h('dt', {}, 'Password'), h('dd', {}, h('code', { style: 'user-select:all' }, k.password)))),
    [{ label: 'Copy password', cls: 'primary', fn: (close) => { navigator.clipboard && navigator.clipboard.writeText(k.password); toast('Copied'); } }]);
}

/* Delete dialog: optionally also deletes the stored data, behind a typed confirmation. */
function deleteDialog({ title, name, kindLabel, dataLabel, destName, base, back }) {
  const withData = h('input', { type: 'checkbox' });
  const typed = h('input', { type: 'text', placeholder: name, autocomplete: 'off' });
  const typedBox = h('div', { class: 'hidden', style: 'margin-top:10px' }, field(`Type “${name}” to confirm`, typed, 'This cannot be undone.'));
  withData.addEventListener('change', () => typedBox.classList.toggle('hidden', !withData.checked));
  modal(title, h('div', {},
    h('p', { style: 'margin-top:0' }, `Delete ${kindLabel} “${name}”? Its schedule stops and its configuration is removed.`),
    h('label', { class: 'check' }, withData, `Also permanently delete ${dataLabel}${destName ? ' on ' + destName : ''}`),
    h('div', { class: 'small muted', style: 'margin:4px 0 0 24px' }, 'Leave unticked to keep the data on the destination (you can remove orphaned backup repositories later from Infrastructure).'),
    typedBox),
    [{ label: 'Delete', cls: 'danger', fn: async (close) => {
      if (withData.checked && typed.value !== name) { toast('Type the exact name to delete the data', true); return; }
      try {
        const q = withData.checked ? `?delete_data=1&confirm=${encodeURIComponent(name)}` : '';
        const r = await api(base + q, 'DELETE');
        close(); toast(r.run_id ? 'Deleted; removing the stored data…' : 'Deleted');
        location.hash = r.run_id ? '#/runs/' + r.run_id : back;
      } catch (e) { toast(e.message, true); }
    } }]);
}

async function deleteSnapshot(jobId, snap, copyId) {
  if (!confirm(`Permanently delete the restore point from ${new Date(snap.time).toLocaleString()} (${snap.id.slice(0, 8)})? Files that only exist in it are lost. This cannot be undone.`)) return;
  try {
    const r = await api(`/jobs/${jobId}/snapshots/${snap.id.slice(0, 16)}` + (copyId ? `?copy=${copyId}` : ''), 'DELETE');
    toast('Deleting restore point…'); location.hash = '#/runs/' + r.run_id;
  } catch (e) { toast(e.message, true); }
}

async function restorePoints(body, j) {
  body.append(h('div', { class: 'muted' }, 'Loading restore points from the destination agent…'));
  try {
    const snaps = (await api(`/jobs/${j.id}/snapshots`)) || [];
    snaps.sort((a, b) => new Date(b.time) - new Date(a.time));
    put(body, h('div', { class: 'card flush' }, h('div', { class: 'card-head' }, h('h2', {}, `${snaps.length} restore point${snaps.length === 1 ? '' : 's'}`), h('span', { class: 'muted small' }, j.keep_last ? `keeping the last ${j.keep_last}` : 'no limit')),
      snaps.length ? h('table', {}, h('thead', {}, h('tr', {}, ['Snapshot', 'Taken', 'Host', 'Paths', ''].map((x) => h('th', {}, x)))),
        h('tbody', {}, snaps.map((s) => h('tr', {}, h('td', {}, h('code', {}, s.id.slice(0, 8))), h('td', {}, new Date(s.time).toLocaleString()), h('td', {}, s.hostname), h('td', {}, (s.paths || []).map((p) => h('div', {}, h('code', {}, p)))), h('td', {}, h('div', { class: 'actions', style: 'flex-wrap:nowrap' }, h('a', { class: 'btn sm', href: `#/explorer/${j.id}/${s.id.slice(0, 8)}` }, 'Browse'), h('button', { class: 'btn sm danger', onclick: () => deleteSnapshot(j.id, s, '') }, 'Delete'))))))) :
        h('div', { class: 'empty' }, 'No backups have completed yet.')));
  } catch (e) { put(body, h('div', { class: 'note bad' }, e.message)); }
}

async function runHistory(body, id) {
  const runs = await api(`/runs?job_id=${id}&limit=100`);
  put(body, runsTable(runs));
}

function runsTable(runs) {
  return h('div', { class: 'card flush' }, runs.length ? h('div', { class: 'tablewrap' }, h('table', {},
    h('thead', {}, h('tr', {}, ['When', 'Task', 'Result', 'Trigger', 'Duration', 'Agent', 'Message'].map((x) => h('th', {}, x)))),
    h('tbody', {}, runs.map((r) => h('tr', { class: 'click', onclick: () => location.hash = '#/runs/' + r.id },
      h('td', {}, fmtTime(r.created)), h('td', {}, r.kind + (r.copy_name ? ` (${r.copy_name})` : '')), h('td', {}, chip(r.status)),
      h('td', {}, r.trigger), h('td', {}, r.finished && r.started ? dur(r.finished - r.started) : '—'), h('td', {}, r.agent_name),
      h('td', { class: 'small' }, r.message || '')))))) : h('div', { class: 'empty' }, 'No runs yet.'));
}

/* job editor */
function jobForm(c, job) {
  const agents = state.agents.map((a) => a.agent);
  const srcs = agents.filter((a) => (a.roles || []).includes('source'));
  const dsts = agents.filter((a) => (a.roles || []).includes('dest'));
  const editing = !!job;
  const j = job || { enabled: true, paths: [], excludes: [], schedule: '0 2 * * *', keep_last: 30, compression: 'auto', test_mode: 'random', test_schedule: '0 6 * * 0', check_data_pct: 5, bandwidth_kb: 0 };
  const m = j.mount || {};
  const f = {
    name: h('input', { type: 'text', value: j.name || '', required: true, placeholder: 'e.g. File server documents' }),
    enabled: h('input', { type: 'checkbox', checked: j.enabled }),
    src: select(srcs.map((a) => [a.id, a.name]), j.source_agent || (srcs[0] || {}).id),
    dst: select(dsts.map((a) => [a.id, a.name + (a.online ? '' : ' (offline)')]), j.dest_agent || (dsts[0] || {}).id),
    paths: h('textarea', { placeholder: '/srv/data\n/home/user/documents', value: (j.paths || []).join('\n') }),
    excludes: h('textarea', { placeholder: '*.tmp\n/srv/data/cache', value: (j.excludes || []).join('\n') }),
    mtype: select([['', 'No — back up local paths'], ['smb', 'SMB / CIFS share'], ['nfs', 'NFS export']], m.type || ''),
    mremote: h('input', { type: 'text', value: m.remote || '', placeholder: '//nas.local/share   or   nas.local:/export' }),
    muser: h('input', { type: 'text', value: m.username || '' }),
    mpass: h('input', { type: 'password', value: '', placeholder: editing && m.type ? '(unchanged)' : '', autocomplete: 'new-password' }),
    mdomain: h('input', { type: 'text', value: m.domain || '' }),
    mopts: h('input', { type: 'text', value: m.options || '', placeholder: 'e.g. vers=3.0' }),
    keep: h('input', { type: 'number', min: 0, value: j.keep_last }),
    comp: select([['auto', 'Auto (recommended)'], ['max', 'Maximum'], ['off', 'Off']], j.compression),
    bw: h('input', { type: 'number', min: 0, value: j.bandwidth_kb || 0 }),
    tmode: select([['random', 'Random file (chosen at the first backup)'], ['manual', 'A file I choose'], ['off', 'Disabled']], j.test_mode),
    tpath: h('input', { type: 'text', value: j.test_path || '', placeholder: '/srv/data/important/config.db' }),
    pct: h('input', { type: 'number', min: 0, max: 100, value: j.check_data_pct }),
    pw: h('input', { type: 'password', autocomplete: 'new-password', placeholder: 'Leave blank to generate a strong key' }),
  };
  const sched = schedulePicker(j.schedule);
  const tsched = schedulePicker(j.test_schedule, { allowManual: false });
  const hk = hooksFieldset(j.hooks, { defaultAgent: j.dest_agent || f.dst.value });
  const mountBox = h('div', { class: 'row' });
  const tpathWrap = field('Test file path', f.tpath, 'Absolute path as it appears on the source. It must exist in the backup.');
  const tinfo = h('div', { class: 'note' }, j.test_path ? ['Current random test file: ', h('code', {}, j.test_path)] : 'A random non-empty file will be picked automatically after the first backup completes.');
  const sync = () => {
    put(mountBox, ...(f.mtype.value ? [field('Share', f.mremote), field('Username', f.muser), field('Password', f.mpass), field('Domain (SMB)', f.mdomain), field('Extra mount options', f.mopts)] : []));
    tpathWrap.classList.toggle('hidden', f.tmode.value !== 'manual');
    tinfo.classList.toggle('hidden', f.tmode.value !== 'random');
  };
  f.mtype.addEventListener('input', sync); f.tmode.addEventListener('input', sync); sync();
  const err = h('div', { class: 'note bad hidden' });
  const lines = (s) => s.split('\n').map((x) => x.trim()).filter(Boolean);
  const submit = async (e) => {
    e.preventDefault();
    const body = {
      name: f.name.value, enabled: f.enabled.checked, source_agent: f.src.value, dest_agent: f.dst.value,
      paths: lines(f.paths.value), excludes: lines(f.excludes.value), schedule: sched.get(), keep_last: +f.keep.value || 0,
      compression: f.comp.value, bandwidth_kb: +f.bw.value || 0, test_mode: f.tmode.value, test_path: f.tpath.value,
      test_schedule: tsched.get(), check_data_pct: +f.pct.value || 0, repo_password: f.pw.value, hooks: hk.get(),
      mount: f.mtype.value ? { type: f.mtype.value, remote: f.mremote.value, username: f.muser.value, password: f.mpass.value, domain: f.mdomain.value, options: f.mopts.value } : null,
    };
    try {
      const r = editing ? await api('/jobs/' + j.id, 'PUT', body) : await api('/jobs', 'POST', body);
      toast('Job saved'); location.hash = '#/jobs/' + r.id;
    } catch (x) { err.textContent = x.message; err.classList.remove('hidden'); err.scrollIntoView({ block: 'center' }); }
  };
  const noAgents = !srcs.length || !dsts.length;
  put(c, 
    h('div', { class: 'crumb' }, h('a', { href: '#/jobs' }, 'Backup jobs'), ' / ', editing ? j.name : 'New'),
    h('div', { class: 'page-head' }, h('h1', {}, editing ? 'Edit job' : 'New backup job')),
    noAgents ? h('div', { class: 'note warn' }, 'You need at least one source agent and one destination agent. ', h('a', { href: '#/infra' }, 'Deploy agents →')) : null,
    h('form', { class: 'card', onsubmit: submit }, err,
      h('fieldset', {}, h('legend', {}, 'General'), h('div', { class: 'row' }, field('Job name', f.name),
        h('div', { style: 'align-self:end;padding-bottom:8px' }, h('label', { class: 'check' }, f.enabled, 'Enabled (scheduled runs active)')))),
      h('fieldset', {}, h('legend', {}, 'Source & destination'), h('div', { class: 'row' },
        field('Source agent', f.src, 'Runs restic, compresses & encrypts locally'), field('Destination agent', f.dst, editing && j.initialized ? 'Locked after the first backup' : 'Receives data directly from the source')),
        h('div', { class: 'row' }, field('Paths to back up', f.paths, 'One per line'), field('Exclusions', f.excludes, 'One pattern per line')),
        h('div', { class: 'row' }, field('Is the data on a network share?', f.mtype, 'Use when the agent runs near the data instead of on it. The agent needs mount privileges. Paths above are then relative to the share root.')),
        mountBox),
      h('fieldset', {}, h('legend', {}, 'Schedule & retention'), h('div', { class: 'row' }, field('Backup schedule', sched.el),
        field('Maximum restore points', f.keep, '0 = keep everything. Older restore points are pruned after each backup.')),
        h('div', { class: 'row' }, field('Compression', f.comp), field('Bandwidth limit (KB/s)', f.bw, '0 = unlimited'))),
      h('fieldset', {}, h('legend', {}, 'Automatic backup test'), h('div', { class: 'row' }, field('Test file', f.tmode, 'Each test restores this file from the latest restore point and verifies it, then checks the repository chain.'),
        field('Test schedule', tsched.el)), tinfo, tpathWrap,
        h('div', { class: 'row' }, field('Also verify % of stored data', f.pct, 'Reads and verifies this share of pack data each test (0 = index/structure only; higher is slower).'))),
      hk.el,
      editing ? null : h('fieldset', {}, h('legend', {}, 'Encryption'), h('div', { class: 'row' }, field('Repository password (optional)', f.pw, 'Backups are always encrypted. You can view the key later from the job page.'))),
      h('div', { class: 'actions' }, h('button', { class: 'btn primary', disabled: noAgents }, 'Save job'), h('a', { class: 'btn', href: editing ? '#/jobs/' + j.id : '#/jobs' }, 'Cancel'),
        editing ? h('button', { type: 'button', class: 'btn danger right', onclick: () => deleteDialog({ title: 'Delete backup job', name: j.name, kindLabel: 'backup job', dataLabel: 'all of its backups (restore points)', destName: j.dest_name, base: '/jobs/' + j.id, back: '#/jobs' }) }, 'Delete job') : null)));
}

/* ---------- mirror jobs ---------- */
function mirrorStats(r) {
  const x = (r && r.summary) || {};
  if (!r || x.files_copied === undefined && x.new_files === undefined) return '';
  const bits = [];
  if (x.dry_run) bits.push('preview');
  if (x.new_files) bits.push(`${x.new_files} new`);
  if (x.changed_files) bits.push(`${x.changed_files} changed`);
  const del = x.dry_run ? x.would_delete : x.deleted;
  if (del) bits.push(`${del} ${x.dry_run ? 'to delete' : 'deleted'}`);
  const bytes = x.dry_run ? x.bytes_to_copy : x.bytes_copied;
  if (bytes) bits.push(fmtBytes(bytes));
  return bits.length ? bits.join(' · ') : (x.unchanged_files !== undefined ? 'already in sync' : '');
}

const startMirror = (id, body, msg) => guard(async () => {
  const r = await api(`/mirrors/${id}/run`, 'POST', body || {});
  toast(msg || 'Mirror started'); location.hash = '#/runs/' + r.run_id;
});

function mirrorsTable(ms) {
  return h('div', { class: 'card flush' }, h('div', { class: 'card-head' }, h('h2', {}, 'Mirror jobs'), h('span', { class: 'muted small' }, 'raw file-for-file copies, deletions included')),
    h('div', { class: 'tablewrap' }, h('table', {},
      h('thead', {}, h('tr', {}, ['Mirror', 'Schedule', 'Last sync', 'History', ''].map((t) => h('th', {}, t)))),
      h('tbody', {}, ms.map((m) => h('tr', { class: 'click', onclick: (e) => { if (!e.target.closest('a,button')) location.hash = '#/mirrors/' + m.id; } },
        h('td', {}, h('div', { class: 'name' }, m.name, !m.enabled && h('span', { class: 'muted small' }, ' (paused)')),
          h('div', { class: 'route' }, h('span', { class: 'dot ' + (m.source_online ? 'on' : 'off') }), m.source_name, '→', h('span', { class: 'dot ' + (m.dest_online ? 'on' : 'off') }), m.dest_name)),
        h('td', {}, h('div', {}, cronHuman(m.schedule)), h('div', { class: 'small muted' }, m.enabled && m.next_run ? 'next ' + ago(m.next_run) : '')),
        h('td', {}, runCell(m.last_run), h('div', { class: 'small muted' }, mirrorStats(m.last_run))),
        h('td', {}, hist(m.history)),
        h('td', {}, h('div', { class: 'actions', style: 'flex-wrap:nowrap' },
          h('button', { class: 'btn sm', title: 'Resumes automatically if a previous run was interrupted', onclick: startMirror(m.id) }, 'Run now'),
          h('button', { class: 'btn sm', title: 'Show what would change without changing anything', onclick: startMirror(m.id, { dry_run: true }, 'Preview started') }, 'Preview')))))))));
}

async function pageMirrors(c, p, token) {
  if (p[0] === 'new') return mirrorForm(c, null);
  if (p[0] && p[1] === 'edit') return mirrorForm(c, (await api('/mirrors')).find((m) => m.id === p[0]));
  if (p[0]) return mirrorDetail(c, p[0], token);
  const draw = async () => {
    const ms = await api('/mirrors');
    if (token !== renderToken) return;
    put(c,
      h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Mirror jobs'), h('div', { class: 'sub' }, 'Keep a destination folder an exact, ready-to-use copy of a source folder — only changed files are sent, and interrupted syncs resume')),
        h('a', { class: 'btn primary right', href: '#/mirrors/new' }, '+ New mirror job')),
      ms.length ? mirrorsTable(ms) : h('div', { class: 'card' }, h('div', { class: 'empty' }, h('p', {}, 'No mirror jobs yet.'), h('a', { class: 'btn primary', href: '#/mirrors/new' }, 'Create a mirror job'))));
  };
  await draw();
  every(guard(draw), 6000);
}

async function mirrorDetail(c, id, token) {
  const draw = async () => {
    const m = (await api('/mirrors')).find((x) => x.id === id);
    if (!m) { put(c, h('div', { class: 'note bad' }, 'Mirror job not found.')); return; }
    const runs = await api(`/runs?job_id=${id}&limit=50`);
    if (token !== renderToken) return;
    const lr = m.last_run, x = (lr && lr.summary) || {};
    const blocked = lr && lr.status === 'failed' && /safety limit|source is empty/.test(lr.message || '');
    put(c,
      h('div', { class: 'crumb' }, h('a', { href: '#/mirrors' }, 'Mirror jobs'), ' / ', m.name),
      h('div', { class: 'page-head' }, h('h1', {}, m.name), chip(m.health), !m.enabled && chip('off', 'Paused'),
        h('div', { class: 'right actions' },
          h('button', { class: 'btn primary', onclick: startMirror(id) }, 'Run now'),
          h('button', { class: 'btn', onclick: startMirror(id, { dry_run: true }, 'Preview started') }, 'Preview changes'),
          h('a', { class: 'btn', href: `#/mirrors/${id}/edit` }, 'Edit'))),
      blocked ? h('div', { class: 'note bad' }, h('b', {}, 'Deletions were blocked. '), lr.message, h('div', { style: 'margin-top:8px' },
        h('button', { class: 'btn sm danger', onclick: () => { if (confirm('Run the mirror and apply these deletions to the destination? Make sure the source is complete first. This cannot be undone.')) startMirror(id, { force_delete: true }, 'Started with deletions allowed')(); } }, 'I’ve checked — run and allow these deletions'))) : null,
      h('div', { class: 'cols' },
        h('div', { class: 'card' }, h('h2', {}, 'Configuration'), h('dl', { class: 'kv' },
          h('dt', {}, 'Source'), h('dd', {}, h('span', { class: 'dot ' + (m.source_online ? 'on' : 'off') }), ' ', m.source_name, ' ', h('code', {}, m.source_path)),
          m.mount ? [h('dt', {}, 'Network share'), h('dd', {}, `${m.mount.type.toUpperCase()} ${m.mount.remote}`)] : null,
          h('dt', {}, 'Destination'), h('dd', {}, h('span', { class: 'dot ' + (m.dest_online ? 'on' : 'off') }), ' ', m.dest_name, ' ', h('code', {}, m.dest_folder || m.dest_path)),
          m.excludes.length ? [h('dt', {}, 'Exclusions'), h('dd', {}, m.excludes.map((e) => h('div', {}, h('code', {}, e))))] : null,
          h('dt', {}, 'Schedule'), h('dd', {}, cronHuman(m.schedule), m.enabled && m.next_run ? h('span', { class: 'muted' }, ` · next ${fmtTime(m.next_run)}`) : null),
          h('dt', {}, 'Compare by'), h('dd', {}, m.compare === 'checksum' ? 'Content checksum (slow, thorough)' : 'Size & modification time'),
          h('dt', {}, 'Deletions'), h('dd', {}, m.propagate_deletes ? `Propagated${m.max_delete_pct ? `, blocked if more than ${m.max_delete_pct}% of the destination would go` : ''}` : 'Not propagated (extra files are kept)'),
          h('dt', {}, 'Ownership'), h('dd', {}, m.preserve_owner ? `Preserved (by ${m.owner_map === 'names' ? 'name' : 'numeric ID'})` : 'Not preserved'),
          h('dt', {}, 'ACLs & attributes'), h('dd', {}, m.preserve_acls ? 'Preserved' : 'Not preserved'),
          hooksRows(m.hooks),
          h('dt', {}, 'Parallel transfers'), h('dd', {}, String(m.workers)),
          h('dt', {}, 'Bandwidth limit'), h('dd', {}, m.bandwidth_kb ? m.bandwidth_kb + ' KB/s' : 'None'))),
        h('div', { class: 'card' }, h('h2', {}, 'Last sync'), lr ? [
          h('div', {}, chip(lr.status), ' ', h('a', { href: '#/runs/' + lr.id }, fmtTime(lr.finished || lr.created)), lr.message ? h('div', { class: 'small muted' }, lr.message) : null),
          h('div', { style: 'margin:10px 0' }, hist(m.history)),
          x.source_entries !== undefined ? h('dl', { class: 'kv' },
            h('dt', {}, 'Source'), h('dd', {}, `${x.source_entries.toLocaleString()} items · ${fmtBytes(x.source_bytes)}`),
            h('dt', {}, 'Copied'), h('dd', {}, `${(x.files_copied || 0).toLocaleString()} files · ${fmtBytes(x.bytes_copied)}`),
            h('dt', {}, 'New / changed'), h('dd', {}, `${x.new_files || 0} / ${x.changed_files || 0}`),
            h('dt', {}, 'Unchanged'), h('dd', {}, (x.unchanged_files || 0).toLocaleString()),
            h('dt', {}, 'Deleted'), h('dd', {}, String(x.deleted || 0)),
            x.resumed_files ? [h('dt', {}, 'Resumed'), h('dd', {}, `${x.resumed_files} file(s), ${fmtBytes(x.resumed_bytes)} already transferred`)] : null,
            x.errors ? [h('dt', {}, 'Errors'), h('dd', { style: 'color:var(--bad)' }, String(x.errors))] : null,
            h('dt', {}, 'Duration'), h('dd', {}, lr.finished && lr.started ? dur(lr.finished - lr.started) : '—')) : null] : h('div', { class: 'muted' }, 'No sync has run yet.'))),
      h('h2', { style: 'margin:6px 0 10px' }, 'Run history'), runsTable(runs));
  };
  await draw();
  every(guard(draw), 5000);
}

function mirrorForm(c, m) {
  const agents = state.agents.map((a) => a.agent);
  const srcs = agents.filter((a) => (a.roles || []).includes('source'));
  const dsts = agents.filter((a) => (a.roles || []).includes('dest'));
  const editing = !!m;
  const j = m || { enabled: true, source_path: '', dest_path: '', excludes: [], schedule: '0 3 * * *', compare: 'mtime', workers: 4, propagate_deletes: true, max_delete_pct: 25, bandwidth_kb: 0, preserve_owner: false, owner_map: 'numeric', preserve_acls: false };
  const mo = j.mount || {};
  const f = {
    name: h('input', { type: 'text', value: j.name || '', required: true, placeholder: 'e.g. Media library mirror' }),
    enabled: h('input', { type: 'checkbox', checked: j.enabled }),
    src: select(srcs.map((a) => [a.id, a.name]), j.source_agent || (srcs[0] || {}).id),
    spath: h('input', { type: 'text', value: j.source_path || '', required: true, placeholder: '/srv/media' }),
    dst: select(dsts.map((a) => [a.id, a.name + (a.online ? '' : ' (offline)')]), j.dest_agent || (dsts[0] || {}).id),
    dpath: h('input', { type: 'text', value: j.dest_path || '', required: true, placeholder: 'media' }),
    excludes: h('textarea', { placeholder: '*.tmp\n.DS_Store\n/cache', value: (j.excludes || []).join('\n') }),
    mtype: select([['', 'No — the agent can read the folder directly'], ['smb', 'SMB / CIFS share'], ['nfs', 'NFS export']], mo.type || ''),
    mremote: h('input', { type: 'text', value: mo.remote || '', placeholder: '//nas.local/share   or   nas.local:/export' }),
    muser: h('input', { type: 'text', value: mo.username || '' }),
    mpass: h('input', { type: 'password', value: '', placeholder: editing && mo.type ? '(unchanged)' : '', autocomplete: 'new-password' }),
    mdomain: h('input', { type: 'text', value: mo.domain || '' }),
    mopts: h('input', { type: 'text', value: mo.options || '' }),
    compare: select([['mtime', 'Size & modification time (fast — recommended)'], ['checksum', 'Content checksum (reads every file on both sides — slow)']], j.compare),
    workers: h('input', { type: 'number', min: 1, max: 32, value: j.workers }),
    bw: h('input', { type: 'number', min: 0, value: j.bandwidth_kb || 0 }),
    del: h('input', { type: 'checkbox', checked: j.propagate_deletes }),
    pct: h('input', { type: 'number', min: 0, max: 100, value: j.max_delete_pct }),
    own: h('input', { type: 'checkbox', checked: j.preserve_owner }),
    ownmap: select([['numeric', 'Numeric user/group ID (same IDs on both machines)'], ['names', 'User/group name (IDs may differ between machines)']], j.owner_map || 'numeric'),
    acl: h('input', { type: 'checkbox', checked: j.preserve_acls }),
  };
  const sched = schedulePicker(j.schedule);
  const hk = hooksFieldset(j.hooks, { defaultAgent: j.dest_agent || f.dst.value });
  const mountBox = h('div', { class: 'row' });
  const where = h('div', { class: 'hint small muted', style: 'margin-top:4px;font-weight:400' });
  const sync = () => {
    put(mountBox, f.mtype.value ? [field('Share', f.mremote), field('Username', f.muser), field('Password', f.mpass), field('Domain (SMB)', f.mdomain), field('Extra mount options', f.mopts)] : []);
    const ag = dsts.find((a) => a.id === f.dst.value) || {};
    where.textContent = ag.mirror_root ? `Stored at ${ag.mirror_root.replace(/[\\/]+$/, '')}/${(f.dpath.value || '…').replace(/^[\\/]+/, '')}` : '';
  };
  [f.mtype, f.dst, f.dpath].forEach((e) => e.addEventListener('input', sync)); sync();
  const err = h('div', { class: 'note bad hidden' });
  const lines = (t) => t.split('\n').map((x) => x.trim()).filter(Boolean);
  const submit = async (e) => {
    e.preventDefault();
    const body = {
      name: f.name.value, enabled: f.enabled.checked, source_agent: f.src.value, source_path: f.spath.value, dest_agent: f.dst.value, dest_path: f.dpath.value,
      excludes: lines(f.excludes.value), schedule: sched.get(), compare: f.compare.value, workers: +f.workers.value || 4, bandwidth_kb: +f.bw.value || 0,
      propagate_deletes: f.del.checked, max_delete_pct: +f.pct.value || 0,
      preserve_owner: f.own.checked, owner_map: f.ownmap.value, preserve_acls: f.acl.checked, hooks: hk.get(),
      mount: f.mtype.value ? { type: f.mtype.value, remote: f.mremote.value, username: f.muser.value, password: f.mpass.value, domain: f.mdomain.value, options: f.mopts.value } : null,
    };
    try {
      const r = editing ? await api('/mirrors/' + j.id, 'PUT', body) : await api('/mirrors', 'POST', body);
      toast('Mirror job saved'); location.hash = '#/mirrors/' + r.id;
    } catch (x) { err.textContent = x.message; err.classList.remove('hidden'); err.scrollIntoView({ block: 'center' }); }
  };
  const noAgents = !srcs.length || !dsts.length;
  put(c,
    h('div', { class: 'crumb' }, h('a', { href: '#/mirrors' }, 'Mirror jobs'), ' / ', editing ? j.name : 'New'),
    h('div', { class: 'page-head' }, h('h1', {}, editing ? 'Edit mirror job' : 'New mirror job')),
    noAgents ? h('div', { class: 'note warn' }, 'You need at least one source agent and one destination agent. ', h('a', { href: '#/infra' }, 'Deploy agents →')) : null,
    h('form', { class: 'card', onsubmit: submit }, err,
      h('div', { class: 'note' }, 'A mirror is a plain folder tree — not a restic repository. The destination holds ordinary files you can open directly, and it is kept identical to the source: new and changed files are copied, and files deleted on the source are deleted on the destination. For versioned history use a Backup job instead.'),
      h('fieldset', {}, h('legend', {}, 'General'), h('div', { class: 'row' }, field('Job name', f.name), h('div', { style: 'align-self:end;padding-bottom:8px' }, h('label', { class: 'check' }, f.enabled, 'Enabled (scheduled runs active)')))),
      h('fieldset', {}, h('legend', {}, 'Source'), h('div', { class: 'row' }, field('Source agent', f.src, 'The agent that can read the data'), field('Source folder', f.spath, 'Absolute path on that agent (or relative to the share root below)')),
        h('div', { class: 'row' }, field('Is the data on a network share?', f.mtype, 'Use when the agent runs near the data. The agent needs mount privileges.')), mountBox,
        h('div', { class: 'row' }, field('Exclusions', f.excludes, 'One pattern per line (*.tmp, node_modules, /anchored/path). Excluded items are neither copied nor deleted on the destination.'))),
      h('fieldset', {}, h('legend', {}, 'Destination'), h('div', { class: 'row' }, field('Destination agent', f.dst, 'Receives the files directly from the source agent'),
        h('div', {}, field('Destination folder', f.dpath, 'Relative to the destination agent’s mirror folder (set with --mirror-root)'), where))),
      h('fieldset', {}, h('legend', {}, 'Schedule & comparison'), h('div', { class: 'row' }, field('Run', sched.el, 'An interrupted run simply continues at the next run; large files resume part-way.'), field('Detect changes by', f.compare, 'Fast mode compares size and modification time and sends only what differs.')),
        h('div', { class: 'row' }, field('Parallel transfers', f.workers, '1–32. More helps with many small files.'), field('Bandwidth limit (KB/s)', f.bw, '0 = unlimited'))),
      h('fieldset', {}, h('legend', {}, 'Ownership & ACLs'),
        h('div', { class: 'row' }, h('div', {}, h('label', { class: 'check' }, f.own, 'Preserve file owner and group'), h('div', { class: 'hint small muted', style: 'margin:4px 0 0 24px;font-weight:400' }, 'Also keeps setuid/setgid/sticky bits.')), field('Match users and groups by', f.ownmap, 'Names are looked up on the destination machine; unknown names fall back to the numeric ID.')),
        h('div', { class: 'row' }, h('div', {}, h('label', { class: 'check' }, f.acl, 'Preserve ACLs and extended attributes'), h('div', { class: 'hint small muted', style: 'margin:4px 0 0 24px;font-weight:400' }, 'Linux: POSIX ACLs (incl. default ACLs), file capabilities and user.* attributes. macOS: extended attributes only. ACL entries are copied verbatim, so their numeric IDs must mean the same on both machines.'))),
        h('div', { class: 'note warn' }, 'Setting ownership requires the destination agent to run as root (e.g. a systemd service as root); without it files are still copied and the run finishes with a warning. Not supported on Windows. Ownership/ACL changes alone are detected and applied without re-copying file data (this makes the comparison scan slightly slower).')),
      hk.el,
      h('fieldset', {}, h('legend', {}, 'Deletions'), h('div', { class: 'row' }, h('div', { style: 'align-self:center' }, h('label', { class: 'check' }, f.del, 'Delete files on the destination that no longer exist on the source')),
        field('Safety limit (% of destination)', f.pct, 'Refuse to delete if more than this share would go (0 = no limit). An empty or unreadable source is always refused. Protects against an unmounted source wiping the mirror.')),
        h('div', { class: 'note warn' }, 'Deleted files are removed permanently from the destination — there are no restore points. Use Preview to see what a run would do before enabling a schedule.')),
      h('div', { class: 'actions' }, h('button', { class: 'btn primary', disabled: noAgents }, 'Save mirror job'), h('a', { class: 'btn', href: editing ? '#/mirrors/' + j.id : '#/mirrors' }, 'Cancel'),
        editing ? h('button', { type: 'button', class: 'btn danger right', onclick: () => deleteDialog({ title: 'Delete mirror job', name: j.name, kindLabel: 'mirror job', dataLabel: 'the mirrored files (' + (j.dest_folder || j.dest_path) + ')', destName: j.dest_name, base: '/mirrors/' + j.id, back: '#/mirrors' }) }, 'Delete job') : null)));
}

/* ---------- copy jobs ---------- */
async function pageCopies(c, _, token) {
  const draw = async () => {
    const [copies, jobs] = await Promise.all([api('/copyjobs'), api('/jobs')]);
    if (token !== renderToken) return;
    put(c, 
      h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Copy jobs'), h('div', { class: 'sub' }, 'Replicate restore points from one destination to another — directly between agents')),
        h('button', { class: 'btn primary right', onclick: () => copyModal(null, jobs, draw) }, '+ New copy job')),
      h('div', { class: 'card flush' }, copies.length ? h('div', { class: 'tablewrap' }, h('table', {},
        h('thead', {}, h('tr', {}, ['Copy', 'Source job', 'Destination', 'Schedule', 'Last copy', 'Kept', 'Size', ''].map((x) => h('th', {}, x)))),
        h('tbody', {}, copies.map((x) => h('tr', {},
          h('td', {}, h('div', { class: 'name' }, x.name, !x.enabled && h('span', { class: 'muted small' }, ' (paused)'))), h('td', {}, x.source_job), h('td', {}, x.dest_name),
          h('td', {}, cronHuman(x.schedule), x.next_run ? h('div', { class: 'small muted' }, 'next ' + ago(x.next_run)) : null),
          h('td', {}, runCell(x.last_run), hist(x.history)), h('td', {}, x.keep_last ? 'Last ' + x.keep_last : 'All'), h('td', {}, x.repo_size ? fmtBytes(x.repo_size) : '—'),
          h('td', {}, h('div', { class: 'actions' },
            h('button', { class: 'btn sm', onclick: guard(async () => { await api(`/copyjobs/${x.id}/run`, 'POST', {}); toast('Copy queued'); draw(); }) }, 'Run now'),
            h('button', { class: 'btn sm', disabled: !x.keep_last, title: x.keep_last ? `Keep only the newest ${x.keep_last} restore points` : 'No restore-point limit set', onclick: guard(async () => {
              if (!confirm(`Remove restore points on this copy beyond the newest ${x.keep_last}? This cannot be undone.`)) return;
              const r = await api(`/copyjobs/${x.id}/prune`, 'POST', {}); toast('Prune started'); location.hash = '#/runs/' + r.run_id;
            }) }, 'Prune'),
            h('button', { class: 'btn sm', onclick: () => copyModal(x, jobs, draw) }, 'Edit'),
            h('button', { class: 'btn sm', onclick: guard(async () => showKey(await api(`/copyjobs/${x.id}/key`), x.name)) }, 'Key')))))))) :
        h('div', { class: 'empty' }, jobs.length ? 'No copy jobs yet. A copy job gives you a second, independent set of restore points (3-2-1).' : 'Create a backup job first.')));
  };
  await draw();
  every(guard(draw), 8000);
}

function copyModal(x, jobs, done) {
  const dsts = state.agents.map((a) => a.agent).filter((a) => (a.roles || []).includes('dest'));
  const f = {
    name: h('input', { type: 'text', value: x ? x.name : '' }), enabled: h('input', { type: 'checkbox', checked: x ? x.enabled : true }),
    job: select(jobs.map((j) => [j.id, j.name]), x ? x.job_id : (jobs[0] || {}).id), dst: select(dsts.map((a) => [a.id, a.name]), x ? x.dest_agent : (dsts[0] || {}).id),
    keep: h('input', { type: 'number', min: 0, value: x ? x.keep_last : 30 }),
  };
  if (x) { f.job.disabled = true; f.dst.disabled = true; }
  const sched = schedulePicker(x ? x.schedule : '30 3 * * *');
  const hk = hooksFieldset(x && x.hooks, { defaultAgent: x ? x.dest_agent : f.dst.value });
  const err = h('div', { class: 'note bad hidden' });
  const body = h('div', {}, err,
    h('div', { class: 'row', style: 'display:grid;gap:14px' }, field('Name', f.name), field('Backup job to copy', f.job), field('Copy to destination', f.dst, 'Choose a different destination from the primary for real redundancy.'),
      field('Schedule', sched.el), field('Maximum restore points', f.keep, '0 = keep everything'),
      h('label', { class: 'check' }, f.enabled, 'Enabled')), hk.el);
  body.classList.add('wide');
  modal(x ? 'Edit copy job' : 'New copy job', body, [{ label: 'Save', cls: 'primary', fn: async (close) => {
    try {
      const b = { name: f.name.value, enabled: f.enabled.checked, job_id: f.job.value, dest_agent: f.dst.value, schedule: sched.get(), keep_last: +f.keep.value || 0, hooks: hk.get() };
      if (x) await api('/copyjobs/' + x.id, 'PUT', b); else await api('/copyjobs', 'POST', b);
      close(); toast('Copy job saved'); done();
    } catch (e) { err.textContent = e.message; err.classList.remove('hidden'); }
  } }, ...(x ? [{ label: 'Delete…', cls: 'danger', fn: (close) => {
    close(); deleteDialog({ title: 'Delete copy job', name: x.name, kindLabel: 'copy job', dataLabel: 'the copied restore points', destName: x.dest_name, base: '/copyjobs/' + x.id, back: '#/copies' });
  } }] : [])]);
}

/* ---------- infrastructure ---------- */
async function pageInfra(c, _, token) {
  const draw = async () => {
    const [list, st] = await Promise.all([api('/agents'), api('/settings')]);
    if (token !== renderToken) return;
    state.agents = list;
    const cmd = `vk-agent run --manager ${location.origin} --token ${st.enroll_token} --roles source,dest --name "$(hostname)"`;
    put(c, 
      h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Backup infrastructure'), h('div', { class: 'sub' }, 'Agents act as backup sources, destinations, or both')),
        h('button', { class: 'btn right', onclick: () => { document.getElementById('enrol').classList.toggle('hidden'); } }, '+ Deploy an agent')),
      h('div', { class: 'card hidden', id: 'enrol' }, h('h2', {}, 'Deploy a new agent'),
        h('p', { class: 'muted', style: 'margin-top:0' }, 'Copy the single ', h('code', {}, 'vk-agent'), ' binary to the machine (no installer or runtime needed; restic is downloaded automatically on first run) and run:'),
        h('textarea', { readOnly: true, style: 'min-height:56px', onclick: (e) => e.target.select() }, cmd),
        h('p', { class: 'small muted' }, 'Use ', h('code', {}, '--roles source'), ' for machines holding data, ', h('code', {}, '--roles dest'), ' for storage machines (also set ', h('code', {}, '--listen'), ' / ', h('code', {}, '--advertise'), ' if the default address is not reachable by sources). ',
          'Run ', h('code', {}, 'vk-agent systemd'), ' to print a service unit.'),
        h('div', { class: 'actions' }, h('button', { class: 'btn sm', onclick: () => { navigator.clipboard && navigator.clipboard.writeText(cmd); toast('Command copied'); } }, 'Copy command'),
          h('button', { class: 'btn sm', onclick: guard(async () => { if (confirm('Rotate the enrolment token? Existing agents are unaffected.')) { await api('/settings/enroll-token', 'POST', {}); draw(); document.getElementById('enrol').classList.remove('hidden'); } }) }, 'Rotate token'))),
      list.length ? list.map(agentCard) : h('div', { class: 'card' }, h('div', { class: 'empty' }, 'No agents enrolled yet. Click “Deploy an agent”.')));
  };
  await draw();
  every(guard(draw), 8000);
}

function agentCard(x) {
  const a = x.agent, used = x.used;
  const pctFree = a.disk_total ? a.disk_free / a.disk_total : 1;
  const usedPct = a.disk_total ? 100 - pctFree * 100 : 0;
  return h('div', { class: 'card' },
    h('div', { style: 'display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:12px' },
      h('span', { class: 'dot ' + (a.online ? 'on' : 'off') }), h('h2', { style: 'margin:0;font-size:16px' }, a.name),
      (a.roles || []).map((r) => h('span', { class: 'chip ' + (r === 'dest' ? 'running' : 'info') }, r === 'dest' ? 'Destination' : 'Source')),
      chip(a.online ? 'success' : 'failed', a.online ? 'Online' : 'Offline · ' + ago(a.last_seen)),
      a.running ? chip('running', a.running + ' task' + (a.running > 1 ? 's' : '')) : null,
      a.desired ? chip('queued', 'Config change pending') : null,
      h('div', { class: 'right actions' },
        h('button', { class: 'btn sm', onclick: () => agentConfigDialog(x) }, 'Configure'),
        h('button', { class: 'btn sm', disabled: !a.online, title: 'Restart the agent process (it waits until idle)', onclick: guard(async () => { if (!confirm(`Restart agent "${a.name}"? It restarts once it has no tasks running.`)) return; await api(`/agents/${a.id}/restart`, 'POST', {}); toast('Restart requested'); }) }, 'Restart'),
        h('button', { class: 'btn sm', onclick: guard(async () => { const n = prompt('Agent name', a.name); if (n && n.trim()) { await api('/agents/' + a.id, 'PUT', { name: n }); toast('Renamed'); } }) }, 'Rename'),
        h('button', { class: 'btn sm danger', onclick: guard(async () => { if (confirm(`Remove agent "${a.name}"? It must not be used by any job.`)) { await api('/agents/' + a.id, 'DELETE'); toast('Agent removed'); } }) }, 'Remove'))),
    h('div', { class: 'cols' },
      h('dl', { class: 'kv' },
        h('dt', {}, 'Host'), h('dd', {}, `${a.hostname} · ${a.os}/${a.arch}`),
        h('dt', {}, 'Agent / restic'), h('dd', {}, `v${a.version} · restic ${a.restic_version || 'not installed'}`),
        a.advertise ? [h('dt', {}, 'Data endpoint'), h('dd', {}, h('code', {}, a.advertise))] : null,
        h('dt', {}, 'Data directory'), h('dd', {}, h('code', {}, a.data_dir || '—')),
        h('dt', {}, 'Scripts'), h('dd', {}, a.allow_scripts ? 'Allowed (--allow-scripts)' : h('span', { class: 'muted' }, 'Disabled — needed for wake commands and post-job scripts')),
        a.roles && a.roles.includes('dest') ? [h('dt', {}, 'Listens on'), h('dd', {}, h('code', {}, a.listen || '—'))] : null,
        h('dt', {}, 'Roles in use'), h('dd', {}, `${x.source_jobs} backup source job${x.source_jobs === 1 ? '' : 's'} · ${x.dest_repos} stored repositor${x.dest_repos === 1 ? 'y' : 'ies'}`)),
      h('div', {},
        a.disk_total ? [h('div', { style: 'display:flex;justify-content:space-between;margin-bottom:6px' }, h('b', {}, fmtBytes(a.disk_free) + ' free'), h('span', { class: 'muted' }, 'of ' + fmtBytes(a.disk_total))),
          h('div', { class: 'bar' }, h('div', { class: usedPct > 95 ? 'hot' : usedPct > 85 ? 'mid' : '', style: `width:${usedPct.toFixed(1)}%` })),
          pctFree < 0.1 ? h('div', { class: 'note warn', style: 'margin-top:8px' }, 'Low disk space on this agent.') : null] : null,
        a.mirror_total ? h('div', { style: 'margin-top:14px' }, h('div', { style: 'display:flex;justify-content:space-between;margin-bottom:6px' }, h('b', {}, fmtBytes(a.mirror_free) + ' free for mirrors'), h('span', { class: 'muted' }, 'of ' + fmtBytes(a.mirror_total))),
          h('div', { class: 'bar' }, h('div', { class: (100 - a.mirror_free / a.mirror_total * 100) > 95 ? 'hot' : (100 - a.mirror_free / a.mirror_total * 100) > 85 ? 'mid' : '', style: `width:${(100 - a.mirror_free / a.mirror_total * 100).toFixed(1)}%` })),
          h('div', { class: 'small muted', style: 'margin-top:4px' }, h('code', {}, a.mirror_root))) : null,
        x.repos.length ? h('table', { style: 'margin-top:12px' }, h('thead', {}, h('tr', {}, h('th', {}, 'Stored repository'), h('th', {}, 'Type'), h('th', {}, 'Size'))),
          h('tbody', {}, x.repos.map((r) => h('tr', {}, h('td', {}, r.name), h('td', {}, r.kind), h('td', {}, r.size ? fmtBytes(r.size) : '—'))))) : null,
        x.orphans && x.orphans.length ? h('div', { class: 'note warn', style: 'margin-top:12px' }, h('b', {}, 'Unreferenced backup data. '), 'These repositories on this agent belong to no job (for example a job deleted without its data).',
          x.orphans.map((o) => h('div', { style: 'display:flex;gap:10px;align-items:center;margin-top:6px' }, h('code', {}, o.name), h('span', { class: 'muted' }, o.size ? fmtBytes(o.size) : ''),
            h('button', { class: 'btn sm danger right', onclick: () => purgeOrphanDialog(a, o) }, 'Delete data…')))) : null)));
}

function purgeOrphanDialog(a, o) {
  const typed = h('input', { type: 'text', placeholder: o.name, autocomplete: 'off' });
  modal('Delete unreferenced data', h('div', {}, h('p', { style: 'margin-top:0' }, `Permanently delete the repository “${o.name}” (${fmtBytes(o.size)}) from ${a.name}? No job uses it. This cannot be undone.`), field(`Type “${o.name}” to confirm`, typed)),
    [{ label: 'Delete data', cls: 'danger', fn: async (close) => {
      if (typed.value !== o.name) { toast('Type the repository name to confirm', true); return; }
      try { const r = await api(`/agents/${a.id}/purge-repo`, 'POST', { name: o.name, confirm: typed.value }); close(); toast('Deleting…'); location.hash = '#/runs/' + r.run_id; } catch (e) { toast(e.message, true); }
    } }]);
}

function agentConfigDialog(x) {
  const a = x.agent, d = a.desired || {};
  const role = (r) => h('input', { type: 'checkbox', checked: (d.roles || a.roles || []).includes(r), value: r });
  const src = role('source'), dst = role('dest');
  const listen = h('input', { type: 'text', value: d.listen || a.listen || ':8765', placeholder: ':8765' });
  const adv = h('input', { type: 'text', value: d.advertise !== undefined ? d.advertise : (a.advertise_setting || ''), placeholder: 'https://nas.lan:8765  (empty = auto-detect)' });
  const root = h('input', { type: 'text', value: d.mirror_root !== undefined ? d.mirror_root : (a.mirror_root || ''), placeholder: '/mnt/bigdisk/mirrors  (empty = default)' });
  const err = h('div', { class: 'note bad hidden' });
  const save = async (close, confirmMove) => {
    const roles = [src.checked && 'source', dst.checked && 'dest'].filter(Boolean);
    try {
      await api(`/agents/${a.id}/config`, 'PUT', { roles, listen: listen.value, advertise: adv.value, mirror_root: root.value, confirm_mirror_move: !!confirmMove });
      close(); toast('Change queued — the agent applies it when idle and restarts'); 
    } catch (e) {
      if (!confirmMove && /does not move/.test(e.message) && confirm(e.message + '\n\nApply the new folder anyway?')) return save(close, true);
      err.textContent = e.message; err.classList.remove('hidden');
    }
  };
  modal(`Configure “${a.name}”`, h('div', {}, err,
    h('div', { class: 'note' }, 'The agent saves these settings, restarts itself (once it is idle) and reconnects. They override the command-line flags it was started with.'),
    h('div', { style: 'margin-bottom:12px' }, h('div', { style: 'font-weight:600;font-size:13px;margin-bottom:6px' }, 'Roles'), h('label', { class: 'check', style: 'margin:4px 0' }, src, 'Source — reads data to back up or mirror'), h('label', { class: 'check', style: 'margin:4px 0' }, dst, 'Destination — stores backups and mirrors')),
    h('div', { style: 'display:grid;gap:12px' }, field('Data endpoint listen address', listen, 'Destinations accept data from source agents here.'),
      field('Advertised address', adv, 'The https:// URL source agents use to reach this destination. Leave empty to auto-detect.'),
      field('Mirror folder', root, 'Where raw mirrors are stored on this machine. Existing mirrors are not moved.'))),
    [{ label: 'Save & apply', cls: 'primary', fn: (close) => save(close, false) }]);
}

/* ---------- logs & run detail ---------- */
async function pageLogs(c, _, token) {
  const jobs = await api('/jobs');
  const level = select([['', 'All levels'], ['info', 'Info +'], ['warning', 'Warnings +'], ['error', 'Errors only']], 'info');
  const job = select([['', 'All jobs'], ...jobs.map((j) => [j.id, j.name])], '');
  const q = h('input', { type: 'text', placeholder: 'Search messages…', style: 'width:220px;margin:0' });
  level.style.cssText = job.style.cssText = 'width:auto;margin:0';
  const box = h('div', { class: 'log', style: 'max-height:70vh' });
  const more = h('button', { class: 'btn', onclick: () => load(true) }, 'Load older');
  let oldest = 0;
  const load = guard(async (append) => {
    const p = new URLSearchParams({ limit: 200 }); if (level.value) p.set('level', level.value); if (job.value) p.set('job_id', job.value); if (q.value) p.set('q', q.value);
    if (append && oldest) p.set('before', oldest);
    const rows = await api('/logs?' + p);
    if (!append) put(box, );
    if (!rows.length && !append) box.append(h('div', { class: 'muted' }, 'No log entries match.'));
    rows.forEach((l) => box.append(logRow(l)));
    if (rows.length) oldest = rows[rows.length - 1].id;
    more.classList.toggle('hidden', rows.length < 200);
  });
  [level, job].forEach((e) => e.addEventListener('input', () => load(false)));
  let t; q.addEventListener('input', () => { clearTimeout(t); t = setTimeout(() => load(false), 300); });
  put(c, 
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Logs'), h('div', { class: 'sub' }, 'All manager and agent activity')),
      h('div', { class: 'right actions' }, level, job, q)),
    h('div', { class: 'card' }, box), h('div', { style: 'text-align:center' }, more));
  await load(false);
}

async function pageRun(c, p, token) {
  const draw = async () => {
    const d = await api('/runs/' + p[0]);
    if (token !== renderToken) return;
    const r = d.run;
    const live = r.status === 'running' || r.status === 'queued';
    put(c, 
      h('div', { class: 'crumb' }, h('a', { href: '#/jobs/' + r.job_id }, r.job_name), ' / run'),
      h('div', { class: 'page-head' }, h('h1', {}, `${r.kind[0].toUpperCase() + r.kind.slice(1)}${r.copy_name ? ' — ' + r.copy_name : ''}`), chip(r.status),
        live ? h('button', { class: 'btn danger right', onclick: guard(async () => { await api(`/runs/${r.id}/cancel`, 'POST', {}); toast('Cancel requested'); }) }, 'Cancel run') : null),
      r.message ? h('div', { class: 'note ' + (r.status === 'failed' ? 'bad' : 'warn') }, r.message) : null,
      r.status === 'queued' && r.wait_for ? h('div', { class: 'note' }, 'Waiting for the backup target to wake up — this run starts as soon as it is ready (see the log below).') : null,
      h('div', { class: 'card' }, h('dl', { class: 'kv' },
        h('dt', {}, 'Created'), h('dd', {}, fmtTime(r.created) + ' · ' + r.trigger),
        h('dt', {}, 'Agent'), h('dd', {}, r.agent_name),
        h('dt', {}, 'Started'), h('dd', {}, fmtTime(r.started)), h('dt', {}, 'Finished'), h('dd', {}, r.finished ? fmtTime(r.finished) + ` (${dur(r.finished - r.started)})` : '—'),
        Object.entries(r.summary || {}).filter(([k, v]) => typeof v !== 'object').map(([k, v]) => [h('dt', {}, k.replace(/_/g, ' ')), h('dd', {}, /bytes|size|added/.test(k) && typeof v === 'number' ? fmtBytes(v) : String(v))]))),
      h('div', { class: 'card' }, h('h2', {}, 'Log'), h('div', { class: 'log', id: 'runlog', style: 'max-height:60vh' }, d.logs.length ? d.logs.map((l) => logRow({ ...l, run_id: '' })) : 'No output yet.')));
    const lg = document.getElementById('runlog'); if (lg && live) lg.scrollTop = lg.scrollHeight;
    return live;
  };
  const live = await draw();
  if (live) every(guard(draw), 2000);
}

/* ---------- backup explorer ---------- */
const OVERWRITE = [['if-changed', 'Only if the file differs (recommended)'], ['always', 'Always overwrite'], ['if-newer', 'Only if the backup copy is newer'], ['never', 'Never overwrite (skip existing files)']];

async function pageExplorer(c, p, token) {
  const jobs = await api('/jobs');
  const usable = jobs.filter((j) => j.initialized);
  const copies = await api('/copyjobs');
  const st = { job: null, copy: '', snaps: [], snap: '', cwd: '/', entries: [], truncated: false, sel: new Map(), search: '', results: null, loading: false, err: '' };

  const jobSel = select([['', usable.length ? 'Choose a backup job…' : 'No backups yet'], ...usable.map((j) => [j.id, j.name])], '');
  const repoSel = select([['', 'Primary']], '');
  const snapSel = select([['', '—']], '');
  const body = h('div', {});
  const q = h('input', { type: 'text', placeholder: 'Search this restore point by file name…', style: 'margin:0;min-width:240px' });
  [jobSel, repoSel, snapSel].forEach((e) => { e.style.marginTop = '0'; });

  const qs = (extra) => new URLSearchParams({ snapshot: st.snap, ...(st.copy ? { copy: st.copy } : {}), ...extra });
  const jobCopies = () => copies.filter((x) => x.job_id === st.job.id);

  async function loadSnapshots(preselect) {
    st.snaps = []; st.snap = ''; st.entries = []; st.sel.clear(); st.results = null; st.err = '';
    put(snapSel, h('option', { value: '' }, 'Loading restore points…'));
    draw();
    try {
      const list = (await api(`/jobs/${st.job.id}/snapshots` + (st.copy ? `?copy=${st.copy}` : ''))) || [];
      list.sort((a, b) => new Date(b.time) - new Date(a.time));
      st.snaps = list;
      put(snapSel, list.length ? list.map((x) => h('option', { value: x.id }, `${new Date(x.time).toLocaleString()}  ·  ${x.id.slice(0, 8)}`)) : h('option', { value: '' }, 'No restore points'));
      st.snap = preselect && list.find((x) => x.id.startsWith(preselect)) ? list.find((x) => x.id.startsWith(preselect)).id : (list[0] || {}).id || '';
      snapSel.value = st.snap;
      if (st.snap) await openDir('/');
    } catch (e) { st.err = e.message; draw(); }
  }

  async function openDir(dir) {
    st.cwd = dir; st.results = null; st.loading = true; st.err = ''; draw();
    try {
      const d = await api(`/jobs/${st.job.id}/browse?` + qs({ path: dir }));
      st.entries = d.entries || []; st.truncated = d.truncated;
    } catch (e) { st.err = e.message; st.entries = []; }
    st.loading = false; draw();
  }

  async function doSearch() {
    const term = q.value.trim();
    if (!st.snap) return;
    if (term.length < 2) { toast('Type at least 2 characters to search', true); return; }
    st.loading = true; st.err = ''; draw();
    try {
      const d = await api(`/jobs/${st.job.id}/search?` + qs({ q: term }));
      st.results = { term, entries: d.entries || [], truncated: d.truncated };
    } catch (e) { st.err = e.message; st.results = null; }
    st.loading = false; draw();
  }

  function download(e) {
    const a = h('a', { href: `/api/jobs/${st.job.id}/download?` + qs({ path: e.path, type: e.type === 'dir' ? 'dir' : 'file' }), download: '' });
    document.body.append(a); a.click(); a.remove();
    toast(e.type === 'dir' ? 'Preparing zip download…' : 'Download starting…');
  }

  function restoreDialog(items) {
    const j = st.job;
    const srcAgent = state.agents.map((x) => x.agent).find((x) => x.id === j.source_agent) || {};
    const origBlocked = j.mount ? 'This job backs up a network share, so the original location can only be reached by the share itself. Use a new location.'
      : srcAgent.os === 'windows' ? 'Restoring to the original location is not supported on Windows agents yet.'
      : !srcAgent.online ? `The source agent “${srcAgent.name}” is offline.` : '';
    const agents = state.agents.map((x) => x.agent).filter((x) => x.online);
    const mode = { v: 'new' };
    const radio = (val, label, disabled) => h('label', { class: 'check', style: 'margin:6px 0;' + (disabled ? 'opacity:.55' : '') },
      h('input', { type: 'radio', name: 'rmode', value: val, checked: val === 'new', disabled: !!disabled, onchange: () => { mode.v = val; sync(); } }), label);
    const agentSel = select(agents.map((x) => [x.id, x.name + (x.id === j.source_agent ? ' (original source)' : '')]), agents.find((x) => x.id === j.source_agent) ? j.source_agent : (agents[0] || {}).id);
    const target = h('input', { type: 'text', value: '/restore', placeholder: '/path/on/that/agent' });
    const over = select(OVERWRITE, 'if-changed');
    const sure = h('input', { type: 'checkbox' });
    const newBox = h('div', {}, field('Restore onto agent', agentSel, 'Files are written on this machine. It must be able to reach the destination agent.'), h('div', { style: 'height:10px' }), field('Target folder', target, 'Created if missing. Original folder structure is recreated underneath it.'));
    const origBox = h('div', { class: 'hidden' }, h('div', { class: 'note warn' }, `Files are written back to their original paths on “${srcAgent.name}”. Existing files may be replaced according to the overwrite setting.`),
      h('label', { class: 'check' }, sure, 'I understand and want to restore in place'));
    const sync = () => { newBox.classList.toggle('hidden', mode.v !== 'new'); origBox.classList.toggle('hidden', mode.v !== 'original'); };
    modal('Restore', h('div', {},
      h('div', { class: 'muted small', style: 'margin-bottom:8px' }, `From restore point ${new Date(st.snaps.find((x) => x.id === st.snap).time).toLocaleString()}`),
      h('div', { class: 'log', style: 'max-height:120px;margin-bottom:12px' }, items.slice(0, 8).map((e) => h('div', { style: 'display:block' }, (e.type === 'dir' ? '📁 ' : '📄 ') + e.path)), items.length > 8 ? h('div', { class: 'muted', style: 'display:block' }, `…and ${items.length - 8} more`) : null),
      h('div', {}, radio('new', 'Restore to a new location'), radio('original', 'Restore to the original location', !!origBlocked)),
      origBlocked ? h('div', { class: 'small muted', style: 'margin:0 0 8px 24px' }, origBlocked) : null,
      newBox, origBox, h('div', { style: 'margin-top:12px' }, field('If a file already exists', over))),
      [{ label: 'Start restore', cls: 'primary', fn: async (close) => {
        if (mode.v === 'original' && !sure.checked) { toast('Please confirm the in-place restore', true); return; }
        if (mode.v === 'new' && !target.value.trim()) { toast('Enter a target folder', true); return; }
        try {
          const r = await api(`/jobs/${j.id}/restore` + (st.copy ? `?copy=${st.copy}` : ''), 'POST', { snapshot: st.snap, paths: items.map((e) => e.path), mode: mode.v, agent: agentSel.value, target: target.value.trim(), overwrite: over.value });
          close(); toast('Restore started'); location.hash = '#/runs/' + r.run_id;
        } catch (e) { toast(e.message, true); }
      } }]);
  }

  function rowActions(e) {
    return h('div', { class: 'actions', style: 'justify-content:flex-end;flex-wrap:nowrap' },
      e.type === 'file' || e.type === 'dir' ? h('button', { class: 'btn sm', title: e.type === 'dir' ? 'Download as .zip' : 'Download', onclick: () => download(e) }, e.type === 'dir' ? 'Download .zip' : 'Download') : null,
      h('button', { class: 'btn sm', onclick: () => restoreDialog([e]) }, 'Restore…'));
  }

  function list(entries, showPath) {
    const all = h('input', { type: 'checkbox', title: 'Select all', onchange: (ev) => { entries.forEach((e) => ev.target.checked ? st.sel.set(e.path, e) : st.sel.delete(e.path)); draw(); } });
    all.checked = entries.length > 0 && entries.every((e) => st.sel.has(e.path));
    return h('div', { class: 'tablewrap' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', { style: 'width:34px' }, all), h('th', {}, showPath ? 'Path' : 'Name'), h('th', {}, 'Size'), h('th', {}, 'Modified'), h('th', {}))),
      h('tbody', {}, entries.map((e) => {
        const cb = h('input', { type: 'checkbox', checked: st.sel.has(e.path), onchange: (ev) => { ev.target.checked ? st.sel.set(e.path, e) : st.sel.delete(e.path); draw(); } });
        const label = (e.type === 'dir' ? '📁 ' : e.type === 'symlink' ? '🔗 ' : '📄 ') + (showPath ? e.path : e.name);
        return h('tr', { class: e.type === 'dir' && !showPath ? 'click' : '', onclick: (ev) => { if (!ev.target.closest('button,input,a') && e.type === 'dir' && !showPath) openDir(e.path); } },
          h('td', {}, cb),
          h('td', { style: 'word-break:break-all' }, e.type === 'dir' && !showPath ? h('a', { href: 'javascript:void(0)', class: 'name' }, label) : label,
            showPath ? h('div', {}, h('a', { class: 'small', href: 'javascript:void(0)', onclick: () => openDir(e.type === 'dir' ? e.path : e.path.replace(/\/[^/]*$/, '') || '/') }, 'Show in folder')) : null),
          h('td', {}, e.type === 'dir' ? '—' : fmtBytes(e.size)), h('td', { class: 'small' }, e.mtime ? new Date(e.mtime).toLocaleString() : '—'), h('td', {}, rowActions(e)));
      }))));
  }

  function crumbs() {
    const segs = st.cwd.split('/').filter(Boolean);
    const parts = [h('a', { href: 'javascript:void(0)', onclick: () => openDir('/') }, 'All backed-up paths')];
    segs.forEach((sg, i) => { parts.push(h('span', { class: 'muted' }, ' › '), i === segs.length - 1 ? h('b', {}, sg) : h('a', { href: 'javascript:void(0)', onclick: () => openDir('/' + segs.slice(0, i + 1).join('/')) }, sg)); });
    return h('div', { style: 'padding:10px 14px;border-bottom:1px solid var(--border);word-break:break-all' }, parts);
  }

  function draw() {
    if (!st.job) { put(body, h('div', { class: 'card' }, h('div', { class: 'empty' }, usable.length ? 'Choose a backup job and restore point to start browsing. Files are decrypted on the destination agent as you open them.' : 'No job has completed a backup yet.'))); return; }
    const sel = [...st.sel.values()];
    const bar = sel.length ? h('div', { class: 'note', style: 'display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin:0;border-radius:0' },
      h('b', {}, `${sel.length} selected`),
      sel.length === 1 ? h('button', { class: 'btn sm', onclick: () => download(sel[0]) }, sel[0].type === 'dir' ? 'Download .zip' : 'Download') : h('span', { class: 'muted small' }, 'Select a single item to download'),
      h('button', { class: 'btn sm primary', onclick: () => restoreDialog(sel) }, 'Restore selected…'),
      h('button', { class: 'btn sm', onclick: () => { st.sel.clear(); draw(); } }, 'Clear')) : null;
    const dirs = st.entries.slice().sort((a, b) => (a.type === 'dir' ? 0 : 1) - (b.type === 'dir' ? 0 : 1) || a.name.localeCompare(b.name, undefined, { numeric: true }));
    let main;
    if (st.err) main = h('div', { style: 'padding:18px' }, h('div', { class: 'note bad', style: 'margin:0' }, st.err));
    else if (st.loading) main = h('div', { class: 'empty' }, st.results || q.value ? 'Searching…' : 'Decrypting and reading…');
    else if (st.results) main = [h('div', { style: 'padding:10px 14px;border-bottom:1px solid var(--border)', }, h('b', {}, `${st.results.entries.length}${st.results.truncated ? '+' : ''} match${st.results.entries.length === 1 ? '' : 'es'}`), ` for “${st.results.term}” `, h('a', { href: 'javascript:void(0)', onclick: () => { st.results = null; q.value = ''; draw(); } }, 'Clear search')),
      st.results.entries.length ? list(st.results.entries, true) : h('div', { class: 'empty' }, 'No files with that name in this restore point.')];
    else if (!st.snap) main = h('div', { class: 'empty' }, 'No restore points yet.');
    else main = [crumbs(), dirs.length ? list(dirs, false) : h('div', { class: 'empty' }, 'This folder is empty.'), st.truncated ? h('div', { class: 'note warn', style: 'margin:12px' }, `Showing the first ${st.entries.length} entries only.`) : null];
    put(body, bar && h('div', { class: 'card flush', style: 'position:sticky;top:0;z-index:5' }, bar), h('div', { class: 'card flush' }, main));
  }

  jobSel.addEventListener('input', () => {
    st.job = usable.find((j) => j.id === jobSel.value) || null; st.copy = '';
    put(repoSel, h('option', { value: '' }, st.job ? `Primary (${st.job.dest_name})` : 'Primary'), st.job ? jobCopies().map((x) => h('option', { value: x.id }, `Copy: ${x.name} (${x.dest_name})`)) : []);
    repoSel.value = '';
    if (st.job) loadSnapshots(); else draw();
  });
  repoSel.addEventListener('input', () => { st.copy = repoSel.value; loadSnapshots(); });
  snapSel.addEventListener('input', () => { st.snap = snapSel.value; st.sel.clear(); st.results = null; if (st.snap) openDir('/'); });
  q.addEventListener('keydown', (e) => { if (e.key === 'Enter') doSearch(); });

  put(c,
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Backup explorer'), h('div', { class: 'sub' }, 'Open a restore point, browse and search its files, then download or restore them'))),
    h('div', { class: 'card' }, h('div', { class: 'row', style: 'margin-bottom:12px' }, field('Backup job', jobSel), field('Repository', repoSel, 'Browse the primary backup or an offsite copy'), field('Restore point (date)', h('div', {}, snapSel, h('button', { type: 'button', class: 'btn sm danger', style: 'margin-top:6px', onclick: () => { const sn = st.snaps.find((x) => x.id === st.snap); if (sn) deleteSnapshot(st.job.id, sn, st.copy); } }, 'Delete this restore point…')))),
      h('div', { class: 'actions' }, q, h('button', { class: 'btn', onclick: doSearch }, 'Search'))),
    body);
  draw();
  if (p[0] && usable.find((j) => j.id === p[0])) { jobSel.value = p[0]; jobSel.dispatchEvent(new Event('input')); if (p[1]) { await loadSnapshots(p[1]); } }
}

/* ---------- settings ---------- */
async function pageSettings(c) {
  const [s, sys] = await Promise.all([api('/settings'), api('/system')]);
  const f = {
    host: h('input', { type: 'text', value: s.smtp_host, placeholder: 'smtp.example.com' }),
    port: h('input', { type: 'number', value: s.smtp_port }),
    sec: select([['starttls', 'STARTTLS (587)'], ['tls', 'SSL/TLS (465)'], ['none', 'None (25) — not recommended']], s.smtp_security),
    user: h('input', { type: 'text', value: s.smtp_user, autocomplete: 'off' }),
    pass: h('input', { type: 'password', placeholder: s.smtp_pass_set ? '(unchanged)' : '', autocomplete: 'new-password' }),
    from: h('input', { type: 'text', value: s.smtp_from, placeholder: 'Vaultkeeper <backups@example.com>' }),
    to: h('input', { type: 'text', value: s.smtp_to, placeholder: 'ops@example.com, oncall@example.com' }),
    level: select([['info', 'Everything — successes, warnings and failures'], ['warning', 'Warnings and failures only'], ['error', 'Failures only'], ['never', 'Never send email']], s.email_level),
    days: h('input', { type: 'number', min: 7, value: s.log_retention_days }),
    disk: h('input', { type: 'number', min: 0, max: 90, value: s.disk_alert_pct }),
    abOn: h('input', { type: 'checkbox', checked: s.auto_backup }),
    abDir: h('input', { type: 'text', value: s.auto_backup_dir, placeholder: sys.auto_backup_dir }),
    abKeep: h('input', { type: 'number', min: 1, max: 365, value: s.auto_backup_keep }),
    abPass: h('input', { type: 'password', placeholder: s.auto_backup_pass_set ? '(unchanged)' : 'at least 10 characters', autocomplete: 'new-password' }),
  };
  const save = async () => api('/settings', 'PUT', { smtp_host: f.host.value, smtp_port: +f.port.value, smtp_security: f.sec.value, smtp_user: f.user.value, smtp_pass: f.pass.value,
    smtp_from: f.from.value, smtp_to: f.to.value, email_level: f.level.value, log_retention_days: +f.days.value, disk_alert_pct: +f.disk.value || 0,
    auto_backup: f.abOn.checked, auto_backup_dir: f.abDir.value, auto_backup_keep: +f.abKeep.value || 14, auto_backup_pass: f.abPass.value });
  const cur = h('input', { type: 'password', autocomplete: 'current-password' }), nw = h('input', { type: 'password', autocomplete: 'new-password' });

  // --- export / import of the manager configuration
  const exAdmin = h('input', { type: 'password', autocomplete: 'current-password' }), exPass = h('input', { type: 'password', autocomplete: 'new-password', placeholder: 'at least 10 characters' });
  const imAdmin = h('input', { type: 'password', autocomplete: 'current-password' }), imPass = h('input', { type: 'password', autocomplete: 'off' });
  const imFile = h('input', { type: 'file', accept: '.vkcfg,application/json' });
  const doExport = guard(async (e) => {
    e.preventDefault();
    const r = await fetch('/api/system/export', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ admin_password: exAdmin.value, passphrase: exPass.value }) });
    if (!r.ok) { let m = r.statusText; try { m = (await r.json()).error || m; } catch {} throw new Error(m); }
    const blob = await r.blob();
    const a = h('a', { href: URL.createObjectURL(blob), download: (r.headers.get('Content-Disposition') || '').match(/filename="([^"]+)"/)?.[1] || 'vaultkeeper-config.vkcfg' });
    document.body.append(a); a.click(); a.remove(); exPass.value = ''; exAdmin.value = '';
    toast('Configuration exported — keep the file and passphrase somewhere safe');
  });
  const doImport = guard(async (e) => {
    e.preventDefault();
    if (!imFile.files[0]) throw new Error('Choose a .vkcfg file');
    if (!confirm('Replace ALL jobs, agents and settings on this manager with the contents of this file? This cannot be undone. You will be signed out, and the admin password becomes the one from the backup.')) return;
    const data = await imFile.files[0].text();
    const r = await api('/system/import', 'POST', { admin_password: imAdmin.value, passphrase: imPass.value, data });
    toast(`Imported ${r.jobs} backup, ${r.mirrors} mirror and ${r.copies} copy jobs, ${r.agents} agents. Sign in again.`);
    setTimeout(showLogin, 400);
  });

  put(c,
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Settings'), h('div', { class: 'sub' }, 'Alerts, backup of Vaultkeeper itself, and access'))),
    h('form', { class: 'card', onsubmit: guard(async (e) => { e.preventDefault(); await save(); toast('Settings saved'); }) },
      h('h2', {}, 'Email alerts'),
      h('div', { class: 'note' }, 'Choose how noisy email should be. Run results, offline agents, low disk space and other events at or above this level are emailed; everything is always kept in the Logs page.'),
      h('div', { class: 'row' }, field('Send email for', f.level), field('Keep logs for (days)', f.days), field('Low disk space alert (% free)', f.disk, 'Email when a destination has less than this much free (0 = off).')),
      h('div', { class: 'row' }, field('SMTP server', f.host), field('Port', f.port), field('Security', f.sec)),
      h('div', { class: 'row' }, field('Username', f.user, 'Leave empty for no authentication'), field('Password', f.pass)),
      h('div', { class: 'row' }, field('From address', f.from), field('Recipients', f.to, 'Comma separated')),
      h('h2', { style: 'margin-top:8px' }, 'Automatic backup of this manager'),
      h('div', { class: 'note' }, 'Once a day Vaultkeeper writes an encrypted copy of its configuration (jobs, agents, repository keys) to a folder on this machine. Point that folder at a location you back up elsewhere. Restore it on a fresh manager with “Restore configuration” below.'),
      h('div', { class: 'row' }, h('div', { style: 'align-self:end;padding-bottom:8px' }, h('label', { class: 'check' }, f.abOn, 'Enable the daily automatic backup')), field('Folder on the manager', f.abDir, 'Default shown. Created if missing.'), field('Keep the newest', f.abKeep, 'files')),
      h('div', { class: 'row' }, field('Passphrase', f.abPass, 'Encrypts the files. Stored on the manager so backups can run unattended — keep a separate copy.'),
        h('div', { style: 'align-self:end;padding-bottom:6px' }, s.last_auto_backup ? h('span', { class: 'small muted' }, `Last backup ${fmtTime(s.last_auto_backup)}: `, h('b', { style: s.last_auto_backup_msg === 'ok' ? '' : 'color:var(--bad)' }, s.last_auto_backup_msg === 'ok' ? 'ok' : s.last_auto_backup_msg)) : h('span', { class: 'small muted' }, 'No automatic backup has run yet.'))),
      h('div', { class: 'actions' }, h('button', { class: 'btn primary' }, 'Save settings'),
        h('button', { type: 'button', class: 'btn', onclick: guard(async () => { await save(); await api('/settings/test-email', 'POST', {}); toast('Test email sent'); }) }, 'Save & send test email'),
        h('button', { type: 'button', class: 'btn', onclick: guard(async () => { await save(); const r = await api('/system/auto-backup', 'POST', {}); toast('Backup written to ' + r.dir); route(); }) }, 'Save & back up now'))),

    h('div', { class: 'cols' },
      h('form', { class: 'card', onsubmit: doExport },
        h('h2', {}, 'Download a configuration backup'),
        h('p', { class: 'muted small', style: 'margin-top:0' }, 'Everything needed to rebuild this manager — jobs, agents (so they reconnect on their own), settings and the repository keys — in one encrypted file. Run history and logs are not included.'),
        h('div', { style: 'display:grid;gap:12px;margin-bottom:12px' }, field('Your admin password', exAdmin), field('Passphrase for the file', exPass, 'You need it to restore. It cannot be recovered.')),
        h('button', { class: 'btn' }, 'Download backup')),
      h('form', { class: 'card', onsubmit: doImport },
        h('h2', {}, 'Restore configuration'),
        h('div', { class: 'note warn' }, 'Replaces ALL jobs, agents and settings here. Use it on a new or rebuilt manager; agents that were enrolled with the old one reconnect automatically.'),
        h('div', { style: 'display:grid;gap:12px;margin-bottom:12px' }, field('Backup file', imFile), field('Passphrase of the file', imPass), field('This manager’s admin password', imAdmin)),
        h('button', { class: 'btn danger' }, 'Restore from file'))),

    h('form', { class: 'card', onsubmit: guard(async (e) => { e.preventDefault(); await api('/settings/password', 'POST', { current: cur.value, new: nw.value }); toast('Password changed — please sign in again'); showLogin(); }) },
      h('h2', {}, 'Admin password'), h('div', { class: 'row' }, field('Current password', cur), field('New password', nw, 'At least 8 characters')),
      h('button', { class: 'btn' }, 'Change password')),

    h('div', { class: 'card' }, h('h2', {}, 'About this manager'), h('dl', { class: 'kv' },
      h('dt', {}, 'Version'), h('dd', {}, `${sys.version} · ${sys.os}/${sys.arch} · ${sys.go}`),
      h('dt', {}, 'Listening on'), h('dd', {}, h('code', {}, sys.listen), sys.tls ? ' (TLS)' : h('span', { class: 'muted' }, ' — plain HTTP; use a TLS proxy or --tls-cert/--tls-key on untrusted networks')),
      h('dt', {}, 'Data directory'), h('dd', {}, h('code', {}, sys.data_dir)), h('dt', {}, 'Database'), h('dd', {}, h('code', {}, sys.db_path)),
      h('dt', {}, 'Running since'), h('dd', {}, fmtTime(sys.started)),
      h('dt', {}, 'Managing'), h('dd', {}, `${sys.agents_online}/${sys.agents} agents online · ${sys.jobs} backup, ${sys.copies} copy and ${sys.mirrors} mirror jobs`)),
      h('div', { class: 'small muted', style: 'margin-top:8px' }, 'The listen address, TLS certificate and data directory are set when the manager is started (flags or VK_* environment variables).')));
}

/* ---------- boot ---------- */
const $app = document.getElementById('app');
onUnauth = showLogin;
window.addEventListener('hashchange', () => {
  document.querySelectorAll('.modal-bg').forEach((m) => m.remove());
  if (document.querySelector('.shell')) route();
});
(async () => {
  try {
    const s = await (await fetch('/api/session')).json();
    if (s.authenticated) route(); else showLogin();
  } catch { showLogin(); }
})();
})();
