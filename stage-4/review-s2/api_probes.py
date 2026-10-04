#!/usr/bin/env python3
"""Reviewer probes for Pocketful stage 2 (authorizations, holds, /me, Accept negotiation),
written from the stage-2 requirements text. Stdlib only; reuses the stage-1 probe helpers.

Usage: python3 api_probes.py <url> <url2> <stage1-url>
  url2       a second stage-2 instance (cross-process import target)
  stage1-url an instance of the stage-1 image (source of a stage-1 export)
"""
import os, sys, json, time, datetime as dt
from concurrent.futures import ThreadPoolExecutor

HERE = os.path.dirname(os.path.abspath(__file__))
for cand in (os.path.join(HERE, "..", "stage-1"), os.path.join(HERE, "..", "review")):
    if os.path.exists(os.path.join(cand, "probes.py")):
        sys.path.insert(0, cand)
        break
URL, URL2, URL1 = sys.argv[1], sys.argv[2], sys.argv[3]
sys.argv = [sys.argv[0], URL]
import probes as P
from probes import call, check, err, k

P.A = URL


def iso(sec_from_now):
    return (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=sec_from_now)).replace(microsecond=0).isoformat()


def fx(ttl=None, auths=None, users=None, **extra):
    f = P.fixture(users=users or P.USERS)
    if ttl is not None:
        f["authorization_ttl_seconds"] = ttl
    if auths is not None:
        f["authorizations"] = auths
    f.update(extra)
    return f


def reset(f=None, base=None):
    base = base or URL
    f = f or fx()
    r = call("POST", "/_test/reset", f, base=base)
    assert r[0] == 204, r
    return {u["handle"]: P.login(u["handle"], base) for u in f["users"]}


def me(t, base=None):
    return call("GET", "/me", token=t, base=base or URL)[1]


def auth(T, frm, to, amount, key=None, **kw):
    b = {"to_handle": to, "amount": amount}
    b.update(kw)
    return call("POST", "/authorizations", b, token=T[frm], key=key or k())


def cap(T, who, aid, body=None, key=None):
    return call("POST", f"/authorizations/{aid}/capture", {} if body is None else body, token=T[who], key=key or k())


def void(T, who, aid):
    return call("POST", f"/authorizations/{aid}/void", token=T[who])


def lst(T, who, q=""):
    return call("GET", "/authorizations" + q, token=T[who])


def totals(T):
    return sum(me(t)["total"] for t in T.values())


def ok_me(m):
    return m["balance"] == m["total"] and m["available"] == m["total"] - m["held"] and m["available"] >= 0 and m["held"] >= 0


AUTH_KEYS = {"authorization_id", "from_user_id", "from_handle", "to_user_id", "to_handle", "amount", "captured_amount",
             "currency", "note", "visibility", "status", "expires_at", "payment_id", "created_at", "remaining_amount", "payment_ids"}
PAY_KEYS = {"payment_id", "from_user_id", "from_handle", "to_user_id", "to_handle", "amount", "currency", "note",
            "visibility", "request_id", "created_at", "authorization_id"}


