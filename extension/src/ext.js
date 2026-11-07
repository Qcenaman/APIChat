// Chrome-only pieces: tab and page tools, tab screenshots, the unlocked-key
// session cache and opening the app in a tab. Uses chrome.* only, no DOM state.
const Ext = (() => {
  const isPanelTab = () => /[?&]tab=1/.test(location.search);
  const restricted = (url) => !url || /^(chrome|edge|about|devtools|view-source|chrome-extension):/i.test(url) || /^https:\/\/(chrome\.google\.com\/webstore|chromewebstore\.google\.com)/i.test(url);
  const sleep = (ms) => new Promise(r => setTimeout(r, ms));

  // The tab the user is looking at: the active tab of the last focused normal
  // window, skipping this extension's own page when it runs in a tab.
  async function activeTab() {
    let [t] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
    if (t && !t.url.startsWith(chrome.runtime.getURL(''))) return t;
    const all = (await chrome.tabs.query({})).filter(x => !x.url.startsWith(chrome.runtime.getURL('')));
    all.sort((a, b) => (b.lastAccessed || 0) - (a.lastAccessed || 0));
    if (!all.length) throw new Error('no browser tab is open');
    return all[0];
  }
  async function tabFor(id) {
    if (id == null) return activeTab();
    try { return await chrome.tabs.get(id); } catch (e) { throw new Error('no tab with id ' + id + '; use tabs list'); }
  }
  const line = (t) => (t.active ? '* ' : '  ') + t.id + '  ' + (t.title || '(no title)').slice(0, 90) + '  ' + t.url;
  async function settle(id, ms = 8000) {
    const t0 = Date.now();
    while (Date.now() - t0 < ms) { const t = await chrome.tabs.get(id); if (t.status === 'complete') return t; await sleep(150); }
    return chrome.tabs.get(id);
  }

  async function tabs(input) {
    const a = input.action;
    if (a === 'list') {
      const all = await chrome.tabs.query({});
      return all.filter(t => !t.url.startsWith(chrome.runtime.getURL(''))).map(line).join('\n') || '(no tabs)';
    }
    if (a === 'open') {
      if (!input.url) throw new Error('give a url');
      const t = await chrome.tabs.create({ url: input.url, active: !input.background });
      return 'opened\n' + line(await settle(t.id));
    }
    const t = await tabFor(input.tab);
    if (a === 'goto') { if (!input.url) throw new Error('give a url'); await chrome.tabs.update(t.id, { url: input.url }); return 'loaded\n' + line(await settle(t.id)); }
    if (a === 'activate') { await chrome.tabs.update(t.id, { active: true }); await chrome.windows.update(t.windowId, { focused: true }); return 'activated\n' + line(await chrome.tabs.get(t.id)); }
    if (a === 'close') { await chrome.tabs.remove(t.id); return 'closed ' + t.id + ' ' + t.url; }
    if (a === 'reload') { await chrome.tabs.reload(t.id); return 'reloaded\n' + line(await settle(t.id)); }
    if (a === 'back') { await chrome.tabs.goBack(t.id); await sleep(300); return 'back\n' + line(await settle(t.id)); }
    throw new Error('unknown action: ' + a);
  }

  // inPage runs inside the web page (chrome.scripting), so it must be self-contained.
  function inPage(a, args) {
    const vis = (e) => { const r = e.getBoundingClientRect(); if (r.width < 1 || r.height < 1) return false; const s = getComputedStyle(e); return s.visibility !== 'hidden' && s.display !== 'none'; };
    const label = (e) => (e.getAttribute('aria-label') || e.innerText || e.value || e.placeholder || e.title || e.name || e.alt || '').replace(/\s+/g, ' ').trim().slice(0, 80);
    const describe = (e) => {
      const tag = e.tagName.toLowerCase(), type = e.type && tag === 'input' ? ':' + e.type : '';
      let d = e.dataset.apichatRef + ' ' + tag + type + ' "' + label(e) + '"';
      if (tag === 'a' && e.href) d += ' → ' + e.href.slice(0, 100);
      if (tag === 'select') d += ' options: ' + [...e.options].slice(0, 15).map(o => o.text.trim()).join(' | ');
      if (e.disabled) d += ' (disabled)';
      return d;
    };
    const get = () => {
      let e = null;
      if (args.ref) e = document.querySelector('[data-apichat-ref="' + CSS.escape(args.ref) + '"]');
      else if (args.selector) e = document.querySelector(args.selector);
      if (!e) throw new Error(args.ref ? 'ref ' + args.ref + ' is gone; run page find again' : 'nothing matches ' + (args.selector || '(no ref or selector)'));
      return e;
    };
    const head = document.title + '\n' + location.href + '\n';
    if (a === 'text') {
      const all = (document.body ? document.body.innerText : '').replace(/\n{3,}/g, '\n\n');
      const from = Math.max(0, args.from || 0), n = args.chars;
      const part = all.slice(from, from + n);
      const more = from + n < all.length ? '\n[… ' + (all.length - from - n) + ' more characters; next from=' + (from + n) + ']' : '';
      return head + 'characters ' + from + '-' + (from + part.length) + ' of ' + all.length + '\n\n' + part + more;
    }
    if (a === 'find') {
      const sel = args.selector || 'a[href], button, input:not([type=hidden]), textarea, select, summary, [role=button], [role=link], [role=tab], [role=menuitem], [role=checkbox], [role=option], [contenteditable=""], [contenteditable=true], [onclick]';
      const q = (args.query || '').toLowerCase().split(/\s+/).filter(Boolean);
      let n = Number(document.documentElement.dataset.apichatNext || 0);
      const out = [];
      for (const e of document.querySelectorAll(sel)) {
        if (!vis(e)) continue;
        const l = label(e).toLowerCase() + ' ' + (e.id || '').toLowerCase();
        if (q.length && !q.every(w => l.includes(w))) continue;
        if (!e.dataset.apichatRef) e.dataset.apichatRef = 'e' + (++n);
        out.push(describe(e));
        if (out.length >= 60) { out.push('… more; narrow the query'); break; }
      }
      document.documentElement.dataset.apichatNext = n;
      return head + (out.join('\n') || 'nothing found' + (q.length ? ' for "' + args.query + '"' : ''));
    }
    if (a === 'click') {
      const e = get(); e.scrollIntoView({ block: 'center' });
      if (typeof e.focus === 'function') e.focus();
      e.click();
      return 'clicked ' + (e.dataset.apichatRef ? describe(e) : label(e));
    }
    if (a === 'type') {
      const e = get(); e.scrollIntoView({ block: 'center' }); e.focus();
      const text = args.text == null ? '' : String(args.text);
      if (e.tagName === 'SELECT') {
        const o = [...e.options].find(o => o.text.trim() === text || o.value === text) || [...e.options].find(o => o.text.toLowerCase().includes(text.toLowerCase()));
        if (!o) throw new Error('no option "' + text + '"');
        e.value = o.value; e.dispatchEvent(new Event('input', { bubbles: true })); e.dispatchEvent(new Event('change', { bubbles: true }));
        return 'selected "' + o.text.trim() + '"';
      }
      if (e.isContentEditable) {
        if (args.clear !== false) { document.execCommand('selectAll'); document.execCommand('delete'); }
        document.execCommand('insertText', false, text);
      } else {
        const proto = e.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
        const set = Object.getOwnPropertyDescriptor(proto, 'value').set;
        set.call(e, (args.clear === false ? e.value : '') + text);
        e.dispatchEvent(new Event('input', { bubbles: true })); e.dispatchEvent(new Event('change', { bubbles: true }));
      }
      if (args.submit) {
        const k = { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true };
        e.dispatchEvent(new KeyboardEvent('keydown', k)); e.dispatchEvent(new KeyboardEvent('keypress', k)); e.dispatchEvent(new KeyboardEvent('keyup', k));
        if (e.form) { if (e.form.requestSubmit) e.form.requestSubmit(); else e.form.submit(); }
      }
      return 'typed ' + text.length + ' chars' + (args.submit ? ' and submitted' : '');
    }
    if (a === 'scroll') {
      if (args.ref || args.selector) { get().scrollIntoView({ block: 'center' }); }
      else window.scrollBy(0, args.dy || innerHeight * 0.8);
      return 'scrolled to ' + Math.round(scrollY) + ' of ' + Math.round(document.documentElement.scrollHeight - innerHeight);
    }
    throw new Error('unknown action: ' + a);
  }

  async function page(input, cfg) {
    const a = input.action;
    const t = await tabFor(input.tab);
    if (restricted(t.url)) throw new Error('Chrome does not let extensions read or change this page (' + (t.url || 'blank') + ')');
    if (a === 'wait') { await sleep(Math.min(10000, Math.max(0, input.ms || 1000))); const u = await settle(t.id, 2000); return 'waited; ' + u.status + ' ' + u.url; }
    const args = Object.assign({}, input, { chars: (cfg && cfg.page_chars) || 20000 });
    delete args.action; delete args.tab;
    let res;
    try { [res] = await chrome.scripting.executeScript({ target: { tabId: t.id }, func: inPage, args: [a, args] }); }
    catch (e) { throw new Error('could not run in the page: ' + ((e && e.message) || e)); }
    if (!res) throw new Error('the page did not answer');
    if (res.error) throw new Error((res.error && res.error.message) || String(res.error));
    let out = res.result;
    if (a === 'click' || (a === 'type' && input.submit)) {
      await sleep(400);
      const u = await settle(t.id, 5000);
      if (u.url !== t.url) out += '\nnow at ' + u.url;
    }
    return out;
  }

  async function screenshot(input) {
    const t = await tabFor(input.tab);
    if (!t.active) { await chrome.tabs.update(t.id, { active: true }); await sleep(300); }
    const url = await chrome.tabs.captureVisibleTab(t.windowId, { format: 'jpeg', quality: 70 });
    return { text: 'screenshot of ' + t.url, images: [{ mime: 'image/jpeg', data: url.split(',')[1] }] };
  }

  async function run(name, input, cfg) {
    input = input || {};
    if (name === 'tabs') return tabs(input);
    if (name === 'page') return page(input, cfg);
    if (name === 'screenshot') return screenshot(input);
    throw new Error('unknown tool ' + name);
  }

  // chrome.storage.session lives in memory only and is not readable from web pages.
  const session = {
    async get() { try { return (await chrome.storage.session.get('key')).key || null; } catch (e) { return null; } },
    async set(v) { try { await chrome.storage.session.set({ key: v }); } catch (e) {} },
    async clear() { try { await chrome.storage.session.remove('key'); } catch (e) {} },
  };

  function openInTab() { chrome.tabs.create({ url: chrome.runtime.getURL('src/panel.html') + '?tab=1' }); }

  return { run, tabs, page, screenshot, activeTab, restricted, inPage, session, openInTab, isPanelTab };
})();
if (typeof module !== 'undefined') module.exports = Ext;
