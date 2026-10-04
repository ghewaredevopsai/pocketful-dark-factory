package app

import (
	"math"
	"sort"
	"time"
)

// POST /payments/{payment_id}/corrections — the eighth idempotent write path.
//
// Check order (after the idempotency wrapper: 401 → key header → body object → claimed
// key): 404 unknown → 403 not the original sender → 422 field validation → 422
// linked_payment_immutable → 409 stale_revision → 409 insufficient_funds (the current
// debit against available) → 409 historical_overdraft. Everything from the key lookup to
// the stored receipt runs under Server.mu; a refusal changes nothing and claims no key.
func (s *Server) postCorrection(st *State, uid string, m map[string]any, params []string) (int, any, *apiErr) {
	p := st.paymentByID[params[0]]
	if p == nil {
		return 0, nil, notFound("no such payment")
	}
	if p.From != uid {
		return 0, nil, forbidden("only the original sender may correct a payment")
	}
	// fields: every invalid value or wrong type is 422 validation_failed
	exp, ok := integral(m["expected_revision"])
	if !ok || exp < 1 {
		return 0, nil, invalid("expected_revision must be a positive integer")
	}
	amount, ok := integral(m["amount"])
	if !ok || amount < 0 || amount > maxAmount {
		return 0, nil, invalid("amount must be an integer from 0 to 1000000000")
	}
	reason, ok := m["reason"].(string)
	if !ok || runeLen(reason) < 1 || runeLen(reason) > 200 {
		return 0, nil, invalid("reason must be a string of 1 to 200 characters")
	}
	effStr, ok := m["effective_at"].(string)
	var effNs int64
	if ok {
		t, err := time.Parse(time.RFC3339Nano, effStr)
		ok = err == nil
		effNs = t.UnixNano()
	}
	if !ok || effNs > st.nowFloor() {
		return 0, nil, invalid("effective_at must be an RFC 3339 instant with an offset, not later than now")
	}
	if p.SettlementID != nil || p.AuthID != nil || p.RefundOf != nil {
		return 0, nil, errf(422, "linked_payment_immutable", "settlement members, captures and refunds cannot be corrected")
	}
	prev := latestRev(p)
	if int(exp) != prev.N {
		return 0, nil, errf(409, "stale_revision", "expected_revision is not the latest revision")
	}
	if amount < st.refunded[p.ID] {
		return 0, nil, errf(422, "refund_exceeds_payment", "a correction cannot go below the amount already refunded")
	}
	// the difference moves between the same two wallets: an increase debits the sender,
	// a decrease debits the receiver; the current debit must be affordable now
	diff := amount - prev.Amount
	sender, receiver := st.userByID[p.From], st.userByID[p.To]
	debited, credited, d := sender, receiver, diff
	if diff < 0 {
		debited, credited, d = receiver, sender, -diff
	}
	if st.available(debited) < d {
		return 0, nil, errf(409, "insufficient_funds", "the correction's debit exceeds available funds")
	}
	if credited.Balance > maxBalance-d {
		return 0, nil, invalid("the correction pushes a wallet above 2^53")
	}
	// candidate revision; recorded_at strictly increases (monotonic stamp)
	rec, recNs := st.stamp()
	if effNs > recNs {
		return 0, nil, invalid("effective_at must not be later than now")
	}
	rv := &Revision{N: prev.N + 1, Amount: amount, EffectiveAt: effStr, RecordedAt: rec, Reason: reason, eff: effNs, rec: recNs}
	p.Revisions = append(p.Revisions, rv)
	if !st.historyOK(sender) || !st.historyOK(receiver) {
		p.Revisions = p.Revisions[:len(p.Revisions)-1]
		return 0, nil, errf(409, "historical_overdraft", "the correction makes a balance negative in the past")
	}
	sender.Balance -= diff
	receiver.Balance += diff
	return 201, revView(p, rv), nil
}

// historyOK reports whether u's total and available stay nonnegative at every
// effective-time and hold-event boundary, under the latest known revisions. All movements
// at one instant are applied together before the check.
func (st *State) historyOK(u *User) bool {
	const all = math.MaxInt64 // latest knowledge: every recorded revision and event
	type move struct{ t, d int64 }
	var moves []move
	times := map[int64]bool{}
	for _, p := range st.Payments {
		if p.From != u.ID && p.To != u.ID {
			continue
		}
		rv := latestRev(p)
		moves = append(moves, move{rv.eff, signed(p, u.ID, rv.Amount)})
		times[rv.eff] = true
	}
	var holds []*Authorization
	for _, a := range st.Authorizations {
		if a.From != u.ID {
			continue
		}
		holds = append(holds, a)
		times[a.at], times[a.exp] = true, true
		if a.closed != 0 {
			times[a.closed] = true
		}
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].t < moves[j].t })
	bounds := make([]int64, 0, len(times))
	for t := range times {
		bounds = append(bounds, t)
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	total, i := *u.Opening, 0
	for _, b := range bounds {
		for i < len(moves) && moves[i].t <= b { // every movement at or before b, combined
			total += moves[i].d
			i++
		}
		if total < 0 {
			return false
		}
		var held int64
		for _, a := range holds {
			held += st.holdAt(a, b, all)
		}
		if total-held < 0 {
			return false
		}
	}
	return true
}
