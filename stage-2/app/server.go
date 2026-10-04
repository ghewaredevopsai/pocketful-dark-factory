// Package app is the Pocketful HTTP service.
//
// Concurrency design (the single serialization point): all service state lives in one
// in-memory State value guarded by Server.mu. Every operation that reads or changes
// state takes mu for the whole check-and-apply step, so balance checks, debits,
// credits, status transitions and idempotency-key claims are atomic with respect to
// each other. Slow CPU work that touches no state (password hashing, JSON parsing,
// fixture validation) runs before the lock is taken.
package app

import (
	"crypto/sha256"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
)

type Server struct {
	mu sync.Mutex
	st *State

	hashMu    sync.Mutex
	hashCache map[[32]byte]string // sha256(password) -> stored hash; speeds up repeated resets
}

func New() *Server {
	st := &State{Currency: "EUR", MinorUnits: 2, Users: []*User{}, Payments: []*Payment{},
		Requests: []*Request{}, Operators: []string{}}
	st.index()
	return &Server{st: st, hashCache: map[[32]byte]string{}}
}

// handler returns (status, body, error). A nil body with a 2xx status writes no body.
type handler func(r *http.Request, params []string) (int, any, *apiErr)

type route struct {
	method  string
	pattern []string // "*" matches one non-empty path segment
	h       handler
}

func (s *Server) routes() []route {
	return []route{
		{"GET", []string{"health"}, s.health},
		{"POST", []string{"_test", "reset"}, s.reset},
		{"POST", []string{"auth", "signup"}, s.signup},
		{"POST", []string{"auth", "login"}, s.login},
		{"GET", []string{"me"}, s.authed(s.me)},
		{"POST", []string{"payments"}, s.idempotent(s.postPayment)},
		{"POST", []string{"requests"}, s.idempotent(s.postRequest)},
		{"GET", []string{"requests"}, s.authed(s.listRequests)},
		{"POST", []string{"requests", "*", "pay"}, s.idempotent(s.payRequest)},
		{"POST", []string{"requests", "*", "decline"}, s.authed(s.declineRequest)},
		{"POST", []string{"requests", "*", "cancel"}, s.authed(s.cancelRequest)},
		{"GET", []string{"activity"}, s.authed(s.activity)},
		{"POST", []string{"splits"}, s.idempotent(s.postSplit)},
		{"POST", []string{"settlements"}, s.idempotentGuarded(operatorOnly, s.postSettlement)},
		{"GET", []string{"_test", "export"}, s.export},
		{"POST", []string{"authorizations"}, s.idempotent(s.postAuthorization)},
		{"GET", []string{"authorizations"}, s.authed(s.listAuthorizations)},
		{"POST", []string{"authorizations", "*", "capture"}, s.idempotent(s.captureAuthorization)},
		{"POST", []string{"authorizations", "*", "void"}, s.authed(s.voidAuthorization)},
		{"POST", []string{"_test", "import"}, s.importState},
	}
}

func match(pattern, segs []string) ([]string, bool) {
	if len(pattern) != len(segs) {
		return nil, false
	}
	var params []string
	for i, p := range pattern {
		if p == "*" {
			if segs[i] == "" {
				return nil, false
			}
			params = append(params, segs[i])
		} else if p != segs[i] {
			return nil, false
		}
	}
	return params, true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			log.Printf("panic: %v", v)
			writeErr(w, errf(500, "internal_error", "internal error"))
		}
	}()
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	pathKnown := false
	for _, rt := range s.routes() {
		params, ok := match(rt.pattern, segs)
		if !ok {
			continue
		}
		pathKnown = true
		if rt.method != r.Method {
			continue
		}
		status, body, e := rt.h(r, params)
		if e != nil {
			writeErr(w, e)
			return
		}
		if body == nil {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, body)
		return
	}
	if pathKnown {
		writeErr(w, errf(405, "method_not_allowed", "method not allowed on this path"))
		return
	}
	writeErr(w, notFound("no such route"))
}

// authed resolves the bearer token before anything else (401 precedes body checks).
func (s *Server) authed(h func(r *http.Request, uid string, params []string) (int, any, *apiErr)) handler {
	return func(r *http.Request, params []string) (int, any, *apiErr) {
		uid, ok := s.caller(r)
		if !ok {
			return 0, nil, unauthed()
		}
		return h(r, uid, params)
	}
}

func (s *Server) caller(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", false
	}
	s.lock()
	defer s.mu.Unlock()
	uid, ok := s.st.Tokens[tok]
	if ok && s.st.userByID[uid] == nil {
		return "", false
	}
	return uid, ok
}

func (s *Server) health(r *http.Request, _ []string) (int, any, *apiErr) {
	return 200, map[string]any{"status": "ok"}, nil
}

