// Work item 3 tests: splits (§9 rounding), settlements (§11), export/import (§10),
// and a 50-way mixed concurrent load.
package tests

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pocketful/app"
)

// ---- splits -------------------------------------------------------------------------

func shares(r resp) []int64 {
	var out []int64
	for _, s := range r.body["shares"].([]any) {
		out = append(out, int64(s.(map[string]any)["amount"].(float64)))
	}
	return out
}

func TestSplitTableRows(t *testing.T) {
	srv := setup(t)
	users := []string{}
	for _, h := range []string{"a1", "a2", "a3", "a4", "a5"} {
		users = append(users, user(h, 0))
	}
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", fixture(users...)), 204)
	tok := login(t, srv, "a1@example.com", "correct horse")
	rows := []struct {
		amount int
		n      int
		want   []int64
	}{{1000, 3, []int64{334, 333, 333}}, {1, 3, []int64{1, 0, 0}}, {10, 3, []int64{4, 3, 3}},
		{999, 3, []int64{333, 333, 333}}, {5, 5, []int64{1, 1, 1, 1, 1}}, {1000000000, 3, []int64{333333334, 333333333, 333333333}}}
	for _, row := range rows {
		hs, _ := json.Marshal([]string{"a1", "a2", "a3", "a4", "a5"}[:row.n])
		r := wantStatus(t, post(t, srv, "/splits", tok, newKey(), fmt.Sprintf(`{"amount":%d,"participant_handles":%s}`, row.amount, hs)), 201)
		if got := shares(r); !reflect.DeepEqual(got, row.want) {
			t.Fatalf("%d/%d = %v want %v", row.amount, row.n, got, row.want)
		}
		if len(r.body["requests"].([]any)) != row.n-1 {
			t.Fatalf("requests %s", r.raw)
		}
	}
}

func TestSplitShapeOrderAndZeroShares(t *testing.T) {
	w := newWorld(t)
	r := wantStatus(t, post(t, w.srv, "/splits", w.ada, newKey(), `{"amount":3000,"participant_handles":["ada","bob","cy"],"note":"dinner"}`), 201)
	b := r.body
	if b["amount"] != 3000.0 || b["currency"] != "EUR" || b["note"] != "dinner" || !stampRE.MatchString(b["created_at"].(string)) {
		t.Fatal(r.raw)
	}
	if id, _ := b["split_id"].(string); id == "" {
		t.Fatal(r.raw)
	}
	reqs := b["requests"].([]any)
	if len(reqs) != 2 || reqs[0].(map[string]any)["payer_handle"] != "bob" || reqs[1].(map[string]any)["payer_handle"] != "cy" {
		t.Fatal(r.raw)
	}
	for _, q := range reqs {
		q := q.(map[string]any)
		if q["requester_handle"] != "ada" || q["status"] != "pending" || q["note"] != "dinner" || q["amount"] != 1000.0 {
			t.Fatal(q)
		}
	}
	// caller omitted from participants, other order: extra unit moves
	r = wantStatus(t, post(t, w.srv, "/splits", w.ada, newKey(), `{"amount":1000,"participant_handles":["cy","bob","ada"]}`), 201)
	if s := r.body["shares"].([]any)[0].(map[string]any); s["handle"] != "cy" || s["amount"] != 334.0 {
		t.Fatal(r.raw)
	}
	r = wantStatus(t, post(t, w.srv, "/splits", w.ada, newKey(), `{"amount":1,"participant_handles":["bob","cy"]}`), 201)
	if got := shares(r); !reflect.DeepEqual(got, []int64{1, 0}) || len(r.body["requests"].([]any)) != 2 {
		t.Fatal(r.raw)
	}
	// only the caller: one share, no requests
	r = wantStatus(t, post(t, w.srv, "/splits", w.ada, newKey(), `{"amount":999,"participant_handles":["ada"]}`), 201)
	if !strings.Contains(r.raw, `"requests":[]`) || !reflect.DeepEqual(shares(r), []int64{999}) {
		t.Fatal(r.raw)
	}
	// split checks no balance and is not a feed item
	wantStatus(t, post(t, w.srv, "/splits", w.cy, newKey(), `{"amount":1000000,"participant_handles":["cy","ada"]}`), 201)
	if n := len(do(t, w.srv, "GET", "/activity", w.ada, "").body["payments"].([]any)); n != 0 {
		t.Fatal("split in feed")
	}
	if total(t, w) != 13000 {
		t.Fatal("split moved money")
	}
}

