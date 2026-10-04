// Property test: random histories (seeded payments with equal and distinct instants,
// API payments, settlements, holds with partial/final captures, voids and clock expiry)
// checked against a deliberately naive reference that recomputes every view from the raw
// event list (no caches, no indexes):
//
//   - GET /me?as_of=T&known_at=K equals the reference total/held/available for every user,
//     at random instants and at exact event boundaries (±1µs), past and future;
//   - the sum of all balances equals the seeded total in every view;
//   - GET /statement equals the reference entries, ordering, opening and closing balances
//     for random windows and knowledge instants; opening + Σ deltas = closing; paging with
//     random limit/offset returns the same full-window values;
//   - snapshots page exactly the original result after further writes.
package tests

import (
	"fmt"
	"math/rand"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"pocketful/app"
)

type refPayment struct {
	id       string
	from, to string
	amount   int64
	at       int64 // created_at = effective = recorded (no corrections in this item)
}

type refHold struct {
	id               string
	from             string
	amount           int64
	created, expires int64
	captures         []refPayment
	closedAt         int64 // void or final capture event time; 0 if not closed that way
}

type refModel struct {
	opening  map[string]int64
	payments []refPayment
	holds    []*refHold
}

func (m *refModel) total(u string, t, k int64) int64 {
	b := m.opening[u]
	for _, p := range m.payments {
		if p.at <= t && p.at <= k {
			if p.from == u {
				b -= p.amount
			}
			if p.to == u {
				b += p.amount
			}
		}
	}
	return b
}

func (m *refModel) held(u string, t, k int64) int64 {
	var h int64
	for _, a := range m.holds {
		if a.from != u || a.created > t || a.created > k || a.expires <= t {
			continue
		}
		if a.closedAt != 0 && a.closedAt <= t && a.closedAt <= k {
			continue
		}
		rem := a.amount
		for _, c := range a.captures {
			if c.at <= t && c.at <= k {
				rem -= c.amount
			}
		}
		h += rem
	}
	return h
}

type refEntry struct {
	id         string
	delta, bal int64
}

func (m *refModel) statement(u string, from *int64, to, k int64) (int64, []refEntry, int64) {
	open := m.opening[u]
	var in []refPayment
	for _, p := range m.payments {
		if (p.from != u && p.to != u) || p.at > k {
			continue
		}
		d := p.amount
		if p.from == u {
			d = -d
		}
		if from != nil && p.at < *from {
			open += d
		} else if p.at < to {
			in = append(in, p)
		}
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].at != in[j].at {
			return in[i].at < in[j].at
		}
		return in[i].id < in[j].id
	})
	bal := open
	var out []refEntry
	for _, p := range in {
		d := p.amount
		if p.from == u {
			d = -d
		}
		bal += d
		out = append(out, refEntry{p.id, d, bal})
	}
	return open, out, bal
}

func parseNs(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(s)
	}
	return t.UnixNano()
}

func fmtNs(ns int64) string { return time.Unix(0, ns).UTC().Format(time.RFC3339Nano) }

func TestOracleRandomHistories(t *testing.T) {
	seeds := []int64{1, 2, 3, 4, 5, 6}
	if testing.Short() {
		seeds = seeds[:2]
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) { oracleRun(t, seed) })
	}
}

