import './polyfills.js';
import makeWASocket, { DisconnectReason, makeCacheableSignalKeyStore, Browsers, jidNormalizedUser } from '@whiskeysockets/baileys';
import pino from 'pino';
import QRCode from 'qrcode';
import { useIndexedDBAuthState, clearAuthState } from './auth-idb.js';

const $ = (s) => document.querySelector(s);
const el = {
  qrScreen: $('#qr-screen'), qrCanvas: $('#qr-canvas'), qrHint: $('#qr-hint'), qrError: $('#qr-error'),
  app: $('#app'), chatList: $('#chat-list'), header: $('#chat-header'), messages: $('#messages'),
  composer: $('#composer'), input: $('#input'), send: $('#send'), status: $('#status'), me: $('#me'), logout: $('#logout'),
};

const state = { status: 'loading', me: null, chats: new Map(), messages: new Map(), active: null, names: new Map(), errors: [] };
let sock = null;

const logger = pino({ level: new URLSearchParams(location.search).get('debug') ? 'debug' : 'warn', browser: { asObject: false } });

function fail(msg) {
  console.error(msg);
  if (!state.errors.includes(msg)) state.errors.push(msg);
  render();
}
window.addEventListener('error', (e) => fail('Error: ' + e.message));
window.addEventListener('unhandledrejection', (e) => fail('Unhandled: ' + (e.reason?.message || e.reason)));
document.addEventListener('securitypolicyviolation', (e) => fail('Blocked by CSP: ' + e.violatedDirective + ' → ' + e.blockedURI));

function text(m) {
  const msg = m.message;
  if (!msg) return '';
  const inner = msg.ephemeralMessage?.message || msg.viewOnceMessageV2?.message || msg.viewOnceMessage?.message || msg.documentWithCaptionMessage?.message || msg;
  return inner.conversation || inner.extendedTextMessage?.text || inner.imageMessage?.caption && '📷 ' + inner.imageMessage.caption || (inner.imageMessage && '📷 Photo')
    || (inner.videoMessage && '🎥 Video') || (inner.audioMessage && (inner.audioMessage.ptt ? '🎤 Voice message' : '🎵 Audio')) || (inner.stickerMessage && '🩷 Sticker')
    || (inner.documentMessage && '📄 ' + (inner.documentMessage.fileName || 'Document')) || (inner.locationMessage && '📍 Location') || (inner.contactMessage && '👤 Contact')
    || (inner.protocolMessage && '') || (inner.reactionMessage && '') || '[unsupported message]';
}
function nameFor(jid) {
  return state.names.get(jid) || (jid.endsWith('@g.us') ? 'Group ' + jid.split('@')[0] : '+' + jid.split('@')[0]);
}
function addMessage(m) {
  const chat = jidNormalizedUser(m.key.remoteJid);
  if (!chat || chat === 'status@broadcast') return;
  const t = text(m);
  if (!t) return;
  if (!state.messages.has(chat)) state.messages.set(chat, new Map());
  const ts = Number(m.messageTimestamp) || Math.floor(Date.now() / 1000);
  state.messages.get(chat).set(m.key.id, { id: m.key.id, fromMe: !!m.key.fromMe, text: t, ts, sender: m.key.participant ? jidNormalizedUser(m.key.participant) : chat, pushName: m.pushName });
  const c = state.chats.get(chat) || { jid: chat, lastTs: 0 };
  if (ts >= c.lastTs) { c.lastTs = ts; c.lastText = t; }
  state.chats.set(chat, c);
  if (m.pushName && !m.key.fromMe && !chat.endsWith('@g.us')) state.names.set(chat, m.pushName);
}

