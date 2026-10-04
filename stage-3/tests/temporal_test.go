// Stage-3 work item A tests: payment timestamps, /me as_of/known_at, statements,
// snapshots, revisions, historical holds, closed_at, imports of real stage-1/stage-2
// exports, and a 10^4-payment performance check. The randomized oracle test is in
// oracle_test.go.
package tests

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pocketful/app"
)

func qs(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return "?" + v.Encode()
}

func num(v any) int64 { return int64(v.(float64)) }

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000-07:00") }

// seededWorld: ada 10000, bob 2500, cy 500 after two seeded payments at fixed instants.
// ada's opening = 10000 + 500 - 200 = 10300; bob's = 2500 - 500 + 200 = 2200.
func seededWorld(t *testing.T) (*httptest.Server, map[string]string) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 10000) + `,` + user("bob", 2500) + `,` + user("cy", 500) + `],
	 "payments":[{"id":"p_s1","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-20T10:00:00+00:00"},
	             {"id":"p_s2","from_user_id":"u_bob","to_user_id":"u_ada","amount":200,"created_at":"2026-09-21T12:00:00+02:00","visibility":"private"}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	toks := map[string]string{}
	for _, h := range []string{"ada", "bob", "cy"} {
		toks[h] = login(t, srv, h+"@example.com", "correct horse")
	}
	return srv, toks
}

func TestMeAsOfBoundaries(t *testing.T) {
	srv, tk := seededWorld(t)
	at := func(asOf string) resp {
		return wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", asOf), tk["ada"], ""), 200)
	}
	cases := map[string]int64{
		"2026-09-20T09:59:59.999999+00:00": 10300, // before everything: opening balance
		"2026-09-20T10:00:00+00:00":        9800,  // exactly at a payment: it counts (inclusive)
		"2026-09-20T12:00:00+02:00":        9800,  // same instant written with another offset
		"2026-09-21T09:59:59Z":             9800,
		"2026-09-21T10:00:00Z":             10000, // = 2026-09-21T12:00:00+02:00
		"2099-01-01T00:00:00+00:00":        10000, // future: the current balance
	}
	for asOf, want := range cases {
		r := at(asOf)
		if num(r.body["balance"]) != want || num(r.body["total"]) != want || num(r.body["available"]) != want || num(r.body["held"]) != 0 {
			t.Fatalf("as_of %s: %s", asOf, r.raw)
		}
		if r.body["as_of"] != asOf {
			t.Fatalf("as_of not echoed exactly: %v vs %s", r.body["as_of"], asOf)
		}
	}
	// the sum over all wallets is the seeded total in every historical view
	for _, asOf := range []string{"2000-01-01T00:00:00Z", "2026-09-20T10:00:00Z", "2026-09-21T10:00:00Z", "2099-01-01T00:00:00Z"} {
		var sum int64
		for _, h := range []string{"ada", "bob", "cy"} {
			sum += num(wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", asOf), tk[h], ""), 200).body["balance"])
		}
		if sum != 13000 {
			t.Fatalf("as_of %s: sum %d", asOf, sum)
		}
	}
	// plain /me is unchanged: no as_of / known_at keys
	r := wantStatus(t, do(t, srv, "GET", "/me", tk["ada"], ""), 200)
	if _, ok := r.body["as_of"]; ok || num(r.body["balance"]) != 10000 {
		t.Fatal(r.raw)
	}
	// a raw '+' in the query (decoded as a space) still works and is echoed with '+'
	r = wantStatus(t, do(t, srv, "GET", "/me?as_of=2026-09-20T10:00:00+00:00", tk["ada"], ""), 200)
	if r.body["as_of"] != "2026-09-20T10:00:00+00:00" || num(r.body["balance"]) != 9800 {
		t.Fatal(r.raw)
	}
	for _, bad := range []string{"", "2026-09-20", "2026-09-20T10:00:00", "yesterday", "2026-13-01T00:00:00Z", "1700000000"} {
		wantErr(t, do(t, srv, "GET", "/me"+qs("as_of", bad), tk["ada"], ""), 422, "validation_failed")
		wantErr(t, do(t, srv, "GET", "/me"+qs("known_at", bad), tk["ada"], ""), 422, "validation_failed")
	}
}

func TestKnownAtExcludesWhatWasNotYetRecorded(t *testing.T) {
	w := newWorld(t)
	before := iso(time.Now().Add(-time.Second))
	p := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":700}`), 201)
	created := p.body["created_at"].(string)
	r := wantStatus(t, do(t, w.srv, "GET", "/me"+qs("known_at", before), w.ada, ""), 200)
	if num(r.body["balance"]) != 10000 || r.body["known_at"] != before {
		t.Fatalf("payment recorded after known_at must not count: %s", r.raw)
	}
	r = wantStatus(t, do(t, w.srv, "GET", "/me"+qs("known_at", created), w.ada, ""), 200) // recorded at or before
	if num(r.body["balance"]) != 9300 {
		t.Fatal(r.raw)
	}
	r = wantStatus(t, do(t, w.srv, "GET", "/me"+qs("as_of", before, "known_at", "2099-01-01T00:00:00Z"), w.ada, ""), 200)
	if num(r.body["balance"]) != 10000 {
		t.Fatal(r.raw)
	}
	// statement with known_at before the payment: no entry, echo known_at
	s := wantStatus(t, do(t, w.srv, "GET", "/statement"+qs("known_at", before), w.ada, ""), 200)
	if len(s.body["entries"].([]any)) != 0 || s.body["known_at"] != before || num(s.body["closing_balance"]) != 10000 {
		t.Fatal(s.raw)
	}
}

