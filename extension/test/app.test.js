// Runs the side panel in jsdom with a fake IndexedDB, a fake API and a fake chrome.*.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { JSDOM } = require('jsdom');
require('fake-indexeddb/auto');

const root = path.join(__dirname, '..');
// Inline the panel's scripts (jsdom does not load chrome-extension:// files).
const html = fs.readFileSync(path.join(root, 'src/panel.html'), 'utf8')
  .replace(/<link rel="stylesheet" href="app.css">/, '')
  .replace(/<script src="([^"]+)"><\/script>/g, (m, src) => '<script>' + fs.readFileSync(path.join(root, 'src', src), 'utf8').replace(/<\/script/gi, '<\\/script') + '</script>');

function toolUse(id, name, input) {
  return sse([
    { type: 'message_start', message: { id: 'm', model: 'claude-haiku-5-5', usage: { input_tokens: 50 } } },
    { type: 'content_block_start', index: 0, content_block: { type: 'tool_use', id, name, input: {} } },
    { type: 'content_block_delta', index: 0, delta: { type: 'input_json_delta', partial_json: JSON.stringify(input) } },
    { type: 'content_block_stop', index: 0 },
    { type: 'message_delta', delta: { stop_reason: 'tool_use' }, usage: { output_tokens: 5 } },
  ]);
}

// A tiny fake of the chrome APIs the panel uses.
function fakeChrome(state) {
  const tabs = state.tabs;
  const find = (id) => { const t = tabs.find(t => t.id === id); if (!t) throw new Error('No tab with id: ' + id); return t; };
  return {
    runtime: { getURL: (p) => 'chrome-extension://abc/' + p },
    storage: { session: {
      get: async (k) => (k in state.session ? { [k]: state.session[k] } : {}),
      set: async (o) => { Object.assign(state.session, o); },
      remove: async (k) => { delete state.session[k]; },
    } },
    tabs: {
      query: async (q) => tabs.filter(t => !q.active || t.active).map(t => ({ ...t })),
      get: async (id) => ({ ...find(id) }),
      create: async (o) => { const t = { id: 100 + tabs.length, windowId: 1, url: o.url, title: 'New', active: o.active !== false, status: 'complete' }; tabs.push(t); state.calls.push(['create', o]); return { ...t }; },
      update: async (id, o) => { const t = find(id); if (o.active) tabs.forEach(x => { x.active = x === t; }); if (o.url) t.url = o.url; state.calls.push(['update', id, o]); return { ...t }; },
      remove: async (id) => { state.calls.push(['remove', id]); tabs.splice(tabs.indexOf(find(id)), 1); },
      reload: async () => {}, goBack: async () => {},
      captureVisibleTab: async () => 'data:image/jpeg;base64,QUJD',
    },
    windows: { update: async () => {} },
    scripting: { executeScript: async ({ target, func, args }) => { state.calls.push(['script', target.tabId, args[0], args[1]]); return [{ result: state.page(args[0], args[1]) }]; } },
  };
}
const chromeState = { tabs: [], session: {}, calls: [], page: () => '' };

function sse(events) { return events.map(e => 'data: ' + JSON.stringify(e) + '\n\n').join(''); }
function reply(text, model = 'claude-haiku-5-5') {
  return sse([
    { type: 'message_start', message: { id: 'm', model, usage: { input_tokens: 100 } } },
    { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
    { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text } },
    { type: 'content_block_stop', index: 0 },
    { type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 20 } },
  ]);
}

