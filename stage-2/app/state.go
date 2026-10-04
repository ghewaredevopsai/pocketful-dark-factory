package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// State is the whole service state. It is only ever read or written while holding
// Server.mu (the single serialization point), and it is plain data so it can be
// exported and imported as one JSON object.
type State struct {
	Currency   string            `json:"currency"`
	MinorUnits int               `json:"minor_units"`
	Users      []*User           `json:"users"`
	Payments   []*Payment        `json:"payments"`
	Requests   []*Request        `json:"requests"`
	Operators  []string          `json:"settlement_operator_ids"`
	Tokens     map[string]string `json:"tokens"` // bearer token -> user id
	// completed idempotent writes, keyed by idemKey(user, method, path, key)
	Idem    map[string]*IdemRecord `json:"idempotency"`
	NextID  int64                  `json:"next_id"`  // counter for generated ids
	NextSeq int64                  `json:"next_seq"` // insertion order, tie-break for "newest first"
	// stage 2: holds. TTL 0 (a stage-1 export) means the default 600 s.
	TTL            int64            `json:"authorization_ttl_seconds"`
	Authorizations []*Authorization `json:"authorizations"`

	// indexes, rebuilt by index(); never serialized
	userByID     map[string]*User
	userByHandle map[string]*User
	userByEmail  map[string]*User // lowercased email
	operator     map[string]bool
	settlements  map[string]bool // settlement ids already used by payments
	paymentByID  map[string]*Payment
	requestByID  map[string]*Request
	authByID     map[string]*Authorization
	open         map[*Authorization]bool // open authorizations, for the expiry sweep
	held         map[string]int64        // user id -> sum of open holds' remaining amounts
}

type User struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Handle      string `json:"handle"`
	PassHash    string `json:"password_hash"`
	Balance     int64  `json:"balance"`
}

type Payment struct {
	ID           string  `json:"id"`
	From         string  `json:"from_user_id"`
	To           string  `json:"to_user_id"`
	Amount       int64   `json:"amount"`
	Note         string  `json:"note"`
	Visibility   string  `json:"visibility"`
	RequestID    *string `json:"request_id"`
	SettlementID *string `json:"settlement_id"`
	AuthID       *string `json:"authorization_id"`
	CreatedAt    string  `json:"created_at"`
	Seq          int64   `json:"seq"`
	at           int64   // CreatedAt as unix nanoseconds, for ordering
}

type Request struct {
	ID        string  `json:"id"`
	Requester string  `json:"requester_id"`
	Payer     string  `json:"payer_id"`
	Amount    int64   `json:"amount"`
	Note      string  `json:"note"`
	Status    string  `json:"status"`
	PaymentID *string `json:"payment_id"`
	CreatedAt string  `json:"created_at"`
	Seq       int64   `json:"seq"`
	at        int64   // CreatedAt as unix nanoseconds, for ordering
}

// IdemRecord is a completed idempotent write: the canonical request body and the
// exact original response body. Failed (4xx) attempts are never recorded.
type IdemRecord struct {
	Body     string          `json:"body"`
	Response json.RawMessage `json:"response"`
}

func idemKey(uid, method, path, key string) string {
	return uid + "\x00" + method + "\x00" + path + "\x00" + key
}