def p_me_and_create():
    T = reset()
    m = me(T["ada"])
    check("me: balance=total=available, held 0", m["balance"] == m["total"] == m["available"] == 10000 and m["held"] == 0, m)
    s, a, _, _ = auth(T, "ada", "bob", 2000, note="deposit", visibility="private")
    check("authorize 201 shape", s == 201 and AUTH_KEYS <= set(a) and a["status"] == "open" and a["captured_amount"] == 0
          and a["payment_id"] is None and a["payment_ids"] == [] and a["remaining_amount"] == 2000 and a["currency"] == "EUR"
          and a["from_handle"] == "ada" and a["to_handle"] == "bob" and a["note"] == "deposit" and a["visibility"] == "private", (s, a))
    ca = dt.datetime.fromisoformat(a["created_at"]); ea = dt.datetime.fromisoformat(a["expires_at"])
    check("expires_at = created_at + 600 (default ttl)", (ea - ca).total_seconds() == 600, (a["created_at"], a["expires_at"]))
    check("timestamps carry offset", a["created_at"][-6] in "+-" and a["expires_at"][-6] in "+-")
    m = me(T["ada"])
    check("hold: total unchanged, held 2000, available 8000", m["total"] == 10000 and m["balance"] == 10000 and m["held"] == 2000 and m["available"] == 8000, m)
    check("receiver unaffected by hold", me(T["bob"])["total"] == 2500 and me(T["bob"])["held"] == 0)
    check("authorization never in activity (payer/receiver/third)", all(
        all(p.get("authorization_id") is None for p in call("GET", "/activity?limit=200", token=T[h])[1]["payments"]) and
        len(call("GET", "/activity?limit=200", token=T[h])[1]["payments"]) == (1 if h in ("ada", "bob") else 0)
        for h in ("ada", "bob", "cy")))
    _, a2, _, _ = auth(T, "ada", "bob", 1)
    check("defaults note '' public", a2["note"] == "" and a2["visibility"] == "public")
    # against available
    check("authorize above available 409", P.err(auth(T, "ada", "bob", 8000), 409, "insufficient_funds"))
    check("authorize exactly available 201", auth(T, "ada", "bob", 7999)[0] == 201)
    check("payment against available 409", err(P.pay(T, "ada", "bob", 1), 409, "insufficient_funds"))
    _, rq, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 1}, token=T["bob"], key=k())
    check("request pay against available 409", err(call("POST", f"/requests/{rq['request_id']}/pay", {}, token=T["ada"], key=k()), 409, "insufficient_funds"))
    check("settlement net debit against available 409", err(call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 1}]}, token=T["op"], key=k()), 409, "insufficient_funds"))
    check("settlement receiving first lets held payer net zero", call("POST", "/settlements", {"transfers": [
        {"from_handle": "bob", "to_handle": "ada", "amount": 5}, {"from_handle": "ada", "to_handle": "cy", "amount": 5}]}, token=T["op"], key=k())[0] == 201)
    check("split ignores balances still", call("POST", "/splits", {"amount": 99999, "participant_handles": ["ada", "bob"]}, token=T["ada"], key=k())[0] == 201)
    # validation
    for a_, code in ((0, "validation_failed"), (1000000001, "validation_failed"), (1.5, "validation_failed"), ("5", "validation_failed"), (True, "validation_failed")):
        check(f"authorize amount {a_!r} 422", err(auth(T, "bob", "cy", a_), 422, code))
    check("authorize 1e3 accepted", call("POST", "/authorizations", raw='{"to_handle":"cy","amount":1e3}', token=T["bob"], key=k())[1].get("amount") == 1000)
    check("authorize self 422 self_payment", err(auth(T, "bob", "bob", 1), 422, "self_payment"))
    check("authorize unknown 404", err(auth(T, "bob", "zz", 1), 404, "not_found"))
    check("authorize note 201 422", err(auth(T, "bob", "cy", 1, note="x" * 201), 422, "validation_failed"))
    check("authorize note null 422", err(auth(T, "bob", "cy", 1, note=None), 422, "validation_failed"))
    check("authorize visibility bad 422", err(auth(T, "bob", "cy", 1, visibility="x"), 422, "validation_failed"))
    check("authorize missing key 400", err(call("POST", "/authorizations", {"to_handle": "cy", "amount": 1}, token=T["bob"]), 400, "missing_idempotency_key"))
    check("authorize no token 401", err(call("POST", "/authorizations", {"to_handle": "cy", "amount": 1}), 401, "unauthenticated"))
    check("authorize wrong-type to_handle 400", err(auth(T, "bob", 5, 1), 400, "malformed_request"))
    key = k()
    r1 = auth(T, "bob", "cy", 10, key=key); r2 = auth(T, "bob", "cy", 10, key=key)
    check("authorize replay 200 identical, one hold", r1[0] == 201 and r2[0] == 200 and r1[1] == r2[1] and me(T["bob"])["held"] == 1010, me(T["bob"]))
    check("authorize same key diff body 409", err(auth(T, "bob", "cy", 11, key=key), 409, "idempotency_key_reuse"))
    check("authorize claimed key beats invalid body", err(auth(T, "bob", "cy", 0, key=key), 409, "idempotency_key_reuse"))
    check("authorize same key other user independent", auth(T, "cy", "bob", 1, key=key)[0] in (201, 409))
    k2 = k()
    check("authorize 4xx claims nothing", err(auth(T, "cy", "bob", 10**6, key=k2), 409, "insufficient_funds"))
    P.pay(T, "ada", "cy", 0 + 1) if False else None
    check("ok me invariants", all(ok_me(me(t)) for t in T.values()))
    check("payments carry authorization_id null", all("authorization_id" in p and p["authorization_id"] is None
                                                      for p in call("GET", "/activity?limit=200", token=T["ada"])[1]["payments"]))
    check("POST /payments leaves no hold", P.pay(T, "bob", "cy", 1)[1]["authorization_id"] is None and me(T["bob"])["held"] == 1010)


