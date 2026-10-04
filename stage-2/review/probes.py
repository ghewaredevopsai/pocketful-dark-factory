#!/usr/bin/env python3
"""Reviewer probe suite for Pocketful stage 1, written from the requirements text.

Usage: python3 probes.py http://127.0.0.1:18080 [http://127.0.0.1:18081]
The second URL (another instance of the same image) is used for cross-process import.
Stdlib only. Prints one line per probe and a PASS/FAIL total.
"""
import json, sys, threading, time, uuid, re
import http.client
from urllib.parse import urlparse
from concurrent.futures import ThreadPoolExecutor

A = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:18080"
B = sys.argv[2] if len(sys.argv) > 2 else None
RESULTS = []


def call(method, path, body=None, token=None, key=None, raw=None, base=A, headers=None, timeout=10):
    u = urlparse(base)
    c = http.client.HTTPConnection(u.hostname, u.port, timeout=timeout)
    h = {"Content-Type": "application/json"}
    if token:
        h["Authorization"] = "Bearer " + token
    if key is not None:
        h["Idempotency-Key"] = key
    h.update(headers or {})
    data = raw if raw is not None else (json.dumps(body) if body is not None else None)
    if isinstance(data, str):
        data = data.encode()
    t0 = time.monotonic()
    c.request(method, path, body=data, headers=h)
    r = c.getresponse()
    txt = r.read().decode()
    dt = time.monotonic() - t0
    c.close()
    try:
        js = json.loads(txt) if txt else None
    except ValueError:
        js = txt
    return r.status, js, dt, r.getheader("Content-Type")


def check(name, cond, detail=""):
    RESULTS.append((name, bool(cond)))
    print(("PASS " if cond else "FAIL ") + name + ("" if cond else "  -> " + str(detail)[:400]))


def err(res, status, code):
    s, js, _, _ = res
    return s == status and isinstance(js, dict) and js.get("error", {}).get("code") == code and \
        isinstance(js["error"].get("message"), str)


def k():
    return uuid.uuid4().hex


USERS = [("ada", 10000), ("bob", 2500), ("cy", 0), ("dee", 700), ("op", 0)]


def fixture(minor=2, cur="EUR", users=USERS, ops=("u_op",), extra=None):
    f = {"currency": cur, "minor_units": minor,
         "users": [{"id": "u_" + h, "email": h + "@example.com", "password": "correct horse",
                    "display_name": h.title(), "handle": h, "balance": b} for h, b in users],
         "payments": [{"id": "p_seed", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 500,
                       "note": "coffee", "visibility": "private"}],
         "requests": [{"id": "rq_seed", "requester_id": "u_bob", "payer_id": "u_ada", "amount": 1200,
                       "note": "taxi", "status": "pending"}],
         "settlement_operator_ids": list(ops)}
    if extra:
        f.update(extra)
    return f


def reset(f=None, base=A):
    f = f or fixture()
    s = call("POST", "/_test/reset", f, base=base)
    assert s[0] == 204, s
    return {u["handle"]: login(u["handle"], base) for u in f["users"]}


def login(h, base=A):
    s, js, _, _ = call("POST", "/auth/login", {"email": h + "@example.com", "password": "correct horse"}, base=base)
    assert s == 200, (s, js)
    return js["token"]


def me(t, base=A):
    return call("GET", "/me", token=t, base=base)[1]


def total(T, base=A):
    return sum(me(t, base)["balance"] for t in T.values())


def pay(T, frm, to, amount, key=None, **kw):
    b = {"to_handle": to, "amount": amount}
    b.update(kw)
    return call("POST", "/payments", b, token=T[frm], key=key or k())


# ---------------------------------------------------------------- probes

def p_runtime():
    s, js, _, ct = call("GET", "/health")
    check("health 200 ok", s == 200 and js == {"status": "ok"}, (s, js))
    check("content-type json utf-8", ct and "application/json" in ct and "utf-8" in ct.lower(), ct)
    check("unknown route 404 envelope", err(call("GET", "/nope"), 404, "not_found"))


def p_reset():
    T = reset()
    check("reset: seeded totals", total(T) == 13200, total(T))
    s = call("POST", "/_test/reset", fixture(users=[("ada", -1), ("bob", 5)]))
    check("reset negative balance 422", err(s, 422, "validation_failed"), s)
    check("reset 422 left state unchanged", total(T) == 13200)
    check("reset unparseable 400", err(call("POST", "/_test/reset", raw="{bad"), 400, "malformed_request"))
    for mu, cur in ((0, "JPY"), (3, "BHD")):
        T2 = reset(fixture(minor=mu, cur=cur))
        m = me(T2["ada"])
        check(f"reset minor_units {mu}", m["currency"] == cur and m["minor_units"] == mu, m)
    T = reset(); reset()
    check("repeated reset ok, old tokens invalid", err(call("GET", "/me", token=T["ada"]), 401, "unauthenticated"))
    T = reset()
    check("me shape", me(T["ada"]) == {"user_id": "u_ada", "display_name": "Ada", "handle": "ada",
                                       "balance": 10000, "currency": "EUR", "minor_units": 2}, me(T["ada"]))


