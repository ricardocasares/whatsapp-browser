// Main-thread UI for the browser-only whatsmeow client.
const worker = new Worker('worker.js' + location.search);

const state = {
  status: 'loading',
  me: null,
  pushName: '',
  chats: new Map(),      // jid -> {jid, name, isGroup, lastTs, lastText, unread, presence}
  messages: new Map(),   // jid -> Map(id -> message)
  contacts: new Map(),   // jid -> name
  active: null,
  qr: null,
  reason: '',
};

const $ = (s) => document.querySelector(s);
const el = {
  qrScreen: $('#qr-screen'),
  qrImg: $('#qr-img'),
  qrHint: $('#qr-hint'),
  app: $('#app'),
  chatList: $('#chat-list'),
  search: $('#search'),
  header: $('#chat-header'),
  headerName: $('#chat-header-name'),
  headerSub: $('#chat-header-sub'),
  messages: $('#messages'),
  composer: $('#composer'),
  input: $('#input'),
  sendBtn: $('#send'),
  status: $('#status'),
  statusText: $('#status-text'),
  me: $('#me'),
  logout: $('#logout'),
  reconnect: $('#reconnect'),
  newChat: $('#new-chat'),
  empty: $('#empty'),
};

// ---- persistence of UI state (chats + recent messages) ----
const UI_DB = 'whatsmeow-browser-ui';
function openUIDB() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(UI_DB, 1);
    req.onupgradeneeded = () => req.result.createObjectStore('kv');
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}
async function uiLoad() {
  try {
    const db = await openUIDB();
    const data = await new Promise((res, rej) => {
      const r = db.transaction('kv', 'readonly').objectStore('kv').get('ui');
      r.onsuccess = () => res(r.result);
      r.onerror = () => rej(r.error);
    });
    if (!data) return;
    for (const c of data.chats || []) state.chats.set(c.jid, c);
    for (const [jid, list] of Object.entries(data.messages || {})) {
      const m = new Map();
      for (const msg of list) m.set(msg.id, msg);
      state.messages.set(jid, m);
    }
    for (const [jid, name] of Object.entries(data.contacts || {})) state.contacts.set(jid, name);
  } catch (e) {
    console.warn('ui state load failed', e);
  }
}
let saveTimer = null;
function uiSave() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(async () => {
    try {
      const messages = {};
      for (const [jid, m] of state.messages) {
        messages[jid] = [...m.values()].sort((a, b) => a.ts - b.ts).slice(-200);
      }
      const data = {
        chats: [...state.chats.values()].map((c) => ({ ...c, presence: '' })),
        messages,
        contacts: Object.fromEntries(state.contacts),
      };
      const db = await openUIDB();
      db.transaction('kv', 'readwrite').objectStore('kv').put(data, 'ui');
    } catch (e) {
      console.warn('ui state save failed', e);
    }
  }, 800);
}
async function uiClear() {
  try {
    const db = await openUIDB();
    db.transaction('kv', 'readwrite').objectStore('kv').clear();
  } catch (e) {}
}