func oracleRun(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	names := []string{"ada", "bob", "cy", "dee"}
	end := map[string]int64{"ada": 50000, "bob": 30000, "cy": 20000, "dee": 10000}
	m := &refModel{opening: map[string]int64{}}
	for _, n := range names {
		m.opening["u_"+n] = end[n]
	}
	// seeded history: some equal instants, some with offsets, some omitted (= reset time)
	base := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	var seedJSON []string
	var omitted []refPayment
	for i := 0; i < 12; i++ {
		from, to := names[rng.Intn(4)], names[rng.Intn(4)]
		if from == to {
			continue
		}
		amt := int64(1 + rng.Intn(800))
		id := fmt.Sprintf("p_seed%02d", i)
		p := refPayment{id: id, from: "u_" + from, to: "u_" + to, amount: amt}
		m.opening["u_"+from] += amt // opening = ending balance minus the net of seeded payments
		m.opening["u_"+to] -= amt
		if i%5 == 4 {
			seedJSON = append(seedJSON, fmt.Sprintf(`{"id":%q,"from_user_id":"u_%s","to_user_id":"u_%s","amount":%d}`, id, from, to, amt))
			omitted = append(omitted, p)
			continue
		}
		at := base.Add(time.Duration(rng.Intn(6)) * time.Hour) // few distinct instants -> ties
		zone := time.FixedZone("x", 3600*(rng.Intn(5)-2))
		p.at = at.UnixNano()
		seedJSON = append(seedJSON, fmt.Sprintf(`{"id":%q,"from_user_id":"u_%s","to_user_id":"u_%s","amount":%d,"created_at":%q}`,
			id, from, to, amt, at.In(zone).Format(time.RFC3339)))
		m.payments = append(m.payments, p)
	}
	var users []string
	for _, n := range names {
		users = append(users, user(n, end[n]))
	}
	// pin the reset >= 300 ms into a second, then seed two payments with fractional
	// created_at just before it (inside the same second) and an open hold without created_at
	for time.Now().Nanosecond() < 300_000_000 || time.Now().Nanosecond() > 850_000_000 {
		time.Sleep(5 * time.Millisecond)
	}
	var latestSupplied int64
	for _, p := range m.payments {
		if p.at > latestSupplied {
			latestSupplied = p.at
		}
	}
	for j := 0; j < 2; j++ {
		at := time.Now().Add(-time.Duration(20+rng.Intn(200)) * time.Millisecond).UTC().Truncate(time.Microsecond)
		id := fmt.Sprintf("p_frac%d", j)
		m.opening["u_ada"] += 100
		m.opening["u_cy"] -= 100
		m.payments = append(m.payments, refPayment{id: id, from: "u_ada", to: "u_cy", amount: 100, at: at.UnixNano()})
		seedJSON = append(seedJSON, fmt.Sprintf(`{"id":%q,"from_user_id":"u_ada","to_user_id":"u_cy","amount":100,"created_at":%q}`, id, at.Format("2006-01-02T15:04:05.000000Z07:00")))
		if at.UnixNano() > latestSupplied {
			latestSupplied = at.UnixNano()
		}
	}
	seedHold := fmt.Sprintf(`{"id":"a_seed","from_user_id":"u_cy","to_user_id":"u_ada","amount":%d,"status":"open","expires_at":%q}`,
		end["cy"]/2, time.Now().Add(3*time.Hour).UTC().Format(time.RFC3339))
	f := fmt.Sprintf(`{"currency":"EUR","minor_units":2,"authorization_ttl_seconds":1,"settlement_operator_ids":["u_dee"],"users":[%s],"payments":[%s],"authorizations":[%s]}`,
		joinStr(users), joinStr(seedJSON), seedHold)
	resetStart := time.Now().UnixNano()
	resetNs := resetStart
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	tok := map[string]string{}
	for _, n := range names {
		tok["u_"+n] = login(t, srv, n+"@example.com", "correct horse")
	}
	// the seeded hold starts at the reset instant, at or after every supplied seeded created_at
	sh := wantStatus(t, do(t, srv, "GET", "/authorizations", tok["u_cy"], ""), 200).body["authorizations"].([]any)[0].(map[string]any)
	sHold := &refHold{id: "a_seed", from: "u_cy", amount: num(sh["amount"]), created: parseNs(sh["created_at"].(string)), expires: parseNs(sh["expires_at"].(string))}
	if sHold.created < latestSupplied {
		t.Fatalf("seeded hold created %s before a supplied seeded created_at %s", fmtNs(sHold.created), fmtNs(latestSupplied))
	}
	m.holds = append(m.holds, sHold)
	// the reset time of omitted seeded payments, read back from the revisions endpoint
	for _, p := range omitted {
		r := wantStatus(t, do(t, srv, "GET", "/payments/"+p.id+"/revisions", tok[p.from], ""), 200)
		p.at = parseNs(r.body["revisions"].([]any)[0].(map[string]any)["effective_at"].(string))
		if p.at < resetStart || p.at != sHold.created {
			t.Fatalf("omitted created_at %s is not the reset instant %s (reset began %s)", fmtNs(p.at), fmtNs(sHold.created), fmtNs(resetStart))
		}
		m.payments = append(m.payments, p)
	}
	var instants []int64
	for _, p := range m.payments {
		instants = append(instants, p.at)
	}
	var lastEvent int64
	event := func(ns int64, what string) {
		if ns <= lastEvent {
			t.Fatalf("event clock went backwards at %s: %s after %s", what, fmtNs(ns), fmtNs(lastEvent))
		}
		lastEvent = ns
	}
	addPay := func(body map[string]any) {
		if body["settlement_id"] == nil { // settlement members share committed_at (checked once)
			event(parseNs(body["created_at"].(string)), "payment")
		}
		p := refPayment{id: body["payment_id"].(string), from: body["from_user_id"].(string), to: body["to_user_id"].(string),
			amount: num(body["amount"]), at: parseNs(body["created_at"].(string))}
		m.payments = append(m.payments, p)
		instants = append(instants, p.at)
	}
	holdByID := map[string]*refHold{}
	holdTo := map[string]string{} // hold id -> receiver
	var openHolds []*refHold
	var snaps []struct {
		user, token string
		body        resp
	}
	ops := 60
	for i := 0; i < ops; i++ {
		u := "u_" + names[rng.Intn(4)]
		other := "u_" + names[rng.Intn(4)]
		handle := other[2:]
		switch rng.Intn(8) {
		case 0, 1:
			if u == other {
				continue
			}
			r := post(t, srv, "/payments", tok[u], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, handle, 1+rng.Intn(3000)))
			if r.status == 201 {
				addPay(r.body)
			}
		case 2:
			a, b := "u_"+names[rng.Intn(4)], "u_"+names[rng.Intn(4)]
			if a == b || u == other {
				continue
			}
			r := post(t, srv, "/settlements", tok["u_dee"], newKey(), fmt.Sprintf(`{"transfers":[{"from_handle":%q,"to_handle":%q,"amount":%d},{"from_handle":%q,"to_handle":%q,"amount":%d}]}`,
				u[2:], handle, 1+rng.Intn(500), a[2:], b[2:], 1+rng.Intn(500)))
			if r.status == 201 {
				event(parseNs(r.body["committed_at"].(string)), "settlement")
				for _, p := range r.body["payments"].([]any) {
					addPay(p.(map[string]any))
				}
			}
		case 3:
			if u == other {
				continue
			}
			r := post(t, srv, "/authorizations", tok[u], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, handle, 1+rng.Intn(2000)))
			if r.status == 201 {
				h := &refHold{id: r.body["authorization_id"].(string), from: u, amount: num(r.body["amount"]),
					created: parseNs(r.body["created_at"].(string)), expires: parseNs(r.body["expires_at"].(string))}
				event(h.created, "authorization")
				holdByID[h.id] = h
				m.holds = append(m.holds, h)
				openHolds = append(openHolds, h)
				instants = append(instants, h.created, h.expires)
				_ = other
				holdTo[h.id] = other
			}
		case 4:
			if len(openHolds) == 0 {
				continue
			}
			h := openHolds[rng.Intn(len(openHolds))]
			final := rng.Intn(2) == 0
			r := post(t, srv, "/authorizations/"+h.id+"/capture", tok[holdTo[h.id]], newKey(), fmt.Sprintf(`{"amount":%d,"final":%v}`, 1+rng.Intn(300), final))
			if r.status == 201 {
				event(parseNs(r.body["created_at"].(string)), "capture")
				c := refPayment{id: r.body["payment_id"].(string), from: r.body["from_user_id"].(string), to: r.body["to_user_id"].(string),
					amount: num(r.body["amount"]), at: parseNs(r.body["created_at"].(string))}
				m.payments = append(m.payments, c)
				h.captures = append(h.captures, c)
				instants = append(instants, c.at)
				var rem int64 = h.amount
				for _, x := range h.captures {
					rem -= x.amount
				}
				if final || rem == 0 {
					h.closedAt = c.at
				}
			}
		case 5:
			if len(openHolds) == 0 {
				continue
			}
			h := openHolds[rng.Intn(len(openHolds))]
			r := post(t, srv, "/authorizations/"+h.id+"/void", tok[h.from], "", "")
			if r.status == 200 && h.closedAt == 0 {
				h.closedAt = parseNs(r.body["closed_at"].(string))
				event(h.closedAt, "void")
				instants = append(instants, h.closedAt)
			}
		case 7:
			if u == other {
				continue
			}
			amt := 1 + rng.Intn(2000)
			r := post(t, srv, "/payments", tok[other], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, u[2:], amt))
			if r.status != 201 {
				continue
			}
			addPay(r.body)
			// hold everything available, so the hold depends on the money that just arrived
			av := num(wantStatus(t, do(t, srv, "GET", "/me", tok[u], ""), 200).body["available"])
			if av < 1 || av > 1000000000 {
				continue
			}
			r = post(t, srv, "/authorizations", tok[u], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, other[2:], av))
			if r.status == 201 {
				h := &refHold{id: r.body["authorization_id"].(string), from: u, amount: num(r.body["amount"]),
					created: parseNs(r.body["created_at"].(string)), expires: parseNs(r.body["expires_at"].(string))}
				event(h.created, "authorization after funding")
				m.holds = append(m.holds, h)
				openHolds = append(openHolds, h)
				instants = append(instants, h.created, h.expires)
				holdTo[h.id] = other
			}
		case 6:
			r := wantStatus(t, do(t, srv, "GET", "/statement?limit=3", tok[u], ""), 200)
			snaps = append(snaps, struct {
				user, token string
				body        resp
			}{u, r.body["snapshot"].(string), r})
		}
		if i == ops/2 {
			time.Sleep(1100 * time.Millisecond) // let ttl=1 holds expire without traffic
		}
	}
	now := time.Now().UnixNano()
	// query instants: every event ±1µs, far past, now, far future
	cands := []int64{0, now, now + int64(time.Hour), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()}
	for _, x := range instants {
		cands = append(cands, x, x-1000, x+1000)
	}
	pick := func() int64 { return cands[rng.Intn(len(cands))] }
	var seededTotal int64
	for _, n := range names {
		seededTotal += end[n]
	}
	checks := 0
	for q := 0; q < 120; q++ {
		T, K := pick(), pick()
		if T == 0 {
			T = now
		}
		if K == 0 {
			K = now + int64(time.Hour)
		}
		var sum int64
		for _, n := range names {
			u := "u_" + n
			r := wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", fmtNs(T), "known_at", fmtNs(K)), tok[u], ""), 200)
			wt, wh := m.total(u, T, K), m.held(u, T, K)
			if num(r.body["total"]) != wt || num(r.body["balance"]) != wt || num(r.body["held"]) != wh || num(r.body["available"]) != wt-wh {
				t.Fatalf("seed run: %s at T=%s K=%s: got %s, want total %d held %d", n, fmtNs(T), fmtNs(K), r.raw, wt, wh)
			}
			sum += wt
			checks++
			if T >= resetNs && (wt < 0 || wt-wh < 0) {
				t.Fatalf("negative view for %s at T=%s K=%s: total %d held %d", n, fmtNs(T), fmtNs(K), wt, wh)
			}
		}
		if sum != seededTotal {
			t.Fatalf("sum %d != seeded %d at T=%s K=%s", sum, seededTotal, fmtNs(T), fmtNs(K))
		}
		// a statement over a random window
		u := "u_" + names[rng.Intn(4)]
		var from *int64
		args := []string{"known_at", fmtNs(K)}
		to := pick()
		if to == 0 {
			to = now + int64(time.Hour)
		}
		args = append(args, "to", fmtNs(to))
		if rng.Intn(3) > 0 {
			fr := pick()
			if fr > to {
				fr, to = to, fr
				args[3] = fmtNs(to)
			}
			from = &fr
			args = append(args, "from", fmtNs(fr))
		}
		wo, we, wc := m.statement(u, from, to, K)
		full := wantStatus(t, do(t, srv, "GET", "/statement"+qs(append(args, "limit", "200")...), tok[u], ""), 200)
		got := []refEntry{}
		for _, e := range full.body["entries"].([]any) {
			e := e.(map[string]any)
			got = append(got, refEntry{e["payment"].(map[string]any)["payment_id"].(string), num(e["delta"]), num(e["balance_after"])})
		}
		if we == nil {
			we = []refEntry{}
		}
		if num(full.body["opening_balance"]) != wo || num(full.body["closing_balance"]) != wc || !reflect.DeepEqual(got, we) {
			t.Fatalf("statement %v\n got  open %v close %v %v\n want open %d close %d %v", args, full.body["opening_balance"], full.body["closing_balance"], got, wo, wc, we)
		}
		var s int64 = wo
		for _, e := range we {
			s += e.delta
		}
		if s != wc {
			t.Fatal("reference arithmetic broken")
		}
		// paging invariance
		limit, offset := 1+rng.Intn(4), rng.Intn(len(we)+2)
		pg := wantStatus(t, do(t, srv, "GET", "/statement"+qs(append(args, "limit", fmt.Sprint(limit), "offset", fmt.Sprint(offset))...), tok[u], ""), 200)
		if num(pg.body["opening_balance"]) != wo || num(pg.body["closing_balance"]) != wc || pg.body["has_more"] != (offset+limit < len(we)) {
			t.Fatalf("paging changed the window: %s", pg.raw)
		}
		for i, e := range pg.body["entries"].([]any) {
			if num(e.(map[string]any)["balance_after"]) != we[offset+i].bal {
				t.Fatal("balance_after changed by paging")
			}
		}
		checks++
	}
	// snapshots taken during the run page exactly their original first page
	for _, sn := range snaps {
		r := wantStatus(t, do(t, srv, "GET", "/statement?snapshot="+sn.token+"&limit=3", tok[sn.user], ""), 200)
		if !reflect.DeepEqual(r.body["entries"], sn.body.body["entries"]) || r.body["opening_balance"] != sn.body.body["opening_balance"] ||
			r.body["closing_balance"] != sn.body.body["closing_balance"] || r.body["has_more"] != sn.body.body["has_more"] {
			t.Fatalf("snapshot changed after later writes:\n%s\n%s", sn.body.raw, r.raw)
		}
		checks++
	}
	t.Logf("seed %d: %d payments, %d holds, %d snapshots, %d checks", seed, len(m.payments), len(m.holds), len(snaps), checks)
}

func joinStr(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
