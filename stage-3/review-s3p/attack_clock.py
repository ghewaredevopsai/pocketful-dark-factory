#!/usr/bin/env python3
"""Attack probes for the stage-3 event clock: can history order an event before one that truly
preceded it? Usage: python3 attack_clock.py <s3-url> <stage2-url>"""
import os, sys, json, time, datetime as dt, random
from concurrent.futures import ThreadPoolExecutor
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
URL, URLS2 = sys.argv[1], sys.argv[2]
sys.argv = [sys.argv[0], URL, URL, URL, URLS2]
import s3_probes as S
from s3_probes import call, check, err, k, q, ns, user, basic_fixture, me, stmt, corr, reset, full_statement, ago
import probes as P

UTC = dt.timezone.utc
def iso_ns(n): return dt.datetime.fromtimestamp(n // 10**9, UTC).replace(microsecond=(n % 10**9) // 1000).isoformat()
def pm(n, d_us): return iso_ns(n + d_us * 1000)


def view(t, at, known=None):
    qq = "?as_of=" + q(at) + ("&known_at=" + q(known) if known else "")
    x = me(t, qq)[1]
    return x["total"], x["held"], x["available"]


def a_sequence():
    """Every event type back-to-back; created/closed instants strictly ordered and views at ±1µs exact."""
    T = reset(dict(basic_fixture(ops=("u_op",)), authorization_ttl_seconds=600))
    ev = []
    p1 = P.pay(T, "bob", "cy", 700)[1]; ev.append(("pay", p1["created_at"]))
    a1 = call("POST", "/authorizations", {"to_handle": "ada", "amount": 700}, token=T["cy"], key=k())[1]; ev.append(("auth", a1["created_at"]))
    c1 = call("POST", f"/authorizations/{a1['authorization_id']}/capture", {"amount": 200, "final": False}, token=T["ada"], key=k())[1]; ev.append(("capture", c1["created_at"]))
    st = call("POST", "/settlements", {"transfers": [{"from_handle": "dee", "to_handle": "cy", "amount": 100}]}, token=T["op"], key=k())[1]; ev.append(("settle", st["committed_at"]))
    a2 = call("POST", "/authorizations", {"to_handle": "ada", "amount": 100}, token=T["cy"], key=k())[1]; ev.append(("auth2", a2["created_at"]))
    v2 = call("POST", f"/authorizations/{a2['authorization_id']}/void", token=T["cy"])[1]; ev.append(("void", v2["closed_at"]))
    cr = corr(T["bob"], p1["payment_id"], {"expected_revision": 1, "amount": 800, "effective_at": p1["created_at"], "reason": "x"})[1]; ev.append(("corr-recorded", cr["recorded_at"]))
    c2 = call("POST", f"/authorizations/{a1['authorization_id']}/capture", {}, token=T["ada"], key=k())[1]; ev.append(("final-capture", c2["created_at"]))
    insts = [ns(x[1]) for x in ev]
    check("event instants strictly increase across all event types", all(insts[i] < insts[i + 1] for i in range(len(insts) - 1)), ev)
    check("all event stamps carry microseconds + offset", all("." in x[1] and x[1][-6] in "+-" for x in ev), ev)
    check("auth expires_at - created_at == ttl exactly", ns(a1["expires_at"]) - ns(a1["created_at"]) == 600 * 10**9, (a1["created_at"], a1["expires_at"]))
    al = {x["authorization_id"]: x for x in call("GET", "/authorizations", token=T["cy"])[1]["authorizations"]}
    check("closed_at of final capture == capture created_at; void closed_at == void response",
          ns(al[a1["authorization_id"]]["closed_at"]) == ns(c2["created_at"]) and ns(al[a2["authorization_id"]]["closed_at"]) == ns(v2["closed_at"]))
    # cy view at ±1µs of each event (cy: +800 at p1 (corrected), hold 700 from a1, -200 at c1, +100 settle, hold 100 a2..void, -500 final)
    exp = {}
    tA, tC1, tS, tA2, tV, tC2 = [ns(x[1]) for x in ev[1:6]] + [ns(ev[7][1])]
    cases = [(tA, -1, (800, 0, 800)), (tA, 0, (800, 700, 100)), (tC1, -1, (800, 700, 100)), (tC1, 0, (600, 500, 100)),
             (tS, 0, (700, 500, 200)), (tA2, -1, (700, 500, 200)), (tA2, 0, (700, 600, 100)), (tV, -1, (700, 600, 100)), (tV, 0, (700, 500, 200)),
             (tC2, -1, (700, 500, 200)), (tC2, 0, (200, 0, 200))]
    bad = []
    for base, d, want in cases:
        got = view(T["cy"], pm(base, d))
        if got != want: bad.append((pm(base, d), got, want))
    check("cy /me?as_of at each event instant and 1µs before matches hand-computed history", not bad, bad)
    # known_at before correction recorded: p1 counted at 700
    got = view(T["cy"], pm(tA, 0), pm(ns(cr["recorded_at"]), -1))
    check("known_at 1µs before correction recorded: original amount", got == (700, 700, 0), got)
    # same instant written with other offsets
    alt = dt.datetime.fromisoformat(a1["created_at"]).astimezone(dt.timezone(dt.timedelta(hours=-8))).isoformat()
    check("as_of at hold instant in -08:00 offset == +00:00", view(T["cy"], alt) == view(T["cy"], a1["created_at"]))
    # correction effective exactly at the hold instant
    T2 = reset(basic_fixture())
    p = P.pay(T2, "bob", "cy", 500)[1]
    a = call("POST", "/authorizations", {"to_handle": "ada", "amount": 500}, token=T2["cy"], key=k())[1]
    r = corr(T2["bob"], p["payment_id"], {"expected_revision": 1, "amount": 500, "effective_at": a["created_at"], "reason": "same instant as hold"})
    check("correction moving funding to exactly the hold instant: combined → 201", r[0] == 201, r[:2])
    r = corr(T2["bob"], p["payment_id"], {"expected_revision": 2, "amount": 500, "effective_at": pm(ns(a["created_at"]), 1), "reason": "after hold"})
    check("moving funding 1µs after the hold → 409 historical_overdraft", err(r, 409, "historical_overdraft"), r[:2])


def a_seeded_reset_second():
    """Seeded payment with a µs created_at inside the reset second vs a seeded open hold without created_at."""
    now = dt.datetime.now(UTC)
    # wait until well into a second so floor(reset) < seeded instant <= reset
    while now.microsecond < 300000:
        time.sleep(0.05); now = dt.datetime.now(UTC)
    seeded_at = (now - dt.timedelta(milliseconds=100)).isoformat()
    f = basic_fixture([{"id": "sf", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 700, "created_at": seeded_at}],
                      users=[user("ada", 10000), user("bob", 1800), user("cy", 700), user("dee", 700), user("op", 0)])
    f["authorizations"] = [{"id": "ah", "from_user_id": "u_cy", "to_user_id": "u_ada", "amount": 700, "status": "open", "expires_at": ago(-7200)}]
    r = call("POST", "/_test/reset", f)
    check("fixture: seeded µs payment + seeded open hold (no created_at) accepted", r[0] == 204, r)
    T = {h: P.login(h) for h in ("ada", "bob", "cy", "dee")}
    a = [x for x in call("GET", "/authorizations", token=T["cy"])[1]["authorizations"] if x["authorization_id"] == "ah"][0]
    print("INFO seeded payment", seeded_at, "seeded hold created_at", a["created_at"])
    check("seeded hold (assumed created at reset) is not before a seeded payment that precedes reset", ns(a["created_at"]) >= ns(seeded_at), (seeded_at, a["created_at"]))
    neg = []
    for at in (a["created_at"], seeded_at, pm(ns(seeded_at), -1), pm(ns(seeded_at), 1)):
        t_, h_, av = view(T["cy"], at)
        if av < 0 or t_ < 0: neg.append((at, t_, h_, av))
    check("no negative available in any seeded-history view", not neg, neg)
    r = corr(T["bob"], "sf", {"expected_revision": 1, "amount": 701, "effective_at": seeded_at, "reason": "credit cy 1"})
    check("credit-only correction to the funding payment → 201 (no false overdraft)", r[0] == 201, r[:2])


def a_stage2_import():
    """Stage-2 export with whole-second payments/holds, imported into stage 3."""
    T = reset(dict(basic_fixture(ops=("u_op",)), authorization_ttl_seconds=600), base=URLS2)
    # fund then authorize everything, nonfinal capture, void, all within one second if possible
    while time.time() % 1 > 0.3: time.sleep(0.01)
    p = call("POST", "/payments", {"to_handle": "cy", "amount": 600}, token=T["bob"], key=k(), base=URLS2)[1]
    a = call("POST", "/authorizations", {"to_handle": "ada", "amount": 600}, token=T["cy"], key=k(), base=URLS2)[1]
    c = call("POST", f"/authorizations/{a['authorization_id']}/capture", {"amount": 100, "final": False}, token=T["ada"], key=k(), base=URLS2)[1]
    a2 = call("POST", "/authorizations", {"to_handle": "bob", "amount": 50}, token=T["dee"], key=k(), base=URLS2)[1]
    call("POST", f"/authorizations/{a2['authorization_id']}/void", token=T["dee"], base=URLS2)
    print("INFO stage-2 times", p["created_at"], a["created_at"], c["created_at"])
    ex = call("GET", "/_test/export", base=URLS2)[1]
    check("stage-2 export with holds imports", call("POST", "/_test/import", ex)[0] == 204)
    m = me(T["cy"])[1]
    check("imported current: cy total 500, held 500, available 0", (m["total"], m["held"], m["available"]) == (500, 500, 0), m)
    neg = []
    for at in (p["created_at"], a["created_at"], c["created_at"], ago(1)):
        for who in ("cy", "dee"):
            t_, h_, av = view(T[who], at)
            if av < 0 or t_ < 0: neg.append((who, at, t_, h_, av))
    check("imported whole-second history: no negative available at event instants", not neg, neg)
    al = call("GET", "/authorizations", token=T["cy"])[1]["authorizations"]
    x = [y for y in al if y["authorization_id"] == a["authorization_id"]][0]
    check("imported hold: expires_at - created_at == 600, closed_at null while open", ns(x["expires_at"]) - ns(x["created_at"]) == 600 * 10**9 and x["closed_at"] is None, x)
    r = corr(T["bob"], p["payment_id"], {"expected_revision": 1, "amount": 650, "effective_at": p["created_at"], "reason": "credit"})
    check("credit-only correction after stage-2 import → 201", r[0] == 201, r[:2])
    a3 = call("POST", "/authorizations", {"to_handle": "ada", "amount": 50}, token=T["cy"], key=k())[1]
    check("new hold after import is stamped after all imported events", ns(a3["created_at"]) > max(ns(p["created_at"]), ns(c["created_at"])))


def a_race():
    """authorize vs corrections vs payments: no negative available in any past view afterwards."""
    users = [user(f"u{i}", 2000) for i in range(8)]
    T = reset({"currency": "EUR", "minor_units": 2, "users": users, "payments": [], "requests": [], "authorization_ttl_seconds": 600})
    hs = [f"u{i}" for i in range(8)]
    pays, sts, lat = [], [], []
    random.seed(9)
    def job(i):
        a, b = random.sample(hs, 2)
        kind = i % 4
        if kind == 0:
            r = P.pay(T, a, b, random.randint(1, 300))
            if r[0] == 201: pays.append((a, r[1]["payment_id"], r[1]["created_at"]))
        elif kind == 1:
            r = call("POST", "/authorizations", {"to_handle": b, "amount": random.randint(1, 400)}, token=T[a], key=k())
        elif kind == 2 and pays:
            owner, pid, cat = random.choice(pays)
            cur = S.revs(T[owner], pid)[1]["revisions"][-1]["revision"]
            r = corr(T[owner], pid, {"expected_revision": cur, "amount": random.randint(0, 400), "effective_at": cat, "reason": "race"})
        else:
            r = me(T[a], "?as_of=" + q(ago(random.randint(0, 3))))
        sts.append(r[0]); lat.append(r[2])
    with ThreadPoolExecutor(50) as ex:
        list(ex.map(job, range(800)))
    check(f"race authorize/correct/pay: no 5xx ({len(sts)}), max {max(lat):.2f}s", all(s < 500 for s in sts) and max(lat) < 5, sorted(set(sts)))
    # scan every event instant per user
    neg, bad_sum = [], []
    insts = set()
    for h in hs:
        for e in full_statement(T[h])["entries"]: insts.add(ns(e["effective_at"]))
        for a in call("GET", "/authorizations?limit=200", token=T[h])[1]["authorizations"]:
            insts.add(ns(a["created_at"]))
            if a["closed_at"]: insts.add(ns(a["closed_at"]))
    insts = sorted(insts)
    sample = insts[::max(1, len(insts) // 80)] + insts[-3:]
    for n in sample:
        tot = 0
        for h in hs:
            t_, h_, av = view(T[h], iso_ns(n))
            tot += t_
            if av < 0 or t_ < 0: neg.append((h, iso_ns(n), t_, h_, av))
        if tot != 16000: bad_sum.append((iso_ns(n), tot))
    check(f"race: no negative total/available at {len(sample)} event instants x 8 users", not neg, neg[:3])
    check("race: sum of totals == seeded at every sampled instant", not bad_sum, bad_sum[:3])


def main():
    for f in (a_sequence, a_seeded_reset_second, a_stage2_import, a_race):
        print(f"== {f.__name__}", flush=True)
        try: f()
        except Exception as e:
            import traceback; traceback.print_exc(); check(f"{f.__name__} ran", False, repr(e)[:300])
    n = sum(1 for _, ok in P.RESULTS if ok)
    print(f"TOTAL {n}/{len(P.RESULTS)} passed, {len(P.RESULTS) - n} failed")


if __name__ == "__main__":
    main()