function fakeFetch(log) {
  return async (url, opts = {}) => {
    log.push({ url, opts });
    const body = opts.body ? JSON.parse(opts.body) : null;
    const ok = (text, type = 'text/event-stream') => new Response(text, { status: 200, headers: { 'content-type': type } });
    if (url.endsWith('/v1/models')) return ok(JSON.stringify({ data: [{ id: 'claude-haiku-5-5', display_name: 'Haiku' }] }), 'application/json');
    if (url.endsWith('/v1/messages')) {
      if (fakeFetch.script && body.max_tokens > 20) {
        const last = body.messages[body.messages.length - 1];
        const n = body.messages.filter(m => m.role === 'assistant').length;
        fakeFetch.seen.push(body);
        if (n < fakeFetch.script.length) { const [name, input] = fakeFetch.script[n]; return ok(toolUse('tu' + n, name, input)); }
        return ok(reply('Done: ' + last.content[0].content[0].text));
      }
      if (opts.headers['x-api-key'] !== 'sk-test') return new Response(JSON.stringify({ error: { type: 'authentication_error', message: 'bad key' } }), { status: 401 });
      if (body.max_tokens === 5) return ok(reply('1'));
      if (body.max_tokens === 20) return ok(reply('Quick hello'));
      if (fakeFetch.bridge) {
        const last = body.messages[body.messages.length - 1];
        if (last.content[0].type === 'tool_result') return ok(reply('Done: ' + last.content[0].content[0].text));
        return ok(sse([
          { type: 'message_start', message: { id: 'm', model: 'claude-haiku-5-5', usage: { input_tokens: 50 } } },
          { type: 'content_block_start', index: 0, content_block: { type: 'tool_use', id: 'tu1', name: 'run', input: {} } },
          { type: 'content_block_delta', index: 0, delta: { type: 'input_json_delta', partial_json: '{"command":"dir"}' } },
          { type: 'content_block_stop', index: 0 },
          { type: 'message_delta', delta: { stop_reason: 'tool_use' }, usage: { output_tokens: 5 } },
        ]));
      }
      return ok(reply('Hello **there**', 'claude-haiku-5-5'));
    }
    if (url.includes('/bridge/')) {
      if (!fakeFetch.bridge) throw new TypeError('Failed to fetch');
      if (opts.headers['x-bridge-key'] !== 'bk') return new Response(JSON.stringify({ error: { message: 'bad bridge key' } }), { status: 401 });
      if (url.endsWith('/bridge/info')) return ok(JSON.stringify({ app: 'apichat', version: '0.3.0', tokensaver: '1.0', profile: 'guest', instructions: 'TS', guest_folders: ['D:\\Work'], tools: [{ name: 'read', description: 'r', input_schema: { type: 'object' } }, { name: 'run', description: 'x', input_schema: { type: 'object' } }] }), 'application/json');
      if (url.endsWith('/bridge/call')) {
        if (body.name === 'run' && !body.approved) return new Response(JSON.stringify({ confirm: { tool: 'run', action: 'run' } }), { status: 202 });
        return ok(JSON.stringify({ text: 'ran ' + body.name + (body.approved ? ' (approved)' : ''), images: [], error: false }), 'application/json');
      }
    }
    throw new Error('unexpected ' + url);
  };
}

function load(log) {
  const dom = new JSDOM(html, {
    runScripts: 'dangerously', pretendToBeVisual: true, url: 'http://localhost/src/panel.html',
    beforeParse(w) {
      w.chrome = fakeChrome(chromeState);
      Object.defineProperty(w, 'crypto', { value: require('node:crypto').webcrypto });
      w.fetch = fakeFetch(log); w.Response = Response; w.TextEncoder = TextEncoder; w.TextDecoder = TextDecoder;
      w.indexedDB = indexedDB;
      w.confirm = () => true; w.scrollTo = () => {};
      w.HTMLElement.prototype.scrollIntoView = () => {};
    },
  });
  return dom;
}

const until = async (fn, ms = 8000) => { const t0 = Date.now(); while (!fn()) { if (Date.now() - t0 > ms) throw new Error('timeout'); await new Promise(r => setTimeout(r, 25)); } };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

