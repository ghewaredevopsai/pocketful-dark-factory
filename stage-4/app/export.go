package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

type rawJSON = json.RawMessage

// importDoc is the export envelope. The state is decoded strictly into State.
type importDoc struct {
	Track         *string         `json:"track"`
	FormatVersion json.RawMessage `json:"format_version"`
	State         json.RawMessage `json:"state"`
}

// importState: validate everything first (destination untouched on any error), then swap
// the whole state under the lock. Replacement, not merge (EX-04..EX-08, EX-14).
func (s *Server) importState(r *http.Request, _ []string) (int, any, *apiErr) {
	b, e := readBody(r, maxTestBody)
	if e != nil {
		return 0, nil, e
	}
	if _, e := parseObject(b); e != nil {
		return 0, nil, e
	}
	var doc importDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return 0, nil, invalid("export envelope has wrong field types")
	}
	if doc.Track == nil || *doc.Track != "pocketful" {
		return 0, nil, invalid(`track must be "pocketful"`)
	}
	if fv, ok := integral(json.Number(strings.TrimSpace(string(doc.FormatVersion)))); !ok || fv != 1 {
		return 0, nil, invalid("format_version must be 1")
	}
	if len(doc.State) == 0 || doc.State[0] != '{' {
		return 0, nil, invalid("state must be an object")
	}
	st := &State{}
	dec := json.NewDecoder(bytes.NewReader(doc.State))
	dec.DisallowUnknownFields()
	if err := dec.Decode(st); err != nil {
		return 0, nil, invalid("state is not a valid pocketful state: " + err.Error())
	}
	if e := validateState(st); e != nil {
		return 0, nil, e
	}
	s.lock()
	s.st = st
	s.mu.Unlock()
	return 204, nil, nil
}

func validateState(st *State) *apiErr {
	bad := func(msg string) *apiErr { return invalid("state: " + msg) }
	if st.Currency == "" {
		return bad("currency missing")
	}
	if st.MinorUnits != 0 && st.MinorUnits != 2 && st.MinorUnits != 3 {
		return bad("minor_units must be 0, 2 or 3")
	}
	if st.Users == nil {
		return bad("users missing")
	}
	if st.Payments == nil {
		st.Payments = []*Payment{}
	}
	if st.Requests == nil {
		st.Requests = []*Request{}
	}
	if st.Operators == nil {
		st.Operators = []string{}
	}
	if st.NextID < 0 || st.NextSeq < 0 {
		return bad("counters must not be negative")
	}
	users, handles, emails := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var total int64
	for _, u := range st.Users {
		if u == nil || u.ID == "" || users[u.ID] || !handleRE.MatchString(u.Handle) || handles[u.Handle] ||
			u.Email == "" || emails[strings.ToLower(u.Email)] || !strings.HasPrefix(u.PassHash, "pbkdf2-sha256$") {
			return bad("invalid or duplicate user")
		}
		if u.Balance < 0 || u.Balance > maxBalance {
			return bad("balance out of range")
		}
		total += u.Balance
		if total > maxBalance {
			return bad("total balance exceeds 2^53")
		}
		users[u.ID], handles[u.Handle], emails[strings.ToLower(u.Email)] = true, true, true
	}
	ids := map[string]bool{}
	for _, p := range st.Payments {
		if p == nil || p.ID == "" || ids[p.ID] || !users[p.From] || !users[p.To] ||
			p.Amount < 0 || p.Amount > maxAmount || (p.Visibility != "public" && p.Visibility != "private") ||
			stampNanos(p.CreatedAt) == 0 {
			return bad("invalid or duplicate payment")
		}
		ids[p.ID] = true
		// stage-3 revision log (absent in stage-1/2 exports: index() synthesizes revision 1)
		var prevRec int64
		for i, rv := range p.Revisions {
			if rv == nil || rv.N != i+1 || rv.Amount < 0 || rv.Amount > maxAmount ||
				stampNanos(rv.EffectiveAt) == 0 || stampNanos(rv.RecordedAt) <= prevRec ||
				(i == 0 && rv.Amount != p.Amount) {
				return bad("invalid payment revision")
			}
			prevRec = stampNanos(rv.RecordedAt)
		}
	}
	ids = map[string]bool{}
	for _, r := range st.Requests {
		if r == nil || r.ID == "" || ids[r.ID] || !users[r.Requester] || !users[r.Payer] ||
			r.Amount < 0 || r.Amount > maxAmount || stampNanos(r.CreatedAt) == 0 {
			return bad("invalid or duplicate request")
		}
		switch r.Status {
		case "pending", "paid", "declined", "cancelled":
		default:
			return bad("invalid request status")
		}
		ids[r.ID] = true
	}
	for _, id := range st.Operators {
		if !users[id] {
			return bad("unknown settlement operator")
		}
	}
	for tok, uid := range st.Tokens {
		if tok == "" || !users[uid] {
			return bad("token for unknown user")
		}
	}
	for k, rec := range st.Idem {
		if rec == nil || !json.Valid(rec.Response) || !users[strings.SplitN(k, "\x00", 2)[0]] {
			return bad("invalid idempotency record")
		}
	}
	if st.TTL < 0 || st.TTL > maxTTL {
		return bad("authorization_ttl_seconds out of range")
	}
	ids = map[string]bool{}
	for _, a := range st.Authorizations {
		if a == nil || a.ID == "" || ids[a.ID] || !users[a.From] || !users[a.To] || a.Amount < 1 || a.Amount > maxAmount ||
			a.Captured < 0 || a.Captured > a.Amount || (a.Visibility != "public" && a.Visibility != "private") ||
			stampNanos(a.ExpiresAt) == 0 || stampNanos(a.CreatedAt) == 0 {
			return bad("invalid or duplicate authorization")
		}
		switch a.Status {
		case "open", "captured", "voided", "expired":
		default:
			return bad("invalid authorization status")
		}
		ids[a.ID] = true
	}
	for tok, sn := range st.Snapshots {
		if sn == nil || tok == "" || !users[sn.User] {
			return bad("invalid statement snapshot")
		}
	}
	st.index()
	for _, u := range st.Users {
		if st.held[u.ID] > u.Balance {
			return bad("open holds exceed a balance")
		}
		if *u.Opening+st.netOf(u.ID) != u.Balance { // the cached balance must match the log
			return bad("balance does not match the payment log")
		}
	}
	// stage 4: every refund reverses an existing non-refund payment, is never corrected,
	// and a target's refunds never exceed its current amount
	for _, p := range st.Payments {
		if p.RefundOf == nil {
			continue
		}
		t := st.paymentByID[*p.RefundOf]
		if t == nil || t.RefundOf != nil || p.From != t.To || p.To != t.From || len(p.Revisions) != 1 {
			return bad("invalid refund link")
		}
		if st.refunded[t.ID] > latestRev(t).Amount {
			return bad("refunds exceed their payment")
		}
	}
	return nil
}
