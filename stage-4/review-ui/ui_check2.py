"""Real-browser checks for competing clients, uncertain outcomes, the stage-1 → stage-2
upgrade and edge cases (implementer's own; extends ui_check.py).

The page talks to a small reverse proxy (this file) that forwards to the service
containers on an internal Docker network (no outbound access). The proxy can delay or
fail chosen responses AFTER the backend has answered (so writes are committed), and can
switch its API backend from a stage-1 to a stage-2 container while the page stays loaded.

    docker build -t pocketful-stage2 stage-2 && docker build -t pocketful-stage1 stage-1
    <kickoff>/.venv/bin/python stage-2/review-ui/ui_check2.py
"""
from __future__ import annotations

import http.client
import http.server
import json
import socketserver
import sys
import threading
import time
import urllib.parse
import uuid
from datetime import datetime, timedelta, timezone

from playwright.sync_api import sync_playwright

import ui_check as base
from ui_check import check, fixture, sel, text, user

HOP = {"connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "proxy-authorization", "proxy-authenticate", "host", "content-length"}
UI_PATHS = {"/", "/split", "/signup", "/login"}


class Proxy:
    """Forwards to `api` (switchable) and serves UI files from `ui`. Rules act once each."""

    def __init__(self, ui: str, api: str):
        self.ui, self.api = ui, api
        self.rules: list[dict] = []
        self.lock = threading.Lock()
        proxy = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *a):
                pass

            def do_ANY(self):
                n = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(n) if n else None
                path = urllib.parse.urlsplit(self.path).path
                html = "text/html" in (self.headers.get("Accept") or "")
                is_ui = self.command == "GET" and (path in UI_PATHS or path.startswith("/static/")
                                                   or (path in ("/requests", "/authorizations") and html))
                target = proxy.ui if is_ui else proxy.api
                rule = None if is_ui else proxy.take(self.command, path)
                if rule and rule.get("drop_before"):
                    return self.reply(502, b'{"error":{"code":"bad_gateway","message":"upstream unavailable"}}')
                u = urllib.parse.urlsplit(target)
                conn = http.client.HTTPConnection(u.hostname, u.port, timeout=30)
                headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP}
                conn.request(self.command, self.path, body=body, headers=headers)
                resp = conn.getresponse()
                data = resp.read()
                ctype = resp.getheader("Content-Type")
                conn.close()
                if rule:
                    time.sleep(rule.get("delay", 0))
                    if rule.get("status"):
                        return self.reply(rule["status"], b'{"error":{"code":"bad_gateway","message":"proxy error"}}')
                self.reply(resp.status, data, ctype)

            def reply(self, status, data, ctype="application/json; charset=utf-8"):
                try:
                    self.send_response(status)
                    if ctype:
                        self.send_header("Content-Type", ctype)
                    self.send_header("Content-Length", str(len(data)))
                    self.end_headers()
                    self.wfile.write(data)
                except (BrokenPipeError, ConnectionResetError):
                    pass  # the browser gave up (client timeout)

            do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = do_ANY

        class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
            daemon_threads = True
            allow_reuse_address = True

        self.server = Server(("127.0.0.1", 0), Handler)
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def rule(self, method, path, **kw):
        with self.lock:
            self.rules.append({"method": method, "path": path, **kw})

    def take(self, method, path):
        with self.lock:
            for r in self.rules:
                if r["method"] == method and (path == r["path"] or path.startswith(r["path"] + "?") or r["path"].endswith("*") and path.startswith(r["path"][:-1])):
                    self.rules.remove(r)
                    return r
        return None

    def clear(self):
        with self.lock:
            self.rules.clear()


class Ctx:
    def __init__(self, pw, proxy, s2, s1=None):
        self.proxy, self.s2, self.s1 = proxy, s2, s1
        self.browser = pw.chromium.launch()
        self.errors: list[str] = []
        self.foreign: list[str] = []
        self.sent: list[tuple[str, str, str | None, str | None]] = []  # (method, path, key, body)

    def page(self, w=1280, h=800, reduced=False):
        c = self.browser.new_context(base_url=self.proxy.base, viewport={"width": w, "height": h},
                                     reduced_motion="reduce" if reduced else "no-preference")
        p = c.new_page()
        p.set_default_timeout(12000)
        p.on("pageerror", lambda e: self.errors.append(str(e)))
        p.on("console", lambda m: m.type == "error" and not m.text.startswith("Failed to load resource") and self.errors.append(m.text))

        def on_req(r):
            if not r.url.startswith(self.proxy.base):
                self.foreign.append(r.url)
            if r.method == "POST":
                self.sent.append((r.method, urllib.parse.urlsplit(r.url).path, r.headers.get("idempotency-key"), r.post_data))
        p.on("request", on_req)
        return p

    def login(self, p, handle="ada"):
        p.goto("/login")
        p.fill(sel("login-email"), f"{handle}@example.com")
        p.fill(sel("login-password"), "correct horse")
        p.click(sel("login-submit"))
        p.wait_for_selector(sel("current-user"))

    def posts(self, path):
        return [s for s in self.sent if s[1] == path]