// cachedHash hashes a password, reusing a previous hash of the same password.
// Fixtures seed many users with the same password and the harness resets often.
func (s *Server) cachedHash(pw string) string {
	k := sha256.Sum256([]byte(pw))
	s.hashMu.Lock()
	h, ok := s.hashCache[k]
	s.hashMu.Unlock()
	if ok {
		return h
	}
	h = hashPassword(pw)
	s.hashMu.Lock()
	if len(s.hashCache) > 10000 {
		s.hashCache = map[[32]byte]string{}
	}
	s.hashCache[k] = h
	s.hashMu.Unlock()
	return h
}

func (s *Server) reset(r *http.Request, _ []string) (int, any, *apiErr) {
	b, e := readBody(r, maxTestBody)
	if e != nil {
		return 0, nil, e
	}
	m, e := parseObject(b)
	if e != nil {
		return 0, nil, e
	}
	st, seeds, e := parseFixture(m)
	if e != nil {
		return 0, nil, e
	}
	// hash seed passwords in parallel, outside the state lock
	var wg sync.WaitGroup
	work := make(chan seedUser)
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for su := range work {
				su.user.PassHash = s.cachedHash(su.password)
			}
		}()
	}
	for _, su := range seeds {
		work <- su
	}
	close(work)
	wg.Wait()

	s.lock()
	s.st = st
	s.mu.Unlock()
	return 204, nil, nil
}

func (s *Server) signup(r *http.Request, _ []string) (int, any, *apiErr) {
	b, e := readBody(r, maxBody)
	if e != nil {
		return 0, nil, e
	}
	m, e := parseObject(b)
	if e != nil {
		return 0, nil, e
	}
	for _, k := range []string{"email", "password", "display_name"} {
		if _, _, e := optString(m, k); e != nil {
			return 0, nil, e
		}
	}
	email, e := reqString(m, "email")
	if e != nil {
		return 0, nil, e
	}
	pw, e := reqString(m, "password")
	if e != nil {
		return 0, nil, e
	}
	name, e := reqString(m, "display_name")
	if e != nil {
		return 0, nil, e
	}
	if !validEmail(email) {
		return 0, nil, invalid("email must be of the form local@domain")
	}
	if runeLen(pw) < 8 {
		return 0, nil, invalid("password must be at least 8 characters")
	}
	handle := deriveHandle(email)
	hash := hashPassword(pw)

	s.lock()
	defer s.mu.Unlock()
	st := s.st
	if st.userByEmail[strings.ToLower(email)] != nil {
		return 0, nil, errf(409, "email_taken", "email already registered")
	}
	if st.userByHandle[handle] != nil {
		return 0, nil, errf(409, "handle_taken", "the handle derived from this email is taken")
	}
	u := &User{ID: st.newID("u_"), Email: email, DisplayName: name, Handle: handle, PassHash: hash}
	st.Users = append(st.Users, u)
	st.userByID[u.ID] = u
	st.userByHandle[u.Handle] = u
	st.userByEmail[strings.ToLower(email)] = u
	tok := newToken()
	st.Tokens[tok] = u.ID
	return 201, map[string]any{"user_id": u.ID, "display_name": u.DisplayName, "token": tok}, nil
}

func (s *Server) login(r *http.Request, _ []string) (int, any, *apiErr) {
	b, e := readBody(r, maxBody)
	if e != nil {
		return 0, nil, e
	}
	m, e := parseObject(b)
	if e != nil {
		return 0, nil, e
	}
	for _, k := range []string{"email", "password"} {
		if _, _, e := optString(m, k); e != nil {
			return 0, nil, e
		}
	}
	email, e := reqString(m, "email")
	if e != nil {
		return 0, nil, e
	}
	pw, e := reqString(m, "password")
	if e != nil {
		return 0, nil, e
	}

	s.lock()
	u := s.st.userByEmail[strings.ToLower(email)]
	var hash string
	if u != nil {
		hash = u.PassHash
	}
	s.mu.Unlock()
	if u == nil || !checkPassword(hash, pw) {
		return 0, nil, unauthed()
	}

	s.lock()
	defer s.mu.Unlock()
	if s.st.userByID[u.ID] != u { // state was reset or replaced while verifying
		return 0, nil, unauthed()
	}
	tok := newToken()
	s.st.Tokens[tok] = u.ID
	return 200, map[string]any{"user_id": u.ID, "display_name": u.DisplayName, "token": tok}, nil
}

func (s *Server) me(r *http.Request, uid string, _ []string) (int, any, *apiErr) {
	s.lock()
	defer s.mu.Unlock()
	u := s.st.userByID[uid]
	if u == nil { // a reset landed between token check and here
		return 0, nil, unauthed()
	}
	return 200, map[string]any{"user_id": u.ID, "display_name": u.DisplayName, "handle": u.Handle,
		"balance": u.Balance, "total": u.Balance, "available": s.st.available(u), "held": s.st.held[u.ID],
		"currency": s.st.Currency, "minor_units": s.st.MinorUnits}, nil
}
