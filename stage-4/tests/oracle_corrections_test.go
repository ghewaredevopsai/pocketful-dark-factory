// Property test with corrections: random histories mixing seeded payments (shared
// instants), API payments, settlements (same-instant pairs), holds, captures, voids and
// corrections (valid, stale, linked, unaffordable, historical-overdraft candidates,
// effective_at at/around other events, far past, zero amounts, repeats, replays).
//
// A naive reference ledger decides every correction's outcome independently — 422 linked,
// 409 stale_revision, 409 insufficient_funds (current debit vs available), 409
// historical_overdraft (recomputing total and available from scratch at every boundary of
// both parties, all movements at an instant combined) or 201 — and the service must agree.
// After every step all users' /me views at random (as_of, known_at) and a statement over a
// random window are compared with the reference, and the sum of balances is checked.
package tests

import (
	"fmt"
	"math"
	"math/rand"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"pocketful/app"
)

type lRev struct{ amount, eff, rec int64 }

type lPay struct {
	id, from, to string
	linked       bool   // settlement member, capture or refund: not correctable singly
	refundOf     string // stage 4: the refunded payment ("" otherwise)
	settle       string // settlement id ("" otherwise)
	batchImm     bool   // capture or refund: not correctable in a batch either
	revs         []lRev
}

// refunded is the naive sum of every refund of p.
func (l *ledger) refunded(p *lPay) int64 {
	var n int64
	for _, q := range l.pays {
		if q.refundOf == p.id {
			n += q.revs[0].amount
		}
	}
	return n
}

// decideRefund returns the expected (status, code) of a refund by p's receiver.
func (l *ledger) decideRefund(p *lPay, amount, now int64) (int, string) {
	if p.refundOf != "" {
		return 422, "invalid_refund_target"
	}
	if l.refunded(p)+amount > p.revs[len(p.revs)-1].amount {
		return 422, "refund_exceeds_payment"
	}
	if l.total(p.to, math.MaxInt64, math.MaxInt64)-l.held(p.to, now, math.MaxInt64) < amount {
		return 409, "insufficient_funds"
	}
	return 201, ""
}

type ledger struct {
	opening map[string]int64
	pays    []*lPay
	byID    map[string]*lPay
	holds   []*refHold
}

func (l *ledger) sel(p *lPay, k int64) *lRev {
	var s *lRev
	for i := range p.revs {
		if p.revs[i].rec <= k {
			s = &p.revs[i]
		}
	}
	return s
}

func (l *ledger) total(u string, t, k int64) int64 {
	b := l.opening[u]
	for _, p := range l.pays {
		if r := l.sel(p, k); r != nil && r.eff <= t {
			if p.from == u {
				b -= r.amount
			}
			if p.to == u {
				b += r.amount
			}
		}
	}
	return b
}

func (l *ledger) held(u string, t, k int64) int64 {
	return (&refModel{holds: l.holds}).held(u, t, k)
}

// historyOK: naive — every boundary instant, total and available recomputed from scratch.
func (l *ledger) historyOK(u string) bool {
	const all = math.MaxInt64
	var bounds []int64
	for _, p := range l.pays {
		if p.from == u || p.to == u {
			bounds = append(bounds, p.revs[len(p.revs)-1].eff)
		}
	}
	for _, h := range l.holds {
		if h.from == u {
			bounds = append(bounds, h.created, h.expires)
			if h.closedAt != 0 {
				bounds = append(bounds, h.closedAt)
			}
			for _, c := range h.captures {
				bounds = append(bounds, c.at)
			}
		}
	}
	for _, b := range bounds {
		tot := l.total(u, b, all)
		if tot < 0 || tot-l.held(u, b, all) < 0 {
			return false
		}
	}
	return true
}