def amount_of(p, t):
    return p.get_attribute(sel(t), "data-amount")


def feed(svc, handle):
    return svc.call("GET", "/activity?limit=200", token=svc.token(handle))[1]["payments"]


# ---------------------------------------------------------------- 1. latest refresh wins

def latest_refresh_wins(cx):
    s2, px = cx.s2, cx.proxy
    s2.reset(fixture())
    p = cx.page()
    cx.login(p)
    p.wait_for_selector(f"{sel('wallet-available')}[data-amount='10000']")
    ada = s2.token("ada")

    # (a) earlier manual refresh delayed past a later manual refresh
    px.rule("GET", "/me", delay=2.0)
    px.rule("GET", "/activity", delay=2.0)
    p.click(sel("wallet-refresh"))                      # read #1 (old state), delayed
    time.sleep(0.3)
    pay = s2.call("POST", "/payments", {"to_handle": "bob", "amount": 300}, token=ada, key=uuid.uuid4().hex)[1]
    p.click(sel("wallet-refresh"))                      # read #2 (new state), fast
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='9700']")
    p.wait_for_timeout(2600)                            # read #1 arrives now
    check(amount_of(p, "wallet-balance") == "9700" and amount_of(p, "wallet-available") == "9700"
          and p.query_selector(sel(f"activity-item-{pay['payment_id']}")) is not None,
          "wallet: delayed earlier refresh does not overwrite a later one")

    # (b) the other order: earlier fast, later delayed — the later one still lands
    p.click(sel("wallet-refresh"))
    s2.call("POST", "/payments", {"to_handle": "bob", "amount": 100}, token=ada, key=uuid.uuid4().hex)
    px.rule("GET", "/me", delay=1.5)
    px.rule("GET", "/activity", delay=1.5)
    p.click(sel("wallet-refresh"))
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='9600']", timeout=6000)
    check(True, "wallet: in-order delayed later refresh applies")

    # (c) action-triggered refresh (after paying) delayed vs a later manual refresh
    px.rule("GET", "/me", delay=2.0)
    px.rule("GET", "/activity", delay=2.0)
    p.fill(sel("pay-handle"), "bob")
    p.fill(sel("pay-amount"), "1.00")
    p.click(sel("pay-submit"))                          # POST fast; its refresh is delayed
    p.wait_for_selector(sel("pay-success"))
    s2.call("POST", "/payments", {"to_handle": "cy", "amount": 500}, token=ada, key=uuid.uuid4().hex)
    p.click(sel("wallet-refresh"))
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='9000']")
    p.wait_for_timeout(2600)
    check(amount_of(p, "wallet-balance") == "9000", "wallet: delayed action refresh does not overwrite a later manual refresh",
          amount_of(p, "wallet-balance"))

    # (d) holds change available/held under the same rule
    px.rule("GET", "/me", delay=2.0)
    px.rule("GET", "/activity", delay=2.0)
    p.click(sel("wallet-refresh"))
    time.sleep(0.3)
    s2.call("POST", "/authorizations", {"to_handle": "bob", "amount": 2000}, token=ada, key=uuid.uuid4().hex)
    p.click(sel("wallet-refresh"))
    p.wait_for_selector(f"{sel('wallet-held')}[data-amount='2000']")
    p.wait_for_timeout(2600)
    check(amount_of(p, "wallet-available") == "7000" and p.query_selector(sel("wallet-held")) is not None,
          "wallet: available/held keep the later read")
    p.context.close()

    # requests screen: the refresh after a decline is delayed; a later action's refresh wins
    s2.reset(fixture())
    bob = s2.token("bob")
    r1, r2 = [s2.call("POST", "/requests", {"payer_handle": "ada", "amount": 100 + i}, token=bob, key=uuid.uuid4().hex)[1]["request_id"] for i in range(2)]
    p = cx.page()
    cx.login(p)
    p.goto("/requests")
    p.wait_for_selector(sel(f"request-decline-{r1}"))
    px.rule("GET", "/requests", delay=2.0)
    px.rule("GET", "/me", delay=2.0)
    p.click(sel(f"request-decline-{r1}"))
    p.wait_for_selector(sel("request-success"))
    r3 = s2.call("POST", "/requests", {"payer_handle": "ada", "amount": 777}, token=bob, key=uuid.uuid4().hex)[1]["request_id"]
    p.click(sel(f"request-decline-{r2}"))
    p.wait_for_selector(sel(f"request-item-{r3}"))
    p.wait_for_timeout(2600)
    check(p.query_selector(sel(f"request-item-{r3}")) is not None
          and p.get_attribute(sel(f"request-item-{r2}"), "data-status") == "declined",
          "requests: delayed earlier refresh does not overwrite a later one")
    p.context.close()

    # holds screen: same with voids
    s2.reset(fixture())
    ada = s2.token("ada")
    a1, a2 = [s2.call("POST", "/authorizations", {"to_handle": "bob", "amount": 100 + i}, token=ada, key=uuid.uuid4().hex)[1]["authorization_id"] for i in range(2)]
    p = cx.page()
    cx.login(p)
    p.goto("/authorizations")
    p.wait_for_selector(sel(f"authorization-void-{a1}"))
    px.rule("GET", "/authorizations", delay=2.0)
    px.rule("GET", "/me", delay=2.0)
    p.click(sel(f"authorization-void-{a1}"))
    p.wait_for_selector(sel("authorization-success"))
    a3 = s2.call("POST", "/authorizations", {"to_handle": "cy", "amount": 300}, token=ada, key=uuid.uuid4().hex)[1]["authorization_id"]
    p.click(sel(f"authorization-void-{a2}"))
    p.wait_for_selector(sel(f"authorization-item-{a3}"))
    p.wait_for_timeout(2600)
    check(p.query_selector(sel(f"authorization-item-{a3}")) is not None and amount_of(p, "wallet-held") == "300",
          "holds: delayed earlier refresh does not overwrite a later one")
    p.context.close()
    px.clear()