def p_auth():
    T = reset()
    s, js, _, _ = call("POST", "/auth/signup", {"email": "Zed.Q-X+1@ex.com", "password": "12345678", "display_name": "Z"})
    check("signup 201 shape", s == 201 and set(js) == {"user_id", "display_name", "token"}, (s, js))
    m = me(js["token"])
    check("signup derived handle", m["handle"] == "zed_q_x_1" and m["balance"] == 0, m)
    s, js, _, _ = call("POST", "/auth/signup", {"email": "abcdefghijklmnopqrstuvwxyz@ex.com", "password": "12345678", "display_name": "L"})
    check("signup handle truncated to 20", s == 201 and me(js["token"])["handle"] == "abcdefghijklmnopqrst", (s, js))
    check("signup email taken 409", err(call("POST", "/auth/signup", {"email": "ada@example.com", "password": "12345678", "display_name": "x"}), 409, "email_taken"))
    s = call("POST", "/auth/signup", {"email": "ada@other.org", "password": "12345678", "display_name": "x"})
    check("signup handle taken 409", err(s, 409, "handle_taken"), s)
    check("handle_taken created no account", call("POST", "/auth/login", {"email": "ada@other.org", "password": "12345678"})[0] == 401)
    check("signup short password 422", err(call("POST", "/auth/signup", {"email": "q1@x.com", "password": "1234567", "display_name": "x"}), 422, "validation_failed"))
    for e in ("noat", "@x.com", "q@", "a@b@c"):
        check(f"signup bad email {e!r} 422", err(call("POST", "/auth/signup", {"email": e, "password": "12345678", "display_name": "x"}), 422, "validation_failed"))
    check("login wrong pw 401", err(call("POST", "/auth/login", {"email": "ada@example.com", "password": "nope nope"}), 401, "unauthenticated"))
    check("login unknown 401", err(call("POST", "/auth/login", {"email": "zz@example.com", "password": "nope nope"}), 401, "unauthenticated"))
    t2 = login("ada")
    check("multiple tokens valid", me(T["ada"])["handle"] == "ada" and me(t2)["handle"] == "ada")
    for h in ({"Authorization": "Bearer"}, {"Authorization": "Basic abc"}, {"Authorization": "Bearer nope"}):
        check(f"401 for {h}", err(call("GET", "/me", headers=h), 401, "unauthenticated"))
    check("signup non-object body 400", err(call("POST", "/auth/signup", raw="[1]"), 400, "malformed_request"))
    check("signup wrong type 400", err(call("POST", "/auth/signup", {"email": 5, "password": "12345678", "display_name": "x"}), 400, "malformed_request"))
    ex = call("GET", "/_test/export")[1]
    check("export has no plaintext password", "correct horse" not in json.dumps(ex) and "12345678" not in json.dumps(ex))


def p_amounts():
    T = reset()
    for a, ok in ((1000, True), (1000.0, True), ("RAW1e3", True), ("RAW1E3", True), ("RAW10e2", True),
                  (0, False), (-1, False), (1.5, False), ("1000", False), (True, False), (None, False),
                  (1000000001, False), ("RAW1e400", False), ("RAW1e-400", False), ([], False), ({}, False),
                  ("RAW0.1e4", True), ("RAW-0", False)):
        if isinstance(a, str) and a.startswith("RAW"):
            raw = '{"to_handle":"bob","amount":%s}' % a[3:]
            r = call("POST", "/payments", token=T["ada"], key=k(), raw=raw)
        else:
            r = pay(T, "ada", "bob", a)
        if ok:
            check(f"amount {a!r} accepted", r[0] == 201 and r[1]["amount"] == 1000 and isinstance(r[1]["amount"], int), r)
        else:
            check(f"amount {a!r} 422", err(r, 422, "validation_failed"), r)
    T = reset(fixture(users=[("ada", 2000000000), ("bob", 0), ("cy", 0), ("dee", 0), ("op", 0)]))
    check("amount 1000000000 accepted", pay(T, "ada", "bob", 1000000000)[0] == 201)
    check("amount 1 accepted", pay(T, "ada", "bob", 1)[0] == 201)
    check("total conserved", total(T) == 2000000000 + 0)


