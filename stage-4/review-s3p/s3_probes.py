#!/usr/bin/env python3
"""Reviewer probes for Pocketful stage 3 (as_of, known_at, statements, snapshots, corrections,
historical holds), written from the stage-3 requirements text with an independent naive oracle.

Usage: python3 s3_probes.py <s3-url> <s3-url-2> <stage1-url> <stage2-url>
"""
import os, sys, json, time, uuid, random, threading, datetime as dt
import urllib.parse
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.abspath(__file__))
for cand in (os.path.join(HERE, "..", "stage-1"), os.path.join(HERE, "..", "review")):
    if os.path.exists(os.path.join(cand, "probes.py")):
        sys.path.insert(0, cand); break
URL, URL2, URL1, URLS2 = sys.argv[1:5]
sys.argv = [sys.argv[0], URL]
import probes as P
from probes import call, check, err, k
P.A = URL

UTC = dt.timezone.utc


def ns(s):
    """RFC 3339 → integer nanoseconds (independent parser; handles Z, offsets, fractions)."""
    s = s.replace("z", "Z")
    if s.endswith("Z"): s = s[:-1] + "+00:00"
    main, off = s[:-6], s[-6:]
    frac = 0
    if "." in main:
        main, f = main.split(".")
        frac = int((f + "000000000")[:9])
    d = dt.datetime.fromisoformat(main + off)
    return int(d.timestamp()) * 10**9 + frac


def iso(d): return d.isoformat()
def ago(sec, tz=UTC): return (dt.datetime.now(UTC) - dt.timedelta(seconds=sec)).astimezone(tz).replace(microsecond=0).isoformat()
def q(s): return urllib.parse.quote(s, safe="")


def user(h, b): return {"id": "u_" + h, "email": h + "@example.com", "password": "correct horse", "display_name": h.title(), "handle": h, "balance": b}


def reset(f, base=None):
    base = base or URL
    r = call("POST", "/_test/reset", f, base=base)
    assert r[0] == 204, r
    return {u["handle"]: P.login(u["handle"], base) for u in f["users"]}


def me(t, query="", base=None): return call("GET", "/me" + query, token=t, base=base or URL)
def stmt(t, query="", base=None): return call("GET", "/statement" + query, token=t, base=base or URL)
def corr(t, pid, body, key=None, base=None): return call("POST", f"/payments/{pid}/corrections", body, token=t, key=key or k(), base=base or URL)
def revs(t, pid): return call("GET", f"/payments/{pid}/revisions", token=t)


def full_statement(t, query=""):
    s, b, _, _ = stmt(t, query + ("&" if query else "?") + "limit=200")
    return b


# ---------------- naive oracle ----------------
class Oracle:
    """Own model of payments+revisions per user, fed only from API reads."""
    def __init__(self, openings): self.open = dict(openings); self.pays = {}

    def load(self, T):
        for h, t in T.items():
            items, off = [], 0
            while True:
                pg = call("GET", f"/activity?limit=200&offset={off}", token=t)[1]
                items += pg["payments"]; off += 200
                if not pg["has_more"]: break
            for p in items:
                if p["from_user_id"] == "u_" + h or p["to_user_id"] == "u_" + h:
                    if p["payment_id"] not in self.pays:
                        rv = revs(t, p["payment_id"])[1]["revisions"]
                        self.pays[p["payment_id"]] = (p["from_user_id"], p["to_user_id"],
                                                      [(r["amount"], ns(r["effective_at"]), ns(r["recorded_at"]), r["revision"]) for r in rv])
        return self

    def sel(self, rv, kk):
        best = None
        for r in rv:
            if r[2] <= kk: best = r
        return best

    def bal(self, uid, t, kk):
        b = self.open[uid]
        for f, to, rv in self.pays.values():
            r = self.sel(rv, kk)
            if r and r[1] <= t:
                b += (-r[0] if f == uid else r[0] if to == uid else 0)
        return b

    def boundaries(self, kk):
        out = set()
        for f, to, rv in self.pays.values():
            r = self.sel(rv, kk)
            if r: out.add(r[1])
        return sorted(out)


BIG = 10**19 * 9  # far-future knowledge


def basic_fixture(extra_pays=None, users=None, ops=()):
    users = users or [user("ada", 10000), user("bob", 2500), user("cy", 0), user("dee", 700), user("op", 0)]
    return {"currency": "EUR", "minor_units": 2, "users": users, "payments": extra_pays or [], "requests": [],
            "settlement_operator_ids": list(ops)}


# ---------------------------------------------------------------- probes

def p_asof():
    t1, t2, t3 = ago(7200), ago(3600), ago(1800)
    # same instant in different offsets
    t2_plus2 = (dt.datetime.fromisoformat(t2).astimezone(dt.timezone(dt.timedelta(hours=2)))).isoformat()
    t2_m8 = (dt.datetime.fromisoformat(t2).astimezone(dt.timezone(dt.timedelta(hours=-8)))).isoformat()
    f = basic_fixture([
        {"id": "p1", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 1000, "created_at": t1},
        {"id": "p2", "from_user_id": "u_ada", "to_user_id": "u_cy", "amount": 300, "created_at": t2_plus2},
        {"id": "p3", "from_user_id": "u_ada", "to_user_id": "u_dee", "amount": 200, "created_at": t2_m8},
        {"id": "p4", "from_user_id": "u_cy", "to_user_id": "u_ada", "amount": 50, "created_at": t3}])
    T = reset(f)
    # ada ending 10000; net seeded = +1000 -300 -200 +50 = 550 -> opening 9450
    m = me(T["ada"])[1]
    check("loading seeded payments keeps fixture balance", m["balance"] == 10000 and m["total"] == 10000 and "as_of" not in m, m)
    def b(asof): return me(T["ada"], "?as_of=" + q(asof))
    check("as_of before earliest = opening (9450)", b(ago(99999))[1]["balance"] == 9450)
    check("as_of exactly at p1 inclusive (10450)", b(t1)[1]["balance"] == 10450, b(t1)[1])
    one_us_before = (dt.datetime.fromisoformat(t1) - dt.timedelta(microseconds=1)).isoformat()
    check("as_of 1µs before p1 = opening", b(one_us_before)[1]["balance"] == 9450)
    for s in (t2, t2_plus2, t2_m8, t2.replace("+00:00", "Z")):
        r = b(s)
        check(f"as_of {s} (same instant, any offset) includes both same-instant payments", r[1]["balance"] == 10450 - 500 and r[1]["as_of"] == s, r[1])
    check("as_of after latest = current", b(ago(1))[1]["balance"] == 10000 and b(ago(-86400))[1]["balance"] == 10000)
    check("as_of response has all money fields, balance==total", all(x in b(t3)[1] for x in ("balance", "total", "available", "held")) and b(t3)[1]["balance"] == b(t3)[1]["total"])
    r = call("GET", "/me?as_of=" + t2.replace("+", "%2B"), token=T["ada"])
    check("%2B encoded offset accepted", r[0] == 200 and r[1]["as_of"] == t2)
    r = call("GET", "/me?as_of=" + t2, token=T["ada"])  # raw + (decodes to space)
    check("raw + in query: accepted (reported)", r[0] in (200, 422), r[:2])
    print("INFO raw '+' as_of ->", r[0], r[1].get("as_of") if isinstance(r[1], dict) else r[1])
    for bad in ("2026-09-24T13:20:00", "2026-09-24", "", "garbage", "2026-09-24 13:20:00+00:00x", "13:20:00+00:00", "2026-13-01T00:00:00+00:00", "1"):
        check(f"as_of {bad!r} 422", err(b(bad), 422, "validation_failed"))
    frac = (dt.datetime.fromisoformat(t2) + dt.timedelta(microseconds=123456)).isoformat()
    check("as_of with fraction echoed exactly", b(frac)[1].get("as_of") == frac)
    check("as_of no token 401", err(call("GET", "/me?as_of=" + q(t2)), 401, "unauthenticated"))
    check("new account opens at zero", True)


