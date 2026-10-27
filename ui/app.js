(() => {
const TOKEN = window.APICHAT_TOKEN;
const $ = (s, el = document) => el.querySelector(s);
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const fmt$ = (v) => '$' + (v || 0).toFixed(v >= 10 ? 2 : 3);

const S = {
  chats: [], chat: null, models: [], settings: {}, budget: 200, usage: null,
  es: null, busy: false, atts: [], cards: {}, live: null, status: '',
};

async function api(path, opts = {}) {
  opts.headers = Object.assign({ 'x-token': TOKEN }, opts.headers || {});
  if (opts.json !== undefined) { opts.body = JSON.stringify(opts.json); opts.headers['content-type'] = 'application/json'; delete opts.json; }
  const r = await fetch(path, opts);
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
  return data;
}

// ---------- markdown ----------
marked.setOptions({ gfm: true, breaks: true });
function md(text) {
  const html = DOMPurify.sanitize(marked.parse(text || ''), { ADD_ATTR: ['target'] });
  const div = el('div', 'md');
  div.innerHTML = html;
  div.querySelectorAll('a').forEach(a => { a.target = '_blank'; a.rel = 'noopener'; });
  div.querySelectorAll('pre').forEach(pre => {
    const b = el('button', 'copy ghost', 'copy');
    b.onclick = () => { navigator.clipboard.writeText(pre.innerText.replace(/copy$/, '')); b.textContent = 'copied'; setTimeout(() => b.textContent = 'copy', 1200); };
    pre.appendChild(b);
  });
  return div;
}

// ---------- state ----------
async function loadState() {
  const st = await api('/api/state');
  S.chats = st.chats || []; S.settings = st.settings; S.budget = st.budget; S.usage = st.usage;
  renderChatList(); renderMeter();
}
async function loadModels() {
  try { const m = await api('/api/models'); S.models = m.models || []; } catch (e) { S.models = []; }
  renderModelSelect();
}

function renderMeter() {
  const spent = S.usage ? S.usage.cost : 0;
  const pct = S.budget > 0 ? Math.min(100, spent / S.budget * 100) : 0;
  const f = $('#meter-fill');
  f.style.width = pct + '%';
  f.className = pct >= 100 ? 'over' : pct >= 80 ? 'warn' : '';
  $('#meter-spent').textContent = fmt$(spent);
  $('#meter-budget').textContent = '$' + S.budget;
}

function renderChatList() {
  const list = $('#chat-list'); list.innerHTML = '';
  for (const c of S.chats) {
    const item = el('div', 'chat-item' + (S.chat && S.chat.id === c.id ? ' active' : ''));
    item.appendChild(el('span', 't', c.title || 'New chat'));
    item.appendChild(el('span', 'c', c.cost ? fmt$(c.cost) : ''));
    const x = el('span', 'x', '✕'); x.title = 'Delete';
    x.onclick = async (ev) => { ev.stopPropagation(); if (!confirm('Delete this chat?')) return; await api('/api/chats/' + c.id, { method: 'DELETE' }); if (S.chat && S.chat.id === c.id) { S.chat = null; await newChat(); } await loadState(); };
    item.appendChild(x);
    item.onclick = () => openChat(c.id);
    list.appendChild(item);
  }
}

function renderModelSelect() {
  const sel = $('#model'); sel.innerHTML = '';
  const def = S.settings.model || 'auto';
  const add = (v, t) => { const o = el('option', '', t); o.value = v; sel.appendChild(o); };
  add('', 'Default (' + def + ')');
  add('auto', 'Auto (cheapest that fits)');
  const seen = new Set();
  for (const m of S.models) { if (!/^claude-/.test(m.id) || seen.has(m.id)) continue; seen.add(m.id); add(m.id, m.display_name || m.id); }
  for (const id of ['claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-5-5']) if (!seen.has(id)) add(id, id);
  sel.value = S.chat ? (S.chat.model || '') : '';
}

// ---------- chats ----------
async function newChat() {
  const c = await api('/api/chats', { method: 'POST', json: {} });
  S.chats.unshift({ id: c.id, title: '', cost: 0 });
  await openChat(c.id);
}

async function openChat(id) {
  if (S.es) { S.es.close(); S.es = null; }
  const r = await api('/api/chats/' + id);
  S.chat = r.chat; S.busy = r.busy; S.cards = {}; S.live = null;
  localStorage.setItem('apichat.last', id);
  $('#title').value = S.chat.title || '';
  $('#chat-cost').textContent = S.chat.cost ? fmt$(S.chat.cost) : '';
  renderModelSelect();
  renderChatList();
  renderMessages();
  setBusy(S.busy);
  subscribe(id);
  $('#input').focus();
}

function setBusy(b) {
  S.busy = b;
  $('#send').classList.toggle('hidden', b);
  $('#stop').classList.toggle('hidden', !b);
  if (!b) setStatus('');
}

function setStatus(text) {
  S.status = text;
  const s = $('#status'); s.innerHTML = '';
  if (text) { s.appendChild(el('span', 'pulse')); s.appendChild(document.createTextNode(text)); }
}

// ---------- rendering messages ----------
function splitNotes(text) {
  const m = text.match(/\n\n\[((?:Attached file|Image|PDF|File) [\s\S]*)\]$/);
  if (!m) return { text, notes: [] };
  return { text: text.slice(0, m.index), notes: m[1].split('\n') };
}

function renderUser(msg) {
  const wrap = el('div', 'msg user');
  const col = el('div');
  col.style.maxWidth = '80%';
  const row = el('div', 'attach-row');
  let text = '';
  for (const b of msg.content) {
    if (b.type === 'image') { const img = el('img'); img.src = 'data:' + b.source.media_type + ';base64,' + b.source.data; row.appendChild(img); }
    else if (b.type === 'document') row.appendChild(el('span', 'file-chip', '📄 ' + (b.title || 'document')));
    else if (b.type === 'text') text += b.text;
  }
  const { text: t, notes } = splitNotes(text);
  for (const n of notes) { const m = n.match(/^(?:Attached file|Image|PDF|File) "([^"]+)"/); if (m && !row.textContent.includes(m[1])) row.appendChild(el('span', 'file-chip', '📎 ' + m[1])); }
  if (row.childNodes.length) col.appendChild(row);
  const bubble = el('div', 'bubble', t.trim() || (row.childNodes.length ? '' : '(empty)'));
  bubble.style.maxWidth = 'none';
  if (t.trim()) col.appendChild(bubble);
  wrap.appendChild(col);
  return wrap;
}

function summarizeInput(input) {
  if (!input || typeof input !== 'object') return '';
  const parts = [];
  for (const k of ['action', 'path', 'command', 'query', 'url', 'question', 'window', 'name', 'text', 'pattern']) {
    if (input[k] != null && typeof input[k] !== 'object') parts.push(String(input[k]).slice(0, 80));
  }
  if (input.steps) parts.push(input.steps.map(s => s.op || Object.keys(s)[0]).join(', '));
  if (input.paths) parts.push(input.paths.join(', ').slice(0, 80));
  return parts.join(' · ');
}

function toolCard(block) {
  const card = el('div', 'tool');
  const head = el('div', 'tool-head');
  head.appendChild(el('span', 'dot'));
  head.appendChild(el('span', 'name', block.name));
  head.appendChild(el('span', 'sum', summarizeInput(block.input)));
  card.appendChild(head);
  const body = el('div', 'tool-body');
  body.appendChild(el('div', 'lbl', 'input'));
  const pre = el('pre', '', JSON.stringify(block.input, null, 2));
  body.appendChild(pre);
  body.appendChild(el('div', 'lbl result-lbl hidden', 'result'));
  card.appendChild(body);
  head.onclick = () => card.classList.toggle('open');
  S.cards[block.id] = card;
  return card;
}

function fillResult(id, text, images, isErr) {
  const card = S.cards[id]; if (!card) return;
  card.classList.add('done'); if (isErr) card.classList.add('err');
  const body = $('.tool-body', card);
  $('.result-lbl', body).classList.remove('hidden');
  if (text) body.appendChild(el('pre', '', text));
  for (const src of images || []) { const img = el('img'); img.src = src; body.appendChild(img); }
  if (isErr) card.classList.add('open');
}

function searchCard(block) {
  const card = el('div', 'tool search');
  const head = el('div', 'tool-head');
  head.appendChild(el('span', 'dot'));
  head.appendChild(el('span', 'name', 'web_search'));
  head.appendChild(el('span', 'sum', (block.input && block.input.query) || ''));
  card.appendChild(head);
  const body = el('div', 'tool-body');
  card.appendChild(body);
  head.onclick = () => card.classList.toggle('open');
  S.cards[block.id] = card;
  return card;
}

function fillSearch(block) {
  const card = S.cards[block.tool_use_id]; if (!card) return;
  card.classList.add('done');
  const body = $('.tool-body', card);
  const items = Array.isArray(block.content) ? block.content : [];
  if (!Array.isArray(block.content)) { card.classList.add('err'); body.appendChild(el('pre', '', JSON.stringify(block.content))); return; }
  $('.sum', card).textContent += ' · ' + items.length + ' results';
  for (const it of items) { const a = el('a', '', it.title || it.url); a.href = it.url; a.target = '_blank'; a.rel = 'noopener'; const d = el('div'); d.appendChild(a); body.appendChild(d); }
}

function renderAssistant(msg) {
  const wrap = el('div', 'msg assistant');
  const bubble = el('div', 'bubble');
  for (const b of msg.content) {
    if (b.type === 'text') { if (b.text && b.text.trim()) bubble.appendChild(md(b.text)); }
    else if (b.type === 'tool_use') bubble.appendChild(toolCard(b));
    else if (b.type === 'server_tool_use') bubble.appendChild(searchCard(b));
    else if (b.type === 'web_search_tool_result') fillSearch(b);
    else if (b.type === 'thinking' && b.thinking) { const d = el('details', 'think'); d.appendChild(el('summary', '', 'Thought')); d.appendChild(el('pre', '', b.thinking)); bubble.appendChild(d); }
  }
  wrap.appendChild(bubble);
  return wrap;
}

function renderMessages() {
  const box = $('#messages'); box.innerHTML = ''; S.cards = {};
  for (const m of S.chat.messages) {
    if (m.role === 'user') {
      if (m.content.length && m.content[0].type === 'tool_result') {
        for (const r of m.content) {
          let text = '', images = [];
          const c = r.content;
          if (typeof c === 'string') text = c;
          else for (const b of c || []) { if (b.type === 'text') text += b.text; if (b.type === 'image') images.push('data:' + b.source.media_type + ';base64,' + b.source.data); }
          fillResult(r.tool_use_id, text, images, !!r.is_error);
        }
      } else box.appendChild(renderUser(m));
    } else box.appendChild(renderAssistant(m));
  }
  scrollDown(true);
}

function scrollDown(force) {
  const box = $('#messages');
  const near = box.scrollHeight - box.scrollTop - box.clientHeight < 120;
  if (force || near) box.scrollTop = box.scrollHeight;
}

function notice(text, cls, button) {
  const n = el('div', 'notice ' + (cls || ''), text);
  if (button) { const b = el('button', 'ghost', button.label); b.onclick = button.onclick; n.appendChild(b); }
  $('#messages').appendChild(n);
  scrollDown();
}

// ---------- live streaming ----------
function liveEl() {
  if (S.live) return S.live;
  const wrap = el('div', 'msg assistant');
  const bubble = el('div', 'bubble');
  const think = el('details', 'think hidden'); think.appendChild(el('summary', '', 'Thinking')); think.appendChild(el('pre'));
  const body = el('div', 'md');
  bubble.appendChild(think); bubble.appendChild(body);
  wrap.appendChild(bubble);
  $('#messages').appendChild(wrap);
  S.live = { wrap, body, think, text: '', thinking: '', raf: null };
  return S.live;
}
function liveRender() {
  const l = S.live; if (!l || l.raf) return;
  l.raf = requestAnimationFrame(() => {
    l.raf = null;
    const n = md(l.text); l.body.replaceWith(n); l.body = n;
    if (l.thinking) { l.think.classList.remove('hidden'); $('pre', l.think).textContent = l.thinking; }
    scrollDown();
  });
}
function liveClear() { if (S.live) { S.live.wrap.remove(); S.live = null; } }

function subscribe(id) {
  const es = new EventSource('/api/chats/' + id + '/events?token=' + TOKEN);
  S.es = es;
  es.onmessage = (e) => {
    if (!S.chat || S.chat.id !== id) return;
    const ev = JSON.parse(e.data); const d = ev.data || {};
    switch (ev.type) {
      case 'hello': setBusy(!!d.busy); break;
      case 'snapshot': { setBusy(true); const l = liveEl(); l.text = d.text || ''; l.thinking = d.thinking || ''; liveRender(); setStatus(d.status || 'working'); if (d.tool && d.tool.confirm) showConfirm(d.tool.confirm); break; }
      case 'user': S.chat.messages.push(d.message); $('#messages').appendChild(renderUser(d.message)); scrollDown(true); setBusy(true); break;
      case 'status': setStatus(d.text); break;
      case 'model': setStatus('using ' + d.model); break;
      case 'text': { const l = liveEl(); l.text += d.text; liveRender(); break; }
      case 'thinking': { const l = liveEl(); l.thinking += d.text; liveRender(); break; }
      case 'assistant': {
        liveClear();
        S.chat.messages.push(d.message);
        const node = renderAssistant(d.message);
        const u = d.usage || {};
        const line = el('div', 'usage-line', `${fmt$(d.cost)} · in ${u.input || 0} + cached ${u.cache_read || 0} · out ${u.output || 0}` + (u.web_searches ? ` · ${u.web_searches} searches` : ''));
        node.appendChild(line);
        $('#messages').appendChild(node);
        S.chat.cost = d.chat_cost; $('#chat-cost').textContent = fmt$(d.chat_cost);
        scrollDown(); break;
      }
      case 'tool_start': { const c = S.cards[d.id]; if (c) c.classList.add('running'); setStatus('running ' + d.name + ' ' + summarizeInput(d.input)); break; }
      case 'tool_result': {
        fillResult(d.id, d.text, d.images, d.error);
        const msgs = S.chat.messages; const last = msgs[msgs.length - 1];
        const block = { type: 'tool_result', tool_use_id: d.id, content: [{ type: 'text', text: d.text }], is_error: d.error };
        if (last && last.role === 'user' && last.content[0] && last.content[0].type === 'tool_result') last.content.push(block); else msgs.push({ role: 'user', content: [block] });
        scrollDown(); break;
      }
      case 'confirm': showConfirm(d); break;
      case 'confirm_done': { const c = document.getElementById('confirm-' + CSS.escape(d.id)); if (c) c.remove(); break; }
      case 'title': S.chat.title = d.title; $('#title').value = d.title; loadState(); break;
      case 'notice': notice(d.text); break;
      case 'error': liveClear(); notice(d.message, 'err'); setBusy(false); loadState(); break;
      case 'stopped': liveClear(); notice('Stopped.'); setBusy(false); loadState(); break;
      case 'done': {
        liveClear(); setBusy(false);
        S.chat.cost = d.chat_cost; $('#chat-cost').textContent = fmt$(d.chat_cost);
        if (d.clipboard_changed) notice('The clipboard was changed during this turn.', '', { label: 'Restore clipboard', onclick: async (ev) => { try { await api('/api/chats/' + id + '/clipboard/restore', { method: 'POST' }); ev.target.textContent = 'restored'; } catch (e) { ev.target.textContent = e.message; } } });
        loadState(); break;
      }
    }
  };
  es.onerror = () => { /* browser retries */ };
}

function showConfirm(d) {
  if (document.getElementById('confirm-' + d.id)) return;
  const box = el('div', 'confirm'); box.id = 'confirm-' + d.id;
  box.appendChild(el('div', 'q', `Allow ${d.tool} (${d.action})?`));
  box.appendChild(el('pre', '', JSON.stringify(d.args, null, 2)));
  const btns = el('div', 'btns');
  const ok = el('button', 'primary', 'Allow'); const no = el('button', '', 'Deny');
  ok.onclick = () => api('/api/confirm', { method: 'POST', json: { id: d.id, ok: true } });
  no.onclick = () => api('/api/confirm', { method: 'POST', json: { id: d.id, ok: false } });
  btns.appendChild(ok); btns.appendChild(no); box.appendChild(btns);
  $('#messages').appendChild(box); scrollDown(true);
}

// ---------- composer ----------
function renderChips() {
  const c = $('#chips'); c.innerHTML = '';
  S.atts.forEach((a, i) => {
    const chip = el('div', 'chip');
    if (a.file.type.startsWith('image/')) { const img = el('img'); img.src = URL.createObjectURL(a.file); chip.appendChild(img); }
    chip.appendChild(el('span', '', a.file.name + ' (' + Math.round(a.file.size / 1024) + ' KB)'));
    if (!a.file.type.startsWith('image/')) {
      const lab = el('label'); const cb = el('input'); cb.type = 'checkbox'; cb.checked = a.inline; cb.onchange = () => a.inline = cb.checked;
      lab.appendChild(cb); lab.appendChild(document.createTextNode('send inline')); lab.title = 'Unchecked: saved on the PC and read with TokenSaver (cheaper). Checked: sent to the API in full.';
      chip.appendChild(lab);
    }
    const rm = el('span', 'rm', '✕'); rm.onclick = () => { S.atts.splice(i, 1); renderChips(); };
    chip.appendChild(rm);
    c.appendChild(chip);
  });
}
function addFiles(files) {
  for (const f of files) S.atts.push({ file: f, inline: false });
  renderChips();
}

async function send() {
  const text = $('#input').value.trim();
  if (!text && !S.atts.length) return;
  if (!S.chat) await newChat();
  const fd = new FormData();
  fd.append('text', text);
  fd.append('model', $('#model').value);
  fd.append('meta', JSON.stringify(S.atts.map(a => ({ name: a.file.name, inline: a.inline }))));
  for (const a of S.atts) fd.append('files', a.file, a.file.name);
  try {
    await api('/api/chats/' + S.chat.id + '/send', { method: 'POST', body: fd });
    $('#input').value = ''; autosize(); S.atts = []; renderChips(); setBusy(true); setStatus('starting');
  } catch (e) { notice(e.message, 'err'); if (/API key/.test(e.message)) openSettings(); }
}

function autosize() { const t = $('#input'); t.style.height = 'auto'; t.style.height = Math.min(220, t.scrollHeight) + 'px'; }

// ---------- settings ----------
function modal(title, body) { $('#modal-title').textContent = title; const b = $('#modal-body'); b.innerHTML = ''; b.appendChild(body); $('#modal').classList.remove('hidden'); }
function closeModal() { $('#modal').classList.add('hidden'); }

function field(form, label, input, hint) { form.appendChild(el('label', '', label)); form.appendChild(input); if (hint) form.appendChild(el('div', 'hint', hint)); return input; }
function inp(type, value, attrs = {}) { const i = el('input'); i.type = type; i.value = value == null ? '' : value; Object.assign(i, attrs); return i; }
function sel(options, value) { const s = el('select'); for (const [v, t] of options) { const o = el('option', '', t); o.value = v; s.appendChild(o); } s.value = value; return s; }
function chk(value) { const i = el('input'); i.type = 'checkbox'; i.checked = !!value; i.style.width = 'auto'; return i; }
function ta(value, rows = 4) { const t = el('textarea'); t.value = value || ''; t.rows = rows; return t; }

async function openSettings() {
  const s = await api('/api/settings');
  const form = el('div', 'form');
  form.appendChild(el('h3', '', 'API'));
  const key = field(form, 'API key', inp('password', '', { placeholder: s.has_key ? '(saved; paste to replace)' : 'sk-ant-…' }));
  const budget = field(form, 'Monthly credit (USD)', inp('number', s.budget, { step: '1' }));
  const budgetDay = field(form, 'Billing cycle day', inp('number', s.budget_day, { min: 1, max: 28 }), 'Day of month your Max credits refresh');
  const userName = field(form, 'Your name', inp('text', s.user_name));
  form.appendChild(el('h3', '', 'Models'));
  const modelOpts = [['auto', 'auto'], ...S.models.filter(m => /^claude-/.test(m.id)).map(m => [m.id, m.display_name || m.id])];
  for (const id of ['claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-5-5']) if (!modelOpts.some(o => o[0] === id)) modelOpts.push([id, id]);
  const model = field(form, 'Default model', sel(modelOpts, s.model));
  const auto = s.auto || {};
  const aSimple = field(form, 'Auto: simple', sel(modelOpts.slice(1), auto.simple));
  const aNormal = field(form, 'Auto: normal', sel(modelOpts.slice(1), auto.normal));
  const aHard = field(form, 'Auto: hard', sel(modelOpts.slice(1), auto.hard));
  const effort = field(form, 'Effort', sel([['', 'model default'], ['low', 'low'], ['medium', 'medium'], ['high', 'high']], s.effort || ''), 'Lower effort = fewer thinking tokens');
  const thinking = field(form, 'Thinking', sel([['', 'model default'], ['adaptive', 'adaptive']], s.thinking || ''));
  const maxTokens = field(form, 'Max output tokens', inp('number', s.max_tokens));
  const cacheTTL = field(form, 'Cache TTL', sel([['1h', '1 hour (best for chatting with pauses)'], ['5m', '5 minutes']], s.cache_ttl || '1h'));
  const webSearch = field(form, 'API web search', chk(s.web_search), '$0.01 per search; TokenSaver web stays free');
  const webMax = field(form, 'Max searches per reply', inp('number', s.web_search_max_uses));
  const shots = field(form, 'Screenshots per turn', inp('number', s.screenshots_per_turn));
  const sys = field(form, 'Extra instructions', ta(s.system_prompt, 3));
  form.appendChild(el('h3', '', 'Profile'));
  const profile = field(form, 'Profile', sel([['owner', 'owner (no limits)'], ['guest', 'guest (limited)']], s.profile));
  const g = s.guest || {};
  const folders = field(form, 'Guest folders', ta((g.folders || []).join('\n'), 3), 'One per line. Guest can only touch files inside these.');
  const toolsOff = field(form, 'Guest tools off', inp('text', (g.tools_off || []).join(', ')), 'tool or tool.action, comma separated');
  const confirmL = field(form, 'Guest asks first for', inp('text', (g.confirm || []).join(', ')));
  const elevated = field(form, 'Guest may run as admin', chk(g.elevated));
  form.appendChild(el('h3', '', 'TokenSaver'));
  const tsRoot = field(form, 'TokenSaver folder', inp('text', s.tokensaver_root), 'Restart after changing');
  const tsCfg = field(form, 'TokenSaver config.json', ta('', 6));
  api('/api/tokensaver/config').then(r => { tsCfg.value = r.text; tsCfg.dataset.orig = r.text; });
  form.appendChild(el('h3', '', 'Window'));
  const port = field(form, 'Port', inp('number', s.port), 'Restart after changing');
  const browser = field(form, 'Browser for the window', inp('text', s.browser || '', { placeholder: 'auto (Edge or Chrome)' }));
  const actions = el('div', 'form-actions full');
  const msg = el('span', 'save-msg');
  const save = el('button', 'primary', 'Save');
  save.onclick = async () => {
    const lines = (v) => v.split(/\n|,/).map(x => x.trim()).filter(Boolean);
    const body = {
      budget: +budget.value, budget_day: +budgetDay.value, user_name: userName.value, model: model.value,
      auto: { simple: aSimple.value, normal: aNormal.value, hard: aHard.value }, effort: effort.value, thinking: thinking.value,
      max_tokens: +maxTokens.value, cache_ttl: cacheTTL.value, web_search: webSearch.checked, web_search_max_uses: +webMax.value,
      screenshots_per_turn: +shots.value, system_prompt: sys.value, profile: profile.value,
      guest: { folders: lines(folders.value), tools_off: lines(toolsOff.value), confirm: lines(confirmL.value), elevated: elevated.checked },
      tokensaver_root: tsRoot.value, port: +port.value, browser: browser.value,
    };
    if (key.value.trim()) body.api_key = key.value.trim();
    try {
      S.settings = await api('/api/settings', { method: 'PUT', json: body });
      if (tsCfg.value !== tsCfg.dataset.orig) await api('/api/tokensaver/config', { method: 'PUT', json: { text: tsCfg.value } });
      msg.textContent = 'Saved'; await loadState(); await loadModels(); setTimeout(closeModal, 500);
    } catch (e) { msg.textContent = e.message; msg.style.color = 'var(--err)'; }
  };
  actions.appendChild(msg); actions.appendChild(save);
  const box = el('div'); box.appendChild(form); box.appendChild(actions);
  modal('Settings', box);
}

async function openUsage() {
  const u = await api('/api/usage');
  const s = u.summary; const box = el('div');
  const stat = (k, v) => { const d = el('div', 'stat'); d.appendChild(el('span', '', k)); d.appendChild(el('span', '', v)); box.appendChild(d); };
  stat('Billing cycle', new Date(s.from).toLocaleDateString() + ' → today');
  stat('Spent', fmt$(s.cost) + ' of $' + u.budget);
  stat('API calls', s.calls); stat('Tool calls', s.tool_calls);
  stat('Input tokens', s.input.toLocaleString() + ' (cached ' + s.cache_read.toLocaleString() + ')');
  stat('Output tokens', s.output.toLocaleString()); stat('Web searches', s.searches);
  if (s.by_day && s.by_day.length) {
    box.appendChild(el('h3', '', 'By day'));
    const bar = el('div', 'bar'); const max = Math.max(...s.by_day.map(d => d.cost));
    for (const d of s.by_day) { const b = el('div'); b.style.height = (max ? d.cost / max * 100 : 0) + '%'; b.title = d.day + ' ' + fmt$(d.cost); bar.appendChild(b); }
    box.appendChild(bar);
  }
  box.appendChild(el('h3', '', 'By model'));
  for (const [m, c] of Object.entries(s.by_model || {}).sort((a, b) => b[1] - a[1])) stat(m, fmt$(c));
  box.appendChild(el('h3', '', 'Tools used'));
  for (const [t, n] of Object.entries(s.by_tool || {}).sort((a, b) => b[1] - a[1])) stat(t, n + '×');
  modal('Usage', box);
}

// ---------- wiring ----------
$('#new-chat').onclick = newChat;
$('#send').onclick = send;
$('#stop').onclick = () => S.chat && api('/api/chats/' + S.chat.id + '/stop', { method: 'POST' });
$('#input').addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); if (!S.busy) send(); } });
$('#input').addEventListener('input', autosize);
$('#input').addEventListener('paste', (e) => { const files = [...(e.clipboardData.files || [])]; if (files.length) { e.preventDefault(); addFiles(files); } });
$('#attach').onclick = () => $('#file').click();
$('#file').onchange = (e) => { addFiles([...e.target.files]); e.target.value = ''; };
document.addEventListener('dragover', (e) => { e.preventDefault(); document.body.classList.add('dragover'); });
document.addEventListener('dragleave', () => document.body.classList.remove('dragover'));
document.addEventListener('drop', (e) => { e.preventDefault(); document.body.classList.remove('dragover'); if (e.dataTransfer.files.length) addFiles([...e.dataTransfer.files]); });
$('#title').addEventListener('change', async () => { if (!S.chat) return; await api('/api/chats/' + S.chat.id, { method: 'PATCH', json: { title: $('#title').value } }); S.chat.title = $('#title').value; loadState(); });
$('#model').addEventListener('change', async () => { if (!S.chat) return; await api('/api/chats/' + S.chat.id, { method: 'PATCH', json: { model: $('#model').value } }); S.chat.model = $('#model').value; });
$('#open-settings').onclick = openSettings;
$('#open-usage').onclick = openUsage;
$('#modal-close').onclick = closeModal;
$('#modal').addEventListener('click', (e) => { if (e.target.id === 'modal') closeModal(); });
$('#head').addEventListener('click', (e) => { if (e.target.id === 'head') $('#side').classList.toggle('show'); });

(async () => {
  await loadState();
  loadModels();
  const last = localStorage.getItem('apichat.last');
  if (last && S.chats.some(c => c.id === last)) await openChat(last);
  else if (S.chats.length) await openChat(S.chats[0].id);
  else await newChat();
  if (!S.settings.has_key) openSettings();
})();
})();