def p_payments():
    T = reset()
    s, js, _, _ = pay(T, "ada", "bob", 1500, note="dinner", visibility="public")
    keys = {"payment_id", "from_user_id", "from_handle", "to_user_id", "to_handle", "amount", "currency",
            "note", "visibility", "request_id", "created_at"}
    check("payment 201 shape", s == 201 and keys <= set(js) and js["request_id"] is None and js["currency"] == "EUR"
          and js["from_handle"] == "ada" and js["to_user_id"] == "u_bob", js)
    check("created_at rfc3339 offset", re.match(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?([+-]\d\d:\d\d|Z)$", js["created_at"]) and not js["created_at"].endswith("Z"), js["created_at"])
    check("id <=64", len(js["payment_id"]) <= 64)
    check("balances moved", me(T["ada"])["balance"] == 8500 and me(T["bob"])["balance"] == 4000)
    s, js, _, _ = pay(T, "ada", "bob", 1)
    check("defaults note '' public", js["note"] == "" and js["visibility"] == "public", js)
    check("insufficient 409", err(pay(T, "cy", "bob", 1), 409, "insufficient_funds"))
    check("exact balance ok", pay(T, "dee", "cy", 700)[0] == 201 and me(T["dee"])["balance"] == 0)
    check("one over balance 409", err(pay(T, "cy", "dee", 701), 409, "insufficient_funds"))
    check("self payment 422", err(pay(T, "ada", "ada", 1), 422, "self_payment"))
    check("unknown handle 404", err(pay(T, "ada", "nobody", 1), 404, "not_found"))
    check("note 200 ok", pay(T, "ada", "bob", 1, note="x" * 200)[0] == 201)
    check("note 201 422", err(pay(T, "ada", "bob", 1, note="x" * 201), 422, "validation_failed"))
    check("note 200 emoji chars ok", pay(T, "ada", "bob", 1, note="😀" * 200)[0] == 201)
    check("note null 422", err(pay(T, "ada", "bob", 1, note=None), 422, "validation_failed"))
    check("note number 422", err(pay(T, "ada", "bob", 1, note=5), 422, "validation_failed"))
    for v in ("PUBLIC", "", None, 1, "friends"):
        check(f"visibility {v!r} 422", err(pay(T, "ada", "bob", 1, visibility=v), 422, "validation_failed"))
    weird = "  héllo\t<b>&amp;</b> 👩‍👩‍👧 \u0301e\"\\ "
    s, js, _, _ = pay(T, "ada", "bob", 1, note=weird)
    feed = call("GET", "/activity?limit=1", token=T["bob"])[1]["payments"][0]
    check("note verbatim round trip", js["note"] == weird and feed["note"] == weird, (js["note"], feed["note"]))
    check("to_handle wrong type 400", err(pay(T, "ada", 5, 1), 400, "malformed_request"))
    check("missing to_handle 422", err(call("POST", "/payments", {"amount": 1}, token=T["ada"], key=k()), 422, "validation_failed"))
    check("unknown fields ignored", pay(T, "ada", "bob", 1, zzz=[1, 2])[0] == 201)
    check("unparseable 400", err(call("POST", "/payments", raw="{", token=T["ada"], key=k()), 400, "malformed_request"))
    check("array body 400", err(call("POST", "/payments", raw="[]", token=T["ada"], key=k()), 400, "malformed_request"))
    check("no token 401 before key", err(call("POST", "/payments", {"to_handle": "bob", "amount": 1}), 401, "unauthenticated"))
    check("missing key 400", err(call("POST", "/payments", {"to_handle": "bob", "amount": 1}, token=T["ada"]), 400, "missing_idempotency_key"))
    check("empty key 400", err(call("POST", "/payments", {"to_handle": "bob", "amount": 1}, token=T["ada"], key=""), 400, "missing_idempotency_key"))
    check("key 255 ok", pay(T, "ada", "bob", 1, key="k" * 255)[0] == 201)
    check("key 256 422", err(pay(T, "ada", "bob", 1, key="k" * 256), 422, "validation_failed"))
    check("key 1 ok", pay(T, "ada", "bob", 1, key="z")[0] == 201)
    check("failed payment no trace", total(T) == 13200)


def p_idempotency():
    T = reset()
    key = k()
    r1 = pay(T, "ada", "bob", 100, key=key, note="n")
    r2 = call("POST", "/payments", raw='{ "note":"n",  "amount":100, "to_handle":"bob"}', token=T["ada"], key=key)
    check("first 201 replay 200 identical", r1[0] == 201 and r2[0] == 200 and r1[1] == r2[1], (r1[:2], r2[:2]))
    check("replay moved money once", me(T["ada"])["balance"] == 9900)
    check("diff body 409", err(pay(T, "ada", "bob", 101, key=key, note="n"), 409, "idempotency_key_reuse"))
    check("claimed key beats invalid body", err(pay(T, "ada", "bob", "zz", key=key), 409, "idempotency_key_reuse"))
    check("claimed key beats invalid body (self)", err(pay(T, "ada", "ada", 1, key=key), 409, "idempotency_key_reuse"))
    check("other user same key independent", pay(T, "bob", "ada", 100, key=key, note="n")[0] == 201)
    r = call("POST", "/requests", {"payer_handle": "bob", "amount": 100, "note": "n"}, token=T["ada"], key=key)
    check("same key different path is new", r[0] == 201, r)
    k2 = k()
    check("4xx claims nothing (insufficient)", err(pay(T, "cy", "bob", 5, key=k2), 409, "insufficient_funds"))
    pay(T, "ada", "cy", 5)
    check("key reusable after 4xx with same body", pay(T, "cy", "bob", 5, key=k2)[0] == 201)
    k3 = k()
    check("4xx validation claims nothing", err(pay(T, "ada", "bob", 0, key=k3), 422, "validation_failed"))
    check("key reusable after 422 with different body", pay(T, "ada", "bob", 7, key=k3)[0] == 201)
    # concurrent identical
    key = k()
    before = me(T["ada"])["balance"]
    with ThreadPoolExecutor(30) as ex:
        rs = list(ex.map(lambda _: pay(T, "ada", "bob", 13, key=key), range(30)))
    sts = sorted(r[0] for r in rs)
    check("concurrent same key: one 201 rest 200", sts.count(201) == 1 and sts.count(200) == 29, sts)
    check("concurrent same key: identical bodies", all(r[1] == rs[0][1] for r in rs))
    check("concurrent same key: effect once", me(T["ada"])["balance"] == before - 13)
    # concurrent conflicting bodies same key
    key = k()
    with ThreadPoolExecutor(20) as ex:
        rs = list(ex.map(lambda i: pay(T, "ada", "bob", 1 + i % 2, key=key), range(20)))
    sts = [r[0] for r in rs]
    check("concurrent conflicting bodies: one 201, others 200/409", sts.count(201) == 1 and set(sts) <= {200, 201, 409}, sts)
    check("total conserved after idem", total(T) == 13200)


def p_requests():
    T = reset()
    s, rq, _, _ = call("POST", "/requests", {"payer_handle": "cy", "amount": 5000, "note": "taxi"}, token=T["bob"], key=k())
    keys = {"request_id", "requester_id", "requester_handle", "payer_id", "payer_handle", "amount", "currency",
            "note", "status", "payment_id", "created_at"}
    check("request 201 shape (exceeds payer balance)", s == 201 and keys <= set(rq) and rq["status"] == "pending"
          and rq["payment_id"] is None and rq["payer_handle"] == "cy", (s, rq))
    rid = rq["request_id"]
    check("request not in feed", all(p.get("request_id") != rid for p in call("GET", "/activity", token=T["cy"])[1]["payments"]))
    check("self request 422", err(call("POST", "/requests", {"payer_handle": "bob", "amount": 1}, token=T["bob"], key=k()), 422, "self_request"))
    check("request unknown 404", err(call("POST", "/requests", {"payer_handle": "zz", "amount": 1}, token=T["bob"], key=k()), 404, "not_found"))
    check("request amount 0 422", err(call("POST", "/requests", {"payer_handle": "cy", "amount": 0}, token=T["bob"], key=k()), 422, "validation_failed"))
    check("request note 201 422", err(call("POST", "/requests", {"payer_handle": "cy", "amount": 1, "note": "x" * 201}, token=T["bob"], key=k()), 422, "validation_failed"))
    check("request missing key 400", err(call("POST", "/requests", {"payer_handle": "cy", "amount": 1}, token=T["bob"]), 400, "missing_idempotency_key"))
    path = f"/requests/{rid}/pay"
    check("pay by requester 403", err(call("POST", path, {}, token=T["bob"], key=k()), 403, "forbidden"))
    check("pay by third party 403", err(call("POST", path, {}, token=T["ada"], key=k()), 403, "forbidden"))
    check("pay unknown 404", err(call("POST", "/requests/rq_nope/pay", {}, token=T["cy"], key=k()), 404, "not_found"))
    pk = k()
    check("pay while short 409", err(call("POST", path, {}, token=T["cy"], key=pk), 409, "insufficient_funds"))
    check("short pay changed nothing", me(T["cy"])["balance"] == 0 and call("GET", "/requests", token=T["cy"])[1]["requests"][0]["status"] == "pending")
    pay(T, "ada", "cy", 5000)
    s, p, _, _ = call("POST", path, {"visibility": "private"}, token=T["cy"], key=pk)
    check("pay after funds arrive 201 (same key reused after 4xx)", s == 201 and p["request_id"] == rid and p["visibility"] == "private"
          and p["from_user_id"] == "u_cy" and p["to_user_id"] == "u_bob" and p["amount"] == 5000 and p["note"] == "taxi", (s, p))
    r = call("POST", path, {"visibility": "private"}, token=T["cy"], key=pk)
    check("pay replay 200 identical after paid", r[0] == 200 and r[1] == p, r)
    check("pay replay {} vs public differ", True)
    check("pay with new key -> 409 not pending", err(call("POST", path, {}, token=T["cy"], key=k()), 409, "request_not_pending"))
    check("pay changed key body 409 reuse", err(call("POST", path, {"visibility": "public"}, token=T["cy"], key=pk), 409, "idempotency_key_reuse"))
    lst = call("GET", "/requests", token=T["bob"])[1]["requests"]
    mine = [x for x in lst if x["request_id"] == rid][0]
    check("request paid carries payment_id", mine["status"] == "paid" and mine["payment_id"] == p["payment_id"], mine)
    check("money moved once", me(T["cy"])["balance"] == 0 and total(T) == 13200)
    check("decline paid 409", err(call("POST", f"/requests/{rid}/decline", token=T["cy"]), 409, "request_not_pending"))
    check("cancel paid 409", err(call("POST", f"/requests/{rid}/cancel", token=T["bob"]), 409, "request_not_pending"))
    # {} vs {"visibility":"public"}
    _, r2, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 1}, token=T["bob"], key=k())
    kk = k()
    a = call("POST", f"/requests/{r2['request_id']}/pay", {}, token=T["ada"], key=kk)
    b = call("POST", f"/requests/{r2['request_id']}/pay", {"visibility": "public"}, token=T["ada"], key=kk)
    check("{} then {visibility:public} same key 409", a[0] == 201 and a[1]["visibility"] == "public" and err(b, 409, "idempotency_key_reuse"), (a, b))
    # decline / cancel
    _, r3, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 1}, token=T["bob"], key=k())
    p3 = f"/requests/{r3['request_id']}"
    check("decline by requester 403", err(call("POST", p3 + "/decline", token=T["bob"]), 403, "forbidden"))
    check("cancel by payer 403", err(call("POST", p3 + "/cancel", token=T["ada"]), 403, "forbidden"))
    check("decline by third party 403", err(call("POST", p3 + "/decline", token=T["cy"]), 403, "forbidden"))
    d = call("POST", p3 + "/decline", token=T["ada"])
    check("decline 200 declined", d[0] == 200 and d[1]["status"] == "declined", d)
    d2 = call("POST", p3 + "/decline", token=T["ada"])
    check("decline twice 200", d2[0] == 200 and d2[1]["status"] == "declined")
    check("cancel declined 409", err(call("POST", p3 + "/cancel", token=T["bob"]), 409, "request_not_pending"))
    check("pay declined 409", err(call("POST", p3 + "/pay", {}, token=T["ada"], key=k()), 409, "request_not_pending"))
    _, r4, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 1}, token=T["bob"], key=k())
    p4 = f"/requests/{r4['request_id']}"
    c = call("POST", p4 + "/cancel", token=T["bob"])
    check("cancel 200", c[0] == 200 and c[1]["status"] == "cancelled")
    check("cancel twice 200", call("POST", p4 + "/cancel", token=T["bob"])[0] == 200)
    check("decline cancelled 409", err(call("POST", p4 + "/decline", token=T["ada"]), 409, "request_not_pending"))
    check("decline unknown 404", err(call("POST", "/requests/zz/decline", token=T["ada"]), 404, "not_found"))
    check("decline no token 401", err(call("POST", p4 + "/decline"), 401, "unauthenticated"))
    # concurrent pay of same request with different keys: at most once
    pay(T, "ada", "dee", 3000)
    _, r5, _, _ = call("POST", "/requests", {"payer_handle": "dee", "amount": 3000}, token=T["bob"], key=k())
    p5 = f"/requests/{r5['request_id']}/pay"
    with ThreadPoolExecutor(25) as ex:
        rs = list(ex.map(lambda _: call("POST", p5, {}, token=T["dee"], key=k()), range(25)))
    sts = [r[0] for r in rs]
    check("concurrent pay different keys: exactly one 201", sts.count(201) == 1 and sts.count(409) == 24, sts)
    check("request moved money once", me(T["dee"])["balance"] == 700 and total(T) == 13200)
    # concurrent pay vs cancel
    _, r6, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 10}, token=T["bob"], key=k())
    p6 = f"/requests/{r6['request_id']}"
    with ThreadPoolExecutor(10) as ex:
        fs = [ex.submit(call, "POST", p6 + "/pay", {}, T["ada"], k()) for _ in range(5)] + \
             [ex.submit(call, "POST", p6 + "/cancel", None, T["bob"]) for _ in range(5)]
        rs = [f.result() for f in fs]
    paid = sum(1 for r in rs[:5] if r[0] == 201)
    canc = sum(1 for r in rs[5:] if r[0] == 200)
    check("pay vs cancel race: exactly one outcome", (paid == 1 and canc == 0) or (paid == 0 and canc == 5), [r[0] for r in rs])


