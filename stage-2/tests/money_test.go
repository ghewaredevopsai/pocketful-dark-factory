// Work item 2 tests: payments, requests, pay/decline/cancel, listing, feed, idempotency,
// and concurrency (conservation and non-negativity under 50 parallel clients).
package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var keyN atomic.Int64

func newKey() string { return fmt.Sprintf("k-%d", keyN.Add(1)) }

// post sends a POST with a bearer token and an Idempotency-Key header (key "" sends none).
func post(t *testing.T, srv *httptest.Server, path, token, key, body string) resp {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("POST %s: %v", path, err)
		return resp{}
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	out := resp{status: r.StatusCode, header: r.Header, raw: string(b)}
	_ = json.Unmarshal(b, &out.body)
	return out
}

type world struct {
	srv          *httptest.Server
	ada, bob, cy string
}

func newWorld(t *testing.T) world {
	srv := setup(t)
	return world{srv, login(t, srv, "ada@example.com", "correct horse"),
		login(t, srv, "bob@example.com", "correct horse"), login(t, srv, "cy@example.com", "correct horse")}
}

func balance(t *testing.T, w world, tok string) int64 {
	t.Helper()
	r := do(t, w.srv, "GET", "/me", tok, "")
	return int64(r.body["balance"].(float64))
}

func total(t *testing.T, w world) int64 {
	return balance(t, w, w.ada) + balance(t, w, w.bob) + balance(t, w, w.cy)
}

func wantStatus(t *testing.T, r resp, status int) resp {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.raw)
	}
	return r
}

var stampRE = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\+00:00$`)

// ---- payments ----------------------------------------------------------------

func TestPaymentShapeAndMovesMoney(t *testing.T) {
	w := newWorld(t)
	r := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1500,"note":"dinner","visibility":"public"}`), 201)
	b := r.body
	for k, v := range map[string]any{"from_user_id": "u_ada", "from_handle": "ada", "to_user_id": "u_bob",
		"to_handle": "bob", "amount": 1500.0, "currency": "EUR", "note": "dinner", "visibility": "public"} {
		if b[k] != v {
			t.Fatalf("%s=%v want %v", k, b[k], v)
		}
	}
	if b["request_id"] != nil || b["settlement_id"] != nil {
		t.Fatalf("request_id/settlement_id must be null: %s", r.raw)
	}
	if _, ok := b["settlement_id"]; !ok {
		t.Fatal("settlement_id field missing")
	}
	if id, _ := b["payment_id"].(string); id == "" || len(id) > 64 {
		t.Fatalf("payment_id %q", id)
	}
	if !stampRE.MatchString(b["created_at"].(string)) {
		t.Fatalf("created_at %v", b["created_at"])
	}
	if balance(t, w, w.ada) != 8500 || balance(t, w, w.bob) != 4000 || total(t, w) != 13000 {
		t.Fatal("money did not move exactly once")
	}
}