func stampNanos(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

var handleRE = regexp.MustCompile(`^[a-z0-9_]{1,20}$`)

const maxAmount = 1_000_000_000
const maxBalance = int64(1) << 53

// now is the server clock, RFC 3339 with an explicit numeric offset (CV-02).
const stampLayout = "2006-01-02T15:04:05-07:00"

func now() string { return time.Now().UTC().Format(stampLayout) }

func (s *State) index() {
	s.userByID = map[string]*User{}
	s.userByHandle = map[string]*User{}
	s.userByEmail = map[string]*User{}
	s.operator = map[string]bool{}
	s.settlements = map[string]bool{}
	s.paymentByID = map[string]*Payment{}
	s.requestByID = map[string]*Request{}
	for _, u := range s.Users {
		s.userByID[u.ID] = u
		s.userByHandle[u.Handle] = u
		s.userByEmail[strings.ToLower(u.Email)] = u
	}
	for _, id := range s.Operators {
		s.operator[id] = true
	}
	for _, p := range s.Payments {
		s.paymentByID[p.ID] = p
		p.at = stampNanos(p.CreatedAt)
		if p.SettlementID != nil {
			s.settlements[*p.SettlementID] = true
		}
		if p.Seq > s.NextSeq {
			s.NextSeq = p.Seq
		}
	}
	for _, r := range s.Requests {
		s.requestByID[r.ID] = r
		r.at = stampNanos(r.CreatedAt)
		if r.Seq > s.NextSeq {
			s.NextSeq = r.Seq
		}
	}
	if s.Tokens == nil {
		s.Tokens = map[string]string{}
	}
	if s.Idem == nil {
		s.Idem = map[string]*IdemRecord{}
	}
	if s.Authorizations == nil {
		s.Authorizations = []*Authorization{}
	}
	if s.TTL == 0 {
		s.TTL = defaultTTL
	}
	s.authByID = map[string]*Authorization{}
	s.open = map[*Authorization]bool{}
	s.held = map[string]int64{}
	for _, a := range s.Authorizations {
		s.authByID[a.ID] = a
		a.at = stampNanos(a.CreatedAt)
		a.exp = stampNanos(a.ExpiresAt)
		if a.PaymentIDs == nil {
			a.PaymentIDs = []string{}
		}
		if a.Status == "open" {
			s.open[a] = true
			s.held[a.From] += a.Amount - a.Captured
		}
		if a.Seq > s.NextSeq {
			s.NextSeq = a.Seq
		}
	}
}

// newID returns a generated id with the given prefix that no existing record uses.
func (s *State) newID(prefix string) string {
	for {
		s.NextID++
		id := fmt.Sprintf("%s%d", prefix, s.NextID)
		if s.userByID[id] == nil && s.paymentByID[id] == nil && s.requestByID[id] == nil && !s.settlements[id] &&
			s.authByID[id] == nil {
			return id
		}
	}
}

func (s *State) seq() int64 {
	s.NextSeq++
	return s.NextSeq
}

// seedUser is a fixture user whose password still has to be hashed (outside the lock).
type seedUser struct {
	user     *User
	password string
}

// parseFixture validates a reset fixture (FX-01..09) and builds a new State. Passwords
// are returned separately so the caller can hash them without holding the lock.
// Any problem is 422 validation_failed and the caller leaves the current state alone.
func parseFixture(m map[string]any) (*State, []seedUser, *apiErr) {
	bad := func(format string, a ...any) (*State, []seedUser, *apiErr) {
		return nil, nil, invalid("fixture: " + fmt.Sprintf(format, a...))
	}
	st := &State{Tokens: map[string]string{}}

	cur, ok := m["currency"].(string)
	if !ok || cur == "" {
		return bad("currency must be a non-empty string")
	}
	st.Currency = cur
	mu, ok := integral(m["minor_units"])
	if !ok || (mu != 0 && mu != 2 && mu != 3) {
		return bad("minor_units must be 0, 2 or 3")
	}
	st.MinorUnits = int(mu)

	list := func(k string, required bool) ([]any, bool) {
		v, present := m[k]
		if !present || (v == nil && !required) {
			return nil, !required
		}
		a, ok := v.([]any)
		return a, ok
	}
	objs := func(a []any) ([]map[string]any, bool) {
		out := make([]map[string]any, 0, len(a))
		for _, v := range a {
			o, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, o)
		}
		return out, true
	}
	str := func(o map[string]any, k string, required bool, def string) (string, bool) {
		v, present := o[k]
		if !present || v == nil {
			return def, !required
		}
		s, ok := v.(string)
		return s, ok && (!required || s != "")
	}
	optID := func(o map[string]any, k string) (*string, bool) {
		v, present := o[k]
		if !present || v == nil {
			return nil, true
		}
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, false
		}
		return &s, true
	}
	stamp := func(o map[string]any) (string, bool) {
		v, present := o["created_at"]
		if !present || v == nil {
			return now(), true
		}
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			return "", false
		}
		return s, true
	}

	rawUsers, ok := list("users", true)
	if !ok {
		return bad("users must be an array")
	}
	users, ok := objs(rawUsers)
	if !ok {
		return bad("every user must be an object")
	}
	st.index()
	var seeds []seedUser
	var total int64
	for i, o := range users {
		u := &User{}
		var pw string
		var good bool
		if u.ID, good = str(o, "id", true, ""); !good {
			return bad("users[%d].id", i)
		}
		if u.Email, good = str(o, "email", true, ""); !good {
			return bad("users[%d].email", i)
		}
		if pw, good = str(o, "password", true, ""); !good {
			return bad("users[%d].password", i)
		}
		if u.DisplayName, good = str(o, "display_name", false, ""); !good {
			return bad("users[%d].display_name", i)
		}
		if u.Handle, good = str(o, "handle", true, ""); !good || !handleRE.MatchString(u.Handle) {
			return bad("users[%d].handle must match ^[a-z0-9_]{1,20}$", i)
		}
		bal, good := integral(o["balance"])
		if !good || bal < 0 || bal > maxBalance {
			return bad("users[%d].balance must be an integer from 0 to 2^53", i)
		}
		u.Balance = bal
		total += bal
		if total > maxBalance {
			return bad("seeded total exceeds 2^53")
		}
		if st.userByID[u.ID] != nil {
			return bad("duplicate user id %q", u.ID)
		}
		if st.userByHandle[u.Handle] != nil {
			return bad("duplicate handle %q", u.Handle)
		}
		if st.userByEmail[strings.ToLower(u.Email)] != nil {
			return bad("duplicate email %q", u.Email)
		}
		st.Users = append(st.Users, u)
		st.userByID[u.ID] = u
		st.userByHandle[u.Handle] = u
		st.userByEmail[strings.ToLower(u.Email)] = u
		seeds = append(seeds, seedUser{u, pw})
	}

	visibility := func(o map[string]any) (string, bool) {
		v, good := str(o, "visibility", false, "public")
		return v, good && (v == "public" || v == "private")
	}
	amount := func(o map[string]any) (int64, bool) {
		a, good := integral(o["amount"])
		return a, good && a >= 1 && a <= maxAmount
	}

	rawPay, ok := list("payments", false)
	if !ok {
		return bad("payments must be an array")
	}
	pays, ok := objs(rawPay)
	if !ok {
		return bad("every payment must be an object")
	}
	for i, o := range pays {
		p := &Payment{}
		var good bool
		if p.ID, good = str(o, "id", true, ""); !good || st.paymentByID[p.ID] != nil {
			return bad("payments[%d].id missing or duplicate", i)
		}
		if p.From, good = str(o, "from_user_id", true, ""); !good || st.userByID[p.From] == nil {
			return bad("payments[%d].from_user_id unknown", i)
		}
		if p.To, good = str(o, "to_user_id", true, ""); !good || st.userByID[p.To] == nil {
			return bad("payments[%d].to_user_id unknown", i)
		}
		if p.Amount, good = amount(o); !good {
			return bad("payments[%d].amount", i)
		}
		if p.Note, good = str(o, "note", false, ""); !good {
			return bad("payments[%d].note", i)
		}
		if p.Visibility, good = visibility(o); !good {
			return bad("payments[%d].visibility", i)
		}
		if p.RequestID, good = optID(o, "request_id"); !good {
			return bad("payments[%d].request_id", i)
		}
		if p.SettlementID, good = optID(o, "settlement_id"); !good {
			return bad("payments[%d].settlement_id", i)
		}
		if p.SettlementID != nil {
			st.settlements[*p.SettlementID] = true
		}
		if p.AuthID, good = optID(o, "authorization_id"); !good {
			return bad("payments[%d].authorization_id", i)
		}
		if p.CreatedAt, good = stamp(o); !good {
			return bad("payments[%d].created_at", i)
		}
		p.Seq = st.seq()
		p.at = stampNanos(p.CreatedAt)
		st.Payments = append(st.Payments, p)
		st.paymentByID[p.ID] = p
	}

	rawReq, ok := list("requests", false)
	if !ok {
		return bad("requests must be an array")
	}
	reqs, ok := objs(rawReq)
	if !ok {
		return bad("every request must be an object")
	}
	for i, o := range reqs {
		r := &Request{}
		var good bool
		if r.ID, good = str(o, "id", true, ""); !good || st.requestByID[r.ID] != nil {
			return bad("requests[%d].id missing or duplicate", i)
		}
		if r.Requester, good = str(o, "requester_id", true, ""); !good || st.userByID[r.Requester] == nil {
			return bad("requests[%d].requester_id unknown", i)
		}
		if r.Payer, good = str(o, "payer_id", true, ""); !good || st.userByID[r.Payer] == nil {
			return bad("requests[%d].payer_id unknown", i)
		}
		if r.Amount, good = amount(o); !good {
			return bad("requests[%d].amount", i)
		}
		if r.Note, good = str(o, "note", false, ""); !good {
			return bad("requests[%d].note", i)
		}
		r.Status, good = str(o, "status", false, "pending")
		if !good || (r.Status != "pending" && r.Status != "paid" && r.Status != "declined" && r.Status != "cancelled") {
			return bad("requests[%d].status", i)
		}
		if r.PaymentID, good = optID(o, "payment_id"); !good {
			return bad("requests[%d].payment_id", i)
		}
		if r.CreatedAt, good = stamp(o); !good {
			return bad("requests[%d].created_at", i)
		}
		r.Seq = st.seq()
		r.at = stampNanos(r.CreatedAt)
		st.Requests = append(st.Requests, r)
		st.requestByID[r.ID] = r
	}

	if v, present := m["authorization_ttl_seconds"]; present {
		ttl, good := integral(v)
		if !good || ttl < 1 || ttl > maxTTL {
			return bad("authorization_ttl_seconds must be a positive integer")
		}
		st.TTL = ttl
	}
	rawAuth, ok := list("authorizations", false)
	if !ok {
		return bad("authorizations must be an array")
	}
	auths, ok := objs(rawAuth)
	if !ok {
		return bad("every authorization must be an object")
	}
	resetAt := time.Now().UnixNano()
	for i, o := range auths {
		a := &Authorization{PaymentIDs: []string{}}
		var good bool
		if a.ID, good = str(o, "id", true, ""); !good || st.authByID[a.ID] != nil {
			return bad("authorizations[%d].id missing or duplicate", i)
		}
		if a.From, good = str(o, "from_user_id", true, ""); !good || st.userByID[a.From] == nil {
			return bad("authorizations[%d].from_user_id unknown", i)
		}
		if a.To, good = str(o, "to_user_id", true, ""); !good || st.userByID[a.To] == nil {
			return bad("authorizations[%d].to_user_id unknown", i)
		}
		if a.Amount, good = amount(o); !good {
			return bad("authorizations[%d].amount", i)
		}
		if v, present := o["captured_amount"]; present && v != nil {
			c, good := integral(v)
			if !good || c < 0 || c > a.Amount {
				return bad("authorizations[%d].captured_amount", i)
			}
			a.Captured = c
		}
		if a.Note, good = str(o, "note", false, ""); !good {
			return bad("authorizations[%d].note", i)
		}
		if a.Visibility, good = visibility(o); !good {
			return bad("authorizations[%d].visibility", i)
		}
		a.Status, good = str(o, "status", false, "open")
		if !good || (a.Status != "open" && a.Status != "captured" && a.Status != "voided" && a.Status != "expired") {
			return bad("authorizations[%d].status", i)
		}
		if a.ExpiresAt, good = str(o, "expires_at", true, ""); !good || stampNanos(a.ExpiresAt) == 0 {
			return bad("authorizations[%d].expires_at must be an RFC 3339 time", i)
		}
		if pid, good := optID(o, "payment_id"); !good {
			return bad("authorizations[%d].payment_id", i)
		} else if pid != nil {
			a.PaymentIDs = []string{*pid}
		}
		if v, present := o["payment_ids"]; present && v != nil {
			arr, good := v.([]any)
			if !good {
				return bad("authorizations[%d].payment_ids", i)
			}
			a.PaymentIDs = []string{}
			for _, x := range arr {
				id, good := x.(string)
				if !good {
					return bad("authorizations[%d].payment_ids", i)
				}
				a.PaymentIDs = append(a.PaymentIDs, id)
			}
		}
		if a.CreatedAt, good = stamp(o); !good {
			return bad("authorizations[%d].created_at", i)
		}
		a.Seq = st.seq()
		a.at, a.exp = stampNanos(a.CreatedAt), stampNanos(a.ExpiresAt)
		if a.Status == "open" && a.exp <= resetAt {
			a.Status = "expired" // already past its deadline: holds nothing
		}
		st.Authorizations = append(st.Authorizations, a)
		st.authByID[a.ID] = a
		if a.Status == "open" {
			st.open[a] = true
			st.held[a.From] += a.Amount - a.Captured
		}
	}
	for _, u := range st.Users {
		if st.held[u.ID] > u.Balance {
			return bad("open holds of %s exceed their balance", u.ID)
		}
	}

	rawOps, ok := list("settlement_operator_ids", false)
	if !ok {
		return bad("settlement_operator_ids must be an array")
	}
	for i, v := range rawOps {
		id, good := v.(string)
		if !good || st.userByID[id] == nil {
			return bad("settlement_operator_ids[%d] is not a seeded user id", i)
		}
		if !st.operator[id] {
			st.Operators = append(st.Operators, id)
			st.operator[id] = true
		}
	}
	if st.Users == nil {
		st.Users = []*User{}
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
	return st, seeds, nil
}
