package app

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
)

// ---- response views --------------------------------------------------------

type paymentView struct {
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
	CreatedAt    string  `json:"created_at"`
}

type requestView struct {
	RequestID       string  `json:"request_id"`
	RequesterID     string  `json:"requester_id"`
	RequesterHandle string  `json:"requester_handle"`
	PayerID         string  `json:"payer_id"`
	PayerHandle     string  `json:"payer_handle"`
	Amount          int64   `json:"amount"`
	Currency        string  `json:"currency"`
	Note            string  `json:"note"`
	Status          string  `json:"status"`
	PaymentID       *string `json:"payment_id"`
	CreatedAt       string  `json:"created_at"`
}

func (s *State) handleOf(uid string) string {
	if u := s.userByID[uid]; u != nil {
		return u.Handle
	}
	return ""
}

func (s *State) paymentView(p *Payment) paymentView {
	return paymentView{p.ID, p.From, s.handleOf(p.From), p.To, s.handleOf(p.To), p.Amount,
		s.Currency, p.Note, p.Visibility, p.RequestID, p.SettlementID, p.CreatedAt}
}

func (s *State) requestView(r *Request) requestView {
	return requestView{r.ID, r.Requester, s.handleOf(r.Requester), r.Payer, s.handleOf(r.Payer),
		r.Amount, s.Currency, r.Note, r.Status, r.PaymentID, r.CreatedAt}
}

// ---- field rules (§5, §8) ----------------------------------------------------

// amountField: required; any non-integral value (string, bool, null, 1.5) or a value
// outside 1..1000000000 is 422 validation_failed (ER-10, PM-04).
func amountField(m map[string]any, k string) (int64, *apiErr) {
	v, present := m[k]
	if !present {
		return 0, invalid(k + " is required")
	}
	a, ok := integral(v)
	if !ok || a < 1 || a > maxAmount {
		return 0, invalid(k + " must be an integer from 1 to 1000000000")
	}
	return a, nil
}

// noteField: optional, default ""; non-string (incl. null) or over 200 characters is 422.
func noteField(m map[string]any) (string, *apiErr) {
	v, present := m["note"]
	if !present {
		return "", nil
	}
	s, ok := v.(string)
	if !ok || runeLen(s) > 200 {
		return "", invalid("note must be a string of at most 200 characters")
	}
	return s, nil
}

// visibilityField: optional, default "public"; anything but "public"/"private" is 422.
func visibilityField(m map[string]any) (string, *apiErr) {
	v, present := m["visibility"]
	if !present {
		return "public", nil
	}
	s, _ := v.(string)
	if s != "public" && s != "private" {
		return "", invalid("visibility must be public or private")
	}
	return s, nil
}

var digitsRE = regexp.MustCompile(`^[0-9]+$`)

// pageParams parses limit (1..200, default 50) and offset (>= 0, default 0); both must be
// plain decimal digits (ER-14, ER-17, ER-18).
func pageParams(r *http.Request) (int, int, *apiErr) {
	q := r.URL.Query()
	limit, offset := 50, 0
	if q.Has("limit") {
		v := q.Get("limit")
		n, err := strconv.Atoi(v)
		if !digitsRE.MatchString(v) || err != nil || n < 1 || n > 200 {
			return 0, 0, invalid("limit must be an integer from 1 to 200")
		}
		limit = n
	}
	if q.Has("offset") {
		v := q.Get("offset")
		n, err := strconv.Atoi(v)
		if !digitsRE.MatchString(v) || err != nil {
			return 0, 0, invalid("offset must be an integer 0 or more")
		}
		offset = n
	}
	return limit, offset, nil
}

func page[T any](items []T, limit, offset int) ([]T, bool) {
	if offset >= len(items) {
		return []T{}, false
	}
	end := offset + limit
	if end >= len(items) {
		return items[offset:], false
	}
	return items[offset:end], true
}

// ---- idempotent writes (§7) --------------------------------------------------

// idempotent wraps the five idempotent write paths. Order of checks (ER-21):
// bearer (done by authed) -> Idempotency-Key header -> body is a JSON object ->
// already-claimed key (replay 200 / 409 reuse) -> the handler's field and resource
// checks. Everything from the key lookup to storing the response runs under s.mu, so
// claim, effect and receipt are one atomic step; a 4xx from the handler claims nothing.
type writeFunc func(st *State, uid string, m map[string]any, params []string) (int, any, *apiErr)