func TestSplitValidation(t *testing.T) {
	w := newWorld(t)
	before := len(do(t, w.srv, "GET", "/requests?limit=200", w.ada, "").body["requests"].([]any))
	cases := []struct {
		body   string
		status int
		code   string
	}{
		{`{"amount":100,"participant_handles":[]}`, 422, "validation_failed"},
		{`{"amount":100,"participant_handles":["ada","ada"]}`, 422, "validation_failed"},
		{`{"amount":100,"participant_handles":["bob","nobody"]}`, 404, "not_found"},
		{`{"amount":100}`, 422, "validation_failed"},
		{`{"amount":100,"participant_handles":"ada"}`, 400, "malformed_request"},
		{`{"amount":100,"participant_handles":["ada",1]}`, 400, "malformed_request"},
		{`{"amount":0,"participant_handles":["ada"]}`, 422, "validation_failed"},
		{`{"amount":"100","participant_handles":["ada"]}`, 422, "validation_failed"},
		{`{"amount":1.5,"participant_handles":["ada"]}`, 422, "validation_failed"},
		{`{"amount":1000000001,"participant_handles":["ada"]}`, 422, "validation_failed"},
		{`{"amount":100,"participant_handles":["ada"],"note":null}`, 422, "validation_failed"},
		{`{"amount":100,"participant_handles":["ada"],"note":"` + strings.Repeat("x", 201) + `"}`, 422, "validation_failed"},
	}
	for _, c := range cases {
		wantErr(t, post(t, w.srv, "/splits", w.ada, newKey(), c.body), c.status, c.code)
	}
	hs := make([]string, 1000)
	for i := range hs {
		hs[i] = fmt.Sprintf("h%d", i)
	}
	b, _ := json.Marshal(map[string]any{"amount": 100, "participant_handles": hs})
	wantErr(t, post(t, w.srv, "/splits", w.ada, newKey(), string(b)), 404, "not_found")
	wantErr(t, post(t, w.srv, "/splits", w.ada, "", `{"amount":100,"participant_handles":["ada"]}`), 400, "missing_idempotency_key")
	if after := len(do(t, w.srv, "GET", "/requests?limit=200", w.ada, "").body["requests"].([]any)); after != before {
		t.Fatal("failed split created requests")
	}
}

func TestSplitReplayAndPaidInFullConserves(t *testing.T) {
	w := newWorld(t)
	k := newKey()
	first := wantStatus(t, post(t, w.srv, "/splits", w.ada, k, `{"amount":1000,"participant_handles":["ada","bob","cy"]}`), 201)
	replay := wantStatus(t, post(t, w.srv, "/splits", w.ada, k, `{"participant_handles":["ada","bob","cy"],"amount":1000}`), 200)
	if first.raw != replay.raw {
		t.Fatal("replay differs")
	}
	wantErr(t, post(t, w.srv, "/splits", w.ada, k, `{"amount":1001,"participant_handles":["ada","bob","cy"]}`), 409, "idempotency_key_reuse")
	if n := len(do(t, w.srv, "GET", "/requests", w.bob, "").body["requests"].([]any)); n != 1 {
		t.Fatalf("replay created requests: %d", n)
	}
	for _, q := range first.body["requests"].([]any) {
		q := q.(map[string]any)
		tok := map[string]string{"bob": w.bob, "cy": w.cy}[q["payer_handle"].(string)]
		wantStatus(t, post(t, w.srv, "/requests/"+q["request_id"].(string)+"/pay", tok, newKey(), `{}`), 201)
	}
	if total(t, w) != 13000 || balance(t, w, w.ada) != 10666 {
		t.Fatal("conservation after split paid in full")
	}
}