func entryIDs(r resp) []string {
	var out []string
	for _, e := range r.body["entries"].([]any) {
		out = append(out, e.(map[string]any)["payment"].(map[string]any)["payment_id"].(string))
	}
	return out
}

func TestStatementWindowOrderAndPaging(t *testing.T) {
	srv, tk := seededWorld(t)
	// an API payment after reset; and a public payment between two others (bob -> cy)
	p3 := wantStatus(t, post(t, srv, "/payments", tk["ada"], newKey(), `{"to_handle":"cy","amount":100}`), 201)
	wantStatus(t, post(t, srv, "/payments", tk["bob"], newKey(), `{"to_handle":"cy","amount":50,"visibility":"public"}`), 201)
	full := wantStatus(t, do(t, srv, "GET", "/statement", tk["ada"], ""), 200)
	if got := entryIDs(full); !reflect.DeepEqual(got, []string{"p_s1", "p_s2", p3.body["payment_id"].(string)}) {
		t.Fatalf("oldest first, only ada's payments: %v", got)
	}
	if num(full.body["opening_balance"]) != 10300 || num(full.body["closing_balance"]) != 9900 || full.body["has_more"] != false {
		t.Fatal(full.raw)
	}
	tok, _ := full.body["snapshot"].(string)
	if tok == "" {
		t.Fatal("first response must carry a snapshot token")
	}
	var deltas, bals []int64
	for _, e := range full.body["entries"].([]any) {
		e := e.(map[string]any)
		deltas = append(deltas, num(e["delta"]))
		bals = append(bals, num(e["balance_after"]))
		for _, k := range []string{"revision", "effective_at", "recorded_at"} {
			if _, ok := e[k]; !ok {
				t.Fatalf("entry lacks %s", k)
			}
		}
		pm := e["payment"].(map[string]any)
		if e["effective_at"] != pm["created_at"] || e["recorded_at"] != pm["created_at"] || num(e["revision"]) != 1 {
			t.Fatalf("revision 1 is effective = recorded = created_at: %v", e)
		}
	}
	if !reflect.DeepEqual(deltas, []int64{-500, 200, -100}) || !reflect.DeepEqual(bals, []int64{9800, 10000, 9900}) {
		t.Fatalf("deltas %v balances %v", deltas, bals)
	}
	// half-open window [from, to): from inclusive, to exclusive
	w := wantStatus(t, do(t, srv, "GET", "/statement"+qs("from", "2026-09-20T10:00:00Z", "to", "2026-09-21T10:00:00Z"), tk["ada"], ""), 200)
	if !reflect.DeepEqual(entryIDs(w), []string{"p_s1"}) || num(w.body["opening_balance"]) != 10300 || num(w.body["closing_balance"]) != 9800 {
		t.Fatal(w.raw)
	}
	w = wantStatus(t, do(t, srv, "GET", "/statement"+qs("from", "2026-09-20T10:00:00.000001Z"), tk["ada"], ""), 200)
	if entryIDs(w)[0] != "p_s2" || num(w.body["opening_balance"]) != 9800 {
		t.Fatal(w.raw)
	}
	e := wantStatus(t, do(t, srv, "GET", "/statement"+qs("from", "2026-09-21T10:00:00Z", "to", "2026-09-21T10:00:00Z"), tk["ada"], ""), 200)
	if len(e.body["entries"].([]any)) != 0 || num(e.body["opening_balance"]) != num(e.body["closing_balance"]) {
		t.Fatal(e.raw)
	}
	wantErr(t, do(t, srv, "GET", "/statement"+qs("from", "2026-09-22T00:00:00Z", "to", "2026-09-21T00:00:00Z"), tk["ada"], ""), 422, "validation_failed")
	for _, q := range []string{"from=", "to=2026-09-21", "known_at=x", "limit=0", "limit=201", "offset=-1", "limit=4.0"} {
		wantErr(t, do(t, srv, "GET", "/statement?"+q, tk["ada"], ""), 422, "validation_failed")
	}
	wantErr(t, do(t, srv, "GET", "/statement", "", ""), 401, "unauthenticated")
	// paging never changes balances; has_more on the last partial page and beyond the end
	for _, pg := range []struct {
		limit, offset, n int
		more             bool
	}{{2, 0, 2, true}, {2, 2, 1, false}, {2, 3, 0, false}, {2, 99, 0, false}, {1, 1, 1, true}} {
		r := wantStatus(t, do(t, srv, "GET", fmt.Sprintf("/statement?limit=%d&offset=%d", pg.limit, pg.offset), tk["ada"], ""), 200)
		if len(r.body["entries"].([]any)) != pg.n || r.body["has_more"] != pg.more ||
			num(r.body["opening_balance"]) != 10300 || num(r.body["closing_balance"]) != 9900 {
			t.Fatalf("page %+v: %s", pg, r.raw)
		}
		if pg.n > 0 && num(r.body["entries"].([]any)[0].(map[string]any)["balance_after"]) != bals[pg.offset] {
			t.Fatalf("balance_after changed by paging: %s", r.raw)
		}
	}
	// cy's statement contains the bob -> cy public payment; ada's never does
	c := wantStatus(t, do(t, srv, "GET", "/statement", tk["cy"], ""), 200)
	if len(c.body["entries"].([]any)) != 2 {
		t.Fatal(c.raw)
	}
}

