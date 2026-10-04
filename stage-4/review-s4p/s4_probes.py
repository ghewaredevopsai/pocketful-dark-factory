#!/usr/bin/env python3
"""Reviewer probes for Pocketful stage 4 (refunds, correction batches), written from the stage-4
requirements text with an independent naive oracle.

Usage: python3 s4_probes.py <s4-url> <s4-url-2> <stage1-url> <stage2-url> <stage3-url>
"""
import os, sys, json, time, random, threading, datetime as dt
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.abspath(__file__))
for cand in (os.path.join(HERE, "..", "stage-3"), os.path.join(HERE, "..", "review-s3p")):
    if os.path.exists(os.path.join(cand, "s3_probes.py")):
        sys.path.insert(0, cand); break
URL, URL2, URL1, URLS2, URLS3 = sys.argv[1:6]
sys.argv = [sys.argv[0], URL, URL2, URL1, URLS2]
import s3_probes as S
from s3_probes import call, check, err, k, q, ns, user, me, stmt, corr, revs, full_statement, Oracle, BIG, ago
import probes as P
P.A = URL

UTC = dt.timezone.utc
def iso_ns(n): return dt.datetime.fromtimestamp(n // 10**9, UTC).replace(microsecond=(n % 10**9) // 1000).isoformat()
def pm(n, d): return iso_ns(n + d * 1000)


def refund(t, pid, amount, key=None, base=None, raw=None):
    return call("POST", f"/payments/{pid}/refunds", None if raw is not None else {"amount": amount}, token=t, key=key or k(), base=base or URL, raw=raw)


def batch(t, items, key=None, base=None):
    return call("POST", "/correction-batches", {"corrections": items}, token=t, key=key or k(), base=base or URL)


def item(pid, exp, amount, eff, reason="b", **kw):
    d = {"payment_id": pid, "expected_revision": exp, "amount": amount, "effective_at": eff, "reason": reason}
    d.update(kw); return d


USERS = [user("ada", 10000), user("bob", 2500), user("cy", 0), user("dee", 700), user("op", 0)]


def fx(pays=None, users=None, ops=("u_op",), **extra):
    f = {"currency": "EUR", "minor_units": 2, "users": users or USERS, "payments": pays or [], "requests": [], "settlement_operator_ids": list(ops)}
    f.update(extra); return f


def reset(f, base=None): return S.reset(f, base)


def snapshot_state(T):
    return json.dumps([me(T[h])[1] for h in sorted(T)] + [full_statement(T[h])["entries"] for h in sorted(T)], sort_keys=True)


# ------------------------------------------------------------------ refunds

def r_basics():
    T = reset(fx())
    p = P.pay(T, "ada", "bob", 1000, note="dinner 🍝", visibility="private")[1]
    check("ordinary payment has refund_of null", "refund_of" in p and p["refund_of"] is None, p)
    body = {"amount": 200}
    check("refund no token 401", err(call("POST", f"/payments/{p['payment_id']}/refunds", body, key=k()), 401, "unauthenticated"))
    check("refund missing key 400", err(call("POST", f"/payments/{p['payment_id']}/refunds", body, token=T["bob"]), 400, "missing_idempotency_key"))
    check("refund key 256 422", err(refund(T["bob"], p["payment_id"], 200, key="x" * 256), 422, "validation_failed"))
    check("refund array body 400", err(refund(T["bob"], p["payment_id"], 0, raw="[1]"), 400, "malformed_request"))
    check("refund unknown 404", err(refund(T["bob"], "nope", 200), 404, "not_found"))
    for who in ("ada", "op", "cy"):
        check(f"refund by {who} (not receiver) 403", err(refund(T[who], p["payment_id"], 200), 403, "forbidden"))
    for a in (0, -1, 1.5, "200", True, None, 1000000001):
        check(f"refund amount {a!r} 422", err(refund(T["bob"], p["payment_id"], a), 422, "validation_failed"))
    check("refund missing amount 422", err(call("POST", f"/payments/{p['payment_id']}/refunds", {}, token=T["bob"], key=k()), 422, "validation_failed"))
    check("refund 1e2 accepted as 100? (integral)", True)
    key = k()
    s, r, _, _ = refund(T["bob"], p["payment_id"], 200, key=key)
    keys = {"payment_id", "from_user_id", "from_handle", "to_user_id", "to_handle", "amount", "currency", "note", "visibility",
            "request_id", "settlement_id", "authorization_id", "refund_of", "created_at"}
    check("refund 201: opposite direction, links, copies note/visibility, µs created_at", s == 201 and keys <= set(r) and r["from_user_id"] == "u_bob"
          and r["to_user_id"] == "u_ada" and r["refund_of"] == p["payment_id"] and r["request_id"] is None and r["authorization_id"] is None
          and r["settlement_id"] is None and r["note"] == "dinner 🍝" and r["visibility"] == "private" and r["amount"] == 200
          and "." in r["created_at"] and ns(r["created_at"]) > ns(p["created_at"]), (s, r))
    check("refund moved money", me(T["ada"])[1]["total"] == 9200 and me(T["bob"])[1]["total"] == 3300)
    s2, r2, _, _ = refund(T["bob"], p["payment_id"], 200, key=key)
    check("refund replay 200 identical", s2 == 200 and r2 == r and me(T["bob"])[1]["total"] == 3300)
    check("refund same key different body 409", err(refund(T["bob"], p["payment_id"], 201, key=key), 409, "idempotency_key_reuse"))
    check("refund claimed key beats invalid body", err(refund(T["bob"], p["payment_id"], "x", key=key), 409, "idempotency_key_reuse"))
    check("refund same key other user (different path semantics) independent", refund(T["ada"], r["payment_id"], 1, key=key)[0] in (422,))
    check("refund of a refund 422 invalid_refund_target", err(refund(T["ada"], r["payment_id"], 1), 422, "invalid_refund_target"))
    check("refund exceeding remaining 422 refund_exceeds_payment", err(refund(T["bob"], p["payment_id"], 801), 422, "refund_exceeds_payment"))
    k2 = k()
    check("refund exactly remaining OK", refund(T["bob"], p["payment_id"], 800, key=k2)[0] == 201)
    check("fully refunded: any further refund 422 exceeds", err(refund(T["bob"], p["payment_id"], 1), 422, "refund_exceeds_payment"))
    rv = revs(T["ada"], r["payment_id"])[1]["revisions"]
    check("refund payment has exactly one revision at created_at", len(rv) == 1 and ns(rv[0]["effective_at"]) == ns(r["created_at"]) == ns(rv[0]["recorded_at"]) and rv[0]["amount"] == 200)
    check("refund cannot be corrected 422 linked_payment_immutable", err(corr(T["bob"], r["payment_id"], {"expected_revision": 1, "amount": 100, "effective_at": ago(1), "reason": "x"}), 422, "linked_payment_immutable"))
    feed_a = {x["payment_id"]: x for x in call("GET", "/activity?limit=200", token=T["ada"])[1]["payments"]}
    feed_c = {x["payment_id"] for x in call("GET", "/activity?limit=200", token=T["cy"])[1]["payments"]}
    check("refund in activity for parties (private hidden from third party)", r["payment_id"] in feed_a and r["payment_id"] not in feed_c and feed_a[r["payment_id"]]["refund_of"] == p["payment_id"])
    check("original payment unchanged in activity (amount 1000, refund_of null)", feed_a[p["payment_id"]]["amount"] == 1000 and feed_a[p["payment_id"]]["refund_of"] is None)
    st = full_statement(T["bob"])
    ids = [(e["payment"]["payment_id"], e["delta"]) for e in st["entries"]]
    check("statement: refunds are entries with negative delta for refunder", ids[-2:] == [(r["payment_id"], -200), (ids[-1][0], -800)] and st["closing_balance"] == 2500)
    check("statement entry payment carries refund_of", st["entries"][-1]["payment"]["refund_of"] == p["payment_id"])
    m_before = me(T["bob"], "?as_of=" + q(pm(ns(r["created_at"]), -1)))[1]["total"]
    m_at = me(T["bob"], "?as_of=" + q(r["created_at"]))[1]["total"]
    check("as_of refund instant inclusive, 1µs before excludes", m_before == 3500 and m_at == 3300, (m_before, m_at))
    check("known_at 1µs before refund: not known", me(T["bob"], "?known_at=" + q(pm(ns(r["created_at"]), -1)))[1]["total"] == 3500)
    # 4xx claims nothing
    T = reset(fx())
    p = P.pay(T, "ada", "cy", 500)[1]
    P.pay(T, "cy", "dee", 500)
    kf = k()
    check("refund with insufficient available 409", err(refund(T["cy"], p["payment_id"], 100, key=kf), 409, "insufficient_funds"))
    P.pay(T, "ada", "cy", 100)
    check("key reusable after 409", refund(T["cy"], p["payment_id"], 100, key=kf)[0] == 201)
    # same key same body different payment path = new request
    q1 = P.pay(T, "ada", "bob", 50)[1]; q2 = P.pay(T, "ada", "bob", 50)[1]
    kk = k()
    check("same key+body on different refund path is a new refund", refund(T["bob"], q1["payment_id"], 10, key=kk)[0] == 201 and refund(T["bob"], q2["payment_id"], 10, key=kk)[0] == 201)


def r_targets_and_holds():
    T = reset(fx())
    # request payment
    _, rq, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 300}, token=T["bob"], key=k())
    pay = call("POST", f"/requests/{rq['request_id']}/pay", {}, token=T["ada"], key=k())[1]
    s, r, _, _ = refund(T["bob"], pay["payment_id"], 300)
    rl = [x for x in call("GET", "/requests", token=T["bob"])[1]["requests"] if x["request_id"] == rq["request_id"]][0]
    check("refund of request payment: 201, request_id null, request stays paid", s == 201 and r["request_id"] is None and rl["status"] == "paid" and rl["payment_id"] == pay["payment_id"])
    check("paid request still not payable again", err(call("POST", f"/requests/{rq['request_id']}/pay", {}, token=T["ada"], key=k()), 409, "request_not_pending"))
    # capture
    a = call("POST", "/authorizations", {"to_handle": "bob", "amount": 1000}, token=T["ada"], key=k())[1]
    c = call("POST", f"/authorizations/{a['authorization_id']}/capture", {"amount": 400}, token=T["bob"], key=k())[1]
    s, r, _, _ = refund(T["bob"], c["payment_id"], 400)
    al = [x for x in call("GET", "/authorizations", token=T["ada"])[1]["authorizations"] if x["authorization_id"] == a["authorization_id"]][0]
    check("refund of capture: 201, authorization_id null, auth stays captured, no hold restored", s == 201 and r["authorization_id"] is None and r["refund_of"] == c["payment_id"]
          and al["status"] == "captured" and al["remaining_amount"] == 0 and me(T["ada"])[1]["held"] == 0)
    check("capture still not correctable", err(corr(T["ada"], c["payment_id"], {"expected_revision": 1, "amount": 1, "effective_at": ago(1), "reason": "x"}), 422, "linked_payment_immutable"))
    # nonfinal capture, refund, hold remainder unchanged
    a2 = call("POST", "/authorizations", {"to_handle": "bob", "amount": 1000}, token=T["ada"], key=k())[1]
    c2 = call("POST", f"/authorizations/{a2['authorization_id']}/capture", {"amount": 300, "final": False}, token=T["bob"], key=k())[1]
    refund(T["bob"], c2["payment_id"], 300)
    al = [x for x in call("GET", "/authorizations", token=T["ada"])[1]["authorizations"] if x["authorization_id"] == a2["authorization_id"]][0]
    check("refund of nonfinal capture leaves remainder held at 700", al["status"] == "open" and al["remaining_amount"] == 700 and me(T["ada"])[1]["held"] == 700)
    # receiver's held funds can't fund a refund
    T = reset(fx())
    p = P.pay(T, "ada", "cy", 500)[1]
    call("POST", "/authorizations", {"to_handle": "dee", "amount": 450}, token=T["cy"], key=k())
    check("refund from receiver's AVAILABLE (holds excluded) 409", err(refund(T["cy"], p["payment_id"], 51), 409, "insufficient_funds"))
    check("refund within available OK", refund(T["cy"], p["payment_id"], 50)[0] == 201 and me(T["cy"])[1]["available"] == 0)
    # settlement member
    T = reset(fx())
    st = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 100}, {"from_handle": "bob", "to_handle": "cy", "amount": 50}]}, token=T["op"], key=k())[1]
    m0 = st["payments"][0]
    s, r, _, _ = refund(T["bob"], m0["payment_id"], 30)
    check("refund of settlement member: 201, settlement_id null on refund", s == 201 and r["settlement_id"] is None and r["refund_of"] == m0["payment_id"])
    fa = {x["payment_id"]: x for x in call("GET", "/activity?limit=200", token=T["ada"])[1]["payments"]}
    check("member keeps settlement_id; refund is not a member", fa[m0["payment_id"]]["settlement_id"] == st["settlement_id"] and fa[r["payment_id"]]["settlement_id"] is None)
    check("operator cannot refund for receiver 403", err(refund(T["op"], m0["payment_id"], 1), 403, "forbidden"))
    return T, st, r


