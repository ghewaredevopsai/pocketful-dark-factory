package app

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Stage 3: historical views. Every function here is a pure function of the append-only
// log (payment revisions, authorization events) plus a view instant T ("as of") and a
// knowledge instant K ("known at"); none of them reads the cached current balances.
//
//   - a payment contributes its latest revision recorded at or before K, applied at that
//     revision's effective time (included when effective_at <= T);
//   - a hold exists from its creation (known at creation) and is reduced by captures
//     (known and effective at capture time); a final capture or void releases it at its
//     event time; expiry releases it at expires_at, known as soon as creation is known.

// selRev is the payment's latest revision recorded at or before k, or nil.
func selRev(p *Payment, k int64) *Revision {
	var sel *Revision
	for _, rv := range p.Revisions {
		if rv.rec <= k {
			sel = rv
		}
	}
	return sel
}

func latestRev(p *Payment) *Revision { return p.Revisions[len(p.Revisions)-1] }

// signed returns the payment's effect on uid for a given amount (0 if not a party).
func signed(p *Payment, uid string, amount int64) int64 {
	switch uid {
	case p.From:
		return -amount
	case p.To:
		return amount
	}
	return 0
}

// netOf is the net effect of every payment's latest revision on uid.
func (st *State) netOf(uid string) int64 {
	var n int64
	for _, p := range st.Payments {
		n += signed(p, uid, latestRev(p).Amount)
	}
	return n
}

// totalAt is uid's balance as of t with knowledge k.
func (st *State) totalAt(u *User, t, k int64) int64 {
	b := *u.Opening
	for _, p := range st.Payments {
		if p.From != u.ID && p.To != u.ID {
			continue
		}
		if rv := selRev(p, k); rv != nil && rv.eff <= t {
			b += signed(p, u.ID, rv.Amount)
		}
	}
	return b
}

// holdAt is the amount authorization a still holds at t, as known at k.
func (st *State) holdAt(a *Authorization, t, k int64) int64 {
	if a.at > t || a.at > k { // not yet created, or creation not yet known
		return 0
	}
	if a.exp <= t { // expiry: effective at expires_at, known once creation is known
		return 0
	}
	if a.closed != 0 && a.Status != "expired" && a.closed <= t && a.closed <= k {
		return 0 // final capture or void, effective and known at its event time
	}
	rem := a.Amount - a.Captured // captured amounts that have no capture payment (seeded)
	for _, id := range a.PaymentIDs {
		p := st.paymentByID[id]
		if p == nil || p.AuthID == nil || *p.AuthID != a.ID {
			continue
		}
		rem += p.Amount // captures with a payment are applied at their own time below
		if p.at <= t && p.at <= k {
			rem -= p.Amount
		}
	}
	if rem < 0 {
		rem = 0
	}
	return rem
}

func (st *State) heldAt(u *User, t, k int64) int64 {
	var h int64
	for _, a := range st.Authorizations {
		if a.From == u.ID {
			h += st.holdAt(a, t, k)
		}
	}
	return h
}

// ---- query parsing ---------------------------------------------------------------------

// instantParam parses an RFC 3339 instant with an explicit offset. A '+' that arrived
// unencoded in a query string (decoded as a space) is restored. Returns the instant, the
// string to echo, whether the parameter was present, and an error for invalid values.
func instantParam(r *http.Request, name string) (int64, string, bool, *apiErr) {
	q := r.URL.Query()
	if !q.Has(name) {
		return 0, "", false, nil
	}
	v := q.Get(name)
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil && strings.Contains(v, " ") {
		v = strings.ReplaceAll(v, " ", "+")
		t, err = time.Parse(time.RFC3339Nano, v)
	}
	if err != nil || !strings.Contains(v, "T") {
		return 0, "", true, invalid(name + " must be an RFC 3339 instant with an offset")
	}
	return t.UnixNano(), v, true, nil
}

// ---- GET /me (with as_of / known_at) --------------------------------------------------------