# ---------------------------------------------------------------- 2. competing clients

def competing_clients(cx):
    s2 = cx.s2
    s2.reset(fixture())
    p = cx.page()
    cx.login(p, "cy")
    p.fill(sel("pay-handle"), "bob")
    p.fill(sel("pay-amount"), "4.00")
    p.fill(sel("pay-note"), "lunch")
    p.select_option(sel("pay-visibility"), "private")
    s2.call("POST", "/payments", {"to_handle": "ada", "amount": 500}, token=s2.token("cy"), key=uuid.uuid4().hex)  # spent elsewhere
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-error"))
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='0']")
    check([p.input_value(sel(x)) for x in ("pay-handle", "pay-amount", "pay-note", "pay-visibility")] == ["bob", "4.00", "lunch", "private"],
          "refused after another client spent: pay-error, balance refreshed, all inputs kept")
    check(len(p.query_selector_all(f"{sel('activity-list')} > li")) == 1, "refused pay refreshed the feed")
    base_shot(p, "wallet-refused-elsewhere-1280")
    p.context.close()

    bob = s2.token("bob")
    mk = lambda payer, amt, tok=bob: s2.call("POST", "/requests", {"payer_handle": payer, "amount": amt}, token=tok, key=uuid.uuid4().hex)[1]["request_id"]
    rq_cancel, rq_decline = mk("ada", 100), mk("ada", 200)
    out_paid = mk("bob", 50, s2.token("ada"))
    p = cx.page()
    cx.login(p)
    p.goto("/requests")
    p.wait_for_selector(sel(f"request-pay-{rq_cancel}"))
    s2.call("POST", f"/requests/{rq_cancel}/cancel", token=bob)          # cancelled elsewhere
    p.click(sel(f"request-pay-{rq_cancel}"))
    p.wait_for_selector(sel("request-error"))
    p.wait_for_selector(sel(f"request-pay-{rq_cancel}"), state="detached")
    check(p.get_attribute(sel(f"request-item-{rq_cancel}"), "data-status") == "cancelled", "pay on a request cancelled elsewhere: request-error + refreshed")
    s2.call("POST", f"/requests/{rq_decline}/cancel", token=bob)          # the decline button is now stale
    p.click(sel(f"request-decline-{rq_decline}"))
    p.wait_for_selector(f"{sel('request-item-' + rq_decline)}[data-status='cancelled']")
    check(p.query_selector(sel("request-error")) is not None and p.query_selector(sel(f"request-decline-{rq_decline}")) is None,
          "decline on a request cancelled elsewhere: request-error + stale button gone")
    s2.call("POST", f"/requests/{out_paid}/pay", {}, token=bob, key=uuid.uuid4().hex)  # paid elsewhere
    p.click(sel(f"request-cancel-{out_paid}"))
    p.wait_for_selector(f"{sel('request-item-' + out_paid)}[data-status='paid']")
    check(p.query_selector(sel("request-error")) is not None, "cancel on a request paid elsewhere: request-error + refreshed")
    base_shot(p, "requests-refused-elsewhere-1280")
    p.context.close()

    exp_fixture = fixture(authorization_ttl_seconds=2)
    s2.reset(exp_fixture)
    ada = s2.token("ada")
    void_me = s2.call("POST", "/authorizations", {"to_handle": "bob", "amount": 100}, token=ada, key=uuid.uuid4().hex)[1]["authorization_id"]
    expire_me = s2.call("POST", "/authorizations", {"to_handle": "bob", "amount": 200}, token=ada, key=uuid.uuid4().hex)[1]["authorization_id"]
    p = cx.page()
    cx.login(p, "bob")
    p.goto("/authorizations")
    p.wait_for_selector(sel(f"authorization-capture-{void_me}"))
    s2.call("POST", f"/authorizations/{void_me}/void", token=ada)
    p.click(sel(f"authorization-capture-{void_me}"))
    p.wait_for_selector(sel("authorization-error"))
    p.wait_for_selector(f"{sel('authorization-item-' + void_me)}[data-status='voided']")
    check(p.query_selector(sel(f"authorization-capture-{void_me}")) is None, "capture on a hold voided elsewhere: authorization-error + refreshed")
    p.wait_for_timeout(2600)                                    # the other hold expires with no traffic
    p.click(sel(f"authorization-capture-{expire_me}"))
    p.wait_for_selector(f"{sel('authorization-item-' + expire_me)}[data-status='expired']")
    check("expired" in (text(p, "authorization-error") or "").lower(), "capture on an expired hold: authorization-error says expired", text(p, "authorization-error"))
    base_shot(p, "holds-capture-refused-1280")
    p.context.close()