// decide returns the expected (status, code) of a correction by the payment's sender.
func (l *ledger) decide(p *lPay, expected int, amount, eff, now int64) (int, string) {
	if p.linked {
		return 422, "linked_payment_immutable"
	}
	if expected != len(p.revs) {
		return 409, "stale_revision"
	}
	if amount < l.refunded(p) {
		return 422, "refund_exceeds_payment"
	}
	prev := p.revs[len(p.revs)-1].amount
	diff := amount - prev
	debited, d := p.from, diff
	if diff < 0 {
		debited, d = p.to, -diff
	}
	if l.total(debited, math.MaxInt64, math.MaxInt64)-l.held(debited, now, math.MaxInt64) < d {
		return 409, "insufficient_funds"
	}
	p.revs = append(p.revs, lRev{amount, eff, math.MaxInt64 - 1})
	ok := l.historyOK(p.from) && l.historyOK(p.to)
	p.revs = p.revs[:len(p.revs)-1]
	if !ok {
		return 409, "historical_overdraft"
	}
	return 201, ""
}

type refItem struct {
	p      *lPay // nil = unknown payment
	exp    int
	amount int64
	eff    int64
}

// decideBatch: the naive reference for POST /correction-batches (shape is generated valid).
func (l *ledger) decideBatch(items []refItem, now int64) (int, string) {
	for _, it := range items { // item errors in input order
		if it.p == nil {
			return 404, "not_found"
		}
		if it.p.batchImm {
			return 422, "linked_payment_immutable"
		}
		if it.exp != len(it.p.revs) {
			return 409, "stale_revision"
		}
		if it.amount < l.refunded(it.p) {
			return 422, "refund_exceeds_payment"
		}
	}
	var touched []string
	in := map[string]bool{}
	for _, it := range items {
		in[it.p.id] = true
		if it.p.settle != "" && !contains2(touched, it.p.settle) {
			touched = append(touched, it.p.settle)
		}
	}
	for _, sid := range touched {
		for _, q := range l.pays {
			if q.settle == sid && !in[q.id] {
				return 422, "incomplete_settlement"
			}
		}
	}
	for _, sid := range touched {
		var eff *int64
		for i, it := range items {
			if it.p.settle == sid {
				if eff == nil {
					eff = &items[i].eff
				} else if *eff != it.eff {
					return 422, "validation_failed"
				}
			}
		}
	}
	net := map[string]int64{}
	for _, it := range items {
		d := it.amount - it.p.revs[len(it.p.revs)-1].amount
		net[it.p.from] -= d
		net[it.p.to] += d
	}
	for u, d := range net {
		if d < 0 && l.total(u, math.MaxInt64, math.MaxInt64)-l.held(u, now, math.MaxInt64) < -d {
			return 409, "insufficient_funds"
		}
	}
	for _, it := range items {
		it.p.revs = append(it.p.revs, lRev{it.amount, it.eff, math.MaxInt64 - 1})
	}
	ok := true
	for u := range net {
		if !l.historyOK(u) {
			ok = false
		}
	}
	for _, it := range items {
		it.p.revs = it.p.revs[:len(it.p.revs)-1]
	}
	if !ok {
		return 409, "historical_overdraft"
	}
	return 201, ""
}

func contains2(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (l *ledger) statement(u string, from *int64, to, k int64) (int64, []refEntry, int64) {
	open := l.opening[u]
	type ent struct {
		id  string
		eff int64
		d   int64
	}
	var in []ent
	for _, p := range l.pays {
		if p.from != u && p.to != u {
			continue
		}
		r := l.sel(p, k)
		if r == nil {
			continue
		}
		d := r.amount
		if p.from == u {
			d = -d
		}
		if from != nil && r.eff < *from {
			open += d
		} else if r.eff < to {
			in = append(in, ent{p.id, r.eff, d})
		}
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].eff != in[j].eff {
			return in[i].eff < in[j].eff
		}
		return in[i].id < in[j].id
	})
	bal := open
	out := []refEntry{}
	for _, e := range in {
		bal += e.d
		out = append(out, refEntry{e.id, e.d, bal})
	}
	return open, out, bal
}

