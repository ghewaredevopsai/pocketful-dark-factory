// Stage-2 work item A tests: authorizations, holds, captures, void, expiry, available
// funds, fixtures, export/import (incl. a stage-1 shaped export) and concurrency.
package tests

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pocketful/app"
)

type wallet struct{ total, available, held, balance int64 }

func me(t *testing.T, srv *httptest.Server, tok string) wallet {
	t.Helper()
	r := wantStatus(t, do(t, srv, "GET", "/me", tok, ""), 200)
	f := func(k string) int64 { return int64(r.body[k].(float64)) }
	w := wallet{f("total"), f("available"), f("held"), f("balance")}
	if w.balance != w.total || w.available != w.total-w.held || w.available < 0 || w.held < 0 {
		t.Fatalf("wallet invariant broken: %+v", w)
	}
	return w
}

func authorize(t *testing.T, w world, tok, to string, amount int) resp {
	t.Helper()
	return post(t, w.srv, "/authorizations", tok, newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, to, amount))
}

func authID(t *testing.T, r resp) string {
	t.Helper()
	wantStatus(t, r, 201)
	return r.body["authorization_id"].(string)
}

func sumTotals(t *testing.T, w world) int64 {
	return me(t, w.srv, w.ada).total + me(t, w.srv, w.bob).total + me(t, w.srv, w.cy).total
}

func TestMeHasHoldFieldsWithNoHolds(t *testing.T) {
	w := newWorld(t)
	got := me(t, w.srv, w.ada)
	if got != (wallet{10000, 10000, 0, 10000}) {
		t.Fatalf("%+v", got)
	}
}

func TestAuthorizationShapeAndHold(t *testing.T) {
	w := newWorld(t)
	r := wantStatus(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"bob","amount":2000,"note":"deposit","visibility":"private"}`), 201)
	b := r.body
	for k, v := range map[string]any{"from_user_id": "u_ada", "from_handle": "ada", "to_user_id": "u_bob", "to_handle": "bob",
		"amount": 2000.0, "captured_amount": 0.0, "remaining_amount": 2000.0, "currency": "EUR", "note": "deposit",
		"visibility": "private", "status": "open"} {
		if b[k] != v {
			t.Fatalf("%s=%v (%s)", k, b[k], r.raw)
		}
	}
	if b["payment_id"] != nil || !reflect.DeepEqual(b["payment_ids"], []any{}) {
		t.Fatal(r.raw)
	}
	c, _ := time.Parse(time.RFC3339, b["created_at"].(string))
	e, _ := time.Parse(time.RFC3339, b["expires_at"].(string))
	if !stampRE.MatchString(b["expires_at"].(string)) || e.Sub(c) != 600*time.Second {
		t.Fatalf("expires_at must be created_at + 600s: %s", r.raw)
	}
	if got := me(t, w.srv, w.ada); got != (wallet{10000, 8000, 2000, 10000}) {
		t.Fatalf("%+v", got)
	}
	if me(t, w.srv, w.bob).total != 2500 {
		t.Fatal("a hold must not move money")
	}
	// not a feed item
	if n := len(do(t, w.srv, "GET", "/activity", w.ada, "").body["payments"].([]any)); n != 0 {
		t.Fatal("authorization in feed")
	}
}

func TestAuthorizationErrors(t *testing.T) {
	w := newWorld(t)
	wantErr(t, post(t, w.srv, "/authorizations", w.ada, "", `{"to_handle":"bob","amount":1}`), 400, "missing_idempotency_key")
	wantErr(t, post(t, w.srv, "/authorizations", "", newKey(), `{"to_handle":"bob","amount":1}`), 401, "unauthenticated")
	wantErr(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"ada","amount":1}`), 422, "self_payment")
	wantErr(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"nobody","amount":1}`), 404, "not_found")
	for _, a := range []string{"0", "-1", "1.5", `"5"`, "true", "1000000001"} {
		wantErr(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"bob","amount":`+a+`}`), 422, "validation_failed")
	}
	wantErr(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"bob","amount":1,"note":null}`), 422, "validation_failed")
	wantErr(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"bob","amount":1,"visibility":"x"}`), 422, "validation_failed")
	wantErr(t, post(t, w.srv, "/authorizations", w.cy, newKey(), `{"to_handle":"bob","amount":501}`), 409, "insufficient_funds")
	// held funds are not available to a second authorization
	authID(t, authorize(t, w, w.cy, "bob", 400))
	wantErr(t, post(t, w.srv, "/authorizations", w.cy, newKey(), `{"to_handle":"bob","amount":101}`), 409, "insufficient_funds")
	authID(t, authorize(t, w, w.cy, "bob", 100))
	if got := me(t, w.srv, w.cy); got.available != 0 || got.held != 500 {
		t.Fatalf("%+v", got)
	}
}

