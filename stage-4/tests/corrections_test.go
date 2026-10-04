// Stage-3 work item B tests: POST /payments/{id}/corrections — validation, precedence,
// idempotency, money movement, historical overdraft (incl. holds and same-instant
// boundaries), linked payments, views after corrections, imports, and 50-way races.
package tests

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"pocketful/app"
)

func correct(t *testing.T, srv *httptest.Server, tok, pid, key, body string) resp {
	t.Helper()
	return post(t, srv, "/payments/"+pid+"/corrections", tok, key, body)
}

func corr(exp int, amount int64, eff, reason string) string {
	return fmt.Sprintf(`{"expected_revision":%d,"amount":%d,"effective_at":%q,"reason":%q}`, exp, amount, eff, reason)
}

// worldWith resets to the given users and seeded payments and logs everyone in.
func worldWith(t *testing.T, users []string, payments string, extra string) (*httptest.Server, map[string]string) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	var us []string
	for _, u := range users {
		parts := strings.SplitN(u, ":", 2)
		var bal int64
		fmt.Sscan(parts[1], &bal)
		us = append(us, user(parts[0], bal))
	}
	f := `{"currency":"EUR","minor_units":2,"users":[` + strings.Join(us, ",") + `],"payments":[` + payments + `]` + extra + `}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	tok := map[string]string{}
	for _, u := range users {
		h := strings.SplitN(u, ":", 2)[0]
		tok[h] = login(t, srv, h+"@example.com", "correct horse")
	}
	return srv, tok
}

func bal(t *testing.T, srv *httptest.Server, tok string, q string) int64 {
	t.Helper()
	return num(wantStatus(t, do(t, srv, "GET", "/me"+q, tok, ""), 200).body["balance"])
}

func TestCorrectionSpecExample(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:9500", "bob:3000"},
		`{"id":"p_1","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"note":"coffee","created_at":"2026-09-21T10:00:00+00:00"}`, "")
	k := newKey()
	r := wantStatus(t, correct(t, srv, tk["ada"], "p_1", k, corr(1, 400, "2026-09-20T12:00:00+00:00", "corrected amount")), 201)
	for kk, v := range map[string]any{"payment_id": "p_1", "revision": 2.0, "amount": 400.0, "effective_at": "2026-09-20T12:00:00+00:00", "reason": "corrected amount"} {
		if r.body[kk] != v {
			t.Fatalf("%s=%v (%s)", kk, r.body[kk], r.raw)
		}
	}
	recorded := r.body["recorded_at"].(string)
	if !stampRE.MatchString(recorded) || !strings.Contains(recorded, ".") {
		t.Fatal(r.raw)
	}
	// decrease debits the receiver: ada +100, bob -100; the sum is unchanged
	if bal(t, srv, tk["ada"], "") != 9600 || bal(t, srv, tk["bob"], "") != 2900 {
		t.Fatal("balances")
	}
	// revisions list: revision 1 (reason "") and 2
	revs := wantStatus(t, do(t, srv, "GET", "/payments/p_1/revisions", tk["bob"], ""), 200).body["revisions"].([]any)
	if len(revs) != 2 || revs[0].(map[string]any)["reason"] != "" || num(revs[0].(map[string]any)["amount"]) != 500 ||
		!reflect.DeepEqual(revs[1], r.body) {
		t.Fatal(revs)
	}
	// /activity shows the original payment, and no correction record
	feed := wantStatus(t, do(t, srv, "GET", "/activity", tk["ada"], ""), 200).body["payments"].([]any)
	if len(feed) != 1 || num(feed[0].(map[string]any)["amount"]) != 500 || feed[0].(map[string]any)["created_at"] != "2026-09-21T10:00:00+00:00" {
		t.Fatal(feed)
	}
	// statement: selected revision 2, at its effective time, amount 400
	s := wantStatus(t, do(t, srv, "GET", "/statement", tk["ada"], ""), 200)
	e := s.body["entries"].([]any)[0].(map[string]any)
	if num(e["revision"]) != 2 || e["effective_at"] != "2026-09-20T12:00:00+00:00" || e["recorded_at"] != recorded ||
		num(e["delta"]) != -400 || num(e["payment"].(map[string]any)["amount"]) != 400 || num(s.body["opening_balance"]) != 10000 {
		t.Fatal(s.raw)
	}
	// known_at before the correction: the original revision, at its original time
	before := "2026-09-25T00:00:00Z"
	if bal(t, srv, tk["ada"], qs("known_at", before)) != 9500 {
		t.Fatal("known_at before the correction")
	}
	if bal(t, srv, tk["ada"], qs("as_of", "2026-09-21T00:00:00Z")) != 9600 || bal(t, srv, tk["ada"], qs("as_of", "2026-09-21T00:00:00Z", "known_at", before)) != 10000 {
		t.Fatal("as_of between the corrected and the original effective time")
	}
	ks := wantStatus(t, do(t, srv, "GET", "/statement"+qs("known_at", before), tk["ada"], ""), 200)
	ke := ks.body["entries"].([]any)[0].(map[string]any)
	if num(ke["revision"]) != 1 || num(ke["delta"]) != -500 || num(ks.body["closing_balance"]) != 9500 {
		t.Fatal(ks.raw)
	}
	// replay: 200 with the original revision even after a newer revision
	wantStatus(t, correct(t, srv, tk["ada"], "p_1", newKey(), corr(2, 450, "2026-09-20T12:00:00Z", "again")), 201)
	rp := wantStatus(t, correct(t, srv, tk["ada"], "p_1", k, corr(1, 400, "2026-09-20T12:00:00+00:00", "corrected amount")), 200)
	if rp.raw != r.raw {
		t.Fatal("replay must return the original revision")
	}
	wantErr(t, correct(t, srv, tk["ada"], "p_1", k, corr(1, 401, "2026-09-20T12:00:00+00:00", "corrected amount")), 409, "idempotency_key_reuse")
	wantErr(t, correct(t, srv, tk["ada"], "p_1", k, `{"amount":"bad"}`), 409, "idempotency_key_reuse") // claimed key beats validation
	if bal(t, srv, tk["ada"], "") != 9550 {
		t.Fatal("replays moved money")
	}
}