// ---- settlements ----------------------------------------------------------------------

func operatorWorld(t *testing.T) world {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 10000) + `,` + user("bob", 2500) + `,` + user("cy", 500) +
		`],"settlement_operator_ids":["u_cy"]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	return world{srv, login(t, srv, "ada@example.com", "correct horse"),
		login(t, srv, "bob@example.com", "correct horse"), login(t, srv, "cy@example.com", "correct horse")}
}

func TestSettlementHappyPath(t *testing.T) {
	w := operatorWorld(t)
	k := newKey()
	body := `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":100,"x":1},
	                      {"from_handle":"bob","to_handle":"ada","amount":50,"note":"back","visibility":"private"}],"y":2}`
	r := wantStatus(t, post(t, w.srv, "/settlements", w.cy, k, body), 201)
	sid, _ := r.body["settlement_id"].(string)
	at, _ := r.body["committed_at"].(string)
	ps := r.body["payments"].([]any)
	if sid == "" || !stampRE.MatchString(at) || len(ps) != 2 {
		t.Fatal(r.raw)
	}
	for i, want := range []map[string]any{
		{"from_handle": "ada", "to_handle": "bob", "amount": 100.0, "note": "", "visibility": "public"},
		{"from_handle": "bob", "to_handle": "ada", "amount": 50.0, "note": "back", "visibility": "private"}} {
		p := ps[i].(map[string]any)
		for k, v := range want {
			if p[k] != v {
				t.Fatalf("payment %d %s=%v", i, k, p[k])
			}
		}
		if p["settlement_id"] != sid || p["request_id"] != nil || p["created_at"] != at || p["currency"] != "EUR" {
			t.Fatal(p)
		}
	}
	if balance(t, w, w.ada) != 9950 || balance(t, w, w.bob) != 2550 || total(t, w) != 13000 {
		t.Fatal("balances")
	}
	replay := wantStatus(t, post(t, w.srv, "/settlements", w.cy, k, body), 200)
	if replay.raw != r.raw || balance(t, w, w.ada) != 9950 {
		t.Fatal("replay")
	}
	// members follow ordinary feed visibility: the operator (cy) sees only the public one
	feed := do(t, w.srv, "GET", "/activity", w.cy, "").body["payments"].([]any)
	if len(feed) != 1 || feed[0].(map[string]any)["visibility"] != "public" {
		t.Fatal(feed)
	}
	// non-member payments expose settlement_id null
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1}`), 201)
	if v, ok := p.body["settlement_id"]; !ok || v != nil {
		t.Fatal(p.raw)
	}
}