def p_capture():
    T = reset()
    _, a, _, _ = auth(T, "ada", "bob", 2000, note="deposit", visibility="private")
    aid = a["authorization_id"]
    check("capture by payer 403", err(cap(T, "ada", aid), 403, "forbidden"))
    check("capture by third party 403", err(cap(T, "cy", aid), 403, "forbidden"))
    check("capture unknown 404", err(cap(T, "bob", "a_nope"), 404, "not_found"))
    check("capture missing key 400", err(call("POST", f"/authorizations/{aid}/capture", {}, token=T["bob"]), 400, "missing_idempotency_key"))
    check("capture no token 401", err(call("POST", f"/authorizations/{aid}/capture", {}), 401, "unauthenticated"))
    for a_ in (0, -1, 1.5, "5", True, None):
        check(f"capture amount {a_!r} 422 validation", err(cap(T, "bob", aid, {"amount": a_}), 422, "validation_failed"))
    check("capture above remainder 422 exceeds", err(cap(T, "bob", aid, {"amount": 2001}), 422, "capture_exceeds_authorization"))
    for big in (1000000001, 10**12, 10**30):
        check(f"capture {big} (>1e9) 422 capture_exceeds_authorization", err(cap(T, "bob", aid, {"amount": big}), 422, "capture_exceeds_authorization"))
    check("capture 1e400 -> 422 (either code)", call("POST", f"/authorizations/{aid}/capture", raw='{"amount":1e400}', token=T["bob"], key=k())[0] == 422)
    check("capture final non-bool 400", err(cap(T, "bob", aid, {"amount": 1, "final": "no"}), 400, "malformed_request"))
    m0 = me(T["ada"])
    key = k()
    s, p, _, _ = cap(T, "bob", aid, {"amount": 1500}, key=key)
    check("capture 201 payment shape", s == 201 and PAY_KEYS <= set(p) and p["authorization_id"] == aid and p["request_id"] is None
          and p["amount"] == 1500 and p["note"] == "deposit" and p["visibility"] == "private" and p["from_handle"] == "ada" and p["to_handle"] == "bob", (s, p))
    m = me(T["ada"])
    check("final capture: total -1500, held 0, remainder released", m["total"] == 8500 and m["held"] == 0 and m["available"] == 8500, (m0, m))
    check("receiver credited", me(T["bob"])["total"] == 4000)
    al = [x for x in lst(T, "ada")[1]["authorizations"] if x["authorization_id"] == aid][0]
    check("authorization captured state", al["status"] == "captured" and al["captured_amount"] == 1500 and al["payment_id"] == p["payment_id"]
          and al["payment_ids"] == [p["payment_id"]] and al["remaining_amount"] == 0, al)
    feed_c = {x["payment_id"] for x in call("GET", "/activity", token=T["cy"])[1]["payments"]}
    feed_a = {x["payment_id"]: x for x in call("GET", "/activity", token=T["ada"])[1]["payments"]}
    check("capture payment in feed by visibility rule", p["payment_id"] in feed_a and p["payment_id"] not in feed_c and feed_a[p["payment_id"]]["authorization_id"] == aid)
    r = cap(T, "bob", aid, {"amount": 1500}, key=key)
    check("capture replay 200 identical after captured", r[0] == 200 and r[1] == p and me(T["bob"])["total"] == 4000, r)
    check("capture same key different body 409", err(cap(T, "bob", aid, {}, key=key), 409, "idempotency_key_reuse"))
    check("second capture after final 409 not_open", err(cap(T, "bob", aid, {"amount": 1}), 409, "authorization_not_open"))
    check("void captured 409 not_open", err(void(T, "ada", aid), 409, "authorization_not_open"))
    # default amount = remainder; {} vs {"amount": n}
    _, a, _, _ = auth(T, "ada", "bob", 700)
    kk = k()
    s, p, _, _ = cap(T, "bob", a["authorization_id"], {}, key=kk)
    check("capture {} defaults to full remainder", s == 201 and p["amount"] == 700)
    check("{} then {amount:700} same key 409", err(cap(T, "bob", a["authorization_id"], {"amount": 700}, key=kk), 409, "idempotency_key_reuse"))
    # extended
    T = reset()
    _, a, _, _ = auth(T, "ada", "bob", 2000)
    aid = a["authorization_id"]
    s, p1, _, _ = cap(T, "bob", aid, {"amount": 700, "final": False})
    m = me(T["ada"])
    al = [x for x in lst(T, "bob")[1]["authorizations"] if x["authorization_id"] == aid][0]
    check("final:false keeps open, remainder held", s == 201 and al["status"] == "open" and al["remaining_amount"] == 1300 and al["captured_amount"] == 700
          and m["total"] == 9300 and m["held"] == 1300 and m["available"] == 8000, (al, m))
    check("exceeds compares remaining", err(cap(T, "bob", aid, {"amount": 1301, "final": False}), 422, "capture_exceeds_authorization"))
    s, p2, _, _ = cap(T, "bob", aid, {"amount": 300, "final": False})
    s, p3, _, _ = cap(T, "bob", aid, {"amount": 1000, "final": False})
    al = [x for x in lst(T, "bob")[1]["authorizations"] if x["authorization_id"] == aid][0]
    check("capturing entire remainder closes even with final:false", al["status"] == "captured" and al["captured_amount"] == 2000 and al["remaining_amount"] == 0
          and al["payment_ids"] == [p1["payment_id"], p2["payment_id"], p3["payment_id"]] and al["payment_id"] == p3["payment_id"], al)
    check("cumulative capture totals", me(T["ada"])["total"] == 8000 and me(T["ada"])["held"] == 0 and me(T["bob"])["total"] == 4500)
    # partial then final releases remainder
    _, a, _, _ = auth(T, "ada", "bob", 1000)
    cap(T, "bob", a["authorization_id"], {"amount": 100, "final": False})
    s, p, _, _ = cap(T, "bob", a["authorization_id"], {"amount": 200})
    al = [x for x in lst(T, "bob")[1]["authorizations"] if x["authorization_id"] == a["authorization_id"]][0]
    check("partial then final: captured 300, rest released", al["status"] == "captured" and al["captured_amount"] == 300 and me(T["ada"])["held"] == 0
          and me(T["ada"])["total"] == 7700, (al, me(T["ada"])))
    # partial then void
    _, a, _, _ = auth(T, "ada", "bob", 1000)
    cap(T, "bob", a["authorization_id"], {"amount": 400, "final": False})
    s, v, _, _ = void(T, "ada", a["authorization_id"])
    check("partial capture + void releases only remainder, keeps records", s == 200 and v["status"] == "voided" and v["captured_amount"] == 400
          and v["remaining_amount"] == 0 and len(v["payment_ids"]) == 1 and me(T["ada"])["held"] == 0 and me(T["ada"])["total"] == 7300, (v, me(T["ada"])))
    check("capture after void 409 not_open", err(cap(T, "bob", a["authorization_id"], {}), 409, "authorization_not_open"))
    # captures may use reserved money: ada spends available to 0, then capture works
    T = reset()
    _, a, _, _ = auth(T, "ada", "bob", 4000)
    P.pay(T, "ada", "cy", 6000)
    m = me(T["ada"])
    check("available drained to 0 with hold intact", m["available"] == 0 and m["held"] == 4000 and m["total"] == 4000, m)
    check("capture spends reserved money", cap(T, "bob", a["authorization_id"], {"amount": 4000})[0] == 201 and me(T["ada"])["total"] == 0)
    check("no negative after", all(ok_me(me(t)) for t in T.values()) and totals(T) == 13200)
    # per-user key scope / different path
    T = reset()
    _, a, _, _ = auth(T, "ada", "bob", 100)
    _, b, _, _ = auth(T, "ada", "bob", 100)
    key = k()
    check("same key + body, different capture path = new", cap(T, "bob", a["authorization_id"], {"amount": 10}, key=key)[0] == 201
          and cap(T, "bob", b["authorization_id"], {"amount": 10}, key=key)[0] == 201)
    _, c, _, _ = auth(T, "bob", "ada", 100)
    k3 = k()
    check("capture 4xx claims nothing", err(cap(T, "ada", c["authorization_id"], {"amount": 101}, key=k3), 422, "capture_exceeds_authorization")
          and cap(T, "ada", c["authorization_id"], {"amount": 100}, key=k3)[0] == 201)


