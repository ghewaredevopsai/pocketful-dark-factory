"""Extra reviewer probes: hostile number/body inputs, time offsets, reset during load."""
import sys, json, time
from concurrent.futures import ThreadPoolExecutor
sys.argv = sys.argv[:2]
import probes as P
from probes import call, check, err, k, reset, fixture, me, pay, total

T = reset()
for raw in ('{"to_handle":"bob","amount":1e999999999}', '{"to_handle":"bob","amount":1' + '0'*5000 + '}',
            '{"to_handle":"bob","amount":-1e-999999}'):
    r = call("POST", "/payments", token=T["ada"], key=k(), raw=raw)
    check(f"hostile amount {raw[25:45]!r} -> 4xx fast", 400 <= r[0] < 500 and r[2] < 1, r[:1])
r = call("POST", "/payments", token=T["ada"], key=k(), raw='{"to_handle":"bob","amount":0.000000000000000000001e21}')
deep = '{"a":' + '[' * 100000 + ']' * 100000 + ',"to_handle":"bob","amount":1}'
r = call("POST", "/payments", token=T["ada"], key=k(), raw=deep)
check("deeply nested body no 5xx", r[0] < 500, r[:2])
r = call("POST", "/payments", token=T["ada"], key=k(), raw='{"to_handle":"bob","amount":1,"pad":"' + 'x' * (2 << 20) + '"}')
check("2 MiB body no 5xx", r[0] < 500, r[:2])
r = call("POST", "/payments", token=T["ada"], key=k(), raw='{"to_handle":"bob","amount":1} {}')
check("trailing data 400", err(r, 400, "malformed_request"), r)
r = call("POST", "/payments", token=T["ada"], key=k(), raw='')
check("empty body 400", err(r, 400, "malformed_request"), r)
# time offsets in seeded data: ordering by instant, value preserved
f = fixture(extra={"payments": [
    {"id": "p_a", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1, "note": "", "visibility": "public", "created_at": "2026-09-24T19:00:00+02:00"},
    {"id": "p_b", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1, "note": "", "visibility": "public", "created_at": "2026-09-24T17:30:00+00:00"},
    {"id": "p_c", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1, "note": "", "visibility": "public", "created_at": "2026-09-24T12:00:00-08:00"}]})
T = reset(f)
fd = call("GET", "/activity", token=T["cy"])[1]["payments"]
ids = [p["payment_id"] for p in fd]
check("seeded offsets ordered by instant (c,b,a) after new ones", ids[-3:] == ["p_c", "p_b", "p_a"], ids)
check("seeded created_at with offset is RFC3339 w/ offset", all(p["created_at"][-6] in "+-" for p in fd), [p["created_at"] for p in fd])
pay(T, "ada", "bob", 1); pay(T, "ada", "bob", 2)
fd = call("GET", "/activity?limit=2", token=T["cy"])[1]["payments"]
check("same-second payments both newest", sorted(p["amount"] for p in fd) == [1, 2], fd)
# reset racing writes: after final reset, state equals fixture exactly
T = reset()
def w(i):
    try: pay(T, "ada", "bob", 1)
    except Exception: pass
with ThreadPoolExecutor(30) as ex:
    fs = [ex.submit(w, i) for i in range(300)]
    for _ in range(5):
        call("POST", "/_test/reset", fixture())
    [f.result() for f in fs]
call("POST", "/_test/reset", fixture())
T = {u["handle"]: P.login(u["handle"]) for u in fixture()["users"]}
check("after reset race, only fixture state", total(T) == 13200 and me(T["ada"])["balance"] == 10000 and
      len(call("GET", "/activity", token=T["cy"])[1]["payments"]) == 0 and len(call("GET", "/activity", token=T["ada"])[1]["payments"]) == 1)
check("method not allowed has envelope", call("DELETE", "/payments", token=T["ada"])[1].get("error") is not None)
n = sum(1 for _, ok in P.RESULTS if ok); print(f"TOTAL {n}/{len(P.RESULTS)}")
r = call("POST", "/payments", token=T["ada"], key=k(), raw='{"to_handle":"bob","amount":0.000000000000000000001e21}')
check("0.000000000000000000001e21 == 1 accepted", r[0] == 201 and r[1]["amount"] == 1, r[:2])
n = sum(1 for _, ok in P.RESULTS if ok); print(f"TOTAL {n}/{len(P.RESULTS)}")