func (s *Server) idempotent(do writeFunc) handler { return s.idempotentGuarded(nil, do) }

// idempotentGuarded runs guard (a permission check such as "is an operator") right after
// authentication, before the key header and body are looked at.
func (s *Server) idempotentGuarded(guard func(st *State, uid string) *apiErr, do writeFunc) handler {
	return s.authed(func(r *http.Request, uid string, params []string) (int, any, *apiErr) {
		if guard != nil {
			s.mu.Lock()
			e := guard(s.st, uid)
			s.mu.Unlock()
			if e != nil {
				return 0, nil, e
			}
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			return 0, nil, errf(400, "missing_idempotency_key", "Idempotency-Key header is required")
		}
		if runeLen(key) > 255 {
			return 0, nil, invalid("Idempotency-Key must be 1 to 255 characters")
		}
		b, e := readBody(r, maxBody)
		if e != nil {
			return 0, nil, e
		}
		m, e := parseObject(b)
		if e != nil {
			return 0, nil, e
		}
		body := canonical(m)
		k := idemKey(uid, r.Method, r.URL.Path, key)

		s.mu.Lock()
		defer s.mu.Unlock()
		st := s.st
		if st.userByID[uid] == nil {
			return 0, nil, unauthed()
		}
		if rec := st.Idem[k]; rec != nil {
			if rec.Body != body {
				return 0, nil, errf(409, "idempotency_key_reuse", "key already used with a different request body")
			}
			return 200, rec.Response, nil
		}
		status, resp, e := do(st, uid, m, params)
		if e != nil {
			return 0, nil, e
		}
		raw := json.RawMessage(marshal(resp))
		st.Idem[k] = &IdemRecord{Body: body, Response: raw}
		return status, raw, nil
	})
}

// transfer moves amount from one wallet to another and records the payment. The caller
// holds s.mu and has already checked that the sender can afford it.
func (st *State) transfer(from, to *User, amount int64, note, vis string, requestID, settlementID *string, at string) *Payment {
	from.Balance -= amount
	to.Balance += amount
	p := &Payment{ID: st.newID("p_"), From: from.ID, To: to.ID, Amount: amount, Note: note,
		Visibility: vis, RequestID: requestID, SettlementID: settlementID, CreatedAt: at,
		Seq: st.seq(), at: stampNanos(at)}
	st.Payments = append(st.Payments, p)
	st.paymentByID[p.ID] = p
	return p
}

// ---- POST /payments ------------------------------------------------------------

func (s *Server) postPayment(st *State, uid string, m map[string]any, _ []string) (int, any, *apiErr) {
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
		return 0, nil, errf(422, "self_payment", "cannot pay yourself")
	}
	to := st.userByHandle[toHandle]
	if to == nil {
		return 0, nil, notFound("no user has that handle")
	}
	if me.Balance < amount {
		return 0, nil, errf(409, "insufficient_funds", "balance is below amount")
	}
	if to.Balance > maxBalance-amount {
		return 0, nil, invalid("receiver balance would exceed 2^53")
	}
	p := st.transfer(me, to, amount, note, vis, nil, nil, now())
	return 201, st.paymentView(p), nil
}

// ---- POST /requests ------------------------------------------------------------

func (s *Server) postRequest(st *State, uid string, m map[string]any, _ []string) (int, any, *apiErr) {
	payerHandle, e := reqString(m, "payer_handle")
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
	me := st.userByID[uid]
	if payerHandle == me.Handle {
		return 0, nil, errf(422, "self_request", "cannot request money from yourself")
	}
	payer := st.userByHandle[payerHandle]
	if payer == nil {
		return 0, nil, notFound("no user has that handle")
	}
	rq := st.newRequest(me.ID, payer.ID, amount, note, now())
	return 201, st.requestView(rq), nil
}

func (st *State) newRequest(requester, payer string, amount int64, note, at string) *Request {
	rq := &Request{ID: st.newID("rq_"), Requester: requester, Payer: payer, Amount: amount,
		Note: note, Status: "pending", CreatedAt: at, Seq: st.seq(), at: stampNanos(at)}
	st.Requests = append(st.Requests, rq)
	st.requestByID[rq.ID] = rq
	return rq
}

// ---- POST /requests/{id}/pay -----------------------------------------------------