test('first run sets key + password, chats, then a reopen asks for the password', async () => {
  let log = [];
  let dom = load(log); let d = dom.window.document;
  await until(() => !d.querySelector('#lock').classList.contains('hidden'));
  assert.ok(!d.querySelector('#lock-setup').classList.contains('hidden'), 'setup shown');
  d.querySelector('#lock-key').value = 'sk-test';
  d.querySelector('#lock-pw1').value = 'pass1234';
  d.querySelector('#lock-pw2').value = 'nope';
  d.querySelector('#lock-form').dispatchEvent(new dom.window.Event('submit', { cancelable: true }));
  await until(() => d.querySelector('#lock-msg').textContent.includes('do not match'));
  d.querySelector('#lock-pw2').value = 'pass1234';
  d.querySelector('#lock-form').dispatchEvent(new dom.window.Event('submit', { cancelable: true }));
  await until(() => d.querySelector('#lock').classList.contains('hidden'), 20000);
  await until(() => d.querySelector('#bridge-state').textContent.includes('not running'));
  assert.ok(d.querySelector('#modal').classList.contains('hidden'), 'settings not forced open when a key exists');

  d.querySelector('#input').value = 'hello';
  d.querySelector('#send').click();
  await until(() => d.querySelector('.msg.assistant .md strong'), 20000);
  assert.equal(d.querySelector('.msg.assistant .md').textContent.trim(), 'Hello there');
  await until(() => !d.querySelector('#send').classList.contains('hidden'), 20000);
  assert.equal(d.querySelector('#title').value, 'Quick hello');
  assert.match(d.querySelector('.usage-line').textContent, /in 100/);
  assert.match(d.querySelector('#meter-spent').textContent, /\$0\.0/);
  const calls = log.filter(l => l.url.endsWith('/v1/messages')).map(l => JSON.parse(l.opts.body));
  assert.equal(calls.length, 3, 'router + reply + title');
  assert.equal(calls[1].model, 'claude-haiku-5-5', 'auto picked the simple model');
  assert.deepEqual(calls[1].tools.map(t => t.name), ['files', 'clipboard', 'tabs', 'page', 'screenshot']);
  assert.equal(calls[1].messages[0].content[0].text, 'hello');
  assert.equal(log[0].opts === undefined || log.every(l => !l.opts.headers || l.opts.headers['anthropic-dangerous-direct-browser-access'] === 'true' || l.url.includes('/bridge/')), true);

  // nothing readable without the password
  const raw = await new Promise((res) => { const r = indexedDB.open('apichat-html'); r.onsuccess = () => { const t = r.result.transaction('kv').objectStore('kv').getAll(); t.onsuccess = () => res(t.result); }; });
  assert.ok(raw.length >= 3);
  for (const v of raw) { const s = JSON.stringify(v); assert.doesNotMatch(s, /sk-test|Hello/); }

  // the unlocked key is kept for the Chrome session: reopening the panel skips the password
  assert.ok(chromeState.session.key, 'key cached in session storage');
  dom.window.close();
  log = []; dom = load(log); d = dom.window.document;
  await until(() => d.querySelector('.msg.assistant .md'), 20000);
  assert.ok(d.querySelector('#lock').classList.contains('hidden'));

  // after Chrome restarts (session cleared): password required, wrong one refused, right one restores the chat
  dom.window.close();
  chromeState.session = {};
  log = []; dom = load(log); d = dom.window.document;
  await until(() => !d.querySelector('#lock').classList.contains('hidden'));
  assert.ok(d.querySelector('#lock-setup').classList.contains('hidden'));
  assert.ok(!d.querySelector('#lock-open').classList.contains('hidden'));
  d.querySelector('#lock-pw').value = 'wrong';
  d.querySelector('#lock-form').dispatchEvent(new dom.window.Event('submit', { cancelable: true }));
  await until(() => d.querySelector('#lock-msg').textContent === 'wrong password', 20000);
  assert.ok(!d.querySelector('#lock').classList.contains('hidden'));
  d.querySelector('#lock-pw').value = 'pass1234';
  d.querySelector('#lock-form').dispatchEvent(new dom.window.Event('submit', { cancelable: true }));
  await until(() => d.querySelector('#lock').classList.contains('hidden'), 20000);
  await until(() => d.querySelector('#chat-list .chat-item'));
  assert.equal(d.querySelector('#chat-list .chat-item .t').textContent, 'Quick hello');
  await until(() => d.querySelector('.msg.assistant .md'));
  assert.equal(d.querySelector('.msg.assistant .md').textContent.trim(), 'Hello there');
  await sleep(50);
  dom.window.close();
});

test('with APIChat running, tools go through the bridge and confirmations show Allow/Deny', async () => {
  fakeFetch.bridge = true; chromeState.session = {};
  const log = [];
  const dom = load(log); const d = dom.window.document;
  await until(() => !d.querySelector('#lock').classList.contains('hidden'));
  d.querySelector('#lock-pw').value = 'pass1234';
  d.querySelector('#lock-form').dispatchEvent(new dom.window.Event('submit', { cancelable: true }));
  await until(() => d.querySelector('#lock').classList.contains('hidden'), 20000);
  await until(() => d.querySelector('#bridge-state').textContent.includes('bad bridge key'));
  // set the bridge key in Settings
  d.querySelector('#open-settings').click();
  await until(() => !d.querySelector('#modal').classList.contains('hidden'));
  const inputs = [...d.querySelectorAll('#modal input')];
  const keyInput = inputs.find(i => i.placeholder && i.placeholder.includes('APIChatHTML bridge'));
  keyInput.value = 'bk';
  [...d.querySelectorAll('#modal button')].find(b => b.textContent === 'Save').click();
  await until(() => d.querySelector('#bridge-state').textContent.includes('connected (guest)'), 20000);
  await until(() => d.querySelector('#modal').classList.contains('hidden'), 20000);

  d.querySelector('#new-chat').click();
  await until(() => d.querySelectorAll('#chat-list .chat-item').length === 2);
  d.querySelector('#model').value = 'claude-haiku-5-5';
  d.querySelector('#model').dispatchEvent(new dom.window.Event('change'));
  d.querySelector('#input').value = 'list the folder';
  d.querySelector('#send').click();
  await until(() => d.querySelector('.confirm'), 20000);
  assert.match(d.querySelector('.confirm .q').textContent, /Allow run \(run\)/);
  assert.ok(d.querySelector('.tool.running'), 'tool card shows running');
  [...d.querySelectorAll('.confirm button')].find(b => b.textContent === 'Allow').click();
  await until(() => d.querySelector('.tool.done'), 20000);
  await until(() => !d.querySelector('#send').classList.contains('hidden'), 20000);
  const texts = [...d.querySelectorAll('.msg.assistant .md')].map(e => e.textContent.trim());
  assert.ok(texts.includes('Done: ran run (approved)'), JSON.stringify(texts));
  assert.match(d.querySelector('.tool.done .tool-body').textContent, /ran run \(approved\)/);
  const calls = log.filter(l => l.url.endsWith('/bridge/call')).map(l => JSON.parse(l.opts.body));
  assert.deepEqual(calls.map(c => c.approved), [false, true]);
  assert.deepEqual(calls[1].input, { command: 'dir' });
  const api = log.filter(l => l.url.endsWith('/v1/messages')).map(l => JSON.parse(l.opts.body));
  assert.deepEqual(api[0].tools.map(t => t.name), ['read', 'run', 'tabs', 'page', 'screenshot']);
  assert.match(api[0].system[0].text, /guest profile.*D:\\Work/);
  await sleep(50);
  dom.window.close();
  fakeFetch.bridge = false;
});