func TestCorrectionIncreaseDecreaseReversal(t *testing.T) {
	w := newWorld(t)
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1000,"visibility":"private","note":"rent"}`), 201)
	id, at := p.body["payment_id"].(string), p.body["created_at"].(string)
	wantStatus(t, correct(t, w.srv, w.ada, id, newKey(), corr(1, 1500, at, "more")), 201) // increase debits the sender
	if me(t, w.srv, w.ada).total != 8500 || me(t, w.srv, w.bob).total != 4000 {
		t.Fatal("increase")
	}
	wantStatus(t, correct(t, w.srv, w.ada, id, newKey(), corr(2, 200, at, "less")), 201) // decrease debits the receiver
	if me(t, w.srv, w.ada).total != 9800 || me(t, w.srv, w.bob).total != 2700 {
		t.Fatal("decrease")
	}
	z := wantStatus(t, correct(t, w.srv, w.ada, id, newKey(), corr(3, 0, at, "reverse")), 201) // zero reverses it
	if num(z.body["amount"]) != 0 || me(t, w.srv, w.ada).total != 10000 || me(t, w.srv, w.bob).total != 2500 {
		t.Fatal("reversal")
	}
	// zero-amount revision: still an entry, zero delta; never counted with the one it replaces
	s := wantStatus(t, do(t, w.srv, "GET", "/statement", w.bob, ""), 200)
	es := s.body["entries"].([]any)
	if len(es) != 1 || num(es[0].(map[string]any)["delta"]) != 0 || num(es[0].(map[string]any)["revision"]) != 4 ||
		num(s.body["opening_balance"]) != 2500 || num(s.body["closing_balance"]) != 2500 {
		t.Fatal(s.raw)
	}
	// parties and visibility unchanged: cy still cannot see it or its revisions
	if n := len(wantStatus(t, do(t, w.srv, "GET", "/activity", w.cy, ""), 200).body["payments"].([]any)); n != 0 {
		t.Fatal("visibility changed")
	}
	wantErr(t, do(t, w.srv, "GET", "/payments/"+id+"/revisions", w.cy, ""), 404, "not_found")
	if n := len(wantStatus(t, do(t, w.srv, "GET", "/payments/"+id+"/revisions", w.bob, ""), 200).body["revisions"].([]any)); n != 4 {
		t.Fatal(n)
	}
	// a correction can be corrected back up from zero (debits the sender again)
	wantStatus(t, correct(t, w.srv, w.ada, id, newKey(), corr(4, 10, at, "small")), 201)
	if me(t, w.srv, w.ada).total != 9990 {
		t.Fatal("from zero")
	}
}

func TestCorrectionValidationAndPrecedence(t *testing.T) {
	w := operatorWorld(t) // cy is the operator
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":100}`), 201)
	id, at := p.body["payment_id"].(string), p.body["created_at"].(string)
	ok := corr(1, 50, at, "fix")
	wantErr(t, correct(t, w.srv, "", id, newKey(), ok), 401, "unauthenticated")
	wantErr(t, correct(t, w.srv, w.ada, id, "", ok), 400, "missing_idempotency_key")
	wantErr(t, correct(t, w.srv, w.ada, id, strings.Repeat("k", 256), ok), 422, "validation_failed")
	wantErr(t, correct(t, w.srv, w.ada, id, newKey(), `[1]`), 400, "malformed_request")
	wantErr(t, correct(t, w.srv, w.ada, "nope", newKey(), ok), 404, "not_found")
	wantErr(t, correct(t, w.srv, w.bob, id, newKey(), ok), 403, "forbidden") // the receiver
	wantErr(t, correct(t, w.srv, w.cy, id, newKey(), ok), 403, "forbidden")  // an operator
	wantErr(t, correct(t, w.srv, w.bob, "nope", newKey(), `{}`), 404, "not_found")
	wantErr(t, correct(t, w.srv, w.bob, id, newKey(), `{}`), 403, "forbidden") // 403 before validation
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	bad := []string{
		`{"amount":1,"effective_at":"` + at + `","reason":"r"}`,
		`{"expected_revision":1,"effective_at":"` + at + `","reason":"r"}`,
		`{"expected_revision":1,"amount":1,"reason":"r"}`,
		`{"expected_revision":1,"amount":1,"effective_at":"` + at + `"}`,
	}
	for _, v := range []string{"0", "-1", "1.5", `"1"`, "true", "null"} {
		bad = append(bad, `{"expected_revision":`+v+`,"amount":1,"effective_at":"`+at+`","reason":"r"}`)
	}
	for _, v := range []string{"-1", "1000000001", `"5"`, "1.5", "null", "false"} {
		bad = append(bad, `{"expected_revision":1,"amount":`+v+`,"effective_at":"`+at+`","reason":"r"}`)
	}
	for _, v := range []string{`""`, `"` + strings.Repeat("x", 201) + `"`, "5", "null"} {
		bad = append(bad, `{"expected_revision":1,"amount":1,"effective_at":"`+at+`","reason":`+v+`}`)
	}
	for _, v := range []string{`"` + future + `"`, `"2026-09-20"`, `"2026-09-20T10:00:00"`, `""`, "5", "null"} {
		bad = append(bad, `{"expected_revision":1,"amount":1,"effective_at":`+v+`,"reason":"r"}`)
	}
	for _, b := range bad {
		wantErr(t, correct(t, w.srv, w.ada, id, newKey(), b), 422, "validation_failed")
	}
	// accepted edges: 200-character (emoji) reason, amount 1e9 boundary checked against funds,
	// unknown fields ignored, expected_revision as 1.0
	wantErr(t, correct(t, w.srv, w.ada, id, newKey(), corr(1, 1000000000, at, "big")), 409, "insufficient_funds")
	r := wantStatus(t, correct(t, w.srv, w.ada, id, newKey(), `{"expected_revision":1.0,"amount":60,"effective_at":"`+at+`","reason":"`+strings.Repeat("😀", 200)+`","x":1}`), 201)
	if num(r.body["revision"]) != 2 {
		t.Fatal(r.raw)
	}
	// stale revision (validation passes first; a stale request with a bad field is 422)
	wantErr(t, correct(t, w.srv, w.ada, id, newKey(), corr(1, 70, at, "late")), 409, "stale_revision")
	wantErr(t, correct(t, w.srv, w.ada, id, newKey(), `{"expected_revision":1,"amount":-5,"effective_at":"`+at+`","reason":"r"}`), 422, "validation_failed")
	// a failed correction claims no key
	k := newKey()
	wantErr(t, correct(t, w.srv, w.ada, id, k, corr(1, 70, at, "late")), 409, "stale_revision")
	wantStatus(t, correct(t, w.srv, w.ada, id, k, corr(2, 70, at, "now right")), 201)
	// linked payments: settlement members and captures (after validation, before stale)
	s := wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":5}]}`), 201)
	mem := s.body["payments"].([]any)[0].(map[string]any)
	wantErr(t, correct(t, w.srv, w.ada, mem["payment_id"].(string), newKey(), corr(9, 1, mem["created_at"].(string), "x")), 422, "linked_payment_immutable")
	wantErr(t, correct(t, w.srv, w.ada, mem["payment_id"].(string), newKey(), corr(1, -1, mem["created_at"].(string), "x")), 422, "validation_failed")
	a := authID(t, authorize(t, w, w.ada, "bob", 300))
	c := wantStatus(t, post(t, w.srv, "/authorizations/"+a+"/capture", w.bob, newKey(), `{"amount":100}`), 201)
	wantErr(t, correct(t, w.srv, w.ada, c.body["payment_id"].(string), newKey(), corr(1, 1, c.body["created_at"].(string), "x")), 422, "linked_payment_immutable")
	// payments from paying a request are ordinary and correctable; the request link is unchanged
	rq := newRequest(t, w, w.bob, "ada", 40)
	rp := wantStatus(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, newKey(), `{}`), 201)
	wantStatus(t, correct(t, w.srv, w.ada, rp.body["payment_id"].(string), newKey(), corr(1, 30, rp.body["created_at"].(string), "discount")), 201)
	l := do(t, w.srv, "GET", "/requests", w.ada, "").body["requests"].([]any)[0].(map[string]any)
	if l["payment_id"] != rp.body["payment_id"] || l["status"] != "paid" || num(l["amount"]) != 40 {
		t.Fatal(l)
	}
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

