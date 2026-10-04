// Work item 1 tests: health, reset/fixture validation, auth, /me, error envelope.
// Run from stage-1/: go test ./tests/ -count=1
package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pocketful/app"
)

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func do(t *testing.T, srv *httptest.Server, method, path, token, body string) resp {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	out := resp{status: r.StatusCode, header: r.Header, raw: string(b)}
	_ = json.Unmarshal(b, &out.body)
	return out
}

func wantErr(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d (%s)", r.status, status, r.raw)
	}
	e, _ := r.body["error"].(map[string]any)
	if e == nil || e["code"] != code {
		t.Fatalf("code %v, want %s (%s)", r.body, code, r.raw)
	}
	if _, ok := e["message"].(string); !ok {
		t.Fatalf("error envelope lacks message: %s", r.raw)
	}
}

func user(h string, bal any) string {
	b, _ := json.Marshal(bal)
	return fmt.Sprintf(`{"id":"u_%s","email":"%s@example.com","password":"correct horse","display_name":"%s","handle":"%s","balance":%s}`, h, h, strings.ToUpper(h), h, b)
}

func fixture(users ...string) string {
	return `{"currency":"EUR","minor_units":2,"users":[` + strings.Join(users, ",") + `],"payments":[],"requests":[]}`
}

var std = fixture(user("ada", 10000), user("bob", 2500), user("cy", 500))

func setup(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	if r := do(t, srv, "POST", "/_test/reset", "", std); r.status != 204 {
		t.Fatalf("reset: %d %s", r.status, r.raw)
	}
	return srv
}

func login(t *testing.T, srv *httptest.Server, email, pw string) string {
	t.Helper()
	r := do(t, srv, "POST", "/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q}`, email, pw))
	if r.status != 200 {
		t.Fatalf("login %s: %d %s", email, r.status, r.raw)
	}
	return r.body["token"].(string)
}

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(app.New())
	defer srv.Close()
	r := do(t, srv, "GET", "/health", "", "")
	if r.status != 200 || r.body["status"] != "ok" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type %q", ct)
	}
}

func TestSeededLoginAndMe(t *testing.T) {
	srv := setup(t)
	tok := login(t, srv, "ada@example.com", "correct horse")
	r := do(t, srv, "GET", "/me", tok, "")
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	want := map[string]any{"user_id": "u_ada", "display_name": "ADA", "handle": "ada",
		"balance": 10000.0, "currency": "EUR", "minor_units": 2.0}
	for k, v := range want {
		if r.body[k] != v {
			t.Fatalf("%s = %v, want %v (%s)", k, r.body[k], v, r.raw)
		}
	}
}

func TestMultipleTokensStayValid(t *testing.T) {
	srv := setup(t)
	t1 := login(t, srv, "ada@example.com", "correct horse")
	t2 := login(t, srv, "ada@example.com", "correct horse")
	if t1 == t2 {
		t.Fatal("tokens should differ")
	}
	for _, tok := range []string{t1, t2} {
		if r := do(t, srv, "GET", "/me", tok, ""); r.status != 200 {
			t.Fatalf("%d", r.status)
		}
	}
}

func TestLoginFailures(t *testing.T) {
	srv := setup(t)
	wantErr(t, do(t, srv, "POST", "/auth/login", "", `{"email":"ada@example.com","password":"wrong pass"}`), 401, "unauthenticated")
	wantErr(t, do(t, srv, "POST", "/auth/login", "", `{"email":"nobody@example.com","password":"correct horse"}`), 401, "unauthenticated")
	wantErr(t, do(t, srv, "POST", "/auth/login", "", `{"email":"ada@example.com"}`), 422, "validation_failed")
	wantErr(t, do(t, srv, "POST", "/auth/login", "", `{"email":1,"password":"x"}`), 400, "malformed_request")
	wantErr(t, do(t, srv, "POST", "/auth/login", "", `{nope`), 400, "malformed_request")
}

