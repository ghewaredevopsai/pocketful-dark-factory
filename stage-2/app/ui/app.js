// Pocketful browser client. Renders every screen from the JSON API.
//
// Rules this file keeps:
// - Money is integer minor units end to end. Parsing and formatting use strings and
//   BigInt, never floating point.
// - Every user-supplied string is inserted with textContent (no innerHTML of data).
// - Each idempotent form keeps an identity {sig, key}: the exact JSON body sent and its
//   Idempotency-Key. An unchanged form re-sends the same key and body (a replay); any
//   change mints a new key. An unknown outcome (network loss, timeout, 5xx) keeps the
//   identity so a retry recovers the original result.
// - Reads are numbered; a response is applied only if it is newer than the last one
//   applied ("latest refresh wins").
'use strict';

const TOKEN_KEY = 'pocketful.token';
const NOTICE_KEY = 'pocketful.notice';
// A request with no answer after this long is treated as an unknown outcome ("uncertain").
const CLIENT_TIMEOUT_MS = 8000;
const PAGE = 50; // list items shown per "Show more" step
let token = localStorage.getItem(TOKEN_KEY);
let me = null; // GET /me body

// ---------------------------------------------------------------- DOM helpers

function h(tag, props, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'text') e.textContent = v;
    else if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? '' : String(v));
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid === null || kid === undefined || kid === false) continue;
    e.append(kid instanceof Node ? kid : String(kid));
  }
  return e;
}
const $ = (sel, root = document) => root.querySelector(sel);
const tid = (name) => `[data-testid="${CSS.escape(name)}"]`;

const ICON = {
  out: '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 14 14 6M7 6h7v7"/></svg>',
  in: '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 6 6 14M13 14H6V7"/></svg>',
  other: '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h11l-3-3M16 13H5l3 3"/></svg>',
  hold: '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="9" width="12" height="8" rx="2"/><path d="M7 9V6.5a3 3 0 0 1 6 0V9"/></svg>',
  lock: '<svg viewBox="0 0 20 20" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><rect x="4" y="9" width="12" height="8" rx="2"/><path d="M7 9V6.5a3 3 0 0 1 6 0V9"/></svg>',
  wallet: '<svg viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 7h14v11a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2z"/><path d="M8 7V5h8v2"/><circle cx="12" cy="13" r="1.6" fill="#fff"/></svg>',
  inbox: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 13h4l2 3h4l2-3h4"/><path d="M5 5h14l1 8v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1v-5z"/></svg>',
};
function icon(name) { const s = h('span', { 'aria-hidden': 'true', class: 'ico' }); s.innerHTML = ICON[name]; return s.firstChild; }

// ---------------------------------------------------------------- money

function fmt(minor) {
  const mu = me ? me.minor_units : 2;
  const cur = me ? me.currency : '';
  let s = BigInt(minor).toString();
  if (mu > 0) {
    s = s.padStart(mu + 1, '0');
    s = s.slice(0, -mu) + '.' + s.slice(-mu);
  }
  return cur ? `${s} ${cur}` : s;
}
// minor units -> the decimal a person would type ("15.00"), used to pre-fill inputs
function toDecimal(minor) { return fmt(minor).split(' ')[0]; }

// "15", "15.5", "15.50", ".5" -> minor units. Never rounds: more places than the
// currency allows is an error, as are signs, exponents, separators and zero.
function parseAmount(text) {
  const mu = me ? me.minor_units : 2;
  const t = String(text).trim();
  if (t === '') return { error: 'Enter an amount.' };
  const m = /^(\d*)(?:\.(\d*))?$/.exec(t);
  if (!m || (m[1] === '' && (m[2] === undefined || m[2] === ''))) {
    return { error: 'Enter the amount as a number, like ' + (mu ? '15.00' : '1500') + '.' };
  }
  const frac = m[2] === undefined ? '' : m[2];
  if (frac.length > mu) {
    return { error: mu === 0
      ? `${me.currency} has no smaller units — enter a whole amount.`
      : `Use at most ${mu} decimal places.` };
  }
  const minor = BigInt(m[1] || '0') * 10n ** BigInt(mu) + BigInt((frac + '0'.repeat(mu)).slice(0, mu) || '0');
  if (minor < 1n) return { error: 'The amount must be more than zero.' };
  if (minor > 1000000000n) return { error: `The most you can send at once is ${fmt(1000000000)}.` };
  return { minor: Number(minor) };
}

// §9 equal split: whole units, first participants get the remainder.
function equalSplit(amount, n) {
  const a = BigInt(amount), k = BigInt(n);
  const base = a / k, rem = a % k;
  return Array.from({ length: n }, (_, i) => Number(base + (BigInt(i) < rem ? 1n : 0n)));
}

// ---------------------------------------------------------------- time

function when(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  const diff = (Date.now() - d.getTime()) / 1000;
  const abs = d.toLocaleString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });
  let rel;
  if (Math.abs(diff) < 45) rel = 'just now';
  else if (diff > 0 && diff < 3600) rel = `${Math.round(diff / 60)} min ago`;
  else if (diff > 0 && diff < 86400) rel = `${Math.round(diff / 3600)} h ago`;
  else if (diff < 0 && -diff < 3600) rel = `in ${Math.max(1, Math.round(-diff / 60))} min`;
  else if (diff < 0 && -diff < 86400) rel = `in ${Math.round(-diff / 3600)} h`;
  else rel = abs;
  return h('time', { datetime: iso, title: abs }, rel === abs ? abs : `${rel} · ${abs}`);
}

// ---------------------------------------------------------------- API

function newKey() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

// Returns {status, ok, data, code} for any HTTP answer below 500, or {uncertain: true}
// when the outcome is unknown (network error, timeout, 5xx).
async function api(method, path, opts = {}) {
  const headers = { Accept: 'application/json' };
  if (token) headers.Authorization = 'Bearer ' + token;
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  if (opts.key) headers['Idempotency-Key'] = opts.key;
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), CLIENT_TIMEOUT_MS);
  try {
    const res = await fetch(path, { method, headers, body: opts.body, signal: ctl.signal, cache: 'no-store', credentials: 'same-origin' });
    let data = null;
    try { data = await res.json(); } catch (_) { /* empty body */ }
    if (res.status >= 500) return { uncertain: true, status: res.status };
    const code = data && data.error ? data.error.code : null;
    if (res.status === 401 && token && opts.auth !== false) { signOut('Your session has ended. Please log in again.'); }
    return { status: res.status, ok: res.status < 300, data, code };
  } catch (_) {
    return { uncertain: true };
  } finally {
    clearTimeout(timer);
  }
}