func TestStatementTiesOrderByPaymentID(t *testing.T) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1000) + `,` + user("bob", 1000) + `],
	 "payments":[{"id":"p_b","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"created_at":"2026-09-20T10:00:00Z"},
	             {"id":"p_a","from_user_id":"u_bob","to_user_id":"u_ada","amount":2,"created_at":"2026-09-20T12:00:00+02:00"},
	             {"id":"p_c","from_user_id":"u_ada","to_user_id":"u_bob","amount":4}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	r := wantStatus(t, do(t, srv, "GET", "/statement", ada, ""), 200)
	if got := entryIDs(r); !reflect.DeepEqual(got, []string{"p_a", "p_b", "p_c"}) {
		t.Fatalf("equal instants order by id; omitted created_at = reset time, last: %v", got)
	}
	// opening = 1000 + 1 - 2 + 4 = 1003; at 10:00Z both p_a and p_b have happened
	at := wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", "2026-09-20T10:00:00Z"), ada, ""), 200)
	if num(at.body["balance"]) != 1004 {
		t.Fatal(at.raw)
	}
	at = wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", "2026-09-20T09:59:59Z"), ada, ""), 200)
	if num(at.body["balance"]) != 1003 {
		t.Fatal(at.raw)
	}
}

func TestSeededCreatedAtRules(t *testing.T) {
	srv := setup(t)
	ada := login(t, srv, "ada@example.com", "correct horse")
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1000) + `,` + user("bob", 1000) + `],
	 "payments":[{"id":"p1","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"created_at":"` + future + `"}]}`
	wantErr(t, do(t, srv, "POST", "/_test/reset", "", f), 422, "validation_failed")
	for _, bad := range []string{`"2026-09-20"`, `"2026-09-20T10:00:00"`, `5`} {
		f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1000) + `,` + user("bob", 1000) + `],
		 "payments":[{"id":"p1","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"created_at":` + bad + `}]}`
		wantErr(t, do(t, srv, "POST", "/_test/reset", "", f), 422, "validation_failed")
	}
	if me(t, srv, ada).total != 10000 {
		t.Fatal("a rejected fixture changed state")
	}
	// omitted created_at = reset time, before any later API payment; balances unchanged by loading
	f = `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1000) + `,` + user("bob", 1000) + `],
	 "payments":[{"id":"p_z","from_user_id":"u_ada","to_user_id":"u_bob","amount":7}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	ada = login(t, srv, "ada@example.com", "correct horse")
	if me(t, srv, ada).total != 1000 {
		t.Fatal("loading seeded payments changed the balance")
	}
	p := wantStatus(t, post(t, srv, "/payments", ada, newKey(), `{"to_handle":"bob","amount":3}`), 201)
	s := wantStatus(t, do(t, srv, "GET", "/statement", ada, ""), 200)
	if got := entryIDs(s); !reflect.DeepEqual(got, []string{"p_z", p.body["payment_id"].(string)}) || num(s.body["opening_balance"]) != 1007 {
		t.Fatalf("%v %s", got, s.raw)
	}
	// a new account opens at zero
	r := wantStatus(t, do(t, srv, "POST", "/auth/signup", "", `{"email":"dee@example.com","password":"correct horse","display_name":"Dee"}`), 201)
	ds := wantStatus(t, do(t, srv, "GET", "/statement", r.body["token"].(string), ""), 200)
	if num(ds.body["opening_balance"]) != 0 || num(ds.body["closing_balance"]) != 0 {
		t.Fatal(ds.raw)
	}
}

func TestSnapshots(t *testing.T) {
	srv, tk := seededWorld(t)
	first := wantStatus(t, do(t, srv, "GET", "/statement?limit=1", tk["ada"], ""), 200)
	tok := first.body["snapshot"].(string)
	all := wantStatus(t, do(t, srv, "GET", "/statement", tk["ada"], ""), 200)
	// later payments do not change the snapshot
	wantStatus(t, post(t, srv, "/payments", tk["ada"], newKey(), `{"to_handle":"bob","amount":10}`), 201)
	wantStatus(t, post(t, srv, "/payments", tk["bob"], newKey(), `{"to_handle":"ada","amount":20}`), 201)
	var paged []any
	for off := 0; off < 5; off++ {
		r := wantStatus(t, do(t, srv, "GET", fmt.Sprintf("/statement?snapshot=%s&limit=1&offset=%d", tok, off), tk["ada"], ""), 200)
		if num(r.body["opening_balance"]) != 10300 || num(r.body["closing_balance"]) != 10000 || r.body["snapshot"] != tok {
			t.Fatal(r.raw)
		}
		if want := off < 1; r.body["has_more"] != want {
			t.Fatalf("offset %d has_more %v", off, r.body["has_more"])
		}
		paged = append(paged, r.body["entries"].([]any)...)
		if len(r.body["entries"].([]any)) == 0 {
			break
		}
	}
	// limit 1 offset 0 -> has_more true; offset 1 -> the second entry, has_more false
	r := wantStatus(t, do(t, srv, "GET", "/statement?snapshot="+tok+"&limit=5", tk["ada"], ""), 200)
	if len(r.body["entries"].([]any)) != 2 || r.body["has_more"] != false {
		t.Fatal(r.raw)
	}
	if !reflect.DeepEqual(r.body["entries"], all.body["entries"]) {
		t.Fatal("snapshot entries differ from the original read")
	}
	_ = paged
	// the live statement now differs
	if live := wantStatus(t, do(t, srv, "GET", "/statement", tk["ada"], ""), 200); len(live.body["entries"].([]any)) != 4 {
		t.Fatal(live.raw)
	}
	for _, extra := range []string{"&from=2026-01-01T00:00:00Z", "&to=2027-01-01T00:00:00Z", "&known_at=2027-01-01T00:00:00Z", "&from="} {
		wantErr(t, do(t, srv, "GET", "/statement?snapshot="+tok+extra, tk["ada"], ""), 422, "validation_failed")
	}
	wantErr(t, do(t, srv, "GET", "/statement?snapshot=ss_nope&from=2026-01-01T00:00:00Z", tk["ada"], ""), 422, "validation_failed")
	wantErr(t, do(t, srv, "GET", "/statement?snapshot=ss_nope", tk["ada"], ""), 404, "not_found")
	wantErr(t, do(t, srv, "GET", "/statement?snapshot="+tok, tk["bob"], ""), 404, "not_found")
	wantErr(t, do(t, srv, "GET", "/statement?snapshot="+tok+"&limit=0", tk["ada"], ""), 422, "validation_failed")
	wantStatus(t, do(t, srv, "GET", "/statement?snapshot="+tok+"&colour=blue", tk["ada"], ""), 200)
	// a snapshot of a known_at read keeps echoing it
	k := wantStatus(t, do(t, srv, "GET", "/statement"+qs("known_at", "2026-09-20T23:00:00Z"), tk["ada"], ""), 200)
	kp := wantStatus(t, do(t, srv, "GET", "/statement?snapshot="+k.body["snapshot"].(string), tk["ada"], ""), 200)
	if kp.body["known_at"] != "2026-09-20T23:00:00Z" || len(kp.body["entries"].([]any)) != 1 {
		t.Fatal(kp.raw)
	}
	// reset kills tokens
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", std), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	wantErr(t, do(t, srv, "GET", "/statement?snapshot="+tok, ada, ""), 404, "not_found")
}

