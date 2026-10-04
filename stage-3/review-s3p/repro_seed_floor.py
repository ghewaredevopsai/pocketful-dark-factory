"""Repro: seeded items without created_at are dated at floor(reset second), before a seeded
payment whose supplied created_at lies later in that same second (still <= reset time)."""
import json, sys, time, datetime as dt, urllib.request, urllib.parse, uuid
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
now = dt.datetime.now(dt.timezone.utc)
while now.microsecond < 300000: time.sleep(0.05); now = dt.datetime.now(dt.timezone.utc)
X = (now - dt.timedelta(milliseconds=100)).isoformat()      # earlier than reset, later than floor(reset)
exp = (now + dt.timedelta(hours=2)).replace(microsecond=0).isoformat()
f = {"currency": "EUR", "minor_units": 2,
     "users": [U("ada", 10000), U("bob", 1800 - 5), U("cy", 700 + 5)],
     "payments": [{"id": "s1", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 700, "created_at": X},
                  {"id": "s2", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 5}],          # created_at omitted = reset time
     "authorizations": [{"id": "ah", "from_user_id": "u_cy", "to_user_id": "u_ada", "amount": 700, "status": "open", "expires_at": exp}]}  # created at reset
print("0. POST /_test/reset:", call("POST", "/_test/reset", f)[0], " seeded s1.created_at =", X)
tc = call("POST", "/auth/login", {"email": "cy@example.com", "password": "correct horse"})[1]["token"]
tb = call("POST", "/auth/login", {"email": "bob@example.com", "password": "correct horse"})[1]["token"]
a = call("GET", "/authorizations", tok=tc)[1]["authorizations"][0]
print("1. seeded hold created_at (should be reset time, >= s1):", a["created_at"])
st = call("GET", "/statement", tok=tc)[1]
print("2. statement order (s2 has omitted created_at = reset time, so expected s1, s2):",
      [(e["payment"]["payment_id"], e["effective_at"]) for e in st["entries"]])
s, m = call("GET", "/me?as_of=" + urllib.parse.quote(a["created_at"], safe=""), tok=tc)
print("3. cy /me?as_of=<hold created_at>:", {k: m[k] for k in ("total", "held", "available")})
s, c = call("POST", "/payments/s1/corrections", {"expected_revision": 1, "amount": 701, "effective_at": X, "reason": "credit cy 1"}, tb, uuid.uuid4().hex)
print("4. bob corrects s1 700 -> 701 (only credits cy):", s, c)
