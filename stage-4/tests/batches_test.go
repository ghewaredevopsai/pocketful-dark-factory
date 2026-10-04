// Stage-4 work item B tests: POST /correction-batches — shape and auth matrix, item
// precedence, settlement completeness and identical instants, combined affordability,
// historical overdraft from the combined batch, atomic rollback, shared recorded_at,
// receipts/snapshots/views, imports, races and a 32-item performance check.
package tests

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"pocketful/app"
)

type bItem struct {
	pid    string
	exp    int
	amount int64
	eff    string
}

func batchBody(items ...bItem) string {
	var xs []string
	for _, it := range items {
		xs = append(xs, fmt.Sprintf(`{"payment_id":%q,"expected_revision":%d,"amount":%d,"effective_at":%q,"reason":"batch fix"}`, it.pid, it.exp, it.amount, it.eff))
	}
	return `{"corrections":[` + strings.Join(xs, ",") + `]}`
}

func batchPost(t *testing.T, srv *httptest.Server, tok, key, body string) resp {
	t.Helper()
	return post(t, srv, "/correction-batches", tok, key, body)
}

// opWorld: ada 10000, bob 2500, cy 500 (cy is the operator) with two seeded ada->bob payments.
func opWorld(t *testing.T, payments string) (*httptest.Server, map[string]string) {
	return worldWith(t, []string{"ada:10000", "bob:2500", "cy:500"}, payments, `,"settlement_operator_ids":["u_cy"]`)
}

func TestBatchSpecExample(t *testing.T) {
	srv, tk := opWorld(t, `{"id":"p_a","from_user_id":"u_ada","to_user_id":"u_bob","amount":300,"created_at":"2026-09-21T10:00:00Z"},
	 {"id":"p_b","from_user_id":"u_ada","to_user_id":"u_bob","amount":200,"created_at":"2026-09-21T11:00:00Z"}`)
	// a single correction first, so the batch must be later than p_a's revision 2
	r2 := wantStatus(t, correct(t, srv, tk["ada"], "p_a", newKey(), corr(1, 300, "2026-09-21T10:00:00Z", "noop")), 201)
	k := newKey()
	body := batchBody(bItem{"p_a", 2, 0, "2026-09-20T12:00:00+00:00"}, bItem{"p_b", 1, 0, "2026-09-20T12:00:00+00:00"})
	r := wantStatus(t, batchPost(t, srv, tk["cy"], k, body), 201)
	bid, _ := r.body["correction_batch_id"].(string)
	rec, _ := r.body["recorded_at"].(string)
	revs := r.body["revisions"].([]any)
	if bid == "" || !strings.Contains(rec, ".") || len(revs) != 2 || !(rec > r2.body["recorded_at"].(string)) {
		t.Fatal(r.raw)
	}
	for i, want := range []struct {
		id  string
		rev float64
	}{{"p_a", 3}, {"p_b", 2}} {
		rv := revs[i].(map[string]any)
		if rv["payment_id"] != want.id || rv["revision"] != want.rev || rv["amount"] != 0.0 || rv["recorded_at"] != rec ||
			rv["correction_batch_id"] != bid || rv["effective_at"] != "2026-09-20T12:00:00+00:00" || rv["reason"] != "batch fix" {
			t.Fatalf("revision %d: %v", i, rv)
		}
	}
	// both reversed: ada +500, bob -500
	if bal(t, srv, tk["ada"], "") != 10500 || bal(t, srv, tk["bob"], "") != 2000 {
		t.Fatal("balances")
	}
	// revision lists, statements and activity
	lst := wantStatus(t, do(t, srv, "GET", "/payments/p_b/revisions", tk["ada"], ""), 200).body["revisions"].([]any)
	if len(lst) != 2 || lst[0].(map[string]any)["correction_batch_id"] != nil || lst[1].(map[string]any)["correction_batch_id"] != bid {
		t.Fatal(lst)
	}
	st := wantStatus(t, do(t, srv, "GET", "/statement", tk["ada"], ""), 200)
	for _, e := range st.body["entries"].([]any) {
		e := e.(map[string]any)
		if e["correction_batch_id"] != bid || num(e["delta"]) != 0 {
			t.Fatalf("statement entry %v", e)
		}
	}
	feed := wantStatus(t, do(t, srv, "GET", "/activity", tk["ada"], ""), 200).body["payments"].([]any)
	if len(feed) != 2 || num(feed[0].(map[string]any)["amount"]) != 200 || num(feed[1].(map[string]any)["amount"]) != 300 {
		t.Fatal("activity must show the original payments")
	}
	// replay after a newer revision: 200 with the original batch response
	wantStatus(t, correct(t, srv, tk["ada"], "p_b", newKey(), corr(2, 50, "2026-09-20T12:00:00Z", "later")), 201)
	rp := wantStatus(t, batchPost(t, srv, tk["cy"], k, body), 200)
	if rp.raw != r.raw {
		t.Fatal("batch replay differs")
	}
	wantErr(t, batchPost(t, srv, tk["cy"], k, batchBody(bItem{"p_a", 2, 1, "2026-09-20T12:00:00Z"})), 409, "idempotency_key_reuse")
	wantErr(t, batchPost(t, srv, tk["cy"], k, `{"corrections":"bad"}`), 409, "idempotency_key_reuse") // claimed key beats invalid
	var sum int64
	for _, h := range []string{"ada", "bob", "cy"} {
		for _, at := range []string{"2026-09-20T11:00:00Z", "2026-09-20T12:00:00Z", "2026-09-21T10:30:00Z", "2099-01-01T00:00:00Z"} {
			_ = at
		}
		sum += bal(t, srv, tk[h], "")
	}
	if sum != 13000 {
		t.Fatal("conservation")
	}
	for _, at := range []string{"2026-09-20T11:00:00Z", "2026-09-20T12:00:00Z", "2026-09-21T10:30:00Z", "2099-01-01T00:00:00Z"} {
		var s int64
		for _, h := range []string{"ada", "bob", "cy"} {
			s += bal(t, srv, tk[h], qs("as_of", at))
		}
		if s != 13000 {
			t.Fatalf("sum at %s: %d", at, s)
		}
	}
}