// An idempotent form's identity: same body → same key.
function identity() {
  let cur = { sig: null, key: null };
  return {
    keyFor(sig) { if (sig !== cur.sig) cur = { sig, key: newKey() }; return cur.key; },
    // a definite refusal (4xx): the next submit starts a new identity
    reset() { cur = { sig: null, key: null }; },
  };
}

function signOut(notice) {
  localStorage.removeItem(TOKEN_KEY);
  token = null;
  if (typeof notice === 'string') sessionStorage.setItem(NOTICE_KEY, notice);
  location.assign('/login');
}

// A stage-1 service (before an upgrade) has no holds: total = balance, nothing held.
function normalizeMe(m) {
  const total = m.total !== undefined ? m.total : m.balance;
  return Object.assign({}, m, { total, held: m.held || 0, available: m.available !== undefined ? m.available : total });
}

// Reads up to `want` items of a paged list (limit 200 per call), newest first.
async function fetchList(path, field, want) {
  const items = [];
  let more = true;
  while (items.length < want && more) {
    const sep = path.includes('?') ? '&' : '?';
    const r = await api('GET', `${path}${sep}limit=${Math.min(200, want - items.length)}&offset=${items.length}`);
    if (r.uncertain || !r.ok) return r;
    items.push(...r.data[field]);
    more = r.data.has_more;
  }
  return { ok: true, status: 200, data: { [field]: items, has_more: more } };
}

// "Couldn't refresh" note: keeps the last numbers on screen and offers a retry.
function refreshNote(slot, res, retry) {
  if (res === 'failed') {
    slot.replaceChildren(h('div', { class: 'msg msg-uncertain', role: 'status', 'data-testid': 'refresh-error' },
      h('span', { class: 'icon', 'aria-hidden': 'true' }, '!'),
      h('div', {}, h('strong', {}, 'Couldn’t refresh.'), ' Showing the last numbers we had. ',
        h('button', { type: 'button', class: 'linklike', onclick: retry }, 'Try again'))));
  } else if (res === 'ok') {
    slot.replaceChildren();
  }
}

function showMore(testid, onclick) {
  return h('div', { class: 'more' }, h('button', { type: 'button', class: 'btn btn-secondary', 'data-testid': testid, onclick }, 'Show more'));
}

const UNCERTAIN = {
  pay: 'We couldn’t confirm whether this payment went through. Retrying is safe — it will only be applied once.',
};

// ---------------------------------------------------------------- messages

const MESSAGES = {
  insufficient_funds: 'Not enough available funds. Money on hold can’t be spent.',
  self_payment: 'You can’t send money to yourself.',
  self_request: 'You can’t request money from yourself.',
  not_found: 'We couldn’t find anyone with that handle.',
  request_not_pending: 'This request is no longer pending — it was already paid, declined or cancelled.',
  forbidden: 'You’re not allowed to do that.',
  validation_failed: 'Please check the details and try again.',
  idempotency_key_reuse: 'That retry didn’t match the original. Please submit it again.',
  authorization_not_open: 'This hold is no longer open.',
  authorization_expired: 'This hold has expired, so it can’t be collected.',
  capture_exceeds_authorization: 'That’s more than the amount still on hold.',
  email_taken: 'An account with this email already exists. Try logging in.',
  handle_taken: 'The handle made from this email is already taken. Try a different email address.',
  unauthenticated: 'That email and password don’t match.',
  missing_idempotency_key: 'Something went wrong sending this. Please try again.',
  malformed_request: 'Something went wrong sending this. Please try again.',
};
function messageFor(r, overrides = {}) {
  return overrides[r.code] || MESSAGES[r.code] || 'Something went wrong. Please try again.';
}

// Renders exactly one status message in `slot`, with testid `${prefix}-${kind}`.
// kind: 'error' (refused, role=alert), 'uncertain' (unknown outcome), 'success' (role=status).
function say(slot, prefix, kind, title, detail, code) {
  slot.replaceChildren();
  if (!kind) return;
  const glyph = { error: '!', uncertain: '?', success: '✓' }[kind];
  // the API error code is kept for support/debugging in data-code, not in the visible text
  slot.append(h('div', { class: `msg msg-${kind}`, 'data-testid': `${prefix}-${kind}`, role: kind === 'success' ? 'status' : 'alert', 'data-code': code || null },
    h('span', { class: 'icon', 'aria-hidden': 'true' }, glyph),
    h('div', {}, h('strong', {}, title), detail ? h('span', {}, detail) : null)));
}

function busy(btn, on, label) {
  if (on) {
    btn.dataset.label = btn.textContent;
    btn.setAttribute('aria-busy', 'true');
    btn.disabled = true;
    btn.replaceChildren(h('span', { class: 'spinner', 'aria-hidden': 'true' }), label);
  } else {
    btn.removeAttribute('aria-busy');
    btn.disabled = false;
    btn.textContent = btn.dataset.label || btn.textContent;
    // a disabled button loses focus; give it back so keyboard users stay in place
    if (!document.activeElement || document.activeElement === document.body) btn.focus({ preventScroll: true });
  }
}

// ---------------------------------------------------------------- shared pieces