func TestPaymentDefaults(t *testing.T) {
	w := newWorld(t)
	r := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1,"colour":"blue"}`), 201)
	if r.body["note"] != "" || r.body["visibility"] != "public" {
		t.Fatal(r.raw)
	}
}

func TestPaymentAmountForms(t *testing.T) {
	w := newWorld(t)
	for _, a := range []string{"1e3", "1000.0", "1000", "1E3", "10000e-1"} {
		r := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":`+a+`}`), 201)
		if r.body["amount"] != 1000.0 {
			t.Fatalf("%s -> %s", a, r.raw)
		}
	}
	for _, a := range []string{"0", "-1", "1000000001", "1.5", `"100"`, "true", "null", "1e400", "1e-400", "1e999999999", "[1]", "{}", "1e9999999999999999999"} {
		wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":`+a+`}`), 422, "validation_failed")
	}
	wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1e9}`), 409, "insufficient_funds")
	wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob"}`), 422, "validation_failed")
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestPaymentNoteAndVisibilityRules(t *testing.T) {
	w := newWorld(t)
	note := "  café 😀 <b>&amp;</b> ñ  "
	r := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), fmt.Sprintf(`{"to_handle":"bob","amount":1,"note":%q}`, note)), 201)
	if r.body["note"] != note || !strings.Contains(r.raw, "<b>&amp;</b>") {
		t.Fatalf("note not verbatim: %s", r.raw)
	}
	wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1,"note":"`+strings.Repeat("😀", 200)+`"}`), 201)
	for _, n := range []string{"null", "1", "true", `"` + strings.Repeat("x", 201) + `"`, `"` + strings.Repeat("😀", 201) + `"`} {
		wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1,"note":`+n+`}`), 422, "validation_failed")
	}
	for _, v := range []string{`"Public"`, `""`, "null", `"secret"`, "1", "true"} {
		wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1,"visibility":`+v+`}`), 422, "validation_failed")
	}
}

func TestPaymentErrors(t *testing.T) {
	w := newWorld(t)
	wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"ada","amount":1}`), 422, "self_payment")
	for _, h := range []string{"nobody", "ADA", "@ada", ""} {
		r := post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"`+h+`","amount":1}`)
		wantErr(t, r, 404, "not_found")
	}
	wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":5,"amount":1}`), 400, "malformed_request")
	wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"amount":1}`), 422, "validation_failed")
	wantErr(t, post(t, w.srv, "/payments", w.cy, newKey(), `{"to_handle":"bob","amount":501}`), 409, "insufficient_funds")
	wantStatus(t, post(t, w.srv, "/payments", w.cy, newKey(), `{"to_handle":"bob","amount":500}`), 201)
	if balance(t, w, w.cy) != 0 || total(t, w) != 13000 {
		t.Fatal("exact-balance payment")
	}
	// failed payments leave no trace in the feed
	if n := len(do(t, w.srv, "GET", "/activity", w.cy, "").body["payments"].([]any)); n != 1 {
		t.Fatalf("feed has %d", n)
	}
}

// ---- precedence and idempotency ---------------------------------------------------

func TestWritePathPrecedence(t *testing.T) {
	w := newWorld(t)
	wantErr(t, post(t, w.srv, "/payments", "", "", `nope`), 401, "unauthenticated")
	wantErr(t, post(t, w.srv, "/payments", "bad", newKey(), `{"to_handle":"bob","amount":1}`), 401, "unauthenticated")
	wantErr(t, post(t, w.srv, "/payments", w.ada, "", `nope`), 400, "missing_idempotency_key")
	wantErr(t, post(t, w.srv, "/payments", w.ada, strings.Repeat("k", 256), `nope`), 422, "validation_failed")
	wantStatus(t, post(t, w.srv, "/payments", w.ada, strings.Repeat("k", 255), `{"to_handle":"bob","amount":1}`), 201)
	wantStatus(t, post(t, w.srv, "/payments", w.ada, strings.Repeat("é", 255), `{"to_handle":"bob","amount":1}`), 201)
	for _, b := range []string{`nope`, `[]`, `"x"`, `1`, `null`, ``} {
		wantErr(t, post(t, w.srv, "/payments", w.ada, newKey(), b), 400, "malformed_request")
	}
	// empty key header is "absent or empty"
	req, _ := http.NewRequest("POST", w.srv.URL+"/payments", strings.NewReader(`{"to_handle":"bob","amount":1}`))
	req.Header.Set("Authorization", "Bearer "+w.ada)
	req.Header["Idempotency-Key"] = []string{""}
	r, _ := http.DefaultClient.Do(req)
	if r.StatusCode != 400 {
		t.Fatalf("empty key: %d", r.StatusCode)
	}
}

func TestReplayAndReuse(t *testing.T) {
	w := newWorld(t)
	k := newKey()
	first := wantStatus(t, post(t, w.srv, "/payments", w.ada, k, `{"to_handle":"bob","amount":300,"note":"x"}`), 201)
	// same JSON value: other key order, whitespace, 300.0
	replay := wantStatus(t, post(t, w.srv, "/payments", w.ada, k, ` { "note" : "x", "amount": 300.0, "to_handle":"bob" } `), 200)
	if !reflect.DeepEqual(first.body, replay.body) {
		t.Fatalf("replay differs:\n%s\n%s", first.raw, replay.raw)
	}
	wantErr(t, post(t, w.srv, "/payments", w.ada, k, `{"to_handle":"bob","amount":301,"note":"x"}`), 409, "idempotency_key_reuse")
	// an invalid body with a claimed key is still reuse (key resolved before validation)
	wantErr(t, post(t, w.srv, "/payments", w.ada, k, `{"amount":"bad"}`), 409, "idempotency_key_reuse")
	if balance(t, w, w.ada) != 9700 {
		t.Fatal("replay moved money")
	}
	// same key on another path is a different request
	wantStatus(t, post(t, w.srv, "/requests", w.ada, k, `{"payer_handle":"bob","amount":10}`), 201)
	// another user with the same key string
	wantStatus(t, post(t, w.srv, "/payments", w.bob, k, `{"to_handle":"cy","amount":300,"note":"x"}`), 201)
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestFailedKeyIsReusable(t *testing.T) {
	w := newWorld(t)
	k := newKey()
	wantErr(t, post(t, w.srv, "/payments", w.cy, k, `{"to_handle":"bob","amount":501}`), 409, "insufficient_funds")
	wantErr(t, post(t, w.srv, "/payments", w.cy, k, `{"to_handle":"bob","amount":0}`), 422, "validation_failed")
	wantStatus(t, post(t, w.srv, "/payments", w.cy, k, `{"to_handle":"bob","amount":10}`), 201)
	wantStatus(t, post(t, w.srv, "/payments", w.cy, k, `{"to_handle":"bob","amount":10}`), 200)
}

// ---- requests ------------------------------------------------------------------------

func newRequest(t *testing.T, w world, tok, payer string, amount int) string {
	t.Helper()
	r := wantStatus(t, post(t, w.srv, "/requests", tok, newKey(), fmt.Sprintf(`{"payer_handle":%q,"amount":%d}`, payer, amount)), 201)
	return r.body["request_id"].(string)
}

func TestRequestShapeAndErrors(t *testing.T) {
	w := newWorld(t)
	r := wantStatus(t, post(t, w.srv, "/requests", w.bob, newKey(), `{"payer_handle":"ada","amount":1200,"note":"taxi"}`), 201)
	for k, v := range map[string]any{"requester_id": "u_bob", "requester_handle": "bob", "payer_id": "u_ada",
		"payer_handle": "ada", "amount": 1200.0, "currency": "EUR", "note": "taxi", "status": "pending"} {
		if r.body[k] != v {
			t.Fatalf("%s=%v (%s)", k, r.body[k], r.raw)
		}
	}
	if _, ok := r.body["payment_id"]; !ok || r.body["payment_id"] != nil || !stampRE.MatchString(r.body["created_at"].(string)) {
		t.Fatal(r.raw)
	}
	// above the payer's balance is fine
	wantStatus(t, post(t, w.srv, "/requests", w.bob, newKey(), `{"payer_handle":"cy","amount":1000000000}`), 201)
	wantErr(t, post(t, w.srv, "/requests", w.bob, newKey(), `{"payer_handle":"bob","amount":1}`), 422, "self_request")
	wantErr(t, post(t, w.srv, "/requests", w.bob, newKey(), `{"payer_handle":"nobody","amount":1}`), 404, "not_found")
	wantErr(t, post(t, w.srv, "/requests", w.bob, newKey(), `{"payer_handle":"ada","amount":1,"note":null}`), 422, "validation_failed")
	wantErr(t, post(t, w.srv, "/requests", w.bob, newKey(), `{"payer_handle":"ada","amount":1.5}`), 422, "validation_failed")
	wantErr(t, post(t, w.srv, "/requests", w.bob, "", `{"payer_handle":"ada","amount":1}`), 400, "missing_idempotency_key")
	if total(t, w) != 13000 {
		t.Fatal("requests must not move money")
	}
}

func TestPayRequest(t *testing.T) {
	w := newWorld(t)
	rq := newRequest(t, w, w.bob, "ada", 1200)
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.bob, newKey(), `{}`), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.cy, newKey(), `{}`), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/requests/rq_nope/pay", w.ada, newKey(), `{}`), 404, "not_found")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, newKey(), `{"visibility":"secret"}`), 422, "validation_failed")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, "", `{}`), 400, "missing_idempotency_key")
	k := newKey()
	p := wantStatus(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, k, `{"visibility":"private"}`), 201)
	if p.body["request_id"] != rq || p.body["visibility"] != "private" || p.body["amount"] != 1200.0 ||
		p.body["from_handle"] != "ada" || p.body["to_handle"] != "bob" || p.body["note"] != "" {
		t.Fatal(p.raw)
	}
	// replay after paid: 200 original, not 409
	rp := wantStatus(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, k, `{"visibility":"private"}`), 200)
	if !reflect.DeepEqual(rp.body, p.body) {
		t.Fatal(rp.raw)
	}
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, k, `{"visibility":"public"}`), 409, "idempotency_key_reuse")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, newKey(), `{}`), 409, "request_not_pending")
	if balance(t, w, w.ada) != 8800 || balance(t, w, w.bob) != 3700 {
		t.Fatal("pay moved wrong amount")
	}
	l := do(t, w.srv, "GET", "/requests", w.bob, "").body["requests"].([]any)[0].(map[string]any)
	if l["status"] != "paid" || l["payment_id"] != p.body["payment_id"] {
		t.Fatal(l)
	}
	// private payment: parties see it, third party does not
	if len(do(t, w.srv, "GET", "/activity", w.cy, "").body["payments"].([]any)) != 0 ||
		len(do(t, w.srv, "GET", "/activity", w.bob, "").body["payments"].([]any)) != 1 {
		t.Fatal("feed contract")
	}
}

func TestEmptyBodyAndExplicitPublicDiffer(t *testing.T) {
	w := newWorld(t)
	rq := newRequest(t, w, w.bob, "ada", 100)
	k := newKey()
	p := wantStatus(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, k, `{}`), 201)
	if p.body["visibility"] != "public" {
		t.Fatal(p.raw)
	}
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, k, `{"visibility":"public"}`), 409, "idempotency_key_reuse")
}

func TestRequestBecomesPayableLater(t *testing.T) {
	w := newWorld(t)
	rq := newRequest(t, w, w.ada, "cy", 1500)
	k := newKey()
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.cy, k, `{}`), 409, "insufficient_funds")
	if balance(t, w, w.cy) != 500 {
		t.Fatal("failed pay changed balance")
	}
	l := do(t, w.srv, "GET", "/requests", w.cy, "").body["requests"].([]any)[0].(map[string]any)
	if l["status"] != "pending" {
		t.Fatal(l)
	}
	wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"cy","amount":1000}`), 201)
	wantStatus(t, post(t, w.srv, "/requests/"+rq+"/pay", w.cy, k, `{}`), 201)
	n := 0
	for _, p := range do(t, w.srv, "GET", "/activity?limit=200", w.cy, "").body["payments"].([]any) {
		if p.(map[string]any)["request_id"] == rq {
			n++
		}
	}
	if n != 1 || total(t, w) != 13000 || balance(t, w, w.cy) != 0 {
		t.Fatalf("payments for request: %d", n)
	}
}