func TestBatchAuthAndShapeMatrix(t *testing.T) {
	srv, tk := opWorld(t, `{"id":"p_a","from_user_id":"u_ada","to_user_id":"u_bob","amount":300,"created_at":"2026-09-21T10:00:00Z"}`)
	ok := batchBody(bItem{"p_a", 1, 100, "2026-09-21T10:00:00Z"})
	wantErr(t, batchPost(t, srv, "", newKey(), ok), 401, "unauthenticated")
	wantErr(t, batchPost(t, srv, tk["ada"], newKey(), ok), 403, "forbidden") // the payment's sender, not an operator
	wantErr(t, batchPost(t, srv, tk["ada"], "", ok), 403, "forbidden")       // 403 before the key check
	wantErr(t, batchPost(t, srv, tk["cy"], "", ok), 400, "missing_idempotency_key")
	wantErr(t, batchPost(t, srv, tk["cy"], strings.Repeat("k", 256), ok), 422, "validation_failed")
	wantErr(t, batchPost(t, srv, tk["cy"], newKey(), `[1]`), 400, "malformed_request")
	many := make([]bItem, 33)
	for i := range many {
		many[i] = bItem{fmt.Sprintf("p_%d", i), 1, 1, "2026-09-21T10:00:00Z"}
	}
	for _, b := range []string{`{}`, `{"corrections":{}}`, `{"corrections":"x"}`, `{"corrections":[]}`, batchBody(many...), `{"corrections":[1]}`,
		batchBody(bItem{"p_a", 1, 100, "2026-09-21T10:00:00Z"}, bItem{"p_a", 1, 50, "2026-09-21T10:00:00Z"})} {
		wantErr(t, batchPost(t, srv, tk["cy"], newKey(), b), 422, "validation_failed")
	}
	// item field validation (each → 422), unknown fields ignored
	for _, it := range []string{`{"expected_revision":1,"amount":1,"effective_at":"2026-09-21T10:00:00Z","reason":"r"}`,
		`{"payment_id":"p_a","expected_revision":0,"amount":1,"effective_at":"2026-09-21T10:00:00Z","reason":"r"}`,
		`{"payment_id":"p_a","expected_revision":1,"amount":-1,"effective_at":"2026-09-21T10:00:00Z","reason":"r"}`,
		`{"payment_id":"p_a","expected_revision":1,"amount":1,"effective_at":"2026-09-21","reason":"r"}`,
		`{"payment_id":"p_a","expected_revision":1,"amount":1,"effective_at":"2099-01-01T00:00:00Z","reason":"r"}`,
		`{"payment_id":"p_a","expected_revision":1,"amount":1,"effective_at":"2026-09-21T10:00:00Z","reason":""}`} {
		wantErr(t, batchPost(t, srv, tk["cy"], newKey(), `{"corrections":[`+it+`]}`), 422, "validation_failed")
	}
	wantErr(t, batchPost(t, srv, tk["cy"], newKey(), batchBody(bItem{"p_nope", 1, 1, "2026-09-21T10:00:00Z"})), 404, "not_found")
	wantStatus(t, batchPost(t, srv, tk["cy"], newKey(), `{"x":1,"corrections":[{"payment_id":"p_a","expected_revision":1,"amount":100,"effective_at":"2026-09-21T10:00:00Z","reason":"r","y":2}]}`), 201)
	// the operator may correct any wallets; a single correction by the operator is still 403
	wantErr(t, correct(t, srv, tk["cy"], "p_a", newKey(), corr(2, 90, "2026-09-21T10:00:00Z", "x")), 403, "forbidden")
}

