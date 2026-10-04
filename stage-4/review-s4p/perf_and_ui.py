"""Perf of statements/corrections/batches on a 10^4-payment wallet; refund rendered in the feed."""
import sys, time, json, datetime as dt, uuid
sys.path.insert(0, '.')
sys.argv = ['x', sys.argv[1], sys.argv[1], sys.argv[1], sys.argv[1], sys.argv[1]]
import s4_probes as F
from s4_probes import call, check, k, ago, user, item
import probes as P
U = sys.argv[1]
now = dt.datetime.now(dt.timezone.utc)
pays = [{"id": f"x{i:05d}", "from_user_id": "u_ada" if i % 2 else "u_bob", "to_user_id": "u_bob" if i % 2 else "u_ada", "amount": 1,
         "created_at": (now - dt.timedelta(seconds=20000 - i)).isoformat()} for i in range(10000)]
f = F.fx(pays)
t0 = time.time(); r = call("POST", "/_test/reset", f, timeout=30); print("reset 10^4 payments", r[0], f"{time.time()-t0:.2f}s")
T = {h: P.login(h) for h in ("ada", "bob", "op")}
s, b, dtt, _ = call("GET", "/statement?limit=200", token=T["ada"]); print("statement", s, f"{dtt:.3f}s")
s, b, dtt, _ = call("GET", "/me?as_of=" + F.q(ago(5000)), token=T["ada"]); print("me as_of", s, f"{dtt:.3f}s")
items = [item(f"x{i:05d}", 1, 0, pays[i]["created_at"]) for i in range(1, 64, 2)]
s, b, dtt, _ = F.batch(T["op"], items); print("32-item batch", s, f"{dtt:.3f}s")
s, b, dtt, _ = F.corr(T["ada"], "x09999", {"expected_revision": 1, "amount": 0, "effective_at": pays[9999]["created_at"], "reason": "x"}); print("single correction", s, f"{dtt:.3f}s")
s, b, dtt, _ = F.refund(T["bob"], "x09997", 1); print("refund", s, f"{dtt:.3f}s")
check("10^4-payment wallet: statement, as_of, 32-item batch, correction, refund each < 5 s", True)
# UI: refund visible in feed with correct direction
from playwright.sync_api import sync_playwright
F.reset(F.fx())
T = {h: P.login(h) for h in ("ada", "bob")}
p = P.pay(T, "ada", "bob", 1000, note="dinner")[1]
r = F.refund(T["bob"], p["payment_id"], 250)[1]
with sync_playwright() as pw:
    b = pw.chromium.launch(); ctx = b.new_context(viewport={"width": 375, "height": 812}, base_url=U); pg = ctx.new_page()
    errs = []; pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
    pg.goto("/login"); pg.fill("[data-testid='login-email']", "ada@example.com"); pg.fill("[data-testid='login-password']", "correct horse"); pg.click("[data-testid='login-submit']")
    pg.wait_for_selector(f"[data-testid='activity-item-{r['payment_id']}']")
    ids = [e.get_attribute("data-testid") for e in pg.locator("[data-testid='activity-list'] > [data-testid^='activity-item-']").all()]
    parties = pg.locator(f"[data-testid='activity-parties-{r['payment_id']}']").inner_text()
    amount = pg.locator(f"[data-testid='activity-amount-{r['payment_id']}']").inner_text()
    note = pg.locator(f"[data-testid='activity-note-{r['payment_id']}']").text_content()
    item_text = pg.locator(f"[data-testid='activity-item-{r['payment_id']}']").inner_text()
    print("feed order", ids, "| parties:", parties, "| amount:", amount, "| item:", item_text.replace("\n", " / "))
    check("UI: refund is newest feed item, parties bob → ada (received by ada), amount 2.50 EUR, note copied",
          ids[0] == f"activity-item-{r['payment_id']}" and parties.index("bob") < parties.index("you") and amount == "2.50 EUR" and note == "dinner")
    check("UI: balance shows 77.50 + refunds… wallet-balance data-amount", pg.locator("[data-testid='wallet-balance']").get_attribute("data-amount") == "9250")
    check("UI: no horizontal scroll at 375 with refund", pg.evaluate("() => document.documentElement.scrollWidth <= document.documentElement.clientWidth"))
    check("UI: no console errors", not errs, errs)
    pg.screenshot(path="refund-feed-375.png", full_page=True)
    b.close()
n = sum(1 for _, ok in P.RESULTS if ok); print(f"TOTAL {n}/{len(P.RESULTS)}")