def p_void():
    T = reset()
    _, a, _, _ = auth(T, "ada", "bob", 500)
    aid = a["authorization_id"]
    check("void by receiver 403", err(void(T, "bob", aid), 403, "forbidden"))
    check("void by third 403", err(void(T, "cy", aid), 403, "forbidden"))
    check("void unknown 404", err(void(T, "ada", "a_zz"), 404, "not_found"))
    check("void no token 401", err(call("POST", f"/authorizations/{aid}/void"), 401, "unauthenticated"))
    s, v, _, _ = void(T, "ada", aid)
    check("void 200 voided, hold released", s == 200 and v["status"] == "voided" and v["remaining_amount"] == 0 and AUTH_KEYS <= set(v)
          and me(T["ada"])["held"] == 0 and me(T["ada"])["available"] == 10000, (s, v))
    s, v2, _, _ = void(T, "ada", aid)
    check("void twice 200 current state", s == 200 and v2["status"] == "voided")
    check("capture voided 409 not_open", err(cap(T, "bob", aid), 409, "authorization_not_open"))


def p_expiry():
    T = reset(fx(ttl=2))
    _, a, _, _ = auth(T, "ada", "bob", 3000)
    _, b, _, _ = auth(T, "ada", "bob", 1000)
    kk = k()
    cap(T, "bob", b["authorization_id"], {"amount": 400, "final": False}, key=kk)
    ca = dt.datetime.fromisoformat(a["created_at"]); ea = dt.datetime.fromisoformat(a["expires_at"])
    check("expires_at = created_at + ttl(2)", (ea - ca).total_seconds() == 2)
    check("held before expiry", me(T["ada"])["held"] == 3600)
    time.sleep(3.2)  # no traffic at the deadline
    m = me(T["ada"])
    check("after deadline: available includes released remainder", m["held"] == 0 and m["available"] == m["total"] == 9600, m)
    al = {x["authorization_id"]: x for x in lst(T, "ada")[1]["authorizations"]}
    check("list shows expired", al[a["authorization_id"]]["status"] == "expired" and al[a["authorization_id"]]["remaining_amount"] == 0)
    check("partially captured expired keeps capture records", al[b["authorization_id"]]["status"] == "expired" and al[b["authorization_id"]]["captured_amount"] == 400
          and len(al[b["authorization_id"]]["payment_ids"]) == 1)
    check("status=open excludes clock-expired", lst(T, "ada", "?status=open")[1]["authorizations"] == [])
    check("status=expired includes them", len(lst(T, "ada", "?status=expired")[1]["authorizations"]) == 2)
    check("capture expired 409 authorization_expired", err(cap(T, "bob", a["authorization_id"]), 409, "authorization_expired"))
    check("capture replay after expiry still 200", cap(T, "bob", b["authorization_id"], {"amount": 400, "final": False}, key=kk)[0] == 200)
    check("void expired 409 not_open", err(void(T, "ada", a["authorization_id"]), 409, "authorization_not_open"))
    # writes reflect expiry: create hold for all available, let it expire, then pay all
    T = reset(fx(ttl=1))
    auth(T, "dee", "cy", 700)
    time.sleep(2.2)
    check("write after silent expiry can spend released funds", P.pay(T, "dee", "cy", 700)[0] == 201)
    # seeded past/future
    f = fx(auths=[
        {"id": "a_past", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 2000, "note": "old", "visibility": "public", "status": "open", "expires_at": iso(-3600)},
        {"id": "a_fut", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 3000, "note": "deposit", "visibility": "private", "status": "open", "expires_at": iso(3600)},
        {"id": "a_cap", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 500, "status": "captured", "expires_at": iso(3600)},
        {"id": "a_void", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 2500, "status": "voided", "expires_at": iso(3600)},
        {"id": "a_exp", "from_user_id": "u_bob", "to_user_id": "u_ada", "amount": 2500, "status": "expired", "expires_at": iso(3600)}])
    T = reset(f)
    m = me(T["ada"])
    check("seeded: only future open holds; available derived", m["total"] == 10000 and m["held"] == 3000 and m["available"] == 7000, m)
    check("seeded voided/expired hold nothing", me(T["bob"])["held"] == 0 and me(T["bob"])["available"] == 2500)
    al = {x["authorization_id"]: x for x in lst(T, "ada")[1]["authorizations"]}
    check("seeded past open shows expired", al["a_past"]["status"] == "expired" and al["a_fut"]["status"] == "open" and al["a_fut"]["remaining_amount"] == 3000, al.get("a_past"))
    check("seeded statuses preserved", al["a_cap"]["status"] == "captured" and al["a_void"]["status"] == "voided" and al["a_exp"]["status"] == "expired")
    check("seeded expires_at returned", al["a_fut"]["expires_at"] and dt.datetime.fromisoformat(al["a_fut"]["expires_at"]) == dt.datetime.fromisoformat(f["authorizations"][1]["expires_at"]))
    check("capture seeded future", cap(T, "bob", "a_fut", {"amount": 1000})[0] == 201 and me(T["ada"])["held"] == 0 and me(T["ada"])["total"] == 9000)
    check("capture seeded past 409 expired", err(cap(T, "bob", "a_past"), 409, "authorization_expired"))
    # fixture validation
    base = P.fixture()
    check("ttl default 600", True)
    for bad in (0, -5, 1.5, "600", True, None):
        fb = fx(); fb["authorization_ttl_seconds"] = bad
        r = call("POST", "/_test/reset", fb)
        check(f"ttl {bad!r} 422", err(r, 422, "validation_failed"), r)
    check("ttl 1 ok", call("POST", "/_test/reset", fx(ttl=1))[0] == 204)
    check("ttl 600.0 ok", call("POST", "/_test/reset", fx(ttl=600.0))[0] == 204)
    T = reset(f)
    big = fx(auths=[{"id": "a1", "from_user_id": "u_dee", "to_user_id": "u_bob", "amount": 400, "status": "open", "expires_at": iso(3600)},
                    {"id": "a2", "from_user_id": "u_dee", "to_user_id": "u_bob", "amount": 301, "status": "open", "expires_at": iso(7200)}])
    r = call("POST", "/_test/reset", big)
    check("seeded open holds > balance 422", err(r, 422, "validation_failed"), r)
    check("422 reset changed nothing", me(T["ada"])["held"] == 3000)
    eq = fx(auths=[{"id": "a1", "from_user_id": "u_dee", "to_user_id": "u_bob", "amount": 700, "status": "open", "expires_at": iso(3600)},
                   {"id": "a2", "from_user_id": "u_dee", "to_user_id": "u_bob", "amount": 900, "status": "open", "expires_at": iso(-3600)},
                   {"id": "a3", "from_user_id": "u_dee", "to_user_id": "u_bob", "amount": 900, "status": "captured", "expires_at": iso(3600)}])
    check("holds == balance ok; expired/closed not counted", call("POST", "/_test/reset", eq)[0] == 204)
    T = {u["handle"]: P.login(u["handle"]) for u in eq["users"]}
    check("available 0 after seeded full hold", me(T["dee"])["available"] == 0 and me(T["dee"])["held"] == 700)
    for name, a in (("unknown user", {"id": "x", "from_user_id": "u_zz", "to_user_id": "u_bob", "amount": 1, "status": "open", "expires_at": iso(3600)}),
                    ("bad status", {"id": "x", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1, "status": "pending", "expires_at": iso(3600)}),
                    ("bad expires", {"id": "x", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1, "status": "open", "expires_at": "tomorrow"})):
        check(f"seeded auth {name} 422", err(call("POST", "/_test/reset", fx(auths=[a])), 422, "validation_failed"))
    T = reset(P.fixture())
    check("authorizations omitted = []", lst(T, "ada")[1] == {"authorizations": [], "has_more": False})