def p_list_requests():
    T = reset()
    ids = []
    for i in range(5):
        ids.append(call("POST", "/requests", {"payer_handle": "ada", "amount": i + 1}, token=T["bob"], key=k())[1]["request_id"])
        time.sleep(0.01)
    out = call("POST", "/requests", {"payer_handle": "bob", "amount": 9}, token=T["ada"], key=k())[1]["request_id"]
    call("POST", "/requests", {"payer_handle": "dee", "amount": 9}, token=T["cy"], key=k())
    s, js, _, _ = call("GET", "/requests", token=T["ada"])
    got = [r["request_id"] for r in js["requests"]]
    check("list requests only own (incl seed)", set(got) == set(ids) | {out, "rq_seed"}, got)
    check("list requests newest first", got.index(out) < got.index(ids[0]) and got.index(ids[4]) < got.index(ids[0]), got)
    inc = [r["request_id"] for r in call("GET", "/requests?direction=incoming", token=T["ada"])[1]["requests"]]
    check("direction incoming", set(inc) == set(ids) | {"rq_seed"}, inc)
    og = [r["request_id"] for r in call("GET", "/requests?direction=outgoing", token=T["ada"])[1]["requests"]]
    check("direction outgoing", og == [out], og)
    check("third party sees none", [r for r in call("GET", "/requests", token=T["op"])[1]["requests"]] == [])
    call("POST", f"/requests/{ids[0]}/decline", token=T["ada"])
    dl = call("GET", "/requests?status=declined", token=T["ada"])[1]["requests"]
    check("status filter", [r["request_id"] for r in dl] == [ids[0]], dl)
    for q in ("direction=both", "status=PAID", "status=", "direction=", "limit=0", "limit=201", "limit=-1", "limit=1e9",
              "limit=4.0", "limit=%2B4", "offset=-1", "offset=1e2", "limit=abc", "limit="):
        check(f"GET /requests?{q} 422", err(call("GET", "/requests?" + q, token=T["ada"]), 422, "validation_failed"))
    p = call("GET", "/requests?limit=2&offset=0", token=T["ada"])[1]
    check("limit 2 has_more", len(p["requests"]) == 2 and p["has_more"] is True, p)
    p = call("GET", "/requests?limit=2&offset=5", token=T["ada"])[1]
    check("last page has_more false", len(p["requests"]) == 2 and p["has_more"] is False, p)
    p = call("GET", "/requests?limit=7", token=T["ada"])[1]
    check("exact fit has_more false", len(p["requests"]) == 7 and p["has_more"] is False, len(p["requests"]))
    p = call("GET", "/requests?offset=100", token=T["ada"])[1]
    check("offset past end empty", p == {"requests": [], "has_more": False}, p)
    check("limit 200 / 1 ok", call("GET", "/requests?limit=200", token=T["ada"])[0] == 200 and call("GET", "/requests?limit=1", token=T["ada"])[0] == 200)
    check("unknown query ignored", call("GET", "/requests?foo=bar", token=T["ada"])[0] == 200)
    check("leading zeros digits ok", call("GET", "/requests?limit=007", token=T["ada"])[0] == 200)
    check("list requests no token 401", err(call("GET", "/requests"), 401, "unauthenticated"))