func TestDeclineAndCancel(t *testing.T) {
	w := newWorld(t)
	rq := newRequest(t, w, w.bob, "ada", 100)
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/decline", w.bob, "", ""), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/cancel", w.ada, "", ""), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/decline", w.cy, "", ""), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/requests/nope/decline", w.ada, "", ""), 404, "not_found")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/decline", "", "", ""), 401, "unauthenticated")
	r := wantStatus(t, post(t, w.srv, "/requests/"+rq+"/decline", w.ada, "", ""), 200)
	if r.body["status"] != "declined" || r.body["request_id"] != rq {
		t.Fatal(r.raw)
	}
	wantStatus(t, post(t, w.srv, "/requests/"+rq+"/decline", w.ada, "", "garbage"), 200)
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/cancel", w.bob, "", ""), 409, "request_not_pending")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, newKey(), `{}`), 409, "request_not_pending")

	rq2 := newRequest(t, w, w.bob, "ada", 100)
	r = wantStatus(t, post(t, w.srv, "/requests/"+rq2+"/cancel", w.bob, "", ""), 200)
	if r.body["status"] != "cancelled" {
		t.Fatal(r.raw)
	}
	wantStatus(t, post(t, w.srv, "/requests/"+rq2+"/cancel", w.bob, "", ""), 200)
	wantErr(t, post(t, w.srv, "/requests/"+rq2+"/decline", w.ada, "", ""), 409, "request_not_pending")

	rq3 := newRequest(t, w, w.bob, "ada", 100)
	wantStatus(t, post(t, w.srv, "/requests/"+rq3+"/pay", w.ada, newKey(), `{}`), 201)
	wantErr(t, post(t, w.srv, "/requests/"+rq3+"/cancel", w.bob, "", ""), 409, "request_not_pending")
	wantErr(t, post(t, w.srv, "/requests/"+rq3+"/decline", w.ada, "", ""), 409, "request_not_pending")
}

