// Stage-4 work item A tests: refunds (POST /payments/{id}/refunds) and the stage-3
// correction rules they change; views, snapshots, imports of real stage-1/2/3 exports,
// and concurrency.
package tests

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pocketful/app"
)

func refund(t *testing.T, srv *httptest.Server, tok, pid, key string, body string) resp {
	t.Helper()
	return post(t, srv, "/payments/"+pid+"/refunds", tok, key, body)
}

func amt(n int64) string { return fmt.Sprintf(`{"amount":%d}`, n) }

func TestRefundShapeLimitsAndCorrectedAmount(t *testing.T) {
	w := operatorWorld(t) // cy is the operator
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1000,"note":"dinner","visibility":"private"}`), 201)
	if v, ok := p.body["refund_of"]; !ok || v != nil {
		t.Fatalf("other payments carry refund_of null: %s", p.raw)
	}
	id := p.body["payment_id"].(string)
	r := wantStatus(t, refund(t, w.srv, w.bob, id, newKey(), amt(300)), 201)
	for k, v := range map[string]any{"refund_of": id, "from_handle": "bob", "to_handle": "ada", "amount": 300.0, "note": "dinner",
		"visibility": "private", "request_id": nil, "authorization_id": nil, "settlement_id": nil, "currency": "EUR"} {
		if r.body[k] != v {
			t.Fatalf("%s=%v (%s)", k, r.body[k], r.raw)
		}
	}
	if !stampRE.MatchString(r.body["created_at"].(string)) || !(r.body["created_at"].(string) > p.body["created_at"].(string)) {
		t.Fatal(r.raw)
	}
	if me(t, w.srv, w.ada).total != 9300 || me(t, w.srv, w.bob).total != 3200 {
		t.Fatal("money")
	}
	// the refund has exactly one revision, effective = recorded = created_at
	rid := r.body["payment_id"].(string)
	revs := wantStatus(t, do(t, w.srv, "GET", "/payments/"+rid+"/revisions", w.ada, ""), 200).body["revisions"].([]any)
	if len(revs) != 1 || revs[0].(map[string]any)["effective_at"] != r.body["created_at"] || revs[0].(map[string]any)["recorded_at"] != r.body["created_at"] {
		t.Fatal(revs)
	}
	// cumulative up to exactly the amount; one more unit exceeds
	wantStatus(t, refund(t, w.srv, w.bob, id, newKey(), amt(700)), 201)
	wantErr(t, refund(t, w.srv, w.bob, id, newKey(), amt(1)), 422, "refund_exceeds_payment")
	// a refund of a refund
	wantErr(t, refund(t, w.srv, w.ada, rid, newKey(), amt(1)), 422, "invalid_refund_target")
	// refunds cannot be corrected
	wantErr(t, correct(t, w.srv, w.bob, rid, newKey(), corr(1, 1, r.body["created_at"].(string), "x")), 422, "linked_payment_immutable")
	// against the CURRENT corrected amount
	q := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":500}`), 201)
	qid, qat := q.body["payment_id"].(string), q.body["created_at"].(string)
	wantStatus(t, correct(t, w.srv, w.ada, qid, newKey(), corr(1, 200, qat, "less")), 201)
	wantErr(t, refund(t, w.srv, w.bob, qid, newKey(), amt(201)), 422, "refund_exceeds_payment")
	wantStatus(t, refund(t, w.srv, w.bob, qid, newKey(), amt(150)), 201)
	// a correction below the refunded amount → 422; to exactly it, or above, is fine
	wantErr(t, correct(t, w.srv, w.ada, qid, newKey(), corr(2, 149, qat, "too low")), 422, "refund_exceeds_payment")
	wantStatus(t, correct(t, w.srv, w.ada, qid, newKey(), corr(2, 150, qat, "exactly")), 201)
	wantStatus(t, correct(t, w.srv, w.ada, qid, newKey(), corr(3, 400, qat, "more")), 201)
	wantStatus(t, refund(t, w.srv, w.bob, qid, newKey(), amt(250)), 201)
	// stale beats refund_exceeds; refund_exceeds beats insufficient_funds
	wantErr(t, correct(t, w.srv, w.ada, qid, newKey(), corr(1, 0, qat, "stale")), 409, "stale_revision")
	// corrected to 0, then any refund exceeds
	z := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":100}`), 201)
	wantStatus(t, correct(t, w.srv, w.ada, z.body["payment_id"].(string), newKey(), corr(1, 0, z.body["created_at"].(string), "void")), 201)
	wantErr(t, refund(t, w.srv, w.bob, z.body["payment_id"].(string), newKey(), amt(1)), 422, "refund_exceeds_payment")
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestRefundValidationAndPrecedence(t *testing.T) {
	w := operatorWorld(t)
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":100}`), 201)
	id := p.body["payment_id"].(string)
	wantErr(t, refund(t, w.srv, "", id, newKey(), amt(10)), 401, "unauthenticated")
	wantErr(t, refund(t, w.srv, w.bob, id, "", amt(10)), 400, "missing_idempotency_key")
	wantErr(t, refund(t, w.srv, w.bob, id, newKey(), `[]`), 400, "malformed_request")
	wantErr(t, refund(t, w.srv, w.bob, "nope", newKey(), amt(10)), 404, "not_found")
	for _, who := range []string{w.ada, w.cy} { // the sender, the operator/third party
		wantErr(t, refund(t, w.srv, who, id, newKey(), amt(10)), 403, "forbidden")
	}
	wantErr(t, refund(t, w.srv, w.ada, id, newKey(), `{}`), 403, "forbidden") // 403 before validation
	for _, v := range []string{"0", "-1", "1.5", `"5"`, "true", "null", "1000000001", "[1]"} {
		wantErr(t, refund(t, w.srv, w.bob, id, newKey(), `{"amount":`+v+`}`), 422, "validation_failed")
	}
	wantErr(t, refund(t, w.srv, w.bob, id, newKey(), `{}`), 422, "validation_failed")
	wantErr(t, refund(t, w.srv, w.bob, id, newKey(), amt(101)), 422, "refund_exceeds_payment")
	// idempotency: replay, reuse, claimed key beats an invalid body, failures claim nothing
	k := newKey()
	r := wantStatus(t, refund(t, w.srv, w.bob, id, k, `{"amount":40,"x":1}`), 201)
	rp := wantStatus(t, refund(t, w.srv, w.bob, id, k, `{"x":1,"amount":40.0}`), 200)
	if rp.raw != r.raw {
		t.Fatal("replay differs")
	}
	wantErr(t, refund(t, w.srv, w.bob, id, k, amt(41)), 409, "idempotency_key_reuse")
	wantErr(t, refund(t, w.srv, w.bob, id, k, `{"amount":"bad"}`), 409, "idempotency_key_reuse")
	k2 := newKey()
	wantErr(t, refund(t, w.srv, w.bob, id, k2, amt(61)), 422, "refund_exceeds_payment")
	if me(t, w.srv, w.bob).total != 2560 {
		t.Fatal("a failed refund changed money")
	}
	wantStatus(t, refund(t, w.srv, w.bob, id, k2, amt(60)), 201)
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestRefundUsesAvailableFunds(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:1000", "bob:0", "cy:0"}, "", "")
	w := world{srv: srv, ada: tk["ada"], bob: tk["bob"], cy: tk["cy"]}
	p := wantStatus(t, post(t, srv, "/payments", tk["ada"], newKey(), `{"to_handle":"bob","amount":1000}`), 201)
	authID(t, authorize(t, w, tk["bob"], "cy", 900)) // bob: total 1000, available 100
	wantErr(t, refund(t, srv, tk["bob"], p.body["payment_id"].(string), newKey(), amt(101)), 409, "insufficient_funds")
	wantStatus(t, refund(t, srv, tk["bob"], p.body["payment_id"].(string), newKey(), amt(100)), 201)
	if g := me(t, srv, tk["bob"]); g != (wallet{900, 0, 900, 900}) {
		t.Fatalf("%+v", g)
	}
}

func TestRefundTargetKinds(t *testing.T) {
	w := operatorWorld(t)
	// a capture: the hold is not restored and the authorization stays captured
	a := authID(t, authorize(t, w, w.ada, "bob", 500))
	c := wantStatus(t, post(t, w.srv, "/authorizations/"+a+"/capture", w.bob, newKey(), `{"amount":300}`), 201)
	rc := wantStatus(t, refund(t, w.srv, w.bob, c.body["payment_id"].(string), newKey(), amt(300)), 201)
	if rc.body["authorization_id"] != nil || rc.body["refund_of"] != c.body["payment_id"] {
		t.Fatal(rc.raw)
	}
	l := wantStatus(t, do(t, w.srv, "GET", "/authorizations", w.ada, ""), 200).body["authorizations"].([]any)[0].(map[string]any)
	if l["status"] != "captured" || num(l["captured_amount"]) != 300 || me(t, w.srv, w.ada).held != 0 {
		t.Fatal(l)
	}
	// a request payment: the request stays paid
	rq := newRequest(t, w, w.bob, "ada", 80)
	rp := wantStatus(t, post(t, w.srv, "/requests/"+rq+"/pay", w.ada, newKey(), `{"visibility":"private"}`), 201)
	rr := wantStatus(t, refund(t, w.srv, w.bob, rp.body["payment_id"].(string), newKey(), amt(80)), 201)
	if rr.body["request_id"] != nil || rr.body["visibility"] != "private" {
		t.Fatal(rr.raw)
	}
	q := do(t, w.srv, "GET", "/requests", w.ada, "").body["requests"].([]any)[0].(map[string]any)
	if q["status"] != "paid" || q["payment_id"] != rp.body["payment_id"] {
		t.Fatal(q)
	}
	// a settlement member: membership unchanged, the refund is not a member
	s := wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":60},{"from_handle":"bob","to_handle":"cy","amount":10}]}`), 201)
	mem := s.body["payments"].([]any)[0].(map[string]any)
	rs := wantStatus(t, refund(t, w.srv, w.bob, mem["payment_id"].(string), newKey(), amt(60)), 201)
	if rs.body["settlement_id"] != nil {
		t.Fatal(rs.raw)
	}
	// single corrections of members, captures and refunds stay immutable
	for _, x := range []struct {
		tok string
		p   map[string]any
	}{{w.ada, mem}, {w.ada, c.body}, {w.bob, rs.body}} { // each by its own sender
		wantErr(t, correct(t, w.srv, x.tok, x.p["payment_id"].(string), newKey(), corr(1, 1, x.p["created_at"].(string), "x")), 422, "linked_payment_immutable")
	}
	feed := wantStatus(t, do(t, w.srv, "GET", "/activity?limit=200", w.cy, ""), 200).body["payments"].([]any)
	members := 0
	for _, f := range feed {
		if f.(map[string]any)["settlement_id"] == s.body["settlement_id"] {
			members++
		}
	}
	if members != 2 {
		t.Fatal("membership changed")
	}
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
}

func TestRefundViewsAndSnapshots(t *testing.T) {
	w := newWorld(t)
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1000,"note":"rent"}`), 201)
	snap := wantStatus(t, do(t, w.srv, "GET", "/statement", w.ada, ""), 200)
	r := wantStatus(t, refund(t, w.srv, w.bob, p.body["payment_id"].(string), newKey(), amt(400)), 201)
	at := r.body["created_at"].(string)
	tm, _ := time.Parse(time.RFC3339Nano, at)
	before := tm.Add(-time.Microsecond).UTC().Format(time.RFC3339Nano)
	// the feed shows the refund as an ordinary public payment, newest first, to a third party
	feed := wantStatus(t, do(t, w.srv, "GET", "/activity", w.cy, ""), 200).body["payments"].([]any)
	if len(feed) != 2 || feed[0].(map[string]any)["refund_of"] != p.body["payment_id"] || feed[0].(map[string]any)["from_handle"] != "bob" {
		t.Fatal(feed)
	}
	// statement entry; as_of / known_at at the refund instant ±1µs
	s := wantStatus(t, do(t, w.srv, "GET", "/statement", w.ada, ""), 200)
	es := s.body["entries"].([]any)
	if len(es) != 2 || num(es[1].(map[string]any)["delta"]) != 400 || es[1].(map[string]any)["payment"].(map[string]any)["refund_of"] != p.body["payment_id"] ||
		num(s.body["closing_balance"]) != 9400 {
		t.Fatal(s.raw)
	}
	if bal(t, w.srv, w.ada, qs("as_of", before)) != 9000 || bal(t, w.srv, w.ada, qs("as_of", at)) != 9400 ||
		bal(t, w.srv, w.ada, qs("as_of", "2099-01-01T00:00:00Z", "known_at", before)) != 9000 {
		t.Fatal("historical views around the refund")
	}
	// the snapshot taken before the refund is unchanged
	again := wantStatus(t, do(t, w.srv, "GET", "/statement?snapshot="+snap.body["snapshot"].(string), w.ada, ""), 200)
	if again.raw != snap.raw {
		t.Fatal("snapshot changed after a refund")
	}
}

// A refund is a fixed movement at its own instant: moving the payment that funded it to
// after the refund would make the refunder negative in the past.
func TestCorrectionAroundARefundIsHistoricallyChecked(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:500", "bob:500", "cy:0"},
		`{"id":"p_in","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-20T10:00:00Z"}`, "")
	r := wantStatus(t, refund(t, srv, tk["bob"], "p_in", newKey(), amt(300)), 201) // bob: 200
	// bob also pays cy his remaining 200: at the refund instant bob had 500 - 300 = 200
	wantStatus(t, post(t, srv, "/payments", tk["bob"], newKey(), `{"to_handle":"cy","amount":200}`), 201)
	rt, _ := time.Parse(time.RFC3339Nano, r.body["created_at"].(string))
	later := rt.Add(time.Microsecond).UTC().Format(time.RFC3339Nano)
	// moving the 500 after the refund: bob would be -300 at the refund instant
	wantErr(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(1, 500, later, "later")), 409, "historical_overdraft")
	// moving it to exactly the refund instant combines both movements -> fine
	wantStatus(t, correct(t, srv, tk["ada"], "p_in", newKey(), corr(1, 500, r.body["created_at"].(string), "same instant")), 201)
}

func TestRefundRaces(t *testing.T) {
	w := newWorld(t)
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1000}`), 201)
	id, at := p.body["payment_id"].(string), p.body["created_at"].(string)
	out := make([]resp, 60)
	start := time.Now()
	parallel(60, func(i int) {
		if i%6 == 5 { // the sender keeps lowering the amount concurrently
			out[i] = correct(t, w.srv, w.ada, id, newKey(), corr(1+i/6, int64(1000-10*i), at, "lower"))
			return
		}
		out[i] = refund(t, w.srv, w.bob, id, newKey(), amt(30))
	})
	if time.Since(start) > 5*time.Second {
		t.Fatal("too slow")
	}
	for _, r := range out {
		if r.status >= 500 {
			t.Fatalf("5xx: %s", r.raw)
		}
	}
	revs := wantStatus(t, do(t, w.srv, "GET", "/payments/"+id+"/revisions", w.ada, ""), 200).body["revisions"].([]any)
	current := num(revs[len(revs)-1].(map[string]any)["amount"])
	var refunded int64
	for _, f := range wantStatus(t, do(t, w.srv, "GET", "/activity?limit=200", w.ada, ""), 200).body["payments"].([]any) {
		if f.(map[string]any)["refund_of"] == id {
			refunded += num(f.(map[string]any)["amount"])
		}
	}
	if refunded > current || refunded == 0 {
		t.Fatalf("refunded %d exceeds current amount %d", refunded, current)
	}
	for _, tok := range []string{w.ada, w.bob, w.cy} {
		if g := me(t, w.srv, tok); g.total < 0 || g.available < 0 {
			t.Fatal("negative")
		}
	}
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
	// same key concurrently: one 201, the rest identical 200s
	q := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":500}`), 201)
	k := newKey()
	parallel(50, func(i int) { out[i] = refund(t, w.srv, w.bob, q.body["payment_id"].(string), k, amt(100)) })
	n201 := 0
	for _, r := range out[:50] {
		if r.status == 201 {
			n201++
		} else if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	n := 0
	for _, f := range wantStatus(t, do(t, w.srv, "GET", "/activity?limit=200", w.ada, ""), 200).body["payments"].([]any) {
		if f.(map[string]any)["refund_of"] == q.body["payment_id"] {
			n++
		}
	}
	if n201 != 1 || n != 1 || total(t, w) != 13000 {
		t.Fatalf("same-key refunds: 201=%d refunds=%d", n201, n)
	}
}