// bob receives 500 from ada at T1 and pays cy 500 at T2. Moving ada's payment after T2
// makes bob negative between T2 and the new time, although his end balance is fine.
func TestHistoricalOverdraftAtAnIntermediateBoundary(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:500", "bob:0", "cy:500"},
		`{"id":"p_in","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-20T10:00:00Z"},
		 {"id":"p_out","from_user_id":"u_bob","to_user_id":"u_cy","amount":500,"created_at":"2026-09-21T10:00:00Z"}`, "")
	wantErr(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(1, 500, "2026-09-22T10:00:00Z", "later")), 409, "historical_overdraft")
	wantErr(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(1, 500, "2026-09-21T10:00:00.000001Z", "1µs later")), 409, "historical_overdraft")
	// nothing changed
	revs := wantStatus(t, do(t, srv, "GET", "/payments/p_in/revisions", tk["ada"], ""), 200).body["revisions"].([]any)
	if len(revs) != 1 || bal(t, srv, tk["bob"], "") != 0 {
		t.Fatal("a refused correction changed state")
	}
	// at exactly T2 both movements happen together: 0 at that boundary -> accepted
	wantStatus(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(1, 500, "2026-09-21T12:00:00+02:00", "same instant")), 201)
	if bal(t, srv, tk["bob"], qs("as_of", "2026-09-21T10:00:00Z")) != 0 || bal(t, srv, tk["bob"], qs("as_of", "2026-09-20T23:00:00Z")) != 0 {
		t.Fatal("history")
	}
	// decreasing what bob received makes him negative after T2 now -> current debit check first
	wantErr(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(2, 100, "2026-09-21T10:00:00Z", "less")), 409, "insufficient_funds")
	// increasing what ada sent: ada's history must allow it (opening 1000, 500 sent at T2)
	wantStatus(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(2, 900, "2026-09-21T10:00:00Z", "more")), 201)
	wantErr(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(3, 1001, "2026-09-21T10:00:00Z", "all")), 409, "insufficient_funds")
	var sum int64
	for _, h := range []string{"ada", "bob", "cy"} {
		sum += bal(t, srv, tk[h], qs("as_of", "2026-09-21T10:00:00Z"))
	}
	if sum != 1000 {
		t.Fatal("sum in a historical view")
	}
}