// ---- listing and feed ----------------------------------------------------------------

func ids(r resp, field, id string) []string {
	var out []string
	for _, x := range r.body[field].([]any) {
		out = append(out, x.(map[string]any)[id].(string))
	}
	return out
}

func TestListRequests(t *testing.T) {
	w := newWorld(t)
	a := newRequest(t, w, w.bob, "ada", 100) // incoming for ada
	b := newRequest(t, w, w.ada, "bob", 200) // outgoing for ada
	c := newRequest(t, w, w.bob, "cy", 300)  // not ada's
	all := wantStatus(t, do(t, w.srv, "GET", "/requests", w.ada, ""), 200)
	if got := ids(all, "requests", "request_id"); !reflect.DeepEqual(got, []string{b, a}) || all.body["has_more"] != false {
		t.Fatalf("newest first, scoped: %v", got)
	}
	if got := ids(do(t, w.srv, "GET", "/requests?direction=incoming", w.ada, ""), "requests", "request_id"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatal(got)
	}
	if got := ids(do(t, w.srv, "GET", "/requests?direction=outgoing&foo=bar", w.ada, ""), "requests", "request_id"); !reflect.DeepEqual(got, []string{b}) {
		t.Fatal(got)
	}
	wantStatus(t, post(t, w.srv, "/requests/"+a+"/decline", w.ada, "", ""), 200)
	if got := ids(do(t, w.srv, "GET", "/requests?status=declined", w.ada, ""), "requests", "request_id"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatal(got)
	}
	if got := do(t, w.srv, "GET", "/requests?status=paid", w.ada, "").body["requests"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
	if got := ids(do(t, w.srv, "GET", "/requests", w.cy, ""), "requests", "request_id"); !reflect.DeepEqual(got, []string{c}) {
		t.Fatal(got)
	}
	p := do(t, w.srv, "GET", "/requests?limit=1&offset=0", w.ada, "")
	if len(p.body["requests"].([]any)) != 1 || p.body["has_more"] != true {
		t.Fatal(p.raw)
	}
	p = do(t, w.srv, "GET", "/requests?limit=1&offset=1", w.ada, "")
	if len(p.body["requests"].([]any)) != 1 || p.body["has_more"] != false {
		t.Fatal(p.raw)
	}
	p = do(t, w.srv, "GET", "/requests?offset=99", w.ada, "")
	if p.status != 200 || len(p.body["requests"].([]any)) != 0 || p.body["has_more"] != false {
		t.Fatal(p.raw)
	}
	for _, q := range []string{"direction=both", "status=open", "direction=", "limit=0", "limit=201", "limit=-5",
		"offset=-1", "offset=abc", "limit=1e9", "limit=4.0", "limit=%2B4", "limit=", "offset=", "limit=99999999999999999999"} {
		wantErr(t, do(t, w.srv, "GET", "/requests?"+q, w.ada, ""), 422, "validation_failed")
	}
	wantStatus(t, do(t, w.srv, "GET", "/requests?limit=200&offset=0", w.ada, ""), 200)
	wantErr(t, do(t, w.srv, "GET", "/requests", "", ""), 401, "unauthenticated")
}

func TestActivityFeed(t *testing.T) {
	w := newWorld(t)
	newRequest(t, w, w.bob, "ada", 100) // requests never appear in the feed
	if n := len(do(t, w.srv, "GET", "/activity", w.ada, "").body["payments"].([]any)); n != 0 {
		t.Fatal("request leaked into feed")
	}
	var want []string
	for i, vis := range []string{"public", "private", "public"} {
		r := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), fmt.Sprintf(`{"to_handle":"bob","amount":%d,"visibility":%q}`, 10+i, vis)), 201)
		want = append([]string{r.body["payment_id"].(string)}, want...)
	}
	if got := ids(do(t, w.srv, "GET", "/activity", w.bob, ""), "payments", "payment_id"); !reflect.DeepEqual(got, want) {
		t.Fatalf("newest first: %v want %v", got, want)
	}
	if got := ids(do(t, w.srv, "GET", "/activity", w.cy, ""), "payments", "payment_id"); !reflect.DeepEqual(got, []string{want[0], want[2]}) {
		t.Fatalf("third party: %v", got)
	}
	p := do(t, w.srv, "GET", "/activity?limit=2&offset=0&direction=both&status=open", w.ada, "")
	if p.status != 200 || len(p.body["payments"].([]any)) != 2 || p.body["has_more"] != true {
		t.Fatal(p.raw)
	}
	for _, q := range []string{"limit=0", "limit=201", "offset=-1", "offset=abc", "limit=1e2", "limit=%2B4", "limit=4.0"} {
		wantErr(t, do(t, w.srv, "GET", "/activity?"+q, w.ada, ""), 422, "validation_failed")
	}
}