func TestRevisionsEndpoint(t *testing.T) {
	srv, tk := seededWorld(t)
	r := wantStatus(t, do(t, srv, "GET", "/payments/p_s1/revisions", tk["bob"], ""), 200)
	revs := r.body["revisions"].([]any)
	want := map[string]any{"payment_id": "p_s1", "revision": 1.0, "amount": 500.0, "effective_at": "2026-09-20T10:00:00+00:00",
		"recorded_at": "2026-09-20T10:00:00+00:00", "reason": ""}
	if len(revs) != 1 || !reflect.DeepEqual(revs[0], want) {
		t.Fatal(r.raw)
	}
	pub := wantStatus(t, post(t, srv, "/payments", tk["ada"], newKey(), `{"to_handle":"bob","amount":5,"visibility":"public"}`), 201)
	id := pub.body["payment_id"].(string)
	wantErr(t, do(t, srv, "GET", "/payments/"+id+"/revisions", tk["cy"], ""), 404, "not_found")
	wantErr(t, do(t, srv, "GET", "/payments/nope/revisions", tk["ada"], ""), 404, "not_found")
	wantErr(t, do(t, srv, "GET", "/payments/"+id+"/revisions", "", ""), 401, "unauthenticated")
	r = wantStatus(t, do(t, srv, "GET", "/payments/"+id+"/revisions", tk["ada"], ""), 200)
	rv := r.body["revisions"].([]any)[0].(map[string]any)
	if rv["effective_at"] != pub.body["created_at"] || rv["recorded_at"] != pub.body["created_at"] {
		t.Fatal(r.raw)
	}
}

func TestSettlementMembersAndMicroTimestamps(t *testing.T) {
	w := operatorWorld(t)
	s := wantStatus(t, post(t, w.srv, "/settlements", w.cy, newKey(), `{"transfers":[{"from_handle":"ada","to_handle":"bob","amount":10},{"from_handle":"bob","to_handle":"ada","amount":3}]}`), 201)
	at := s.body["committed_at"].(string)
	if !strings.Contains(at, ".") {
		t.Fatalf("stage-3 event times carry microseconds: %s", at)
	}
	for _, p := range s.body["payments"].([]any) {
		id := p.(map[string]any)["payment_id"].(string)
		rv := wantStatus(t, do(t, w.srv, "GET", "/payments/"+id+"/revisions", w.ada, ""), 200).body["revisions"].([]any)[0].(map[string]any)
		if rv["effective_at"] != at || rv["recorded_at"] != at {
			t.Fatalf("member revision 1 must use committed_at: %v", rv)
		}
	}
	// stamps strictly increase even within one microsecond burst
	var prev string
	for i := 0; i < 20; i++ {
		c := wantStatus(t, post(t, w.srv, "/payments", w.ada, newKey(), `{"to_handle":"bob","amount":1}`), 201).body["created_at"].(string)
		if c <= prev {
			t.Fatalf("not increasing: %s then %s", prev, c)
		}
		prev = c
	}
}