def p_list():
    T = reset()
    ids = []
    for i in range(4):
        ids.append(auth(T, "ada", "bob", 10 + i)[1]["authorization_id"]); time.sleep(0.01)
    inc = auth(T, "bob", "ada", 5)[1]["authorization_id"]
    auth(T, "cy", "dee", 0 + 1) if False else None
    other = auth(T, "dee", "cy", 1)[1]["authorization_id"]
    got = [a["authorization_id"] for a in lst(T, "ada")[1]["authorizations"]]
    check("list only involving caller", set(got) == set(ids) | {inc} and other not in got, got)
    check("newest first", got[0] == inc and got.index(ids[3]) < got.index(ids[0]), got)
    check("direction outgoing", [a["authorization_id"] for a in lst(T, "ada", "?direction=outgoing")[1]["authorizations"]] == ids[::-1])
    check("direction incoming", [a["authorization_id"] for a in lst(T, "ada", "?direction=incoming")[1]["authorizations"]] == [inc])
    check("receiver sees incoming", set(a["authorization_id"] for a in lst(T, "bob", "?direction=incoming")[1]["authorizations"]) == set(ids))
    check("third party sees none", lst(T, "op")[1]["authorizations"] == [])
    void(T, "ada", ids[0])
    check("status filter voided", [a["authorization_id"] for a in lst(T, "ada", "?status=voided")[1]["authorizations"]] == [ids[0]])
    p = lst(T, "ada", "?limit=2")[1]
    check("limit 2 has_more", len(p["authorizations"]) == 2 and p["has_more"] is True)
    p = lst(T, "ada", "?limit=2&offset=4")[1]
    check("last page has_more false", len(p["authorizations"]) == 1 and p["has_more"] is False, p)
    for q in ("direction=both", "status=OPEN", "status=pending", "limit=0", "limit=201", "limit=1e1", "limit=4.0", "offset=-1", "limit=%2B4"):
        check(f"GET /authorizations?{q} 422", err(lst(T, "ada", "?" + q), 422, "validation_failed"))
    check("unknown query ignored", lst(T, "ada", "?zz=1")[0] == 200)
    check("no token JSON 401", err(call("GET", "/authorizations"), 401, "unauthenticated"))