def p_feed():
    T = reset()
    a = pay(T, "ada", "bob", 1, visibility="private")[1]["payment_id"]
    b = pay(T, "bob", "cy", 1, visibility="public")[1]["payment_id"]
    _, rq, _, _ = call("POST", "/requests", {"payer_handle": "dee", "amount": 2}, token=T["cy"], key=k())
    c = call("POST", f"/requests/{rq['request_id']}/pay", {"visibility": "private"}, token=T["dee"], key=k())[1]["payment_id"]
    def feed(h):
        return {p["payment_id"]: p for p in call("GET", "/activity?limit=200", token=T[h])[1]["payments"]}
    fa, fb, fc, fd, fo = feed("ada"), feed("bob"), feed("cy"), feed("dee"), feed("op")
    check("private seen by sender", a in fa and "p_seed" in fa)
    check("private seen by receiver", a in fb and "p_seed" in fb)
    check("private hidden from third parties", a not in fc and a not in fd and a not in fo and "p_seed" not in fo)
    check("public seen by everyone", all(b in f for f in (fa, fb, fc, fd, fo)))
    check("private request payment: payer and requester only", c in fc and c in fd and c not in fa and c not in fo)
    check("visibility identical across parties", fa[a]["visibility"] == fb[a]["visibility"] == "private")
    check("feed has payments only", all("payment_id" in p for p in fa.values()))
    check("operator gets no private access", a not in fo and call("GET", "/requests", token=T["op"])[1]["requests"] == [])
    order = [p["payment_id"] for p in call("GET", "/activity", token=T["bob"])[1]["payments"]]
    check("feed newest first (seed last)", order[-1] == "p_seed", order)
    for q in ("limit=0", "limit=201", "offset=-1", "limit=1.0"):
        check(f"activity {q} 422", err(call("GET", "/activity?" + q, token=T["ada"]), 422, "validation_failed"))
    p = call("GET", "/activity?limit=1", token=T["bob"])[1]
    check("activity has_more", len(p["payments"]) == 1 and p["has_more"] is True)
    check("activity no token 401", err(call("GET", "/activity"), 401, "unauthenticated"))