// insufficient_funds (the current debit) takes precedence over historical_overdraft.
func TestInsufficientFundsBeatsHistoricalOverdraft(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:500", "bob:0", "cy:500"},
		`{"id":"p_in","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-20T10:00:00Z"},
		 {"id":"p_out","from_user_id":"u_bob","to_user_id":"u_cy","amount":500,"created_at":"2026-09-21T10:00:00Z"}`, "")
	// reducing to 0 debits bob 500 now (he has 0) and would also be negative at T2
	wantErr(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(1, 0, "2026-09-20T10:00:00Z", "reverse")), 409, "insufficient_funds")
}

// A hold reserves funds historically: available must stay nonnegative at the hold's
// creation boundary under the corrected history.
func TestHistoricalOverdraftFromAHold(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:1000", "bob:1000", "cy:1000"},
		`{"id":"p_b","from_user_id":"u_bob","to_user_id":"u_ada","amount":500,"created_at":"2026-09-20T10:00:00Z"}`, "")
	w := world{srv: srv, ada: tk["ada"], bob: tk["bob"], cy: tk["cy"]}
	authID(t, authorize(t, w, tk["ada"], "cy", 800)) // ada: total 1000, held 800, available 200
	wantStatus(t, post(t, srv, "/payments", tk["cy"], newKey(), `{"to_handle":"ada","amount":300}`), 201)
	// now ada available = 1300 - 800 = 500. Reducing bob's 500 to 100 debits ada 400 (affordable now),
	// but at the hold's creation ada had 1000 - 400 = 600 < 800 held -> available negative then
	wantErr(t, correct(t, srv, tk["bob"], "p_b", newKey(), corr(1, 100, "2026-09-20T10:00:00Z", "less")), 409, "historical_overdraft")
	wantStatus(t, correct(t, srv, tk["bob"], "p_b", newKey(), corr(1, 300, "2026-09-20T10:00:00Z", "a bit less")), 201) // 800 at creation: ok
	if g := me(t, srv, tk["ada"]); g.total != 1100 || g.available != 300 {
		t.Fatalf("%+v", g)
	}
	// historical /me reflects the hold and the corrected amount together
	r := wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", "2026-09-21T00:00:00Z"), tk["ada"], ""), 200)
	if num(r.body["total"]) != 800 || num(r.body["held"]) != 0 {
		t.Fatal(r.raw)
	}
}