func TestHistoricalHoldsAndClosedAt(t *testing.T) {
	w := ttlWorld(t, 2)
	a1 := wantStatus(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"bob","amount":1000}`), 201)
	if v, ok := a1.body["closed_at"]; !ok || v != nil {
		t.Fatalf("closed_at null while open: %s", a1.raw)
	}
	id1 := a1.body["authorization_id"].(string)
	created := a1.body["created_at"].(string)
	c1 := wantStatus(t, post(t, w.srv, "/authorizations/"+id1+"/capture", w.bob, newKey(), `{"amount":300,"final":false}`), 201)
	capAt := c1.body["created_at"].(string)
	a2 := wantStatus(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"cy","amount":500}`), 201)
	id2 := a2.body["authorization_id"].(string)
	v := wantStatus(t, post(t, w.srv, "/authorizations/"+id2+"/void", w.ada, "", ""), 200)
	voidAt, _ := v.body["closed_at"].(string)
	if voidAt == "" || !strings.Contains(voidAt, ".") {
		t.Fatal(v.raw)
	}
	a3 := wantStatus(t, post(t, w.srv, "/authorizations", w.ada, newKey(), `{"to_handle":"cy","amount":200}`), 201)
	id3 := a3.body["authorization_id"].(string)
	c3 := wantStatus(t, post(t, w.srv, "/authorizations/"+id3+"/capture", w.cy, newKey(), `{"amount":50}`), 201) // final
	view := func(asOf, knownAt string) wallet {
		q := qs("as_of", asOf)
		if knownAt != "" {
			q = qs("as_of", asOf, "known_at", knownAt)
		}
		r := wantStatus(t, do(t, w.srv, "GET", "/me"+q, w.ada, ""), 200)
		got := wallet{num(r.body["total"]), num(r.body["available"]), num(r.body["held"]), num(r.body["balance"])}
		if got.balance != got.total || got.available != got.total-got.held {
			t.Fatalf("inconsistent view %+v", got)
		}
		return got
	}
	tm := func(s string) time.Time { x, _ := time.Parse(time.RFC3339Nano, s); return x }
	just := func(s string, d time.Duration) string { return tm(s).Add(d).UTC().Format(time.RFC3339Nano) }
	// before creation: nothing held
	if g := view(just(created, -time.Microsecond), ""); g.held != 0 || g.total != 10000 {
		t.Fatalf("%+v", g)
	}
	// right after the partial capture of a1: 1000-300 = 700 held by a1 (a2/a3 later)
	if g := view(capAt, ""); g.total != 9700 || g.held < 700 {
		t.Fatalf("%+v", g)
	}
	// after a1's capture is known but known_at before it: capture not counted yet
	if g := view(capAt, just(capAt, -time.Microsecond)); g.total != 10000 {
		t.Fatalf("capture not yet known: %+v", g)
	}
	// after the void and the final capture: only a1's remaining 700 is held
	after := c3.body["created_at"].(string)
	if g := view(after, ""); g != (wallet{9650, 8950, 700, 9650}) {
		t.Fatalf("%+v", g)
	}
	// a3 is closed at its capture time with closed_at = capture created_at
	l := wantStatus(t, do(t, w.srv, "GET", "/authorizations", w.ada, ""), 200).body["authorizations"].([]any)
	closed := map[string]any{}
	for _, x := range l {
		x := x.(map[string]any)
		closed[x["authorization_id"].(string)] = x["closed_at"]
	}
	if closed[id3] != after || closed[id2] != voidAt || closed[id1] != nil {
		t.Fatalf("closed_at: %v", closed)
	}
	// queries beyond now: a1 expires at its deadline (known since creation)
	exp := a1.body["expires_at"].(string)
	if g := view(just(exp, -time.Microsecond), ""); g.held != 700 {
		t.Fatalf("%+v", g)
	}
	if g := view(exp, ""); g.held != 0 || g.available != 9650 {
		t.Fatalf("expires at its deadline: %+v", g)
	}
	time.Sleep(2100 * time.Millisecond)
	l = wantStatus(t, do(t, w.srv, "GET", "/authorizations?status=expired", w.ada, ""), 200).body["authorizations"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["closed_at"] != exp {
		t.Fatalf("expiry closed_at = expires_at: %v", l)
	}
	// statements contain money movements only; each capture once, with its link
	s := wantStatus(t, do(t, w.srv, "GET", "/statement", w.ada, ""), 200)
	links := 0
	for _, e := range s.body["entries"].([]any) {
		if e.(map[string]any)["payment"].(map[string]any)["authorization_id"] != nil {
			links++
		}
	}
	if len(s.body["entries"].([]any)) != 2 || links != 2 {
		t.Fatal(s.raw)
	}
}