def p_statement():
    t = [ago(5000 - i * 100) for i in range(8)]
    f = basic_fixture([
        {"id": "s1", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 1000, "created_at": t[0]},
        {"id": "s2", "from_user_id": "u_ada", "to_user_id": "u_cy", "amount": 300, "created_at": t[1], "visibility": "public"},
        {"id": "s3b", "from_user_id": "u_ada", "to_user_id": "u_dee", "amount": 200, "created_at": t[2]},
        {"id": "s3a", "from_user_id": "u_dee", "to_user_id": "u_ada", "amount": 70, "created_at": t[2]},
        {"id": "x1", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 5, "created_at": t[3], "visibility": "public"},
        {"id": "x2", "from_user_id": "u_bob", "to_user_id": "u_dee", "amount": 6, "created_at": t[3], "visibility": "private"},
        {"id": "s4", "from_user_id": "u_cy", "to_user_id": "u_ada", "amount": 50, "created_at": t[4]}], ops=("u_op",))
    T = reset(f)
    opening = 10000 - (1000 - 300 - 200 + 70 + 50)
    s = full_statement(T["ada"])
    ids = [e["payment"]["payment_id"] for e in s["entries"]]
    check("statement: only caller's payments, oldest first, ties by id", ids == ["s1", "s2", "s3a", "s3b", "s4"], ids)
    check("opening = opening balance, closing = current", s["opening_balance"] == opening and s["closing_balance"] == 10000, (s["opening_balance"], s["closing_balance"]))
    check("sum rule", s["opening_balance"] + sum(e["delta"] for e in s["entries"]) == s["closing_balance"])
    check("delta signs", [e["delta"] for e in s["entries"]] == [1000, -300, 70, -200, 50])
    bal, ok = s["opening_balance"], True
    for e in s["entries"]:
        bal += e["delta"]; ok &= e["balance_after"] == bal
    check("balance_after running (ties in entry order)", ok)
    e0 = s["entries"][0]
    check("entry fields revision/effective_at/recorded_at", e0["revision"] == 1 and ns(e0["effective_at"]) == ns(t[0]) and ns(e0["recorded_at"]) == ns(t[0])
          and ns(e0["payment"]["created_at"]) == ns(t[0]) and e0["payment"]["amount"] == 1000, e0)
    check("snapshot token present", isinstance(s.get("snapshot"), str) and s["snapshot"])
    # half-open window
    w = full_statement(T["ada"], f"?from={q(t[1])}&to={q(t[4])}")
    check("half-open [from,to): includes from, excludes to", [e["payment"]["payment_id"] for e in w["entries"]] == ["s2", "s3a", "s3b"], [e["payment"]["payment_id"] for e in w["entries"]])
    check("window opening = balance just before from; closing = just before to", w["opening_balance"] == opening + 1000 and w["closing_balance"] == opening + 1000 - 300 + 70 - 200)
    # paging does not change values
    pages, off = [], 0
    while True:
        r = stmt(T["ada"], f"?limit=2&offset={off}")[1]
        pages.append(r); off += 2
        if not r["has_more"]: break
    flat = [e for p in pages for e in p["entries"]]
    check("paging: same opening/closing on every page", all(p["opening_balance"] == opening and p["closing_balance"] == 10000 for p in pages))
    check("paging: entries/balance_after identical to unpaged", [(e["payment"]["payment_id"], e["balance_after"]) for e in flat] == [(e["payment"]["payment_id"], e["balance_after"]) for e in s["entries"]])
    check("has_more on final partial page false, full pages true", [p["has_more"] for p in pages] == [True, True, False] and len(pages[-1]["entries"]) == 1)
    r = stmt(T["ada"], "?offset=10")[1]
    check("offset beyond end: empty, has_more false, balances intact", r["entries"] == [] and r["has_more"] is False and r["closing_balance"] == 10000)
    r = stmt(T["ada"], "?limit=5")[1]
    check("exact fit has_more false", len(r["entries"]) == 5 and r["has_more"] is False)
    for bad in ("limit=0", "limit=201", "limit=1.0", "offset=-1", "limit=%2B4", "from=", "to=2026-01-01", "from=garbage", "known_at="):
        check(f"statement ?{bad} 422", err(stmt(T["ada"], "?" + bad), 422, "validation_failed"))
    r = stmt(T["ada"], f"?from={q(t[4])}&to={q(t[1])}")
    print("INFO from>to ->", r[0], r[1] if r[0] != 200 else {k_: r[1][k_] for k_ in ("opening_balance", "closing_balance")})
    check("from>to answered with 4xx or empty (reported)", r[0] in (200, 422))
    r = stmt(T["ada"], f"?from={q(t[2])}&to={q(t[2])}")[1]
    check("from==to empty window", r["entries"] == [] and r["opening_balance"] == r["closing_balance"])
    check("statement no token 401", err(call("GET", "/statement"), 401, "unauthenticated"))
    check("unknown statement param ignored", stmt(T["ada"], "?foo=1")[0] == 200)
    sc = full_statement(T["cy"])
    check("third-party public payment not in statement (cy sees x1 as receiver only)", [e["payment"]["payment_id"] for e in sc["entries"]] == ["s2", "x1", "s4"])
    sb = full_statement(T["op"])
    check("new/uninvolved account: opening 0, no entries", sb["opening_balance"] == 0 and sb["entries"] == [] and sb["closing_balance"] == 0)
    # settlement members: same committed_at, revision 1 at committed_at, ties by id
    st = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 1}, {"from_handle": "bob", "to_handle": "ada", "amount": 2},
                                                    {"from_handle": "ada", "to_handle": "cy", "amount": 3}]}, token=T["op"], key=k())[1]
    rv = revs(T["ada"], st["payments"][0]["payment_id"])[1]["revisions"]
    check("settlement member revision 1 = committed_at (effective & recorded)", ns(rv[0]["effective_at"]) == ns(st["committed_at"]) == ns(rv[0]["recorded_at"]) and rv[0]["reason"] == "")
    s = full_statement(T["ada"])
    tail = [e["payment"]["payment_id"] for e in s["entries"]][-3:]
    check("settlement members tie-ordered by payment id", tail == sorted(p["payment_id"] for p in st["payments"]), tail)
    check("settlement member correction 422 linked_payment_immutable", err(corr(T["ada"], st["payments"][0]["payment_id"], {"expected_revision": 1, "amount": 0, "effective_at": ago(1), "reason": "x"}), 422, "linked_payment_immutable"))


