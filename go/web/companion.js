const storageKey = 'erdai.realtime.device.v1';
const pairing = document.querySelector('#pairing');
const conversation = document.querySelector('#conversation');
const messages = document.querySelector('#messages');
const stateLabel = document.querySelector('#state');
const personaLabel = document.querySelector('#persona');
const messageInput = document.querySelector('#message');
const voiceButton = document.querySelector('#voice');
let device = JSON.parse(localStorage.getItem(storageKey) || 'null');
let socket;
let clientSequence = 0;
let reconnectTimer;
let recognition;

function setState(value) {
  const labels = { idle: '在这里', thinking: '正在想', speaking: '正在说', tool_running: '正在处理', error: '出了点问题', connecting: '正在连接', offline: '连接已断开' };
  stateLabel.textContent = labels[value] || value;
}

function addMessage(text, kind) {
  if (!String(text || '').trim()) return;
  const node = document.createElement('div');
  node.className = `message ${kind}`;
  node.textContent = text;
  messages.append(node);
  messages.scrollTop = messages.scrollHeight;
}

function envelope(type, payload = {}) {
  clientSequence += 1;
  return { version: 1, eventId: crypto.randomUUID(), sessionId: device.sessionId || '', sequence: clientSequence, timestamp: new Date().toISOString(), type, payload };
}

function connect() {
  if (!device?.token) return;
  clearTimeout(reconnectTimer);
  setState('connecting');
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const params = device.sessionId ? `?sessionId=${encodeURIComponent(device.sessionId)}` : '';
  const encoded = btoa(unescape(encodeURIComponent(device.token))).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
  socket = new WebSocket(`${scheme}//${location.host}/api/v2/realtime${params}`, ['erdai.realtime.v1', `erdai.token.${encoded}`]);
  socket.addEventListener('message', handleEvent);
  socket.addEventListener('close', () => {
    setState('offline');
    reconnectTimer = setTimeout(connect, 1800);
  });
  socket.addEventListener('error', () => socket.close());
}

function handleEvent(event) {
  const packet = JSON.parse(event.data);
  const payload = packet.payload || {};
  if (packet.type === 'session.ready') {
    device.sessionId = payload.sessionId;
    clientSequence = Number(payload.lastClientSequence || 0);
    localStorage.setItem(storageKey, JSON.stringify(device));
    pairing.hidden = true;
    conversation.hidden = false;
    if (payload.personaId) personaLabel.textContent = payload.personaId;
    setState(payload.state || 'idle');
  }
  if (packet.type === 'agent.state') setState(payload.state || 'idle');
  if (packet.type === 'response.commit') {
    addMessage(payload.text, 'agent');
    socket.send(JSON.stringify(envelope('delivery.ack', { deliveryId: payload.deliveryId })));
    if (payload.text && 'speechSynthesis' in window) {
      const utterance = new SpeechSynthesisUtterance(payload.text);
      utterance.lang = 'zh-CN';
      speechSynthesis.cancel();
      speechSynthesis.speak(utterance);
    }
  }
  if (packet.type === 'error') addMessage(payload.message || '这次没接住。', 'system');
}

document.querySelector('#pair-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const error = document.querySelector('#pair-error');
  error.hidden = true;
  const response = await fetch('/api/v2/realtime/pair', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ code: document.querySelector('#pair-code').value.trim(), name: document.querySelector('#device-name').value.trim() }) });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    error.textContent = '配对码无效或已过期。';
    error.hidden = false;
    return;
  }
  device = { token: payload.data.token, id: payload.data.device.id, name: payload.data.device.name, sessionId: '' };
  localStorage.setItem(storageKey, JSON.stringify(device));
  connect();
});

document.querySelector('#composer').addEventListener('submit', (event) => {
  event.preventDefault();
  const text = messageInput.value.trim();
  if (!text || socket?.readyState !== WebSocket.OPEN) return;
  addMessage(text, 'user');
  socket.send(JSON.stringify(envelope('input.text', { text })));
  messageInput.value = '';
});

messageInput.addEventListener('input', () => {
  messageInput.style.height = 'auto';
  messageInput.style.height = `${Math.min(messageInput.scrollHeight, 150)}px`;
});

voiceButton.addEventListener('click', () => {
  const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!Recognition) {
    addMessage('这个浏览器不支持语音转写。', 'system');
    return;
  }
  recognition?.abort();
  recognition = new Recognition();
  recognition.lang = 'zh-CN';
  recognition.interimResults = true;
  recognition.onstart = () => voiceButton.classList.add('listening');
  recognition.onend = () => voiceButton.classList.remove('listening');
  recognition.onresult = (event) => {
    messageInput.value = Array.from(event.results).map((result) => result[0].transcript).join('');
  };
  recognition.start();
});

document.querySelector('#settings').addEventListener('click', () => {
  if (!device || !confirm('解除这台桌面的本地配对？')) return;
  socket?.close();
  clearTimeout(reconnectTimer);
  localStorage.removeItem(storageKey);
  location.reload();
});

if (device?.token) connect();