async function openUnlocked(log) {
  const dom = load(log); const d = dom.window.document;
  await until(() => !d.querySelector('#lock').classList.contains('hidden') || d.querySelector('#chat-list .chat-item'));
  if (!d.querySelector('#lock').classList.contains('hidden')) {
    d.querySelector('#lock-pw').value = 'pass1234';
    d.querySelector('#lock-form').dispatchEvent(new dom.window.Event('submit', { cancelable: true }));
    await until(() => d.querySelector('#lock').classList.contains('hidden'), 20000);
  }
  await until(() => d.querySelector('#chat-list .chat-item'));
  d.querySelector('#new-chat').click();
  await sleep(50);
  d.querySelector('#model').value = 'claude-haiku-5-5';
  d.querySelector('#model').dispatchEvent(new dom.window.Event('change'));
  return dom;
}

test('tab and page tools run in Chrome, even when APIChat is connected, and clicks ask first', async () => {
  fakeFetch.bridge = true;
  chromeState.tabs = [
    { id: 7, windowId: 1, url: 'https://example.com/', title: 'Example', active: true, status: 'complete' },
    { id: 8, windowId: 1, url: 'chrome://settings', title: 'Settings', active: false, status: 'complete' },
  ];
  chromeState.calls = [];
  chromeState.page = (a, args) => a === 'find' ? 'Example\nhttps://example.com/\ne1 button "Sign in"' : a === 'click' ? 'clicked ' + args.ref : 'text';
  fakeFetch.script = [
    ['tabs', { action: 'list' }],
    ['page', { action: 'find', query: 'sign in' }],
    ['page', { action: 'click', ref: 'e1' }],
    ['page', { action: 'text', tab: 8 }],
    ['screenshot', {}],
  ];
  fakeFetch.seen = [];
  const log = [];
  const dom = await openUnlocked(log); const d = dom.window.document;
  d.querySelector('#input').value = 'sign me in';
  d.querySelector('#send').click();
  await until(() => d.querySelector('.confirm'), 20000);
  assert.match(d.querySelector('.confirm .q').textContent, /Allow page \(click\)/);
  [...d.querySelectorAll('.confirm button')].find(b => b.textContent === 'Allow').click();
  await until(() => !d.querySelector('#send').classList.contains('hidden'), 20000);

  const results = fakeFetch.seen.slice(1).map(b => b.messages[b.messages.length - 1].content[0]);
  const txt = (r) => r.content.map(c => c.text || '').join('');
  assert.match(txt(results[0]), /\* 7  Example  https:\/\/example\.com\//);
  assert.match(txt(results[1]), /e1 button "Sign in"/);
  assert.match(txt(results[2]), /clicked e1/);
  assert.equal(results[3].is_error, true);
  assert.match(txt(results[3]), /does not let extensions/);
  assert.equal(results[4].content.find(c => c.type === 'image').source.data, 'QUJD');
  const scripts = chromeState.calls.filter(c => c[0] === 'script');
  assert.deepEqual(scripts.map(c => [c[1], c[2]]), [[7, 'find'], [7, 'click']]);
  assert.equal(scripts[0][3].query, 'sign in');
  assert.equal(scripts[0][3].chars, 20000);
  assert.equal(log.filter(l => l.url.endsWith('/bridge/call')).length, 0, 'extension tools never go to the bridge');
  assert.match(fakeFetch.seen[0].system[0].text, /Chrome extension/);
  await sleep(50);
  dom.window.close();
  fakeFetch.script = null; fakeFetch.bridge = false;
});

test('the lock button forgets the session key', async () => {
  const dom = await openUnlocked([]); const d = dom.window.document;
  assert.ok(chromeState.session.key);
  d.querySelector('#lock-btn').click();
  await until(() => !chromeState.session.key);
  dom.window.close();
});