func (s *Server) me(r *http.Request, uid string, _ []string) (int, any, *apiErr) {
	asOf, asOfEcho, hasAsOf, e := instantParam(r, "as_of")
	if e != nil {
		return 0, nil, e
	}
	knownAt, knownEcho, hasKnown, e := instantParam(r, "known_at")
	if e != nil {
		return 0, nil, e
	}
	s.lock()
	defer s.mu.Unlock()
	st := s.st
	u := st.userByID[uid]
	if u == nil { // a reset landed between token check and here
		return 0, nil, unauthed()
	}
	body := map[string]any{"user_id": u.ID, "display_name": u.DisplayName, "handle": u.Handle,
		"currency": st.Currency, "minor_units": st.MinorUnits}
	if !hasAsOf && !hasKnown {
		// current corrected values
		body["balance"], body["total"], body["available"], body["held"] = u.Balance, u.Balance, st.available(u), st.held[u.ID]
		return 200, body, nil
	}
	start := st.nowFloor() // the instant the read began
	t, k := start, start
	if hasAsOf {
		t = asOf
		body["as_of"] = asOfEcho
	}
	if hasKnown {
		k = knownAt
		body["known_at"] = knownEcho
	}
	total, held := st.totalAt(u, t, k), st.heldAt(u, t, k)
	body["balance"], body["total"], body["held"], body["available"] = total, total, held, total-held
	return 200, body, nil
}

// ---- statements -----------------------------------------------------------------------------

type statementEntry struct {
	p   *Payment
	rv  *Revision
	d   int64
	bal int64
}

// statement computes the full window [from, to) for u with knowledge k (from nil = the
// opening of the wallet). Entries are ordered by selected effective_at, then payment id.
func (st *State) statement(u *User, from *int64, to, k int64) (int64, []statementEntry, int64) {
	var sel []statementEntry
	opening := *u.Opening
	for _, p := range st.Payments {
		if p.From != u.ID && p.To != u.ID {
			continue
		}
		rv := selRev(p, k)
		if rv == nil {
			continue
		}
		d := signed(p, u.ID, rv.Amount)
		switch {
		case from != nil && rv.eff < *from:
			opening += d
		case rv.eff < to:
			sel = append(sel, statementEntry{p: p, rv: rv, d: d})
		}
	}
	sort.Slice(sel, func(i, j int) bool {
		if sel[i].rv.eff != sel[j].rv.eff {
			return sel[i].rv.eff < sel[j].rv.eff
		}
		return sel[i].p.ID < sel[j].p.ID
	})
	bal := opening
	for i := range sel {
		bal += sel[i].d
		sel[i].bal = bal
	}
	return opening, sel, bal
}

func newSnapshotToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "ss_" + hex.EncodeToString(b)
}

func (s *Server) getStatement(r *http.Request, uid string, _ []string) (int, any, *apiErr) {
	q := r.URL.Query()
	limit, offset, e := pageParams(r)
	if e != nil {
		return 0, nil, e
	}
	if q.Has("snapshot") {
		if q.Has("from") || q.Has("to") || q.Has("known_at") {
			return 0, nil, invalid("only limit and offset may accompany snapshot")
		}
		s.lock()
		defer s.mu.Unlock()
		snap := s.st.Snapshots[q.Get("snapshot")]
		if snap == nil || snap.User != uid {
			return 0, nil, notFound("no such statement snapshot")
		}
		return 200, s.st.renderStatement(q.Get("snapshot"), snap, limit, offset), nil
	}
	fromV, _, hasFrom, e := instantParam(r, "from")
	if e != nil {
		return 0, nil, e
	}
	toV, _, hasTo, e := instantParam(r, "to")
	if e != nil {
		return 0, nil, e
	}
	knownV, knownEcho, hasKnown, e := instantParam(r, "known_at")
	if e != nil {
		return 0, nil, e
	}
	if hasFrom && hasTo && fromV > toV {
		return 0, nil, invalid("from must not be after to")
	}
	s.lock()
	defer s.mu.Unlock()
	st := s.st
	if st.userByID[uid] == nil {
		return 0, nil, unauthed()
	}
	_, taken := st.stamp() // the read's instant; every later write is stamped after it
	snap := &Snapshot{User: uid, To: taken, K: taken, V4: true}
	if hasFrom {
		f := fromV
		snap.From = &f
	}
	if hasTo {
		snap.To = toV
	}
	if snap.From != nil && *snap.From > snap.To {
		return 0, nil, invalid("from must not be after to")
	}
	if hasKnown {
		snap.KnownAt = knownEcho
		if knownV < taken {
			snap.K = knownV
		}
	}
	token := newSnapshotToken()
	st.Snapshots[token] = snap
	return 200, st.renderStatement(token, snap, limit, offset), nil
}

