// Pinned-clock tests: the wall clock is replaced (app.SetClock) so every instant is exact
// and no outcome depends on timing luck. Covers the reset instant and seeded defaults,
// capture/void at the expiry instant, correction "not later than now", imports into a
// service whose clock is behind, and snapshot freezing against later stamps.
package tests

import (
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"pocketful/app"
)

type fakeClock struct{ ns atomic.Int64 }

func (c *fakeClock) set(t time.Time)     { c.ns.Store(t.UnixNano()) }
func (c *fakeClock) add(d time.Duration) { c.ns.Add(int64(d)) }
func (c *fakeClock) now() int64          { return c.ns.Load() }
func micro(t time.Time) string           { return t.UTC().Format("2006-01-02T15:04:05.000000-07:00") }
func pinClock(t *testing.T, at time.Time) *fakeClock {
	c := &fakeClock{}
	c.set(at)
	t.Cleanup(app.SetClock(c.now))
	return c
}

// The reset instant X sits 150 ms into a second. Supplied seeded instants: X-100ms (same
// second), X-1µs and X; omitted ones take exactly X; X+1µs is in the future (422).
func TestPinnedResetInstantAndSeededDefaults(t *testing.T) {
	X := time.Date(2026, 10, 4, 5, 0, 0, 150_000_000, time.UTC)
	clk := pinClock(t, X)
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	users := `"users":[` + user("ada", 10000) + `,` + user("bob", 1795) + `,` + user("cy", 705) + `]`
	future := `{"currency":"EUR","minor_units":2,` + users + `,"payments":[{"id":"sf","from_user_id":"u_bob","to_user_id":"u_cy","amount":1,"created_at":"` + micro(X.Add(time.Microsecond)) + `"}]}`
	wantErr(t, do(t, srv, "POST", "/_test/reset", "", future), 422, "validation_failed")
	f := `{"currency":"EUR","minor_units":2,` + users + `,
	 "payments":[{"id":"s1","from_user_id":"u_bob","to_user_id":"u_cy","amount":700,"created_at":"` + micro(X.Add(-100*time.Millisecond)) + `"},
	             {"id":"s_m1","from_user_id":"u_cy","to_user_id":"u_bob","amount":10,"created_at":"` + micro(X.Add(-time.Microsecond)) + `"},
	             {"id":"s_eq","from_user_id":"u_bob","to_user_id":"u_cy","amount":10,"created_at":"` + micro(X) + `"},
	             {"id":"s2","from_user_id":"u_bob","to_user_id":"u_cy","amount":5}],
	 "authorizations":[{"id":"ah","from_user_id":"u_cy","to_user_id":"u_ada","amount":700,"status":"open","expires_at":"2026-10-04T07:00:00Z"}]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	cy := login(t, srv, "cy@example.com", "correct horse")
	bob := login(t, srv, "bob@example.com", "correct horse")
	hold := wantStatus(t, do(t, srv, "GET", "/authorizations", cy, ""), 200).body["authorizations"].([]any)[0].(map[string]any)
	if hold["created_at"] != micro(X) {
		t.Fatalf("seeded hold created_at %v, want the reset instant %s", hold["created_at"], micro(X))
	}
	s := wantStatus(t, do(t, srv, "GET", "/statement", cy, ""), 200)
	if got := entryIDs(s); !reflect.DeepEqual(got, []string{"s1", "s_m1", "s2", "s_eq"}) {
		t.Fatalf("order by instant, ties by id: %v", got)
	}
	// cy: opening = 705 - 700 + 10 - 10 - 5 = 0
	cases := []struct {
		at          time.Time
		total, held int64
	}{
		{X.Truncate(time.Second), 0, 0}, // the reset second's boundary: before everything
		{X.Add(-100 * time.Millisecond), 700, 0},
		{X.Add(-time.Microsecond), 690, 0}, // the hold does not exist yet
		{X, 705, 700},                      // s_eq, s2 and the hold all at X, combined
		{X.Add(time.Microsecond), 705, 700},
	}
	for _, c := range cases {
		r := wantStatus(t, do(t, srv, "GET", "/me"+qs("as_of", micro(c.at)), cy, ""), 200)
		if num(r.body["total"]) != c.total || num(r.body["held"]) != c.held || num(r.body["available"]) < 0 {
			t.Fatalf("as_of %s: %s (want total %d held %d)", micro(c.at), r.raw, c.total, c.held)
		}
	}
	// a correction that only credits cy is accepted; API events come strictly after X
	r := wantStatus(t, correct(t, srv, bob, "s1", newKey(), corr(1, 701, micro(X.Add(-100*time.Millisecond)), "credit")), 201)
	if !(r.body["recorded_at"].(string) > micro(X)) {
		t.Fatalf("recorded_at %v not after the reset instant", r.body["recorded_at"])
	}
	p := wantStatus(t, post(t, srv, "/payments", bob, newKey(), `{"to_handle":"cy","amount":1}`), 201)
	if !(p.body["created_at"].(string) > r.body["recorded_at"].(string)) {
		t.Fatalf("frozen clock: stamps keep increasing by 1µs: %v", p.body["created_at"])
	}
	_ = clk
}

// A capture or void whose own event instant is at or after expires_at finds the hold
// expired, even when the sweep's clock reading was earlier.
func TestCaptureAndVoidAtTheExpiryInstant(t *testing.T) {
	Y := time.Date(2026, 10, 4, 6, 0, 0, 500_000_000, time.UTC)
	clk := pinClock(t, Y)
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	f := `{"currency":"EUR","minor_units":2,"authorization_ttl_seconds":60,"users":[` + user("ada", 1000) + `,` + user("bob", 0) + `]}`
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", f), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	bob := login(t, srv, "bob@example.com", "correct horse")
	w := world{srv: srv}
	a := wantStatus(t, authorize(t, w, ada, "bob", 300), 201)
	created, _ := time.Parse(time.RFC3339Nano, a.body["created_at"].(string))
	exp := created.Add(60 * time.Second)
	if a.body["expires_at"] != micro(exp) {
		t.Fatalf("expires_at %v, want %s", a.body["expires_at"], micro(exp))
	}
	id := a.body["authorization_id"].(string)
	clk.set(exp.Add(-2 * time.Microsecond)) // the next stamp is exp-1µs (strictly increasing) or the clock
	c := wantStatus(t, post(t, srv, "/authorizations/"+id+"/capture", bob, newKey(), `{"amount":100,"final":false}`), 201)
	if !(c.body["created_at"].(string) < micro(exp)) {
		t.Fatalf("capture before expiry stamped %v", c.body["created_at"])
	}
	// the clock reads exp-1µs, so the sweep sees the hold open; one stamping read takes
	// exp-1µs, so the capture's own stamp lands exactly on exp
	clk.set(exp.Add(-time.Microsecond))
	wantStatus(t, do(t, srv, "GET", "/statement", ada, ""), 200)
	wantErr(t, post(t, srv, "/authorizations/"+id+"/capture", bob, newKey(), `{"amount":100,"final":false}`), 409, "authorization_expired")
	wantErr(t, post(t, srv, "/authorizations/"+id+"/void", ada, "", ""), 409, "authorization_not_open")
	l := wantStatus(t, do(t, srv, "GET", "/authorizations", ada, ""), 200).body["authorizations"].([]any)[0].(map[string]any)
	if l["status"] != "expired" || l["closed_at"] != micro(exp) || num(l["captured_amount"]) != 100 {
		t.Fatal(l)
	}
	// a second hold voided 1µs before its deadline is voided, not expired
	b := wantStatus(t, authorize(t, w, ada, "bob", 50), 201)
	bc, _ := time.Parse(time.RFC3339Nano, b.body["created_at"].(string))
	clk.set(bc.Add(60*time.Second - 3*time.Microsecond))
	v := wantStatus(t, post(t, srv, "/authorizations/"+b.body["authorization_id"].(string)+"/void", ada, "", ""), 200)
	if v.body["status"] != "voided" || !(v.body["closed_at"].(string) < b.body["expires_at"].(string)) {
		t.Fatal(v.raw)
	}
	if g := me(t, srv, ada); g.held != 0 || g.total != 900 {
		t.Fatalf("%+v", g)
	}
}

// effective_at equal to the request's now is accepted; 1µs later is in the future.
func TestCorrectionEffectiveAtNowBoundary(t *testing.T) {
	Z := time.Date(2026, 10, 4, 7, 0, 0, 250_000_000, time.UTC)
	clk := pinClock(t, Z)
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", std), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	p := wantStatus(t, post(t, srv, "/payments", ada, newKey(), `{"to_handle":"bob","amount":100}`), 201)
	id := p.body["payment_id"].(string)
	clk.add(time.Second)
	now := time.Unix(0, clk.now())
	wantErr(t, correct(t, srv, ada, id, newKey(), corr(1, 90, micro(now.Add(time.Microsecond)), "future")), 422, "validation_failed")
	r := wantStatus(t, correct(t, srv, ada, id, newKey(), corr(1, 90, micro(now), "now")), 201)
	if r.body["effective_at"] != micro(now) || !(r.body["recorded_at"].(string) >= micro(now)) {
		t.Fatal(r.raw)
	}
}

// Imported history defines the clock floor: a service whose wall clock is behind the
// exported events still stamps every new event after all of them.
func TestImportIntoAServiceWithAnEarlierClock(t *testing.T) {
	A := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	clk := pinClock(t, A)
	src := httptest.NewServer(app.New())
	t.Cleanup(src.Close)
	wantStatus(t, do(t, src, "POST", "/_test/reset", "", std), 204)
	ada := login(t, src, "ada@example.com", "correct horse")
	w := world{srv: src}
	wantStatus(t, post(t, src, "/payments", ada, newKey(), `{"to_handle":"bob","amount":100}`), 201)
	h := wantStatus(t, authorize(t, w, ada, "bob", 200), 201)
	exp := wantStatus(t, do(t, src, "GET", "/_test/export", "", ""), 200)
	clk.set(A.Add(-time.Hour)) // the destination's clock is an hour behind
	dst := httptest.NewServer(app.New())
	t.Cleanup(dst.Close)
	wantStatus(t, do(t, dst, "POST", "/_test/import", "", exp.raw), 204)
	p := wantStatus(t, post(t, dst, "/payments", ada, newKey(), `{"to_handle":"bob","amount":1}`), 201)
	if !(p.body["created_at"].(string) > h.body["created_at"].(string)) {
		t.Fatalf("new payment %v not after imported hold %v", p.body["created_at"], h.body["created_at"])
	}
	a2 := wantStatus(t, authorize(t, world{srv: dst}, ada, "bob", 1), 201)
	if !(a2.body["created_at"].(string) > p.body["created_at"].(string)) {
		t.Fatal("hold not after the payment")
	}
}

// A snapshot taken with the clock frozen excludes a correction recorded right after it.
func TestSnapshotFrozenAgainstTheNextStamp(t *testing.T) {
	pinClock(t, time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", std), 204)
	ada := login(t, srv, "ada@example.com", "correct horse")
	p := wantStatus(t, post(t, srv, "/payments", ada, newKey(), `{"to_handle":"bob","amount":100}`), 201)
	s := wantStatus(t, do(t, srv, "GET", "/statement", ada, ""), 200)
	wantStatus(t, correct(t, srv, ada, p.body["payment_id"].(string), newKey(), corr(1, 40, p.body["created_at"].(string), "fix")), 201)
	again := wantStatus(t, do(t, srv, "GET", "/statement?snapshot="+s.body["snapshot"].(string), ada, ""), 200)
	if again.raw != s.raw {
		t.Fatalf("snapshot changed:\n%s\n%s", s.raw, again.raw)
	}
	live := wantStatus(t, do(t, srv, "GET", "/statement", ada, ""), 200)
	if num(live.body["closing_balance"]) != 9960 {
		t.Fatal(live.raw)
	}
}

// Seeded closed_at may not lie in the future, nor before a supplied created_at.
func TestSeededClosedAtBounds(t *testing.T) {
	X := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	pinClock(t, X)
	srv := httptest.NewServer(app.New())
	t.Cleanup(srv.Close)
	base := `{"currency":"EUR","minor_units":2,"users":[` + user("ada", 1000) + `,` + user("bob", 0) + `],"authorizations":[`
	hold := func(extra string) string {
		return base + `{"id":"a","from_user_id":"u_ada","to_user_id":"u_bob","amount":10,"status":"voided","expires_at":"2026-10-05T00:00:00Z"` + extra + `}]}`
	}
	wantErr(t, do(t, srv, "POST", "/_test/reset", "", hold(`,"closed_at":"`+micro(X.Add(time.Microsecond))+`"`)), 422, "validation_failed")
	wantErr(t, do(t, srv, "POST", "/_test/reset", "", hold(`,"created_at":"2026-10-03T00:00:00Z","closed_at":"2026-10-02T00:00:00Z"`)), 422, "validation_failed")
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", hold(`,"created_at":"2026-10-02T00:00:00Z","closed_at":"2026-10-03T00:00:00Z"`)), 204)
	wantStatus(t, do(t, srv, "POST", "/_test/reset", "", hold(`,"closed_at":"2026-10-03T00:00:00Z"`)), 204) // lifecycle need not be reconstructed
}