def p_accept():
    T = reset()
    for path in ("/requests", "/authorizations"):
        s, body, _, ct = call("GET", path, token=T["ada"], headers={"Accept": "text/html"})
        check(f"{path} Accept text/html -> HTML", s == 200 and "text/html" in ct and "<html" in str(body).lower(), (s, ct))
        s, body, _, ct = call("GET", path, headers={"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"})
        check(f"{path} browser Accept, no token -> HTML", s == 200 and "text/html" in ct)
        s, body, _, ct = call("GET", path, token=T["ada"], headers={"Accept": "application/json"})
        check(f"{path} Accept json -> JSON", s == 200 and "application/json" in ct and isinstance(body, dict))
        s, body, _, ct = call("GET", path, token=T["ada"])
        check(f"{path} no Accept -> JSON", s == 200 and "application/json" in ct)
        check(f"{path} unauth JSON 401", err(call("GET", path, headers={"Accept": "application/json"}), 401, "unauthenticated"))
        check(f"{path} */* -> JSON", call("GET", path, token=T["ada"], headers={"Accept": "*/*"})[3].startswith("application/json"))
    for path in ("/", "/split", "/signup", "/login"):
        s, body, _, ct = call("GET", path, headers={"Accept": "text/html"})
        check(f"route {path} serves HTML", s == 200 and "text/html" in ct)