func TestHeldFundsCannotBeSpentElsewhere(t *testing.T) {
	w := operatorWorld(t)                          // cy is the operator
	authID(t, authorize(t, w, w.bob, "ada", 2000)) // bob: total 2500, available 500
	wantErr(t, post(t, w.srv, "/payments", w.bob, newKey(), `{"to_handle":"cy","amount":501}`), 409, "insufficient_funds")
	rq := newRequest(t, w, w.ada, "bob", 501)
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.bob, newKey(), `{}`), 409, "insufficient_funds")
	wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"bob","to_handle":"ada","amount":501}]}`), 409, "insufficient_funds")
	// net debit within available is fine, also when it routes through bob
	wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"bob","to_handle":"ada","amount":900},{"from_handle":"ada","to_handle":"bob","amount":400}]}`), 201)
	wantStatus(t, post(t, w.srv, "/payments", w.bob, newKey(), `{"to_handle":"cy","amount":0.0e0}`), 422)
	if got := me(t, w.srv, w.bob); got != (wallet{2000, 0, 2000, 2000}) {
		t.Fatalf("%+v", got)
	}
	if sumTotals(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestFullCaptureByDefault(t *testing.T) {
	w := newWorld(t)
	id := authID(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"bob","amount":2000,"note":"deposit","visibility":"private"}`))
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.ada, newKey(), `{}`), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.cy, newKey(), `{}`), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/authorizations/nope/capture", w.bob, newKey(), `{}`), 404, "not_found")
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, "", `{}`), 400, "missing_idempotency_key")
	k := newKey()
	p := wantStatus(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, k, `{}`), 201)
	for kk, v := range map[string]any{"authorization_id": id, "amount": 2000.0, "from_handle": "ada", "to_handle": "bob",
		"note": "deposit", "visibility": "private", "currency": "EUR"} {
		if p.body[kk] != v {
			t.Fatalf("%s=%v (%s)", kk, p.body[kk], p.raw)
		}
	}
	if p.body["request_id"] != nil || p.body["settlement_id"] != nil || p.body["payment_id"] == "" {
		t.Fatal(p.raw)
	}
	if got := me(t, w.srv, w.ada); got != (wallet{8000, 8000, 0, 8000}) {
		t.Fatalf("%+v", got)
	}
	if me(t, w.srv, w.bob).total != 4500 {
		t.Fatal("receiver not credited")
	}
	// replay: 200, same body, no money; {} vs {"amount":2000} are different bodies
	rp := wantStatus(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, k, `{}`), 200)
	if rp.raw != p.raw {
		t.Fatal("replay differs")
	}
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, k, `{"amount":2000}`), 409, "idempotency_key_reuse")
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), `{}`), 409, "authorization_not_open")
	if me(t, w.srv, w.bob).total != 4500 {
		t.Fatal("replay moved money")
	}
	// the capture payment is in the feed for the parties only (private)
	feed := do(t, w.srv, "GET", "/activity", w.ada, "").body["payments"].([]any)
	if len(feed) != 1 || feed[0].(map[string]any)["authorization_id"] != id {
		t.Fatal(feed)
	}
	if n := len(do(t, w.srv, "GET", "/activity", w.cy, "").body["payments"].([]any)); n != 0 {
		t.Fatal("private capture visible to third party")
	}
	l := wantStatus(t, do(t, w.srv, "GET", "/authorizations", w.ada, ""), 200).body["authorizations"].([]any)[0].(map[string]any)
	if l["status"] != "captured" || l["captured_amount"] != 2000.0 || l["remaining_amount"] != 0.0 || l["payment_id"] != p.body["payment_id"] {
		t.Fatal(l)
	}
	// ordinary payments carry authorization_id null
	pp := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1}`), 201)
	if v, ok := pp.body["authorization_id"]; !ok || v != nil {
		t.Fatal(pp.raw)
	}
}