func TestSettlementNetAffordability(t *testing.T) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("op", 0) + `,` + user("ada", 100) + `,` + user("bob", 0) + `,` + user("cy", 0) +
		`],"settlement_operator_ids":["u_op"]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	op := login(t, srv, "op@example.com", "correct horse")
	// bob is empty but nets to zero: the chain is affordable regardless of order
	wantStatus(t, post(t, srv, "/settlements", op, newKey(), `{"transfers":[
		{"from_handle":"bob","to_handle":"cy","amount":100},{"from_handle":"ada","to_handle":"bob","amount":100}]}`), 201)
	// now cy holds 100; a batch leaving anyone negative fails as a whole
	k := newKey()
	body := `{"transfers":[{"from_handle":"cy","to_handle":"ada","amount":100},{"from_handle":"bob","to_handle":"ada","amount":1}]}`
	wantErr(t, post(t, srv, "/settlements", op, k, body), 409, "insufficient_funds")
	cy := login(t, srv, "cy@example.com", "correct horse")
	if r := do(t, srv, "GET", "/me", cy, ""); r.body["balance"] != 100.0 {
		t.Fatal("failed settlement moved money")
	}
	// the failed key claimed nothing
	wantStatus(t, post(t, srv, "/settlements", op, k, `{"transfers":[{"from_handle":"cy","to_handle":"ada","amount":100}]}`), 201)
}

func TestSettlementErrors(t *testing.T) {
	w := operatorWorld(t)
	tr := func(entries ...string) string { return `{"transfers":[` + strings.Join(entries, ",") + `]}` }
	ok := `{"from_handle":"ada","to_handle":"bob","amount":1}`
	wantErr(t, post(t, w.srv, "/settlements", "", newKey(), tr(ok)), 401, "unauthenticated")
	wantErr(t, post(t, w.srv, "/settlements", w.ada, newKey(), tr(ok)), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/settlements", w.ada, "", tr(ok)), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/settlements", w.cy, "", tr(ok)), 400, "missing_idempotency_key")
	wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), `[]`), 400, "malformed_request")
	many := make([]string, 33)
	for i := range many {
		many[i] = ok
	}
	for _, b := range []string{`{}`, `{"transfers":[]}`, `{"transfers":{}}`, `{"transfers":"x"}`, tr(many...), tr(`1`),
		tr(`{"to_handle":"bob","amount":1}`), tr(`{"from_handle":1,"to_handle":"bob","amount":1}`)} {
		wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), b), 422, "validation_failed")
	}
	wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), tr(many[:32]...)), 201)
	for _, e := range []string{`{"from_handle":"ada","to_handle":"bob","amount":0}`, `{"from_handle":"ada","to_handle":"bob","amount":"1"}`,
		`{"from_handle":"ada","to_handle":"bob","amount":1,"note":null}`, `{"from_handle":"ada","to_handle":"bob","amount":1,"visibility":"x"}`} {
		wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), tr(ok, e)), 422, "validation_failed")
	}
	wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), tr(`{"from_handle":"ada","to_handle":"nobody","amount":1}`)), 404, "not_found")
	wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), tr(`{"from_handle":"ada","to_handle":"ada","amount":1}`)), 422, "self_payment")
	// entry errors in input order, all before insufficient funds
	huge := `{"from_handle":"cy","to_handle":"ada","amount":1000000}`
	wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), tr(huge, `{"from_handle":"ada","to_handle":"nobody","amount":1}`, `{"from_handle":"bob","to_handle":"bob","amount":1}`)), 404, "not_found")
	wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), tr(huge, `{"from_handle":"bob","to_handle":"bob","amount":1}`, `{"from_handle":"ada","to_handle":"nobody","amount":1}`)), 422, "self_payment")
	wantErr(t, post(t, w.srv, "/settlements", w.cy, newKey(), tr(huge, ok)), 409, "insufficient_funds")
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestOperatorGetsNoExtraAccess(t *testing.T) {
	w := operatorWorld(t)
	rq := newRequest(t, w, w.bob, "ada", 10)
	wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1,"visibility":"private"}`), 201)
	if n := len(do(t, w.srv, "GET", "/requests", w.cy, "").body["requests"].([]any)); n != 0 {
		t.Fatal("operator sees others' requests")
	}
	if n := len(do(t, w.srv, "GET", "/activity", w.cy, "").body["payments"].([]any)); n != 0 {
		t.Fatal("operator sees private payments")
	}
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/pay", w.cy, newKey(), `{}`), 403, "forbidden")
	wantErr(t, post(t, w.srv, "/requests/"+rq+"/cancel", w.cy, "", ""), 403, "forbidden")
}