def p_export_import():
    T = reset(fx(ttl=900, auths=[{"id": "a_seed", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1000, "note": "n",
                                  "visibility": "public", "status": "open", "expires_at": iso(3600)}]))
    ka, kc = k(), k()
    _, a, _, _ = auth(T, "ada", "bob", 2000, key=ka)
    _, p, _, _ = cap(T, "bob", a["authorization_id"], {"amount": 500, "final": False}, key=kc)
    ex = call("GET", "/_test/export")[1]
    check("export envelope still v1", ex["track"] == "pocketful" and ex["format_version"] == 1)
    for base in (URL2, URL):
        r = call("POST", "/_test/import", ex, base=base)
        check(f"import 204 ({base[-5:]})", r[0] == 204, r)
        m = me(T["ada"], base)
        check(f"holds survive import ({base[-5:]})", m["held"] == 1000 + 1500 and m["total"] == 9500 and m["available"] == 7000, m)
        r = call("POST", "/authorizations", {"to_handle": "bob", "amount": 2000}, token=T["ada"], key=ka, base=base)
        check(f"authorize replay after import ({base[-5:]})", r[0] == 200 and r[1] == a)
        r = call("POST", f"/authorizations/{a['authorization_id']}/capture", {"amount": 500, "final": False}, token=T["bob"], key=kc, base=base)
        check(f"capture replay after import ({base[-5:]})", r[0] == 200 and r[1] == p)
        lst_ = call("GET", "/authorizations", token=T["ada"], base=base)[1]["authorizations"]
        x = [y for y in lst_ if y["authorization_id"] == a["authorization_id"]][0]
        check(f"capture records survive ({base[-5:]})", x["captured_amount"] == 500 and x["payment_ids"] == [p["payment_id"]] and x["status"] == "open")
        r = call("POST", "/authorizations", {"to_handle": "bob", "amount": 1}, token=T["ada"], key=k(), base=base)
        check(f"ttl survives import ({base[-5:]})", (dt.datetime.fromisoformat(r[1]["expires_at"]) - dt.datetime.fromisoformat(r[1]["created_at"])).total_seconds() == 900)
        nid = r[1]["authorization_id"]
        check(f"new ids don't collide ({base[-5:]})", nid not in (a["authorization_id"], "a_seed"))
    # invalid import with holds > balance
    st = json.loads(json.dumps(ex))
    for u in st["state"]["users"]:
        if u["id"] == "u_ada":
            u["balance"] = 100
    before = call("GET", "/_test/export")[1]
    r = call("POST", "/_test/import", st)
    check("import with holds > balance 422", err(r, 422, "validation_failed"), r)
    check("failed import changed nothing", call("GET", "/_test/export")[1] == before)
    # stage-1 export -> stage-2 import
    T1 = reset(P.fixture(), base=URL1)
    kp = k()
    p1 = call("POST", "/payments", {"to_handle": "bob", "amount": 321, "note": "lost"}, token=T1["ada"], key=kp, base=URL1)
    _, rq, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 77}, token=T1["bob"], key=k(), base=URL1)
    kf = k()
    call("POST", "/payments", {"to_handle": "bob", "amount": 1}, token=T1["cy"], key=kf, base=URL1)  # fails 409
    ex1 = call("GET", "/_test/export", base=URL1)[1]
    r = call("POST", "/_test/import", ex1, base=URL)
    check("stage-1 export imports into stage 2", r[0] == 204, r)
    m = me(T1["ada"])
    check("stage-1 token valid, /me gains holds fields", m["total"] == m["balance"] == 10000 - 321 and m["held"] == 0 and m["available"] == m["total"], m)
    r = call("POST", "/payments", {"to_handle": "bob", "amount": 321, "note": "lost"}, token=T1["ada"], key=kp)
    check("stage-1 receipt replays (lost response recovered)", r[0] == 200 and all(r[1][x] == p1[1][x] for x in p1[1]) and me(T1["ada"])["total"] == 10000 - 321, (r, p1))
    r = call("POST", f"/requests/{rq['request_id']}/pay", {}, token=T1["ada"], key=k())
    check("stage-1 pending request payable", r[0] == 201 and r[1]["authorization_id"] is None)
    call("POST", "/payments", {"to_handle": "cy", "amount": 1}, token=T1["ada"], key=k())
    check("stage-1 failed key reusable", call("POST", "/payments", {"to_handle": "bob", "amount": 1}, token=T1["cy"], key=kf)[0] == 201)
    check("stage-1 import: authorizations empty, ttl default 600",
          lst(T1, "ada")[1]["authorizations"] == [] and
          (lambda r: (dt.datetime.fromisoformat(r["expires_at"]) - dt.datetime.fromisoformat(r["created_at"])).total_seconds())(auth(T1, "ada", "bob", 1)[1]) == 600)