func TestPartialFinalCaptureReleasesRemainder(t *testing.T) {
	w := newWorld(t)
	id := authID(t, authorize(t, w, w.ada, "bob", 2000))
	p := wantStatus(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), `{"amount":1500}`), 201)
	if p.body["amount"] != 1500.0 {
		t.Fatal(p.raw)
	}
	if got := me(t, w.srv, w.ada); got != (wallet{8500, 8500, 0, 8500}) {
		t.Fatalf("remainder not released in the same step: %+v", got)
	}
}

func TestExtendedCapture(t *testing.T) {
	w := newWorld(t)
	id := authID(t, authorize(t, w, w.ada, "bob", 2000))
	cap := func(body string) resp { return post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), body) }
	p1 := wantStatus(t, cap(`{"amount":700,"final":false}`), 201)
	if got := me(t, w.srv, w.ada); got != (wallet{9300, 8000, 1300, 9300}) {
		t.Fatalf("%+v", got)
	}
	wantErr(t, cap(`{"amount":1301,"final":false}`), 422, "capture_exceeds_authorization")
	wantErr(t, cap(`{"amount":0,"final":false}`), 422, "validation_failed")
	// above 1e9 can never fit the remainder: the capture rule, not the generic range rule
	wantErr(t, cap(`{"amount":1000000001,"final":false}`), 422, "capture_exceeds_authorization")
	wantErr(t, cap(`{"amount":1e20}`), 422, "capture_exceeds_authorization")
	wantErr(t, cap(`{"amount":-5}`), 422, "validation_failed")
	wantErr(t, cap(`{"amount":"700"}`), 422, "validation_failed")
	wantErr(t, cap(`{"amount":1.5}`), 422, "validation_failed")
	wantErr(t, cap(`{"amount":5,"final":"no"}`), 400, "malformed_request")
	p2 := wantStatus(t, cap(`{"amount":300,"final":false}`), 201)
	l := do(t, w.srv, "GET", "/authorizations", w.bob, "").body["authorizations"].([]any)[0].(map[string]any)
	if l["status"] != "open" || l["captured_amount"] != 1000.0 || l["remaining_amount"] != 1000.0 || l["payment_id"] != p2.body["payment_id"] ||
		!reflect.DeepEqual(l["payment_ids"], []any{p1.body["payment_id"], p2.body["payment_id"]}) {
		t.Fatal(l)
	}
	// omitted amount = remainder; capturing all of it closes even with final:false
	p3 := wantStatus(t, cap(`{"final":false}`), 201)
	if p3.body["amount"] != 1000.0 {
		t.Fatal(p3.raw)
	}
	l = do(t, w.srv, "GET", "/authorizations", w.bob, "").body["authorizations"].([]any)[0].(map[string]any)
	if l["status"] != "captured" || l["remaining_amount"] != 0.0 || len(l["payment_ids"].([]any)) != 3 {
		t.Fatal(l)
	}
	wantErr(t, cap(`{"amount":1}`), 409, "authorization_not_open")
	if got := me(t, w.srv, w.ada); got != (wallet{8000, 8000, 0, 8000}) || sumTotals(t, w) != 13000 {
		t.Fatalf("%+v", got)
	}
}