func TestBearerRequired(t *testing.T) {
	srv := setup(t)
	wantErr(t, do(t, srv, "GET", "/me", "", ""), 401, "unauthenticated")
	wantErr(t, do(t, srv, "GET", "/me", "nonsense", ""), 401, "unauthenticated")
	req, _ := http.NewRequest("GET", srv.URL+"/me", nil)
	req.Header.Set("Authorization", "Basic abc")
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 401 {
		t.Fatalf("basic auth: %d", r.StatusCode)
	}
}

func TestSignup(t *testing.T) {
	srv := setup(t)
	r := do(t, srv, "POST", "/auth/signup", "", `{"email":"Dee.Ann+tag@example.com","password":"correct horse","display_name":"Dee","extra":1}`)
	if r.status != 201 || r.body["display_name"] != "Dee" || r.body["user_id"] == "" || r.body["token"] == "" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	me := do(t, srv, "GET", "/me", r.body["token"].(string), "")
	if me.body["handle"] != "dee_ann_tag" || me.body["balance"] != 0.0 {
		t.Fatalf("%s", me.raw)
	}
	// login with the new account works
	login(t, srv, "Dee.Ann+tag@example.com", "correct horse")
}

func TestSignupHandleTruncatedAndUnicode(t *testing.T) {
	srv := setup(t)
	r := do(t, srv, "POST", "/auth/signup", "", `{"email":"`+strings.Repeat("a", 30)+`@example.com","password":"correct horse","display_name":"L"}`)
	me := do(t, srv, "GET", "/me", r.body["token"].(string), "")
	if me.body["handle"] != strings.Repeat("a", 20) {
		t.Fatalf("%s", me.raw)
	}
	r = do(t, srv, "POST", "/auth/signup", "", `{"email":"Zoë-X@example.com","password":"correct horse","display_name":"Z"}`)
	me = do(t, srv, "GET", "/me", r.body["token"].(string), "")
	if me.body["handle"] != "zo__x" {
		t.Fatalf("%s", me.raw)
	}
}

func TestSignupErrors(t *testing.T) {
	srv := setup(t)
	wantErr(t, do(t, srv, "POST", "/auth/signup", "", `{"email":"ada@example.com","password":"correct horse","display_name":"A"}`), 409, "email_taken")
	wantErr(t, do(t, srv, "POST", "/auth/signup", "", `{"email":"ADA@example.com","password":"correct horse","display_name":"A"}`), 409, "email_taken")
	wantErr(t, do(t, srv, "POST", "/auth/signup", "", `{"email":"ada@other.example","password":"correct horse","display_name":"A"}`), 409, "handle_taken")
	// no account was created by the failed signup
	wantErr(t, do(t, srv, "POST", "/auth/login", "", `{"email":"ada@other.example","password":"correct horse"}`), 401, "unauthenticated")
	wantErr(t, do(t, srv, "POST", "/auth/signup", "", `{"email":"x@example.com","password":"1234567","display_name":"A"}`), 422, "validation_failed")
	if r := do(t, srv, "POST", "/auth/signup", "", `{"email":"x8@example.com","password":"12345678","display_name":"A"}`); r.status != 201 {
		t.Fatalf("8-char password: %d", r.status)
	}
	for _, e := range []string{"noat", "@example.com", "x@", "a@b@c", "a b@c"} {
		wantErr(t, do(t, srv, "POST", "/auth/signup", "", `{"email":"`+e+`","password":"correct horse","display_name":"A"}`), 422, "validation_failed")
	}
	wantErr(t, do(t, srv, "POST", "/auth/signup", "", `{"password":"correct horse","display_name":"A"}`), 422, "validation_failed")
	wantErr(t, do(t, srv, "POST", "/auth/signup", "", `{"email":"y@example.com","password":12345678,"display_name":"A"}`), 400, "malformed_request")
	for _, b := range []string{`[]`, `"x"`, `1`, `null`, ``, `{"a":1} {}`} {
		wantErr(t, do(t, srv, "POST", "/auth/signup", "", b), 400, "malformed_request")
	}
}

