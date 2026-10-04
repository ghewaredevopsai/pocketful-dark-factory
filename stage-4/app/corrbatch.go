package app

import "fmt"

// POST /correction-batches — the tenth idempotent write path (stage 4). A settlement
// operator corrects 1..32 payments at once, including complete settlements.
//
// Check order: 401 → 403 not an operator → key header → body object → claimed key (200
// replay / 409 reuse) → shape (corrections is an array of 1..32 objects with distinct
// payment_ids: 422) → item errors in INPUT ORDER, within one item: 404 → 422 fields → 422
// linked_payment_immutable (captures, refunds) → 409 stale_revision → 422
// refund_exceeds_payment → settlement completeness (422 incomplete_settlement), then
// identical effective instants per settlement (422 validation_failed) → current funds by
// the combined net change per wallet (409 insufficient_funds) → historical total and
// available at every boundary with all candidate revisions applied together (409
// historical_overdraft). A refusal changes nothing and claims no key; success commits
// every revision with one shared recorded_at stamp.

type batchItem struct {
	p *Payment
	f correctionFields
}

func (s *Server) postCorrectionBatch(st *State, uid string, m map[string]any, _ []string) (int, any, *apiErr) {
	raw, ok := m["corrections"].([]any)
	if !ok || len(raw) < 1 || len(raw) > 32 {
		return 0, nil, invalid("corrections must be an array of 1 to 32 corrections")
	}
	objs := make([]map[string]any, len(raw))
	seen := map[string]bool{}
	for i, v := range raw {
		o, ok := v.(map[string]any)
		if !ok {
			return 0, nil, invalid(fmt.Sprintf("corrections[%d] must be an object", i))
		}
		if id, ok := o["payment_id"].(string); ok {
			if seen[id] {
				return 0, nil, invalid("payment_ids in a batch must be distinct")
			}
			seen[id] = true
		}
		objs[i] = o
	}
	// item errors, strictly in input order
	items := make([]batchItem, len(objs))
	for i, o := range objs {
		id, ok := o["payment_id"].(string)
		if !ok {
			return 0, nil, invalid(fmt.Sprintf("corrections[%d].payment_id must be a string", i))
		}
		p := st.paymentByID[id]
		if p == nil {
			return 0, nil, notFound(fmt.Sprintf("corrections[%d]: no such payment", i))
		}
		f, e := st.correctionFields(o)
		if e != nil {
			return 0, nil, e
		}
		if p.AuthID != nil || p.RefundOf != nil {
			return 0, nil, errf(422, "linked_payment_immutable", fmt.Sprintf("corrections[%d]: captures and refunds cannot be corrected", i))
		}
		if int(f.exp) != latestRev(p).N {
			return 0, nil, errf(409, "stale_revision", fmt.Sprintf("corrections[%d]: expected_revision is not the latest revision", i))
		}
		if f.amount < st.refunded[p.ID] {
			return 0, nil, errf(422, "refund_exceeds_payment", fmt.Sprintf("corrections[%d]: below the amount already refunded", i))
		}
		items[i] = batchItem{p, f}
	}
	// settlement completeness: every member of every touched settlement is in the batch
	var touched []string
	inBatch := map[string]*batchItem{}
	for i := range items {
		inBatch[items[i].p.ID] = &items[i]
		if sid := items[i].p.SettlementID; sid != nil && !contains(touched, *sid) {
			touched = append(touched, *sid)
		}
	}
	for _, sid := range touched {
		for _, q := range st.Payments {
			if q.SettlementID != nil && *q.SettlementID == sid && inBatch[q.ID] == nil {
				return 0, nil, errf(422, "incomplete_settlement", "a batch must include every member of a settlement it corrects")
			}
		}
	}
	for _, sid := range touched {
		var eff *int64
		for i := range items {
			if q := items[i].p; q.SettlementID != nil && *q.SettlementID == sid {
				if eff == nil {
					eff = &items[i].f.effNs
				} else if *eff != items[i].f.effNs {
					return 0, nil, invalid("members of one settlement must have identical effective instants")
				}
			}
		}
	}
	// current funds: the combined net change per wallet
	net := map[*User]int64{}
	var order []*User
	for _, it := range items {
		diff := it.f.amount - latestRev(it.p).Amount
		from, to := st.userByID[it.p.From], st.userByID[it.p.To]
		for _, u := range []*User{from, to} {
			if _, ok := net[u]; !ok {
				net[u] = 0
				order = append(order, u)
			}
		}
		net[from] -= diff
		net[to] += diff
	}
	for _, u := range order {
		if d := net[u]; d < 0 && st.available(u) < -d {
			return 0, nil, errf(409, "insufficient_funds", "the batch's combined debit exceeds available funds")
		}
	}
	for _, u := range order {
		if d := net[u]; d > 0 && u.Balance > maxBalance-d {
			return 0, nil, invalid("the batch pushes a wallet above 2^53")
		}
	}
	// historical: all candidate revisions at once, one shared recorded_at
	rec, recNs := st.stamp()
	bid := st.newID("cb_")
	added := make([]*Revision, len(items))
	for i, it := range items { // effNs <= nowFloor() <= recNs, so no revision lies in the future
		id := bid
		rv := &Revision{N: latestRev(it.p).N + 1, Amount: it.f.amount, EffectiveAt: it.f.effStr, RecordedAt: rec,
			Reason: it.f.reason, BatchID: &id, eff: it.f.effNs, rec: recNs}
		it.p.Revisions = append(it.p.Revisions, rv)
		added[i] = rv
	}
	for _, u := range order {
		if !st.historyOK(u) {
			for _, it := range items {
				it.p.Revisions = it.p.Revisions[:len(it.p.Revisions)-1]
			}
			return 0, nil, errf(409, "historical_overdraft", "the batch makes a balance negative in the past")
		}
	}
	for _, u := range order {
		u.Balance += net[u]
	}
	st.batches[bid] = true
	revs := make([]revisionView, len(items))
	for i, it := range items {
		revs[i] = revView(it.p, added[i])
	}
	return 201, map[string]any{"correction_batch_id": bid, "recorded_at": rec, "revisions": revs}, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