function field(id, label, input, hint, control) {
  input.id = id;
  input.setAttribute('data-testid', id);
  if (hint) input.setAttribute('aria-describedby', id + '-hint');
  return h('div', { class: 'field' },
    h('label', { for: id }, label),
    control || input,
    hint ? h('span', { class: 'hint', id: id + '-hint' }, hint) : null);
}
function amountInput(id, label, hint) {
  const input = h('input', { type: 'text', inputmode: 'decimal', autocomplete: 'off', placeholder: me && me.minor_units ? '0.' + '0'.repeat(me.minor_units) : '0' });
  const wrap = h('div', { class: 'affix' }, input, h('span', { class: 'suffix', 'aria-hidden': 'true' }, me ? me.currency : ''));
  return field(id, label, input, hint, wrap);
}
function handleInput(id, label, hint) {
  const input = h('input', { type: 'text', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false', placeholder: 'handle' });
  const wrap = h('div', { class: 'affix has-prefix' }, h('span', { class: 'prefix', 'aria-hidden': 'true' }, '@'), input);
  return field(id, label, input, hint, wrap);
}
function visibilitySelect(id, label) {
  const sel = h('select', {},
    h('option', { value: 'public' }, 'Public'),
    h('option', { value: 'private' }, 'Private'));
  return field(id, label, sel, 'Private: only the two of you see it.');
}
// a compact control with a small visible label, for actions inside list items
function miniField(id, label, control) {
  return h('div', { class: 'mini-field' }, h('label', { for: id, class: 'mini-label' }, label), control);
}
function cleanHandle(v) { return String(v).trim().replace(/^@/, ''); }

function emptyState(testid, title, text, action) {
  const art = h('div', { class: 'empty-art', 'aria-hidden': 'true' });
  art.innerHTML = ICON.inbox;
  return h('div', { class: 'empty', 'data-testid': testid }, art, h('h3', {}, title), h('p', { class: 'small' }, text), action || null);
}

function statusBadge(status) {
  const label = { pending: 'Pending', paid: 'Paid', declined: 'Declined', cancelled: 'Cancelled', open: 'On hold', captured: 'Collected', voided: 'Released', expired: 'Expired' }[status] || status;
  return h('span', { class: `badge st-${status}` }, label);
}
function privacyBadge(vis) {
  return vis === 'private'
    ? h('span', { class: 'badge badge-private' }, icon('lock'), 'Private')
    : h('span', { class: 'badge badge-public' }, 'Public');
}

// Wallet numbers. On "/" they are the hero card; elsewhere a compact summary.
function walletNumbers(big) {
  const avail = h('span', { 'data-testid': 'wallet-available', 'data-amount': me.available, class: 'money' }, fmt(me.available));
  const total = h('span', { 'data-testid': 'wallet-balance', 'data-amount': me.total, class: 'money' }, fmt(me.total));
  const held = me.held > 0 ? h('span', { 'data-testid': 'wallet-held', 'data-amount': me.held, class: 'money' }, fmt(me.held)) : null;
  if (big) {
    return h('div', {},
      h('p', { class: 'label', id: 'available-label' }, 'Available to spend'),
      h('p', { class: 'balance-hero', 'aria-describedby': 'available-label' }, avail),
      h('dl', { class: 'balance-meta' },
        h('div', {}, h('dt', {}, 'Total balance'), h('dd', {}, total)),
        held ? h('div', {}, h('dt', {}, 'On hold'), h('dd', {}, h('span', { class: 'held-chip' }, icon('lock'), held))) : null));
  }
  return h('div', { class: 'summary', 'aria-label': 'Your balance' },
    h('span', { class: 'part' }, 'Available'), h('span', { class: 'headline' }, avail),
    h('span', { class: 'part' }, 'Total ', total),
    held ? h('span', { class: 'part held' }, 'On hold ', held) : null);
}

// ---------------------------------------------------------------- refresh (latest wins)

let readSeq = 0, appliedSeq = 0;
// Fetches /me plus the given extra reads; applies them only if no newer read was applied.
async function refresh(extra, apply) {
  const seq = ++readSeq;
  const [m, ...rest] = await Promise.all([api('GET', '/me'), ...extra.map((p) => (typeof p === 'function' ? p() : api('GET', p)))]);
  if (seq <= appliedSeq) return 'stale';
  if (m.uncertain || !m.ok || rest.some((r) => r.uncertain || !r.ok)) return 'failed';
  appliedSeq = seq;
  me = normalizeMe(m.data);
  apply(rest.map((r) => r.data));
  return 'ok';
}

// ---------------------------------------------------------------- header

const NAV = [['/', 'Wallet'], ['/requests', 'Requests'], ['/split', 'Split'], ['/authorizations', 'Holds']];

function renderHeader() {
  const header = $('#site-header');
  const mark = h('span', { class: 'brand-mark', 'aria-hidden': 'true' });
  mark.innerHTML = ICON.wallet;
  const bar = h('div', { class: 'bar' }, h('a', { class: 'brand', href: '/' }, mark, 'Pocketful'));
  if (me) {
    bar.append(h('div', { class: 'who' },
      h('span', { class: 'avatar', 'aria-hidden': 'true' }, (me.display_name || me.handle || '?').trim().charAt(0).toUpperCase() || '?'),
      h('span', { class: 'who-text' },
        h('span', { class: 'who-name', 'data-testid': 'current-user', title: me.display_name }, me.display_name || me.handle),
        h('span', { class: 'who-handle', 'data-testid': 'current-handle' }, me.handle)),
      h('button', { class: 'btn btn-quiet', type: 'button', 'data-testid': 'logout-button', onclick: signOut }, 'Log out')));
  } else if (!token) {
    bar.append(h('div', { class: 'who' },
      h('a', { class: 'btn btn-quiet', href: '/login' }, 'Log in'),
      h('a', { class: 'btn btn-primary', href: '/signup' }, 'Create account')));
  }
  const nodes = [bar];
  if (me) {
    nodes.push(h('nav', { class: 'nav', 'aria-label': 'Main' },
      NAV.map(([href, label]) => h('a', { href, 'aria-current': location.pathname === href ? 'page' : null }, label))));
  }
  header.replaceChildren(...nodes);
}

// ---------------------------------------------------------------- wallet "/"

function feedItem(p) {
  const out = p.from_user_id === me.user_id, inc = p.to_user_id === me.user_id;
  const kind = out ? 'out' : inc ? 'in' : 'other';
  const word = out ? 'Sent' : inc ? 'Received' : 'Between others';
  const parties = h('p', { class: 'parties', 'data-testid': `activity-parties-${p.payment_id}` },
    out ? `You (${p.from_handle}) → ${p.to_handle}` : inc ? `${p.from_handle} → you (${p.to_handle})` : `${p.from_handle} → ${p.to_handle}`);
  const dir = h('span', { class: `dir dir-${kind}`, 'aria-hidden': 'true' }, icon(kind));
  return h('li', { class: 'item', 'data-testid': `activity-item-${p.payment_id}`, 'data-visibility': p.visibility },
    dir,
    h('div', { class: 'item-main' },
      parties,
      h('p', { class: 'note', 'data-testid': `activity-note-${p.payment_id}` }, p.note),
      h('div', { class: 'meta' }, when(p.created_at), privacyBadge(p.visibility),
        p.request_id ? h('span', { class: 'badge badge-public' }, 'Request') : null,
        p.authorization_id ? h('span', { class: 'badge badge-public' }, 'From a hold') : null,
        p.settlement_id ? h('span', { class: 'badge badge-public' }, 'Settlement') : null)),
    h('div', { class: `item-amount ${out ? 'amt-out' : inc ? 'amt-in' : ''}` },
      h('span', { class: 'dirword' }, word),
      h('span', { class: 'money', 'data-testid': `activity-amount-${p.payment_id}` }, fmt(p.amount))));
}

function renderFeed(slot, payments) {
  if (!payments.length) {
    slot.replaceChildren(emptyState('empty-activity', 'No activity yet',
      'Payments you send or receive, and public payments between others, will show up here.'));
    return;
  }
  slot.replaceChildren(h('ul', { class: 'list', 'data-testid': 'activity-list', 'aria-label': 'Activity, newest first' },
    payments.map(feedItem)));
}

function payForm(onDone) {
  const ident = identity();
  const status = h('div', { 'aria-live': 'polite' });
  const submit = h('button', { class: 'btn btn-primary btn-block', type: 'submit', 'data-testid': 'pay-submit' }, 'Send money');
  const form = h('form', { novalidate: true, 'aria-labelledby': 'pay-title' },
    handleInput('pay-handle', 'To'),
    h('div', { class: 'row two' }, amountInput('pay-amount', 'Amount'), visibilitySelect('pay-visibility', 'Who can see it')),
    field('pay-note', 'Note (optional)', h('input', { type: 'text', maxlength: '200', placeholder: 'What’s it for?' })),
    submit, status);
  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    if (submit.disabled) return;
    const handle = cleanHandle($('#pay-handle', form).value);
    const amt = parseAmount($('#pay-amount', form).value);
    if (!handle) return say(status, 'pay', 'error', 'Who are you paying?', ' Enter their handle.');
    if (amt.error) { $('#pay-amount', form).setAttribute('aria-invalid', 'true'); return say(status, 'pay', 'error', 'Check the amount.', ' ' + amt.error); }
    $('#pay-amount', form).removeAttribute('aria-invalid');
    const body = JSON.stringify({ to_handle: handle, amount: amt.minor, note: $('#pay-note', form).value, visibility: $('#pay-visibility', form).value });
    const key = ident.keyFor(body);
    busy(submit, true, 'Sending…');
    const r = await api('POST', '/payments', { body, key });
    busy(submit, false);
    if (r.uncertain) {
      // unknown outcome: keep the identity; pressing "Send money" again with the unchanged
      // form re-sends the same key and body. If a field is changed, this note stays until
      // the next submit, which then starts a new payment.
      say(status, 'pay', 'uncertain', 'Not confirmed yet.', ' ' + UNCERTAIN.pay);
      return;
    }
    if (r.ok) say(status, 'pay', 'success', `Sent ${fmt(r.data.amount)} to ${r.data.to_handle}.`, r.status === 200 ? ' Confirmed — this payment was made once; nothing more was taken.' : null);
    else { ident.reset(); say(status, 'pay', 'error', 'Payment not sent.', ' ' + messageFor(r), r.code); }
    onDone();
  });
  return form;
}