func TestCorrectionMovesPaymentAcrossStatementWindowsAndSnapshots(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:9500", "bob:3000"},
		`{"id":"p_1","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-21T10:00:00Z"}`, "")
	win := qs("from", "2026-09-21T00:00:00Z", "to", "2026-09-22T00:00:00Z")
	before := wantStatus(t, do(t, srv, "GET", "/statement"+win, tk["ada"], ""), 200)
	if len(before.body["entries"].([]any)) != 1 {
		t.Fatal(before.raw)
	}
	snap := before.body["snapshot"].(string)
	wantStatus(t, correct(t, srv, tk["ada"], "p_1", newKey(), corr(1, 500, "2026-09-19T10:00:00Z", "earlier")), 201)
	after := wantStatus(t, do(t, srv, "GET", "/statement"+win, tk["ada"], ""), 200)
	if len(after.body["entries"].([]any)) != 0 || num(after.body["opening_balance"]) != 9500 {
		t.Fatalf("moved out of the window: %s", after.raw)
	}
	early := wantStatus(t, do(t, srv, "GET", "/statement"+qs("to", "2026-09-20T00:00:00Z"), tk["ada"], ""), 200)
	if len(early.body["entries"].([]any)) != 1 {
		t.Fatal("moved into the earlier window")
	}
	again := wantStatus(t, do(t, srv, "GET", "/statement?snapshot="+snap, tk["ada"], ""), 200)
	again.body["snapshot"], before.body["snapshot"] = nil, nil
	if !reflect.DeepEqual(again.body, before.body) {
		t.Fatalf("snapshot changed after a correction:\n%s\n%s", before.raw, again.raw)
	}
}