# ---------------------------------------------------------------- 3. uncertain outcomes

def lose_after_commit(route):
    route.fetch()   # the server receives and commits it
    route.abort()   # but the browser never sees the answer


def uncertain_pay(cx):
    s2, px = cx.s2, cx.proxy
    variants = [
        ("lost after commit", lambda p: p.route("**/payments", lose_after_commit), True),
        ("lost before the server", lambda p: p.route("**/payments", lambda r: r.abort()), False),
        ("client timeout", lambda p: px.rule("POST", "/payments", delay=10.0), True),
        ("502 from a proxy", lambda p: px.rule("POST", "/payments", status=502), True),
    ]
    for i, (name, arm, committed) in enumerate(variants):
        s2.reset(fixture())
        p = cx.page(375 if i % 2 else 1280, 812 if i % 2 else 800)
        cx.login(p)
        p.fill(sel("pay-handle"), "bob")
        p.fill(sel("pay-amount"), "12.34")
        p.fill(sel("pay-note"), f"try {i}")
        start = len(cx.posts("/payments"))
        arm(p)
        p.click(sel("pay-submit"))
        p.wait_for_selector(sel("pay-uncertain"), timeout=15000)
        check(p.query_selector(sel("pay-error")) is None and len(text(p, "pay-uncertain")) > 20,
              f"uncertain ({name}): pay-uncertain shown, not pay-error")
        committed_now = len([x for x in feed(s2, "ada") if x["amount"] == 1234])
        check(committed_now == (1 if committed else 0), f"uncertain ({name}): server state as expected before retry", committed_now)
        if i == 0:
            base_shot(p, "wallet-uncertain-1280")
        if i == 1:
            base_shot(p, "wallet-uncertain-375")
        p.unroute("**/payments")
        px.clear()
        p.click(sel("pay-submit"))
        p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='{10000 - 1234}']")
        p.wait_for_selector(sel("pay-success"))
        sent = cx.posts("/payments")[start:]
        check(len(sent) == 2 and sent[0][2] == sent[1][2] and sent[0][3] == sent[1][3],
              f"uncertain ({name}): retry sends the same key and byte-identical body", sent)
        check(p.query_selector(sel("pay-uncertain")) is None and p.query_selector(sel("pay-error")) is None,
              f"uncertain ({name}): success clears both messages")
        n = len([x for x in feed(s2, "ada") if x["amount"] == 1234])
        check(n == 1 and len(p.query_selector_all(f"{sel('activity-list')} > li")) == 1, f"uncertain ({name}): money moved exactly once", n)
        if i == 0:
            base_shot(p, "wallet-pay-success-1280")
        p.context.close()

    # changing a field after an uncertain outcome starts a new identity
    s2.reset(fixture())
    p = cx.page()
    cx.login(p)
    p.fill(sel("pay-handle"), "bob")
    p.fill(sel("pay-amount"), "2.00")
    p.route("**/payments", lambda r: r.abort())
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-uncertain"))
    p.unroute("**/payments")
    k0 = cx.posts("/payments")[-1][2]
    p.fill(sel("pay-amount"), "3.00")
    check(p.query_selector(sel("pay-uncertain")) is not None, "after an edit the uncertain note stays until the next submit")
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-success"))
    check(cx.posts("/payments")[-1][2] != k0, "changed field after uncertain: new key")

    # a definite 4xx is a refusal and the next submit uses a new key
    p.fill(sel("pay-amount"), "999.00")
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-error"))
    k1 = cx.posts("/payments")[-1][2]
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-error"))
    check(cx.posts("/payments")[-1][2] != k1, "after a 4xx refusal the next submit uses a new key")
    base_shot(p, "wallet-pay-error-1280")

    # rapid double submit and Enter repeat never send two keys
    p.fill(sel("pay-amount"), "0.50")
    before = len(cx.posts("/payments"))
    px.rule("POST", "/payments", delay=0.8)
    p.dblclick(sel("pay-submit"))
    p.wait_for_selector(sel("pay-success"))
    p.wait_for_timeout(300)
    keys = {x[2] for x in cx.posts("/payments")[before:]}
    check(len(cx.posts("/payments")[before:]) == 1, "double-click sends one request while in flight", len(cx.posts("/payments")[before:]))
    p.fill(sel("pay-amount"), "0.25")
    before = len(cx.posts("/payments"))
    px.rule("POST", "/payments", delay=0.8)
    p.focus(sel("pay-amount"))
    p.keyboard.press("Enter")
    p.keyboard.press("Enter")
    p.keyboard.press("Enter")
    p.wait_for_selector(sel("pay-success"))
    p.wait_for_timeout(1500)
    keys = {x[2] for x in cx.posts("/payments")[before:]}
    n = len([x for x in feed(s2, "ada") if x["amount"] == 25])
    check(len(keys) == 1 and n == 1, "Enter repeat: one key, one payment", (keys, n))
    p.context.close()