func TestVoid(t *testing.T) {
	w := newWorld(t)
	id := authID(t, authorize(t, w, w.ada, "bob", 2000))
	wantStatus(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), `{"amount":500,"final":false}`), 201)
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/void", w.bob, "", ""), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/void", w.cy, "", ""), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/authorizations/nope/void", w.ada, "", ""), 404, "not_found")
	r := wantStatus(t, post(t, w.srv, "/authorizations/"+id+"/void", w.ada, "", ""), 200)
	if r.body["status"] != "voided" || r.body["captured_amount"] != 500.0 || r.body["remaining_amount"] != 0.0 || len(r.body["payment_ids"].([]any)) != 1 {
		t.Fatal(r.raw)
	}
	if got := me(t, w.srv, w.ada); got != (wallet{9500, 9500, 0, 9500}) {
		t.Fatalf("%+v", got)
	}
	r2 := wantStatus(t, post(t, w.srv, "/authorizations/"+id+"/void", w.ada, "", ""), 200)
	if r2.raw != r.raw {
		t.Fatal("void twice must return the current state")
	}
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), `{}`), 409, "authorization_not_open")
	id2 := authID(t, authorize(t, w, w.ada, "bob", 10))
	wantStatus(t, post(t, w.srv, "/authorizations/"+id2+"/capture", w.bob, newKey(), `{}`), 201)
	wantErr(t, post(t, w.srv, "/authorizations/"+id2+"/void", w.ada, "", ""), 409, "authorization_not_open")
}

func ttlWorld(t *testing.T, ttl int) world {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	f := fmt.Sprintf(`{"currency":"EUR","minor_units":2,"authorization_ttl_seconds":%d,"users":[%s,%s,%s]}`,
		ttl, user("ada", 10000), user("bob", 2500), user("cy", 500))
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	return world{srv, login(t, srv, "ada@example.com", "correct horse"),
		login(t, srv, "bob@example.com", "correct horse"), login(t, srv, "cy@example.com", "correct horse")}
}