func TestSeededPaymentsFollowFeedContract(t *testing.T) {
	srv := setup(t)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1) + `,` + user("bob", 1) + `,` + user("cy", 1) + `],
	 "payments":[{"id":"p_1","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"note":"coffee","visibility":"private"},
	             {"id":"p_2","from_user_id":"u_bob","to_user_id":"u_ada","amount":5,"created_at":"2026-09-24T19:00:00+02:00"}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	cy := login(t, srv, "cy@example.com", "correct horse")
	ada := login(t, srv, "ada@example.com", "correct horse")
	if got := ids(do(t, srv, "GET", "/activity", cy, ""), "payments", "payment_id"); !reflect.DeepEqual(got, []string{"p_2"}) {
		t.Fatal(got)
	}
	r := do(t, srv, "GET", "/activity", ada, "")
	if got := ids(r, "payments", "payment_id"); len(got) != 2 {
		t.Fatal(got)
	}
	// seeded timestamps are returned as given; new ids never collide with seeded ones
	if !strings.Contains(r.raw, `"2026-09-24T19:00:00+02:00"`) {
		t.Fatal(r.raw)
	}
}

func TestGeneratedIDsDoNotCollideWithSeeded(t *testing.T) {
	srv := setup(t)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 100) + `,` + user("bob", 0) + `],
	 "payments":[{"id":"p_1","from_user_id":"u_ada","to_user_id":"u_bob","amount":1},{"id":"p_2","from_user_id":"u_ada","to_user_id":"u_bob","amount":1}],
	 "requests":[{"id":"rq_3","requester_id":"u_bob","payer_id":"u_ada","amount":1,"status":"pending"}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	seen := map[string]bool{"p_1": true, "p_2": true, "rq_3": true}
	for i := 0; i < 5; i++ {
		r := wantStatus(t, post(t, srv, "/payments", ada, newKey(), `{"to_handle":"bob","amount":1}`), 201)
		id := r.body["payment_id"].(string)
		if seen[id] {
			t.Fatalf("id %s reused", id)
		}
		seen[id] = true
	}
	// paying the seeded request works
	wantStatus(t, post(t, srv, "/requests/rq_3/pay", ada, newKey(), `{}`), 201)
}

