package app

import (
	"net/http"
	"sort"
	"time"
)

// Authorizations (stage 2). A hold reserves money on the payer's wallet without moving
// it: `held` is the sum of the remaining amounts of the payer's open authorizations, and
// available = total − held is what payments, authorizations and settlement net debits
// may spend. Captures move money from the payer's total and shrink the hold by the same
// amount, so available is unchanged by a capture. All of it runs under Server.mu.
//
// Expiry is lazy: lock() calls sweep(now) before every read or write, which turns every
// open authorization whose expires_at is at or before now into `expired` and releases its
// remainder. So a deadline that passes with no traffic is visible on the next request.

const defaultTTL = 600
const maxTTL = 10_000_000_000 // seconds; keeps expires_at a 4-digit-year RFC 3339 time

type Authorization struct {
	ID         string   `json:"id"`
	From       string   `json:"from_user_id"`
	To         string   `json:"to_user_id"`
	Amount     int64    `json:"amount"`
	Captured   int64    `json:"captured_amount"`
	Note       string   `json:"note"`
	Visibility string   `json:"visibility"`
	Status     string   `json:"status"` // open | captured | voided | expired
	ExpiresAt  string   `json:"expires_at"`
	PaymentIDs []string `json:"payment_ids"`
	CreatedAt  string   `json:"created_at"`
	Seq        int64    `json:"seq"`
	// stage 3: when the hold closed (final capture, void: event time; expiry: expires_at)
	ClosedAt *string `json:"closed_at"`
	at, exp  int64   // CreatedAt, ExpiresAt as unix nanoseconds
	closed   int64   // ClosedAt as unix nanoseconds (0 while open)
}

func (a *Authorization) remaining() int64 {
	if a.Status != "open" {
		return 0
	}
	return a.Amount - a.Captured
}

// lock takes the single state lock and applies expiry up to now.
func (s *Server) lock() {
	s.mu.Lock()
	s.st.sweep(s.st.nowFloor())
}

func (st *State) sweep(now int64) {
	for a := range st.open {
		if a.exp <= now {
			st.close(a, "expired", a.ExpiresAt) // expiry takes effect at expires_at
		}
	}
}

// close ends an open authorization, releasing whatever is still held.
func (st *State) close(a *Authorization, status, at string) {
	st.held[a.From] -= a.Amount - a.Captured
	a.Status = status
	a.ClosedAt = &at
	a.closed = stampNanos(at)
	delete(st.open, a)
}

// synthesizedClose gives a close time to a hold closed before stage 3 (or seeded closed):
// expiry at expires_at; capture at its last capture; void at the last known event.
func (st *State) synthesizedClose(a *Authorization) string {
	if a.Status == "expired" {
		return a.ExpiresAt
	}
	last := a.CreatedAt
	for _, id := range a.PaymentIDs {
		if p := st.paymentByID[id]; p != nil && p.at > stampNanos(last) {
			last = p.CreatedAt
		}
	}
	return last
}

func (st *State) available(u *User) int64 { return u.Balance - st.held[u.ID] }

type authorizationView struct {
	AuthorizationID string   `json:"authorization_id"`
	FromUserID      string   `json:"from_user_id"`
	FromHandle      string   `json:"from_handle"`
	ToUserID        string   `json:"to_user_id"`
	ToHandle        string   `json:"to_handle"`
	Amount          int64    `json:"amount"`
	CapturedAmount  int64    `json:"captured_amount"`
	RemainingAmount int64    `json:"remaining_amount"`
	Currency        string   `json:"currency"`
	Note            string   `json:"note"`
	Visibility      string   `json:"visibility"`
	Status          string   `json:"status"`
	ExpiresAt       string   `json:"expires_at"`
	PaymentID       *string  `json:"payment_id"`
	PaymentIDs      []string `json:"payment_ids"`
	CreatedAt       string   `json:"created_at"`
	ClosedAt        *string  `json:"closed_at"`
}

func (st *State) authorizationView(a *Authorization) authorizationView {
	var last *string
	if n := len(a.PaymentIDs); n > 0 {
		p := a.PaymentIDs[n-1]
		last = &p
	}
	ids := append([]string{}, a.PaymentIDs...)
	return authorizationView{a.ID, a.From, st.handleOf(a.From), a.To, st.handleOf(a.To), a.Amount,
		a.Captured, a.remaining(), st.Currency, a.Note, a.Visibility, a.Status, a.ExpiresAt, last, ids, a.CreatedAt, a.ClosedAt}
}

// ---- POST /authorizations -----------------------------------------------------------

func (s *Server) postAuthorization(st *State, uid string, m map[string]any, _ []string) (int, any, *apiErr) {
	toHandle, e := reqString(m, "to_handle")
	if e != nil {
		return 0, nil, e
	}
	amount, e := amountField(m, "amount")
	if e != nil {
		return 0, nil, e
	}
	note, e := noteField(m)
	if e != nil {
		return 0, nil, e
	}
	vis, e := visibilityField(m)
	if e != nil {
		return 0, nil, e
	}
	me := st.userByID[uid]
	if toHandle == me.Handle {
		return 0, nil, errf(422, "self_payment", "cannot authorize a payment to yourself")
	}
	to := st.userByHandle[toHandle]
	if to == nil {
		return 0, nil, notFound("no user has that handle")
	}
	if st.available(me) < amount {
		return 0, nil, errf(409, "insufficient_funds", "available balance is below amount")
	}
	// created_at comes from the same monotonic µs event clock as payments, captures and
	// voids, so a hold never starts before an earlier write; expires_at = created_at + ttl
	// exactly (same fractional part)
	created, cNs := st.stamp()
	expires := time.Unix(0, cNs).Add(time.Duration(st.TTL) * time.Second).UTC().Format(microLayout)
	a := &Authorization{ID: st.newID("a_"), From: me.ID, To: to.ID, Amount: amount, Note: note,
		Visibility: vis, Status: "open", ExpiresAt: expires, PaymentIDs: []string{}, CreatedAt: created,
		Seq: st.seq(), at: stampNanos(created), exp: stampNanos(expires)}
	st.Authorizations = append(st.Authorizations, a)
	st.authByID[a.ID] = a
	st.open[a] = true
	st.held[me.ID] += amount
	return 201, st.authorizationView(a), nil
}