// ---- helpers ----
function send(cmd) {
  worker.postMessage(cmd);
}
let reqCounter = 0;
function chatFor(jid, name, isGroup) {
  let c = state.chats.get(jid);
  if (!c) {
    c = { jid, name: name || prettyJID(jid), isGroup: !!isGroup, lastTs: 0, lastText: '', unread: 0, presence: '' };
    state.chats.set(jid, c);
  } else if (name && (c.name === prettyJID(jid) || !c.name)) {
    c.name = name;
  }
  return c;
}
function prettyJID(jid) {
  const [user, server] = jid.split('@');
  if (server === 's.whatsapp.net') return '+' + user;
  if (server === 'g.us') return 'Group ' + user;
  return user;
}
function addMessage(m, { bump = true, countUnread = true } = {}) {
  const chat = chatFor(m.chat, m.chatName, m.isGroup);
  let list = state.messages.get(m.chat);
  if (!list) {
    list = new Map();
    state.messages.set(m.chat, list);
  }
  const isNew = !list.has(m.id);
  if (m.kind === 'revoke') {
    const existing = list.get(m.id);
    if (existing) {
      existing.text = m.text;
      existing.kind = 'revoke';
      return;
    }
  }
  list.set(m.id, { ...m, status: m.status || (m.fromMe ? 'sent' : '') });
  if (bump && m.ts >= chat.lastTs) {
    chat.lastTs = m.ts;
    chat.lastText = (m.isGroup && !m.fromMe ? m.senderName + ': ' : '') + m.text;
  }
  if (isNew && countUnread && !m.fromMe && state.active !== m.chat) chat.unread = (chat.unread || 0) + 1;
}
function fmtTime(ts) {
  const d = new Date(ts * 1000);
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  if (sameDay) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const diff = (now - d) / 86400000;
  if (diff < 7) return d.toLocaleDateString([], { weekday: 'short' });
  return d.toLocaleDateString([], { day: '2-digit', month: '2-digit', year: '2-digit' });
}
function fmtDay(ts) {
  const d = new Date(ts * 1000);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return 'Today';
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (d.toDateString() === y.toDateString()) return 'Yesterday';
  return d.toLocaleDateString([], { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
}
function escapeHTML(s) {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
}
function linkify(text) {
  return escapeHTML(text).replace(/(https?:\/\/[^\s<]+)/g, '<a href="$1" target="_blank" rel="noopener">$1</a>');
}
function initials(name) {
  return name.replace(/[^\p{L}\p{N} ]/gu, '').trim().split(/\s+/).slice(0, 2).map((w) => w[0] || '').join('').toUpperCase() || '#';
}
function hue(s) {
  let h = 0;
  for (const c of s) h = (h * 31 + c.charCodeAt(0)) % 360;
  return h;
}

// ---- rendering ----
function render() {
  renderStatus();
  el.qrScreen.classList.toggle('hidden', !(state.status === 'qr' || state.status === 'loading'));
  el.app.classList.toggle('hidden', state.status === 'qr' || state.status === 'loading');
  if (state.status === 'qr') {
    if (state.qr) {
      el.qrImg.src = state.qr;
      el.qrImg.classList.remove('hidden');
      el.qrHint.textContent = 'Open WhatsApp on your phone → Linked devices → Link a device, then scan this code.';
    } else {
      el.qrHint.textContent = 'Generating QR code…';
    }
  } else if (state.status === 'loading') {
    el.qrImg.classList.add('hidden');
    el.qrHint.textContent = 'Loading WhatsApp engine (WebAssembly)…';
  }
  renderChatList();
  renderConversation();
}
function renderStatus() {
  const map = {
    loading: ['Loading…', 'gray'],
    qr: ['Waiting for QR scan', 'yellow'],
    connecting: ['Connecting…', 'yellow'],
    paired: ['Paired, finishing login…', 'yellow'],
    connected: ['Connected', 'green'],
    disconnected: ['Disconnected' + (state.reason ? ' – ' + state.reason : ''), 'red'],
    logged_out: ['Logged out', 'red'],
  };
  const [text, color] = map[state.status] || [state.status, 'gray'];
  el.statusText.textContent = text;
  el.status.dataset.color = color;
  el.me.textContent = state.pushName || (state.me ? prettyJID(state.me) : '');
  el.reconnect.classList.toggle('hidden', state.status !== 'disconnected');
}
function sortedChats() {
  const q = el.search.value.trim().toLowerCase();
  return [...state.chats.values()]
    .filter((c) => !c.archived || c.unread)
    .filter((c) => !q || c.name.toLowerCase().includes(q) || c.jid.includes(q))
    .sort((a, b) => b.lastTs - a.lastTs);
}
function renderChatList() {
  const chats = sortedChats();
  el.chatList.innerHTML = '';
  if (chats.length === 0) {
    el.chatList.innerHTML = '<div class="chat-empty">No chats yet. Messages will appear here as they arrive, or start a new chat with the + button.</div>';
    return;
  }
  for (const c of chats) {
    const div = document.createElement('div');
    div.className = 'chat' + (c.jid === state.active ? ' active' : '');
    div.dataset.jid = c.jid;
    div.innerHTML = `
      <div class="avatar" style="--h:${hue(c.jid)}">${escapeHTML(initials(c.name))}</div>
      <div class="chat-body">
        <div class="chat-top"><span class="chat-name">${escapeHTML(c.name)}</span><span class="chat-time">${c.lastTs ? fmtTime(c.lastTs) : ''}</span></div>
        <div class="chat-bottom"><span class="chat-last">${escapeHTML(c.presence === 'composing' ? 'typing…' : c.lastText || '')}</span>${c.unread ? `<span class="badge">${c.unread}</span>` : ''}</div>
      </div>`;
    div.onclick = () => openChat(c.jid);
    el.chatList.appendChild(div);
  }
}
function renderConversation() {
  const jid = state.active;
  if (!jid) {
    el.header.classList.add('hidden');
    el.composer.classList.add('hidden');
    el.messages.classList.add('hidden');
    el.empty.classList.remove('hidden');
    return;
  }
  el.empty.classList.add('hidden');
  el.header.classList.remove('hidden');
  el.composer.classList.remove('hidden');
  el.messages.classList.remove('hidden');
  const chat = chatFor(jid);
  el.headerName.textContent = chat.name;
  el.headerSub.textContent = chat.presence === 'composing' ? 'typing…' : chat.isGroup ? 'Group' : prettyJID(jid);
  const list = [...(state.messages.get(jid)?.values() || [])].sort((a, b) => a.ts - b.ts || (a.seq || 0) - (b.seq || 0));
  const atBottom = el.messages.scrollHeight - el.messages.scrollTop - el.messages.clientHeight < 80;
  el.messages.innerHTML = '';
  let lastDay = '';
  for (const m of list) {
    const day = fmtDay(m.ts);
    if (day !== lastDay) {
      const d = document.createElement('div');
      d.className = 'day';
      d.textContent = day;
      el.messages.appendChild(d);
      lastDay = day;
    }
    const div = document.createElement('div');
    div.className = 'msg ' + (m.fromMe ? 'out' : 'in') + (m.kind === 'revoke' ? ' revoked' : '');
    const sender = chat.isGroup && !m.fromMe ? `<div class="sender" style="--h:${hue(m.sender)}">${escapeHTML(m.senderName)}</div>` : '';
    const tick = m.fromMe ? `<span class="tick ${m.status || ''}">${m.status === 'read' ? '✓✓' : m.status === 'delivered' ? '✓✓' : m.status === 'failed' ? '!' : m.status === 'pending' ? '🕓' : '✓'}</span>` : '';
    div.innerHTML = `${sender}<div class="text">${linkify(m.text)}</div><div class="meta">${fmtTime(m.ts)}${tick}</div>`;
    el.messages.appendChild(div);
  }
  if (atBottom || state.scrollToBottom) {
    el.messages.scrollTop = el.messages.scrollHeight;
    state.scrollToBottom = false;
  }
}
function openChat(jid) {
  state.active = jid;
  state.scrollToBottom = true;
  const chat = chatFor(jid);
  if (chat.unread) {
    const unreadCount = chat.unread;
    chat.unread = 0;
    const bySender = new Map();
    for (const m of [...(state.messages.get(jid)?.values() || [])].filter((m) => !m.fromMe && !m.id.startsWith('pending-')).slice(-unreadCount)) {
      if (!bySender.has(m.sender)) bySender.set(m.sender, []);
      bySender.get(m.sender).push(m.id);
    }
    for (const [sender, ids] of bySender) send({ type: 'markRead', jid, sender, ids });
  }
  render();
  el.input.focus();
  uiSave();
}

// ---- events from worker ----
let seq = 0;
worker.onmessage = (e) => {
  const evt = e.data;
  switch (evt.type) {
    case 'ready':
      break;
    case 'status':
      state.status = evt.status;
      state.reason = evt.reason || '';
      if (evt.jid) state.me = evt.jid;
      if (evt.pushName) state.pushName = evt.pushName;
      if (evt.status === 'qr') state.qr = null;
      if (evt.status === 'logged_out') {
        state.chats.clear();
        state.messages.clear();
        state.contacts.clear();
        state.active = null;
        state.me = null;
        state.pushName = '';
        uiClear();
      }
      break;
    case 'qr':
      state.qr = evt.png;
      break;
    case 'qr_timeout':
      state.qr = null;
      break;
    case 'message':
      evt.message.seq = ++seq;
      addMessage(evt.message);
      if (!evt.message.fromMe && document.hidden && Notification.permission === 'granted') {
        new Notification(evt.message.chatName, { body: evt.message.text });
      }
      uiSave();
      break;
    case 'history':
      for (const c of evt.chats || []) {
        const chat = chatFor(c.jid, c.name, c.isGroup);
        if (c.name) chat.name = c.name;
        chat.archived = !!c.archived;
        if (c.unread) chat.unread = c.unread;
        if (c.lastTs && c.lastTs > chat.lastTs) chat.lastTs = c.lastTs;
      }
      for (const m of evt.messages || []) {
        m.seq = ++seq;
        addMessage(m, { countUnread: false });
      }
      uiSave();
      break;
    case 'chats':
      for (const c of evt.chats || []) {
        const chat = chatFor(c.jid, c.name, c.isGroup);
        if (c.name) chat.name = c.name;
      }
      uiSave();
      break;
    case 'chat':
      if (evt.name) {
        state.contacts.set(evt.jid, evt.name);
        const chat = state.chats.get(evt.jid);
        if (chat) chat.name = evt.name;
      }
      break;
    case 'contacts':
      for (const c of evt.contacts || []) {
        state.contacts.set(c.jid, c.name);
        const chat = state.chats.get(c.jid);
        if (chat && !chat.isGroup) chat.name = c.name;
      }
      uiSave();
      break;
    case 'sent': {
      const pendingMsg = state.pending?.get(evt.reqId);
      if (pendingMsg) {
        const list = state.messages.get(pendingMsg.chat);
        if (evt.ok) {
          list.delete(pendingMsg.id);
          addMessage({ ...evt.message, seq: pendingMsg.seq, status: 'sent' });
        } else {
          pendingMsg.status = 'failed';
          pendingMsg.text += '  (failed: ' + evt.error + ')';
        }
        state.pending.delete(evt.reqId);
      }
      uiSave();
      break;
    }
    case 'receipt': {
      const list = state.messages.get(evt.chat);
      if (list) {
        for (const id of evt.ids) {
          const m = list.get(id);
          if (!m || !m.fromMe) continue;
          if (evt.kind === 'read' || evt.kind === 'read-self') m.status = 'read';
          else if (evt.kind === '' || evt.kind === 'delivered') {
            if (m.status !== 'read') m.status = 'delivered';
          }
        }
      }
      break;
    }
    case 'presence': {
      const chat = state.chats.get(evt.chat);
      if (chat) {
        chat.presence = evt.state;
        if (evt.state === 'composing') setTimeout(() => { chat.presence = ''; render(); }, 10000);
      }
      break;
    }
    case 'sync_complete':
      break;
    case 'error':
      console.error('worker error:', evt.error);
      break;
  }
  render();
};

// ---- UI actions ----
state.pending = new Map();
function sendCurrent() {
  const text = el.input.value.trim();
  if (!text || !state.active) return;
  const reqId = 'r' + ++reqCounter;
  const msg = {
    id: 'pending-' + reqId, chat: state.active, chatName: chatFor(state.active).name, isGroup: state.active.endsWith('@g.us'),
    sender: state.me || '', senderName: 'You', fromMe: true, text, kind: 'text', ts: Math.floor(Date.now() / 1000), status: 'pending', seq: ++seq,
  };
  state.pending.set(reqId, msg);
  addMessage(msg);
  send({ type: 'send', reqId, jid: state.active, text });
  el.input.value = '';
  el.input.style.height = '';
  state.scrollToBottom = true;
  render();
}
el.sendBtn.onclick = sendCurrent;
el.input.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendCurrent();
  }
});
let typingTimer = null;
el.input.addEventListener('input', () => {
  el.input.style.height = '';
  el.input.style.height = Math.min(el.input.scrollHeight, 160) + 'px';
  if (!state.active) return;
  if (!typingTimer) send({ type: 'typing', jid: state.active, text: 'composing' });
  clearTimeout(typingTimer);
  typingTimer = setTimeout(() => {
    typingTimer = null;
    send({ type: 'typing', jid: state.active, text: 'paused' });
  }, 3000);
});
el.search.addEventListener('input', renderChatList);
el.logout.onclick = () => {
  if (confirm('Log out and unlink this browser from your phone?')) send({ type: 'logout' });
};
el.reconnect.onclick = () => send({ type: 'reconnect' });
el.newChat.onclick = () => {
  const input = prompt('Phone number with country code (e.g. 14155551234), or a full JID:');
  if (!input) return;
  let jid = input.trim();
  if (!jid.includes('@')) jid = jid.replace(/[^0-9]/g, '') + '@s.whatsapp.net';
  const name = state.contacts.get(jid);
  chatFor(jid, name, jid.endsWith('@g.us'));
  openChat(jid);
};
if ('Notification' in window && Notification.permission === 'default') {
  document.addEventListener('click', () => Notification.requestPermission(), { once: true });
}

uiLoad().then(render);