def uncertain_other_writes(cx):
    s2 = cx.s2
    s2.reset(fixture())
    bob = s2.token("bob")
    rq, rq2 = [s2.call("POST", "/requests", {"payer_handle": "ada", "amount": a}, token=bob, key=uuid.uuid4().hex)[1]["request_id"] for a in (700, 701)]
    hold, hold2 = [s2.call("POST", "/authorizations", {"to_handle": "ada", "amount": a}, token=bob, key=uuid.uuid4().hex)[1]["authorization_id"] for a in (900, 901)]

    def lost_then_retry(p, path_glob, path, click, done_sel, prefix, shot=None, before=False):
        start = len(cx.posts(path))
        p.route(path_glob, (lambda r: r.abort()) if before else lose_after_commit)
        click()
        p.wait_for_selector(sel(f"{prefix}-uncertain"))
        check(p.query_selector(sel(f"{prefix}-error")) is None, f"{path}: lost response shows {prefix}-uncertain")
        if shot:
            base_shot(p, shot)
        p.unroute(path_glob)
        click()
        p.wait_for_selector(done_sel)
        sent = cx.posts(path)[start:]
        check(len(sent) == 2 and sent[0][2] == sent[1][2] and sent[0][3] == sent[1][3], f"{path}: retry reuses key and body", sent)

    p = cx.page()
    cx.login(p)
    p.fill(sel("request-handle"), "cy")
    p.fill(sel("request-amount"), "4.44")
    lost_then_retry(p, "**/requests", "/requests", lambda: p.click(sel("request-submit")), sel("request-success"), "request", "wallet-request-uncertain-1280")
    n = len([r for r in s2.call("GET", "/requests", token=s2.token("cy"))[1]["requests"] if r["amount"] == 444])
    check(n == 1, "request created exactly once", n)
    base_shot(p, "wallet-request-success-1280")

    p.goto("/split")
    p.fill(sel("split-amount"), "9.00")
    p.fill(sel("split-handles"), "ada,cy")
    lost_then_retry(p, "**/splits", "/splits", lambda: p.click(sel("split-submit")), sel("split-success"), "split")
    n = len([r for r in s2.call("GET", "/requests", token=s2.token("cy"))[1]["requests"] if r["amount"] == 450])
    check(n == 1, "split requests created exactly once", n)

    # request pay: lost after commit -> the refresh shows it paid and the UI confirms it
    p.goto("/requests")
    p.wait_for_selector(sel(f"request-pay-{rq}"))
    p.route(f"**/requests/{rq}/pay", lose_after_commit)
    p.click(sel(f"request-pay-{rq}"))
    p.wait_for_selector(f"{sel('request-item-' + rq)}[data-status='paid']")
    p.wait_for_selector(sel("request-success"))
    p.unroute(f"**/requests/{rq}/pay")
    check(p.query_selector(sel("request-uncertain")) is None and "Confirmed" in text(p, "request-success"),
          "request pay lost after commit: refresh confirms it, no uncertainty left")
    n = len([x for x in feed(s2, "ada") if x["request_id"] == rq])
    check(n == 1, "request paid exactly once", n)
    # request pay: lost before the server -> stays pending, uncertain, retry with the same key
    lost_then_retry(p, f"**/requests/{rq2}/pay", f"/requests/{rq2}/pay", lambda: p.click(sel(f"request-pay-{rq2}")),
                    f"{sel('request-item-' + rq2)}[data-status='paid']", "request", "requests-uncertain-1280", before=True)
    n = len([x for x in feed(s2, "ada") if x["request_id"] == rq2])
    check(n == 1, "request (lost before server) paid exactly once", n)

    p.goto("/authorizations")
    p.fill(sel("authorize-handle"), "cy")
    p.fill(sel("authorize-amount"), "6.00")
    lost_then_retry(p, "**/authorizations", "/authorizations", lambda: p.click(sel("authorize-submit")), sel("authorize-success"), "authorize")
    n = len([a for a in s2.call("GET", "/authorizations", token=s2.token("cy"))[1]["authorizations"] if a["amount"] == 600])
    check(n == 1, "hold placed exactly once", n)
    # capture: lost after commit -> refresh shows it captured, UI confirms
    p.wait_for_selector(sel(f"authorization-capture-{hold}"))
    p.fill(sel(f"authorization-capture-amount-{hold}"), "5.00")
    p.route(f"**/authorizations/{hold}/capture", lose_after_commit)
    p.click(sel(f"authorization-capture-{hold}"))
    p.wait_for_selector(f"{sel('authorization-item-' + hold)}[data-status='captured']")
    p.wait_for_selector(sel("authorization-success"))
    p.unroute(f"**/authorizations/{hold}/capture")
    check(p.query_selector(sel("authorization-uncertain")) is None, "capture lost after commit: refresh confirms it")
    n = len([x for x in feed(s2, "ada") if x.get("authorization_id") == hold])
    check(n == 1, "capture moved money exactly once", n)
    # capture: lost before the server -> retry with the same key and body
    p.fill(sel(f"authorization-capture-amount-{hold2}"), "4.00")
    lost_then_retry(p, f"**/authorizations/{hold2}/capture", f"/authorizations/{hold2}/capture", lambda: p.click(sel(f"authorization-capture-{hold2}")),
                    f"{sel('authorization-item-' + hold2)}[data-status='captured']", "authorization", "holds-capture-uncertain-1280", before=True)
    n = len([x for x in feed(s2, "ada") if x.get("authorization_id") == hold2])
    check(n == 1, "capture (lost before server) moved money exactly once", n)
    base_shot(p, "holds-capture-success-1280")
    p.set_viewport_size({"width": 375, "height": 812})
    base_shot(p, "holds-capture-success-375")
    p.context.close()


