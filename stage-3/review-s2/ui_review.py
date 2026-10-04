#!/usr/bin/env python3
"""Reviewer browser checks for Pocketful stage 2, written from the stage-2 requirements text.
Playwright (sync) Chromium. Every element is found by data-testid.

Usage: <kickoff>/.venv/bin/python ui_review.py <stage2-url> <stage1-url> <screens-dir>
"""
import os, sys, json, re, time, uuid, datetime as dt, threading
import urllib.request
from playwright.sync_api import sync_playwright, expect

S2, S1, SHOTS = sys.argv[1], sys.argv[2], sys.argv[3]
os.makedirs(SHOTS, exist_ok=True)
RESULTS = []
expect.set_options(timeout=6000)


def check(name, cond, detail=""):
    RESULTS.append((name, bool(cond)))
    print(("PASS " if cond else "FAIL ") + name + ("" if cond else "  -> " + str(detail)[:300]), flush=True)


def api(method, path, body=None, token=None, key=None, base=S2):
    req = urllib.request.Request(base + path, method=method, data=json.dumps(body).encode() if body is not None else None)
    req.add_header("Content-Type", "application/json")
    if token: req.add_header("Authorization", "Bearer " + token)
    if key: req.add_header("Idempotency-Key", key)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            t = r.read().decode(); return r.status, (json.loads(t) if t else None)
    except urllib.error.HTTPError as e:
        t = e.read().decode(); return e.code, (json.loads(t) if t else None)


def k(): return uuid.uuid4().hex
def sel(t): return f"[data-testid='{t}']"
def iso(s): return (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=s)).replace(microsecond=0).isoformat()


def money(minor, mu=2, cur="EUR"):
    if mu == 0: return f"{minor} {cur}"
    t = str(minor).rjust(mu + 1, "0"); return f"{t[:-mu]}.{t[-mu:]} {cur}"


def user(h, bal, name=None):
    return {"id": "u_" + h, "email": h + "@example.com", "password": "correct horse", "display_name": name or h.title(), "handle": h, "balance": bal}


def fixture(mu=2, cur="EUR", users=None, auths=None, **extra):
    f = {"currency": cur, "minor_units": mu, "users": users or [user("ada", 10000), user("bob", 2500), user("cy", 0)],
         "payments": [], "requests": []}
    if auths is not None: f["authorizations"] = auths
    f.update(extra); return f


def reset(f, base=S2):
    s, b = api("POST", "/_test/reset", f, base=base); assert s == 204, (s, b)


def token(h, base=S2):
    return api("POST", "/auth/login", {"email": h + "@example.com", "password": "correct horse"}, base=base)[1]["token"]


def me(h, base=S2):
    return api("GET", "/me", token=token(h, base), base=base)[1]


class Ctx:
    """One browser page with request/console/origin bookkeeping."""
    def __init__(self, browser, w=1280, hgt=800):
        self.ctx = browser.new_context(viewport={"width": w, "height": hgt}, base_url=S2)
        self.page = self.ctx.new_page()
        self.reqs, self.console, self.foreign = [], [], []
        self.page.on("request", self._req)
        self.page.on("console", lambda m: self.console.append(m.text) if m.type == "error" else None)
        self.page.on("pageerror", lambda e: self.console.append("pageerror " + str(e)))

    def _req(self, r):
        self.reqs.append(r)
        if not r.url.startswith(S2) and not r.url.startswith("data:"):
            self.foreign.append(r.url)

    def posts(self, path):
        return [r for r in self.reqs if r.method == "POST" and r.url.split("?")[0].endswith(path)]

    def login(self, h="ada"):
        p = self.page
        p.goto("/login")
        p.fill(sel("login-email"), h + "@example.com"); p.fill(sel("login-password"), "correct horse")
        p.click(sel("login-submit")); p.wait_for_selector(sel("current-user"))
        p.wait_for_selector(sel("wallet-available"))

    def close(self): self.ctx.close()


def text(p, t): return p.locator(sel(t)).inner_text().strip()
def count(p, t): return p.locator(sel(t)).count()


def no_hscroll(p):
    return p.evaluate("() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 0")


# ---------------------------------------------------------------- checks

def t_auth(b):
    reset(fixture())
    c = Ctx(b); p = c.page
    p.goto("/login")
    check("login: auth-error absent before error", count(p, "auth-error") == 0)
    p.fill(sel("login-email"), "ada@example.com"); p.fill(sel("login-password"), "wrong pass"); p.click(sel("login-submit"))
    p.wait_for_selector(sel("auth-error")); check("bad login shows auth-error", True)
    p.goto("/signup")
    check("signup inputs present", all(count(p, t) == 1 for t in ("signup-email", "signup-password", "signup-display-name", "signup-submit")))
    p.fill(sel("signup-email"), "Zoë.Q+x@example.com"); p.fill(sel("signup-password"), "correct horse"); p.fill(sel("signup-display-name"), "Zoë Q")
    p.click(sel("signup-submit")); p.wait_for_selector(sel("current-user"))
    check("signup → current-user contains display name", "Zoë Q" in text(p, "current-user"), text(p, "current-user"))
    check("current-handle exact derived handle", p.locator(sel("current-handle")).text_content() == "zo__q_x", repr(p.locator(sel("current-handle")).text_content()))
    for route in ("/", "/requests", "/split", "/authorizations"):
        p.goto(route); p.wait_for_selector(sel("current-user"))
        check(f"current-user visible on {route}", p.locator(sel("current-user")).is_visible() and p.locator(sel("logout-button")).is_visible())
    p.click(sel("logout-button")); p.wait_for_url(re.compile(r".*/login$"))
    check("logout returns to login, no current-user", count(p, "current-user") == 0)
    p.goto("/signup"); p.fill(sel("signup-email"), "ada@example.com"); p.fill(sel("signup-password"), "correct horse"); p.fill(sel("signup-display-name"), "x")
    p.click(sel("signup-submit")); p.wait_for_selector(sel("auth-error")); check("signup email taken → auth-error", True)
    check("auth: no console errors", not [x for x in c.console if "401" not in x and "409" not in x], c.console)
    c.close()