function requestForm(onDone) {
  const ident = identity();
  const status = h('div', { 'aria-live': 'polite' });
  const submit = h('button', { class: 'btn btn-secondary btn-block', type: 'submit', 'data-testid': 'request-submit' }, 'Request money');
  const form = h('form', { novalidate: true },
    h('div', { class: 'row two' }, handleInput('request-handle', 'From'), amountInput('request-amount', 'Amount')),
    field('request-note', 'Note (optional)', h('input', { type: 'text', maxlength: '200', placeholder: 'e.g. Taxi on Friday' })),
    submit, status);
  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    if (submit.disabled) return;
    const handle = cleanHandle($('#request-handle', form).value);
    const amt = parseAmount($('#request-amount', form).value);
    if (!handle) return say(status, 'request', 'error', 'Who should pay you?', ' Enter their handle.');
    if (amt.error) return say(status, 'request', 'error', 'Check the amount.', ' ' + amt.error);
    const body = JSON.stringify({ payer_handle: handle, amount: amt.minor, note: $('#request-note', form).value });
    busy(submit, true, 'Sending…');
    const r = await api('POST', '/requests', { body, key: ident.keyFor(body) });
    busy(submit, false);
    if (r.uncertain) return say(status, 'request', 'uncertain', 'Not confirmed yet.', ' We couldn’t confirm whether this request was sent. Retrying is safe — it will only be sent once.');
    if (r.ok) say(status, 'request', 'success', `Asked ${r.data.payer_handle} for ${fmt(r.data.amount)}.`, null);
    else { ident.reset(); say(status, 'request', 'error', 'Request not sent.', ' ' + messageFor(r, { not_found: 'We couldn’t find anyone with that handle.' }), r.code); }
    onDone();
  });
  return form;
}