func TestExpiryWithoutTrafficAtTheDeadline(t *testing.T) {
	w := ttlWorld(t, 1)
	r := authorize(t, w, w.ada, "bob", 2000)
	id := authID(t, r)
	c, _ := time.Parse(time.RFC3339, r.body["created_at"].(string))
	e, _ := time.Parse(time.RFC3339, r.body["expires_at"].(string))
	if e.Sub(c) != time.Second {
		t.Fatal(r.raw)
	}
	idPartial := authID(t, authorize(t, w, w.ada, "bob", 1000))
	wantStatus(t, post(t, w.srv, "/authorizations/"+idPartial+"/capture", w.bob, newKey(), `{"amount":400,"final":false}`), 201)
	time.Sleep(2100 * time.Millisecond) // no request at the deadline
	if got := me(t, w.srv, w.ada); got != (wallet{9600, 9600, 0, 9600}) {
		t.Fatalf("expiry not reflected in /me: %+v", got)
	}
	ex := do(t, w.srv, "GET", "/authorizations?status=expired", w.ada, "").body["authorizations"].([]any)
	if len(ex) != 2 {
		t.Fatalf("expired list: %v", ex)
	}
	if n := len(do(t, w.srv, "GET", "/authorizations?status=open", w.ada, "").body["authorizations"].([]any)); n != 0 {
		t.Fatal("expired matches open")
	}
	for _, a := range ex {
		a := a.(map[string]any)
		if a["authorization_id"] == idPartial && (a["captured_amount"] != 400.0 || len(a["payment_ids"].([]any)) != 1 || a["remaining_amount"] != 0.0) {
			t.Fatal("expiry lost capture records", a)
		}
	}
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), `{}`), 409, "authorization_expired")
	wantErr(t, post(t, w.srv, "/authorizations/"+id+"/void", w.ada, "", ""), 409, "authorization_not_open")
	if sumTotals(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestListAuthorizations(t *testing.T) {
	w := newWorld(t)
	a := authID(t, authorize(t, w, w.ada, "bob", 10)) // outgoing for ada
	b := authID(t, authorize(t, w, w.bob, "ada", 20)) // incoming for ada
	authID(t, authorize(t, w, w.bob, "cy", 30))       // not ada's
	get := func(q string) []string {
		return ids(do(t, w.srv, "GET", "/authorizations"+q, w.ada, ""), "authorizations", "authorization_id")
	}
	if got := get(""); !reflect.DeepEqual(got, []string{b, a}) {
		t.Fatalf("newest first, scoped: %v", got)
	}
	if got := get("?direction=outgoing"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatal(got)
	}
	if got := get("?direction=incoming"); !reflect.DeepEqual(got, []string{b}) {
		t.Fatal(got)
	}
	wantStatus(t, post(t, w.srv, "/authorizations/"+a+"/void", w.ada, "", ""), 200)
	if got := get("?status=voided"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatal(got)
	}
	r := do(t, w.srv, "GET", "/authorizations?limit=1", w.ada, "")
	if len(r.body["authorizations"].([]any)) != 1 || r.body["has_more"] != true {
		t.Fatal(r.raw)
	}
	for _, q := range []string{"direction=both", "status=pending", "limit=0", "limit=201", "offset=-1", "limit=4.0"} {
		wantErr(t, do(t, w.srv, "GET", "/authorizations?"+q, w.ada, ""), 422, "validation_failed")
	}
	wantErr(t, do(t, w.srv, "GET", "/authorizations", "", ""), 401, "unauthenticated")
}

func TestSeededAuthorizations(t *testing.T) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 10000) + `,` + user("bob", 2500) + `],
	 "authorizations":[
	  {"id":"a_1","from_user_id":"u_ada","to_user_id":"u_bob","amount":2000,"note":"deposit","visibility":"public","status":"open","expires_at":"` + future + `"},
	  {"id":"a_2","from_user_id":"u_ada","to_user_id":"u_bob","amount":3000,"status":"open","expires_at":"` + past + `"},
	  {"id":"a_3","from_user_id":"u_bob","to_user_id":"u_ada","amount":100,"status":"captured","expires_at":"` + future + `"}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	bob := login(t, srv, "bob@example.com", "correct horse")
	if got := me(t, srv, ada); got != (wallet{10000, 8000, 2000, 10000}) {
		t.Fatalf("%+v", got)
	}
	l := do(t, srv, "GET", "/authorizations", ada, "")
	st := map[string]string{}
	for _, a := range l.body["authorizations"].([]any) {
		a := a.(map[string]any)
		st[a["authorization_id"].(string)] = a["status"].(string)
	}
	if !reflect.DeepEqual(st, map[string]string{"a_1": "open", "a_2": "expired", "a_3": "captured"}) {
		t.Fatal(st)
	}
	// new ids never collide with seeded ones; seeded open hold can be captured
	w := world{srv: srv, ada: ada, bob: bob}
	for i := 0; i < 4; i++ {
		id := authID(t, authorize(t, w, ada, "bob", 1))
		if id == "a_1" || id == "a_2" || id == "a_3" {
			t.Fatal("id collision", id)
		}
	}
	wantStatus(t, post(t, srv, "/authorizations/a_1/capture", bob, newKey(), `{"amount":2000}`), 201)
	if got := me(t, srv, ada); got.total != 8000 || got.held != 4 {
		t.Fatalf("%+v", got)
	}
}

func TestFixtureHoldValidation(t *testing.T) {
	srv := setup(t)
	tok := login(t, srv, "ada@example.com", "correct horse")
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	base := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1000) + `,` + user("bob", 0) + `]`
	auth := func(fields string) string { return base + `,"authorizations":[` + fields + `]}` }
	// an expired over-sized hold is fine; an unexpired one is a reset error
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", auth(`{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":5000,"status":"open","expires_at":"`+past+`"}`)), 204)
	tok = login(t, srv, "ada@example.com", "correct horse")
	bad := []string{
		auth(`{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":600,"status":"open","expires_at":"` + future + `"},{"id":"b","from_user_id":"u_ada","to_user_id":"u_bob","amount":401,"status":"open","expires_at":"` + future + `"}`),
		auth(`{"id":"a","from_user_id":"u_ada","to_user_id":"u_x","amount":1,"status":"open","expires_at":"` + future + `"}`),
		auth(`{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"status":"pending","expires_at":"` + future + `"}`),
		auth(`{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"status":"open","expires_at":"tomorrow"}`),
		auth(`{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"status":"open","expires_at":"` + future + `"},{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"status":"open","expires_at":"` + future + `"}`),
		base + `,"authorization_ttl_seconds":0}`,
		base + `,"authorization_ttl_seconds":-5}`,
		base + `,"authorization_ttl_seconds":1.5}`,
		base + `,"authorization_ttl_seconds":"600"}`,
		base + `,"authorizations":{}}`,
	}
	for i, f := range bad {
		wantErr(t, do(t, srv, "POST", "/_test/reset", "", f), 422, "validation_failed")
		_ = i
	}
	if me(t, srv, tok).total != 1000 {
		t.Fatal("rejected fixture changed state")
	}
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", base+`,"authorization_ttl_seconds":30.0}`), 204)
}