def p_splits():
    T = reset()
    def split(amount, hs, who="ada", key=None, **kw):
        b = {"amount": amount, "participant_handles": hs}
        b.update(kw)
        return call("POST", "/splits", b, token=T[who], key=key or k())
    for amt, n, exp in ((1000, 3, [334, 333, 333]), (1, 3, [1, 0, 0]), (10, 3, [4, 3, 3]), (999, 3, [333, 333, 333]), (5, 5, [1, 1, 1, 1, 1])):
        hs = ["ada", "bob", "cy", "dee", "op"][:n]
        s, js, _, _ = split(amt, hs)
        check(f"split {amt}/{n} shares {exp}", s == 201 and [x["amount"] for x in js["shares"]] == exp
              and [x["handle"] for x in js["shares"]] == hs, (s, js))
        check(f"split {amt}/{n} requests excl caller, in order", [r["payer_handle"] for r in js["requests"]] == hs[1:]
              and [r["amount"] for r in js["requests"]] == exp[1:] and all(r["status"] == "pending" and r["requester_handle"] == "ada" for r in js["requests"]), js["requests"])
    s, js, _, _ = split(1000, ["bob", "cy", "ada"], note="dinner")
    check("split order: extra unit to first", [x["amount"] for x in js["shares"]] == [334, 333, 333] and js["shares"][0]["handle"] == "bob")
    check("split body shape", {"split_id", "amount", "currency", "note", "shares", "requests", "created_at"} <= set(js) and js["note"] == "dinner" and js["currency"] == "EUR")
    s, js, _, _ = split(1, ["bob", "cy", "dee"])
    check("split caller omitted: requests for all", len(js["requests"]) == 3 and [r["amount"] for r in js["requests"]] == [1, 0, 0], js)
    check("zero share still a request", js["requests"][1]["amount"] == 0 and js["requests"][1]["status"] == "pending")
    z = js["requests"][1]["request_id"]
    zp = call("POST", f"/requests/{z}/pay", {}, token=T["cy"], key=k())
    check("paying a zero-share request (reported only)", zp[0] in (201, 409, 422), zp)
    s, js, _, _ = split(500, ["ada"])
    check("split only caller valid, requests []", s == 201 and js["requests"] == [] and js["shares"] == [{"handle": "ada", "amount": 500}], js)
    check("split empty 422", err(split(10, []), 422, "validation_failed"))
    check("split duplicate 422", err(split(10, ["bob", "bob"]), 422, "validation_failed"))
    check("split dup caller 422", err(split(10, ["ada", "ada"]), 422, "validation_failed"))
    before = len(call("GET", "/requests?limit=200", token=T["bob"])[1]["requests"])
    check("split unknown handle 404", err(split(10, ["bob", "zz"]), 404, "not_found"))
    after = len(call("GET", "/requests?limit=200", token=T["bob"])[1]["requests"])
    check("unknown handle split created nothing", before == after, (before, after))
    for a in (0, 1000000001, 1.5, "100", True):
        check(f"split amount {a!r} 422", err(split(a, ["bob"]), 422, "validation_failed"))
    check("split 1000000000 ok", split(1000000000, ["bob", "cy"])[0] == 201)
    check("split note 201 422", err(split(10, ["bob"], note="x" * 201), 422, "validation_failed"))
    check("split note null 422", err(split(10, ["bob"], note=None), 422, "validation_failed"))
    check("split handles not array 400", err(split(10, "bob"), 400, "malformed_request"))
    check("split missing key 400", err(call("POST", "/splits", {"amount": 1, "participant_handles": ["bob"]}, token=T["ada"]), 400, "missing_idempotency_key"))
    key = k()
    r1 = split(10, ["ada", "bob"], key=key)
    r2 = split(10, ["ada", "bob"], key=key)
    check("split replay 200 identical", r1[0] == 201 and r2[0] == 200 and r1[1] == r2[1])
    check("split diff body 409", err(split(11, ["ada", "bob"], key=key), 409, "idempotency_key_reuse"))
    check("split not a feed item", all("split_id" not in p for p in call("GET", "/activity?limit=200", token=T["ada"])[1]["payments"]))
    check("split checked no balance (cy has 0)", split(5000, ["cy", "ada"], who="cy")[0] == 201)
    # pay all split requests in full, conservation
    T = reset()
    s, js, _, _ = split(1001, ["bob", "ada", "dee"], who="ada")
    for r in js["requests"]:
        h = r["payer_handle"]
        call("POST", f"/requests/{r['request_id']}/pay", {}, token=T[h], key=k())
    check("after splits paid total conserved", total(T) == 13200 and me(T["ada"])["balance"] == 10000 + 334 + 333, me(T["ada"]))


def p_settlements():
    T = reset()
    def st(transfers, who="op", key=None, raw=None):
        return call("POST", "/settlements", {"transfers": transfers} if raw is None else None, token=T[who], key=key or k(), raw=raw)
    check("settlement no token 401", err(call("POST", "/settlements", {"transfers": []}), 401, "unauthenticated"))
    check("settlement non-operator 403", err(st([{"from_handle": "ada", "to_handle": "bob", "amount": 1}], who="ada"), 403, "forbidden"))
    check("settlement missing key 400", err(call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "bob", "amount": 1}]}, token=T["op"]), 400, "missing_idempotency_key"))
    # net affordability: cy has 0 but receives first
    tr = [{"from_handle": "ada", "to_handle": "cy", "amount": 100}, {"from_handle": "cy", "to_handle": "bob", "amount": 100, "note": "n", "visibility": "private", "zz": 1}]
    s, js, _, _ = st(tr)
    check("settlement 201 net-affordable", s == 201 and {"settlement_id", "committed_at", "payments"} <= set(js), (s, js))
    ps = js["payments"]
    check("members in input order", [(p["from_handle"], p["to_handle"]) for p in ps] == [("ada", "cy"), ("cy", "bob")])
    check("members linked, null request_id, same created_at == committed_at",
          all(p["settlement_id"] == js["settlement_id"] and p["request_id"] is None and p["created_at"] == js["committed_at"] for p in ps), ps)
    check("member defaults/notes", ps[0]["note"] == "" and ps[0]["visibility"] == "public" and ps[1]["note"] == "n" and ps[1]["visibility"] == "private")
    check("settlement balances", me(T["cy"])["balance"] == 0 and me(T["bob"])["balance"] == 2600 and total(T) == 13200)
    normal = pay(T, "ada", "bob", 1)[1]
    check("nonmember settlement_id null", "settlement_id" in normal and normal["settlement_id"] is None, normal)
    f = {p["payment_id"]: p for p in call("GET", "/activity?limit=200", token=T["dee"])[1]["payments"]}
    check("members follow feed visibility", ps[0]["payment_id"] in f and ps[1]["payment_id"] not in f)
    fc = {p["payment_id"]: p for p in call("GET", "/activity?limit=200", token=T["cy"])[1]["payments"]}
    check("feed member carries settlement_id", fc[ps[1]["payment_id"]]["settlement_id"] == js["settlement_id"])
    # insufficient collective funds
    snap = [me(T[h])["balance"] for h in ("ada", "bob", "cy", "dee")]
    check("collective insufficient 409", err(st([{"from_handle": "dee", "to_handle": "cy", "amount": 700}, {"from_handle": "cy", "to_handle": "bob", "amount": 701}]), 409, "insufficient_funds"))
    check("409 all-or-nothing", snap == [me(T[h])["balance"] for h in ("ada", "bob", "cy", "dee")])
    # entry precedence in input order
    r = st([{"from_handle": "cy", "to_handle": "bob", "amount": 99999}, {"from_handle": "zz", "to_handle": "bob", "amount": 1}, {"from_handle": "ada", "to_handle": "ada", "amount": 1}])
    check("entry error beats insufficient (404 first)", err(r, 404, "not_found"), r)
    r = st([{"from_handle": "ada", "to_handle": "ada", "amount": 1}, {"from_handle": "zz", "to_handle": "bob", "amount": 1}])
    check("first entry error wins (self_payment)", err(r, 422, "self_payment"), r)
    r = st([{"from_handle": "ada", "to_handle": "bob", "amount": 0}, {"from_handle": "zz", "to_handle": "bob", "amount": 1}])
    check("first entry error wins (amount 422)", err(r, 422, "validation_failed"), r)
    r = st([{"from_handle": "ada", "to_handle": "bob", "amount": 1, "note": None}])
    check("member note null 422", err(r, 422, "validation_failed"), r)
    r = st([{"from_handle": "ada", "to_handle": "bob", "amount": 1, "visibility": "x"}])
    check("member bad visibility 422", err(r, 422, "validation_failed"), r)
    check("transfers empty 422", err(st([]), 422, "validation_failed"))
    t32 = [{"from_handle": "ada", "to_handle": "bob", "amount": 1}] * 32
    s32 = st(t32)
    check("32 transfers ok", s32[0] == 201 and len(s32[1]["payments"]) == 32 and len({p["payment_id"] for p in s32[1]["payments"]}) == 32)
    check("33 transfers 422", err(st(t32 + t32[:1]), 422, "validation_failed"))
    check("transfers missing 422", err(call("POST", "/settlements", {}, token=T["op"], key=k()), 422, "validation_failed"))
    check("transfer element not object 422", err(st([5]), 422, "validation_failed"))
    # replay
    key = k()
    r1 = st(tr[:1] + [{"from_handle": "bob", "to_handle": "dee", "amount": 3}], key=key)
    bal = total(T)
    r2 = st(tr[:1] + [{"from_handle": "bob", "to_handle": "dee", "amount": 3}], key=key)
    check("settlement replay 200 identical", r1[0] == 201 and r2[0] == 200 and r1[1] == r2[1])
    check("settlement replay no movement", me(T["dee"])["balance"] == 703 and total(T) == bal)
    check("settlement diff body 409", err(st(tr[:1], key=key), 409, "idempotency_key_reuse"))
    k4 = k()
    check("failed settlement claims no key", err(st([{"from_handle": "cy", "to_handle": "bob", "amount": 10**6}], key=k4), 409, "insufficient_funds"))
    check("key reusable after failed settlement", st(tr[:1], key=k4)[0] == 201)
    # concurrent settlements vs payments: no negative, conservation
    T = reset()
    def s_job(i):
        return st([{"from_handle": "dee", "to_handle": "cy", "amount": 100}, {"from_handle": "cy", "to_handle": "bob", "amount": 100}])
    def p_job(i):
        return pay(T, "dee", "ada", 100)
    with ThreadPoolExecutor(40) as ex:
        fs = [ex.submit(s_job if i % 2 else p_job, i) for i in range(40)]
        rs = [f.result() for f in fs]
    check("settlement/payment race: only 201/409", {r[0] for r in rs} <= {201, 409}, {r[0] for r in rs})
    ok = sum(1 for r in rs if r[0] == 201)
    check("settlement/payment race: dee spent exactly 700", ok == 7 and me(T["dee"])["balance"] == 0 and total(T) == 13200, (ok, me(T["dee"])))