func TestCorrectImportedOlderStageExports(t *testing.T) {
	for _, dir := range []string{"stage-1", "stage-2"} {
		t.Run(dir, func(t *testing.T) {
			src := &httptest.Server{URL: startStage(t, dir)}
			wantStatus(t, do(t, src, "POST", "/_test/reset", "", std), 204)
			ada := login(t, src, "ada@example.com", "correct horse")
			p := wantStatus(t, post(t, src, "/payments", ada, newKey(), `{"to_handle":"bob","amount":800}`), 201)
			exp := wantStatus(t, do(t, src, "GET", "/_test/export", "", ""), 200)
			dst := httptest.NewServer(app.New())
			t.Cleanup(dst.Close)
			wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
			id := p.body["payment_id"].(string)
			r := wantStatus(t, correct(t, dst, ada, id, newKey(), corr(1, 300, p.body["created_at"].(string), "imported fix")), 201)
			if num(r.body["revision"]) != 2 || me(t, dst, ada).total != 9700 {
				t.Fatal(r.raw)
			}
			revs := wantStatus(t, do(t, dst, "GET", "/payments/"+id+"/revisions", ada, ""), 200).body["revisions"].([]any)
			if len(revs) != 2 || revs[0].(map[string]any)["recorded_at"] != p.body["created_at"] {
				t.Fatal(revs)
			}
		})
	}
}