func TestBatchSettlementRules(t *testing.T) {
	w := operatorWorld(t) // cy operator
	s := wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":100},{"from_handle":"bob","to_handle":"cy","amount":40}]}`), 201)
	m1 := s.body["payments"].([]any)[0].(map[string]any)
	m2 := s.body["payments"].([]any)[1].(map[string]any)
	at := s.body["committed_at"].(string)
	other := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"cy","amount":10}`), 201)
	// subset → incomplete_settlement; different instants → validation_failed
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{m1["payment_id"].(string), 1, 90, at})), 422, "incomplete_settlement")
	tm, _ := time.Parse(time.RFC3339Nano, at)
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{m1["payment_id"].(string), 1, 90, at}, bItem{m2["payment_id"].(string), 1, 40, tm.Add(time.Microsecond).Format(time.RFC3339Nano)})), 422, "validation_failed")
	// a single correction of a member stays linked_payment_immutable
	wantErr(t, correct(t, w.srv, w.ada, m1["payment_id"].(string), newKey(), corr(1, 90, at, "x")), 422, "linked_payment_immutable")
	// the same instant written with another offset, plus a non-member item: accepted
	plus2 := tm.In(time.FixedZone("x", 2*3600)).Format("2006-01-02T15:04:05.999999-07:00")
	r := wantStatus(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{m1["payment_id"].(string), 1, 90, at}, bItem{m2["payment_id"].(string), 1, 35, plus2},
		bItem{other.body["payment_id"].(string), 1, 5, other.body["created_at"].(string)})), 201)
	if len(r.body["revisions"].([]any)) != 3 {
		t.Fatal(r.raw)
	}
	// the original settlement receipt is unchanged; membership unchanged; activity shows originals
	feed := wantStatus(t, do(t, w.srv, "GET", "/activity?limit=200", w.bob, ""), 200).body["payments"].([]any)
	got := map[string]float64{}
	for _, f := range feed {
		f := f.(map[string]any)
		if f["settlement_id"] == s.body["settlement_id"] {
			got[f["payment_id"].(string)] = f["amount"].(float64)
		}
	}
	if got[m1["payment_id"].(string)] != 100 || got[m2["payment_id"].(string)] != 40 {
		t.Fatal(got)
	}
	// money: ada +10 (100→90) +5 (10→5); bob -10 +5; cy -5 -5
	if me(t, w.srv, w.ada).total != 10000-100-10+10+5 || total(t, w) != 13000 {
		t.Fatalf("ada %d", me(t, w.srv, w.ada).total)
	}
	// a second batch on the members needs both again, at their latest revisions
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{m1["payment_id"].(string), 1, 80, at}, bItem{m2["payment_id"].(string), 2, 35, at})), 409, "stale_revision")
	// captures and refunds stay immutable in a batch
	a := authID(t, authorize(t, w, w.ada, "bob", 100))
	c := wantStatus(t, post(t, w.srv, "/authorizations/"+a+"/capture", w.bob, newKey(), `{}`), 201)
	rf := wantStatus(t, refund(t, w.srv, w.bob, c.body["payment_id"].(string), newKey(), amt(10)), 201)
	for _, x := range []map[string]any{c.body, rf.body} {
		wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{x["payment_id"].(string), 1, 1, x["created_at"].(string)})), 422, "linked_payment_immutable")
	}
	// refunds of members are not members: refund m1, then a full batch still needs only m1, m2
	wantStatus(t, refund(t, w.srv, w.bob, m1["payment_id"].(string), newKey(), amt(20)), 201)
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{m1["payment_id"].(string), 2, 19, at}, bItem{m2["payment_id"].(string), 2, 35, at})), 422, "refund_exceeds_payment")
	wantStatus(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{m1["payment_id"].(string), 2, 20, at}, bItem{m2["payment_id"].(string), 2, 35, at})), 201)
}

func TestBatchPrecedence(t *testing.T) {
	w := operatorWorld(t)
	s := wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":100},{"from_handle":"bob","to_handle":"cy","amount":40}]}`), 201)
	m1 := s.body["payments"].([]any)[0].(map[string]any)["payment_id"].(string)
	at := s.body["committed_at"].(string)
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"cy","amount":400}`), 201)
	pid, pat := p.body["payment_id"].(string), p.body["created_at"].(string)
	// items in input order: item 1's stale beats item 2's 404, and vice versa
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{pid, 2, 1, pat}, bItem{"p_nope", 1, 1, pat})), 409, "stale_revision")
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{"p_nope", 1, 1, pat}, bItem{pid, 2, 1, pat})), 404, "not_found")
	// within one item: 404 → fields → linked → stale → refund_exceeds
	// item errors beat completeness: an incomplete settlement plus an invalid item 2
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), `{"corrections":[{"payment_id":"`+m1+`","expected_revision":1,"amount":90,"effective_at":"`+at+`","reason":"r"},{"payment_id":"`+pid+`","expected_revision":1,"amount":-1,"effective_at":"`+pat+`","reason":"r"}]}`), 422, "validation_failed")
	// completeness beats insufficient funds (cy has 540: increasing cy's debit by 1e6 is unaffordable)
	// (raising pid to 1e9 is also unaffordable for ada: only the order makes this incomplete_settlement)
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{m1, 1, 90, at}, bItem{pid, 1, 1000000000, pat})), 422, "incomplete_settlement")
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{pid, 1, 1000000000, pat})), 409, "insufficient_funds")
	// insufficient funds beats historical overdraft: cy's 400 receipt reduced to 0 (debit 400 > 540? no) —
	// reduce cy's receipt by more than cy's available: cy available 540; reduce pid by 400 ok, so add a hold
	authID(t, authorize(t, w, w.cy, "ada", 700)) // cy: total 940, available 240
	wantErr(t, batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{pid, 1, 0, "2026-01-01T00:00:00Z"})), 409, "insufficient_funds")
	if total(t, w) != 13000 || len(wantStatus(t, do(t, w.srv, "GET", "/payments/"+pid+"/revisions", w.ada, ""), 200).body["revisions"].([]any)) != 1 {
		t.Fatal("rejected batches changed state")
	}
}

// Combined effect: item A credits a wallet with what item B debits from it.
func TestBatchCombinedAffordability(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:1000", "bob:301", "cy:1000", "dee:0"}, `
	 {"id":"p_x","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-20T10:00:00Z"},
	 {"id":"p_y","from_user_id":"u_bob","to_user_id":"u_cy","amount":500,"created_at":"2026-09-20T11:00:00Z"},
	 {"id":"p_z","from_user_id":"u_cy","to_user_id":"u_bob","amount":300,"created_at":"2026-09-20T12:00:00Z"},
	 {"id":"p_w","from_user_id":"u_dee","to_user_id":"u_bob","amount":1,"created_at":"2026-09-20T12:30:00Z"}`, `,"settlement_operator_ids":["u_dee"]`)
	// bob: +500, -500, +300, +1 = 301 now. Raising p_y by 400 debits bob 400 (> 301): alone it fails
	wantErr(t, batchPost(t, srv, tk["dee"], newKey(), batchBody(bItem{"p_y", 1, 900, "2026-09-20T11:00:00Z"})), 409, "insufficient_funds")
	// with p_x raised by 400 at the same time (credits bob 400) the combined effect is affordable
	r := wantStatus(t, batchPost(t, srv, tk["dee"], newKey(), batchBody(bItem{"p_y", 1, 900, "2026-09-20T11:00:00Z"}, bItem{"p_x", 1, 900, "2026-09-20T10:00:00Z"})), 201)
	_ = r
	if bal(t, srv, tk["bob"], "") != 301 || bal(t, srv, tk["ada"], "") != 600 || bal(t, srv, tk["cy"], "") != 1400 {
		t.Fatal("balances after the combined batch")
	}
	// the opposite: each item alone is affordable, together they overdraw cy (800 now)
	wantStatus(t, post(t, srv, "/payments", tk["cy"], newKey(), `{"to_handle":"ada","amount":600}`), 201) // cy 800
	// raising p_z 300 -> 900 debits cy 600; lowering p_y 900 -> 500 debits cy 400: 1000 > 800
	wantErr(t, batchPost(t, srv, tk["dee"], newKey(), batchBody(bItem{"p_z", 1, 900, "2026-09-20T12:00:00Z"}, bItem{"p_y", 2, 500, "2026-09-20T11:00:00Z"})), 409, "insufficient_funds")
	if bal(t, srv, tk["cy"], "") != 800 {
		t.Fatal("rollback")
	}
}

// Historical overdraft created only by the combination: each item alone keeps history
// nonnegative, together bob dips below zero at an intermediate boundary.
func TestBatchHistoricalOverdraftFromTheCombination(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:500", "bob:0", "cy:1000", "dee:0"}, `
	 {"id":"p_in1","from_user_id":"u_ada","to_user_id":"u_bob","amount":250,"created_at":"2026-09-20T10:00:00Z"},
	 {"id":"p_in2","from_user_id":"u_ada","to_user_id":"u_bob","amount":250,"created_at":"2026-09-20T10:30:00Z"},
	 {"id":"p_out","from_user_id":"u_bob","to_user_id":"u_cy","amount":250,"created_at":"2026-09-20T11:00:00Z"},
	 {"id":"p_out2","from_user_id":"u_bob","to_user_id":"u_cy","amount":250,"created_at":"2026-09-20T13:00:00Z"}`, `,"settlement_operator_ids":["u_dee"]`)
	// moving either receipt to 12:00 alone is fine (bob still has 250 at 11:00); both → -250 at 11:00
	later := "2026-09-20T12:00:00Z"
	k := newKey()
	wantErr(t, batchPost(t, srv, tk["dee"], k, batchBody(bItem{"p_in1", 1, 250, later}, bItem{"p_in2", 1, 250, later})), 409, "historical_overdraft")
	for _, id := range []string{"p_in1", "p_in2"} {
		if n := len(wantStatus(t, do(t, srv, "GET", "/payments/"+id+"/revisions", tk["ada"], ""), 200).body["revisions"].([]any)); n != 1 {
			t.Fatal("rollback")
		}
	}
	// insufficient funds beats historical overdraft: bob holds 0 now; reducing a receipt and moving
	// it late is both unaffordable now and overdrawn in the past
	wantErr(t, batchPost(t, srv, tk["dee"], newKey(), batchBody(bItem{"p_in2", 1, 100, later}, bItem{"p_in1", 1, 250, later})), 409, "insufficient_funds")
	wantStatus(t, batchPost(t, srv, tk["dee"], k, batchBody(bItem{"p_in1", 1, 250, later})), 201) // the failed key is reusable
}

func TestBatchSnapshotsReceiptsAndImport(t *testing.T) {
	w := operatorWorld(t)
	s := wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":100}]}`), 201)
	sKey := newKey()
	s2 := wantStatus(t, post(t, w.srv, "/settlements", w.cy, sKey, `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":70}]}`), 201)
	snap := wantStatus(t, do(t, w.srv, "GET", "/statement", w.ada, ""), 200)
	m := s.body["payments"].([]any)[0].(map[string]any)
	m2 := s2.body["payments"].([]any)[0].(map[string]any)
	bKey := newKey()
	bBody := batchBody(bItem{m["payment_id"].(string), 1, 0, s.body["committed_at"].(string)}, bItem{m2["payment_id"].(string), 1, 7, s2.body["committed_at"].(string)})
	b := wantStatus(t, batchPost(t, w.srv, w.cy, bKey, bBody), 201)
	// the original settlement retry is byte-identical; the snapshot is frozen; new statements change
	if rs := wantStatus(t, post(t, w.srv, "/settlements", w.cy, sKey, `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":70}]}`), 200); rs.raw != s2.raw {
		t.Fatal("settlement receipt changed")
	}
	if again := wantStatus(t, do(t, w.srv, "GET", "/statement?snapshot="+snap.body["snapshot"].(string), w.ada, ""), 200); again.raw != snap.raw {
		t.Fatal("snapshot changed")
	}
	if live := wantStatus(t, do(t, w.srv, "GET", "/statement", w.ada, ""), 200); num(live.body["closing_balance"]) != 10000-7 {
		t.Fatal(live.raw)
	}
	// export → import: the batch replays identically, revisions keep their batch id
	exp := wantStatus(t, do(t, w.srv, "GET", "/_test/export", "", ""), 200)
	dst := httptest.NewServer(app.New())
	t.Cleanup(dst.Close)
	wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
	if rp := wantStatus(t, batchPost(t, dst, w.cy, bKey, bBody), 200); rp.raw != b.raw {
		t.Fatal("batch receipt changed after import")
	}
	if again := wantStatus(t, do(t, dst, "GET", "/statement?snapshot="+snap.body["snapshot"].(string), w.ada, ""), 200); again.raw != snap.raw {
		t.Fatal("snapshot changed after import")
	}
	// a new batch id never collides with an imported one
	p := wantStatus(t, post(t, dst, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":5}`), 201)
	b2 := wantStatus(t, batchPost(t, dst, w.cy, newKey(), batchBody(bItem{p.body["payment_id"].(string), 1, 4, p.body["created_at"].(string)})), 201)
	if b2.body["correction_batch_id"] == b.body["correction_batch_id"] {
		t.Fatal("batch id collision")
	}
	// invalid import states → 422
	var doc map[string]any
	_ = json.Unmarshal([]byte(exp.raw), &doc)
	for _, x := range doc["state"].(map[string]any)["payments"].([]any) {
		x := x.(map[string]any)
		if x["id"] == m["payment_id"] {
			revs := x["revisions"].([]any)
			revs[1].(map[string]any)["recorded_at"] = "2026-01-01T00:00:00Z" // differs from its batch partner
		}
	}
	bad, _ := json.Marshal(doc)
	wantErr(t, do(t, httptest.NewServer(app.New()), "POST", "/_test/import", "", string(bad)), 422, "validation_failed")
}

func TestBatchSharedRecordedAtStrictlyLater(t *testing.T) {
	pinClock(t, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)) // frozen: only the stamp clock advances
	srv, tk := opWorld(t, `{"id":"p_a","from_user_id":"u_ada","to_user_id":"u_bob","amount":300,"created_at":"2026-09-21T10:00:00Z"},
	 {"id":"p_b","from_user_id":"u_ada","to_user_id":"u_bob","amount":200,"created_at":"2026-09-21T11:00:00Z"}`)
	c := wantStatus(t, correct(t, srv, tk["ada"], "p_b", newKey(), corr(1, 210, "2026-09-21T11:00:00Z", "x")), 201)
	b := wantStatus(t, batchPost(t, srv, tk["cy"], newKey(), batchBody(bItem{"p_a", 1, 250, "2026-09-21T10:00:00Z"}, bItem{"p_b", 2, 220, "2026-09-21T11:00:00Z"})), 201)
	if !(b.body["recorded_at"].(string) > c.body["recorded_at"].(string)) {
		t.Fatalf("batch %v not after the member's previous %v", b.body["recorded_at"], c.body["recorded_at"])
	}
	// known_at = the batch instant - 1µs sees the previous revisions only
	rt, _ := time.Parse(time.RFC3339Nano, b.body["recorded_at"].(string))
	k := rt.Add(-time.Microsecond).UTC().Format(time.RFC3339Nano)
	// ada: 10000 - 10 (single) = 9990 before the batch; +50 -10 = 10030 with it
	if bal(t, srv, tk["ada"], qs("known_at", k)) != 9990 || bal(t, srv, tk["ada"], qs("known_at", b.body["recorded_at"].(string))) != 10030 {
		t.Fatal("known_at around the shared recorded_at")
	}
}