async function start() {
  const { state: auth, saveCreds } = await useIndexedDBAuthState();
  sock = makeWASocket({
    auth: { creds: auth.creds, keys: makeCacheableSignalKeyStore(auth.keys, logger) },
    logger,
    browser: Browsers.macOS('Chrome'),
    syncFullHistory: false,
    markOnlineOnConnect: false,
    generateHighQualityLinkPreview: false,
  });
  sock.ev.on('creds.update', saveCreds);
  sock.ev.on('connection.update', async (u) => {
    const { connection, lastDisconnect, qr } = u;
    if (qr) {
      state.status = 'qr';
      await QRCode.toCanvas(el.qrCanvas, qr, { width: 280, margin: 1 });
    }
    if (connection === 'connecting') state.status = state.status === 'qr' ? 'qr' : 'connecting';
    if (connection === 'open') {
      state.status = 'connected';
      state.me = jidNormalizedUser(sock.user?.id);
      state.names.set(state.me, sock.user?.name || 'You');
    }
    if (connection === 'close') {
      const code = lastDisconnect?.error?.output?.statusCode;
      const loggedOut = code === DisconnectReason.loggedOut;
      state.status = loggedOut ? 'logged_out' : 'disconnected';
      state.reason = lastDisconnect?.error?.message || String(code);
      if (loggedOut) { await clearAuthState(); state.me = null; state.chats.clear(); state.messages.clear(); }
      render();
      setTimeout(start, loggedOut ? 500 : 2000);
      return;
    }
    render();
  });
  sock.ev.on('messaging-history.set', ({ chats, contacts, messages }) => {
    for (const c of contacts || []) if (c.name || c.notify) state.names.set(jidNormalizedUser(c.id), c.name || c.notify);
    for (const c of chats || []) if (c.name) state.names.set(jidNormalizedUser(c.id), c.name);
    for (const m of messages || []) addMessage(m);
    render();
  });
  sock.ev.on('contacts.upsert', (cs) => { for (const c of cs) if (c.name || c.notify) state.names.set(jidNormalizedUser(c.id), c.name || c.notify); render(); });
  sock.ev.on('contacts.update', (cs) => { for (const c of cs) if (c.name || c.notify) state.names.set(jidNormalizedUser(c.id), c.name || c.notify); render(); });
  sock.ev.on('chats.upsert', (cs) => { for (const c of cs) if (c.name) state.names.set(jidNormalizedUser(c.id), c.name); render(); });
  sock.ev.on('groups.upsert', (gs) => { for (const g of gs) state.names.set(g.id, g.subject); render(); });
  sock.ev.on('messages.upsert', ({ messages }) => { for (const m of messages) addMessage(m); render(); });
}

function render() {
  const onboarding = !state.me;
  el.qrScreen.classList.toggle('hidden', !onboarding);
  el.app.classList.toggle('hidden', onboarding);
  el.qrError.classList.toggle('hidden', !state.errors.length);
  el.qrError.textContent = state.errors.join('\n');
  el.qrCanvas.classList.toggle('hidden', state.status !== 'qr');
  el.qrHint.textContent = { loading: 'Loading Baileys…', connecting: 'Connecting to WhatsApp…', qr: 'Scan with WhatsApp → Linked devices → Link a device', disconnected: 'Disconnected: ' + state.reason + ' (retrying)', logged_out: 'Logged out, reconnecting…' }[state.status] || state.status;
  el.status.textContent = state.status;
  el.me.textContent = state.me ? nameFor(state.me) : '';
  const chats = [...state.chats.values()].sort((a, b) => b.lastTs - a.lastTs);
  el.chatList.innerHTML = chats.map((c) => `<div class="chat ${c.jid === state.active ? 'active' : ''}" data-jid="${c.jid}"><b>${esc(nameFor(c.jid))}</b><span>${esc(c.lastText || '')}</span></div>`).join('') || '<div class="muted">No chats yet. Messages appear here as they arrive.</div>';
  for (const d of el.chatList.querySelectorAll('.chat')) d.onclick = () => { state.active = d.dataset.jid; render(); el.messages.scrollTop = el.messages.scrollHeight; };
  const active = state.active;
  el.header.textContent = active ? nameFor(active) : 'Baileys in the browser';
  el.composer.classList.toggle('hidden', !active);
  const list = active ? [...state.messages.get(active)?.values() || []].sort((a, b) => a.ts - b.ts) : [];
  const atBottom = el.messages.scrollHeight - el.messages.scrollTop - el.messages.clientHeight < 80;
  el.messages.innerHTML = list.map((m) => `<div class="msg ${m.fromMe ? 'out' : 'in'}">${active.endsWith('@g.us') && !m.fromMe ? `<div class="sender">${esc(m.pushName || nameFor(m.sender))}</div>` : ''}${esc(m.text)}<div class="time">${new Date(m.ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</div></div>`).join('');
  if (atBottom) el.messages.scrollTop = el.messages.scrollHeight;
}
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

async function sendCurrent() {
  const t = el.input.value.trim();
  if (!t || !state.active || !sock) return;
  el.input.value = '';
  try {
    const sent = await sock.sendMessage(state.active, { text: t });
    if (sent) { addMessage(sent); render(); }
  } catch (e) { fail('Send failed: ' + e.message); }
}
el.send.onclick = sendCurrent;
el.input.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendCurrent(); } });
el.logout.onclick = async () => { if (confirm('Log out and unlink?')) { try { await sock.logout(); } catch {} await clearAuthState(); location.reload(); } };

render();
start().catch((e) => fail('Start failed: ' + (e.stack || e.message || e)));