# ---------------------------------------------------------------- 4. upgrade without reload

def upgrade(cx):
    s1, s2, px = cx.s1, cx.s2, cx.proxy
    s1.reset(fixture())
    s2.reset(fixture(users=[user("zed", 5)]))       # unrelated destination data, replaced by import
    px.api = s1.base                                   # the page starts against the stage-1 API
    p = cx.page()
    cx.login(p)                                        # token minted by stage 1
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='10000']")
    bob1 = s1.token("bob")
    rq = s1.call("POST", "/requests", {"payer_handle": "ada", "amount": 1200, "note": "taxi"}, token=bob1, key=uuid.uuid4().hex)[1]["request_id"]

    p.fill(sel("pay-handle"), "bob")
    p.fill(sel("pay-amount"), "25.00")
    p.fill(sel("pay-note"), "before upgrade")
    p.route("**/payments", lose_after_commit)          # stage 1 commits; the answer is lost
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-uncertain"))
    p.unroute("**/payments")
    first = cx.posts("/payments")[-1]
    p.fill(sel("request-handle"), "cy")                # a form holding typed values across the upgrade
    p.fill(sel("request-amount"), "3.00")

    snap = s1.call("GET", "/_test/export")[1]          # upgrade: stage-1 export -> stage-2 import
    st, _ = s2.call("POST", "/_test/import", snap)
    check(st == 204, "stage-2 imports the stage-1 export", st)
    px.api = s2.base                                   # same origin, same page, new backend

    p.click(sel("pay-submit"))                         # retry: same key + body against stage 2
    p.wait_for_selector(sel("pay-success"))
    retry = cx.posts("/payments")[-1]
    check(retry[2] == first[2] and retry[3] == first[3], "retry after upgrade uses the stage-1 key and body")
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='7500']")
    pays = [x for x in feed(s2, "ada") if x["amount"] == 2500]
    check(len(pays) == 1, "the original payment is recovered, not duplicated", len(pays))
    check(p.query_selector(sel("pay-uncertain")) is None and p.query_selector(sel("current-user")) is not None and "/login" not in p.url,
          "still signed in after the upgrade, no reload, messages cleared")
    check(p.input_value(sel("request-amount")) == "3.00" and p.input_value(sel("pay-note")) == "before upgrade",
          "typed form values survive the upgrade")
    p.click(sel("request-submit"))
    p.wait_for_selector(sel("request-success"))
    check(True, "a form typed before the upgrade submits after it")
    check(amount_of(p, "wallet-available") == "7500" and p.query_selector(sel("wallet-held")) is None,
          "imported balance shown with stage-2 available/held")
    p.goto("/requests")
    p.wait_for_selector(sel(f"request-pay-{rq}"))
    p.click(sel(f"request-pay-{rq}"))
    p.wait_for_selector(f"{sel('request-item-' + rq)}[data-status='paid']")
    check(True, "a pending stage-1 request is payable through the request screen after the upgrade")
    base_shot(p, "requests-after-upgrade-1280")
    p.context.close()
    px.api = s2.base


