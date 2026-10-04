#!/usr/bin/env python3
"""Systematic seeded-timestamp / reset-instant / reset-race attack probes for stage 3.
Usage: python3 attack_seed.py <s3-url> <stage1-url> <stage2-url>"""
import os, sys, json, time, random, threading, datetime as dt
from concurrent.futures import ThreadPoolExecutor
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
URL, URL1, URLS2 = sys.argv[1:4]
sys.argv = [sys.argv[0], URL, URL, URL1, URLS2]
import s3_probes as S
from s3_probes import call, check, err, k, q, ns, user, me, stmt, corr, full_statement, Oracle, BIG
import probes as P

UTC = dt.timezone.utc
def iso_ns(n, tz=UTC):
    return dt.datetime.fromtimestamp(n // 10**9, UTC).replace(microsecond=(n % 10**9) // 1000).astimezone(tz).isoformat()
def pm(n, d_us): return iso_ns(n + d_us * 1000)
def view(t, at, known=None):
    x = me(t, "?as_of=" + q(at) + ("&known_at=" + q(known) if known else ""))[1]
    return x["total"], x["held"], x["available"]


def logins(f): return {u["handle"]: P.login(u["handle"]) for u in f["users"]}


def into_second(lo=0.3):
    while time.time() % 1 < lo or time.time() % 1 > 0.9:
        time.sleep(0.01)


def s_combinations():
    """Mixed seeded created_at forms; no view may go negative and ordering must follow instants."""
    IST = dt.timezone(dt.timedelta(hours=5, minutes=30)); PST = dt.timezone(dt.timedelta(hours=-8))
    for trial in range(6):
        into_second()
        now = dt.datetime.now(UTC)
        forms = {
            "frac_100ms_before": (now - dt.timedelta(milliseconds=100)).isoformat(),
            "frac_ist": (now - dt.timedelta(milliseconds=50)).astimezone(IST).isoformat(),
            "whole_sec_floor": now.replace(microsecond=0).isoformat(),
            "pst_1h": (now - dt.timedelta(hours=1, microseconds=1)).astimezone(PST).isoformat(),
            "Z_frac": (now - dt.timedelta(milliseconds=10)).isoformat().replace("+00:00", "Z"),
        }
        # cy starts 0; receives money at each supplied instant; omitted payment + omitted hold at reset
        pays, bal = [], 0
        for i, (name, at) in enumerate(forms.items()):
            pays.append({"id": f"f{i}", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 100, "created_at": at})
        pays.append({"id": "fz", "from_user_id": "u_bob", "to_user_id": "u_cy", "amount": 1})               # reset time
        pays.append({"id": "fo", "from_user_id": "u_cy", "to_user_id": "u_dee", "amount": 501, "created_at": forms["frac_100ms_before"].replace("+00:00", "+00:00")} if False else
                    {"id": "fo", "from_user_id": "u_cy", "to_user_id": "u_dee", "amount": 1})                # spends at reset time
        f = {"currency": "EUR", "minor_units": 2, "payments": pays, "requests": [],
             "users": [user("ada", 10000), user("bob", 2500 - 501), user("cy", 500), user("dee", 701)],
             "authorizations": [{"id": "hz", "from_user_id": "u_cy", "to_user_id": "u_ada", "amount": 500, "status": "open", "expires_at": (now + dt.timedelta(hours=2)).isoformat()}]}
        r = call("POST", "/_test/reset", f)
        if r[0] != 204:
            check(f"trial {trial}: fixture accepted", False, r); continue
        T = logins(f)
        st = full_statement(T["cy"])
        reset_at = [e for e in st["entries"] if e["payment"]["payment_id"] == "fz"][0]["effective_at"]
        h = [a for a in call("GET", "/authorizations", token=T["cy"])[1]["authorizations"] if a["authorization_id"] == "hz"][0]
        insts = [ns(e["effective_at"]) for e in st["entries"]]
        ids = [e["payment"]["payment_id"] for e in st["entries"]]
        ok_order = all(insts[i] < insts[i + 1] or (insts[i] == insts[i + 1] and ids[i] < ids[i + 1]) for i in range(len(insts) - 1))
        sup_max = max(ns(v) for v in forms.values())
        check(f"trial {trial}: reset instant >= every supplied seeded instant; seeded hold at reset instant",
              ns(reset_at) >= sup_max and ns(h["created_at"]) == ns(reset_at), (reset_at, iso_ns(sup_max), h["created_at"]))
        check(f"trial {trial}: statement order by instant then id", ok_order, list(zip(ids, [e["effective_at"] for e in st["entries"]])))
        check(f"trial {trial}: supplied offsets preserved as instants", all(ns(e["effective_at"]) == ns(forms[name]) for e in st["entries"] for i, name in enumerate(forms) if e["payment"]["payment_id"] == f"f{i}"))
        neg = []
        for n in sorted(set(insts)) + [ns(reset_at)]:
            for d in (-1, 0, 1):
                v = view(T["cy"], pm(n, d))
                if v[2] < 0 or v[0] < 0: neg.append((pm(n, d), v))
        check(f"trial {trial}: no negative cy view at any seeded instant ±1µs", not neg, neg[:3])
        v_before, v_at = view(T["cy"], pm(ns(reset_at), -1)), view(T["cy"], reset_at)
        check(f"trial {trial}: reset−1µs excludes reset-time items, reset includes them", v_before == (500, 0, 500) and v_at == (500, 500, 0), (v_before, v_at))
        kv = view(T["cy"], reset_at, pm(ns(reset_at), -1))
        check(f"trial {trial}: known_at reset−1µs: reset-time payments/hold not yet known", kv == (500, 0, 500), kv)
        w = full_statement(T["cy"], f"?from={q(reset_at)}")
        check(f"trial {trial}: statement from=reset instant includes exactly the reset-time payments", sorted(e["payment"]["payment_id"] for e in w["entries"]) == ["fo", "fz"], [e["payment"]["payment_id"] for e in w["entries"]])
        w2 = full_statement(T["cy"], f"?to={q(reset_at)}")
        check(f"trial {trial}: statement to=reset instant excludes them; closing 500", "fz" not in [e["payment"]["payment_id"] for e in w2["entries"]] and w2["closing_balance"] == 500)
        r = corr(T["bob"], "f0", {"expected_revision": 1, "amount": 101, "effective_at": reset_at, "reason": "credit at reset instant"})
        check(f"trial {trial}: correction effective at the reset instant (credit) → 201", r[0] == 201, r[:2])
        r = corr(T["bob"], "f1", {"expected_revision": 1, "amount": 100, "effective_at": pm(ns(reset_at), 1), "reason": "move after reset"})
        # moving 100 of cy's 501 funding after the reset-time hold+spend makes cy negative at reset → overdraft
        check(f"trial {trial}: moving funding 1µs after the reset instant → historical_overdraft", err(r, 409, "historical_overdraft"), r[:2])
        api_p = P.pay(T, "ada", "bob", 1)[1]
        check(f"trial {trial}: API events strictly after the reset instant", ns(api_p["created_at"]) > ns(reset_at))
    # future and boundary
    f = {"currency": "EUR", "minor_units": 2, "users": [user("ada", 9999), user("bob", 1)], "payments": [
        {"id": "x", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 1, "created_at": (dt.datetime.now(UTC) + dt.timedelta(milliseconds=500)).isoformat()}], "requests": []}
    check("seeded created_at 500 ms in the future → 422", err(call("POST", "/_test/reset", f), 422, "validation_failed"))
    f["payments"][0]["created_at"] = (dt.datetime.now(UTC) + dt.timedelta(seconds=2)).astimezone(dt.timezone(dt.timedelta(hours=-8))).isoformat()
    check("seeded future in -08:00 → 422", err(call("POST", "/_test/reset", f), 422, "validation_failed"))
    f["payments"][0]["created_at"] = (dt.datetime.now(UTC) - dt.timedelta(microseconds=1)).isoformat()
    check("seeded 1µs-in-the-past fractional → 204", call("POST", "/_test/reset", f)[0] == 204)
    # seeded hold with supplied created_at fractional, expires_at fractional within the hour
    now = dt.datetime.now(UTC)
    f = {"currency": "EUR", "minor_units": 2, "users": [user("ada", 1000), user("bob", 0)], "payments": [], "requests": [],
         "authorizations": [{"id": "h1", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 400, "status": "open",
                             "created_at": (now - dt.timedelta(seconds=10, microseconds=123)).isoformat(), "expires_at": (now + dt.timedelta(hours=1, microseconds=7)).isoformat()},
                            {"id": "h2", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 300, "status": "voided", "created_at": (now - dt.timedelta(hours=2)).isoformat(),
                             "closed_at": (now - dt.timedelta(hours=1)).isoformat(), "expires_at": (now + dt.timedelta(hours=1)).isoformat()},
                            {"id": "h3", "from_user_id": "u_ada", "to_user_id": "u_bob", "amount": 900, "status": "open", "expires_at": (now - dt.timedelta(hours=1)).isoformat()}]}
    r = call("POST", "/_test/reset", f)
    check("seeded holds with fractional created/expires, closed with closed_at, past-due open → 204", r[0] == 204, r)
    T = logins(f)
    al = {a["authorization_id"]: a for a in call("GET", "/authorizations", token=T["ada"])[1]["authorizations"]}
    check("seeded fractional instants preserved", ns(al["h1"]["created_at"]) == ns(f["authorizations"][0]["created_at"]) and ns(al["h1"]["expires_at"]) == ns(f["authorizations"][0]["expires_at"]))
    check("seeded past-due open → expired with closed_at = expires_at; voided keeps closed_at", al["h3"]["status"] == "expired" and ns(al["h3"]["closed_at"]) == ns(al["h3"]["expires_at"])
          and al["h2"]["closed_at"] is not None and al["h1"]["closed_at"] is None, (al["h3"], al["h2"]["closed_at"]))
    c = ns(al["h1"]["created_at"])
    check("seeded hold supplied created_at: held from that instant (±1µs)", view(T["ada"], pm(c, -1))[1] == 0 and view(T["ada"], pm(c, 0))[1] == 400)
    e = ns(al["h1"]["expires_at"])
    check("future query at expires_at releases (±1µs)", view(T["ada"], pm(e, -1))[1] == 400 and view(T["ada"], pm(e, 0))[1] == 0)


def s_snapshot_same_us():
    """Snapshot default `to` vs events stamped right after the read."""
    f = S.basic_fixture()
    T = S.reset(f)
    toks, lock = [], threading.Lock()
    def rd(i):
        r = stmt(T["ada"], "?limit=200")[1]
        with lock: toks.append((r["snapshot"], json.dumps(r, sort_keys=True)))
    def wr(i): P.pay(T, "ada" if i % 2 else "bob", "bob" if i % 2 else "ada", 1)
    with ThreadPoolExecutor(40) as ex:
        list(ex.map(lambda i: rd(i) if i % 2 else wr(i), range(160)))
    same = all(json.dumps(stmt(T["ada"], f"?snapshot={t}&limit=200")[1], sort_keys=True) == body for t, body in toks)
    check(f"{len(toks)} snapshots taken amid writes all page identically afterwards", same)
    # each snapshot's closing balance equals /me?as_of just before its own read... check via sum rule
    ok = all((lambda b: b["opening_balance"] + sum(e["delta"] for e in b["entries"]) == b["closing_balance"])(json.loads(body)) for _, body in toks)
    check("every racing snapshot satisfies the sum rule", ok)


def s_reset_race():
    """reset/import racing in-flight requests: no 5xx, final state is exactly the last fixture."""
    f1 = S.basic_fixture(ops=("u_op",))
    T = S.reset(f1)
    sts, lock = [], threading.Lock()
    stop = threading.Event()
    def hammer():
        while not stop.is_set():
            try:
                t = random.choice(list(T.values()))
                r = random.choice([lambda: P.pay(T, "ada", "bob", 1), lambda: stmt(t, "?limit=5"), lambda: me(t, "?as_of=" + q(S.ago(0))),
                                   lambda: call("POST", "/authorizations", {"to_handle": "cy", "amount": 1}, token=T["ada"], key=k())])()
                with lock: sts.append(r[0])
            except Exception as e:
                with lock: sts.append(-1)
    ths = [threading.Thread(target=hammer) for _ in range(30)]
    for th in ths: th.start()
    ex = None
    for i in range(10):
        if i % 2:
            call("POST", "/_test/reset", f1)
            for h in list(T): T[h] = P.login(h)
        else:
            ex = ex or call("GET", "/_test/export")[1]
            call("POST", "/_test/import", ex)
        time.sleep(0.15)
    stop.set()
    for th in ths: th.join()
    check(f"reset/import races: no 5xx or transport errors over {len(sts)} requests", all(0 < s < 500 for s in sts), sorted(set(sts)))
    call("POST", "/_test/reset", f1)
    T = S.logins(f1) if hasattr(S, "logins") else {u["handle"]: P.login(u["handle"]) for u in f1["users"]}
    tot = sum(me(t)[1]["total"] for t in T.values())
    st = full_statement(T["ada"])
    check("after final reset: exactly the fixture (totals, no entries, no holds)", tot == 13200 and st["entries"] == [] and me(T["ada"])[1]["held"] == 0)


def s_random_oracle():
    """Random fractional seeded history + random API ops + corrections; compare every view with my oracle."""
    random.seed(17)
    for trial in range(3):
        into_second()
        now = dt.datetime.now(UTC)
        hs = ["a", "b", "c", "d"]
        bal = {h: 3000 for h in hs}
        pays = []
        times = sorted(now - dt.timedelta(microseconds=random.randint(1, 5_000_000)) for _ in range(10))
        for i, t in enumerate(times):
            x, y = random.sample(hs, 2)
            amt = random.randint(1, 200)
            pays.append({"id": f"r{trial}_{i:02d}", "from_user_id": "u_" + x, "to_user_id": "u_" + y, "amount": amt,
                         "created_at": t.astimezone(random.choice([UTC, dt.timezone(dt.timedelta(hours=2)), dt.timezone(dt.timedelta(hours=-7))])).isoformat()})
        opening = dict(bal)
        for p in pays:
            bal[p["from_user_id"][2:]] -= p["amount"]; bal[p["to_user_id"][2:]] += p["amount"]
        f = {"currency": "EUR", "minor_units": 2, "payments": pays, "requests": [], "users": [user(h, bal[h]) for h in hs]}
        r = call("POST", "/_test/reset", f)
        assert r[0] == 204, r
        T = {h: P.login(h) for h in hs}
        for _ in range(15):
            x, y = random.sample(hs, 2)
            P.pay(T, x, y, random.randint(1, 150))
        for _ in range(10):
            x = random.choice(hs)
            mine = [p for p in call("GET", "/activity?limit=200", token=T[x])[1]["payments"] if p["from_user_id"] == "u_" + x]
            if not mine: continue
            p = random.choice(mine)
            cur = S.revs(T[x], p["payment_id"])[1]["revisions"][-1]
            eff = iso_ns(random.randint(ns(times[0].isoformat()) - 10**9, ns(S.ago(0))) // 1000 * 1000)
            corr(T[x], p["payment_id"], {"expected_revision": cur["revision"], "amount": random.randint(0, 300), "effective_at": eff, "reason": "rnd"})
        O = Oracle({"u_" + h: opening[h] for h in hs}).load(T)
        bounds = O.boundaries(BIG)
        recs = sorted({r_[2] for _, _, rv in O.pays.values() for r_ in rv})
        mism, neg, sums = [], [], []
        for b in bounds[::2] + bounds[-1:]:
            for kk in (BIG, random.choice(recs)):
                tot = 0
                for h in hs:
                    want = O.bal("u_" + h, b, kk)
                    tot += want
                    got = me(T[h], "?as_of=" + q(iso_ns(b)) + ("" if kk == BIG else "&known_at=" + q(iso_ns(kk))))[1]["total"]
                    if got != want: mism.append((h, iso_ns(b), kk, got, want))
                    if want < 0: neg.append((h, iso_ns(b)))
                if tot != 12000: sums.append(tot)
        check(f"oracle trial {trial}: /me?as_of&known_at == oracle at boundaries ({len(bounds)})", not mism, mism[:3])
        check(f"oracle trial {trial}: no negative, sums conserved in every view", not neg and not sums, (neg[:2], sums[:2]))
        # statements windows at random instants equal oracle
        bad = []
        for _ in range(6):
            fa, ta = sorted(random.sample(bounds, 2)) if len(bounds) > 1 else (bounds[0], bounds[0])
            h = random.choice(hs)
            s_ = full_statement(T[h], f"?from={q(iso_ns(fa))}&to={q(iso_ns(ta))}")
            if s_["opening_balance"] != O.bal("u_" + h, fa - 1, BIG) or s_["closing_balance"] != O.bal("u_" + h, ta - 1, BIG):
                bad.append((h, iso_ns(fa), iso_ns(ta), s_["opening_balance"], O.bal("u_" + h, fa - 1, BIG), s_["closing_balance"], O.bal("u_" + h, ta - 1, BIG)))
        check(f"oracle trial {trial}: statement windows at exact event instants match oracle", not bad, bad[:2])


def main():
    for f in (s_combinations, s_snapshot_same_us, s_reset_race, s_random_oracle):
        print(f"== {f.__name__}", flush=True)
        try: f()
        except Exception as e:
            import traceback; traceback.print_exc(); check(f"{f.__name__} ran", False, repr(e)[:300])
    n = sum(1 for _, ok in P.RESULTS if ok)
    print(f"TOTAL {n}/{len(P.RESULTS)} passed, {len(P.RESULTS) - n} failed")


if __name__ == "__main__":
    main()