def p_export_import():
    T = reset()
    key = k()
    p1 = pay(T, "ada", "bob", 111, key=key, note="✓")
    fk = k()
    pay(T, "cy", "bob", 1, key=fk)  # fails 409
    sk = k()
    s1 = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "dee", "amount": 5}]}, token=T["op"], key=sk)
    _, rq, _, _ = call("POST", "/requests", {"payer_handle": "ada", "amount": 7}, token=T["bob"], key=k())
    s, ex, _, _ = call("GET", "/_test/export")
    check("export 200 envelope", s == 200 and ex["track"] == "pocketful" and ex["format_version"] == 1 and isinstance(ex["state"], dict), (s, str(ex)[:200]))
    pay(T, "ada", "bob", 1)
    s2, ex2, _, _ = call("GET", "/_test/export")
    check("export is a snapshot (later write not in old)", json.dumps(ex) != json.dumps(ex2))
    bases = [A] + ([B] if B else [])
    for base in bases:
        tag = "cross-process" if base != A else "same-process"
        if base != A:
            reset(fixture(users=[("zed", 5), ("ada", 1), ("bob", 1), ("cy", 1), ("dee", 1), ("op", 1)]), base=base)
        s = call("POST", "/_test/import", ex, base=base)
        check(f"import 204 ({tag})", s[0] == 204, s)
        call("POST", "/_test/import", ex, base=base)  # repeat: replacement not merge
        def m(h):
            return call("GET", "/me", token=T[h], base=base)
        check(f"old tokens valid after import ({tag})", m("ada")[0] == 200 and m("ada")[1]["balance"] == 10000 - 111 - 5, m("ada"))
        check(f"balances restored, total ({tag})", sum(m(h)[1]["balance"] for h in T) == 13200)
        check(f"login with password after import ({tag})", call("POST", "/auth/login", {"email": "bob@example.com", "password": "correct horse"}, base=base)[0] == 200)
        check(f"previous dest data removed ({tag})", call("POST", "/auth/login", {"email": "zed@example.com", "password": "correct horse"}, base=base)[0] == 401)
        r = call("POST", "/payments", {"to_handle": "bob", "amount": 111, "note": "✓"}, token=T["ada"], key=key, base=base)
        check(f"payment replay 200 original after import ({tag})", r[0] == 200 and r[1] == p1[1], r)
        r = call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "dee", "amount": 5}]}, token=T["op"], key=sk, base=base)
        check(f"settlement replay after import ({tag})", r[0] == 200 and r[1] == s1[1])
        check(f"operator permission preserved ({tag})", call("POST", "/settlements", {"transfers": [{"from_handle": "ada", "to_handle": "dee", "amount": 1}]}, token=T["op"], key=k(), base=base)[0] == 201)
        call("POST", "/payments", {"to_handle": "cy", "amount": 1}, token=T["ada"], key=k(), base=base)
        r = call("POST", "/payments", {"to_handle": "bob", "amount": 1}, token=T["cy"], key=fk, base=base)
        check(f"failed key reusable after import ({tag})", r[0] == 201, r)
        feed = call("GET", "/activity?limit=200", token=T["ada"], base=base)[1]["payments"]
        orig = [p for p in feed if p["payment_id"] == p1[1]["payment_id"]]
        check(f"ids/timestamps preserved ({tag})", orig and orig[0]["created_at"] == p1[1]["created_at"] and len([p for p in feed if p["payment_id"] == p1[1]["payment_id"]]) == 1)
        lr = call("GET", "/requests", token=T["bob"], base=base)[1]["requests"]
        check(f"requests preserved, pending payable ({tag})", any(x["request_id"] == rq["request_id"] and x["status"] == "pending" for x in lr))
        newp = call("POST", "/payments", {"to_handle": "bob", "amount": 1}, token=T["ada"], key=k(), base=base)[1]
        check(f"new ids don't collide after import ({tag})", newp["payment_id"] not in {p["payment_id"] for p in feed})
    # invalid imports leave state unchanged
    T_old = T
    T = reset()
    before = call("GET", "/_test/export")[1]
    for name, body in (("missing state", {"track": "pocketful", "format_version": 1}),
                       ("wrong track", dict(ex, track="tablekeeper")),
                       ("wrong version", dict(ex, format_version=2)),
                       ("state not object", dict(ex, state=[1])),
                       ("state garbage", dict(ex, state={"foo": 1})),
                       ("missing track", {"format_version": 1, "state": ex["state"]}),
                       ("empty object", {})):
        r = call("POST", "/_test/import", body)
        check(f"import {name} 422", err(r, 422, "validation_failed"), r)
    check("import unparseable 400", err(call("POST", "/_test/import", raw="{x"), 400, "malformed_request"))
    after = call("GET", "/_test/export")[1]
    check("failed imports changed nothing", before == after)
    # reset clears imported state
    call("POST", "/_test/import", ex)
    check("imported token valid", call("GET", "/me", token=T_old["ada"])[0] == 200)
    reset()
    check("reset clears imported tokens", err(call("GET", "/me", token=T_old["ada"]), 401, "unauthenticated"))