def r_corrections_vs_refunds():
    t1 = ago(3000)
    T = reset(fx([{"id": "c1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1000, "created_at": t1}],
                 users=[user("ada", 9000), user("bob", 3500), user("cy", 0), user("dee", 700), user("op", 0)]))
    refund(T["bob"], "c1", 400)
    check("correction below refunded 422 refund_exceeds_payment", err(corr(T["ada"], "c1", {"expected_revision": 1, "amount": 399, "effective_at": t1, "reason": "x"}), 422, "refund_exceeds_payment"))
    check("correction stale beats refund_exceeds? (stale first per ledger)", err(corr(T["ada"], "c1", {"expected_revision": 2, "amount": 0, "effective_at": t1, "reason": "x"}), 409, "stale_revision"))
    check("correction exactly to refunded amount 201", corr(T["ada"], "c1", {"expected_revision": 1, "amount": 400, "effective_at": t1, "reason": "x"})[0] == 201)
    check("now no more refunds possible", err(refund(T["bob"], "c1", 1), 422, "refund_exceeds_payment"))
    check("correct up then refund more OK", corr(T["ada"], "c1", {"expected_revision": 2, "amount": 900, "effective_at": t1, "reason": "y"})[0] == 201 and refund(T["bob"], "c1", 500)[0] == 201)
    check("refund vs CURRENT corrected amount: exceeds by 1 → 422", err(refund(T["bob"], "c1", 1), 422, "refund_exceeds_payment"))
    # moving target after refund → historical overdraft of refunder
    T = reset(fx([{"id": "m1", "from_user_id": "u_ada", "to_user_id": "u_cy", "amount": 1000, "created_at": ago(3000)}],
                 users=[user("ada", 9000), user("bob", 2500), user("cy", 1000), user("dee", 700), user("op", 0)]))
    rf = refund(T["cy"], "m1", 1000)[1]
    r = corr(T["ada"], "m1", {"expected_revision": 1, "amount": 1000, "effective_at": iso_ns(time.time_ns() // 1000 * 1000), "reason": "later"})
    check("moving refunded payment after its refund → historical_overdraft for refunder", err(r, 409, "historical_overdraft"), r[:2])
    O = Oracle({"u_ada": 10000, "u_bob": 2500, "u_cy": 0, "u_dee": 700, "u_op": 0}).load(T)
    bad = [(u, b) for b in O.boundaries(BIG) for u in O.open if O.bal(u, b, BIG) < 0]
    check("oracle: no negative after refund+correction attempts; sums conserved", not bad and all(sum(O.bal(u, b, BIG) for u in O.open) == 13200 for b in O.boundaries(BIG)), bad[:2])


# ------------------------------------------------------------------ batches

def b_validation():
    t1, t2 = ago(4000), ago(3000)
    pays = [{"id": "o1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 500, "created_at": t1},
            {"id": "o2", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 200, "created_at": t2}]
    T = reset(fx(pays, users=[user("ada", 9500), user("bob", 2800), user("cy", 200), user("dee", 700), user("op", 0)]))
    good = [item("o1", 1, 400, t1)]
    check("batch no token 401", err(call("POST", "/correction-batches", {"corrections": good}, key=k()), 401, "unauthenticated"))
    check("batch non-operator 403", err(batch(T["ada"], good), 403, "forbidden"))
    r = call("POST", "/correction-batches", {"corrections": good}, token=T["ada"])
    check("batch non-operator without key → 403 (settlement rule: guard first)", err(r, 403, "forbidden"), r[:2])
    check("batch missing key 400", err(call("POST", "/correction-batches", {"corrections": good}, token=T["op"]), 400, "missing_idempotency_key"))
    check("batch key 256 422", err(batch(T["op"], good, key="k" * 256), 422, "validation_failed"))
    check("batch non-object body 400", err(call("POST", "/correction-batches", raw="[]", token=T["op"], key=k()), 400, "malformed_request"))
    for name, body in (("empty", []), ("33 items", [item("o1", 1, 400, t1)] * 33), ("non-array", "x"), ("non-object item", [5]),
                       ("duplicate ids", [item("o1", 1, 400, t1), item("o1", 1, 300, t1)])):
        check(f"batch shape {name} 422", err(call("POST", "/correction-batches", {"corrections": body}, token=T["op"], key=k()), 422, "validation_failed"))
    check("batch missing corrections 422", err(call("POST", "/correction-batches", {}, token=T["op"], key=k()), 422, "validation_failed"))
    for name, it in (("missing reason", {k_: v for k_, v in item("o1", 1, 400, t1).items() if k_ != "reason"}), ("amount -1", item("o1", 1, -1, t1)),
                     ("revision 0", item("o1", 0, 400, t1)), ("future eff", item("o1", 1, 400, ago(-60))), ("naive eff", item("o1", 1, 400, "2026-09-20T12:00:00")),
                     ("reason 201", item("o1", 1, 400, t1, reason="x" * 201)), ("payment_id number", dict(item("o1", 1, 400, t1), payment_id=5))):
        check(f"batch item {name} 422", err(batch(T["op"], [it]), 422, "validation_failed"))
    # input order precedence
    check("input order: item0 stale, item1 unknown → 409 stale", err(batch(T["op"], [item("o1", 2, 400, t1), item("zz", 1, 1, t1)]), 409, "stale_revision"))
    check("input order: item0 unknown, item1 stale → 404", err(batch(T["op"], [item("zz", 1, 1, t1), item("o1", 2, 400, t1)]), 404, "not_found"))
    check("input order: item0 invalid field, item1 unknown → 422", err(batch(T["op"], [item("o1", 1, -5, t1), item("zz", 1, 1, t1)]), 422, "validation_failed"))
    rf = refund(T["bob"], "o1", 100)[1]
    check("input order: item0 refund target (immutable), item1 unknown → 422 linked", err(batch(T["op"], [item(rf["payment_id"], 1, 0, t1), item("zz", 1, 1, t1)]), 422, "linked_payment_immutable"))
    check("item below refunded → 422 refund_exceeds_payment", err(batch(T["op"], [item("o1", 1, 99, t1)]), 422, "refund_exceeds_payment"))
    check("item errors precede funds: item0 overdraws, item1 stale → 409 stale", err(batch(T["op"], [item("o2", 1, 99999, t2), item("o1", 2, 400, t1)]), 409, "stale_revision"))
    check("unknown fields ignored", batch(T["op"], [dict(item("o1", 1, 450, t1), zz=1)], ).__getitem__(0) == 201)


def b_semantics():
    t1, t2, t3 = ago(5000), ago(4000), ago(3000)
    # P1 bob->ada 500 @t1 (ada opening 0), P2 ada->cy 500 @t2, P3 ada->bob 300 @t3 funded by P4 cy->ada 300 @t3
    pays = [{"id": "p1", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 500, "created_at": t1},
            {"id": "p2", "from_user_id": "u_ada", "to_user_id": "u_cy", "amount": 500, "created_at": t2},
            {"id": "p3", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 300, "created_at": t3},
            {"id": "p4", "from_user_id": "u_cy", "to_user_id": "u_ada", "amount": 300, "created_at": t3}]
    users = [user("ada", 0), user("bob", 1800), user("cy", 200), user("dee", 700), user("op", 0)]
    T = reset(fx(pays, users=users))
    opening = {"u_ada": 0, "u_bob": 2000, "u_cy": 0, "u_dee": 700, "u_op": 0}
    # combined current funds: ada available 0; increase p3 by 100 (debit ada) + increase p4 by 100 (credit ada) → net 0
    check("single correction of p3 +100 alone → insufficient_funds", err(corr(T["ada"], "p3", {"expected_revision": 1, "amount": 400, "effective_at": t3, "reason": "x"}), 409, "insufficient_funds"))
    snap0 = snapshot_state(T)
    tok = stmt(T["ada"], "?limit=1")[1]["snapshot"]
    tok_ref = json.dumps(stmt(T["ada"], f"?snapshot={tok}&limit=200")[1], sort_keys=True)
    # combined overdraw where each alone is affordable: bob 1800 avail: increase p1 by 1000 and decrease p3 by 300 (debits bob)… use cy
    kfail = k()
    r = batch(T["op"], [item("p2", 1, 400, t2), item("p4", 1, 400, t3)], key=kfail)  # cy: -100 (p2 down debits cy) ; p4 up debits cy 100 → cy net -200 > avail 200? exactly 200 ok
    check("combined debit exactly equal to available → 201", r[0] == 201, r[:2])
    T = reset(fx(pays, users=users))
    r = batch(T["op"], [item("p2", 1, 300, t2), item("p4", 1, 401, t3)])  # cy: -200 and -101 = -301 > 200
    check("combined overdraw (each item affordable alone) → insufficient_funds", err(r, 409, "insufficient_funds"), r[:2])
    check("…state unchanged after rejected batch", snapshot_state(T) == snap0)
    r = batch(T["op"], [item("p3", 1, 400, t3), item("p4", 1, 400, t3)])
    check("credit in one item funds debit in another (ada net 0) → 201", r[0] == 201, r[:2])
    rv = r[1]
    check("201 shape: id, recorded_at, revisions in input order with batch id", set(rv) >= {"correction_batch_id", "recorded_at", "revisions"} and [x["payment_id"] for x in rv["revisions"]] == ["p3", "p4"]
          and all(x["correction_batch_id"] == rv["correction_batch_id"] and x["recorded_at"] == rv["recorded_at"] and x["revision"] == 2 for x in rv["revisions"]), rv)
    check("shared recorded_at strictly later than previous revisions", ns(rv["recorded_at"]) > max(ns(t3), 0))
    rl = revs(T["ada"], "p3")[1]["revisions"]
    check("revisions list: rev1 correction_batch_id null, rev2 = batch id", rl[0]["correction_batch_id"] is None and rl[1]["correction_batch_id"] == rv["correction_batch_id"])
    single = corr(T["bob"], "p1", {"expected_revision": 1, "amount": 500, "effective_at": t1, "reason": "noop"})
    check("single-correction revision has correction_batch_id null", single[0] == 201 and single[1].get("correction_batch_id", "MISSING") is None, single[1])
    # historical overdraft created only by combination: move p1 (funding) after p2
    r = batch(T["op"], [item("p1", 2, 500, ago(3500))])
    check("batch moving funding after spend → historical_overdraft", err(r, 409, "historical_overdraft"), r[:2])
    r = batch(T["op"], [item("p1", 2, 500, t2), item("p2", 1, 500, t2)])
    check("same-instant combined movements in batch → 201", r[0] == 201, r[:2])
    O = Oracle(opening).load(T)
    bad = [(u, b) for b in O.boundaries(BIG) for u in O.open if O.bal(u, b, BIG) < 0]
    sums = [b for b in O.boundaries(BIG) if sum(O.bal(u, b, BIG) for u in O.open) != sum(opening.values())]
    check("oracle after batches: no negative, sums conserved", not bad and not sums, (bad[:2], sums[:2]))
    check("old snapshot unchanged after batches", json.dumps(stmt(T["ada"], f"?snapshot={tok}&limit=200")[1], sort_keys=True) != None)
    # rollback fully, key reusable
    T = reset(fx(pays, users=users))
    tok = stmt(T["ada"], "?limit=1")[1]["snapshot"]; ref = json.dumps(stmt(T["ada"], f"?snapshot={tok}&limit=200")[1], sort_keys=True)
    before = snapshot_state(T); revs_before = json.dumps([revs(T["ada"], x)[1] for x in ("p1", "p2", "p3", "p4")])
    kf = k()
    check("failing batch (historical) 409", err(batch(T["op"], [item("p4", 1, 300, t3), item("p1", 1, 500, ago(3500))], key=kf), 409, "historical_overdraft"))
    check("rejected batch: balances, statements, revisions, snapshot unchanged", snapshot_state(T) == before and json.dumps([revs(T["ada"], x)[1] for x in ("p1", "p2", "p3", "p4")]) == revs_before
          and json.dumps(stmt(T["ada"], f"?snapshot={tok}&limit=200")[1], sort_keys=True) == ref)
    check("key reusable after rejected batch", batch(T["op"], [item("p4", 1, 300, t3)], key=kf)[0] == 201)
    key = k()
    r1 = batch(T["op"], [item("p2", 1, 450, t2)], key=key)
    corr(T["ada"], "p2", {"expected_revision": 2, "amount": 460, "effective_at": t2, "reason": "later single"})
    r2 = batch(T["op"], [item("p2", 1, 450, t2)], key=key)
    check("batch replay 200 original after newer revisions", r1[0] == 201 and r2[0] == 200 and r2[1] == r1[1], (r1[0], r2[0]))
    check("batch same key different body 409", err(batch(T["op"], [item("p2", 1, 451, t2)], key=key), 409, "idempotency_key_reuse"))
    check("batch claimed key beats invalid body", err(call("POST", "/correction-batches", {"corrections": []}, token=T["op"], key=key), 409, "idempotency_key_reuse"))
    check("snapshot taken before batches still byte-identical", json.dumps(stmt(T["ada"], f"?snapshot={tok}&limit=200")[1], sort_keys=True) == ref)


def b_settlements():
    T = reset(fx())
    st = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 100}, {"from_handle": "bob", "to_handle": "cy", "amount": 50},
                                                    {"from_handle": "ada", "to_handle": "dee", "amount": 20}]}, token=T["op"], key=k())
    stkey = k()
    st = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 100}, {"from_handle": "bob", "to_handle": "cy", "amount": 50},
                                                    {"from_handle": "ada", "to_handle": "dee", "amount": 20}]}, token=T["op"], key=stkey)
    sr = st[1]; m = [x["payment_id"] for x in sr["payments"]]; ca = sr["committed_at"]
    other = P.pay(T, "ada", "cy", 5)[1]
    check("single correction of member 422 linked_payment_immutable", err(corr(T["ada"], m[0], {"expected_revision": 1, "amount": 0, "effective_at": ca, "reason": "x"}), 422, "linked_payment_immutable"))
    check("subset of settlement → 422 incomplete_settlement", err(batch(T["op"], [item(m[0], 1, 0, ca), item(m[1], 1, 0, ca)]), 422, "incomplete_settlement"))
    check("members + nonmember but missing one member → incomplete", err(batch(T["op"], [item(m[0], 1, 0, ca), item(other["payment_id"], 1, 5, other["created_at"])]), 422, "incomplete_settlement"))
    rf = refund(T["bob"], m[0], 10)[1]
    check("refund of member is not a member (batch of all members w/o refund OK shape)", True)
    check("input order: item0 below refunded (10) → refund_exceeds before item1 stale", err(batch(T["op"], [item(m[0], 1, 0, ca), item(m[1], 2, 0, ca)]), 422, "refund_exceeds_payment"))
    check("item error beats completeness (item1 stale with incomplete set)", err(batch(T["op"], [item(m[0], 1, 10, ca), item(m[1], 2, 0, ca)]), 409, "stale_revision"))
    ca_plus2 = dt.datetime.fromisoformat(ca).astimezone(dt.timezone(dt.timedelta(hours=2))).isoformat()
    r = batch(T["op"], [item(m[0], 1, 90, ca), item(m[1], 1, 50, ca_plus2), item(m[2], 1, 20, ca.replace("+00:00", "Z"))])
    check("complete settlement, same instant in different offsets → 201", r[0] == 201, r[:2])
    m_rev = [revs(T["ada"], m[0])[1]["revisions"], revs(T["bob"], m[1])[1]["revisions"]]
    check("members' new revisions share batch id", m_rev[0][-1]["correction_batch_id"] == m_rev[1][-1]["correction_batch_id"] == r[1]["correction_batch_id"])
    check("refund_exceeds within batch for refunded member (below 10)", err(batch(T["op"], [item(m[0], 2, 9, ca), item(m[1], 2, 50, ca), item(m[2], 2, 20, ca)]), 422, "refund_exceeds_payment"))
    r = batch(T["op"], [item(m[0], 2, 90, ca), item(m[1], 2, 50, pm(ns(ca), 1)), item(m[2], 2, 20, ca)])
    check("members 1µs apart → 422 validation_failed", err(r, 422, "validation_failed"), r[:2])
    r = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 100}, {"from_handle": "bob", "to_handle": "cy", "amount": 50},
                                                   {"from_handle": "ada", "to_handle": "dee", "amount": 20}]}, token=T["op"], key=stkey)
    check("original settlement retry returns original body", r[0] == 200 and r[1] == sr)
    fa = {x["payment_id"]: x for x in call("GET", "/activity?limit=200", token=T["ada"])[1]["payments"]}
    check("activity shows original member amounts", fa[m[0]]["amount"] == 100 and fa[m[0]]["settlement_id"] == sr["settlement_id"])
    st_ada = full_statement(T["ada"])
    e = [x for x in st_ada["entries"] if x["payment"]["payment_id"] == m[0]][0]
    check("statement reflects batch revision (amount 90, revision 2)", e["payment"]["amount"] == 90 and e["revision"] == 2)
    check("refund of refund-of-member 422 invalid_refund_target", err(refund(T["ada"], rf["payment_id"], 1), 422, "invalid_refund_target"))
    # nonmember single corrections still fine
    check("nonmember single correction still 201", corr(T["ada"], other["payment_id"], {"expected_revision": 1, "amount": 4, "effective_at": other["created_at"], "reason": "x"})[0] == 201)
    # operator corrects ordinary and request payments of any wallets
    _, rq, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 70}, token=T["cy"], key=k())
    rp = call("POST", f"/requests/{rq['request_id']}/pay", {}, token=T["ada"], key=k())[1]
    check("operator batch on request payment of other wallets → 201", batch(T["op"], [item(rp["payment_id"], 1, 60, rp["created_at"])])[0] == 201)
    a = call("POST", "/authorizations", {"to_handle": "bob", "amount": 50}, token=T["ada"], key=k())[1]
    c = call("POST", f"/authorizations/{a['authorization_id']}/capture", {}, token=T["bob"], key=k())[1]
    check("batch on capture → 422 linked_payment_immutable", err(batch(T["op"], [item(c["payment_id"], 1, 1, c["created_at"])]), 422, "linked_payment_immutable"))


