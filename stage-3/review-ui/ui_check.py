"""Real-browser checks for the stage-2 UI (implementer's own; not the shipped tests).

Runs the built image on an internal Docker network (no outbound access), drives
Chromium with Playwright, and checks testids, exact texts, flows, layout (no horizontal
scroll at 375/768/1280 on every route), console errors, same-origin requests and colour
contrast. Saves full-page screenshots to review-ui/screens/.

    docker build -t pocketful-stage2 stage-2
    <kickoff>/.venv/bin/python stage-2/review-ui/ui_check.py [--image pocketful-stage2]
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys
import time
import urllib.request
import uuid

from playwright.sync_api import sync_playwright

HERE = pathlib.Path(__file__).resolve().parent
SHOTS = HERE / "screens"
NET = "pf-ui-internal"
VIEWPORTS = {375: (375, 812), 768: (768, 1024), 1280: (1280, 800)}
ROUTES = ["/", "/requests", "/split", "/authorizations", "/login", "/signup"]

results: list[tuple[bool, str]] = []


def check(ok: bool, name: str, detail: str = "") -> bool:
    results.append((bool(ok), name))
    print(("PASS " if ok else "FAIL ") + name + ("" if ok else f"  -> {detail}"), flush=True)
    return bool(ok)


def sel(t: str) -> str:
    return f"[data-testid='{t}']"


def user(handle, balance, name=None):
    return {"id": f"u_{handle}", "email": f"{handle}@example.com", "password": "correct horse",
            "display_name": name or handle.title(), "handle": handle, "balance": balance}


def fixture(currency="EUR", minor=2, users=None, **extra):
    f = {"currency": currency, "minor_units": minor,
         "users": users or [user("ada", 10000), user("bob", 2500), user("cy", 500)],
         "payments": [], "requests": []}
    f.update(extra)
    return f


# ---------------------------------------------------------------- service + API

class Service:
    def __init__(self, image):
        subprocess.run(["docker", "network", "create", "--internal", NET], capture_output=True)
        self.name = "pf-ui-" + uuid.uuid4().hex[:6]
        subprocess.run(["docker", "run", "-d", "--rm", "--name", self.name, "--network", NET, "--read-only",
                        "--cpus", "2", "--memory", "2g", "-e", "PORT=8080", image], check=True, capture_output=True)
        ip = subprocess.run(["docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", self.name],
                            check=True, capture_output=True, text=True).stdout.strip()
        self.base = f"http://{ip}:8080"
        for _ in range(100):
            try:
                urllib.request.urlopen(self.base + "/health", timeout=1)
                break
            except Exception:
                time.sleep(0.1)

    def stop(self):
        subprocess.run(["docker", "rm", "-f", self.name], capture_output=True)

    def call(self, method, path, body=None, token=None, key=None):
        req = urllib.request.Request(self.base + path, method=method,
                                     data=None if body is None else json.dumps(body).encode())
        req.add_header("Accept", "application/json")
        req.add_header("Content-Type", "application/json")
        if token:
            req.add_header("Authorization", "Bearer " + token)
        if key:
            req.add_header("Idempotency-Key", key)
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                raw = r.read()
                return r.status, (json.loads(raw) if raw else None)
        except urllib.error.HTTPError as e:
            raw = e.read()
            return e.code, (json.loads(raw) if raw else None)

    def reset(self, f):
        assert self.call("POST", "/_test/reset", f)[0] == 204

    def token(self, handle):
        return self.call("POST", "/auth/login", {"email": f"{handle}@example.com", "password": "correct horse"})[1]["token"]


# ---------------------------------------------------------------- contrast

def _lin(c):
    c = c / 255
    return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4


def lum(rgb):
    r, g, b = rgb
    return 0.2126 * _lin(r) + 0.7152 * _lin(g) + 0.0722 * _lin(b)


def ratio(a, b):
    la, lb = sorted([lum(a), lum(b)], reverse=True)
    return (la + 0.05) / (lb + 0.05)


def rgb(css: str):
    m = re.match(r"rgba?\((\d+),\s*(\d+),\s*(\d+)", css)
    return tuple(int(x) for x in m.groups()) if m else None


# ---------------------------------------------------------------- browser helpers

class Run:
    def __init__(self, pw, svc):
        self.svc = svc
        self.browser = pw.chromium.launch(args=[f"--unsafely-treat-insecure-origin-as-secure={svc.base}"])
        self.console_errors: list[str] = []
        self.foreign: list[str] = []
        self.posts: list[str] = []

    def page(self, width=1280, height=800):
        ctx = self.browser.new_context(base_url=self.svc.base, viewport={"width": width, "height": height})
        p = ctx.new_page()
        p.set_default_timeout(8000)
        # expected 4xx API answers are logged by Chromium as "Failed to load resource"; they
        # are not script errors. Script errors and 5xx responses are what we fail on.
        p.on("console", lambda m: m.type == "error" and not m.text.startswith("Failed to load resource")
             and self.console_errors.append(m.text))
        p.on("response", lambda r: r.status >= 500 and self.console_errors.append(f"5xx {r.status} {r.url}"))
        p.on("pageerror", lambda e: self.console_errors.append(str(e)))

        def on_request(r):
            if not r.url.startswith(self.svc.base) and not r.url.startswith("data:"):
                self.foreign.append(r.url)
            if r.method == "POST":
                self.posts.append(r.url.replace(self.svc.base, ""))
        p.on("request", on_request)
        return p

    def login(self, p, handle="ada"):
        p.goto("/login")
        p.fill(sel("login-email"), f"{handle}@example.com")
        p.fill(sel("login-password"), "correct horse")
        p.click(sel("login-submit"))
        p.wait_for_selector(sel("current-user"))

    def shot(self, p, name):
        SHOTS.mkdir(parents=True, exist_ok=True)
        p.screenshot(path=str(SHOTS / f"{name}.png"), full_page=True)

    def no_hscroll(self, p, label):
        sw, iw = p.evaluate("[document.documentElement.scrollWidth, window.innerWidth]")
        check(sw <= iw, f"no horizontal scroll: {label}", f"scrollWidth {sw} > innerWidth {iw}")


def amount_of_avail(p):
    return p.get_attribute(sel("wallet-available"), "data-amount")


def text(p, t):
    return (p.text_content(sel(t)) or "").strip()


# ---------------------------------------------------------------- scenarios

def auth_flows(run, svc):
    svc.reset(fixture())
    p = run.page()
    p.goto("/requests")
    p.wait_for_url("**/login")
    check(True, "signed-out visit to /requests lands on /login")
    p.goto("/signup")
    p.fill(sel("signup-email"), "Dee.Ann@example.com")
    p.fill(sel("signup-display-name"), "Dee Ann")
    p.fill(sel("signup-password"), "short")
    p.click(sel("signup-submit"))
    p.wait_for_selector(sel("auth-error"))
    check(not any("/auth/signup" in x for x in run.posts), "short password refused in the form without a request")
    run.shot(p, "signup-error-1280")
    p.fill(sel("signup-password"), "correct horse")
    p.click(sel("signup-submit"))
    p.wait_for_selector(sel("current-user"))
    check("Dee Ann" in text(p, "current-user"), "current-user has the display name")
    check(text(p, "current-handle") == "dee_ann", "current-handle is exactly the handle", text(p, "current-handle"))
    check(p.url.rstrip("/") == svc.base, "signup lands on /", p.url)
    check(p.query_selector(sel("auth-error")) is None, "auth-error absent without an error")
    p.click(sel("logout-button"))
    p.wait_for_selector(sel("current-user"), state="detached")
    check(True, "logout removes current-user")
    p.goto("/login")
    p.fill(sel("login-email"), "ada@example.com")
    p.fill(sel("login-password"), "wrong password")
    p.click(sel("login-submit"))
    p.wait_for_selector(sel("auth-error"))
    check(True, "bad login shows auth-error")
    run.shot(p, "login-error-1280")
    p.context.close()


def wallet_flows(run, svc):
    svc.reset(fixture())
    p = run.page()
    run.login(p)
    p.wait_for_selector(sel("wallet-available"))
    check(text(p, "wallet-available") == "100.00 EUR" and p.get_attribute(sel("wallet-available"), "data-amount") == "10000",
          "wallet-available formatted + data-amount")
    check(text(p, "wallet-balance") == "100.00 EUR" and p.get_attribute(sel("wallet-balance"), "data-amount") == "10000",
          "wallet-balance formatted + data-amount")
    check(p.query_selector(sel("wallet-held")) is None, "wallet-held absent when nothing is held")
    big = p.evaluate("""() => { const f = s => parseFloat(getComputedStyle(document.querySelector(s)).fontSize);
        return [f("[data-testid=wallet-available]"), f("[data-testid=wallet-balance]")]; }""")
    check(big[0] > big[1] * 1.5, "available is the headline number", big)
    run.shot(p, "wallet-1280")

    before = len(run.posts)
    p.fill(sel("pay-handle"), "bob")
    p.fill(sel("pay-amount"), "15.005")
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-error"))
    check(len(run.posts) == before, "15.005 refused without a request")
    for bad in ["abc", "1e3", "-5", "0", "1,000", ""]:
        p.fill(sel("pay-amount"), bad)
        p.click(sel("pay-submit"))
        p.wait_for_selector(sel("pay-error"))
    check(len(run.posts) == before, "nonnumeric/zero/negative/exponent/separator/empty refused without a request")
    run.shot(p, "wallet-pay-error-1280")

    p.fill(sel("pay-amount"), "15.5")
    p.fill(sel("pay-note"), "<b>lunch</b> 😀")
    p.select_option(sel("pay-visibility"), "private")
    p.click(sel("pay-submit"))
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='8450']")
    check(p.query_selector(sel("pay-error")) is None, "pay-error removed after success")
    check(p.input_value(sel("pay-amount")) == "15.5" and p.input_value(sel("pay-handle")) == "bob", "pay form keeps its values")
    run.shot(p, "wallet-pay-success-1280")
    pays = [x for x in run.posts if x == "/payments"]
    p.click(sel("pay-submit"))
    p.wait_for_timeout(700)
    check(p.get_attribute(sel("wallet-balance"), "data-amount") == "8450" and p.query_selector(sel("pay-error")) is None,
          "unchanged resubmit is a replay: balance falls once, no pay-error")
    feed = svc.call("GET", "/activity", token=svc.token("ada"))[1]["payments"]
    check(len([x for x in feed if x["amount"] == 1550]) == 1, "one payment in the feed after double submit")
    pid = feed[0]["payment_id"]
    check(p.get_attribute(sel(f"activity-item-{pid}"), "data-visibility") == "private", "activity data-visibility")
    check(text(p, f"activity-amount-{pid}") == "15.50 EUR", "activity amount exact")
    check(p.text_content(sel(f"activity-note-{pid}")) == "<b>lunch</b> 😀", "note rendered verbatim as text (no HTML)")
    parties = text(p, f"activity-parties-{pid}")
    check("ada" in parties and "bob" in parties, "activity-parties has both handles", parties)

    p.fill(sel("pay-amount"), "15")
    p.click(sel("pay-submit"))
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='6950']")
    check(True, "changed field is a new payment ('15' -> 1500)")
    order = p.eval_on_selector_all(f"{sel('activity-list')} > *", "els => els.map(e => e.getAttribute('data-testid'))")
    check(order[0] != f"activity-item-{pid}", "newest first in the DOM")

    # refresh observes another client's payment; form not cleared
    p.fill(sel("pay-note"), "keep me")
    status, made = svc.call("POST", "/payments", {"to_handle": "cy", "amount": 300}, token=svc.token("ada"), key=uuid.uuid4().hex)
    p.click(sel("wallet-refresh"))
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='6650']")
    p.wait_for_selector(sel(f"activity-item-{made['payment_id']}"))
    check(p.input_value(sel("pay-note")) == "keep me", "wallet-refresh keeps the pay form")

    # authorise form on the wallet ("Reserve money")
    p.fill(sel("authorize-handle"), "bob")
    p.fill(sel("authorize-amount"), "10.00")
    p.click(sel("authorize-submit"))
    p.wait_for_selector(sel("authorize-success"))
    p.wait_for_selector(f"{sel('wallet-held')}[data-amount='1000']")
    check(amount_of_avail(p) == str(6650 - 1000), "authorize from the wallet: available and held refresh")
    p.fill(sel("authorize-amount"), "99999.00")
    p.click(sel("authorize-submit"))
    p.wait_for_selector(sel("authorize-error"))
    check("insufficient_funds" not in (p.text_content(sel("authorize-error")) or "")
          and p.get_attribute(sel("authorize-error"), "data-code") == "insufficient_funds",
          "refusal text is human; the API code is only in data-code")
    run.shot(p, "wallet-authorize-1280")

    # request form
    p.fill(sel("request-handle"), "bob")
    p.fill(sel("request-amount"), "12.00")
    p.fill(sel("request-note"), "taxi")
    p.click(sel("request-submit"))
    p.wait_for_selector(sel("request-success"))
    p.fill(sel("request-handle"), "nobody")
    p.click(sel("request-submit"))
    p.wait_for_selector(sel("request-error"))
    check(True, "request form: success and refusal")
    p.context.close()

    # insufficient funds as cy keeps inputs
    p = run.page()
    run.login(p, "cy")
    p.fill(sel("pay-handle"), "bob")
    p.fill(sel("pay-amount"), "99.00")
    p.fill(sel("pay-note"), "too much")
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-error"))
    check(p.input_value(sel("pay-amount")) == "99.00" and p.input_value(sel("pay-note")) == "too much",
          "refused payment preserves inputs")
    run.shot(p, "wallet-insufficient-1280")
    p.context.close()


def requests_flows(run, svc):
    svc.reset(fixture())
    p = run.page()
    run.login(p, "cy")
    p.goto("/requests")
    p.wait_for_selector(sel("empty-requests"))
    check(p.query_selector(sel("incoming-list")) is not None, "lists present with empty-requests")
    run.shot(p, "requests-empty-1280")
    p.context.close()

    bob = svc.token("bob")
    ids = [svc.call("POST", "/requests", {"payer_handle": "ada", "amount": a, "note": n}, token=bob, key=uuid.uuid4().hex)[1]["request_id"]
           for a, n in [(1200, "taxi"), (300, "coffee"), (90000, "rent")]]
    ada = svc.token("ada")
    out = svc.call("POST", "/requests", {"payer_handle": "bob", "amount": 100}, token=ada, key=uuid.uuid4().hex)[1]["request_id"]
    p = run.page()
    run.login(p)
    p.goto("/requests")
    p.wait_for_selector(sel(f"request-item-{ids[0]}"))
    check(text(p, f"request-amount-{ids[0]}") == "12.00 EUR", "request amount exact")
    check(p.query_selector(sel(f"request-pay-{out}")) is None and p.query_selector(sel(f"request-cancel-{out}")) is not None,
          "outgoing pending: cancel only")
    check(p.query_selector(sel(f"request-cancel-{ids[0]}")) is None, "incoming pending: no cancel")
    run.shot(p, "requests-1280")
    p.click(sel(f"request-pay-{ids[0]}"))
    p.wait_for_selector(f"{sel('request-item-' + ids[0])}[data-status='paid']")
    check(p.query_selector(sel(f"request-pay-{ids[0]}")) is None, "pay button gone after paying")
    p.click(sel(f"request-decline-{ids[1]}"))
    p.wait_for_selector(f"{sel('request-item-' + ids[1])}[data-status='declined']")
    p.click(sel(f"request-pay-{ids[2]}"))
    p.wait_for_selector(sel("request-error"))
    check(True, "unaffordable request shows request-error")
    run.shot(p, "requests-error-1280")
    # cancelled elsewhere while the pay button is visible
    rq = svc.call("POST", "/requests", {"payer_handle": "ada", "amount": 50}, token=bob, key=uuid.uuid4().hex)[1]["request_id"]
    p.goto("/requests")
    p.wait_for_selector(sel(f"request-pay-{rq}"))
    svc.call("POST", f"/requests/{rq}/cancel", token=bob)
    p.click(sel(f"request-pay-{rq}"))
    p.wait_for_selector(sel("request-error"))
    p.wait_for_selector(sel(f"request-pay-{rq}"), state="detached")
    check(True, "stale pay: request-error and the button disappears")
    p.click(sel(f"request-cancel-{out}"))
    p.wait_for_selector(f"{sel('request-item-' + out)}[data-status='cancelled']")
    check(True, "cancel outgoing request")
    p.context.close()


def split_flows(run, svc):
    for cur, mu, typed, expect, total in [("EUR", 2, "10.00", ["3.34 EUR", "3.33 EUR", "3.33 EUR"], 1000),
                                          ("JPY", 0, "10", ["4 JPY", "3 JPY", "3 JPY"], 10),
                                          ("BHD", 3, "1", ["0.334 BHD", "0.333 BHD", "0.333 BHD"], 1000)]:
        svc.reset(fixture(cur, mu))
        p = run.page()
        run.login(p)
        p.goto("/split")
        p.fill(sel("split-amount"), typed)
        p.fill(sel("split-handles"), "ada, bob,cy")
        p.wait_for_selector(sel("split-preview"))
        got = [text(p, f"split-share-{x}") for x in ("ada", "bob", "cy")]
        check(got == expect, f"split preview {cur} {typed}/3", got)
        p.click(sel("split-submit"))
        p.wait_for_selector(sel("split-success"))
        reqs = svc.call("GET", "/requests", token=svc.token("bob"))[1]["requests"]
        shares = {"EUR": [334, 333, 333], "JPY": [4, 3, 3], "BHD": [334, 333, 333]}[cur]
        check(len(reqs) == 1 and reqs[0]["amount"] == shares[1], f"server share equals preview ({cur})", reqs)
        if cur == "EUR":
            run.shot(p, "split-success-1280")
            p.fill(sel("split-handles"), "ada,nobody")
            p.click(sel("split-submit"))
            p.wait_for_selector(sel("split-error"))
            check(True, "split-error on unknown handle")
            p.fill(sel("split-handles"), "cy,bob,ada")
            check(text(p, "split-share-cy") == "3.34 EUR", "preview follows handle order")
            run.shot(p, "split-error-1280")
        p.context.close()


def hold_flows(run, svc):
    from datetime import datetime, timedelta, timezone
    exp = (datetime.now(timezone.utc) + timedelta(hours=2)).strftime("%Y-%m-%dT%H:%M:%S+00:00")
    svc.reset(fixture(authorizations=[{"id": "a_seed", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 2000,
                                       "note": "deposit", "visibility": "public", "status": "open", "expires_at": exp}]))
    p = run.page()
    run.login(p)
    p.wait_for_selector(sel("wallet-held"))
    check(text(p, "wallet-available") == "80.00 EUR" and text(p, "wallet-held") == "20.00 EUR" and text(p, "wallet-balance") == "100.00 EUR",
          "seeded hold shown right after reset (available/held/total)")
    run.shot(p, "wallet-with-hold-1280")
    p.goto("/authorizations")
    p.wait_for_selector(sel("authorization-item-a_seed"))
    check(text(p, "authorization-expires-a_seed") == exp, "authorization-expires is the RFC 3339 text")
    check(p.query_selector(sel("authorization-void-a_seed")) is not None and p.query_selector(sel("authorization-capture-a_seed")) is None,
          "outgoing open: void only")
    p.fill(sel("authorize-handle"), "cy")
    p.fill(sel("authorize-amount"), "90.00")
    p.click(sel("authorize-submit"))
    p.wait_for_selector(sel("authorize-error"))
    check(True, "authorize-error on insufficient available funds")
    p.fill(sel("authorize-amount"), "30")
    p.click(sel("authorize-submit"))
    p.wait_for_selector(sel("authorize-success"))
    p.wait_for_selector(f"{sel('wallet-available')}[data-amount='5000']")
    check(text(p, "wallet-held") == "50.00 EUR", "new hold reflected in held")
    run.shot(p, "holds-1280")
    p.context.close()

    p = run.page()
    run.login(p, "bob")
    p.goto("/authorizations")
    p.wait_for_selector(sel("authorization-capture-a_seed"))
    check(p.input_value(sel("authorization-capture-amount-a_seed")) == "20.00", "capture amount pre-filled with remaining")
    p.fill(sel("authorization-capture-amount-a_seed"), "15.00")
    p.click(sel("authorization-capture-a_seed"))
    p.wait_for_selector(f"{sel('authorization-item-a_seed')}[data-status='captured']")
    check(text(p, "authorization-captured-a_seed") == "15.00 EUR", "authorization-captured shown when captured")
    check(p.query_selector(sel("authorization-capture-a_seed")) is None, "no capture button once captured")
    run.shot(p, "holds-captured-1280")
    p.context.close()

    p = run.page()
    run.login(p)
    p.goto("/authorizations")
    items = p.eval_on_selector_all(f"{sel('authorization-list')} > *", "els => els.map(e => e.getAttribute('data-testid'))")
    new_id = items[0].replace("authorization-item-", "")
    p.click(sel(f"authorization-void-{new_id}"))
    p.wait_for_selector(f"{sel('authorization-item-' + new_id)}[data-status='voided']")
    p.wait_for_selector(f"{sel('wallet-available')}[data-amount='8500']")
    check(p.query_selector(sel("wallet-held")) is None, "void releases the hold; wallet-held disappears at zero")
    p.context.close()

    svc.reset(fixture())
    p = run.page()
    run.login(p, "cy")
    p.goto("/authorizations")
    p.wait_for_selector(sel("empty-authorizations"))
    check(True, "empty-authorizations")
    run.shot(p, "holds-empty-1280")
    p.context.close()


def states(run, svc):
    svc.reset(fixture())
    p = run.page()
    run.login(p)
    # loading: hold the activity read
    p.route("**/activity*", lambda r: (time.sleep(1.5), r.continue_()))
    p.goto("/")
    p.wait_for_selector(sel("wallet-available"))
    run.shot(p, "wallet-loading-1280")
    p.unroute("**/activity*")
    p.wait_for_selector(sel("empty-activity"))
    run.shot(p, "wallet-empty-1280")
    # uncertain: the response is lost after the server commits
    def lose(route):
        route.fetch()
        route.abort()
    p.route("**/payments", lose)
    p.fill(sel("pay-handle"), "bob")
    p.fill(sel("pay-amount"), "5.00")
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-uncertain"))
    check(p.query_selector(sel("pay-error")) is None and text(p, "pay-uncertain") != "", "lost response shows pay-uncertain, not pay-error")
    run.shot(p, "wallet-uncertain-1280")
    p.unroute("**/payments")
    p.click(sel("pay-submit"))
    p.wait_for_selector(f"{sel('wallet-balance')}[data-amount='9500']")
    check(p.query_selector(sel("pay-uncertain")) is None and p.query_selector(sel("pay-error")) is None,
          "retry with same key recovers: money moved once, messages cleared")
    p.context.close()


def layout(run, svc):
    svc.reset(fixture(authorizations=[]))
    tok = svc.token("ada")
    bob = svc.token("bob")
    for i in range(3):
        svc.call("POST", "/payments", {"to_handle": "bob", "amount": 1234 + i, "note": "Groceries for the week at the market" if i else "",
                                       "visibility": "private" if i == 1 else "public"}, token=tok, key=uuid.uuid4().hex)
    svc.call("POST", "/requests", {"payer_handle": "ada", "amount": 4200, "note": "Concert tickets"}, token=bob, key=uuid.uuid4().hex)
    svc.call("POST", "/requests", {"payer_handle": "bob", "amount": 999, "note": "Taxi"}, token=tok, key=uuid.uuid4().hex)
    svc.call("POST", "/authorizations", {"to_handle": "bob", "amount": 2500, "note": "Hotel deposit"}, token=tok, key=uuid.uuid4().hex)
    svc.call("POST", "/authorizations", {"to_handle": "ada", "amount": 700, "note": "Bike rental"}, token=bob, key=uuid.uuid4().hex)
    for w, (vw, vh) in VIEWPORTS.items():
        p = run.page(vw, vh)
        run.login(p)
        for route in ROUTES:
            p.goto(route)
            anchor = {"/": "pay-submit", "/requests": "incoming-list", "/split": "split-submit", "/authorizations": "authorize-submit",
                      "/login": "login-submit", "/signup": "signup-submit"}[route]
            p.wait_for_selector(sel(anchor), state="attached")
            p.wait_for_load_state("networkidle")
            if route == "/split":
                p.fill(sel("split-amount"), "100.00")
                p.fill(sel("split-handles"), "ada,bob,cy")
            run.no_hscroll(p, f"{route} @ {vw}px")
            unlabeled = p.evaluate("""() => [...document.querySelectorAll('input,select,textarea')].filter(e => {
                if (e.type === 'hidden' || e.getClientRects().length === 0) return false;
                const l = e.id && document.querySelector('label[for="' + CSS.escape(e.id) + '"]');
                if (!l) return true;
                const r = l.getBoundingClientRect();
                return r.width < 2 || r.height < 2 || getComputedStyle(l).visibility === 'hidden'; }).map(e => e.outerHTML.slice(0, 90))""")
            check(not unlabeled, f"every visible input has a visible label on {route} @ {vw}px", unlabeled)
            check(p.query_selector(sel("current-user")) is not None, f"current-user on {route} @ {vw}px")
            name = (route.strip("/") or "wallet")
            run.shot(p, f"{name}-{vw}")
        # every input has a visible label
        p.goto("/")
        p.wait_for_selector(sel("pay-submit"))
        unlabeled = p.evaluate("""() => [...document.querySelectorAll('input,select')].filter(e => {
            const l = e.id && document.querySelector('label[for="' + e.id + '"]');
            return !(l || e.getAttribute('aria-label'));}).map(e => e.outerHTML)""")
        check(not unlabeled, f"every input labelled @ {vw}px", unlabeled)
        small = p.evaluate("""() => [...document.querySelectorAll('button, .nav a, input, select')].filter(e => {
            const r = e.getBoundingClientRect(); return r.width > 0 && r.height < 44; }).map(e => e.outerHTML.slice(0, 80))""")
        check(not small, f"touch targets >= 44px tall @ {vw}px", small)
        p.context.close()


def contrast(run, svc):
    svc.reset(fixture(authorizations=[]))
    p = run.page()
    run.login(p)
    p.fill(sel("pay-handle"), "nobody")
    p.fill(sel("pay-amount"), "1")
    p.click(sel("pay-submit"))
    p.wait_for_selector(sel("pay-error"))
    pairs = p.evaluate("""() => {
      const cs = (el) => getComputedStyle(el);
      // nearest painted background; for a gradient, its lightest stop (the worst case for light text)
      const bg = (el) => { while (el) { const st = cs(el); const g = st.backgroundImage.match(/rgba?\([^)]*\)/g);
          if (g && st.backgroundImage.includes('gradient')) return g.join('|');
          const c = st.backgroundColor; if (c && !c.endsWith(', 0)') && c !== 'transparent') return c; el = el.parentElement; } return 'rgb(255, 255, 255)'; };
      const q = (s) => document.querySelector(s);
      const out = {};
      const add = (name, el, fgProp) => { if (el) out[name] = [cs(el)[fgProp || 'color'], bg(el)]; };
      add('headline available on balance card', q('[data-testid=wallet-available]'));
      add('secondary label on balance card', q('.balance-meta dt'));
      add('secondary text (muted)', q('.page-head p'));
      add('primary button', q('[data-testid=pay-submit]'));
      add('secondary button', q('[data-testid=request-submit]'));
      add('error message', q('[data-testid=pay-error]'));
      add('nav link', q('.nav a:not([aria-current])'));
      add('active nav link', q('.nav a[aria-current]'));
      add('field label', q('label'));
      add('hint text', q('.card-head p'));
      const inp = q('[data-testid=pay-note]');
      out['input border (UI component)'] = [cs(inp).borderTopColor, bg(inp.parentElement)];
      return out; }""")
    for name, (fg, bgc) in pairs.items():
        need = 3.0 if "UI component" in name else 4.5
        r = min(ratio(rgb(fg), rgb(b)) for b in bgc.split("|"))
        check(r >= need, f"contrast {name}: {r:.2f}:1 (need {need})", f"{fg} on {bgc}")
    # success / uncertain message colours from the stylesheet tokens
    tokens = p.evaluate("""() => { const s = getComputedStyle(document.documentElement);
        return ['--success','--success-bg','--uncertain','--uncertain-bg','--held','--surface','--pending','--pending-bg','--danger','--danger-bg']
          .map(k => s.getPropertyValue(k).trim()); }""")
    hexes = [tuple(int(t[i:i + 2], 16) for i in (1, 3, 5)) for t in tokens]
    for name, fg, bgc in [("success message", 0, 1), ("uncertain message", 2, 3), ("held text", 4, 5), ("pending badge", 6, 7), ("danger message", 8, 9)]:
        r = ratio(hexes[fg], hexes[bgc])
        check(r >= 4.5, f"contrast {name}: {r:.2f}:1 (need 4.5)", tokens)
    # keyboard focus ring visible
    p.focus(sel("pay-submit"))
    p.keyboard.press("Shift+Tab")
    p.keyboard.press("Tab")
    ring = p.evaluate("() => { const e = document.activeElement; const s = getComputedStyle(e); return [e.dataset.testid, s.outlineStyle, s.outlineWidth]; }")
    check(ring[1] != "none" and ring[2] not in ("0px", ""), "focus-visible ring on keyboard focus", ring)
    p.context.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image", default="pocketful-stage3")
    args = ap.parse_args()
    svc = Service(args.image)
    try:
        status, _ = svc.call("GET", "/health")
        check(status == 200, "service healthy on an internal (no outbound) network")
        with sync_playwright() as pw:
            run = Run(pw, svc)
            for scenario in (auth_flows, wallet_flows, requests_flows, split_flows, hold_flows, states, layout, contrast):
                try:
                    scenario(run, svc)
                except Exception as e:  # report and continue with the next scenario
                    check(False, f"{scenario.__name__} raised", repr(e)[:300])
            check(not run.console_errors, "no console errors", run.console_errors[:5])
            check(not run.foreign, "every request is same-origin", run.foreign[:5])
            run.browser.close()
    finally:
        svc.stop()
    passed = sum(1 for ok, _ in results if ok)
    print(f"TOTAL {passed}/{len(results)} passed")
    sys.exit(0 if passed == len(results) else 1)


if __name__ == "__main__":
    main()
