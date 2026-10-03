package app

import (
	"fmt"
	"net/http"
)

// ---- POST /splits (§8, §9) -------------------------------------------------------

type shareView struct {
	Handle string `json:"handle"`
	Amount int64  `json:"amount"`
}

// equalSplit implements §9: whole units, sum exactly amount, differ by at most one,
// the larger shares going to the first participants in the given order.
func equalSplit(amount int64, n int) []int64 {
	base, rem := amount/int64(n), amount%int64(n)
	out := make([]int64, n)
	for i := range out {
		out[i] = base
		if int64(i) < rem {
			out[i]++
		}
	}
	return out
}

func (s *Server) postSplit(st *State, uid string, m map[string]any, _ []string) (int, any, *apiErr) {
	amount, e := amountField(m, "amount")
	if e != nil {
		return 0, nil, e
	}
	raw, present := m["participant_handles"]
	if !present {
		return 0, nil, invalid("participant_handles is required")
	}
	list, ok := raw.([]any)
	if !ok {
		return 0, nil, malformed("participant_handles must be an array")
	}
	handles := make([]string, 0, len(list))
	for _, v := range list {
		h, ok := v.(string)
		if !ok {
			return 0, nil, malformed("participant_handles must contain strings")
		}
		handles = append(handles, h)
	}
	if len(handles) == 0 {
		return 0, nil, invalid("participant_handles must not be empty")
	}
	seen := map[string]bool{}
	for _, h := range handles {
		if seen[h] {
			return 0, nil, invalid("participant_handles contains a duplicate")
		}
		seen[h] = true
	}
	note, e := noteField(m)
	if e != nil {
		return 0, nil, e
	}
	// resolve every handle before creating anything: all requests or none
	users := make([]*User, len(handles))
	for i, h := range handles {
		if users[i] = st.userByHandle[h]; users[i] == nil {
			return 0, nil, notFound("no user has handle " + h)
		}
	}
	at := now()
	shares := equalSplit(amount, len(handles))
	shareViews := make([]shareView, len(handles))
	reqs := []requestView{}
	for i, u := range users {
		shareViews[i] = shareView{u.Handle, shares[i]}
		if u.ID == uid {
			continue
		}
		reqs = append(reqs, st.requestView(st.newRequest(uid, u.ID, shares[i], note, at)))
	}
	return 201, map[string]any{"split_id": st.newID("sp_"), "amount": amount, "currency": st.Currency,
		"note": note, "shares": shareViews, "requests": reqs, "created_at": at}, nil
}

// ---- POST /settlements (§11) -----------------------------------------------------

func operatorOnly(st *State, uid string) *apiErr {
	if !st.operator[uid] {
		return forbidden("only a settlement operator may submit settlements")
	}
	return nil
}

type transferIn struct {
	from, to  *User
	amount    int64
	note, vis string
}

func (s *Server) postSettlement(st *State, uid string, m map[string]any, _ []string) (int, any, *apiErr) {
	raw, present := m["transfers"]
	list, ok := raw.([]any)
	// a missing, non-array, empty or oversized batch is a malformed batch shape: 422
	if !present || !ok || len(list) < 1 || len(list) > 32 {
		return 0, nil, invalid("transfers must be an array of 1 to 32 transfers")
	}
	// entry errors, in input order, before any funds check
	ins := make([]transferIn, len(list))
	for i, v := range list {
		o, ok := v.(map[string]any)
		if !ok {
			return 0, nil, invalid(fmt.Sprintf("transfers[%d] must be an object", i))
		}
		fromH, ok1 := o["from_handle"].(string)
		toH, ok2 := o["to_handle"].(string)
		if !ok1 || !ok2 {
			return 0, nil, invalid(fmt.Sprintf("transfers[%d] needs string from_handle and to_handle", i))
		}
		t := &ins[i]
		var e *apiErr
		if t.amount, e = amountField(o, "amount"); e != nil {
			return 0, nil, e
		}
		if t.note, e = noteField(o); e != nil {
			return 0, nil, e
		}
		if t.vis, e = visibilityField(o); e != nil {
			return 0, nil, e
		}
		if t.from = st.userByHandle[fromH]; t.from == nil {
			return 0, nil, notFound(fmt.Sprintf("transfers[%d]: no user has handle %s", i, fromH))
		}
		if t.to = st.userByHandle[toH]; t.to == nil {
			return 0, nil, notFound(fmt.Sprintf("transfers[%d]: no user has handle %s", i, toH))
		}
		if t.from == t.to {
			return 0, nil, errf(422, "self_payment", fmt.Sprintf("transfers[%d] pays its own sender", i))
		}
	}
	// affordable iff every wallet's balance after the whole batch is within 0..2^53
	net := map[*User]int64{}
	for _, t := range ins {
		net[t.from] -= t.amount
		net[t.to] += t.amount
	}
	for u, d := range net {
		if u.Balance+d < 0 {
			return 0, nil, errf(409, "insufficient_funds", "the batch leaves a wallet below zero")
		}
		if u.Balance+d > maxBalance {
			return 0, nil, invalid("the batch pushes a wallet above 2^53")
		}
	}
	// commit: balances jump straight to their final values, so no wallet is ever
	// negative, even between two movements of the same batch
	for u, d := range net {
		u.Balance += d
	}
	sid := st.newID("st_")
	st.settlements[sid] = true
	at := now()
	views := make([]paymentView, len(ins))
	for i, t := range ins {
		id := sid
		p := &Payment{ID: st.newID("p_"), From: t.from.ID, To: t.to.ID, Amount: t.amount, Note: t.note,
			Visibility: t.vis, SettlementID: &id, CreatedAt: at, Seq: st.seq(), at: stampNanos(at)}
		st.Payments = append(st.Payments, p)
		st.paymentByID[p.ID] = p
		views[i] = st.paymentView(p)
	}
	return 201, map[string]any{"settlement_id": sid, "committed_at": at, "payments": views}, nil
}

// ---- export / import (§10) ---------------------------------------------------------

func (s *Server) export(r *http.Request, _ []string) (int, any, *apiErr) {
	s.mu.Lock()
	state := marshal(s.st) // serialized under the lock: an atomic, read-only snapshot
	s.mu.Unlock()
	return 200, map[string]any{"track": "pocketful", "format_version": 1, "state": rawJSON(state)}, nil
}