// startStageOne builds and runs the real stage-1 service (../../stage-1) as a separate
// process, so the upgrade test imports a genuine stage-1 export.
func startStageOne(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "stage1")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join("..", "..", "stage-1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build stage-1: %v %s", err, out)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; i < 100; i++ {
		if r, err := http.Get(url + "/health"); err == nil {
			r.Body.Close()
			return url
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("stage-1 did not become healthy")
	return ""
}

func TestStageOneExportImports(t *testing.T) {
	src := &httptest.Server{URL: startStageOne(t)}
	wantStatus(t, do(t, src, "POST", "/_test/reset", "", std), 204)
	ada := login(t, src, "ada@example.com", "correct horse")
	bob := login(t, src, "bob@example.com", "correct horse")
	k := newKey()
	pay := wantStatus(t, post(t, src, "/payments", ada, k, `{"to_handle":"bob","amount":100}`), 201)
	rqR := wantStatus(t, post(t, src, "/requests", bob, newKey(), `{"payer_handle":"ada","amount":50}`), 201)
	rq := rqR.body["request_id"].(string)
	exp := wantStatus(t, do(t, src, "GET", "/_test/export", "", ""), 200)
	if strings.Contains(exp.raw, "authoriz") {
		t.Fatal("stage-1 export unexpectedly mentions authorizations")
	}

	dst := httptest.NewServer(app.New())
	t.Cleanup(dst.Close)
	wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
	// the browser's stage-1 token still works; balances are the imported ones
	if got := me(t, dst, ada); got != (wallet{9900, 9900, 0, 9900}) {
		t.Fatalf("%+v", got)
	}
	// a lost-response retry recovers the original stage-1 receipt, byte for byte
	r := wantStatus(t, post(t, dst, "/payments", ada, k, `{"to_handle":"bob","amount":100}`), 200)
	if r.raw != pay.raw {
		t.Fatalf("replay after stage-1 import differs:\n%s\n%s", pay.raw, r.raw)
	}
	if me(t, dst, ada).total != 9900 {
		t.Fatal("replay moved money")
	}
	// pending request still payable; new ids do not collide; default ttl applies
	p := wantStatus(t, post(t, dst, "/requests/"+rq+"/pay", ada, newKey(), `{}`), 201)
	if p.body["payment_id"] == pay.body["payment_id"] || p.body["authorization_id"] != nil {
		t.Fatal(p.raw)
	}
	a := authorize(t, world{srv: dst}, ada, "bob", 1)
	c, _ := time.Parse(time.RFC3339, a.body["created_at"].(string))
	e, _ := time.Parse(time.RFC3339, a.body["expires_at"].(string))
	if e.Sub(c) != 600*time.Second {
		t.Fatal(a.raw)
	}
}

func TestExportImportCarriesHolds(t *testing.T) {
	w := ttlWorld(t, 900)
	id := authID(t, authorize(t, w, w.ada, "bob", 2000))
	k := newKey()
	c := wantStatus(t, post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, k, `{"amount":500,"final":false}`), 201)
	exp := wantStatus(t, do(t, w.srv, "GET", "/_test/export", "", ""), 200)
	dst := httptest.NewServer(app.New())
	t.Cleanup(dst.Close)
	wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
	if got := me(t, dst, w.ada); got != (wallet{9500, 8000, 1500, 9500}) {
		t.Fatalf("%+v", got)
	}
	r := wantStatus(t, post(t, dst, "/authorizations/"+id+"/capture", w.bob, k, `{"amount":500,"final":false}`), 200)
	if r.raw != c.raw {
		t.Fatal("capture replay after import differs")
	}
	wantStatus(t, post(t, dst, "/authorizations/"+id+"/capture", w.bob, newKey(), `{}`), 201)
	a := authorize(t, world{srv: dst}, w.ada, "bob", 1)
	cc, _ := time.Parse(time.RFC3339, a.body["created_at"].(string))
	e, _ := time.Parse(time.RFC3339, a.body["expires_at"].(string))
	if e.Sub(cc) != 900*time.Second {
		t.Fatal("ttl not carried", a.raw)
	}
	// an import whose holds exceed a balance is rejected
	var doc map[string]any
	_ = json.Unmarshal([]byte(exp.raw), &doc)
	doc["state"].(map[string]any)["authorizations"].([]any)[0].(map[string]any)["amount"] = 1_000_000_000
	b, _ := json.Marshal(doc)
	wantErr(t, do(t, dst, "POST", "/_test/import", "", string(b)), 422, "validation_failed")
}