// ---- concurrency ---------------------------------------------------------------------

func parallel(n int, f func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			f(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

func TestConcurrentPayOfOneRequestWithDifferentKeys(t *testing.T) {
	w := newWorld(t)
	rq := newRequest(t, w, w.bob, "ada", 1000)
	codes := make([]resp, 50)
	parallel(50, func(i int) { codes[i] = post(t, w.srv, "/requests/"+rq+"/pay", w.ada, newKey(), `{}`) })
	n201, n409 := 0, 0
	for _, r := range codes {
		switch {
		case r.status == 201:
			n201++
		case r.status == 409 && strings.Contains(r.raw, "request_not_pending"):
			n409++
		default:
			t.Fatalf("unexpected %d %s", r.status, r.raw)
		}
	}
	if n201 != 1 || n409 != 49 || balance(t, w, w.ada) != 9000 || total(t, w) != 13000 {
		t.Fatalf("201=%d 409=%d", n201, n409)
	}
}

func TestConcurrentSameKey(t *testing.T) {
	w := newWorld(t)
	k := newKey()
	out := make([]resp, 50)
	parallel(50, func(i int) { out[i] = post(t, w.srv, "/payments", w.ada, k, `{"to_handle":"bob","amount":100}`) })
	n201 := 0
	var first map[string]any
	for _, r := range out {
		if r.status == 201 {
			n201++
		} else if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.raw)
		}
		if first == nil {
			first = r.body
		} else if !reflect.DeepEqual(first, r.body) {
			t.Fatal("bodies differ")
		}
	}
	if n201 != 1 || balance(t, w, w.ada) != 9900 {
		t.Fatalf("201=%d", n201)
	}
}