func TestOracleWithCorrections(t *testing.T) {
	seeds := []int64{11, 12, 14, 15, 16, 17, 18, 19}
	if testing.Short() {
		seeds = seeds[:2]
	}
	outcomes := map[string]int{}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) { correctionsRun(t, seed, outcomes) })
	}
	t.Logf("correction outcomes across seeds: %v", outcomes)
	for _, want := range []string{"201", "stale_revision", "insufficient_funds", "historical_overdraft", "linked_payment_immutable", "replay",
		"refund", "refund:refund_exceeds_payment", "refund:invalid_refund_target", "refund_exceeds_payment",
		"batch", "batch:stale_revision", "batch:incomplete_settlement", "batch:linked_payment_immutable"} {
		if outcomes[want] == 0 {
			t.Errorf("the random runs never produced %s", want)
		}
	}
}

func correctionsRun(t *testing.T, seed int64, outcomes map[string]int) {
	rng := rand.New(rand.NewSource(seed))
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	names := []string{"ada", "bob", "cy", "dee"}
	end := map[string]int64{"ada": 4000, "bob": 3000, "cy": 2000, "dee": 1000}
	l := &ledger{opening: map[string]int64{}, byID: map[string]*lPay{}}
	for _, n := range names {
		l.opening["u_"+n] = end[n]
	}
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	var seedJSON []string
	var instants []int64
	for i := 0; i < 10; i++ {
		from, to := names[rng.Intn(4)], names[rng.Intn(4)]
		if from == to {
			continue
		}
		amt := int64(1 + rng.Intn(600))
		at := base.Add(time.Duration(rng.Intn(4)) * 6 * time.Hour)
		id := fmt.Sprintf("p_s%02d", i)
		l.opening["u_"+from] += amt
		l.opening["u_"+to] -= amt
		p := &lPay{id: id, from: "u_" + from, to: "u_" + to, revs: []lRev{{amt, at.UnixNano(), at.UnixNano()}}}
		l.pays = append(l.pays, p)
		l.byID[id] = p
		instants = append(instants, at.UnixNano())
		seedJSON = append(seedJSON, fmt.Sprintf(`{"id":%q,"from_user_id":"u_%s","to_user_id":"u_%s","amount":%d,"created_at":%q}`, id, from, to, amt, at.Format(time.RFC3339)))
	}
	// seeded history must be nonnegative (the spec promises it); skip seeds that are not
	for _, n := range names {
		if !l.historyOK("u_"+n) || l.opening["u_"+n] < 0 {
			t.Skipf("seed %d produced an inconsistent seeded history", seed)
		}
	}
	var users []string
	for _, n := range names {
		users = append(users, user(n, end[n]))
	}
	f := fmt.Sprintf(`{"currency":"EUR","minor_units":2,"authorization_ttl_seconds":600,"settlement_operator_ids":["u_dee"],"users":[%s],"payments":[%s]}`,
		joinStr(users), joinStr(seedJSON))
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	tok := map[string]string{}
	for _, n := range names {
		tok["u_"+n] = login(t, srv, n+"@example.com", "correct horse")
	}
	addPay := func(body map[string]any, linked bool) *lPay {
		at := parseNs(body["created_at"].(string))
		p := &lPay{id: body["payment_id"].(string), from: body["from_user_id"].(string), to: body["to_user_id"].(string), linked: linked,
			revs: []lRev{{num(body["amount"]), at, at}}}
		if sid, ok := body["settlement_id"].(string); ok {
			p.settle = sid
		}
		p.batchImm = body["authorization_id"] != nil || body["refund_of"] != nil
		l.pays = append(l.pays, p)
		l.byID[p.id] = p
		instants = append(instants, at)
		return p
	}
	holdTo := map[string]string{}
	var open []*refHold
	var last struct {
		pid, key, body, raw string
	}
	compare := func(step int) {
		now := time.Now().UnixNano()
		cands := append([]int64{0, now, now + int64(time.Hour), base.Add(-time.Hour).UnixNano()}, instants...)
		T, K := cands[rng.Intn(len(cands))], cands[rng.Intn(len(cands))]
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
			wt, wh := l.total(u, T, K), l.held(u, T, K)
			if num(r.body["total"]) != wt || num(r.body["held"]) != wh || num(r.body["available"]) != wt-wh {
				t.Fatalf("step %d %s T=%s K=%s: got %s want total %d held %d", step, n, fmtNs(T), fmtNs(K), r.raw, wt, wh)
			}
			sum += wt
			if wt < 0 || wt-wh < 0 { // seeded history is consistent here, so no view may be negative
				t.Fatalf("step %d negative view for %s at T=%s K=%s: total %d held %d", step, n, fmtNs(T), fmtNs(K), wt, wh)
			}
			cur := wantStatus(t, do(t, srv, "GET", "/me", tok[u], ""), 200)
			if num(cur.body["total"]) != l.total(u, math.MaxInt64, math.MaxInt64) {
				t.Fatalf("step %d %s current total %s", step, n, cur.raw)
			}
		}
		var seeded int64
		for _, n := range names {
			seeded += end[n]
		}
		if sum != seeded {
			t.Fatalf("step %d: sum %d != %d", step, sum, seeded)
		}
		u := "u_" + names[rng.Intn(4)]
		to := cands[rng.Intn(len(cands))]
		if to == 0 {
			to = now + int64(time.Hour)
		}
		args := []string{"known_at", fmtNs(K), "to", fmtNs(to), "limit", "200"}
		var from *int64
		if rng.Intn(2) == 0 {
			fr := cands[rng.Intn(len(cands))]
			if fr <= to {
				from = &fr
				args = append(args, "from", fmtNs(fr))
			}
		}
		wo, we, wc := l.statement(u, from, to, K)
		s := wantStatus(t, do(t, srv, "GET", "/statement"+qs(args...), tok[u], ""), 200)
		got := []refEntry{}
		for _, e := range s.body["entries"].([]any) {
			e := e.(map[string]any)
			got = append(got, refEntry{e["payment"].(map[string]any)["payment_id"].(string), num(e["delta"]), num(e["balance_after"])})
		}
		if num(s.body["opening_balance"]) != wo || num(s.body["closing_balance"]) != wc || !reflect.DeepEqual(got, we) {
			t.Fatalf("step %d statement %v:\n got %v %v %v\nwant %d %v %d", step, args, s.body["opening_balance"], got, s.body["closing_balance"], wo, we, wc)
		}
	}
	for step := 0; step < 90; step++ {
		u := "u_" + names[rng.Intn(4)]
		other := "u_" + names[rng.Intn(4)]
		switch op := rng.Intn(14); {
		case op >= 12: // a correction batch by the operator (dee)
			var items []refItem
			used := map[string]bool{}
			add := func(p *lPay, eff int64) {
				if used[p.id] || len(items) >= 32 {
					return
				}
				used[p.id] = true
				exp := len(p.revs)
				if rng.Intn(10) == 0 {
					exp = 1 + rng.Intn(len(p.revs)+1)
				}
				cur := p.revs[len(p.revs)-1].amount
				a := []int64{0, cur, cur + int64(rng.Intn(300)), cur - int64(rng.Intn(300)), int64(rng.Intn(1500))}[rng.Intn(5)]
				if a < 0 {
					a = 0
				}
				items = append(items, refItem{p, exp, a, eff})
			}
			pickEff := func() int64 {
				now := time.Now().Add(-time.Millisecond).UnixNano()
				e := []int64{instants[rng.Intn(len(instants))], instants[rng.Intn(len(instants))] + 1000, base.Add(-time.Hour).UnixNano(), now}[rng.Intn(4)]
				if e > now {
					e = now
				}
				return e
			}
			for n := 1 + rng.Intn(4); n > 0; n-- {
				p := l.pays[rng.Intn(len(l.pays))]
				eff := pickEff()
				if p.settle != "" && rng.Intn(4) > 0 { // usually the complete settlement, at one instant
					for _, q := range l.pays {
						if q.settle == p.settle {
							e := eff
							if rng.Intn(8) == 0 {
								e = pickEff() // sometimes a differing instant
							}
							add(q, e)
						}
					}
					continue
				}
				add(p, eff)
			}
			var parts []string
			for _, it := range items {
				parts = append(parts, fmt.Sprintf(`{"payment_id":%q,"expected_revision":%d,"amount":%d,"effective_at":%q,"reason":"b"}`, it.p.id, it.exp, it.amount, fmtNs(it.eff)))
			}
			if rng.Intn(15) == 0 { // an unknown payment somewhere in the batch
				parts = append(parts, fmt.Sprintf(`{"payment_id":"p_nope","expected_revision":1,"amount":1,"effective_at":%q,"reason":"b"}`, fmtNs(base.UnixNano())))
				items = append(items, refItem{nil, 1, 1, base.UnixNano()})
			}
			wantSt, wantCode := l.decideBatch(items, time.Now().UnixNano())
			r := post(t, srv, "/correction-batches", tok["u_dee"], newKey(), `{"corrections":[`+joinStr(parts)+`]}`)
			code := ""
			if e, ok := r.body["error"].(map[string]any); ok {
				code, _ = e["code"].(string)
			}
			if r.status != wantSt || code != wantCode {
				t.Fatalf("step %d batch of %d: got %d %s, reference %d %s", step, len(items), r.status, r.raw, wantSt, wantCode)
			}
			if r.status == 201 {
				rec := parseNs(r.body["recorded_at"].(string))
				for _, it := range items {
					if prev := it.p.revs[len(it.p.revs)-1].rec; rec <= prev {
						t.Fatalf("shared recorded_at %s not after %s", fmtNs(rec), fmtNs(prev))
					}
					it.p.revs = append(it.p.revs, lRev{it.amount, it.eff, rec})
					instants = append(instants, it.eff)
				}
				instants = append(instants, rec)
				outcomes["batch"]++
			} else {
				outcomes["batch:"+wantCode]++
			}
		case op >= 10: // refunds by the receiver, of any kind of target
			p := l.pays[rng.Intn(len(l.pays))]
			cur := p.revs[len(p.revs)-1].amount
			var a int64
			switch rng.Intn(3) {
			case 0:
				a = 1 + int64(rng.Intn(int(cur)+1)) // within the current amount (maybe over with earlier refunds)
			case 1:
				a = cur - l.refunded(p) + 1 // exactly one over the remaining refundable amount
				if a < 1 {
					a = 1
				}
			default:
				a = 1 + int64(rng.Intn(2000))
			}
			wantSt, wantCode := l.decideRefund(p, a, time.Now().UnixNano())
			r := refund(t, srv, tok[p.to], p.id, newKey(), fmt.Sprintf(`{"amount":%d}`, a))
			code := ""
			if e, ok := r.body["error"].(map[string]any); ok {
				code, _ = e["code"].(string)
			}
			if r.status != wantSt || code != wantCode {
				t.Fatalf("step %d refund of %s (%d, refunded %d of %d): got %d %s, reference %d %s", step, p.id, a, l.refunded(p), cur, r.status, r.raw, wantSt, wantCode)
			}
			if r.status == 201 {
				q := addPay(r.body, true)
				q.refundOf = p.id
				outcomes["refund"]++
			} else {
				outcomes["refund:"+wantCode]++
			}
		case op < 2:
			if u == other {
				continue
			}
			r := post(t, srv, "/payments", tok[u], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, other[2:], 1+rng.Intn(1500)))
			if r.status == 201 {
				addPay(r.body, false)
			}
		case op == 2:
			a, b, c := names[rng.Intn(4)], names[rng.Intn(4)], names[rng.Intn(4)]
			if a == b || b == c {
				continue
			}
			r := post(t, srv, "/settlements", tok["u_dee"], newKey(), fmt.Sprintf(`{"transfers":[{"from_handle":%q,"to_handle":%q,"amount":%d},{"from_handle":%q,"to_handle":%q,"amount":%d}]}`,
				a, b, 1+rng.Intn(400), b, c, 1+rng.Intn(400)))
			if r.status == 201 {
				for _, p := range r.body["payments"].([]any) {
					addPay(p.(map[string]any), true)
				}
			}
		case op == 3:
			if u == other {
				continue
			}
			r := post(t, srv, "/authorizations", tok[u], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, other[2:], 1+rng.Intn(1500)))
			if r.status == 201 {
				h := &refHold{id: r.body["authorization_id"].(string), from: u, amount: num(r.body["amount"]),
					created: parseNs(r.body["created_at"].(string)), expires: parseNs(r.body["expires_at"].(string))}
				l.holds = append(l.holds, h)
				open = append(open, h)
				holdTo[h.id] = other
				instants = append(instants, h.created)
			}
		case op == 4:
			if len(open) == 0 {
				continue
			}
			h := open[rng.Intn(len(open))]
			final := rng.Intn(2) == 0
			r := post(t, srv, "/authorizations/"+h.id+"/capture", tok[holdTo[h.id]], newKey(), fmt.Sprintf(`{"amount":%d,"final":%v}`, 1+rng.Intn(400), final))
			if r.status == 201 {
				p := addPay(r.body, true)
				c := refPayment{id: p.id, from: p.from, to: p.to, amount: p.revs[0].amount, at: p.revs[0].eff}
				h.captures = append(h.captures, c)
				rem := h.amount
				for _, x := range h.captures {
					rem -= x.amount
				}
				if final || rem == 0 {
					h.closedAt = c.at
				}
			}
		case op == 5:
			if len(open) == 0 {
				continue
			}
			h := open[rng.Intn(len(open))]
			r := post(t, srv, "/authorizations/"+h.id+"/void", tok[h.from], "", "")
			if r.status == 200 && h.closedAt == 0 {
				h.closedAt = parseNs(r.body["closed_at"].(string))
				instants = append(instants, h.closedAt)
			}
		default: // corrections (and replays)
			if last.key != "" && rng.Intn(6) == 0 {
				p := l.byID[last.pid]
				r := correct(t, srv, tok[p.from], last.pid, last.key, last.body)
				if r.status != 200 || r.raw != last.raw {
					t.Fatalf("replay: %d %s want %s", r.status, r.raw, last.raw)
				}
				outcomes["replay"]++
				continue
			}
			p := l.pays[rng.Intn(len(l.pays))]
			exp := len(p.revs)
			if rng.Intn(6) == 0 {
				exp = 1 + rng.Intn(len(p.revs)+1)
			}
			prev := p.revs[len(p.revs)-1].amount
			var amount int64
			switch rng.Intn(5) {
			case 0:
				amount = 0
			case 1:
				amount = prev
			case 2:
				amount = prev + int64(rng.Intn(400))
			case 3:
				amount = prev - int64(rng.Intn(400))
				if amount < 0 {
					amount = 0
				}
			default:
				amount = int64(rng.Intn(3000))
			}
			now := time.Now().Add(-time.Millisecond).UnixNano()
			var eff int64
			switch rng.Intn(4) {
			case 0:
				eff = instants[rng.Intn(len(instants))] // exactly another event's instant
			case 1:
				eff = instants[rng.Intn(len(instants))] + int64(1000*(rng.Intn(3)-1))
			case 2:
				eff = base.Add(-time.Hour).UnixNano() // before everything
			default:
				eff = now
			}
			if eff > now {
				eff = now
			}
			wantSt, wantCode := l.decide(p, exp, amount, eff, time.Now().UnixNano())
			key := newKey()
			body := corr(exp, amount, fmtNs(eff), fmt.Sprintf("step %d", step))
			r := correct(t, srv, tok[p.from], p.id, key, body)
			code := ""
			if r.body != nil {
				if e, ok := r.body["error"].(map[string]any); ok {
					code, _ = e["code"].(string)
				}
			}
			if r.status != wantSt || code != wantCode {
				t.Fatalf("step %d correction of %s (rev %d->%d, %d->%d at %s): got %d %s, reference %d %s",
					step, p.id, len(p.revs), exp, p.revs[len(p.revs)-1].amount, amount, fmtNs(eff), r.status, r.raw, wantSt, wantCode)
			}
			if r.status == 201 {
				p.revs = append(p.revs, lRev{amount, eff, parseNs(r.body["recorded_at"].(string))})
				instants = append(instants, eff, p.revs[len(p.revs)-1].rec)
				last.pid, last.key, last.body, last.raw = p.id, key, body, r.raw
				outcomes["201"]++
			} else {
				outcomes[wantCode]++
			}
		}
		compare(step)
	}
}