func TestSeededHoldsHistory(t *testing.T) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 10000) + `,` + user("bob", 0) + `],
	 "authorizations":[
	  {"id":"a_open","from_user_id":"u_ada","to_user_id":"u_bob","amount":2000,"status":"open","expires_at":"` + future + `"},
	  {"id":"a_old","from_user_id":"u_ada","to_user_id":"u_bob","amount":1000,"status":"open","created_at":"2026-09-20T00:00:00Z","expires_at":"` + future + `"},
	  {"id":"a_v","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"status":"voided","expires_at":"` + future + `","closed_at":"2026-09-21T00:00:00Z"}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	at := func(asOf string) int64 {
		return num(wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", asOf), ada, ""), 200).body["held"])
	}
	if at("2026-09-20T12:00:00Z") != 1000 || at("2099-01-01T00:00:00Z") != 0 {
		t.Fatal("seeded created_at and expiry")
	}
	now := num(wantStatus(t, do(t, srv, "GET", "/me", ada, ""), 200).body["held"])
	if now != 3000 || at(time.Now().UTC().Format(time.RFC3339Nano)) != 3000 {
		t.Fatalf("held now %d", now)
	}
	futureCreated := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	bad := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 10000) + `,` + user("bob", 0) + `],
	 "authorizations":[{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":1,"status":"open","created_at":"` + futureCreated + `","expires_at":"` + future + `"}]}`
	wantErr(t, do(t, srv, "POST", "/_test/reset", "", bad), 422, "validation_failed")
}

// startStage builds and runs another stage's service from ../../<dir> as a separate process.
func startStage(t *testing.T, dir string) string {
	t.Helper()
	if dir == "stage-1" {
		return startStageOne(t)
	}
	tdir := t.TempDir()
	bin := tdir + "/svc"
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join("..", "..", dir)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build %s: %v %s", dir, err, out)
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
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; i < 100; i++ {
		if r, err := http.Get(u + "/health"); err == nil {
			r.Body.Close()
			return u
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(dir + " did not become healthy")
	return ""
}

func TestImportOlderStagesBuildsHistory(t *testing.T) {
	for _, dir := range []string{"stage-1", "stage-2"} {
		t.Run(dir, func(t *testing.T) {
			src := &httptest.Server{URL: startStage(t, dir)}
			f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 10000) + `,` + user("bob", 2500) + `,` + user("cy", 500) + `],
			 "payments":[{"id":"p_s1","from_user_id":"u_ada","to_user_id":"u_bob","amount":500,"created_at":"2026-09-20T10:00:00+00:00"}],
			 "settlement_operator_ids":["u_cy"]}`
			wantStatus(t, do(t, src, "POST", "/_test/reset", "", f), 204)
			ada := login(t, src, "ada@example.com", "correct horse")
			bob := login(t, src, "bob@example.com", "correct horse")
			cy := login(t, src, "cy@example.com", "correct horse")
			k := newKey()
			pay := wantStatus(t, post(t, src, "/payments", ada, k, `{"to_handle":"bob","amount":100}`), 201)
			set := wantStatus(t, post(t, src, "/settlements", cy, newKey(), `{"transfers":[{"from_handle":"bob","to_handle":"ada","amount":40}]}`), 201)
			var holdID string
			if dir == "stage-2" {
				a := wantStatus(t, post(t, src, "/authorizations", ada, newKey(), `{"to_handle":"bob","amount":300}`), 201)
				holdID = a.body["authorization_id"].(string)
				wantStatus(t, post(t, src, "/authorizations/"+holdID+"/capture", bob, newKey(), `{"amount":100,"final":false}`), 201)
			}
			exp := wantStatus(t, do(t, src, "GET", "/_test/export", "", ""), 200)
			dst := httptest.NewServer(app.New())
			t.Cleanup(dst.Close)
			wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
			cur := me(t, dst, ada)
			s := wantStatus(t, do(t, dst, "GET", "/statement", ada, ""), 200)
			// opening = current balance - net of every payment; statement arithmetic closes
			sum := num(s.body["opening_balance"])
			for _, e := range s.body["entries"].([]any) {
				sum += num(e.(map[string]any)["delta"])
			}
			if num(s.body["opening_balance"]) != 10500 || sum != num(s.body["closing_balance"]) || num(s.body["closing_balance"]) != cur.total {
				t.Fatalf("%s: %s", dir, s.raw)
			}
			// revision 1 synthesized from created_at (settlement member: committed_at)
			rv := wantStatus(t, do(t, dst, "GET", "/payments/"+pay.body["payment_id"].(string)+"/revisions", ada, ""), 200).body["revisions"].([]any)[0].(map[string]any)
			if rv["effective_at"] != pay.body["created_at"] || rv["recorded_at"] != pay.body["created_at"] || rv["reason"] != "" {
				t.Fatal(rv)
			}
			mem := set.body["payments"].([]any)[0].(map[string]any)["payment_id"].(string)
			rv = wantStatus(t, do(t, dst, "GET", "/payments/"+mem+"/revisions", bob, ""), 200).body["revisions"].([]any)[0].(map[string]any)
			if rv["effective_at"] != set.body["committed_at"] {
				t.Fatal(rv)
			}
			// historical /me works on imported data; the receipt replays; the sum is conserved
			if num(wantStatus(t, do(t, dst, "GET", "/me"+qs("as_of", "2026-09-20T09:00:00Z"), ada, ""), 200).body["balance"]) != 10500 {
				t.Fatal("opening as_of")
			}
			r := wantStatus(t, post(t, dst, "/payments", ada, k, `{"to_handle":"bob","amount":100}`), 200)
			if r.raw != pay.raw {
				t.Fatal("receipt replay changed")
			}
			if dir == "stage-2" {
				a := wantStatus(t, do(t, dst, "GET", "/authorizations", ada, ""), 200).body["authorizations"].([]any)[0].(map[string]any)
				if a["closed_at"] != nil || num(a["remaining_amount"]) != 200 || cur.held != 200 {
					t.Fatal(a)
				}
			}
			var total int64
			for _, tok := range []string{ada, bob, cy} {
				total += me(t, dst, tok).total
			}
			if total != 13000 {
				t.Fatal("conservation after import")
			}
		})
	}
}

func TestExportImportCarriesStageThreeState(t *testing.T) {
	srv, tk := seededWorld(t)
	first := wantStatus(t, do(t, srv, "GET", "/statement?limit=1", tk["ada"], ""), 200)
	exp := wantStatus(t, do(t, srv, "GET", "/_test/export", "", ""), 200)
	dst := httptest.NewServer(app.New())
	t.Cleanup(dst.Close)
	wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
	// snapshots survive the import; openings and revisions too
	r := wantStatus(t, do(t, dst, "GET", "/statement?snapshot="+first.body["snapshot"].(string)+"&limit=1", tk["ada"], ""), 200)
	if !reflect.DeepEqual(r.body["entries"], first.body["entries"]) || num(r.body["opening_balance"]) != 10300 {
		t.Fatal(r.raw)
	}
	// a tampered log is rejected without changing the destination
	var doc map[string]any
	_ = json.Unmarshal([]byte(exp.raw), &doc)
	doc["state"].(map[string]any)["payments"].([]any)[0].(map[string]any)["revisions"] = []any{map[string]any{"revision": 2, "amount": 1, "effective_at": "2026-09-20T10:00:00Z", "recorded_at": "2026-09-20T10:00:00Z", "reason": ""}}
	b, _ := json.Marshal(doc)
	wantErr(t, do(t, dst, "POST", "/_test/import", "", string(b)), 422, "validation_failed")
	_ = json.Unmarshal([]byte(exp.raw), &doc)
	u0 := doc["state"].(map[string]any)["users"].([]any)[0].(map[string]any)
	u0["opening"] = num(u0["opening"]) + 1
	b, _ = json.Marshal(doc)
	wantErr(t, do(t, dst, "POST", "/_test/import", "", string(b)), 422, "validation_failed")
	if num(wantStatus(t, do(t, dst, "GET", "/me", tk["ada"], ""), 200).body["balance"]) != 10000 {
		t.Fatal("rejected import changed the destination")
	}
}

func TestTenThousandPaymentsPerformance(t *testing.T) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	var ps []string
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	hs := []string{"ada", "bob", "cy", "dee"}
	for i := 0; i < 10000; i++ {
		from, to := hs[i%4], hs[(i+1)%4]
		ps = append(ps, fmt.Sprintf(`{"id":"p%05d","from_user_id":"u_%s","to_user_id":"u_%s","amount":%d,"created_at":"%s"}`,
			i, from, to, 1+i%50, base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339)))
	}
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1000000) + `,` + user("bob", 1000000) + `,` + user("cy", 1000000) + `,` + user("dee", 1000000) +
		`],"payments":[` + strings.Join(ps, ",") + `]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	timed := func(name, path string) {
		start := time.Now()
		r := wantStatus(t, do(t, srv, "GET", path, ada, ""), 200)
		d := time.Since(start)
		t.Logf("%-40s %v (%d bytes)", name, d, len(r.raw))
		if d > time.Second {
			t.Fatalf("%s took %v", name, d)
		}
	}
	timed("statement limit=200", "/statement?limit=200")
	timed("statement limit=200 offset=4800", "/statement?limit=200&offset=4800")
	timed("statement window + known_at", "/statement"+qs("from", "2026-09-03T00:00:00Z", "to", "2026-09-05T00:00:00Z", "known_at", "2026-09-04T00:00:00Z"))
	timed("me as_of", "/me"+qs("as_of", "2026-09-04T00:00:00Z"))
	timed("me as_of + known_at", "/me"+qs("as_of", "2026-09-04T00:00:00Z", "known_at", "2026-09-05T00:00:00Z"))
	s := wantStatus(t, do(t, srv, "GET", "/statement?limit=1", ada, ""), 200)
	timed("snapshot page", "/statement?limit=200&offset=2000&snapshot="+s.body["snapshot"].(string))
	timed("payment write", "/me")
	start := time.Now()
	wantStatus(t, post(t, srv, "/payments", ada, newKey(), `{"to_handle":"bob","amount":1}`), 201)
	t.Logf("%-40s %v", "POST /payments", time.Since(start))
}

// Reviewer finding 1 (regression): a hold's creation instant comes from the same µs event
// clock as payments, so funding then authorizing within one second never dates the hold
// before the payment that funded it, and no historical view goes negative.
func TestHoldsAreOrderedOnTheEventClock(t *testing.T) {
	srv, tk := worldWith(t, []string{"ada:0", "bob:1000000"}, "", `,"authorization_ttl_seconds":600`)
	w := world{srv: srv, ada: tk["ada"], bob: tk["bob"]}
	at := func(asOf string) wallet {
		r := wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", asOf), tk["ada"], ""), 200)
		return wallet{num(r.body["total"]), num(r.body["available"]), num(r.body["held"]), num(r.body["balance"])}
	}
	tm := func(s string) time.Time { x, _ := time.Parse(time.RFC3339Nano, s); return x }
	just := func(s string, d time.Duration) string { return tm(s).Add(d).UTC().Format(time.RFC3339Nano) }
	var lastPay *resp
	var prev time.Time
	var held int64
	for i := 0; i < 25; i++ { // no sleeps: most pairs share one wall-clock second
		p := wantStatus(t, post(t, srv, "/payments", tk["bob"], newKey(), `{"to_handle":"ada","amount":100}`), 201)
		a := wantStatus(t, post(t, srv, "/authorizations", tk["ada"], newKey(), `{"to_handle":"bob","amount":100}`), 201)
		pc, ac := tm(p.body["created_at"].(string)), tm(a.body["created_at"].(string))
		if !pc.After(prev) || !ac.After(pc) {
			t.Fatalf("event clock not strictly increasing: prev %v payment %v hold %v", prev, pc, ac)
		}
		prev = ac
		// expires_at = created_at + ttl exactly, same fractional part
		if tm(a.body["expires_at"].(string)).Sub(ac) != 600*time.Second || !stampRE.MatchString(a.body["created_at"].(string)) {
			t.Fatal(a.raw)
		}
		// just before the hold: the new money is there, the hold is not
		if g := at(just(a.body["created_at"].(string), -time.Microsecond)); g.held != held || g.available < 0 || g.total != int64(100*(i+1)) {
			t.Fatalf("pair %d before the hold: %+v", i, g)
		}
		held += 100
		if g := at(a.body["created_at"].(string)); g.held != held || g.available != 0 {
			t.Fatalf("pair %d at the hold: %+v", i, g)
		}
		if g := at(p.body["created_at"].(string)); g.available < 0 {
			t.Fatalf("pair %d at the payment: %+v", i, g)
		}
		lastPay = &p
	}
	// step 5 of the repro: a correction that only credits ada is accepted
	id := lastPay.body["payment_id"].(string)
	wantStatus(t, correct(t, srv, tk["bob"], id, newKey(), corr(1, 600, lastPay.body["created_at"].(string), "underpaid")), 201)
	// second symptom: a hold created right after a nonfinal capture is not counted before it
	wantStatus(t, post(t, srv, "/payments", tk["bob"], newKey(), `{"to_handle":"ada","amount":1000}`), 201)
	a1 := authID(t, authorize(t, w, tk["ada"], "bob", 500))
	c := wantStatus(t, post(t, srv, "/authorizations/"+a1+"/capture", tk["bob"], newKey(), `{"amount":200,"final":false}`), 201)
	a2 := wantStatus(t, post(t, srv, "/authorizations", tk["ada"], newKey(), `{"to_handle":"bob","amount":50}`), 201)
	capAt := c.body["created_at"].(string)
	if !tm(a2.body["created_at"].(string)).After(tm(capAt)) {
		t.Fatal("hold dated before the capture that preceded it")
	}
	if g := at(just(capAt, -time.Microsecond)); g.held != held+500 {
		t.Fatalf("before the capture: %+v (want held %d)", g, held+500)
	}
	r := wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", just(capAt, -time.Microsecond), "known_at", just(capAt, -time.Microsecond)), tk["ada"], ""), 200)
	if num(r.body["held"]) != held+500 {
		t.Fatal(r.raw)
	}
	if g := at(capAt); g.held != held+300 {
		t.Fatalf("at the capture: %+v", g)
	}
	if g := at(a2.body["created_at"].(string)); g.held != held+350 || g.available < 0 {
		t.Fatalf("after the second hold: %+v", g)
	}
	// newest first, ordered on the event clock
	l := wantStatus(t, do(t, srv, "GET", "/authorizations?limit=2", tk["ada"], ""), 200).body["authorizations"].([]any)
	if l[0].(map[string]any)["authorization_id"] != a2.body["authorization_id"] || l[1].(map[string]any)["authorization_id"] != a1 {
		t.Fatal(l)
	}
}

// Reviewer finding 2 (regression): seeded items without created_at take the reset instant
// itself, not the start of its second. The reset is pinned >= 300 ms into a second and a
// supplied seeded payment sits 100 ms before it, inside the same second.
func TestSeededDefaultsUseTheResetInstant(t *testing.T) {
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	for time.Now().Nanosecond() < 300_000_000 || time.Now().Nanosecond() > 900_000_000 {
		time.Sleep(5 * time.Millisecond)
	}
	s1At := time.Now().Add(-100 * time.Millisecond).UTC().Format("2006-01-02T15:04:05.000000-07:00")
	exp := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	f := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 10000) + `,` + user("bob", 1795) + `,` + user("cy", 705) + `],
	 "payments":[{"id":"s1","from_user_id":"u_bob","to_user_id":"u_cy","amount":700,"created_at":"` + s1At + `"},
	             {"id":"s2","from_user_id":"u_bob","to_user_id":"u_cy","amount":5}],
	 "authorizations":[{"id":"ah","from_user_id":"u_cy","to_user_id":"u_ada","amount":700,"status":"open","expires_at":"` + exp + `"}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	cy := login(t, srv, "cy@example.com", "correct horse")
	bob := login(t, srv, "bob@example.com", "correct horse")
	tm := func(s string) time.Time { x, _ := time.Parse(time.RFC3339Nano, s); return x }
	hold := wantStatus(t, do(t, srv, "GET", "/authorizations", cy, ""), 200).body["authorizations"].([]any)[0].(map[string]any)
	if !tm(hold["created_at"].(string)).After(tm(s1At)) {
		t.Fatalf("seeded hold at %v, before the seeded payment at %s", hold["created_at"], s1At)
	}
	s := wantStatus(t, do(t, srv, "GET", "/statement", cy, ""), 200)
	if got := entryIDs(s); !reflect.DeepEqual(got, []string{"s1", "s2"}) {
		t.Fatalf("omitted created_at is reset time, after s1: %v", got)
	}
	if e := s.body["entries"].([]any)[1].(map[string]any); e["effective_at"] != hold["created_at"] {
		t.Fatalf("seeded payment and seeded hold share the reset instant: %v vs %v", e["effective_at"], hold["created_at"])
	}
	for _, asOf := range []string{s1At, hold["created_at"].(string), time.Now().UTC().Format(time.RFC3339Nano)} {
		r := wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", asOf), cy, ""), 200)
		if num(r.body["available"]) < 0 || num(r.body["total"]) < 0 {
			t.Fatalf("negative view at %s: %s", asOf, r.raw)
		}
	}
	// a correction that only credits cy is accepted
	wantStatus(t, correct(t, srv, bob, "s1", newKey(), corr(1, 701, s1At, "credit cy 1")), 201)
	// later API events still come after the reset instant
	p := wantStatus(t, post(t, srv, "/payments", bob, newKey(), `{"to_handle":"cy","amount":1}`), 201)
	if !tm(p.body["created_at"].(string)).After(tm(hold["created_at"].(string))) {
		t.Fatal("API payment not after the reset instant")
	}
}