func TestImportOlderStagesThenRefund(t *testing.T) {
	for _, dir := range []string{"stage-1", "stage-2", "stage-3"} {
		t.Run(dir, func(t *testing.T) {
			src := &httptest.Server{URL: startStage(t, dir)}
			f := `{"currency":"EUR","minor_units":2,"settlement_operator_ids":["u_cy"],"users":[` + user("ada", 10000) + `,` + user("bob", 2500) + `,` + user("cy", 500) + `]}`
			wantStatus(t, do(t, src, "POST", "/_test/reset", "", f), 204)
			ada := login(t, src, "ada@example.com", "correct horse")
			bob := login(t, src, "bob@example.com", "correct horse")
			cy := login(t, src, "cy@example.com", "correct horse")
			k := newKey()
			p := wantStatus(t, post(t, src, "/payments", ada, k, `{"to_handle":"bob","amount":800}`), 201)
			s := wantStatus(t, post(t, src, "/settlements", cy, newKey(), `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":50}]}`), 201)
			var snap resp
			if dir == "stage-3" {
				wantStatus(t, post(t, src, "/payments/"+p.body["payment_id"].(string)+"/corrections", ada, newKey(), corr(1, 700, p.body["created_at"].(string), "fix")), 201)
				snap = wantStatus(t, do(t, src, "GET", "/statement", ada, ""), 200)
			}
			exp := wantStatus(t, do(t, src, "GET", "/_test/export", "", ""), 200)
			dst := httptest.NewServer(app.New())
			t.Cleanup(dst.Close)
			wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
			// receipts replay byte-identical (without refund_of); membership kept
			r := wantStatus(t, post(t, dst, "/payments", ada, k, `{"to_handle":"bob","amount":800}`), 200)
			if r.raw != p.raw || strings.Contains(r.raw, "refund_of") {
				t.Fatalf("receipt changed: %s", r.raw)
			}
			feed := wantStatus(t, do(t, dst, "GET", "/activity", bob, ""), 200).body["payments"].([]any)
			found := false
			for _, x := range feed {
				x := x.(map[string]any)
				if x["settlement_id"] == s.body["settlement_id"] && x["refund_of"] == nil {
					found = true
				}
			}
			if !found {
				t.Fatal("settlement membership lost")
			}
			if dir == "stage-3" {
				again := wantStatus(t, do(t, dst, "GET", "/statement?snapshot="+snap.body["snapshot"].(string), ada, ""), 200)
				if again.raw != snap.raw {
					t.Fatal("stage-3 snapshot changed after import")
				}
			}
			// nothing refunded yet: the full current amount is refundable, no more
			cur := int64(800)
			if dir == "stage-3" {
				cur = 700
			}
			wantErr(t, refund(t, dst, bob, p.body["payment_id"].(string), newKey(), amt(cur+1)), 422, "refund_exceeds_payment")
			wantStatus(t, refund(t, dst, bob, p.body["payment_id"].(string), newKey(), amt(cur)), 201)
			var sum int64
			for _, tok := range []string{ada, bob, cy} {
				sum += me(t, dst, tok).total
			}
			if sum != 13000 {
				t.Fatal("conservation")
			}
		})
	}
}