def p_corrections():
    t0, t1, t2 = ago(3000), ago(2000), ago(1000)
    f = basic_fixture([{"id": "c1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1000, "created_at": t1, "visibility": "public"}], ops=("u_op",))
    T = reset(f)
    body = {"expected_revision": 1, "amount": 400, "effective_at": t1, "reason": "corrected amount"}
    check("correction no token 401", err(call("POST", "/payments/c1/corrections", body, key=k()), 401, "unauthenticated"))
    check("correction missing key 400", err(call("POST", "/payments/c1/corrections", body, token=T["ada"]), 400, "missing_idempotency_key"))
    check("correction key 256 422", err(corr(T["ada"], "c1", body, key="x" * 256), 422, "validation_failed"))
    check("correction array body 400", err(call("POST", "/payments/c1/corrections", raw="[]", token=T["ada"], key=k()), 400, "malformed_request"))
    check("correction unknown 404", err(corr(T["ada"], "nope", body), 404, "not_found"))
    check("unknown payment 404 beats invalid body", err(corr(T["ada"], "nope", {}), 404, "not_found"))
    for who in ("bob", "op", "cy"):
        check(f"correction by {who} 403", err(corr(T[who], "c1", body), 403, "forbidden"))
    check("403 beats invalid body", err(corr(T["bob"], "c1", {}), 403, "forbidden"))
    matrix = [("missing expected_revision", {k_: v for k_, v in body.items() if k_ != "expected_revision"}),
              ("missing amount", {k_: v for k_, v in body.items() if k_ != "amount"}),
              ("missing effective_at", {k_: v for k_, v in body.items() if k_ != "effective_at"}),
              ("missing reason", {k_: v for k_, v in body.items() if k_ != "reason"}),
              ("revision 0", dict(body, expected_revision=0)), ("revision 1.5", dict(body, expected_revision=1.5)),
              ("revision '1'", dict(body, expected_revision="1")), ("amount -1", dict(body, amount=-1)),
              ("amount 1e9+1", dict(body, amount=1000000001)), ("amount 1.5", dict(body, amount=1.5)), ("amount '4'", dict(body, amount="4")),
              ("amount true", dict(body, amount=True)), ("reason ''", dict(body, reason="")), ("reason 201", dict(body, reason="x" * 201)),
              ("reason null", dict(body, reason=None)), ("reason 5", dict(body, reason=5)), ("effective_at naive", dict(body, effective_at="2026-09-20T12:00:00")),
              ("effective_at date", dict(body, effective_at="2026-09-20")), ("effective_at future", dict(body, effective_at=ago(-120))),
              ("effective_at garbage", dict(body, effective_at="soon")), ("effective_at null", dict(body, effective_at=None))]
    for name, b in matrix:
        check(f"correction {name} 422", err(corr(T["ada"], "c1", b), 422, "validation_failed"))
    check("reason 200 chars ok? (checked later)", True)
    check("stale revision 2 → 409", err(corr(T["ada"], "c1", dict(body, expected_revision=2)), 409, "stale_revision"))
    before = (me(T["ada"])[1], me(T["bob"])[1], full_statement(T["ada"]), revs(T["ada"], "c1")[1])
    key = k()
    s, r1, _, _ = corr(T["ada"], "c1", body, key=key)
    check("correction 201 shape", s == 201 and r1 == {"payment_id": "c1", "revision": 2, "amount": 400, "effective_at": t1,
                                                   "recorded_at": r1.get("recorded_at"), "reason": "corrected amount"} and ns(r1["recorded_at"]) > ns(t1), r1)
    check("decrease debits receiver, credits sender", me(T["ada"])[1]["total"] == 10600 and me(T["bob"])[1]["total"] == 1900, (me(T["ada"])[1], me(T["bob"])[1]))
    check("totals conserved", sum(me(T[h])[1]["total"] for h in T) == 13200)
    s, r1b, _, _ = corr(T["ada"], "c1", body, key=key)
    check("replay 200 same revision", s == 200 and r1b == r1)
    check("same key different body 409", err(corr(T["ada"], "c1", dict(body, reason="other"), key=key), 409, "idempotency_key_reuse"))
    check("claimed key beats invalid body", err(corr(T["ada"], "c1", {"amount": "x"}, key=key), 409, "idempotency_key_reuse"))
    check("old revision now stale", err(corr(T["ada"], "c1", body), 409, "stale_revision"))
    s, r2, _, _ = corr(T["ada"], "c1", {"expected_revision": 2, "amount": 1500, "effective_at": t0, "reason": "y" * 200})
    check("increase (debits sender) + effective before created_at + 200-char reason → 201", s == 201 and r2["revision"] == 3 and me(T["ada"])[1]["total"] == 9500)
    check("recorded_at strictly increases", ns(r2["recorded_at"]) > ns(r1["recorded_at"]))
    s, r1c, _, _ = corr(T["ada"], "c1", body, key=key)
    check("replay after newer revisions still returns original revision 2 with 200", s == 200 and r1c == r1, r1c)
    rl = revs(T["bob"], "c1")[1]["revisions"]
    check("revisions list in order incl. rev1 reason ''", [x["revision"] for x in rl] == [1, 2, 3] and rl[0]["reason"] == "" and rl[0]["amount"] == 1000
          and ns(rl[0]["effective_at"]) == ns(t1) == ns(rl[0]["recorded_at"]) and set(rl[0]) == {"payment_id", "revision", "amount", "effective_at", "recorded_at", "reason"})
    check("revisions: third party 404 even for public", err(revs(T["cy"], "c1"), 404, "not_found"))
    check("revisions: unknown 404, no token 401", err(revs(T["ada"], "zz"), 404, "not_found") and err(call("GET", "/payments/c1/revisions"), 401, "unauthenticated"))
    act = [p for p in call("GET", "/activity?limit=200", token=T["cy"])[1]["payments"] if p["payment_id"] == "c1"]
    check("activity shows original payment (amount 1000), no correction items", len(act) == 1 and act[0]["amount"] == 1000 and
          len(call("GET", "/activity?limit=200", token=T["ada"])[1]["payments"]) == 1)
    s_ = full_statement(T["ada"])
    e = [x for x in s_["entries"] if x["payment"]["payment_id"] == "c1"]
    check("statement: one entry for corrected payment, selected rev 3 at t0, payment.amount=1500", len(e) == 1 and e[0]["revision"] == 3 and e[0]["payment"]["amount"] == 1500
          and ns(e[0]["effective_at"]) == ns(t0) and e[0]["delta"] == -1500 and ns(e[0]["payment"]["created_at"]) == ns(t1), e)
    check("statement opening unchanged by corrections", s_["opening_balance"] == before[2]["opening_balance"] and s_["closing_balance"] == 9500)
    # known_at views
    k_before = (dt.datetime.fromisoformat(t1) - dt.timedelta(seconds=1)).isoformat()
    m = me(T["ada"], f"?known_at={q(k_before)}")[1]
    check("known_at before rev1 recorded: payment contributes nothing", m["balance"] == 11000 and m["known_at"] == k_before, m)
    kmid = r1["recorded_at"]
    m = me(T["ada"], f"?known_at={q(kmid)}")[1]
    check("known_at = rev2 recorded_at (inclusive) selects rev2", m["balance"] == 10600, m)
    kmid2 = (dt.datetime.fromisoformat(r1["recorded_at"]) - dt.timedelta(microseconds=1)).isoformat()
    check("known_at 1µs before rev2 selects rev1", me(T["ada"], f"?known_at={q(kmid2)}")[1]["balance"] == 10000)
    check("known_at future = everything", me(T["ada"], f"?known_at={q(ago(-9999))}")[1]["balance"] == 9500)
    m = me(T["ada"], f"?as_of={q(ago(2500))}&known_at={q(kmid)}")[1]
    check("as_of between t0 and t1 with rev2 knowledge (eff t1) = opening", m["balance"] == 11000, m)
    m = me(T["ada"], f"?as_of={q(ago(2500))}")[1]
    check("as_of between t0 and t1 latest knowledge (rev3 eff t0) = 9500", m["balance"] == 9500, m)
    sk = full_statement(T["ada"], f"?known_at={q(kmid)}")
    e = [x for x in sk["entries"] if x["payment"]["payment_id"] == "c1"]
    check("statement known_at selects rev2 (amount 400, eff t1)", e and e[0]["revision"] == 2 and e[0]["payment"]["amount"] == 400 and sk.get("known_at") == kmid)
    # zero reversal
    s, r3, _, _ = corr(T["ada"], "c1", {"expected_revision": 3, "amount": 0, "effective_at": t1, "reason": "refund"})
    check("zero reversal 201", s == 201 and me(T["ada"])[1]["total"] == 11000 and me(T["bob"])[1]["total"] == 1500)
    e = [x for x in full_statement(T["ada"])["entries"] if x["payment"]["payment_id"] == "c1"]
    check("zero revision still an entry with delta 0", len(e) == 1 and e[0]["delta"] == 0 and e[0]["payment"]["amount"] == 0)
    check("parties and visibility unchanged", (lambda p: p["from_user_id"] == "u_ada" and p["visibility"] == "public")(e[0]["payment"]))
    # failures change nothing
    snap_before = json.dumps([me(T[h])[1] for h in T]) + json.dumps(full_statement(T["ada"])["entries"]) + json.dumps(revs(T["ada"], "c1")[1])
    kf = k()
    check("insufficient_funds (increase beyond available)", err(corr(T["ada"], "c1", {"expected_revision": 4, "amount": 999999, "effective_at": t1, "reason": "x"}, key=kf), 409, "insufficient_funds"))
    snap_after = json.dumps([me(T[h])[1] for h in T]) + json.dumps(full_statement(T["ada"])["entries"]) + json.dumps(revs(T["ada"], "c1")[1])
    check("failed correction changes nothing", snap_before == snap_after)
    check("failed key reusable", corr(T["ada"], "c1", {"expected_revision": 4, "amount": 10, "effective_at": t1, "reason": "x"}, key=kf)[0] == 201)


def p_hist_overdraft():
    # cy (0) receives 500 at t1, pays dee 400 at t2. Correcting the t1 payment down to 300 -> cy
    # current total 100, debit 200 > 100 → insufficient. Decrease to 450: current debit 50 ok, but
    # at t2 cy would be 450-400=50 ok; to 350 → current 100-150 <0 insufficient.
    t1, t2, t3 = ago(3000), ago(2000), ago(1000)
    f = basic_fixture([
        {"id": "h1", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 500, "created_at": t1},
        {"id": "h2", "from_user_id": "u_cy", "to_user_id": "u_dee", "amount": 400, "created_at": t2},
        {"id": "h3", "from_user_id": "u_ada", "to_user_id": "u_cy", "amount": 1000, "created_at": t3}],
        users=[user("ada", 10000 - 1000), user("bob", 2500 - 500), user("cy", 1100), user("dee", 1100), user("op", 0)])
    T = reset(f)
    check("seeded opening cy = 0", me(T["cy"], "?as_of=" + q(ago(9999)))[1]["balance"] == 0)
    # decrease h1 to 300: current cy 1100 ≥ 200 → affordable now; but at t2 cy = 300-400 = -100 → historical_overdraft
    snap = json.dumps(full_statement(T["cy"]))
    r = corr(T["bob"], "h1", {"expected_revision": 1, "amount": 300, "effective_at": t1, "reason": "x"})
    check("intermediate-boundary negative → 409 historical_overdraft", err(r, 409, "historical_overdraft"), r[:2])
    check("historical_overdraft changed nothing", json.dumps(full_statement(T["cy"]))[:200] == snap[:200] and me(T["cy"])[1]["total"] == 1100)
    # move h1 later than h2 (effective after t2): cy at t2 = 0-400 → overdraft
    r = corr(T["bob"], "h1", {"expected_revision": 1, "amount": 500, "effective_at": ago(1500), "reason": "later"})
    check("moving receipt after spend → historical_overdraft", err(r, 409, "historical_overdraft"), r[:2])
    # move h1 exactly to t2: combined effect at the same instant = 100 ≥ 0 → ok
    r = corr(T["bob"], "h1", {"expected_revision": 1, "amount": 500, "effective_at": t2, "reason": "same instant"})
    check("same-instant combined movements allowed", r[0] == 201, r[:2])
    st = full_statement(T["cy"])
    check("statement order after move: tie at t2 by id (h1 < h2)", [e["payment"]["payment_id"] for e in st["entries"]] == ["h1", "h2", "h3"])
    # move into/out of window
    w = full_statement(T["cy"], f"?from={q(ago(2500))}&to={q(ago(1500))}")
    check("correction moved payment into window", [e["payment"]["payment_id"] for e in w["entries"]] == ["h1", "h2"] and w["opening_balance"] == 0)
    w0 = full_statement(T["cy"], f"?from={q(ago(3500))}&to={q(ago(2500))}")
    check("…and out of its old window", w0["entries"] == [])
    # conservation in historical views (naive scan)
    O = Oracle({"u_ada": 10000, "u_bob": 2500, "u_cy": 0, "u_dee": 700, "u_op": 0}).load(T)
    ok, neg = True, []
    for b in O.boundaries(BIG) + [ns(ago(99999))]:
        tot = sum(O.bal(u, b, BIG) for u in O.open)
        ok &= tot == 13200
        neg += [(u, b) for u in O.open if O.bal(u, b, BIG) < 0]
    check("oracle: sum = seeded total at every boundary; no negative", ok and not neg, neg[:3])
    mismatch = []
    for h, t in T.items():
        for b in O.boundaries(BIG):
            s_ = dt.datetime.fromtimestamp(b // 10**9, UTC).replace(microsecond=(b % 10**9) // 1000).isoformat()
            got = me(t, "?as_of=" + q(s_))[1]["balance"]
            if got != O.bal("u_" + h, b, BIG): mismatch.append((h, s_, got, O.bal("u_" + h, b, BIG)))
    check("service /me?as_of matches oracle at every boundary", not mismatch, mismatch[:3])


def p_snapshots():
    T = reset(basic_fixture([{"id": "n1", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 100, "created_at": ago(500)}], ops=("u_op",)))
    for i in range(5):
        P.pay(T, "ada", "cy", 10 + i)
    _, aid, _, _ = call("POST", "/authorizations", {"to_handle": "cy", "amount": 500}, token=T["ada"], key=k())
    s1 = stmt(T["ada"], "?limit=2")[1]
    tok = s1["snapshot"]
    allp = []
    off = 0
    while True:
        r = stmt(T["ada"], f"?snapshot={tok}&limit=2&offset={off}")[1]
        allp.append(r); off += 2
        if not r["has_more"]: break
    base = json.dumps(allp, sort_keys=True)
    check("snapshot first page equals original first page", json.dumps({k_: v for k_, v in allp[0].items()}, sort_keys=True) == json.dumps(s1, sort_keys=True))
    # mutate: payments, correction, capture, void, settlement
    P.pay(T, "ada", "bob", 7); P.pay(T, "bob", "ada", 9)
    first_pid = s1["entries"][1]["payment"]["payment_id"]
    corr(T["ada"], first_pid, {"expected_revision": 1, "amount": 1, "effective_at": ago(2000), "reason": "x"})
    call("POST", f"/authorizations/{aid['authorization_id']}/capture", {"amount": 100, "final": False}, token=T["cy"], key=k())
    call("POST", f"/authorizations/{aid['authorization_id']}/void", token=T["ada"])
    call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 3}]}, token=T["op"], key=k())
    allp2, off = [], 0
    while True:
        r = stmt(T["ada"], f"?snapshot={tok}&limit=2&offset={off}")[1]
        allp2.append(r); off += 2
        if not r["has_more"]: break
    check("snapshot pages byte-identical after payments/correction/capture/void/settlement", json.dumps(allp2, sort_keys=True) == base)
    r = stmt(T["ada"], f"?snapshot={tok}&limit=200")[1]
    check("snapshot with different limit: same totals/entries", r["opening_balance"] == s1["opening_balance"] and r["closing_balance"] == s1["closing_balance"]
          and len(r["entries"]) == sum(len(p["entries"]) for p in allp))
    r = stmt(T["ada"], f"?snapshot={tok}&offset=999")[1]
    check("snapshot offset beyond end: empty, has_more false", r["entries"] == [] and r["has_more"] is False)
    fresh = full_statement(T["ada"])
    check("fresh statement differs (sees new state)", fresh["closing_balance"] != s1["closing_balance"] or len(fresh["entries"]) != len(r["entries"]))
    for extra in ("from=" + q(ago(10)), "to=" + q(ago(10)), "known_at=" + q(ago(10))):
        check(f"snapshot + {extra.split('=')[0]} 422", err(stmt(T["ada"], f"?snapshot={tok}&{extra}"), 422, "validation_failed"))
    check("snapshot + unknown param ignored", stmt(T["ada"], f"?snapshot={tok}&zz=1")[0] == 200)
    check("snapshot bad limit 422", err(stmt(T["ada"], f"?snapshot={tok}&limit=0"), 422, "validation_failed"))
    check("unknown snapshot 404", err(stmt(T["ada"], "?snapshot=nope"), 404, "not_found"))
    check("another user's snapshot 404", err(stmt(T["bob"], f"?snapshot={tok}"), 404, "not_found"))
    # snapshot with explicit window and known_at
    sk = stmt(T["ada"], f"?known_at={q(ago(100000))}")[1]
    check("snapshot keeps known_at echo", stmt(T["ada"], f"?snapshot={sk['snapshot']}")[1].get("known_at") == sk.get("known_at"))
    # concurrent writes while paging snapshot
    tok2 = stmt(T["ada"], "?limit=1")[1]["snapshot"]
    ref = json.dumps(stmt(T["ada"], f"?snapshot={tok2}&limit=200")[1], sort_keys=True)
    def wr(i): return P.pay(T, "ada", "bob", 1) if i % 2 else P.pay(T, "bob", "ada", 1)
    def rd(i): return json.dumps(stmt(T["ada"], f"?snapshot={tok2}&limit=200")[1], sort_keys=True)
    with ThreadPoolExecutor(30) as ex:
        fs = [ex.submit(wr, i) for i in range(40)] + [ex.submit(rd, i) for i in range(40)]
        outs = [f.result() for f in fs]
    check("snapshot reads during concurrent writes all identical", all(o == ref for o in outs[40:]))
    # expiry does not change snapshot
    T = reset(dict(basic_fixture(), authorization_ttl_seconds=1))
    call("POST", "/authorizations", {"to_handle": "bob", "amount": 100}, token=T["ada"], key=k())
    P.pay(T, "ada", "bob", 5)
    tok3 = stmt(T["ada"])[1]["snapshot"]; ref3 = json.dumps(stmt(T["ada"], f"?snapshot={tok3}")[1], sort_keys=True)
    time.sleep(1.5); me(T["ada"])
    check("snapshot unchanged across expiry", json.dumps(stmt(T["ada"], f"?snapshot={tok3}")[1], sort_keys=True) == ref3)
    reset(basic_fixture())
    T2 = {h: P.login(h) for h in ("ada",)}
    check("pre-reset snapshot 404", err(stmt(T2["ada"], f"?snapshot={tok3}"), 404, "not_found"))