func TestConcurrentSignupSameEmail(t *testing.T) {
	srv := setup(t)
	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- do(t, srv, "POST", "/auth/signup", "", `{"email":"race@example.com","password":"correct horse","display_name":"R"}`).status
		}()
	}
	wg.Wait()
	close(codes)
	n201 := 0
	for c := range codes {
		if c == 201 {
			n201++
		} else if c != 409 {
			t.Fatalf("unexpected %d", c)
		}
	}
	if n201 != 1 {
		t.Fatalf("%d signups succeeded", n201)
	}
}

func TestResetReplacesState(t *testing.T) {
	srv := setup(t)
	tok := login(t, srv, "ada@example.com", "correct horse")
	r := do(t, srv, "POST", "/auth/signup", "", `{"email":"dee@example.com","password":"correct horse","display_name":"D"}`)
	if r.status != 201 {
		t.Fatal(r.raw)
	}
	jpy := `{"currency":"JPY","minor_units":0,"users":[` + user("ada", 1e3) + `]}`
	if r := do(t, srv, "POST", "/_test/reset", "", jpy); r.status != 204 || r.raw != "" {
		t.Fatalf("%d %q", r.status, r.raw)
	}
	wantErr(t, do(t, srv, "GET", "/me", tok, ""), 401, "unauthenticated") // old tokens gone
	wantErr(t, do(t, srv, "POST", "/auth/login", "", `{"email":"dee@example.com","password":"correct horse"}`), 401, "unauthenticated")
	tok = login(t, srv, "ada@example.com", "correct horse")
	me := do(t, srv, "GET", "/me", tok, "")
	if me.body["currency"] != "JPY" || me.body["minor_units"] != 0.0 || me.body["balance"] != 1000.0 {
		t.Fatal(me.raw)
	}
}

func TestResetAcceptsIntegralNumberForms(t *testing.T) {
	srv := setup(t)
	f := `{"currency":"BHD","minor_units":3.0,"users":[` +
		`{"id":"u1","email":"a@x.io","password":"pw","handle":"a","balance":1e3},` +
		`{"id":"u2","email":"b@x.io","password":"pw","display_name":"B","handle":"b","balance":2500.0}]}`
	if r := do(t, srv, "POST", "/_test/reset", "", f); r.status != 204 {
		t.Fatal(r.raw)
	}
	me := do(t, srv, "GET", "/me", login(t, srv, "a@x.io", "pw"), "")
	if me.body["balance"] != 1000.0 || me.body["minor_units"] != 3.0 {
		t.Fatal(me.raw)
	}
}

func TestResetSeedsPaymentsRequestsOperators(t *testing.T) {
	srv := setup(t)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 9500) + `,` + user("bob", 3000) + `],
	 "payments":[{"id":"p_1","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"note":"coffee","visibility":"public"},
	             {"id":"p_2","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"request_id":"rq_2","created_at":"2026-09-24T19:00:00+02:00"}],
	 "requests":[{"id":"rq_1","requester_id":"u_bob","payer_id":"u_ada","amount":1200,"note":"taxi","status":"pending"},
	             {"id":"rq_2","requester_id":"u_bob","payer_id":"u_ada","amount":1,"note":"","status":"paid","payment_id":"p_2"}],
	 "settlement_operator_ids":["u_ada"]}`
	if r := do(t, srv, "POST", "/_test/reset", "", f); r.status != 204 {
		t.Fatal(r.raw)
	}
	// balances are taken as given, never replayed
	me := do(t, srv, "GET", "/me", login(t, srv, "ada@example.com", "correct horse"), "")
	if me.body["balance"] != 9500.0 {
		t.Fatal(me.raw)
	}
}