func TestCorrectionRaces(t *testing.T) {
	w := newWorld(t)
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1000}`), 201)
	id, at := p.body["payment_id"].(string), p.body["created_at"].(string)
	// same expected revision, different keys: exactly one wins
	out := make([]resp, 50)
	parallel(50, func(i int) {
		out[i] = correct(t, w.srv, w.ada, id, newKey(), corr(1, int64(500+i), at, fmt.Sprint("race ", i)))
	})
	n201, n409 := 0, 0
	for _, r := range out {
		switch {
		case r.status == 201:
			n201++
		case r.status == 409 && strings.Contains(r.raw, "stale_revision"):
			n409++
		default:
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	if n201 != 1 || n409 != 49 {
		t.Fatalf("201=%d 409=%d", n201, n409)
	}
	// same key, same body: one 201, the rest identical 200s
	k := newKey()
	body := corr(2, 700, at, "same key")
	parallel(50, func(i int) { out[i] = correct(t, w.srv, w.ada, id, k, body) })
	n201 = 0
	for _, r := range out {
		if r.status == 201 {
			n201++
		} else if r.status != 200 || r.raw != out[0].raw && strings.TrimSpace(r.raw) == "" {
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	revs := wantStatus(t, do(t, w.srv, "GET", "/payments/"+id+"/revisions", w.ada, ""), 200).body["revisions"].([]any)
	if n201 != 1 || len(revs) != 3 || me(t, w.srv, w.ada).total != 9300 {
		t.Fatalf("201=%d revisions=%d", n201, len(revs))
	}
	// corrections racing payments, holds, settlements-free reads and statements
	snap := wantStatus(t, do(t, w.srv, "GET", "/statement", w.ada, ""), 200)
	toks := []string{w.ada, w.bob, w.cy}
	handles := []string{"ada", "bob", "cy"}
	parallel(50, func(i int) {
		for j := 0; j < 8; j++ {
			from, to := i%3, (i+1+j%2)%3
			var r resp
			switch (i + j) % 4 {
			case 0:
				r = post(t, w.srv, "/payments", toks[from], newKey(), fmt.Sprintf(`{"to_handle":%q,"amount":%d}`, handles[to], 1+(i*j)%300))
				if r.status == 201 && from == 0 {
					pid, pat := r.body["payment_id"].(string), r.body["created_at"].(string)
					r = correct(t, w.srv, toks[from], pid, newKey(), corr(1, int64((i*7+j)%400), pat, "adjust"))
				}
			case 1:
				r = correct(t, w.srv, w.ada, id, newKey(), corr(3+j%2, int64(600+j), at, "contend"))
			case 2:
				r = do(t, w.srv, "GET", "/statement?limit=5", toks[from], "")
			case 3:
				r = do(t, w.srv, "GET", "/me", toks[from], "")
				if r.status == 200 && (num(r.body["available"]) < 0 || num(r.body["total"]) < 0) {
					t.Errorf("negative at a read: %s", r.raw)
				}
			}
			if r.status >= 500 {
				t.Errorf("5xx: %s", r.raw)
			}
		}
	})
	if total(t, w) != 13000 {
		t.Fatal("conservation after races")
	}
	again := wantStatus(t, do(t, w.srv, "GET", "/statement?snapshot="+snap.body["snapshot"].(string), w.ada, ""), 200)
	if again.raw != snap.raw {
		t.Fatal("a snapshot changed during the races")
	}
	// no persisted history is overdrawn: every wallet passes the historical check at every boundary
	for _, tok := range toks {
		s := wantStatus(t, do(t, w.srv, "GET", "/statement?limit=200", tok, ""), 200)
		for _, e := range s.body["entries"].([]any) {
			if num(e.(map[string]any)["balance_after"]) < 0 {
				t.Fatalf("persisted negative history: %v", e)
			}
		}
	}
}

// The same-instant rule must not depend on the order movements are stored in: here the
// spend at T2 is listed before the receipt that the correction moves to T2.
func TestSameInstantMovementsCombineInAnyOrder(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:500", "bob:0", "cy:500"},
		`{"id":"p_a_out","from_user_id":"u_bob","to_user_id":"u_cy","amount":500,"created_at":"2026-09-21T10:00:00Z"},
		 {"id":"p_z_in","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-20T10:00:00Z"}`, "")
	wantStatus(t, correct(t, srv, tk["ada"], "p_z_in", newKey(), corr(1, 500, "2026-09-21T10:00:00Z", "same instant")), 201)
	// and a settlement pair (bob receives and spends at one committed_at) stays consistent
	if bal(t, srv, tk["bob"], qs("as_of", "2026-09-21T10:00:00Z")) != 0 {
		t.Fatal("combined boundary")
	}
}
