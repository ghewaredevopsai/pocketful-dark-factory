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
	// stage 3: the monotonic event clock (unix ns) and the frozen statement snapshots
	LastStamp int64                `json:"last_stamp"`
	Snapshots map[string]*Snapshot `json:"snapshots"`

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
	Balance     int64  `json:"balance"` // current total (cache of Opening + latest revisions)
	// balance before any payment moved; nil in stage-1/2 exports, derived by index()
	Opening *int64 `json:"opening,omitempty"`
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
	// stage 3: append-only revision log; revision 1 is the original payment
	Revisions []*Revision `json:"revisions,omitempty"`
	Seq       int64       `json:"seq"`
	at        int64       // CreatedAt as unix nanoseconds, for ordering
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

// Revision is one immutable version of a payment's amount. Revision 1 is the original
// (effective_at = recorded_at = created_at); corrections append more.
type Revision struct {
	N           int    `json:"revision"`
	Amount      int64  `json:"amount"`
	EffectiveAt string `json:"effective_at"`
	RecordedAt  string `json:"recorded_at"`
	Reason      string `json:"reason"`
	eff, rec    int64  // parsed instants, unix ns
}

// Snapshot freezes one statement read: the window, the effective knowledge instant and the
// echo values. Revisions are append-only and every later write is stamped after Taken, so
// recomputing the statement with known_at = min(known_at, Taken) reproduces the read exactly.
type Snapshot struct {
	User    string `json:"user_id"`
	From    *int64 `json:"from,omitempty"` // nil = opening of the wallet
	To      int64  `json:"to"`
	K       int64  `json:"k"`
	KnownAt string `json:"known_at,omitempty"` // echo of a supplied known_at
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

// microLayout is used for stage-3 event times (payments, revisions, hold events).
const microLayout = "2006-01-02T15:04:05.000000-07:00"

// stamp returns a new event instant from the monotonic state clock: never earlier than
// the wall clock, always at least 1µs after the previous stamp. Everything recorded after
// a statement snapshot is therefore strictly later than the snapshot.
// nowFloor is "now" for checks: the wall clock, but never before the last event stamp
// (stamps may run a few µs ahead of the wall clock in a burst).
func (st *State) nowFloor() int64 {
	if t := clockNow(); t > st.LastStamp {
		return t
	}
	return st.LastStamp
}

func (st *State) stamp() (string, int64) {
	t := clockNow() / 1000 * 1000
	if t <= st.LastStamp {
		t = st.LastStamp + 1000
	}
	st.LastStamp = t
	return time.Unix(0, t).UTC().Format(microLayout), t
}

func now() string { return time.Unix(0, clockNow()).UTC().Format(stampLayout) }

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
		if len(p.Revisions) == 0 { // stage-1/2 data: revision 1 is the original payment
			p.Revisions = []*Revision{{N: 1, Amount: p.Amount, EffectiveAt: p.CreatedAt, RecordedAt: p.CreatedAt}}
		}
		for _, rv := range p.Revisions {
			rv.eff, rv.rec = stampNanos(rv.EffectiveAt), stampNanos(rv.RecordedAt)
			if rv.rec > s.LastStamp {
				s.LastStamp = rv.rec
			}
		}
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
			a.ClosedAt = nil
		} else if a.ClosedAt == nil { // closed before stage 3: synthesize the close time
			c := s.synthesizedClose(a)
			a.ClosedAt = &c
		}
		if a.at > s.LastStamp {
			s.LastStamp = a.at // later events are stamped after every imported creation
		}
		a.closed = 0
		if a.ClosedAt != nil {
			a.closed = stampNanos(*a.ClosedAt)
			if a.closed > s.LastStamp && a.Status != "expired" {
				s.LastStamp = a.closed
			}
		}
		if a.Seq > s.NextSeq {
			s.NextSeq = a.Seq
		}
	}
	// opening balances: given (stage 3), else balance minus the net of current revisions
	for _, u := range s.Users {
		if u.Opening == nil {
			o := u.Balance - s.netOf(u.ID)
			u.Opening = &o
		}
	}
	if s.Snapshots == nil {
		s.Snapshots = map[string]*Snapshot{}
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
	// reset time: the default instant of seeded payments; every later API event is
	// stamped after it, and seeded instants may not lie after it (no future history)
	// the reset instant on the µs event clock: seeded payments and seeded open holds without
	// created_at take exactly this instant, which is at or after every supplied seeded
	// created_at (those may not lie after it) and before every later API event
	resetStr, resetNs := st.stamp()
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
		if v, present := o["created_at"]; !present || v == nil {
			p.CreatedAt = resetStr
		} else if p.CreatedAt, good = stamp(o); !good || stampNanos(p.CreatedAt) > resetNs {
			return bad("payments[%d].created_at must be an RFC 3339 instant not in the future", i)
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
	resetAt := resetNs // the same reset instant as every seeded default
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
		if v, present := o["closed_at"]; present && v != nil && a.Status != "open" {
			c, good := v.(string)
			if !good || stampNanos(c) == 0 || stampNanos(c) > resetNs {
				return bad("authorizations[%d].closed_at must be an RFC 3339 instant not in the future", i)
			}
			a.ClosedAt = &c
		}
		// a seeded open hold without created_at was created at reset (the same instant as
		// seeded payments without created_at, so its history starts consistent)
		if v, present := o["created_at"]; !present || v == nil {
			a.CreatedAt = resetStr
		} else if a.CreatedAt, good = stamp(o); !good || stampNanos(a.CreatedAt) > resetNs {
			return bad("authorizations[%d].created_at", i)
		}
		// a closed seeded hold need not reconstruct its lifecycle; only when both instants
		// are supplied must the close not precede the creation
		if v, present := o["created_at"]; present && v != nil && a.ClosedAt != nil && stampNanos(*a.ClosedAt) < stampNanos(a.CreatedAt) {
			return bad("authorizations[%d].closed_at is before created_at", i)
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
	st.index() // derive openings, revision 1 of seeded payments and hold close times
	return st, seeds, nil
}