# ---------------------------------------------------------------- 5. edge cases

def no_hscroll(p, label):
    sw, iw = p.evaluate("[document.documentElement.scrollWidth, window.innerWidth]")
    check(sw <= iw, f"no horizontal scroll: {label}", (sw, iw))


def edges(cx):
    s2 = cx.s2
    long_handle = "a" * 20
    big = 2 ** 53
    s2.reset(fixture(users=[user("ada", big - 10 ** 9 - 1), user(long_handle, 0), user("bob", 1_000_000_000)]))
    ada, longt = s2.token("ada"), s2.token(long_handle)
    note = "x" * 200
    s2.call("POST", "/payments", {"to_handle": long_handle, "amount": 1_000_000_000, "note": note}, token=ada, key=uuid.uuid4().hex)
    s2.call("POST", "/requests", {"payer_handle": "ada", "amount": 1_000_000_000, "note": note}, token=longt, key=uuid.uuid4().hex)
    s2.call("POST", "/authorizations", {"to_handle": long_handle, "amount": 1_000_000_000, "note": note}, token=ada, key=uuid.uuid4().hex)
    p = cx.page(375, 812)
    cx.login(p)
    p.wait_for_selector(sel("activity-list"))
    check(text(p, "wallet-balance") == f"{(big - 1 - 10 ** 9 - 10 ** 9) // 100}.{(big - 1 - 10 ** 9 - 10 ** 9) % 100:02d} EUR",
          "total near 2^53 formats exactly", text(p, "wallet-balance"))
    no_hscroll(p, "wallet with 200-char note, 20-char handle, near-2^53 balance @375")
    base_shot(p, "wallet-long-note-375")
    for route in ("/requests", "/authorizations", "/split"):
        p.goto(route)
        p.wait_for_load_state("networkidle")
        if route == "/split":
            p.fill(sel("split-amount"), "10000000.00")
            p.fill(sel("split-handles"), ",".join([long_handle, "bob", "ada"]))
        no_hscroll(p, f"{route} with long values @375")
    base_shot(p, "split-long-375")
    p.goto("/requests")
    p.wait_for_selector(sel("incoming-list") + " > li")
    base_shot(p, "requests-long-note-375")
    p.context.close()

    # more than 50 items: Show more, nothing silently truncated, newest first
    s2.reset(fixture(users=[user("ada", 10 ** 9), user("bob", 10 ** 9), user("cy", 0)]))
    ada, bob = s2.token("ada"), s2.token("bob")
    for i in range(120):
        s2.call("POST", "/payments", {"to_handle": "bob", "amount": 1 + i}, token=ada, key=uuid.uuid4().hex)
    for i in range(60):
        s2.call("POST", "/requests", {"payer_handle": "ada", "amount": 1 + i}, token=bob, key=uuid.uuid4().hex)
        s2.call("POST", "/authorizations", {"to_handle": "bob", "amount": 1 + i}, token=ada, key=uuid.uuid4().hex)
    api_ids = [x["payment_id"] for x in s2.call("GET", "/activity?limit=200", token=ada)[1]["payments"]]
    p = cx.page()
    cx.login(p)
    p.wait_for_selector(sel("activity-more"))
    count = lambda: len(p.query_selector_all(f"{sel('activity-list')} > li"))
    check(count() == 50, "feed shows 50 then offers Show more", count())
    p.click(sel("activity-more"))
    p.wait_for_function("n => document.querySelectorAll(\"[data-testid='activity-list'] > li\").length === n", arg=100)
    p.click(sel("activity-more"))
    p.wait_for_function("n => document.querySelectorAll(\"[data-testid='activity-list'] > li\").length === n", arg=120)
    dom = [e.replace("activity-item-", "") for e in p.eval_on_selector_all(f"{sel('activity-list')} > li", "els => els.map(e => e.dataset.testid)")]
    check(dom == api_ids and p.query_selector(sel("activity-more")) is None, "all 120 payments shown newest first; Show more disappears")
    p.goto("/requests")
    p.wait_for_selector(sel("requests-more"))
    p.click(sel("requests-more"))
    p.wait_for_function("() => document.querySelectorAll(\"[data-testid^='request-item-']\").length === 60")
    check(p.query_selector(sel("requests-more")) is None, "requests: Show more reaches all 60")
    p.goto("/authorizations")
    p.wait_for_selector(sel("authorizations-more"))
    p.click(sel("authorizations-more"))
    p.wait_for_function("() => document.querySelectorAll(\"[data-testid^='authorization-item-']\").length === 60")
    check(p.query_selector(sel("authorizations-more")) is None, "holds: Show more reaches all 60")
    p.context.close()


