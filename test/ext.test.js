// The in-page function and the extension packaging.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { JSDOM } = require('jsdom');
const root = path.join(__dirname, '..');

test('manifest and package agree and every referenced file exists', () => {
  const m = JSON.parse(fs.readFileSync(path.join(root, 'manifest.json'), 'utf8'));
  const p = require('../package.json');
  assert.equal(m.manifest_version, 3);
  assert.equal(m.version, p.version);
  const files = [m.side_panel.default_path, m.background.service_worker, ...Object.values(m.icons), ...Object.values(m.action.default_icon)];
  for (const f of files) assert.ok(fs.existsSync(path.join(root, f)), f);
  for (const perm of ['sidePanel', 'tabs', 'scripting', 'storage']) assert.ok(m.permissions.includes(perm), perm);
});

test('the panel page has no inline scripts (MV3 CSP) and its scripts exist', () => {
  const html = fs.readFileSync(path.join(root, 'src/panel.html'), 'utf8');
  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /\son[a-z]+=/i, 'no inline handlers');
  const srcs = [...html.matchAll(/<script src="([^"]+)">/g)].map(m => m[1]);
  assert.deepEqual(srcs, ['../vendor/marked.min.js', '../vendor/purify.min.js', 'core.js', 'ext.js', 'app.js']);
  for (const s of srcs) assert.ok(fs.existsSync(path.join(root, 'src', s)), s);
  for (const f of ['core.js', 'ext.js', 'app.js', 'background.js']) new Function(fs.readFileSync(path.join(root, 'src', f), 'utf8').replace(/^if \(typeof module.*$/m, ''));
});

function pageWindow(body) {
  const dom = new JSDOM('<!doctype html><title>Shop</title><body>' + body + '</body>', { url: 'https://shop.test/', pretendToBeVisual: true, runScripts: 'outside-only' });
  const w = dom.window;
  Object.defineProperty(w.HTMLElement.prototype, 'innerText', { get() { return this.textContent; }, configurable: true });
  w.HTMLElement.prototype.getBoundingClientRect = function () { return this.hasAttribute('hidden') ? { width: 0, height: 0 } : { width: 10, height: 10 }; };
  w.HTMLElement.prototype.scrollIntoView = () => {};
  if (!w.CSS) w.CSS = {};
  if (!w.CSS.escape) w.CSS.escape = (s) => s;
  const Ext = require('../src/ext.js');
  const run = new w.Function('fn', 'a', 'args', 'return fn(a, args)');
  const src = Ext.inPage.toString();
  const inPage = w.eval('(' + src + ')');
  return { w, call: (a, args = {}) => run(inPage, a, Object.assign({ chars: 20000 }, args)) };
}

test('inPage finds visible elements with refs, clicks, types and reads text in chunks', () => {
  const { w, call } = pageWindow('<p>Welcome to the shop</p><a href="/cart">Cart</a><button id="go">Sign in</button><button hidden>Ghost</button><input placeholder="Email"><select><option>Small</option><option value="l">Large</option></select>');
  const d = w.document;
  const found = call('find');
  assert.match(found, /^Shop\nhttps:\/\/shop\.test\//);
  assert.match(found, /e1 a "Cart" → https:\/\/shop\.test\/cart/);
  assert.match(found, /e2 button "Sign in"/);
  assert.doesNotMatch(found, /Ghost/);
  assert.match(found, /e4 select ".*" options: Small \| Large/);
  assert.match(call('find', { query: 'email' }), /^e3 input:text "Email"$/m);
  assert.match(call('find', { query: 'sign' }), /e2 button/, 'refs stay stable');

  let clicked = 0; d.querySelector('#go').addEventListener('click', () => clicked++);
  assert.match(call('click', { ref: 'e2' }), /clicked e2 button/);
  assert.equal(clicked, 1);
  assert.throws(() => call('click', { ref: 'e99' }), /run page find again/);

  let changes = 0; d.querySelector('input').addEventListener('input', () => changes++);
  call('type', { ref: 'e3', text: 'a@b.c' });
  assert.equal(d.querySelector('input').value, 'a@b.c');
  call('type', { ref: 'e3', text: 'x', clear: false });
  assert.equal(d.querySelector('input').value, 'a@b.cx');
  assert.equal(changes, 2);
  assert.match(call('type', { ref: 'e4', text: 'large' }), /selected "Large"/);
  assert.equal(d.querySelector('select').value, 'l');

  const t = call('text', { chars: 10 });
  assert.match(t, /characters 0-10 of \d+\n\nWelcome to/);
  assert.match(t, /next from=10/);
  assert.match(call('text', { from: 10, chars: 1000 }), /the shop/);
});

test('restricted pages are recognised', () => {
  const Ext = require('../src/ext.js');
  assert.ok(Ext.restricted('chrome://settings'));
  assert.ok(Ext.restricted('https://chromewebstore.google.com/detail/x'));
  assert.ok(!Ext.restricted('https://example.com'));
});