// ---- concurrency ------------------------------------------------------------------

func TestConcurrentCapturesDifferentKeys(t *testing.T) {
	w := newWorld(t)
	id := authID(t, authorize(t, w, w.ada, "bob", 1000))
	out := make([]resp, 50)
	parallel(50, func(i int) {
		out[i] = post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), `{"amount":30,"final":false}`)
	})
	n201 := 0
	for _, r := range out {
		switch {
		case r.status == 201:
			n201++
		case r.status == 422 && strings.Contains(r.raw, "capture_exceeds_authorization"):
		case r.status == 409 && strings.Contains(r.raw, "authorization_not_open"):
		default:
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	// 33 × 30 = 990 fits; the 34th would exceed the remaining 10
	if n201 != 33 {
		t.Fatalf("201s = %d", n201)
	}
	if got := me(t, w.srv, w.ada); got != (wallet{9010, 9000, 10, 9010}) || sumTotals(t, w) != 13000 {
		t.Fatalf("%+v", got)
	}
}

func TestConcurrentCaptureSameKey(t *testing.T) {
	w := newWorld(t)
	id := authID(t, authorize(t, w, w.ada, "bob", 1000))
	k := newKey()
	out := make([]resp, 50)
	parallel(50, func(i int) { out[i] = post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, k, `{"amount":600}`) })
	n201 := 0
	for _, r := range out {
		if r.status == 201 {
			n201++
		} else if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	if n201 != 1 || me(t, w.srv, w.bob).total != 3100 || me(t, w.srv, w.ada) != (wallet{9400, 9400, 0, 9400}) {
		t.Fatalf("201s=%d", n201)
	}
}

func TestCaptureRacesVoid(t *testing.T) {
	for round := 0; round < 10; round++ {
		w := newWorld(t)
		id := authID(t, authorize(t, w, w.ada, "bob", 1000))
		var capR, voidR resp
		parallel(2, func(i int) {
			if i == 0 {
				capR = post(t, w.srv, "/authorizations/"+id+"/capture", w.bob, newKey(), `{}`)
			} else {
				voidR = post(t, w.srv, "/authorizations/"+id+"/void", w.ada, "", "")
			}
		})
		got := me(t, w.srv, w.ada)
		switch {
		case capR.status == 201 && voidR.status == 409:
			if got != (wallet{9000, 9000, 0, 9000}) {
				t.Fatalf("%+v", got)
			}
		case voidR.status == 200 && capR.status == 409:
			if got != (wallet{10000, 10000, 0, 10000}) {
				t.Fatalf("%+v", got)
			}
		default:
			t.Fatalf("capture %d, void %d", capR.status, voidR.status)
		}
	}
}

func TestAuthorizeRacesPaymentForSameFunds(t *testing.T) {
	w := newWorld(t) // cy has 500
	out := make([]int, 50)
	parallel(50, func(i int) {
		if i%2 == 0 {
			out[i] = authorize(t, w, w.cy, "bob", 100).status
		} else {
			out[i] = post(t, w.srv, "/payments", w.cy, newKey(), `{"to_handle":"ada","amount":100}`).status
		}
	})
	ok := 0
	for _, c := range out {
		if c == 201 {
			ok++
		} else if c != 409 {
			t.Fatalf("status %d", c)
		}
	}
	got := me(t, w.srv, w.cy)
	if ok != 5 || got.available != 0 || sumTotals(t, w) != 13000 {
		t.Fatalf("ok=%d %+v", ok, got)
	}
}

func TestInvariantsAtEveryReadUnderMixedHoldLoad(t *testing.T) {
	w := operatorWorld(t)
	toks := map[string]string{"ada": w.ada, "bob": w.bob, "cy": w.cy}
	names := []string{"ada", "bob", "cy"}
	parallel(50, func(i int) {
		for j := 0; j < 12; j++ {
			from := names[(i+j)%3]
			to := names[(i+j+1)%3]
			switch (i + j) % 4 {
			case 0:
				r := authorize(t, w, toks[from], to, 1+(i*13+j)%700)
				if r.status == 201 {
					id := r.body["authorization_id"].(string)
					post(t, w.srv, "/authorizations/"+id+"/capture", toks[to], newKey(), fmt.Sprintf(`{"amount":%d,"final":%v}`, 1+(i+j)%5, j%2 == 0))
					if j%3 == 0 {
						post(t, w.srv, "/authorizations/"+id+"/void", toks[from], "", "")
					}
				}
			case 1:
				post(t, w.srv, "/payments", toks[from], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, to, 1+(i*7+j)%400))
			case 2:
				post(t, w.srv, "/settlements", w.cy, newKey(), fmt.Sprintf(`{"transfers":[{"from_handle":%q,"to_handle":%q,"amount":%d}]}`, from, to, 1+(i+j)%300))
			case 3:
				// at every read: balance = total = available + held, available ≥ 0
				r := do(t, w.srv, "GET", "/me", toks[from], "")
				f := func(k string) int64 { return int64(r.body[k].(float64)) }
				if r.status != 200 || f("balance") != f("total") || f("available") != f("total")-f("held") || f("available") < 0 {
					t.Errorf("invariant broken at read: %s", r.raw)
				}
			}
		}
	})
	if sumTotals(t, w) != 13000 {
		t.Fatal("conservation")
	}
}