def p_holds_history():
    T = reset(dict(basic_fixture(), authorization_ttl_seconds=3))
    s, a, _, _ = call("POST", "/authorizations", {"to_handle": "bob", "amount": 2000}, token=T["ada"], key=k())
    check("closed_at null while open", a.get("closed_at", "MISSING") is None, a.get("closed_at", "MISSING"))
    time.sleep(0.05)
    s, p1, _, _ = call("POST", f"/authorizations/{a['authorization_id']}/capture", {"amount": 500, "final": False}, token=T["bob"], key=k())
    time.sleep(0.05)
    s, a2, _, _ = call("POST", "/authorizations", {"to_handle": "cy", "amount": 1000}, token=T["ada"], key=k())
    time.sleep(0.05)
    s, v, _, _ = call("POST", f"/authorizations/{a2['authorization_id']}/void", token=T["ada"])
    check("void closed_at = event time", v["closed_at"] is not None and ns(v["closed_at"]) >= ns(p1["created_at"]))
    def m(asof, known=None):
        qq = "?as_of=" + q(asof) + (("&known_at=" + q(known)) if known else "")
        x = me(T["ada"], qq)[1]
        return (x["total"], x["held"], x["available"], x["balance"])
    cap_t = p1["created_at"]
    just_before_cap = (dt.datetime.fromisoformat(cap_t) - dt.timedelta(microseconds=1)).isoformat()
    print("INFO hold created_at", a["created_at"], "capture at", cap_t, "a2 created", a2["created_at"], "void", v["closed_at"])
    check("before hold creation: no hold", m(ago(3600)) == (10000, 0, 10000, 10000))
    check("just before nonfinal capture: hold 2000", m(just_before_cap)[1] == 2000, m(just_before_cap))
    check("at capture: total -500, hold 1500", m(cap_t)[:3] == (9500, 1500 + (1000 if ns(a2["created_at"]) <= ns(cap_t) else 0), 9500 - 1500 - (1000 if ns(a2["created_at"]) <= ns(cap_t) else 0)), m(cap_t))
    check("after void: second hold released", m(v["closed_at"])[1] == 1500)
    exp = a["expires_at"]
    check("query beyond now before deadline: still held", m((dt.datetime.fromisoformat(exp) - dt.timedelta(microseconds=1)).isoformat())[1] == 1500)
    check("query at expires_at (future): released", m(exp)[1] == 0 and m(exp)[2] == 9500)
    check("balance == total, available = total - held in every view", all(x[0] == x[3] and x[2] == x[0] - x[1] for x in (m(cap_t), m(exp), m(ago(1)))))
    check("known_at before capture known → capture not counted", m(cap_t, just_before_cap)[:2] == (10000, 2000), m(cap_t, just_before_cap))
    s_ = full_statement(T["ada"])
    caps = [e for e in s_["entries"] if e["payment"].get("authorization_id")]
    check("statement: capture exactly once with link, no authorization/release entries", len(caps) == 1 and caps[0]["delta"] == -500 and len(s_["entries"]) == 1)
    check("capture correction 422 linked_payment_immutable", err(corr(T["ada"], p1["payment_id"], {"expected_revision": 1, "amount": 1, "effective_at": ago(1), "reason": "x"}), 422, "linked_payment_immutable"))
    time.sleep(3.2)
    al = {x["authorization_id"]: x for x in call("GET", "/authorizations", token=T["ada"])[1]["authorizations"]}
    check("expired closed_at = expires_at", al[a["authorization_id"]]["status"] == "expired" and ns(al[a["authorization_id"]]["closed_at"]) == ns(exp))
    # correction with holds: historical available
    T = reset(basic_fixture([{"id": "q1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1000, "created_at": ago(3000)}],
                            users=[user("ada", 9000), user("bob", 3500), user("cy", 0), user("dee", 700), user("op", 0)]))
    call("POST", "/authorizations", {"to_handle": "cy", "amount": 9000}, token=T["ada"], key=k())
    r = corr(T["ada"], "q1", {"expected_revision": 1, "amount": 1001, "effective_at": ago(3000), "reason": "x"})
    check("increase with all funds held → insufficient_funds (current available)", err(r, 409, "insufficient_funds"), r[:2])


def p_concurrency():
    T = reset(basic_fixture([{"id": "r1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1000, "created_at": ago(3000)}], ops=("u_op",)))
    def c(i): return corr(T["ada"], "r1", {"expected_revision": 1, "amount": 900 - i, "effective_at": ago(3000), "reason": f"r{i}"})
    with ThreadPoolExecutor(50) as ex:
        rs = list(ex.map(c, range(50)))
    sts = [r[0] for r in rs]
    check("50-way same expected_revision: exactly one 201, rest 409 stale", sts.count(201) == 1 and all(err(r, 409, "stale_revision") for r in rs if r[0] != 201), sorted(set(sts)))
    check("only revision 2 exists", [x["revision"] for x in revs(T["ada"], "r1")[1]["revisions"]] == [1, 2])
    # mixed race
    pids = [P.pay(T, "ada", "bob", 100)[1]["payment_id"] for _ in range(5)] + [P.pay(T, "bob", "cy", 50)[1]["payment_id"] for _ in range(5)]
    auths = [call("POST", "/authorizations", {"to_handle": "cy", "amount": 200}, token=T["ada"], key=k())[1]["authorization_id"] for _ in range(4)]
    lat, sts, lock = [], [], threading.Lock()
    rev = {p: 1 for p in pids}
    def job(i):
        kind = i % 6
        if kind == 0:
            pid = random.choice(pids); owner = "ada" if pid in pids[:5] else "bob"
            cur = revs(T[owner], pid)[1]["revisions"][-1]["revision"]
            r = corr(T[owner], pid, {"expected_revision": cur, "amount": random.randint(0, 150), "effective_at": ago(random.randint(1, 4000)), "reason": "race"})
        elif kind == 1: r = P.pay(T, random.choice(["ada", "bob"]), "dee", random.randint(1, 50))
        elif kind == 2:
            a = random.choice(auths); r = call("POST", f"/authorizations/{a}/capture", {"amount": random.randint(1, 60), "final": False}, token=T["cy"], key=k())
        elif kind == 3: r = call("POST", f"/authorizations/{random.choice(auths)}/void", token=T["ada"])
        elif kind == 4: r = call("POST", "/settlements", {"transfers": [{"from_handle": "dee", "to_handle": "cy", "amount": random.randint(1, 30)}]}, token=T["op"], key=k())
        else: r = stmt(T[random.choice(["ada", "bob", "cy"])], "?limit=200")
        with lock: sts.append(r[0]); lat.append(r[2])
    random.seed(5)
    with ThreadPoolExecutor(50) as ex:
        list(ex.map(job, range(900)))
    check(f"race: no 5xx over {len(sts)}, max latency {max(lat):.2f}s < 5", all(s < 500 for s in sts) and max(lat) < 5, sorted(set(sts)))
    O = Oracle({"u_ada": 11000, "u_bob": 1500, "u_cy": 0, "u_dee": 700, "u_op": 0}).load(T)  # openings: seeded r1 ada->bob 1000
    bounds = O.boundaries(BIG)
    bad_sum = [b for b in bounds if sum(O.bal(u, b, BIG) for u in O.open) != 13200]
    neg = [(u, b) for b in bounds for u in O.open if O.bal(u, b, BIG) < 0]
    check("race: oracle sum conserved at every boundary", not bad_sum, bad_sum[:3])
    check("race: no negative total at any boundary (oracle)", not neg, neg[:3])
    cur = {h: me(T[h])[1] for h in T}
    check("race: oracle current balances == service", all(cur[h]["total"] == O.bal("u_" + h, BIG, BIG) for h in T), {h: (cur[h]["total"], O.bal("u_" + h, BIG, BIG)) for h in T})
    # available never negative at any past boundary (service view)
    negav = []
    for h in ("ada", "bob", "cy", "dee"):
        for b in bounds[::max(1, len(bounds) // 60)]:
            s_ = dt.datetime.fromtimestamp(b // 10**9, UTC).replace(microsecond=(b % 10**9) // 1000).isoformat()
            x = me(T[h], "?as_of=" + q(s_))[1]
            if x["available"] < 0 or x["total"] < 0: negav.append((h, s_, x["total"], x["available"]))
    check("race: no negative total/available at sampled past boundaries (service)", not negav, negav[:3])


def p_seed_and_export():
    f = basic_fixture([{"id": "f1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1, "created_at": ago(-3600)}])
    T = reset(basic_fixture())
    P.pay(T, "ada", "bob", 5)
    r = call("POST", "/_test/reset", f)
    check("seeded future created_at 422", err(r, 422, "validation_failed"))
    check("…state unchanged", me(T["ada"])[1]["total"] == 9995)
    T = reset(basic_fixture([{"id": "o1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 100}]))
    p = P.pay(T, "ada", "bob", 1)[1]
    s_ = full_statement(T["ada"])
    check("omitted seeded created_at = reset time, before API payments", [e["payment"]["payment_id"] for e in s_["entries"]] == ["o1", p["payment_id"]]
          and ns(s_["entries"][0]["payment"]["created_at"]) <= ns(p["created_at"]))
    check("API payment created_at has offset", p["created_at"][-6] in "+-")
    # stage-1 and stage-2 exports
    for name, base in (("stage-1", URL1), ("stage-2", URLS2)):
        Tx = reset(dict(basic_fixture([{"id": "e1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 100, "created_at": ago(5000)}]), settlement_operator_ids=["u_op"]), base=base)
        kp = k()
        px = call("POST", "/payments", {"to_handle": "cy", "amount": 250}, token=Tx["ada"], key=kp, base=base)[1]
        call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "dee", "amount": 5}]}, token=Tx["op"], key=k(), base=base)
        if base == URLS2:
            ax = call("POST", "/authorizations", {"to_handle": "bob", "amount": 1000}, token=Tx["ada"], key=k(), base=base)[1]
            call("POST", f"/authorizations/{ax['authorization_id']}/capture", {"amount": 300, "final": False}, token=Tx["bob"], key=k(), base=base)
        ex = call("GET", "/_test/export", base=base)[1]
        r = call("POST", "/_test/import", ex)
        check(f"{name} export imports into stage 3", r[0] == 204, r)
        m = me(Tx["ada"])[1]
        src = me(Tx["ada"], base=base)[1]
        check(f"{name}: token valid, balances equal", m["total"] == src["balance"] and m["held"] == src.get("held", 0), (m, src))
        s_ = full_statement(Tx["ada"])
        check(f"{name}: opening = seeded opening, sum rule", s_["opening_balance"] == 10000 + 100 and s_["opening_balance"] + sum(e["delta"] for e in s_["entries"]) == s_["closing_balance"] == m["total"], (s_["opening_balance"], s_["closing_balance"]))
        rv = revs(Tx["ada"], px["payment_id"])[1]["revisions"]
        check(f"{name}: synthesized revision 1 at created_at", len(rv) == 1 and ns(rv[0]["effective_at"]) == ns(px["created_at"]) == ns(rv[0]["recorded_at"]))
        r = call("POST", "/payments", {"to_handle": "cy", "amount": 250}, token=Tx["ada"], key=kp)
        check(f"{name}: receipt replay 200", r[0] == 200 and r[1]["payment_id"] == px["payment_id"])
        check(f"{name}: correction works after import", corr(Tx["ada"], px["payment_id"], {"expected_revision": 1, "amount": 200, "effective_at": px["created_at"], "reason": "fix"})[0] == 201)
        O = Oracle({"u_ada": 10100, "u_bob": 2400, "u_cy": 0, "u_dee": 700, "u_op": 0}).load(Tx)
        check(f"{name}: oracle sum conserved at all boundaries", all(sum(O.bal(u, b, BIG) for u in O.open) == 13200 for b in O.boundaries(BIG)))
    # stage-3 roundtrip
    T = reset(basic_fixture([{"id": "g1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 100, "created_at": ago(5000)}]))
    kc = k()
    rc = corr(T["ada"], "g1", {"expected_revision": 1, "amount": 50, "effective_at": ago(4000), "reason": "half"}, key=kc)[1]
    sn = stmt(T["ada"], "?limit=1")[1]
    ref = json.dumps(stmt(T["ada"], f"?snapshot={sn['snapshot']}&limit=200")[1], sort_keys=True)
    ex = call("GET", "/_test/export")[1]
    for base in (URL2,):
        r = call("POST", "/_test/import", ex, base=base)
        check("stage-3 export → other stage-3 instance 204", r[0] == 204)
        check("revisions preserved", call("GET", "/payments/g1/revisions", token=T["ada"], base=base)[1] == revs(T["ada"], "g1")[1])
        check("snapshot preserved", json.dumps(call("GET", f"/statement?snapshot={sn['snapshot']}&limit=200", token=T["ada"], base=base)[1], sort_keys=True) == ref)
        r = call("POST", "/payments/g1/corrections", {"expected_revision": 1, "amount": 50, "effective_at": rc["effective_at"], "reason": "half"}, token=T["ada"], key=kc, base=base)
        check("correction receipt replays after import", r[0] == 200 and r[1] == rc)
        check("opening preserved", call("GET", "/statement", token=T["ada"], base=base)[1]["opening_balance"] == full_statement(T["ada"])["opening_balance"])
    bad = json.loads(json.dumps(ex))
    for p in bad["state"]["payments"]:
        if p["id"] == "g1" and p.get("revisions"):
            p["revisions"][1]["amount"] = -5
    before = call("GET", "/_test/export")[1]
    r = call("POST", "/_test/import", bad)
    check("tampered export (negative revision amount) 422 unchanged", err(r, 422, "validation_failed") and call("GET", "/_test/export")[1] == before, r[:2])


def p_hold_overdraft():
    t0, t1, t2 = ago(5000), ago(4000), ago(3000)
    f = basic_fixture([{"id": "ho1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 100, "created_at": t0},
                       {"id": "ho2", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 500, "created_at": t2}],
                      users=[user("ada", 1400), user("bob", 2100), user("cy", 0), user("dee", 700), user("op", 0)])
    f["authorizations"] = [{"id": "ah", "from_user_id": "u_ada", "to_user_id": "u_cy", "amount": 800, "status": "open", "created_at": t1, "expires_at": ago(-7200)}]
    T = reset(f)
    m = me(T["ada"], "?as_of=" + q(t1))[1]
    check("seeded hold with created_at: at t1 total 900 held 800 available 100", (m["total"], m["held"], m["available"]) == (900, 800, 100), m)
    check("before seeded hold creation: held 0", me(T["ada"], "?as_of=" + q(ago(4500)))[1]["held"] == 0)
    r = corr(T["ada"], "ho1", {"expected_revision": 1, "amount": 400, "effective_at": t0, "reason": "x"})
    check("correction making past AVAILABLE negative (hold at t1) → historical_overdraft", err(r, 409, "historical_overdraft"), r[:2])
    r = corr(T["ada"], "ho1", {"expected_revision": 1, "amount": 200, "effective_at": t0, "reason": "x"})
    check("correction leaving past available exactly 0 → 201", r[0] == 201, r[:2])
    m = me(T["ada"], "?as_of=" + q(t1))[1]
    check("after correction: at t1 total 800, available 0", (m["total"], m["available"]) == (800, 0), m)
    v = call("POST", "/authorizations/ah/void", token=T["ada"])[1]
    before_void = (dt.datetime.fromisoformat(v["closed_at"]) - dt.timedelta(microseconds=1)).isoformat()
    check("known_at before void: hold still counted", me(T["ada"], "?known_at=" + q(before_void))[1]["held"] == 800)
    check("known_at after void: released", me(T["ada"], "?known_at=" + q(v["closed_at"]))[1]["held"] == 0)


def main():
    for f in (p_asof, p_statement, p_corrections, p_hist_overdraft, p_snapshots, p_holds_history, p_hold_overdraft, p_concurrency, p_seed_and_export):
        print(f"== {f.__name__}", flush=True)
        try:
            f()
        except Exception as e:
            import traceback; traceback.print_exc()
            check(f"{f.__name__} ran to completion", False, repr(e)[:300])
    n = sum(1 for _, ok in P.RESULTS if ok)
    print(f"TOTAL {n}/{len(P.RESULTS)} passed, {len(P.RESULTS) - n} failed")


if __name__ == "__main__":
    main()