def t_wallet_format(b):
    for mu, cur, bal, held in ((2, "EUR", 10000, 2000), (0, "JPY", 1200, 0), (3, "BHD", 12345, 5)):
        auths = [{"id": "a_s", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": held, "status": "open", "expires_at": iso(7200)}] if held else []
        reset(fixture(mu, cur, users=[user("ada", bal), user("bob", 0)], auths=auths))
        c = Ctx(b); p = c.page; c.login()
        bal_el = p.locator(sel("wallet-balance")); av = p.locator(sel("wallet-available"))
        check(f"{cur}: wallet-balance text {money(bal, mu, cur)!r}", bal_el.inner_text().strip() == money(bal, mu, cur) and bal_el.get_attribute("data-amount") == str(bal), bal_el.inner_text())
        check(f"{cur}: wallet-available {money(bal - held, mu, cur)!r}", av.inner_text().strip() == money(bal - held, mu, cur) and av.get_attribute("data-amount") == str(bal - held), av.inner_text())
        if held:
            hl = p.locator(sel("wallet-held"))
            check(f"{cur}: wallet-held shown with data-amount", hl.count() == 1 and hl.inner_text().strip() == money(held, mu, cur) and hl.get_attribute("data-amount") == str(held))
            fa = float(av.evaluate("e => parseFloat(getComputedStyle(e).fontSize)")); fb = float(bal_el.evaluate("e => parseFloat(getComputedStyle(e).fontSize)"))
            fh = float(hl.evaluate("e => parseFloat(getComputedStyle(e).fontSize)"))
            check(f"{cur}: available is the headline (font {fa}px vs total {fb}px, held {fh}px)", fa >= 1.5 * max(fb, fh))
            p.screenshot(path=f"{SHOTS}/wallet-holds-{cur}-1280.png", full_page=True)
        else:
            check(f"{cur}: wallet-held absent at zero", count(p, "wallet-held") == 0)
        c.close()


def t_pay_parsing(b):
    reset(fixture())
    c = Ctx(b); p = c.page; c.login()
    cases = [("15", 1500), ("15.5", 1550), ("15.00", 1500), ("15.005", None), ("abc", None), ("1,5", None), ("-3", None), ("1e3", None)]
    for typed, minor in cases:
        n0 = len(c.posts("/payments"))
        p.fill(sel("pay-handle"), "bob"); p.fill(sel("pay-amount"), typed); p.fill(sel("pay-note"), f"t{typed}")
        p.click(sel("pay-submit"))
        if minor is None:
            p.wait_for_selector(sel("pay-error")); time.sleep(0.2)
            check(f"pay-amount {typed!r} rejected in form, no request", len(c.posts("/payments")) == n0, len(c.posts("/payments")) - n0)
        else:
            p.wait_for_selector(sel("pay-success"), timeout=6000) if count(p, "pay-success") == 0 else None
            p.wait_for_function("n => true", arg=None); time.sleep(0.3)
            sent = c.posts("/payments")[n0:]
            body = json.loads(sent[-1].post_data) if sent else {}
            check(f"pay-amount {typed!r} submits {minor}", len(sent) == 1 and body.get("amount") == minor and isinstance(body.get("amount"), int), body)
    vals = [o.get_attribute("value") for o in p.locator(sel("pay-visibility") + " option").all()]
    check("pay-visibility option values exactly public/private", sorted(vals) == ["private", "public"], vals)
    c.close()


def t_pay_retention(b):
    reset(fixture())
    c = Ctx(b); p = c.page; c.login()
    p.fill(sel("pay-handle"), "bob"); p.fill(sel("pay-amount"), "12.34"); p.fill(sel("pay-note"), "lunch"); p.select_option(sel("pay-visibility"), "private")
    p.click(sel("pay-submit"))
    expect(p.locator(sel("wallet-balance"))).to_have_attribute("data-amount", str(10000 - 1234))
    check("pay form keeps values after success", p.input_value(sel("pay-handle")) == "bob" and p.input_value(sel("pay-amount")) == "12.34"
          and p.input_value(sel("pay-note")) == "lunch" and p.input_value(sel("pay-visibility")) == "private")
    check("balance refreshed without reload after pay", True)
    items = p.locator("[data-testid^='activity-item-']")
    check("feed shows the payment", items.count() == 1 and items.first.get_attribute("data-visibility") == "private")
    p.click(sel("pay-submit")); time.sleep(1.0)
    posts = c.posts("/payments")
    check("unchanged resubmit: same key + byte-identical body", len(posts) == 2 and posts[0].headers.get("idempotency-key") == posts[1].headers.get("idempotency-key")
          and posts[0].post_data == posts[1].post_data)
    check("unchanged resubmit: balance fell once, one feed item, no pay-error", me("ada")["total"] == 8766 and items.count() == 1 and count(p, "pay-error") == 0
          and p.locator(sel("wallet-balance")).get_attribute("data-amount") == "8766")
    p.fill(sel("pay-note"), "lunch 2"); p.click(sel("pay-submit"))
    expect(p.locator(sel("wallet-balance"))).to_have_attribute("data-amount", str(8766 - 1234))
    posts = c.posts("/payments")
    check("changed field → new key, new payment", posts[-1].headers.get("idempotency-key") != posts[0].headers.get("idempotency-key") and items.count() == 2)
    c.close()


def t_feed(b):
    f = fixture(users=[user("ada", 10000), user("bob", 2500), user("cy", 0)])
    f["payments"] = [
        {"id": "p_old", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 100, "note": "", "visibility": "public", "created_at": "2026-09-01T10:00:00+00:00"},
        {"id": "p_priv", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 200, "note": "secret", "visibility": "private", "created_at": "2026-09-02T10:00:00+00:00"},
        {"id": "p_new", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1234, "note": "dinner 🍝", "visibility": "public", "created_at": "2026-09-03T12:00:00+02:00"}]
    reset(f)
    c = Ctx(b); p = c.page; c.login("ada")
    ids = [x.get_attribute("data-testid") for x in p.locator(sel("activity-list") + " > [data-testid^='activity-item-']").all()]
    check("feed children newest first in DOM", ids == ["activity-item-p_new", "activity-item-p_old"], ids)
    check("private between others hidden from third party", count(p, "activity-item-p_priv") == 0)
    check("data-visibility attribute", p.locator(sel("activity-item-p_new")).get_attribute("data-visibility") == "public")
    pt = text(p, "activity-parties-p_new")
    check("parties contain both handles", "ada" in pt and "bob" in pt, pt)
    check("amount exact formatted", text(p, "activity-amount-p_new") == "12.34 EUR")
    check("note exact (unicode)", p.locator(sel("activity-note-p_new")).text_content() == "dinner 🍝")
    check("empty note element present with empty text", count(p, "activity-note-p_old") == 1 and p.locator(sel("activity-note-p_old")).text_content() == "")
    check("empty-activity absent when items", count(p, "empty-activity") == 0)
    c.close()
    c = Ctx(b); p = c.page; c.login("cy")
    check("bob→cy private visible to receiver", count(p, "activity-item-p_priv") == 1 and p.locator(sel("activity-item-p_priv")).get_attribute("data-visibility") == "private")
    c.close()
    reset(fixture())
    c = Ctx(b); p = c.page; c.login("cy")
    check("empty-activity shown, no list", count(p, "empty-activity") == 1 and count(p, "activity-item-") == 0)
    p.screenshot(path=f"{SHOTS}/wallet-empty-375.png") if False else None
    c.close()


def t_requests(b):
    reset(fixture())
    ta, tb = token("ada"), token("bob")
    inc = api("POST", "/requests", {"payer_handle": "ada", "amount": 1200, "note": "taxi"}, tb, k())[1]["request_id"]
    inc2 = api("POST", "/requests", {"payer_handle": "ada", "amount": 300}, tb, k())[1]["request_id"]
    big = api("POST", "/requests", {"payer_handle": "ada", "amount": 999999}, tb, k())[1]["request_id"]
    out = api("POST", "/requests", {"payer_handle": "bob", "amount": 500}, ta, k())[1]["request_id"]
    paid = api("POST", "/requests", {"payer_handle": "bob", "amount": 1}, ta, k())[1]["request_id"]
    api("POST", f"/requests/{paid}/pay", {}, tb, k())
    c = Ctx(b); p = c.page; c.login("ada"); p.goto("/requests"); p.wait_for_selector(sel(f"request-item-{inc}"))
    check("incoming/outgoing lists present", count(p, "incoming-list") == 1 and count(p, "outgoing-list") == 1)
    check("incoming item in incoming-list", p.locator(sel("incoming-list") + " " + sel(f"request-item-{inc}")).count() == 1
          and p.locator(sel("outgoing-list") + " " + sel(f"request-item-{out}")).count() == 1)
    check("data-status pending/paid", p.locator(sel(f"request-item-{inc}")).get_attribute("data-status") == "pending" and p.locator(sel(f"request-item-{paid}")).get_attribute("data-status") == "paid")
    check("request-amount exact", text(p, f"request-amount-{inc}") == "12.00 EUR")
    check("pay/decline only on pending incoming", count(p, f"request-pay-{inc}") == 1 and count(p, f"request-decline-{inc}") == 1 and count(p, f"request-cancel-{inc}") == 0)
    check("cancel only on pending outgoing", count(p, f"request-cancel-{out}") == 1 and count(p, f"request-pay-{out}") == 0 and count(p, f"request-decline-{out}") == 0)
    check("no buttons on paid", count(p, f"request-pay-{paid}") + count(p, f"request-decline-{paid}") + count(p, f"request-cancel-{paid}") == 0)
    check("request-error absent initially", count(p, "request-error") == 0)
    p.screenshot(path=f"{SHOTS}/requests-1280.png", full_page=True)
    p.click(sel(f"request-pay-{inc}"))
    expect(p.locator(sel(f"request-item-{inc}"))).to_have_attribute("data-status", "paid")
    check("pay incoming → paid, pay button gone, balance refreshed", count(p, f"request-pay-{inc}") == 0 and me("ada")["total"] == 8801
          and p.locator(sel("wallet-balance")).get_attribute("data-amount") == "8801")
    p.click(sel(f"request-decline-{inc2}")); expect(p.locator(sel(f"request-item-{inc2}"))).to_have_attribute("data-status", "declined")
    check("decline → declined, buttons gone", count(p, f"request-decline-{inc2}") == 0)
    p.click(sel(f"request-cancel-{out}")); expect(p.locator(sel(f"request-item-{out}"))).to_have_attribute("data-status", "cancelled")
    check("cancel → cancelled", count(p, f"request-cancel-{out}") == 0)
    p.click(sel(f"request-pay-{big}")); p.wait_for_selector(sel("request-error"))
    check("insufficient pay → request-error, still pending", p.locator(sel(f"request-item-{big}")).get_attribute("data-status") == "pending")
    # cancelled elsewhere while pay button visible
    stale = api("POST", "/requests", {"payer_handle": "ada", "amount": 100}, tb, k())[1]["request_id"]
    p.click(sel("wallet-refresh")) if count(p, "wallet-refresh") else None
    p.goto("/requests"); p.wait_for_selector(sel(f"request-pay-{stale}"))
    api("POST", f"/requests/{stale}/cancel", None, tb)
    p.click(sel(f"request-pay-{stale}")); p.wait_for_selector(sel("request-error"))
    expect(p.locator(sel(f"request-pay-{stale}"))).to_have_count(0)
    check("cancelled elsewhere → request-error and stale pay button gone", p.locator(sel(f"request-item-{stale}")).get_attribute("data-status") == "cancelled")
    p.screenshot(path=f"{SHOTS}/requests-error-1280.png", full_page=True)
    c.close()
    reset(fixture())
    c = Ctx(b); p = c.page; c.login("cy"); p.goto("/requests"); p.wait_for_selector(sel("empty-requests"))
    check("empty-requests when both empty", True)
    p.screenshot(path=f"{SHOTS}/requests-empty-375.png") if False else None
    c.close()


def t_split(b):
    for mu, cur, amount_typed, minor, n, exp in ((2, "EUR", "10.00", 1000, 3, [334, 333, 333]), (2, "EUR", "0.01", 1, 3, [1, 0, 0]),
                                                 (2, "EUR", "0.10", 10, 3, [4, 3, 3]), (2, "EUR", "9.99", 999, 3, [333, 333, 333]),
                                                 (2, "EUR", "0.05", 5, 5, [1, 1, 1, 1, 1]), (0, "JPY", "1000", 1000, 3, [334, 333, 333]),
                                                 (3, "BHD", "1.000", 1000, 3, [334, 333, 333])):
        hs = ["ada", "bob", "cy", "dee", "eve"][:n]
        reset(fixture(mu, cur, users=[user(h, 0 if h != "ada" else 5000) for h in ["ada", "bob", "cy", "dee", "eve"]]))
        c = Ctx(b); p = c.page; c.login("ada"); p.goto("/split"); p.wait_for_selector(sel("split-amount"))
        rot = hs[1:] + hs[:1]
        p.fill(sel("split-amount"), amount_typed); p.fill(sel("split-handles"), ", ".join(rot))
        p.wait_for_selector(sel("split-preview"))
        prev = [text(p, f"split-share-{h}") for h in rot]
        check(f"split preview {cur} {minor}/{n} = {exp} in order", prev == [money(x, mu, cur) for x in exp], prev)
        check(f"split preview shares inside split-preview ({cur})", p.locator(sel("split-preview") + " [data-testid^='split-share-']").count() == n)
        n0 = len(c.posts("/splits"))
        check("preview posts nothing", n0 == 0)
        p.click(sel("split-submit")); p.wait_for_selector(sel("split-success"))
        resp = None
        sp = api("GET", "/requests?direction=outgoing", token=token("ada"))[1]["requests"]
        server = {r["payer_handle"]: r["amount"] for r in sp}
        check(f"submitted split identical to preview ({cur} {minor}/{n})", all(server[h] == exp[i] for i, h in enumerate(rot) if h != "ada"), server)
        c.close()
    reset(fixture())
    c = Ctx(b); p = c.page; c.login("ada"); p.goto("/split")
    p.fill(sel("split-amount"), "10"); p.fill(sel("split-handles"), "bob, nobody"); p.click(sel("split-submit"))
    p.wait_for_selector(sel("split-error")); check("split unknown handle → split-error", True)
    p.fill(sel("split-amount"), "10.005"); p.click(sel("split-submit")); p.wait_for_selector(sel("split-error"))
    check("split 3 decimals rejected without request", len([r for r in c.posts("/splits")]) == 1)
    p.fill(sel("split-amount"), "12.34"); p.fill(sel("split-handles"), "ada, bob, cy"); p.wait_for_selector(sel("split-preview"))
    p.screenshot(path=f"{SHOTS}/split-1280.png", full_page=True)
    c.close()


def t_refresh_competing(b):
    reset(fixture())
    c = Ctx(b); p = c.page; c.login("ada")
    tb, ta = token("bob"), token("ada")
    api("POST", "/payments", {"to_handle": "ada", "amount": 100}, tb, k())
    p.fill(sel("pay-handle"), "cy"); p.fill(sel("pay-amount"), "1.00"); p.fill(sel("pay-note"), "keep me")
    p.click(sel("wallet-refresh"))
    expect(p.locator(sel("wallet-balance"))).to_have_attribute("data-amount", "10100")
    check("wallet-refresh shows other client's payment, keeps pay form", p.locator("[data-testid^='activity-item-']").count() == 1
          and p.input_value(sel("pay-handle")) == "cy" and p.input_value(sel("pay-amount")) == "1.00" and p.input_value(sel("pay-note")) == "keep me")
    # latest refresh wins: hold the first /me response (old state), refresh again (new state), release old
    held = []
    state = {"hold": True}
    def handler(route):
        if state["hold"]:
            held.append((route, route.fetch()))   # read the OLD state now, answer later
        else:
            route.continue_()
    p.route(re.compile(r".*/(me|activity)(\?.*)?$"), handler)
    # first refresh: /me and /activity -> only the first matched request is delayed; delay both by holding /me
    p.click(sel("wallet-refresh"))
    for _ in range(50):
        if len(held) >= 2: break
        p.wait_for_timeout(100)
    state["hold"] = False
    api("POST", "/payments", {"to_handle": "ada", "amount": 50}, tb, k())   # new state 10150
    p.click(sel("wallet-refresh"))
    expect(p.locator(sel("wallet-balance"))).to_have_attribute("data-amount", "10150")
    for r, resp in held:
        r.fulfill(response=resp)              # the older read arrives last
    p.wait_for_timeout(1000)
    check("delayed old reads were held then delivered", len(held) == 2, len(held))
    check("latest refresh wins over delayed older read", p.locator(sel("wallet-balance")).get_attribute("data-amount") == "10150"
          and p.locator(sel("wallet-available")).get_attribute("data-amount") == "10150", p.locator(sel("wallet-balance")).get_attribute("data-amount"))
    p.unroute(re.compile(r".*/(me|activity)(\?.*)?$"))
    # refused pay after another client spent the balance
    api("POST", "/payments", {"to_handle": "bob", "amount": 10150}, ta, k())
    p.fill(sel("pay-handle"), "cy"); p.fill(sel("pay-amount"), "5.00"); p.fill(sel("pay-note"), "after spend"); p.select_option(sel("pay-visibility"), "private")
    p.click(sel("pay-submit")); p.wait_for_selector(sel("pay-error"))
    expect(p.locator(sel("wallet-balance"))).to_have_attribute("data-amount", "0")
    check("refused pay: pay-error, balance/feed refreshed, inputs preserved", p.input_value(sel("pay-handle")) == "cy" and p.input_value(sel("pay-amount")) == "5.00"
          and p.input_value(sel("pay-note")) == "after spend" and p.input_value(sel("pay-visibility")) == "private" and count(p, "pay-uncertain") == 0
          and p.locator("[data-testid^='activity-item-']").count() == 3)
    p.screenshot(path=f"{SHOTS}/pay-refused-1280.png", full_page=True)
    c.close()


def t_uncertain(b):
    reset(fixture())
    c = Ctx(b); p = c.page; c.login("ada")
    def lose(route):
        route.fetch()          # commits on the server
        route.abort("connectionreset")
    p.route(re.compile(r".*/payments$"), lose)
    p.fill(sel("pay-handle"), "bob"); p.fill(sel("pay-amount"), "7.77"); p.fill(sel("pay-note"), "lost")
    p.click(sel("pay-submit")); p.wait_for_selector(sel("pay-uncertain"))
    check("lost response after commit → pay-uncertain (nonempty), no pay-error", count(p, "pay-error") == 0 and text(p, "pay-uncertain") != "")
    check("server did commit", me("ada")["total"] == 10000 - 777)
    p.screenshot(path=f"{SHOTS}/pay-uncertain-1280.png", full_page=True)
    p.unroute(re.compile(r".*/payments$"))
    p.click(sel("pay-submit"))
    expect(p.locator(sel("pay-uncertain"))).to_have_count(0)
    expect(p.locator(sel("wallet-balance"))).to_have_attribute("data-amount", str(10000 - 777))
    posts = c.posts("/payments")
    check("retry same key + byte-identical body", len(posts) == 2 and posts[0].headers["idempotency-key"] == posts[1].headers["idempotency-key"] and posts[0].post_data == posts[1].post_data)
    check("retry: no pay-error, feed has one payment, money once", count(p, "pay-error") == 0 and p.locator("[data-testid^='activity-item-']").count() == 1 and me("ada")["total"] == 9223)
    # 5xx and timeout-like abort before commit
    p.route(re.compile(r".*/payments$"), lambda r: r.fulfill(status=502, body="bad gateway"))
    p.fill(sel("pay-note"), "five oh two"); p.click(sel("pay-submit")); p.wait_for_selector(sel("pay-uncertain"))
    check("5xx → pay-uncertain not pay-error", count(p, "pay-error") == 0)
    p.unroute(re.compile(r".*/payments$"))
    p.click(sel("pay-submit")); expect(p.locator(sel("pay-uncertain"))).to_have_count(0)
    check("retry after 5xx (never committed) creates exactly one payment", me("ada")["total"] == 9223 - 777)
    check("uncertain flow: no console errors other than aborted fetch", all("Failed to load" in x or "502" in x or "ERR_" in x for x in c.console), c.console)
    c.close()


def t_upgrade(b):
    """Browser on the stage-2 origin talks to the stage-1 API; export→import; then to stage-2."""
    reset(fixture(), base=S1)
    reset(fixture(users=[user("zed", 1)]))
    c = Ctx(b); p = c.page
    upgraded = {"v": False}
    def to_s1(route):
        if upgraded["v"] or "/static/" in route.request.url:
            return route.continue_()
        url = route.request.url
        if route.request.resource_type == "document":
            return route.continue_()
        route.continue_(url=url.replace(S2, S1))
    p.route("**/*", to_s1)
    c.login("ada")
    tb = token("bob", S1)
    rq = api("POST", "/requests", {"payer_handle": "ada", "amount": 250}, tb, k(), base=S1)[1]["request_id"]
    # lost payment before export
    lost = {"on": True}
    def maybe_lose(route):
        if lost["on"]:
            route.fetch(url=route.request.url.replace(S2, S1)); route.abort("connectionreset")
        else:
            route.fallback()
    p.route(re.compile(r".*/payments$"), maybe_lose)
    p.fill(sel("pay-handle"), "bob"); p.fill(sel("pay-amount"), "3.21"); p.fill(sel("pay-note"), "pre-upgrade")
    p.click(sel("pay-submit")); p.wait_for_selector(sel("pay-uncertain"))
    check("upgrade: lost payment on stage 1 → pay-uncertain", me("ada", S1)["balance"] == 10000 - 321)
    s, ex = api("GET", "/_test/export", base=S1)
    s2, _ = api("POST", "/_test/import", ex)
    check("upgrade: stage-1 export imported into stage 2", s == 200 and s2 == 204, s2)
    upgraded["v"] = True; lost["on"] = False
    p.click(sel("pay-submit"))
    expect(p.locator(sel("pay-uncertain"))).to_have_count(0)
    expect(p.locator(sel("wallet-balance"))).to_have_attribute("data-amount", str(10000 - 321))
    posts = c.posts("/payments")
    check("upgrade: retry same key+body recovered original, money once, still signed in",
          posts[0].headers["idempotency-key"] == posts[-1].headers["idempotency-key"] and posts[0].post_data == posts[-1].post_data
          and me("ada")["total"] == 9679 and count(p, "pay-error") == 0 and count(p, "current-user") == 1
          and p.locator("[data-testid^='activity-item-']").count() == 1)
    check("upgrade: imported balance shows available", p.locator(sel("wallet-available")).get_attribute("data-amount") == "9679")
    p.goto("/requests"); p.wait_for_selector(sel(f"request-pay-{rq}"))
    p.click(sel(f"request-pay-{rq}")); expect(p.locator(sel(f"request-item-{rq}"))).to_have_attribute("data-status", "paid")
    check("upgrade: pending stage-1 request payable via request screen", me("ada")["total"] == 9679 - 250)
    c.close()


def t_holds_ui(b):
    f = fixture(users=[user("ada", 10000), user("bob", 2500), user("cy", 0)], auths=[
        {"id": "a_in", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 1500, "note": "deposit", "visibility": "public", "status": "open", "expires_at": iso(7200)},
        {"id": "a_out", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 2000, "note": "rent", "visibility": "private", "status": "open", "expires_at": iso(3600)},
        {"id": "a_cap", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 700, "captured_amount": 500, "status": "captured", "expires_at": iso(3600)},
        {"id": "a_old", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 900, "status": "open", "expires_at": iso(-3600)}])
    reset(f)
    c = Ctx(b); p = c.page; c.login("ada")
    check("seeded hold visible right after reset: available headline", p.locator(sel("wallet-available")).get_attribute("data-amount") == "8000"
          and p.locator(sel("wallet-held")).get_attribute("data-amount") == "2000" and text(p, "wallet-available") == "80.00 EUR")
    p.goto("/authorizations"); p.wait_for_selector(sel("authorization-item-a_in"))
    ids = [x.get_attribute("data-testid") for x in p.locator(sel("authorization-list") + " > [data-testid^='authorization-item-']").all()]
    check("authorization-list has all, involving caller", set(ids) == {f"authorization-item-{x}" for x in ("a_in", "a_out", "a_cap", "a_old")}, ids)
    st = {x: p.locator(sel(f"authorization-item-{x}")).get_attribute("data-status") for x in ("a_in", "a_out", "a_cap", "a_old")}
    check("data-status (seeded past open → expired)", st == {"a_in": "open", "a_out": "open", "a_cap": "captured", "a_old": "expired"}, st)
    check("authorization-amount exact", text(p, "authorization-amount-a_in") == "15.00 EUR")
    lst = {a["authorization_id"]: a for a in api("GET", "/authorizations", token=token("ada"))[1]["authorizations"]}
    check("authorization-expires text is RFC 3339 expires_at", p.locator(sel("authorization-expires-a_in")).text_content().strip() == lst["a_in"]["expires_at"])
    check("captured amount only on captured", count(p, "authorization-captured-a_cap") == 1 and text(p, "authorization-captured-a_cap") == "5.00 EUR"
          and count(p, "authorization-captured-a_in") == 0 and count(p, "authorization-captured-a_old") == 0)
    check("capture input prefilled with remaining, only on incoming open", p.input_value(sel("authorization-capture-amount-a_in")) == "15.00"
          and count(p, "authorization-capture-amount-a_out") == 0 and count(p, "authorization-capture-a_out") == 0 and count(p, "authorization-capture-a_cap") == 0)
    check("void only on outgoing open", count(p, "authorization-void-a_out") == 1 and count(p, "authorization-void-a_in") == 0 and count(p, "authorization-void-a_old") == 0)
    p.screenshot(path=f"{SHOTS}/holds-1280.png", full_page=True)
    p.fill(sel("authorization-capture-amount-a_in"), "10.00"); p.click(sel("authorization-capture-a_in"))
    expect(p.locator(sel("authorization-item-a_in"))).to_have_attribute("data-status", "captured")
    check("capture via UI: captured 10.00, balance refreshed", text(p, "authorization-captured-a_in") == "10.00 EUR" and me("ada")["total"] == 11000
          and me("bob")["held"] == 0)
    p.click(sel("authorization-void-a_out")); expect(p.locator(sel("authorization-item-a_out"))).to_have_attribute("data-status", "voided")
    check("void via UI releases hold; wallet-held gone", count(p, "authorization-void-a_out") == 0 and count(p, "wallet-held") == 0 and me("ada")["held"] == 0)
    # refused capture: voided elsewhere
    tb = token("bob")
    a2 = api("POST", "/authorizations", {"to_handle": "ada", "amount": 100}, tb, k())[1]["authorization_id"]
    p.goto("/authorizations"); p.wait_for_selector(sel(f"authorization-capture-{a2}"))
    api("POST", f"/authorizations/{a2}/void", None, tb)
    p.click(sel(f"authorization-capture-{a2}")); p.wait_for_selector(sel("authorization-error"))
    expect(p.locator(sel(f"authorization-capture-{a2}"))).to_have_count(0)
    check("refused capture → authorization-error, list refreshed", p.locator(sel(f"authorization-item-{a2}")).get_attribute("data-status") == "voided")
    # authorize form
    p.fill(sel("authorize-handle"), "cy"); p.fill(sel("authorize-amount"), "500.00"); p.click(sel("authorize-submit"))
    p.wait_for_selector(sel("authorize-error")); check("authorize above available → authorize-error", me("ada")["held"] == 0)
    p.fill(sel("authorize-amount"), "1.005"); p.click(sel("authorize-submit")); p.wait_for_selector(sel("authorize-error"))
    check("authorize 3 decimals rejected without request", len(c.posts("/authorizations")) == 1)
    p.fill(sel("authorize-amount"), "12.50"); p.fill(sel("authorize-note"), "hold it"); p.select_option(sel("authorize-visibility"), "private")
    p.click(sel("authorize-submit"))
    expect(p.locator(sel("wallet-held"))).to_have_attribute("data-amount", "1250")
    newest = p.locator(sel("authorization-list") + " > [data-testid^='authorization-item-']").first
    check("new hold newest first, open, available updated", newest.get_attribute("data-status") == "open" and p.locator(sel("wallet-available")).get_attribute("data-amount") == str(11000 - 1250))
    vals = sorted(o.get_attribute("value") for o in p.locator(sel("authorize-visibility") + " option").all())
    check("authorize-visibility options public/private", vals == ["private", "public"])
    p.screenshot(path=f"{SHOTS}/holds-after-1280.png", full_page=True)
    c.close()
    reset(fixture())
    c = Ctx(b); p = c.page; c.login("cy"); p.goto("/authorizations"); p.wait_for_selector(sel("empty-authorizations"))
    check("empty-authorizations shown", True); c.close()


def t_layout(b):
    long = "W" * 200
    f = fixture(users=[user("ada", 200000), user("verylonghandle_abcde", 1200000, "Bartholomew Maximilian Longname"), user("cy", 0)],
                auths=[{"id": "a_l", "from_user_id": "u_verylonghandle_abcde", "to_user_id": "u_ada", "amount": 999999, "note": long, "status": "open", "expires_at": iso(7200)},
                       {"id": "a_o", "from_user_id": "u_ada", "to_user_id": "u_verylonghandle_abcde", "amount": 123456, "note": long, "status": "open", "expires_at": iso(7200)}])
    reset(f)
    tb = token("verylonghandle_abcde"); ta = token("ada")
    api("POST", "/payments", {"to_handle": "ada", "amount": 100000, "note": long}, tb, k())
    api("POST", "/requests", {"payer_handle": "ada", "amount": 99999, "note": long}, tb, k())
    api("POST", "/requests", {"payer_handle": "verylonghandle_abcde", "amount": 12345, "note": long}, ta, k())
    for w, hgt in ((375, 812), (768, 1024), (1280, 800)):
        c = Ctx(b, w, hgt); p = c.page; c.login("ada")
        for route, ready in (("/", "activity-list"), ("/requests", "incoming-list"), ("/split", "split-amount"), ("/authorizations", "authorization-list")):
            p.goto(route); p.wait_for_selector(sel(ready), state="attached"); time.sleep(0.3)
            if route == "/split":
                p.fill(sel("split-amount"), "1234.56"); p.fill(sel("split-handles"), "ada, verylonghandle_abcde, cy")
            ok = no_hscroll(p)
            sw = p.evaluate("() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]")
            check(f"no horizontal scroll {route} @{w} (long notes/handles)", ok, sw)
            p.screenshot(path=f"{SHOTS}/{route.strip('/') or 'wallet'}-{w}.png", full_page=True)
        c.close()
        c = Ctx(b, w, hgt); p = c.page
        for route in ("/login", "/signup"):
            p.goto(route); p.wait_for_selector(sel(route.strip("/") + "-submit"))
            check(f"no horizontal scroll {route} @{w}", no_hscroll(p))
            p.screenshot(path=f"{SHOTS}/{route.strip('/')}-{w}.png", full_page=True)
        c.close()


def t_a11y(b):
    reset(fixture(auths=[{"id": "a_x", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 100, "status": "open", "expires_at": iso(7200)}]))
    c = Ctx(b, 375, 812); p = c.page; c.login("ada")
    for route, ready in (("/", "pay-handle"), ("/requests", "incoming-list"), ("/split", "split-amount"), ("/authorizations", "authorize-handle"), ("/login", None), ("/signup", None)):
        if route in ("/login", "/signup"):
            c2 = Ctx(b, 375, 812); q = c2.page; q.goto(route); q.wait_for_selector(sel(route.strip("/") + "-submit"))
        else:
            q = p; q.goto(route); q.wait_for_selector(sel(ready), state="attached"); q.wait_for_timeout(300)
        unlabeled = q.evaluate("""() => [...document.querySelectorAll('input,select,textarea')].filter(e => {
            const id = e.id; const lab = id && document.querySelector(`label[for="${id}"]`);
            const vis = lab && (lab.offsetWidth > 1 || lab.classList.contains('visually-hidden'));
            return !(vis || e.getAttribute('aria-label') || e.closest('label'));
        }).map(e => e.outerHTML.slice(0, 80))""")
        check(f"every input labelled on {route}", not unlabeled, unlabeled)
        hidden_labels = q.evaluate("""() => [...document.querySelectorAll('input,select')].filter(e => {
            const lab = e.id && document.querySelector(`label[for="${e.id}"]`);
            return !(lab && lab.offsetWidth > 1 && lab.offsetHeight > 1);
        }).map(e => e.getAttribute('data-testid') || e.id)""")
        if hidden_labels:
            print(f"INFO {route}: inputs without a *visible* label element: {hidden_labels}")
        small = q.evaluate("""() => [...document.querySelectorAll('button, a.btn, input, select')].filter(e => e.offsetParent).map(e => {
            const r = e.getBoundingClientRect(); return [e.getAttribute('data-testid') || e.textContent.trim().slice(0, 20), Math.round(r.width), Math.round(r.height)]
        }).filter(([n, w, h]) => h < 24 || w < 24)""")
        check(f"touch targets >= 24px on {route}", not small, small)
        if route in ("/login", "/signup"): c2.close()
    # focus ring
    p.goto("/"); p.wait_for_selector(sel("pay-handle"))
    p.keyboard.press("Tab")
    found = None
    for _ in range(25):
        info = p.evaluate("""() => { const e = document.activeElement; const s = getComputedStyle(e);
            return [e.tagName, e.getAttribute('data-testid'), s.outlineStyle, s.outlineWidth, s.boxShadow] }""")
        if info[1] == "pay-handle" or info[1] == "pay-submit":
            found = info
            break
        p.keyboard.press("Tab")
    check("keyboard focus visible (outline or ring) on pay field", found and (found[2] != "none" and found[3] != "0px" or (found[4] and found[4] != "none")), found)
    # contrast of key text
    contrast = p.evaluate("""() => {
      function lum(c){const m=c.match(/[\\d.]+/g).map(Number);const [r,g,b]=m.slice(0,3).map(v=>{v/=255;return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4)});return 0.2126*r+0.7152*g+0.0722*b}
      function bg(e){while(e){const st=getComputedStyle(e); const g=st.backgroundImage.match(/rgba?\\([^)]*\\)/); if(g) return g[0]; const c=st.backgroundColor; if(c && !c.startsWith('rgba') && c!=='transparent') return c; e=e.parentElement} return 'rgb(255,255,255)'}
      const out=[]; for (const e of document.querySelectorAll('label, p, span, a, button, h1, h2, h3, dt, dd, li, strong, time')) {
        if (!e.offsetParent || !e.textContent.trim() || [...e.children].some(c=>c.textContent.trim())) continue;
        const s=getComputedStyle(e); const L1=lum(s.color), L2=lum(bg(e)); const r=(Math.max(L1,L2)+0.05)/(Math.min(L1,L2)+0.05);
        const big = parseFloat(s.fontSize) >= 24 || (parseFloat(s.fontSize) >= 18.66 && parseInt(s.fontWeight) >= 700);
        if (r < (big ? 3 : 4.5)) out.push([e.textContent.trim().slice(0,30), r.toFixed(2)]);
      } return out; }""")
    check("text contrast >= WCAG AA on wallet", not contrast, contrast[:8])
    check("a11y pages: all requests same-origin", not c.foreign, c.foreign[:5])
    check("a11y pages: no console errors", not c.console, c.console[:5])
    c.close()


def t_repair(b):
    """Re-verification of 620406d: authorize form on '/', visible labels, no raw codes."""
    for w, hgt in ((375, 812), (1280, 800)):
        reset(fixture(users=[user("ada", 10000), user("bob", 2500), user("cy", 0)]))
        c = Ctx(b, w, hgt); p = c.page; c.login("ada")
        check(f"@{w}: authorize form on /", all(count(p, t) == 1 for t in ("authorize-handle", "authorize-amount", "authorize-note", "authorize-visibility", "authorize-submit")))
        vals = sorted(o.get_attribute("value") for o in p.locator(sel("authorize-visibility") + " option").all())
        check(f"@{w}: / authorize-visibility public/private", vals == ["private", "public"])
        p.fill(sel("authorize-handle"), "bob"); p.fill(sel("authorize-amount"), "20"); p.fill(sel("authorize-note"), "deposit")
        p.click(sel("authorize-submit"))
        expect(p.locator(sel("wallet-held"))).to_have_attribute("data-amount", "2000")
        check(f"@{w}: authorize from / refreshes available/held, total unchanged, feed unchanged",
              p.locator(sel("wallet-available")).get_attribute("data-amount") == "8000" and p.locator(sel("wallet-balance")).get_attribute("data-amount") == "10000"
              and count(p, "empty-activity") == 1 and me("ada")["held"] == 2000)
        check(f"@{w}: authorize form keeps values", p.input_value(sel("authorize-handle")) == "bob" and p.input_value(sel("authorize-amount")) == "20")
        p.click(sel("authorize-submit")); p.wait_for_timeout(800)
        posts = c.posts("/authorizations")
        check(f"@{w}: unchanged resubmit = replay (same key+body), one hold", len(posts) == 2 and posts[0].headers["idempotency-key"] == posts[1].headers["idempotency-key"]
              and posts[0].post_data == posts[1].post_data and me("ada")["held"] == 2000 and count(p, "authorize-error") == 0)
        p.fill(sel("authorize-amount"), "90"); p.click(sel("authorize-submit")); p.wait_for_selector(sel("authorize-error"))
        et = text(p, "authorize-error")
        check(f"@{w}: refused (insufficient available) → authorize-error, no raw code in text", "insufficient_funds" not in et and "(" not in et, et)
        check(f"@{w}: refused keeps inputs", p.input_value(sel("authorize-amount")) == "90")
        p.fill(sel("authorize-amount"), "1.001"); n0 = len(c.posts("/authorizations")); p.click(sel("authorize-submit")); p.wait_for_selector(sel("authorize-error"))
        check(f"@{w}: / authorize 3 decimals rejected, no request", len(c.posts("/authorizations")) == n0)
        # uncertain on the / authorize form, then retry same key+body
        def lose(route):
            route.fetch(); route.abort("connectionreset")
        p.route(re.compile(r".*/authorizations$"), lambda r: lose(r) if r.request.method == "POST" else r.continue_())
        p.fill(sel("authorize-amount"), "5"); p.click(sel("authorize-submit")); p.wait_for_selector(sel("authorize-uncertain"))
        check(f"@{w}: lost authorize response → authorize-uncertain not error", count(p, "authorize-error") == 0 and me("ada")["held"] == 2500)
        p.unroute(re.compile(r".*/authorizations$"))
        p.click(sel("authorize-submit")); expect(p.locator(sel("authorize-uncertain"))).to_have_count(0)
        posts = c.posts("/authorizations")
        check(f"@{w}: authorize retry same key+body, held once", posts[-1].headers["idempotency-key"] == posts[-2].headers["idempotency-key"] and posts[-1].post_data == posts[-2].post_data
              and me("ada")["held"] == 2500 and p.locator(sel("wallet-held")).get_attribute("data-amount") == "2500")
        check(f"@{w}: no horizontal scroll on / with reserve card", no_hscroll(p), p.evaluate("() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]"))
        p.screenshot(path=f"{SHOTS}/repair-wallet-{w}.png", full_page=True)
        # pay error text has no code
        p.fill(sel("pay-handle"), "nobody"); p.fill(sel("pay-amount"), "1"); p.click(sel("pay-submit")); p.wait_for_selector(sel("pay-error"))
        pe = text(p, "pay-error")
        check(f"@{w}: pay-error text has no raw code", not re.search(r"\(?[a-z]+_[a-z_]+\)?", pe), pe)
        # visible labels on inline controls
        tb = token("bob")
        rq = api("POST", "/requests", {"payer_handle": "ada", "amount": 100}, tb, k())[1]["request_id"]
        api("POST", "/authorizations", {"to_handle": "ada", "amount": 100}, tb, k())
        for route, ready in (("/", "pay-handle"), ("/requests", f"request-pay-{rq}"), ("/authorizations", "authorize-handle")):
            p.goto(route); p.wait_for_selector(sel(ready)); p.wait_for_timeout(300)
            nolabel = p.evaluate("""() => [...document.querySelectorAll('input,select')].filter(e => {
                const lab = e.id && document.querySelector(`label[for="${e.id}"]`);
                return !(lab && lab.offsetWidth > 1 && lab.offsetHeight > 1 && getComputedStyle(lab).visibility !== 'hidden');
            }).map(e => e.getAttribute('data-testid') || e.id)""")
            check(f"@{w}: every input on {route} has a visible <label for>", not nolabel, nolabel)
            check(f"@{w}: no horizontal scroll on {route}", no_hscroll(p))
            p.screenshot(path=f"{SHOTS}/repair-{route.strip('/') or 'wallet'}-{w}.png", full_page=True)
        # refused capture message has no code
        a2 = api("GET", "/authorizations?direction=incoming&status=open", token=token("ada"))[1]["authorizations"][0]["authorization_id"]
        api("POST", f"/authorizations/{a2}/void", None, tb)
        p.click(sel(f"authorization-capture-{a2}")); p.wait_for_selector(sel("authorization-error"))
        at = text(p, "authorization-error")
        check(f"@{w}: authorization-error text has no raw code", "authorization_not_open" not in at and "(" not in at, at)
        # contrast of the reserve card text on /
        p.goto("/"); p.wait_for_selector(sel("authorize-handle"))
        low = p.evaluate("""() => {
          function lum(c){const m=c.match(/[\\d.]+/g).map(Number);const [r,g,b]=m.slice(0,3).map(v=>{v/=255;return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4)});return 0.2126*r+0.7152*g+0.0722*b}
          function bg(e){while(e){const c=getComputedStyle(e).backgroundColor; if(c && c!=='rgba(0, 0, 0, 0)' && c!=='transparent') return c; e=e.parentElement} return 'rgb(255, 255, 255)'}
          const card = document.querySelector('[aria-labelledby="hold-title"]'); const out=[];
          for (const e of card.querySelectorAll('label,p,h2,a,button,span')) { if(!e.offsetParent||!e.textContent.trim()) continue;
            const s=getComputedStyle(e); const r=(Math.max(lum(s.color),lum(bg(e)))+0.05)/(Math.min(lum(s.color),lum(bg(e)))+0.05);
            if (r<4.5) out.push([e.textContent.trim().slice(0,30), r.toFixed(2)]); } return out; }""")
        check(f"@{w}: reserve card text contrast >= 4.5:1", not low, low)
        check(f"@{w}: repair pages same-origin, no console errors", not c.foreign and all("ERR_" in x or "Failed to load" in x for x in c.console), (c.foreign, c.console))
        c.close()


def main():
    with sync_playwright() as pw:
        b = pw.chromium.launch()
        for t in (t_auth, t_wallet_format, t_pay_parsing, t_pay_retention, t_feed, t_requests, t_split, t_refresh_competing,
                  t_uncertain, t_upgrade, t_holds_ui, t_layout, t_a11y, t_repair):
            print(f"== {t.__name__}", flush=True)
            try:
                t(b)
            except Exception as e:
                import traceback; traceback.print_exc()
                check(f"{t.__name__} ran to completion", False, repr(e)[:300])
        b.close()
    n = sum(1 for _, ok in RESULTS if ok)
    print(f"TOTAL {n}/{len(RESULTS)} passed, {len(RESULTS) - n} failed")


if __name__ == "__main__":
    main()