def p_load():
    T = reset(fixture(users=[(f"u{i}", 1000) for i in range(20)] + [("op", 0)], ops=("u_op",), extra={"payments": [], "requests": []}))
    hs = [f"u{i}" for i in range(20)]
    import random
    random.seed(7)
    lat, statuses = [], []
    lock = threading.Lock()
    def job(i):
        a, b = random.sample(hs, 2)
        kind = i % 5
        if kind == 0:
            r = call("POST", "/settlements", {"transfers": [{"from_handle": a, "to_handle": b, "amount": random.randint(1, 600)},
                                                            {"from_handle": b, "to_handle": a, "amount": random.randint(1, 600)}]}, token=T["op"], key=k())
        elif kind == 1:
            r = call("POST", "/splits", {"amount": random.randint(1, 999), "participant_handles": [a, b]}, token=T[a], key=k())
        elif kind == 2:
            r = call("GET", "/activity", token=T[a])
        else:
            r = pay(T, a, b, random.randint(1, 400))
        with lock:
            lat.append(r[2]); statuses.append(r[0])
    t0 = time.monotonic()
    with ThreadPoolExecutor(50) as ex:
        list(ex.map(job, range(2000)))
    # pay outstanding requests concurrently
    reqs = []
    for h in hs:
        for rq in call("GET", "/requests?direction=incoming&status=pending&limit=200", token=T[h])[1]["requests"]:
            reqs.append((h, rq["request_id"]))
    with ThreadPoolExecutor(50) as ex:
        rs = list(ex.map(lambda x: call("POST", f"/requests/{x[1]}/pay", {}, token=T[x[0]], key=k()), reqs))
    statuses += [r[0] for r in rs]; lat += [r[2] for r in rs]
    dt = time.monotonic() - t0
    bals = [me(T[h])["balance"] for h in hs + ["op"]]
    check(f"load: no 5xx over {len(statuses)} requests", all(s < 500 for s in statuses), sorted(set(statuses)))
    check("load: conservation", sum(bals) == 20000, sum(bals))
    check("load: no negative balance", min(bals) >= 0, min(bals))
    check(f"load: max latency < 5s (max {max(lat):.3f}s, total {dt:.1f}s)", max(lat) < 5)
    ex = call("GET", "/_test/export")[1]["state"]
    seen = {}
    for p in ex["payments"]:
        if p.get("request_id"):
            seen[p["request_id"]] = seen.get(p["request_id"], 0) + 1
    check("load: each request paid at most once", all(v == 1 for v in seen.values()))
    # 50 concurrent logins + big reset within limits
    with ThreadPoolExecutor(50) as ex:
        rs = list(ex.map(lambda i: call("POST", "/auth/login", {"email": f"u{i%20}@example.com", "password": "correct horse"}), range(50)))
    check(f"50 concurrent logins 200 < 5s (max {max(r[2] for r in rs):.2f}s)", all(r[0] == 200 for r in rs) and max(r[2] for r in rs) < 5)
    big = fixture(users=[(f"b{i}", 10) for i in range(300)], ops=())
    for i, u in enumerate(big["users"]):
        u["password"] = f"password-{i}"
    big["payments"] = []
    big["requests"] = []
    r = call("POST", "/_test/reset", big, timeout=30)
    check(f"300-user unique-password reset 204 < 10s ({r[2]:.2f}s)", r[0] == 204 and r[2] < 10, r[:1])


def main():
    for f in (p_runtime, p_reset, p_auth, p_amounts, p_payments, p_idempotency, p_requests, p_list_requests,
              p_feed, p_splits, p_settlements, p_export_import, p_load):
        print(f"== {f.__name__}")
        try:
            f()
        except Exception as e:  # a crashed probe group is a failure, not a pass
            import traceback; traceback.print_exc()
            check(f"{f.__name__} ran to completion", False, repr(e))
    n = sum(1 for _, ok in RESULTS if ok)
    print(f"TOTAL {n}/{len(RESULTS)} passed, {len(RESULTS) - n} failed")
    sys.exit(0 if n == len(RESULTS) else 1)


if __name__ == "__main__":
    main()