func (s *Server) payRequest(st *State, uid string, m map[string]any, params []string) (int, any, *apiErr) {
	vis, e := visibilityField(m)
	if e != nil {
		return 0, nil, e
	}
	rq := st.requestByID[params[0]]
	if rq == nil {
		return 0, nil, notFound("no such request")
	}
	if rq.Payer != uid {
		return 0, nil, forbidden("only the payer may pay a request")
	}
	if rq.Status != "pending" {
		return 0, nil, errf(409, "request_not_pending", "request is "+rq.Status)
	}
	payer, requester := st.userByID[rq.Payer], st.userByID[rq.Requester]
	if payer.Balance < rq.Amount {
		return 0, nil, errf(409, "insufficient_funds", "balance is below amount")
	}
	if requester.Balance > maxBalance-rq.Amount {
		return 0, nil, invalid("receiver balance would exceed 2^53")
	}
	id := rq.ID
	p := st.transfer(payer, requester, rq.Amount, rq.Note, vis, &id, nil, now())
	rq.Status = "paid"
	pid := p.ID
	rq.PaymentID = &pid
	return 201, st.paymentView(p), nil
}

// ---- decline / cancel (no key; idempotent by state) -------------------------------

func (s *Server) declineRequest(r *http.Request, uid string, params []string) (int, any, *apiErr) {
	return s.settleRequest(uid, params[0], true)
}

func (s *Server) cancelRequest(r *http.Request, uid string, params []string) (int, any, *apiErr) {
	return s.settleRequest(uid, params[0], false)
}

func (s *Server) settleRequest(uid, id string, decline bool) (int, any, *apiErr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	rq := st.requestByID[id]
	if rq == nil {
		return 0, nil, notFound("no such request")
	}
	target := "cancelled"
	if decline {
		target = "declined"
		if rq.Payer != uid {
			return 0, nil, forbidden("only the payer may decline a request")
		}
	} else if rq.Requester != uid {
		return 0, nil, forbidden("only the requester may cancel a request")
	}
	switch rq.Status {
	case target:
	case "pending":
		rq.Status = target
	default:
		return 0, nil, errf(409, "request_not_pending", "request is "+rq.Status)
	}
	return 200, st.requestView(rq), nil
}

// ---- GET /requests and GET /activity ------------------------------------------------

func newestFirst(atI, atJ, seqI, seqJ int64) bool {
	if atI != atJ {
		return atI > atJ
	}
	return seqI > seqJ
}

func (s *Server) listRequests(r *http.Request, uid string, _ []string) (int, any, *apiErr) {
	q := r.URL.Query()
	dir, status := q.Get("direction"), q.Get("status")
	if q.Has("direction") && dir != "incoming" && dir != "outgoing" {
		return 0, nil, invalid("direction must be incoming or outgoing")
	}
	if q.Has("status") && status != "pending" && status != "paid" && status != "declined" && status != "cancelled" {
		return 0, nil, invalid("status must be pending, paid, declined or cancelled")
	}
	limit, offset, e := pageParams(r)
	if e != nil {
		return 0, nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	var mine []*Request
	for _, rq := range st.Requests {
		if rq.Payer != uid && rq.Requester != uid {
			continue
		}
		if (dir == "incoming" && rq.Payer != uid) || (dir == "outgoing" && rq.Requester != uid) {
			continue
		}
		if status != "" && rq.Status != status {
			continue
		}
		mine = append(mine, rq)
	}
	sort.Slice(mine, func(i, j int) bool { return newestFirst(mine[i].at, mine[j].at, mine[i].Seq, mine[j].Seq) })
	pg, more := page(mine, limit, offset)
	out := make([]requestView, 0, len(pg))
	for _, rq := range pg {
		out = append(out, st.requestView(rq))
	}
	return 200, map[string]any{"requests": out, "has_more": more}, nil
}

func (s *Server) activity(r *http.Request, uid string, _ []string) (int, any, *apiErr) {
	limit, offset, e := pageParams(r)
	if e != nil {
		return 0, nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	var feed []*Payment
	for _, p := range st.Payments {
		if p.Visibility == "public" || p.From == uid || p.To == uid { // FD-02, the only rule
			feed = append(feed, p)
		}
	}
	sort.Slice(feed, func(i, j int) bool { return newestFirst(feed[i].at, feed[j].at, feed[i].Seq, feed[j].Seq) })
	pg, more := page(feed, limit, offset)
	out := make([]paymentView, 0, len(pg))
	for _, p := range pg {
		out = append(out, st.paymentView(p))
	}
	return 200, map[string]any{"payments": out, "has_more": more}, nil
}