func TestBatchRaces(t *testing.T) {
	w := operatorWorld(t)
	var ps []resp
	for i := 0; i < 6; i++ {
		ps = append(ps, wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), fmt.Sprintf(`{"to_handle":"bob","amount":%d}`, 100+i)), 201))
	}
	id := func(i int) string { return ps[i].body["payment_id"].(string) }
	at := func(i int) string { return ps[i].body["created_at"].(string) }
	snap := wantStatus(t, do(t, w.srv, "GET", "/statement", w.ada, ""), 200)
	// same batch, same key: one 201, the rest 200 identical
	k := newKey()
	body := batchBody(bItem{id(0), 1, 50, at(0)}, bItem{id(1), 1, 50, at(1)})
	out := make([]resp, 50)
	parallel(50, func(i int) { out[i] = batchPost(t, w.srv, w.cy, k, body) })
	n201 := 0
	for _, r := range out {
		if r.status == 201 {
			n201++
		} else if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	if n201 != 1 {
		t.Fatalf("201=%d", n201)
	}
	// overlapping batches sharing payment 2's expected revision 1, plus single corrections of it
	parallel(50, func(i int) {
		if i%5 == 0 {
			out[i] = correct(t, w.srv, w.ada, id(2), newKey(), corr(1, int64(10+i), at(2), "single"))
			return
		}
		out[i] = batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{id(2), 1, int64(20 + i), at(2)}, bItem{id(3 + i%3), 1, int64(30 + i), at(3 + i%3)}))
	})
	n201 = 0
	for _, r := range out {
		switch {
		case r.status == 201:
			n201++
		case r.status == 409 && strings.Contains(r.raw, "stale_revision"):
		default:
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	revs := wantStatus(t, do(t, w.srv, "GET", "/payments/"+id(2)+"/revisions", w.ada, ""), 200).body["revisions"].([]any)
	if n201 != 1 || len(revs) != 2 {
		t.Fatalf("exactly one winner on a shared expected revision: 201=%d revisions=%d", n201, len(revs))
	}
	// batches racing payments, refunds, settlements and reads
	start := time.Now()
	parallel(50, func(i int) {
		var r resp
		switch i % 5 {
		case 0:
			q := post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":7}`)
			if q.status == 201 {
				r = batchPost(t, w.srv, w.cy, newKey(), batchBody(bItem{q.body["payment_id"].(string), 1, 3, q.body["created_at"].(string)}))
			}
		case 1:
			r = refund(t, w.srv, w.bob, id(5), newKey(), amt(1))
		case 2:
			r = post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"bob","to_handle":"ada","amount":1}]}`)
		case 3:
			r = do(t, w.srv, "GET", "/statement?limit=10", w.ada, "")
		case 4:
			r = do(t, w.srv, "GET", "/me", w.bob, "")
			if r.status == 200 && (num(r.body["available"]) < 0 || num(r.body["total"]) < 0) {
				t.Errorf("negative read %s", r.raw)
			}
		}
		if r.status >= 500 {
			t.Errorf("5xx %s", r.raw)
		}
	})
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("races took %v", d)
	}
	if total(t, w) != 13000 {
		t.Fatal("conservation")
	}
	if again := wantStatus(t, do(t, w.srv, "GET", "/statement?snapshot="+snap.body["snapshot"].(string), w.ada, ""), 200); again.raw != snap.raw {
		t.Fatal("snapshot changed")
	}
	// naive past-boundary scan: total and available never negative at any event instant
	var instants []string
	for _, tok := range []string{w.ada, w.bob, w.cy} {
		for _, e := range wantStatus(t, do(t, w.srv, "GET", "/statement?limit=200", tok, ""), 200).body["entries"].([]any) {
			instants = append(instants, e.(map[string]any)["effective_at"].(string))
		}
	}
	for _, tok := range []string{w.ada, w.bob, w.cy} {
		for _, in := range instants {
			r := wantStatus(t, do(t, w.srv, "GET", "/me"+qs("as_of", in), tok, ""), 200)
			if num(r.body["total"]) < 0 || num(r.body["available"]) < 0 {
				t.Fatalf("negative past view at %s: %s", in, r.raw)
			}
		}
	}
	_ = reflect.DeepEqual
}

func TestBatchOf32OnALargeWallet(t *testing.T) {
	var ps []string
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10000; i++ {
		from, to := "ada", "bob"
		if i%2 == 1 {
			from, to = "bob", "ada"
		}
		ps = append(ps, fmt.Sprintf(`{"id":"p%05d","from_user_id":"u_%s","to_user_id":"u_%s","amount":%d,"created_at":"%s"}`,
			i, from, to, 1+i%40, base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339)))
	}
	srv, tk := worldWith(t, []string{"ada:1000000", "bob:1000000", "cy:0"}, strings.Join(ps, ","), `,"settlement_operator_ids":["u_cy"]`)
	var items []bItem
	for i := 0; i < 32; i++ {
		n := i * 300
		items = append(items, bItem{fmt.Sprintf("p%05d", n), 1, int64(1 + n%40 + 1), base.Add(time.Duration(n) * time.Minute).Format(time.RFC3339)})
	}
	start := time.Now()
	r := wantStatus(t, batchPost(t, srv, tk["cy"], newKey(), batchBody(items...)), 201)
	d := time.Since(start)
	t.Logf("32-item batch on wallets with 10^4 payments: %v (%d revisions)", d, len(r.body["revisions"].([]any)))
	if d > time.Second {
		t.Fatalf("too slow: %v", d)
	}
}