func TestImportRejectsInvalidRefunds(t *testing.T) {
	w := newWorld(t)
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":100}`), 201)
	wantStatus(t, refund(t, w.srv, w.bob, p.body["payment_id"].(string), newKey(), amt(60)), 201)
	exp := wantStatus(t, do(t, w.srv, "GET", "/_test/export", "", ""), 200)
	mutate := func(f func(ps []any)) string {
		var doc map[string]any
		_ = json.Unmarshal([]byte(exp.raw), &doc)
		f(doc["state"].(map[string]any)["payments"].([]any))
		b, _ := json.Marshal(doc)
		return string(b)
	}
	find := func(ps []any, refund bool) map[string]any {
		for _, x := range ps {
			if _, ok := x.(map[string]any)["refund_of"]; ok == refund {
				return x.(map[string]any)
			}
		}
		return nil
	}
	cases := map[string]string{
		"unknown target":   mutate(func(ps []any) { find(ps, true)["refund_of"] = "p_nope" }),
		"refund of refund": mutate(func(ps []any) { r := find(ps, true); r["refund_of"] = r["id"] }),
		"wrong direction": mutate(func(ps []any) {
			r := find(ps, true)
			r["from_user_id"], r["to_user_id"] = r["to_user_id"], r["from_user_id"]
		}),
		"exceeds target": mutate(func(ps []any) {
			o := find(ps, false)
			o["amount"] = 50
			o["revisions"].([]any)[0].(map[string]any)["amount"] = 50
		}),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			dst := httptest.NewServer(app.New())
			t.Cleanup(dst.Close)
			wantErr(t, do(t, dst, "POST", "/_test/import", "", b), 422, "validation_failed")
		})
	}
	dst := httptest.NewServer(app.New())
	t.Cleanup(dst.Close)
	wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
}