func TestResetRejectsBadFixturesAndKeepsState(t *testing.T) {
	srv := setup(t)
	tok := login(t, srv, "ada@example.com", "correct horse")
	bad := map[string]string{
		"negative balance":     fixture(user("ada", -1)),
		"fractional balance":   fixture(user("ada", 1.5)),
		"string balance":       fixture(user("ada", "10")),
		"bad handle":           fixture(user("Ada", 1)),
		"long handle":          fixture(user(strings.Repeat("a", 21), 1)),
		"dup handle":           `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `,{"id":"u_x","email":"x@e.com","password":"p","handle":"ada","balance":1}]}`,
		"dup id":               `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `,{"id":"u_ada","email":"x@e.com","password":"p","handle":"x","balance":1}]}`,
		"minor_units 1":        `{"currency":"EUR","minor_units":1,"users":[]}`,
		"minor_units str":      `{"currency":"EUR","minor_units":"2","users":[]}`,
		"no currency":          `{"minor_units":2,"users":[]}`,
		"no users":             `{"currency":"EUR","minor_units":2}`,
		"users not array":      `{"currency":"EUR","minor_units":2,"users":{}}`,
		"missing password":     `{"currency":"EUR","minor_units":2,"users":[{"id":"u","email":"u@e.com","handle":"u","balance":1}]}`,
		"payment unknown user": `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `],"payments":[{"id":"p","from_user_id":"u_ada","to_user_id":"u_zz","amount":1}]}`,
		"request unknown user": `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `],"requests":[{"id":"r","requester_id":"u_zz","payer_id":"u_ada","amount":1,"status":"pending"}]}`,
		"request bad status":   `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `,` + user("bob", 1) + `],"requests":[{"id":"r","requester_id":"u_bob","payer_id":"u_ada","amount":1,"status":"open"}]}`,
		"unknown operator":     `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `],"settlement_operator_ids":["u_zz"]}`,
		"bad visibility":       `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `,` + user("bob", 1) + `],"payments":[{"id":"p","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"visibility":"secret"}]}`,
	}
	for name, f := range bad {
		t.Run(name, func(t *testing.T) {
			wantErr(t, do(t, srv, "POST", "/_test/reset", "", f), 422, "validation_failed")
		})
	}
	wantErr(t, do(t, srv, "POST", "/_test/reset", "", `{nope`), 400, "malformed_request")
	wantErr(t, do(t, srv, "POST", "/_test/reset", "", `[]`), 400, "malformed_request")
	// state is untouched: the old token and balance still work
	me := do(t, srv, "GET", "/me", tok, "")
	if me.status != 200 || me.body["balance"] != 10000.0 {
		t.Fatal(me.raw)
	}
}

func TestRoutingErrors(t *testing.T) {
	srv := setup(t)
	wantErr(t, do(t, srv, "GET", "/nope", "", ""), 404, "not_found")
	wantErr(t, do(t, srv, "POST", "/health", "", ""), 405, "method_not_allowed")
	wantErr(t, do(t, srv, "GET", "/_test/reset", "", ""), 405, "method_not_allowed")
}

func TestResetOfManyUsersAndConcurrentLoginsAreFast(t *testing.T) {
	srv := setup(t)
	users := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		// distinct passwords so the hash cache does not help
		users = append(users, fmt.Sprintf(`{"id":"u%d","email":"u%d@e.com","password":"pw-%d","handle":"u%d","balance":1}`, i, i, i, i))
	}
	start := time.Now()
	if r := do(t, srv, "POST", "/_test/reset", "", fixture(users...)); r.status != 204 {
		t.Fatal(r.raw)
	}
	// generous bounds (the spec's budgets are 10 s for reset, 5 s per request) so that
	// slow instrumented runs such as -race do not flake
	if d := time.Since(start); d > 9*time.Second {
		t.Fatalf("reset of 200 users took %v", d)
	}
	start = time.Now()
	var wg sync.WaitGroup
	fails := make(chan string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := do(t, srv, "POST", "/auth/login", "", fmt.Sprintf(`{"email":"u%d@e.com","password":"pw-%d"}`, i, i))
			if r.status != 200 {
				fails <- r.raw
			}
		}(i)
	}
	wg.Wait()
	close(fails)
	for f := range fails {
		t.Fatal(f)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("50 concurrent logins took %v", d)
	}
}