// ---- POST /authorizations/{id}/capture ----------------------------------------------

func (s *Server) captureAuthorization(st *State, uid string, m map[string]any, params []string) (int, any, *apiErr) {
	a := st.authByID[params[0]]
	if a == nil {
		return 0, nil, notFound("no such authorization")
	}
	if a.To != uid {
		return 0, nil, forbidden("only the receiver may capture an authorization")
	}
	var amount int64
	v, hasAmount := m["amount"]
	if hasAmount {
		// below 1 or not an integer: validation_failed. A whole number above 1e9 can never
		// fit the remainder (an authorization is at most 1e9), so it is reported with the
		// capture rule, capture_exceeds_authorization, once the status checks pass.
		r, ok := ratOf(v)
		if !ok || !r.IsInt() || r.Sign() < 1 {
			return 0, nil, invalid("amount must be a whole number of at least 1")
		}
		if n := r.Num(); n.IsInt64() && n.Int64() <= maxAmount {
			amount = n.Int64()
		} else {
			amount = maxAmount + 1
		}
	}
	final := true
	if fv, present := m["final"]; present {
		b, ok := fv.(bool)
		if !ok {
			return 0, nil, malformed("final must be a boolean")
		}
		final = b
	}
	// the capture's own event instant decides expiry: a hold whose expires_at is at or
	// before it has expired, even if the sweep (wall clock) still saw it open
	at, atNs := st.stamp()
	if a.Status == "open" && a.exp <= atNs {
		st.close(a, "expired", a.ExpiresAt)
	}
	switch a.Status {
	case "open":
	case "expired":
		return 0, nil, errf(409, "authorization_expired", "the authorization has expired")
	default:
		return 0, nil, errf(409, "authorization_not_open", "authorization is "+a.Status)
	}
	rem := a.remaining()
	if !hasAmount {
		amount = rem
	}
	if amount > rem {
		return 0, nil, errf(422, "capture_exceeds_authorization", "amount is above the remaining authorized amount")
	}
	payer, receiver := st.userByID[a.From], st.userByID[a.To]
	if receiver.Balance > maxBalance-amount {
		return 0, nil, invalid("receiver balance would exceed 2^53")
	}
	// one atomic step: money moves out of the payer's total, the hold shrinks by the same
	// amount, and a final capture releases the rest
	id := a.ID
	p := st.transfer(payer, receiver, amount, a.Note, a.Visibility, nil, nil, at)
	p.AuthID = &id
	st.held[a.From] -= amount
	a.Captured += amount
	a.PaymentIDs = append(a.PaymentIDs, p.ID)
	if final || a.Captured == a.Amount {
		st.close(a, "captured", p.CreatedAt)
	}
	return 201, st.paymentView(p), nil
}

// ---- POST /authorizations/{id}/void ---------------------------------------------------

func (s *Server) voidAuthorization(r *http.Request, uid string, params []string) (int, any, *apiErr) {
	s.lock()
	defer s.mu.Unlock()
	st := s.st
	a := st.authByID[params[0]]
	if a == nil {
		return 0, nil, notFound("no such authorization")
	}
	if a.From != uid {
		return 0, nil, forbidden("only the payer may void an authorization")
	}
	at, atNs := st.stamp() // the void's event instant also decides expiry
	if a.Status == "open" && a.exp <= atNs {
		st.close(a, "expired", a.ExpiresAt)
	}
	switch a.Status {
	case "open":
		st.close(a, "voided", at)
	case "voided":
	default:
		return 0, nil, errf(409, "authorization_not_open", "authorization is "+a.Status)
	}
	return 200, st.authorizationView(a), nil
}

// ---- GET /authorizations --------------------------------------------------------------

func (s *Server) listAuthorizations(r *http.Request, uid string, _ []string) (int, any, *apiErr) {
	q := r.URL.Query()
	dir, status := q.Get("direction"), q.Get("status")
	if q.Has("direction") && dir != "incoming" && dir != "outgoing" {
		return 0, nil, invalid("direction must be incoming or outgoing")
	}
	if q.Has("status") && status != "open" && status != "captured" && status != "voided" && status != "expired" {
		return 0, nil, invalid("status must be open, captured, voided or expired")
	}
	limit, offset, e := pageParams(r)
	if e != nil {
		return 0, nil, e
	}
	s.lock()
	defer s.mu.Unlock()
	st := s.st
	var mine []*Authorization
	for _, a := range st.Authorizations {
		if a.From != uid && a.To != uid {
			continue
		}
		if (dir == "outgoing" && a.From != uid) || (dir == "incoming" && a.To != uid) {
			continue
		}
		if status != "" && a.Status != status {
			continue
		}
		mine = append(mine, a)
	}
	sort.Slice(mine, func(i, j int) bool { return newestFirst(mine[i].at, mine[j].at, mine[i].Seq, mine[j].Seq) })
	pg, more := page(mine, limit, offset)
	out := make([]authorizationView, 0, len(pg))
	for _, a := range pg {
		out = append(out, st.authorizationView(a))
	}
	return 200, map[string]any{"authorizations": out, "has_more": more}, nil
}