// legacyPaymentView is the stage-3 payment shape (no refund_of), for imported snapshots.
type legacyPaymentView struct {
	PaymentID    string  `json:"payment_id"`
	FromUserID   string  `json:"from_user_id"`
	FromHandle   string  `json:"from_handle"`
	ToUserID     string  `json:"to_user_id"`
	ToHandle     string  `json:"to_handle"`
	Amount       int64   `json:"amount"`
	Currency     string  `json:"currency"`
	Note         string  `json:"note"`
	Visibility   string  `json:"visibility"`
	RequestID    *string `json:"request_id"`
	SettlementID *string `json:"settlement_id"`
	AuthID       *string `json:"authorization_id"`
	CreatedAt    string  `json:"created_at"`
}

type entryView struct {
	Payment      any    `json:"payment"`
	Delta        int64  `json:"delta"`
	BalanceAfter int64  `json:"balance_after"`
	Revision     int    `json:"revision"`
	EffectiveAt  string `json:"effective_at"`
	RecordedAt   string `json:"recorded_at"`
}

func (st *State) renderStatement(token string, snap *Snapshot, limit, offset int) map[string]any {
	u := st.userByID[snap.User]
	opening, entries, closing := st.statement(u, snap.From, snap.To, snap.K)
	pg, more := page(entries, limit, offset)
	out := make([]entryView, 0, len(pg))
	for _, en := range pg {
		pv := st.paymentView(en.p)
		pv.Amount = en.rv.Amount // the selected amount for this statement
		var payment any = pv
		if !snap.V4 {
			payment = legacyPaymentView{pv.PaymentID, pv.FromUserID, pv.FromHandle, pv.ToUserID, pv.ToHandle, pv.Amount,
				pv.Currency, pv.Note, pv.Visibility, pv.RequestID, pv.SettlementID, pv.AuthID, pv.CreatedAt}
		}
		out = append(out, entryView{payment, en.d, en.bal, en.rv.N, en.rv.EffectiveAt, en.rv.RecordedAt})
	}
	body := map[string]any{"opening_balance": opening, "entries": out, "closing_balance": closing,
		"has_more": more, "snapshot": token}
	if snap.KnownAt != "" {
		body["known_at"] = snap.KnownAt
	}
	return body
}

// ---- GET /payments/{id}/revisions -------------------------------------------------------------

type revisionView struct {
	PaymentID   string  `json:"payment_id"`
	Revision    int     `json:"revision"`
	Amount      int64   `json:"amount"`
	EffectiveAt string  `json:"effective_at"`
	RecordedAt  string  `json:"recorded_at"`
	Reason      string  `json:"reason"`
	BatchID     *string `json:"correction_batch_id"` // stage 4: null unless made by a batch
}

func revView(p *Payment, rv *Revision) revisionView {
	return revisionView{p.ID, rv.N, rv.Amount, rv.EffectiveAt, rv.RecordedAt, rv.Reason, rv.BatchID}
}

func (s *Server) getRevisions(r *http.Request, uid string, params []string) (int, any, *apiErr) {
	s.lock()
	defer s.mu.Unlock()
	p := s.st.paymentByID[params[0]]
	if p == nil || (p.From != uid && p.To != uid) { // a third party cannot tell it exists
		return 0, nil, notFound("no such payment")
	}
	out := make([]revisionView, 0, len(p.Revisions))
	for _, rv := range p.Revisions {
		out = append(out, revView(p, rv))
	}
	return 200, map[string]any{"revisions": out}, nil
}
