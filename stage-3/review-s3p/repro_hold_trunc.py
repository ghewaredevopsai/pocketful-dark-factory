"""Repro: authorization created_at truncated to whole seconds precedes a same-second funding payment."""
import json, sys, time, uuid, urllib.request, urllib.parse
B = sys.argv[1]
def call(m, p, body=None, tok=None, key=None):
    r = urllib.request.Request(B + p, method=m, data=json.dumps(body).encode() if body is not None else None)
    r.add_header("Content-Type", "application/json")
    if tok: r.add_header("Authorization", "Bearer " + tok)
    if key: r.add_header("Idempotency-Key", key)
    try:
        with urllib.request.urlopen(r) as x: t = x.read().decode(); return x.status, json.loads(t) if t else None
    except urllib.error.HTTPError as e: return e.code, json.loads(e.read().decode())
U = lambda h, b: {"id": "u_"+h, "email": h+"@example.com", "password": "correct horse", "display_name": h, "handle": h, "balance": b}
for attempt in range(20):
    call("POST", "/_test/reset", {"currency": "EUR", "minor_units": 2, "users": [U("ada", 0), U("bob", 10000)]})
    ta = call("POST", "/auth/login", {"email": "ada@example.com", "password": "correct horse"})[1]["token"]
    tb = call("POST", "/auth/login", {"email": "bob@example.com", "password": "correct horse"})[1]["token"]
    # wait until early in a wall-clock second so both writes land in the same second
    while time.time() % 1 > 0.2: time.sleep(0.01)
    s1, p = call("POST", "/payments", {"to_handle": "ada", "amount": 5000}, tb, uuid.uuid4().hex)
    s2, a = call("POST", "/authorizations", {"to_handle": "bob", "amount": 5000}, ta, uuid.uuid4().hex)
    if p["created_at"][:19] == a["created_at"][:19]:
        break
print("1. POST /payments bob->ada 5000:", s1, "created_at", p["created_at"])
print("2. POST /authorizations ada->bob 5000:", s2, "created_at", a["created_at"])
q = urllib.parse.quote(a["created_at"], safe="")
s, m = call("GET", f"/me?as_of={q}", tok=ta)
print(f"3. GET /me?as_of={a['created_at']} (ada):", s, {k: m[k] for k in ("total", "held", "available")})
s, cur = call("GET", "/me", tok=ta)
print("   current /me (ada):", {k: cur[k] for k in ("total", "held", "available")})
s, c = call("POST", f"/payments/{p['payment_id']}/corrections",
            {"expected_revision": 1, "amount": 6000, "effective_at": p["created_at"], "reason": "underpaid"}, tb, uuid.uuid4().hex)
print("4. bob corrects his payment to ada 5000 -> 6000 (credits ada):", s, c)