func TestConcurrentDrainOneWallet(t *testing.T) {
	srv := setup(t)
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", fixture(user("ada", 1000), user("bob", 0))), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	out := make([]int, 50)
	parallel(50, func(i int) {
		out[i] = post(t, srv, "/payments", ada, newKey(), `{"to_handle":"bob","amount":30}`).status
	})
	ok := 0
	for _, c := range out {
		if c == 201 {
			ok++
		} else if c != 409 {
			t.Fatalf("status %d", c)
		}
	}
	w := world{srv: srv, ada: ada, bob: login(t, srv, "bob@example.com", "correct horse")}
	if ok != 33 || balance(t, w, ada) != 10 || balance(t, w, w.bob) != 990 {
		t.Fatalf("ok=%d", ok)
	}
}

func TestConcurrentRingConservesMoney(t *testing.T) {
	w := newWorld(t)
	toks := []string{w.ada, w.bob, w.cy}
	handles := []string{"ada", "bob", "cy"}
	parallel(50, func(i int) {
		for j := 0; j < 10; j++ {
			from := (i + j) % 3
			to := (from + 1 + j%2) % 3
			r := post(t, w.srv, "/payments", toks[from], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, handles[to], 1+(i*7+j)%400))
			if r.status != 201 && r.status != 409 {
				t.Errorf("status %d %s", r.status, r.raw)
			}
			if r.status >= 500 {
				t.Errorf("5xx")
			}
		}
	})
	if got := total(t, w); got != 13000 {
		t.Fatalf("total %d", got)
	}
	for _, tok := range toks {
		if balance(t, w, tok) < 0 {
			t.Fatal("negative balance")
		}
	}
}