def resilience(cx):
    s2 = cx.s2
    s2.reset(fixture())
    p = cx.page()
    cx.login(p)
    # a failed refresh keeps the numbers and says so
    p.route("**/me", lambda r: r.abort())
    p.click(sel("wallet-refresh"))
    p.wait_for_selector(sel("refresh-error"))
    check(amount_of(p, "wallet-balance") == "10000", "failed refresh keeps the last numbers with a note")
    base_shot(p, "wallet-refresh-failed-1280")
    p.unroute("**/me")
    p.click(f"{sel('refresh-error')} button")
    p.wait_for_selector(sel("refresh-error"), state="detached")
    check(True, "retry from the note clears it")
    # back / forward and titles
    p.click(".nav a[href='/requests']")
    p.wait_for_selector(sel("incoming-list"), state="attached")
    check(p.title() == "Requests · Pocketful", "title on /requests", p.title())
    p.go_back()
    p.wait_for_selector(sel("wallet-available"))
    check(p.title() == "Wallet · Pocketful", "back returns to the wallet", p.title())
    p.go_forward()
    p.wait_for_selector(sel("incoming-list"), state="attached")
    check(True, "forward returns to requests")
    # session ends (state reset wipes tokens) -> clean redirect with a notice
    s2.reset(fixture())
    p.goto("/")
    p.wait_for_url("**/login")
    check(p.query_selector(sel("current-user")) is None, "invalid token at load goes to /login")
    cx.login(p)
    s2.reset(fixture())
    p.click(sel("wallet-refresh"))
    p.wait_for_url("**/login")
    p.wait_for_selector(sel("session-notice"))
    check(True, "401 during use: redirect to /login with a notice")
    base_shot(p, "login-session-ended-1280")
    # server unreachable at initial load
    cx.login(p)
    p.route("**/me", lambda r: r.abort())
    p.goto("/")
    p.wait_for_selector(sel("load-error"))
    base_shot(p, "wallet-unreachable-1280")
    p.unroute("**/me")
    p.click(f"{sel('load-error')} button")
    p.wait_for_selector(sel("wallet-available"))
    check(True, "unreachable at load: friendly error, retry recovers")
    p.context.close()

    # keyboard only
    p = cx.page(reduced=True)
    cx.login(p)
    p.wait_for_selector(sel("pay-handle"))
    p.focus(sel("pay-handle"))
    p.keyboard.type("bob")
    p.keyboard.press("Tab")
    check(p.evaluate("document.activeElement.dataset.testid") == "pay-amount", "Tab: handle -> amount")
    p.keyboard.type("1.50")
    p.keyboard.press("Tab")
    check(p.evaluate("document.activeElement.dataset.testid") == "pay-visibility", "Tab: amount -> visibility")
    p.keyboard.press("Tab")
    check(p.evaluate("document.activeElement.dataset.testid") == "pay-note", "Tab: visibility -> note")
    p.keyboard.type("keyboard")
    p.keyboard.press("Enter")
    p.wait_for_selector(sel("pay-success"))
    check(p.evaluate("!!document.activeElement.closest('form')"), "focus stays in the form after submitting",
          p.evaluate("document.activeElement.outerHTML.slice(0, 80)"))
    p.fill(sel("pay-amount"), "15.005")
    p.keyboard.press("Enter")
    p.wait_for_selector(sel("pay-error"))
    check(p.get_attribute(sel("pay-error"), "role") == "alert", "refusals are announced (role=alert)")
    check(p.query_selector(sel("pay-success")) is None, "only one message at a time")
    dur = p.evaluate("""() => { const s = document.createElement('span'); s.className = 'spinner'; document.body.append(s);
        const d = getComputedStyle(s).animationDuration; s.remove(); return d; }""")
    check(dur in ("1e-06s", "0.000001s", "0s"), "prefers-reduced-motion stops the spinner animation", dur)
    p.context.close()


def base_shot(p, name):
    base.SHOTS.mkdir(parents=True, exist_ok=True)
    p.screenshot(path=str(base.SHOTS / f"{name}.png"), full_page=True)


def main():
    s2 = base.Service("pocketful-stage4")  # the service under test
    s1 = base.Service("pocketful-stage1")
    px = Proxy(ui=s2.base, api=s2.base)
    try:
        with sync_playwright() as pw:
            cx = Ctx(pw, px, s2, s1)
            for scenario in (latest_refresh_wins, competing_clients, uncertain_pay, uncertain_other_writes, upgrade, edges, resilience):
                try:
                    scenario(cx)
                except Exception as e:
                    check(False, f"{scenario.__name__} raised", repr(e)[:400])
                px.clear()
                px.api = s2.base
            check(not cx.errors, "no script errors", cx.errors[:5])
            check(not cx.foreign, "every request is same-origin", cx.foreign[:5])
            cx.browser.close()
    finally:
        s1.stop()
        s2.stop()
        px.server.shutdown()
    passed = sum(1 for ok, _ in base.results if ok)
    print(f"TOTAL {passed}/{len(base.results)} passed")
    sys.exit(0 if passed == len(base.results) else 1)


if __name__ == "__main__":
    main()