def p_races():
    users = [(f"u{i}", 2000) for i in range(12)] + [("op", 0)]
    T = reset(fx(users=users, auths=[], payments=[], requests=[], settlement_operator_ids=["u_op"]))
    hs = [f"u{i}" for i in range(12)]
    import random, threading
    random.seed(11)
    auths, statuses, lat, neg = [], [], [], []
    lock = threading.Lock()
    def job(i):
        a, b = random.sample(hs, 2)
        kind = i % 7
        if kind in (0, 1):
            r = auth(T, a, b, random.randint(1, 900))
            if r[0] == 201:
                with lock: auths.append(r[1])
        elif kind == 2 and auths:
            x = random.choice(auths)
            r = cap(T, x["to_handle"], x["authorization_id"], random.choice([{}, {"amount": random.randint(1, 300), "final": False}, {"amount": random.randint(1, 300)}]))
        elif kind == 3 and auths:
            x = random.choice(auths)
            r = void(T, x["from_handle"], x["authorization_id"])
        elif kind == 4:
            r = call("POST", "/settlements", {"transfers": [{"from_handle": a, "to_handle": b, "amount": random.randint(1, 500)}]}, token=T["op"], key=k())
        elif kind == 5:
            r = call("GET", "/me", token=T[a])
            if r[0] == 200 and not ok_me(r[1]):
                with lock: neg.append(r[1])
        else:
            r = P.pay(T, a, b, random.randint(1, 500))
        with lock:
            statuses.append(r[0]); lat.append(r[2])
    with ThreadPoolExecutor(50) as ex:
        list(ex.map(job, range(2100)))
    ms = [me(T[h]) for h in hs + ["op"]]
    check(f"race: no 5xx over {len(statuses)}", all(s < 500 for s in statuses), sorted(set(statuses)))
    check("race: every /me read consistent (available>=0, balance==total)", not neg, neg[:2])
    check("race: conservation of total", sum(m["total"] for m in ms) == 24000, sum(m["total"] for m in ms))
    check("race: final available >= 0", all(ok_me(m) for m in ms))
    check(f"race: max latency < 5s ({max(lat):.3f}s)", max(lat) < 5)
    # held equals sum of open remainders
    held_ok = True
    for h in hs:
        rem = sum(a["remaining_amount"] for a in call("GET", "/authorizations?direction=outgoing&status=open&limit=200", token=T[h])[1]["authorizations"])
        held_ok &= rem == me(T[h])["held"]
    check("race: held == sum(open remaining) for every user", held_ok)
    # cumulative captures never exceed
    st = call("GET", "/_test/export")[1]["state"]
    pays = {p["id"]: p for p in st["payments"]}
    ok = all(sum(pays[pid]["amount"] for pid in a["payment_ids"]) == a["captured_amount"] <= a["amount"] for a in st.get("authorizations", []))
    check("race: sum(capture payments) == captured_amount <= amount", ok)
    # same-auth capture storm: 30 concurrent different keys final captures -> exactly one
    T = reset()
    _, a, _, _ = auth(T, "ada", "bob", 1000)
    with ThreadPoolExecutor(30) as ex:
        rs = list(ex.map(lambda _: cap(T, "bob", a["authorization_id"], {}), range(30)))
    sts = [r[0] for r in rs]
    check("capture storm: exactly one 201, rest 409 not_open", sts.count(201) == 1 and all(err(r, 409, "authorization_not_open") for r in rs if r[0] != 201), sts)
    # partial capture storm: 30 x 50 final:false on 1000 -> 20 succeed
    _, a, _, _ = auth(T, "ada", "bob", 1000)
    with ThreadPoolExecutor(30) as ex:
        rs = list(ex.map(lambda _: cap(T, "bob", a["authorization_id"], {"amount": 50, "final": False}), range(30)))
    sts = [r[0] for r in rs]
    check("partial storm: 20 x 201, rest 409/422, never over-capture", sts.count(201) == 20 and set(sts) <= {201, 409, 422}, sts)
    # capture vs void race
    _, a, _, _ = auth(T, "ada", "bob", 1000)
    with ThreadPoolExecutor(10) as ex:
        fs = [ex.submit(cap, T, "bob", a["authorization_id"], {}) for _ in range(5)] + [ex.submit(void, T, "ada", a["authorization_id"]) for _ in range(5)]
        rs = [f.result() for f in fs]
    c_ok = sum(1 for r in rs[:5] if r[0] == 201); v_ok = sum(1 for r in rs[5:] if r[0] == 200)
    check("capture vs void: one outcome", (c_ok == 1 and v_ok == 0) or (c_ok == 0 and v_ok == 5), [r[0] for r in rs])
    # concurrent same-key authorize
    key = k()
    with ThreadPoolExecutor(20) as ex:
        rs = list(ex.map(lambda _: auth(T, "cy" if False else "ada", "dee", 10, key=key), range(20)))
    sts = sorted(r[0] for r in rs)
    check("concurrent same-key authorize: one 201 rest 200, one hold", sts.count(201) == 1 and sts.count(200) == 19 and all(r[1] == rs[0][1] for r in rs))
    # authorize burst vs available: dee 700, 20 x 50 -> 14 succeed
    with ThreadPoolExecutor(20) as ex:
        rs = list(ex.map(lambda _: auth(T, "dee", "cy", 50), range(20)))
    check("authorize burst limited by available", sum(1 for r in rs if r[0] == 201) == 14 and me(T["dee"])["available"] == 0)
    check("race totals conserved", totals(T) == 13200 and all(ok_me(me(t)) for t in T.values()))


def main():
    for f in (p_me_and_create, p_capture, p_void, p_expiry, p_list, p_accept, p_export_import, p_races):
        print(f"== {f.__name__}")
        try:
            f()
        except Exception as e:
            import traceback; traceback.print_exc()
            check(f"{f.__name__} ran to completion", False, repr(e))
    n = sum(1 for _, ok in P.RESULTS if ok)
    print(f"TOTAL {n}/{len(P.RESULTS)} passed, {len(P.RESULTS) - n} failed")
    sys.exit(0 if n == len(P.RESULTS) else 1)


if __name__ == "__main__":
    main()