func TestConcurrentSettlementsConserve(t *testing.T) {
	w := operatorWorld(t)
	k := newKey()
	out := make([]resp, 50)
	parallel(50, func(i int) {
		key := newKey()
		if i%2 == 0 {
			key = k
		}
		out[i] = post(t, w.srv, "/settlements", w.cy, key, `{"transfers":[{"from_handle":"bob","to_handle":"ada","amount":2500},{"from_handle":"ada","to_handle":"cy","amount":100}]}`)
	})
	n201 := 0
	for _, r := range out {
		if r.status == 201 {
			n201++
		} else if r.status != 200 && r.status != 409 {
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	// bob can afford it once only (2500 -> 0)
	if n201 != 1 || balance(t, w, w.bob) != 0 || total(t, w) != 13000 {
		t.Fatalf("201=%d", n201)
	}
}

// ---- export / import ------------------------------------------------------------------

func TestExportImportRoundTripAcrossProcesses(t *testing.T) {
	src := operatorWorld(t)
	// build up interesting state: payment, pay, split, settlement, a failed key
	payKey := newKey()
	pay := wantStatus(t, post(t, src.srv, "/payments", src.ada, payKey, `{"to_handle":"bob","amount":300,"note":"x","visibility":"private"}`), 201)
	rq := newRequest(t, src, src.bob, "ada", 200)
	rqKey := newKey()
	paid := wantStatus(t, post(t, src.srv, "/requests/"+rq+"/pay", src.ada, rqKey, `{}`), 201)
	splitKey := newKey()
	split := wantStatus(t, post(t, src.srv, "/splits", src.ada, splitKey, `{"amount":10,"participant_handles":["ada","bob","cy"]}`), 201)
	stKey := newKey()
	settle := wantStatus(t, post(t, src.srv, "/settlements", src.cy, stKey, `{"transfers":[{"from_handle":"ada","to_handle":"cy","amount":5}]}`), 201)
	failKey := newKey()
	wantErr(t, post(t, src.srv, "/payments", src.cy, failKey, `{"to_handle":"bob","amount":999999}`), 409, "insufficient_funds")
	feedBefore := do(t, src.srv, "GET", "/activity", src.ada, "").raw
	reqsBefore := do(t, src.srv, "GET", "/requests", src.bob, "").raw

	exp := wantStatus(t, do(t, src.srv, "GET", "/_test/export", "", ""), 200)
	if exp.body["track"] != "pocketful" || exp.body["format_version"] != 1.0 {
		t.Fatal(exp.raw[:200])
	}
	if strings.Contains(exp.raw, "correct horse") {
		t.Fatal("plaintext password in export")
	}
	// snapshot is not affected by later writes on the source
	wantStatus(t, post(t, src.srv, "/payments", src.ada, newKey(), `{"to_handle":"bob","amount":1}`), 201)
	exp2 := do(t, src.srv, "GET", "/_test/export", "", "")
	if exp2.raw == exp.raw {
		t.Fatal("export did not see the new write")
	}

	// a different process: fresh server, different port, with its own unrelated data
	dst := httptest.NewServer(app.New())
	t.Cleanup(dst.Close)
	wantStatus(t, do(t, dst, "POST", "/_test/reset", "", fixture(user("zed", 5))), 204)
	zed := login(t, dst, "zed@example.com", "correct horse")
	for i := 0; i < 2; i++ { // repeated import is replacement, not merge
		wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
	}
	wantErr(t, do(t, dst, "GET", "/me", zed, ""), 401, "unauthenticated") // old credentials gone
	wantErr(t, do(t, dst, "POST", "/auth/login", "", `{"email":"zed@example.com","password":"correct horse"}`), 401, "unauthenticated")

	// existing tokens, balances, feed, requests survive unchanged
	if got := do(t, dst, "GET", "/activity", src.ada, "").raw; got != feedBefore {
		t.Fatalf("feed changed:\n%s\n%s", got, feedBefore)
	}
	if got := do(t, dst, "GET", "/requests", src.bob, "").raw; got != reqsBefore {
		t.Fatal("requests changed")
	}
	// hashed-password login works
	login(t, dst, "ada@example.com", "correct horse")
	// every retry returns its original receipt with 200 and moves nothing
	adaBefore := do(t, dst, "GET", "/me", src.ada, "").body["balance"]
	for _, c := range []struct {
		path, tok, key, body string
		orig                 resp
	}{
		{"/payments", src.ada, payKey, `{"to_handle":"bob","amount":300,"note":"x","visibility":"private"}`, pay},
		{"/requests/" + rq + "/pay", src.ada, rqKey, `{}`, paid},
		{"/splits", src.ada, splitKey, `{"amount":10,"participant_handles":["ada","bob","cy"]}`, split},
		{"/settlements", src.cy, stKey, `{"transfers":[{"from_handle":"ada","to_handle":"cy","amount":5}]}`, settle},
	} {
		r := wantStatus(t, post(t, dst, c.path, c.tok, c.key, c.body), 200)
		if r.raw != c.orig.raw {
			t.Fatalf("%s replay differs after import", c.path)
		}
	}
	if do(t, dst, "GET", "/me", src.ada, "").body["balance"] != adaBefore {
		t.Fatal("replay after import moved money")
	}
	// failed key is still reusable; new ids never collide with imported ones
	r := wantStatus(t, post(t, dst, "/payments", src.cy, failKey, `{"to_handle":"bob","amount":1}`), 201)
	if strings.Contains(feedBefore, `"`+r.body["payment_id"].(string)+`"`) {
		t.Fatal("generated id collides with imported id")
	}
	w := world{dst, src.ada, src.bob, src.cy}
	if total(t, w) != 13000 {
		t.Fatal("conservation after import")
	}
	// reset clears imported state
	wantStatus(t, do(t, dst, "POST", "/_test/reset", "", std), 204)
	wantErr(t, do(t, dst, "GET", "/me", src.ada, ""), 401, "unauthenticated")
}

func TestImportRejectsInvalidAndKeepsState(t *testing.T) {
	w := newWorld(t)
	exp := wantStatus(t, do(t, w.srv, "GET", "/_test/export", "", ""), 200)
	var doc map[string]any
	_ = json.Unmarshal([]byte(exp.raw), &doc)
	mutate := func(f func(d map[string]any)) string {
		var d map[string]any
		_ = json.Unmarshal([]byte(exp.raw), &d)
		f(d)
		b, _ := json.Marshal(d)
		return string(b)
	}
	stateOf := func(d map[string]any) map[string]any { return d["state"].(map[string]any) }
	firstUser := func(d map[string]any) map[string]any { return stateOf(d)["users"].([]any)[0].(map[string]any) }
	bad := map[string]string{
		"no track":       mutate(func(d map[string]any) { delete(d, "track") }),
		"wrong track":    mutate(func(d map[string]any) { d["track"] = "tablekeeper" }),
		"no version":     mutate(func(d map[string]any) { delete(d, "format_version") }),
		"version 2":      mutate(func(d map[string]any) { d["format_version"] = 2 }),
		"version string": mutate(func(d map[string]any) { d["format_version"] = "1" }),
		"no state":       mutate(func(d map[string]any) { delete(d, "state") }),
		"state not obj":  mutate(func(d map[string]any) { d["state"] = []any{} }),
		"state empty":    mutate(func(d map[string]any) { d["state"] = map[string]any{} }),
		"neg balance":    mutate(func(d map[string]any) { firstUser(d)["balance"] = -1 }),
		"bad handle":     mutate(func(d map[string]any) { firstUser(d)["handle"] = "BAD" }),
		"plain password": mutate(func(d map[string]any) { firstUser(d)["password_hash"] = "correct horse" }),
		"dup user":       mutate(func(d map[string]any) { s := stateOf(d); s["users"] = append(s["users"].([]any), firstUser(d)) }),
		"dangling token": mutate(func(d map[string]any) { stateOf(d)["tokens"].(map[string]any)["t_x"] = "u_nobody" }),
		"unknown field":  mutate(func(d map[string]any) { stateOf(d)["surprise"] = 1 }),
		"minor units 5":  mutate(func(d map[string]any) { stateOf(d)["minor_units"] = 5 }),
		"payment dangles": mutate(func(d map[string]any) {
			stateOf(d)["payments"] = []any{map[string]any{"id": "p", "from_user_id": "u_ada", "to_user_id": "u_x", "amount": 1, "visibility": "public", "created_at": "2026-01-01T00:00:00+00:00"}}
		}),
		"bad status": mutate(func(d map[string]any) {
			stateOf(d)["requests"] = []any{map[string]any{"id": "r", "requester_id": "u_ada", "payer_id": "u_bob", "amount": 1, "status": "open", "created_at": "2026-01-01T00:00:00+00:00"}}
		}),
	}
	for name, b := range bad {
		t.Run(name, func(t *testing.T) {
			wantErr(t, do(t, w.srv, "POST", "/_test/import", "", b), 422, "validation_failed")
		})
	}
	wantErr(t, do(t, w.srv, "POST", "/_test/import", "", `{nope`), 400, "malformed_request")
	wantErr(t, do(t, w.srv, "POST", "/_test/import", "", `[1]`), 400, "malformed_request")
	if r := do(t, w.srv, "GET", "/me", w.ada, ""); r.status != 200 || r.body["balance"] != 10000.0 {
		t.Fatal("destination changed by a rejected import")
	}
	wantStatus(t, do(t, w.srv, "POST", "/_test/import", "", exp.raw), 204)
}

// ---- mixed load ----------------------------------------------------------------------

func TestMixedConcurrentLoad(t *testing.T) {
	w := operatorWorld(t)
	toks := map[string]string{"ada": w.ada, "bob": w.bob, "cy": w.cy}
	names := []string{"ada", "bob", "cy"}
	var mu sync.Mutex
	var reqIDs []struct{ id, payer string }
	var slow, bad int
	parallel(50, func(i int) {
		rng := rand.New(rand.NewSource(int64(i)))
		for j := 0; j < 20; j++ {
			me := names[rng.Intn(3)]
			other := names[(indexOf(names, me)+1+rng.Intn(2))%3]
			start := time.Now()
			var r resp
			switch rng.Intn(7) {
			case 0:
				r = post(t, w.srv, "/payments", toks[me], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, other, 1+rng.Intn(800)))
			case 1:
				r = post(t, w.srv, "/requests", toks[me], newKey(), fmt.Sprintf(`{"payer_handle":%q,"amount":%d}`, other, 1+rng.Intn(800)))
				if r.status == 201 {
					mu.Lock()
					reqIDs = append(reqIDs, struct{ id, payer string }{r.body["request_id"].(string), other})
					mu.Unlock()
				}
			case 2:
				mu.Lock()
				var id, payer string
				if len(reqIDs) > 0 {
					x := reqIDs[rng.Intn(len(reqIDs))]
					id, payer = x.id, x.payer
				}
				mu.Unlock()
				if id != "" {
					r = post(t, w.srv, "/requests/"+id+"/pay", toks[payer], newKey(), `{}`)
				}
			case 3:
				r = post(t, w.srv, "/splits", toks[me], newKey(), fmt.Sprintf(`{"amount":%d,"participant_handles":["ada","bob","cy"]}`, 1+rng.Intn(1000)))
			case 4:
				r = post(t, w.srv, "/settlements", w.cy, newKey(), fmt.Sprintf(`{"transfers":[{"from_handle":%q,"to_handle":%q,"amount":%d},{"from_handle":%q,"to_handle":%q,"amount":%d}]}`,
					me, other, 1+rng.Intn(500), other, me, 1+rng.Intn(500)))
			case 5:
				r = do(t, w.srv, "GET", "/activity?limit=200", toks[me], "")
			case 6:
				r = do(t, w.srv, "GET", "/requests?limit=200", toks[me], "")
			}
			if time.Since(start) > 5*time.Second {
				mu.Lock()
				slow++
				mu.Unlock()
			}
			if r.status >= 500 || (r.status != 0 && r.status != 200 && r.status != 201 && r.status != 409) {
				mu.Lock()
				bad++
				mu.Unlock()
				t.Errorf("unexpected %d %s", r.status, r.raw)
			}
		}
	})
	if slow != 0 || bad != 0 {
		t.Fatalf("slow=%d bad=%d", slow, bad)
	}
	if got := total(t, w); got != 13000 {
		t.Fatalf("total %d", got)
	}
	for _, tok := range toks {
		if balance(t, w, tok) < 0 {
			t.Fatal("negative balance")
		}
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