async function walletPage(main) {
  const numbers = h('div', {});
  const stamp = h('span', { class: 'stamp', 'aria-live': 'polite' });
  const refreshBtn = h('button', { class: 'btn btn-on-dark', type: 'button', 'data-testid': 'wallet-refresh' }, 'Refresh');
  const feed = h('div', { 'aria-live': 'polite' }, h('div', { class: 'loading-block' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Loading activity…'));
  const feedStatus = h('div', {});
  let shown = PAGE;

  const apply = ([activity]) => {
    numbers.replaceChildren(walletNumbers(true));
    renderFeed(feed, activity.payments);
    if (activity.has_more) feed.append(showMore('activity-more', () => { shown += PAGE; reload(); }));
    stamp.textContent = 'Updated ' + new Date().toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  };
  const reload = async () => {
    const res = await refresh([() => fetchList('/activity', 'payments', shown)], apply);
    refreshNote(feedStatus, res, reload);
  };
  // The refresh button stays enabled while a read is in flight: a second press issues a
  // newer read, and refresh() makes sure an older answer arriving later is ignored.
  let inflight = 0;
  refreshBtn.addEventListener('click', async () => {
    inflight++;
    refreshBtn.setAttribute('aria-busy', 'true');
    refreshBtn.replaceChildren(h('span', { class: 'spinner', 'aria-hidden': 'true' }), 'Refreshing…');
    try { await reload(); } finally {
      if (--inflight === 0) { refreshBtn.removeAttribute('aria-busy'); refreshBtn.textContent = 'Refresh'; }
    }
  });

  numbers.append(walletNumbers(true));
  main.replaceChildren(
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, `Hi, ${me.display_name || me.handle}`), h('p', {}, 'Send, request and keep track of your money.'))),
    h('div', { class: 'grid wallet' },
      h('div', { class: 'stack' },
        h('section', { class: 'card balance-card', 'aria-label': 'Balance' }, numbers, h('div', { class: 'balance-actions' }, refreshBtn, stamp), feedStatus),
        h('section', { class: 'card', 'aria-labelledby': 'pay-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'pay-title' }, 'Send money')), payForm(reload)),
        h('section', { class: 'card', 'aria-labelledby': 'req-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'req-title' }, 'Request money'), h('a', { href: '/requests', class: 'small' }, 'View requests')), requestForm(reload)),
        h('section', { class: 'card', 'aria-labelledby': 'hold-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'hold-title' }, 'Reserve money'), h('a', { href: '/authorizations', class: 'small' }, 'View holds')),
          h('p', { class: 'small muted card-lede' }, 'Put money on hold for someone to collect later. It stays in your balance but can’t be spent until it’s collected, released or expires.'),
          authorizeForm(reload))),
      h('section', { class: 'card', 'aria-labelledby': 'act-title' },
        h('div', { class: 'card-head' }, h('h2', { id: 'act-title' }, 'Activity'), h('p', {}, 'Newest first')), feed)));
  await reload();
}

// ---------------------------------------------------------------- requests

async function requestsPage(main) {
  const summary = h('div', {});
  const alertSlot = h('div', { 'aria-live': 'polite', class: 'page-alert' });
  const incoming = h('ul', { class: 'list', 'data-testid': 'incoming-list', 'aria-label': 'Requests to you' });
  const outgoing = h('ul', { class: 'list', 'data-testid': 'outgoing-list', 'aria-label': 'Requests you sent' });
  const emptySlot = h('div', {});
  const listsGrid = h('div', { class: 'grid two' });
  const refreshSlot = h('div', { class: 'page-alert' });
  const moreSlot = h('div', {});
  const payIdent = {};
  const visChosen = {};
  let latest = {};
  let shown = PAGE;

  const item = (r, isIncoming) => {
    const pending = r.status === 'pending';
    const actions = [];
    if (pending && isIncoming) {
      const vis = h('select', { id: `vis-${r.request_id}`, 'data-testid': `request-visibility-${r.request_id}` },
        h('option', { value: 'public' }, 'Public'), h('option', { value: 'private' }, 'Private'));
      vis.value = visChosen[r.request_id] || 'public';
      vis.addEventListener('change', () => { visChosen[r.request_id] = vis.value; });
      const pay = h('button', { class: 'btn btn-primary', type: 'button', 'data-testid': `request-pay-${r.request_id}` }, `Pay ${fmt(r.amount)}`);
      const decline = h('button', { class: 'btn btn-secondary', type: 'button', 'data-testid': `request-decline-${r.request_id}` }, 'Decline');
      pay.addEventListener('click', async () => {
        const body = JSON.stringify({ visibility: vis.value });
        payIdent[r.request_id] = payIdent[r.request_id] || identity();
        busy(pay, true, 'Paying…');
        const res = await api('POST', `/requests/${encodeURIComponent(r.request_id)}/pay`, { body, key: payIdent[r.request_id].keyFor(body) });
        busy(pay, false);
        if (!res.uncertain && !res.ok) delete payIdent[r.request_id]; // refused: next try is a new attempt
        after(res, `Paid ${fmt(r.amount)} to ${r.requester_handle}.`, 'Payment not sent.',
          () => latest[r.request_id] && latest[r.request_id].status === 'paid');
      });
      decline.addEventListener('click', async () => {
        busy(decline, true, 'Declining…');
        const res = await api('POST', `/requests/${encodeURIComponent(r.request_id)}/decline`);
        busy(decline, false);
        after(res, `Declined ${r.requester_handle}’s request.`, 'Couldn’t decline.');
      });
      actions.push(miniField(`vis-${r.request_id}`, 'Visibility', vis), pay, decline);
    }
    if (pending && !isIncoming) {
      const cancel = h('button', { class: 'btn btn-danger', type: 'button', 'data-testid': `request-cancel-${r.request_id}` }, 'Cancel request');
      cancel.addEventListener('click', async () => {
        busy(cancel, true, 'Cancelling…');
        const res = await api('POST', `/requests/${encodeURIComponent(r.request_id)}/cancel`);
        busy(cancel, false);
        after(res, 'Request cancelled.', 'Couldn’t cancel.');
      });
      actions.push(cancel);
    }
    const kind = isIncoming ? 'out' : 'in';
    return h('li', { class: 'item', 'data-testid': `request-item-${r.request_id}`, 'data-status': r.status },
      h('span', { class: `dir dir-${pending ? kind : 'other'}`, 'aria-hidden': 'true' }, icon(pending ? kind : 'other')),
      h('div', { class: 'item-main' },
        h('p', { class: 'parties' }, isIncoming ? `${r.requester_handle} asks you` : `You asked ${r.payer_handle}`),
        h('p', { class: 'note' }, r.note),
        h('div', { class: 'meta' }, statusBadge(r.status), when(r.created_at))),
      h('div', { class: 'item-amount' }, h('span', { class: 'money', 'data-testid': `request-amount-${r.request_id}` }, fmt(r.amount))),
      actions.length ? h('div', { class: 'actions' }, actions) : null);
  };

  const apply = ([list]) => {
    summary.replaceChildren(walletNumbers(false));
    const mine = list.requests;
    latest = Object.fromEntries(mine.map((r) => [r.request_id, r]));
    const inc = mine.filter((r) => r.payer_id === me.user_id), out = mine.filter((r) => r.requester_id === me.user_id);
    incoming.replaceChildren(...inc.map((r) => item(r, true)));
    outgoing.replaceChildren(...out.map((r) => item(r, false)));
    if (!inc.length) incoming.append(h('li', { class: 'muted small' }, 'Nobody has asked you for money.'));
    if (!out.length) outgoing.append(h('li', { class: 'muted small' }, 'You haven’t asked anyone for money.'));
    // with nothing at all, one friendly empty state replaces the two empty cards
    // (the list containers stay in the page, hidden)
    emptySlot.replaceChildren(!mine.length ? h('section', { class: 'card empty-card' }, emptyState('empty-requests', 'No requests yet',
      'When you ask someone for money, or someone asks you, it shows up here.',
      h('a', { class: 'btn btn-primary', href: '/' }, 'Request money'))) : '');
    listsGrid.hidden = !mine.length;
    moreSlot.replaceChildren(list.has_more ? showMore('requests-more', () => { shown += PAGE; reload(); }) : '');
  };
  const reload = async () => {
    const res = await refresh([() => fetchList('/requests', 'requests', shown)], apply);
    refreshNote(refreshSlot, res, reload);
  };
  // `confirmed()` checks the refreshed list: an unknown outcome that the server shows as
  // done is reported as done; otherwise the action stays retryable with the same key.
  async function after(res, okText, refusedTitle, confirmed) {
    if (res.uncertain) say(alertSlot, 'request', 'uncertain', 'Not confirmed yet.', ' We couldn’t confirm whether that went through. Retrying is safe — a payment is only ever made once.');
    else if (res.ok) say(alertSlot, 'request', 'success', okText, null);
    else say(alertSlot, 'request', 'error', refusedTitle, ' ' + messageFor(res, { not_found: 'This request no longer exists.' }), res.code);
    await reload();
    if (res.uncertain && confirmed && confirmed()) say(alertSlot, 'request', 'success', okText, ' Confirmed after reconnecting.');
  }

  summary.append(walletNumbers(false));
  main.replaceChildren(
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Requests'), h('p', {}, 'Money people have asked you for, and money you’ve asked for.'))),
    summary, refreshSlot, alertSlot, emptySlot, listsGrid, moreSlot);
  listsGrid.append(
      h('section', { class: 'card', 'aria-labelledby': 'in-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'in-title' }, 'To pay'), h('p', {}, 'Requests to you')), incoming),
      h('section', { class: 'card', 'aria-labelledby': 'out-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'out-title' }, 'Asked by you'), h('p', {}, 'Requests you sent')), outgoing));
  await reload();
}

// ---------------------------------------------------------------- split

function splitPage(main) {
  const ident = identity();
  const preview = h('div', { 'aria-live': 'polite' });
  const status = h('div', { 'aria-live': 'polite' });
  const submit = h('button', { class: 'btn btn-primary btn-block', type: 'submit', 'data-testid': 'split-submit' }, 'Split and send requests');
  const handlesOf = (v) => String(v).split(',').map((x) => cleanHandle(x)).filter((x) => x !== '');

  const form = h('form', { novalidate: true },
    amountInput('split-amount', 'Total amount you paid'),
    field('split-handles', 'Split between', h('input', { type: 'text', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false', placeholder: `${me.handle}, bob, cy` }),
      'Handles separated by commas, in order. Include yourself if you’re sharing the cost.'),
    field('split-note', 'Note (optional)', h('input', { type: 'text', maxlength: '200', placeholder: 'e.g. Dinner at Luigi’s' })),
    preview, submit, status);

  const renderPreview = () => {
    const amt = parseAmount($('#split-amount', form).value);
    const hs = handlesOf($('#split-handles', form).value);
    if (amt.error || !hs.length) {
      preview.replaceChildren(h('p', { class: 'hint small muted', style: 'margin-bottom:16px' }, 'Enter an amount and at least one handle to see each person’s share.'));
      return;
    }
    const shares = equalSplit(amt.minor, hs.length);
    const dup = hs.find((x, i) => hs.indexOf(x) !== i);
    preview.replaceChildren(h('div', { class: 'preview', 'data-testid': 'split-preview' },
      h('h3', {}, 'Each person’s share'),
      h('ol', {}, hs.map((x, i) => h('li', {},
        h('span', {}, x, x === me.handle ? h('span', { class: 'you' }, ' (you — no request)') : null),
        h('span', { class: 'money', 'data-testid': `split-share-${x}` }, fmt(shares[i]))))),
      dup ? h('p', { class: 'warn' }, `${dup} is listed twice — each person can appear only once.`) : null));
  };
  form.addEventListener('input', renderPreview);
  renderPreview();

  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    if (submit.disabled) return;
    const amt = parseAmount($('#split-amount', form).value);
    const hs = handlesOf($('#split-handles', form).value);
    if (amt.error) return say(status, 'split', 'error', 'Check the amount.', ' ' + amt.error);
    if (!hs.length) return say(status, 'split', 'error', 'Who are you splitting with?', ' Enter at least one handle.');
    const body = JSON.stringify({ amount: amt.minor, participant_handles: hs, note: $('#split-note', form).value });
    busy(submit, true, 'Splitting…');
    const r = await api('POST', '/splits', { body, key: ident.keyFor(body) });
    busy(submit, false);
    if (r.uncertain) return say(status, 'split', 'uncertain', 'Not confirmed yet.', ' We couldn’t confirm whether this split was created. Retrying is safe — requests are only sent once.');
    if (!r.ok) {
      ident.reset();
      return say(status, 'split', 'error', 'Split not created.', ' ' + messageFor(r, {
        not_found: 'One of those handles doesn’t belong to anyone.',
        validation_failed: 'Each person can appear only once, and the amount must be valid.' }), r.code);
    }
    const asked = r.data.requests.map((q) => `${q.payer_handle} (${fmt(q.amount)})`);
    say(status, 'split', 'success', 'Split created.', asked.length ? ` Requests sent to ${asked.join(', ')}.` : ' Nobody else to ask — no requests were needed.');
  });

  main.replaceChildren(
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Split a bill'), h('p', {}, 'Paid for everyone? Ask each person for their share.'))),
    h('div', { class: 'grid side' },
      h('section', { class: 'card', 'aria-labelledby': 'split-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'split-title' }, 'New split')), form),
      h('section', { class: 'card' }, h('h2', {}, 'How shares work'),
        h('p', { class: 'small muted', style: 'margin-top:8px' }, `Everyone pays the same amount. If it doesn’t divide exactly, the first people in the list pay ${fmt(1)} more each, so the shares always add up to the total.`),
        h('p', { class: 'small muted', style: 'margin-top:8px' }, 'Each person except you gets a request they can pay or decline from their Requests page.'))));
}

// ---------------------------------------------------------------- holds (authorizations)

// The authorise form, used on "/" (as "Reserve money") and on "/authorizations".
function authorizeForm(onDone, primary) {
  const ident = identity();
  const status = h('div', { 'aria-live': 'polite' });
  const submit = h('button', { class: `btn ${primary ? 'btn-primary' : 'btn-secondary'} btn-block`, type: 'submit', 'data-testid': 'authorize-submit' }, 'Place hold');
  const form = h('form', { novalidate: true },
    h('div', { class: 'row two' }, handleInput('authorize-handle', 'For'), amountInput('authorize-amount', 'Amount to hold')),
    h('div', { class: 'row two' }, visibilitySelect('authorize-visibility', 'Who can see the payment'),
      field('authorize-note', 'Note (optional)', h('input', { type: 'text', maxlength: '200', placeholder: 'e.g. Deposit' }))),
    submit, status);
  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    if (submit.disabled) return;
    const handle = cleanHandle($('#authorize-handle', form).value);
    const amt = parseAmount($('#authorize-amount', form).value);
    if (!handle) return say(status, 'authorize', 'error', 'Who is the hold for?', ' Enter their handle.');
    if (amt.error) return say(status, 'authorize', 'error', 'Check the amount.', ' ' + amt.error);
    const body = JSON.stringify({ to_handle: handle, amount: amt.minor, note: $('#authorize-note', form).value, visibility: $('#authorize-visibility', form).value });
    busy(submit, true, 'Placing hold…');
    const r = await api('POST', '/authorizations', { body, key: ident.keyFor(body) });
    busy(submit, false);
    if (r.uncertain) say(status, 'authorize', 'uncertain', 'Not confirmed yet.', ' We couldn’t confirm whether this hold was placed. Retrying is safe — it will only be placed once.');
    else if (r.ok) say(status, 'authorize', 'success', `${fmt(r.data.amount)} is on hold for ${r.data.to_handle}.`, ` It expires ${new Date(r.data.expires_at).toLocaleString('en-GB', { hour: '2-digit', minute: '2-digit', day: 'numeric', month: 'short' })}.`);
    else { ident.reset(); say(status, 'authorize', 'error', 'Hold not placed.', ' ' + messageFor(r, { insufficient_funds: 'Not enough available funds for this hold.' }), r.code); }
    await onDone();
  });

  return form;
}

async function authorizationsPage(main) {
  const capIdent = {};
  const summary = h('div', {});
  const alertSlot = h('div', { 'aria-live': 'polite', class: 'page-alert' });
  const list = h('ul', { class: 'list', 'data-testid': 'authorization-list', 'aria-label': 'Holds, newest first' });
  const emptySlot = h('div', {});
  const refreshSlot = h('div', { class: 'page-alert' });
  const moreSlot = h('div', {});
  let shown = PAGE;
  let latest = {};
  const capTyped = {};

  const item = (a) => {
    const outgoing = a.from_user_id === me.user_id;
    const open = a.status === 'open';
    const actions = [];
    if (open && !outgoing) {
      // a typed amount survives list refreshes, so a retry re-sends the same body
      const typed = capTyped[a.authorization_id];
      const amountIn = h('input', { type: 'text', inputmode: 'decimal', 'data-testid': `authorization-capture-amount-${a.authorization_id}`, id: `cap-${a.authorization_id}`,
        value: typed !== undefined ? typed : toDecimal(a.remaining_amount), oninput: (e) => { capTyped[a.authorization_id] = e.target.value; } });
      const capture = h('button', { class: 'btn btn-primary', type: 'button', 'data-testid': `authorization-capture-${a.authorization_id}` }, 'Collect');
      capture.addEventListener('click', async () => {
        const amt = parseAmount(amountIn.value);
        if (amt.error) return say(alertSlot, 'authorization', 'error', 'Check the amount to collect.', ' ' + amt.error);
        const body = JSON.stringify({ amount: amt.minor });
        capIdent[a.authorization_id] = capIdent[a.authorization_id] || identity();
        busy(capture, true, 'Collecting…');
        const r = await api('POST', `/authorizations/${encodeURIComponent(a.authorization_id)}/capture`, { body, key: capIdent[a.authorization_id].keyFor(body) });
        busy(capture, false);
        if (!r.uncertain && !r.ok) delete capIdent[a.authorization_id];
        after(r, `Collected ${fmt(amt.minor)} from ${a.from_handle}.`, 'Couldn’t collect.',
          () => latest[a.authorization_id] && latest[a.authorization_id].captured_amount >= a.captured_amount + amt.minor);
      });
      actions.push(miniField(`cap-${a.authorization_id}`, 'Amount to collect', amountIn), capture);
    }
    if (open && outgoing) {
      const voidBtn = h('button', { class: 'btn btn-danger', type: 'button', 'data-testid': `authorization-void-${a.authorization_id}` }, 'Release hold');
      voidBtn.addEventListener('click', async () => {
        busy(voidBtn, true, 'Releasing…');
        const r = await api('POST', `/authorizations/${encodeURIComponent(a.authorization_id)}/void`);
        busy(voidBtn, false);
        after(r, `Released ${fmt(a.remaining_amount)} back to your available funds.`, 'Couldn’t release.');
      });
      actions.push(voidBtn);
    }
    return h('li', { class: 'item', 'data-testid': `authorization-item-${a.authorization_id}`, 'data-status': a.status },
      h('span', { class: 'dir dir-hold', 'aria-hidden': 'true' }, icon('hold')),
      h('div', { class: 'item-main' },
        h('p', { class: 'parties' }, outgoing ? `You hold for ${a.to_handle}` : `${a.from_handle} holds for you`),
        h('p', { class: 'note' }, a.note),
        h('div', { class: 'meta' }, statusBadge(a.status), privacyBadge(a.visibility),
          a.captured_amount > 0 ? h('span', {}, 'Collected ', a.status === 'captured'
            ? h('span', { class: 'money', 'data-testid': `authorization-captured-${a.authorization_id}` }, fmt(a.captured_amount))
            : h('span', { class: 'money' }, fmt(a.captured_amount))) : null,
          open ? h('span', { class: 'held-text' }, `${fmt(a.remaining_amount)} still held`) : null),
        h('div', { class: 'meta' }, h('span', {}, open ? 'Expires ' : 'Expiry ', when(a.expires_at))),
        h('div', { class: 'meta' }, h('span', { class: 'mono', title: 'Expiry time (UTC, RFC 3339)', 'data-testid': `authorization-expires-${a.authorization_id}` }, a.expires_at))),
      h('div', { class: 'item-amount' }, h('span', { class: 'dirword' }, 'Authorised'),
        h('span', { class: 'money', 'data-testid': `authorization-amount-${a.authorization_id}` }, fmt(a.amount))),
      actions.length ? h('div', { class: 'actions' }, actions) : null);
  };

  const apply = ([data]) => {
    summary.replaceChildren(walletNumbers(false));
    latest = Object.fromEntries(data.authorizations.map((a) => [a.authorization_id, a]));
    list.replaceChildren(...data.authorizations.map(item));
    emptySlot.replaceChildren(!data.authorizations.length ? emptyState('empty-authorizations', 'No holds',
      'A hold reserves money for someone to collect later. It stays yours until they collect it, and is released if it expires.') : '');
    moreSlot.replaceChildren(data.has_more ? showMore('authorizations-more', () => { shown += PAGE; reload(); }) : '');
  };
  const reload = async () => {
    const res = await refresh([() => fetchList('/authorizations', 'authorizations', shown)], apply);
    refreshNote(refreshSlot, res, reload);
  };
  async function after(r, okText, refusedTitle, confirmed) {
    if (r.uncertain) say(alertSlot, 'authorization', 'uncertain', 'Not confirmed yet.', ' We couldn’t confirm whether that went through. Retrying is safe — nothing is collected twice.');
    else if (r.ok) say(alertSlot, 'authorization', 'success', okText, null);
    else say(alertSlot, 'authorization', 'error', refusedTitle, ' ' + messageFor(r), r.code);
    await reload();
    if (r.uncertain && confirmed && confirmed()) say(alertSlot, 'authorization', 'success', okText, ' Confirmed after reconnecting.');
  }

  summary.append(walletNumbers(false));
  main.replaceChildren(
    h('div', { class: 'page-head' }, h('div', {}, h('h1', {}, 'Holds'), h('p', {}, 'Reserve money now; the other person collects it later.'))),
    summary, refreshSlot, alertSlot,
    h('div', { class: 'grid side' },
      h('section', { class: 'card', 'aria-labelledby': 'hold-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'hold-title' }, 'Place a hold')), authorizeForm(reload, true)),
      h('section', { class: 'card', 'aria-labelledby': 'holds-title' }, h('div', { class: 'card-head' }, h('h2', { id: 'holds-title' }, 'Your holds'), h('p', {}, 'Newest first')), emptySlot, list, moreSlot)));
  await reload();
}

// ---------------------------------------------------------------- signup / login

function signedInNote() {
  if (!me) return null;
  return h('div', { class: 'msg msg-success signed-in-note', role: 'status' }, h('span', { class: 'icon', 'aria-hidden': 'true' }, '✓'),
    h('div', {}, h('strong', {}, `You’re logged in as ${me.display_name || me.handle}.`), ' ', h('a', { href: '/' }, 'Go to your wallet')));
}

function deriveHandle(email) {
  const at = email.indexOf('@');
  if (at < 1) return '';
  return Array.from(email.slice(0, at).toLowerCase()).map((c) => /[a-z0-9_]/.test(c) ? c : '_').slice(0, 20).join('');
}

function authPage(main, mode) {
  const isLogin = mode === 'login';
  const p = isLogin ? 'login' : 'signup';
  const errSlot = h('div', { 'aria-live': 'assertive' });
  const submit = h('button', { class: 'btn btn-primary btn-block', type: 'submit', 'data-testid': `${p}-submit` }, isLogin ? 'Log in' : 'Create account');
  const handleHint = h('span', { class: 'hint', id: 'handle-preview' });
  const fields = [field(`${p}-email`, 'Email', h('input', { type: 'email', autocomplete: 'email', required: true }))];
  if (!isLogin) fields.push(field('signup-display-name', 'Your name', h('input', { type: 'text', autocomplete: 'name', required: true }), 'Shown to people you pay.'));
  fields.push(field(`${p}-password`, 'Password', h('input', { type: 'password', autocomplete: isLogin ? 'current-password' : 'new-password', required: true }),
    isLogin ? null : 'At least 8 characters.'));
  const form = h('form', { novalidate: true }, fields, isLogin ? null : handleHint, errSlot, submit);
  if (!isLogin) {
    form.addEventListener('input', () => {
      const hd = deriveHandle($('#signup-email', form).value);
      handleHint.textContent = hd ? `People will pay you at @${hd}.` : '';
    });
  }
  const showErr = (text) => {
    errSlot.replaceChildren(h('div', { class: 'msg msg-error', role: 'alert', 'data-testid': 'auth-error' },
      h('span', { class: 'icon', 'aria-hidden': 'true' }, '!'), h('div', {}, text)));
  };
  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    if (submit.disabled) return;
    errSlot.replaceChildren();
    const body = { email: $(`#${p}-email`, form).value.trim(), password: $(`#${p}-password`, form).value };
    if (!isLogin) body.display_name = $('#signup-display-name', form).value.trim();
    if (!body.email || !body.password || (!isLogin && !body.display_name)) return showErr('Please fill in every field.');
    if (!isLogin && body.password.length < 8) return showErr('Your password needs at least 8 characters.');
    busy(submit, true, isLogin ? 'Logging in…' : 'Creating account…');
    const r = await api('POST', isLogin ? '/auth/login' : '/auth/signup', { body: JSON.stringify(body), auth: false });
    busy(submit, false);
    if (r.uncertain) return showErr('We couldn’t reach Pocketful. Check your connection and try again.');
    if (!r.ok) {
      return showErr(messageFor(r, { validation_failed: isLogin ? 'Enter your email and password.' : 'Check your email address and use a password of at least 8 characters.' }));
    }
    localStorage.setItem(TOKEN_KEY, r.data.token);
    location.assign('/');
  });
  const notice = sessionStorage.getItem(NOTICE_KEY);
  sessionStorage.removeItem(NOTICE_KEY);
  main.replaceChildren(h('div', { class: 'auth-wrap' }, signedInNote(),
    notice ? h('div', { class: 'msg msg-uncertain signed-in-note', role: 'status', 'data-testid': 'session-notice' },
      h('span', { class: 'icon', 'aria-hidden': 'true' }, '!'), h('div', {}, notice)) : null,
    h('section', { class: 'card' },
      h('h1', {}, isLogin ? 'Welcome back' : 'Create your account'),
      h('p', { class: 'lede' }, isLogin ? 'Log in to send and receive money.' : 'Pay friends by handle, split bills and keep track of who owes what.'),
      form,
      h('p', { class: 'switch' }, isLogin ? 'New to Pocketful? ' : 'Already have an account? ',
        h('a', { href: isLogin ? '/signup' : '/login' }, isLogin ? 'Create an account' : 'Log in')))));
}

// ---------------------------------------------------------------- boot

const PAGES = { '/': walletPage, '/requests': requestsPage, '/split': splitPage, '/authorizations': authorizationsPage };

async function boot() {
  const main = $('#main');
  const path = location.pathname.replace(/\/+$/, '') || '/';
  const isAuthPage = path === '/login' || path === '/signup';
  if (!token && !isAuthPage) { location.replace('/login'); return; }
  if (token) {
    const r = await api('GET', '/me', { auth: false });
    if (r.ok) me = normalizeMe(r.data);
    else if (r.status === 401) { localStorage.removeItem(TOKEN_KEY); token = null; if (!isAuthPage) { location.replace('/login'); return; } }
    else if (!isAuthPage) {
      renderHeader();
      main.removeAttribute('aria-busy');
      main.replaceChildren(h('div', { class: 'card', role: 'alert', 'data-testid': 'load-error' }, h('h1', {}, 'We couldn’t load your wallet'),
        h('p', { class: 'muted', style: 'margin:8px 0 16px' }, 'Check your connection and try again.'),
        h('button', { class: 'btn btn-primary', type: 'button', onclick: () => location.reload() }, 'Try again')));
      return;
    }
  }
  renderHeader();
  if (isAuthPage) authPage(main, path.slice(1));
  else await (PAGES[path] || walletPage)(main);
  main.removeAttribute('aria-busy');
}

boot();