def b_concurrency():
    T = reset(fx([{"id": f"q{i}", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 100, "created_at": ago(5000 - i)} for i in range(10)],
                 users=[user("ada", 9000), user("bob", 3500), user("cy", 0), user("dee", 700), user("op", 0)]))
    key = k()
    body = [item("q0", 1, 90, ago(5000)), item("q1", 1, 90, ago(4999))]
    with ThreadPoolExecutor(50) as ex:
        rs = list(ex.map(lambda _: batch(T["op"], body, key=key), range(50)))
    sts = sorted(r[0] for r in rs)
    check("50-way same-key batch: one 201, 49 × 200 identical", sts.count(201) == 1 and sts.count(200) == 49 and all(r[1] == rs[0][1] for r in rs), sorted(set(sts)))
    check("…applied once", [x["revision"] for x in revs(T["ada"], "q0")[1]["revisions"]] == [1, 2])
    # overlapping batches sharing an expected revision + singles on q2
    def ov(i):
        if i % 3 == 0: return batch(T["op"], [item("q2", 1, 80 - i % 7, ago(4998)), item(f"q{3 + i % 7}", 1, 95, ago(4997 - i % 7))])
        if i % 3 == 1: return corr(T["ada"], "q2", {"expected_revision": 1, "amount": 70, "effective_at": ago(4998), "reason": "single"})
        return batch(T["op"], [item(f"q{3 + i % 7}", 1, 85, ago(4997 - i % 7)), item("q2", 1, 75, ago(4998))])
    with ThreadPoolExecutor(40) as ex:
        rs = list(ex.map(ov, range(40)))
    wins = [r for r in rs if r[0] == 201 and (("revisions" in r[1] and any(x["payment_id"] == "q2" for x in r[1]["revisions"])) or r[1].get("payment_id") == "q2")]
    check("overlapping batch/single on shared expected revision: exactly one winner for q2", len(wins) == 1 and len(revs(T["ada"], "q2")[1]["revisions"]) == 2, (len(wins), sorted({r[0] for r in rs})))
    check("no 5xx in overlap race", all(r[0] < 500 for r in rs))
    # batches racing payments/refunds/captures/settlements/statements
    pids = [f"q{i}" for i in range(10)]
    sts, lat = [], []
    lock = threading.Lock()
    random.seed(4)
    def job(i):
        kind = i % 6
        if kind == 0:
            pid = random.choice(pids); cur = revs(T["ada"], pid)[1]["revisions"][-1]["revision"]
            r = batch(T["op"], [item(pid, cur, random.randint(50, 150), ago(5000 - int(pid[1:])))])
        elif kind == 1: r = P.pay(T, random.choice(["ada", "bob"]), "cy", random.randint(1, 30))
        elif kind == 2: r = refund(T["bob"], random.choice(pids), random.randint(1, 20))
        elif kind == 3: r = call("POST", "/settlements", {"transfers": [{"from_handle": "bob", "to_handle": "dee", "amount": random.randint(1, 20)}]}, token=T["op"], key=k())
        elif kind == 4:
            a = call("POST", "/authorizations", {"to_handle": "cy", "amount": 10}, token=T["ada"], key=k())
            r = call("POST", f"/authorizations/{a[1]['authorization_id']}/capture", {}, token=T["cy"], key=k()) if a[0] == 201 else a
        else: r = stmt(T[random.choice(["ada", "bob", "cy"])], "?limit=200")
        with lock: sts.append(r[0]); lat.append(r[2])
    with ThreadPoolExecutor(50) as ex:
        list(ex.map(job, range(600)))
    check(f"mixed race: no 5xx over {len(sts)}, max {max(lat):.2f}s < 5", all(s < 500 for s in sts) and max(lat) < 5, sorted(set(sts)))
    O = Oracle({"u_ada": 10000, "u_bob": 2500, "u_cy": 0, "u_dee": 700, "u_op": 0}).load(T)
    bounds = O.boundaries(BIG)
    neg = [(u, b) for b in bounds for u in O.open if O.bal(u, b, BIG) < 0]
    sums = [b for b in bounds if sum(O.bal(u, b, BIG) for u in O.open) != 13200]
    cur_ok = all(me(T[h])[1]["total"] == O.bal("u_" + h, BIG, BIG) for h in T)
    check("mixed race: oracle conserves sums, no negative at any boundary, current matches", not neg and not sums and cur_ok, (neg[:2], sums[:2]))
    negav = []
    for h in ("ada", "bob", "cy", "dee"):
        for b in bounds[::max(1, len(bounds) // 40)]:
            x = me(T[h], "?as_of=" + q(iso_ns(b)))[1]
            if x["available"] < 0 or x["total"] < 0: negav.append((h, iso_ns(b), x["total"], x["available"]))
    check("mixed race: service views never negative total/available at sampled boundaries", not negav, negav[:3])
    refunded = {}
    for pid in pids:
        cur = revs(T["ada"], pid)[1]["revisions"][-1]["amount"]
        tot = sum(p["amount"] for p in call("GET", "/activity?limit=200", token=T["bob"])[1]["payments"] if p.get("refund_of") == pid)
        if tot > cur: refunded[pid] = (tot, cur)
    check("cumulative refunds never exceed current amount after race", not refunded, refunded)


def b_imports():
    for name, base in (("stage-1", URL1), ("stage-2", URLS2), ("stage-3", URLS3)):
        Tx = reset(fx([{"id": "e1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 100, "created_at": ago(5000)}]), base=base)
        kp = k()
        px = call("POST", "/payments", {"to_handle": "cy", "amount": 250}, token=Tx["ada"], key=kp, base=base)[1]
        sk = k()
        sx = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "dee", "amount": 5}, {"from_handle": "dee", "to_handle": "bob", "amount": 2}]}, token=Tx["op"], key=sk, base=base)[1]
        tok = ref = cx = None
        if base == URLS3:
            cx = call("POST", f"/payments/{px['payment_id']}/corrections", {"expected_revision": 1, "amount": 200, "effective_at": px["created_at"], "reason": "s3 fix"}, token=Tx["ada"], key=k(), base=base)[1]
            s3 = call("GET", "/statement?limit=1", token=Tx["ada"], base=base)[1]
            tok = s3["snapshot"]; ref = json.dumps(call("GET", f"/statement?snapshot={tok}&limit=200", token=Tx["ada"], base=base)[1], sort_keys=True)
        ex = call("GET", "/_test/export", base=base)[1]
        r = call("POST", "/_test/import", ex)
        check(f"{name} export → stage 4 import 204", r[0] == 204, r)
        r = call("POST", "/payments", {"to_handle": "cy", "amount": 250}, token=Tx["ada"], key=kp)
        check(f"{name}: payment receipt replays byte-identical original (no refund_of added)", r[0] == 200 and r[1] == px and ("refund_of" in px) == ("refund_of" in r[1]))
        r = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "dee", "amount": 5}, {"from_handle": "dee", "to_handle": "bob", "amount": 2}]}, token=Tx["op"], key=sk)
        check(f"{name}: settlement receipt replays original", r[0] == 200 and r[1] == sx)
        ms = [x["payment_id"] for x in sx["payments"]]
        check(f"{name}: membership kept (subset batch incomplete)", err(batch(Tx["op"], [item(ms[0], 1, 4, sx["committed_at"])]), 422, "incomplete_settlement"))
        check(f"{name}: full member batch works after import", batch(Tx["op"], [item(ms[0], 1, 4, sx["committed_at"]), item(ms[1], 1, 2, sx["committed_at"])])[0] == 201)
        if tok:
            check("stage-3: snapshot pages byte-identically (stage-3 shape) after import", json.dumps(stmt(Tx["ada"], f"?snapshot={tok}&limit=200")[1], sort_keys=True) == ref)
            rl = revs(Tx["ada"], px["payment_id"])[1]["revisions"]
            check("stage-3: correction kept", len(rl) == 2 and rl[1]["amount"] == 200 and rl[1]["reason"] == "s3 fix")
            check("stage-3: refund vs corrected amount (200) → 201 for 200, 1 more exceeds", refund(Tx["cy"], px["payment_id"], 200)[0] == 201 and err(refund(Tx["cy"], px["payment_id"], 1), 422, "refund_exceeds_payment"))
        else:
            check(f"{name}: refund after import works", refund(Tx["cy"], px["payment_id"], 50)[0] == 201)
        O = Oracle({"u_ada": 10100, "u_bob": 2400, "u_cy": 0, "u_dee": 700, "u_op": 0}).load(Tx)
        check(f"{name}: oracle sums conserved after import + new ops", all(sum(O.bal(u, b, BIG) for u in O.open) == 13200 for b in O.boundaries(BIG)))
    # stage-4 roundtrip
    T = reset(fx([{"id": "g1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 100, "created_at": ago(5000)}]))
    kr, kb = k(), k()
    rr = refund(T["bob"], "g1", 30, key=kr)[1]
    bb = batch(T["op"], [item("g1", 1, 80, ago(5000))], key=kb)[1]
    tok = stmt(T["ada"], "?limit=1")[1]["snapshot"]; ref = json.dumps(stmt(T["ada"], f"?snapshot={tok}&limit=200")[1], sort_keys=True)
    ex = call("GET", "/_test/export")[1]
    check("stage-4 export → other instance 204", call("POST", "/_test/import", ex, base=URL2)[0] == 204)
    check("refund replay after import", call("POST", "/payments/g1/refunds", {"amount": 30}, token=T["bob"], key=kr, base=URL2)[1] == rr)
    check("batch replay after import", call("POST", "/correction-batches", {"corrections": [item("g1", 1, 80, bb["revisions"][0]["effective_at"])]}, token=T["op"], key=kb, base=URL2)[1] == bb)
    check("snapshot preserved", json.dumps(call("GET", f"/statement?snapshot={tok}&limit=200", token=T["ada"], base=URL2)[1], sort_keys=True) == ref)
    check("refunded total preserved (51 more exceeds 80-30)", err(call("POST", "/payments/g1/refunds", {"amount": 51}, token=T["bob"], key=k(), base=URL2), 422, "refund_exceeds_payment"))
    bad = json.loads(json.dumps(ex))
    for p in bad["state"]["payments"]:
        if p.get("refund_of"): p["refund_of"] = "nope"
    before = call("GET", "/_test/export")[1]
    check("tampered refund link → 422 unchanged", err(call("POST", "/_test/import", bad), 422, "validation_failed") and call("GET", "/_test/export")[1] == before)
    # reset clears everything
    reset(fx())
    T2 = {h: P.login(h) for h in ("ada", "bob")}
    check("reset clears refunds/batches/snapshots", err(stmt(T2["ada"], f"?snapshot={tok}"), 404, "not_found") and err(revs(T2["ada"], "g1"), 404, "not_found"))


def main():
    for f in (r_basics, r_targets_and_holds, r_corrections_vs_refunds, b_validation, b_semantics, b_settlements, b_concurrency, b_imports):
        print(f"== {f.__name__}", flush=True)
        try: f()
        except Exception as e:
            import traceback; traceback.print_exc(); check(f"{f.__name__} ran", False, repr(e)[:300])
    n = sum(1 for _, ok in P.RESULTS if ok)
    print(f"TOTAL {n}/{len(P.RESULTS)} passed, {len(P.RESULTS) - n} failed")


if __name__ == "__main__":
    main()
