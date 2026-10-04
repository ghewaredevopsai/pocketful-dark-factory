// UI routing: Accept negotiation on /requests and /authorizations, the HTML routes and the
// embedded assets. Browser behaviour itself is checked by review-ui/ui_check.py.
package tests

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func get(t *testing.T, url, accept, token string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header.Get("Content-Type"), string(b)
}

func TestAcceptNegotiation(t *testing.T) {
	w := newWorld(t)
	browser := "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	for _, path := range []string{"/requests", "/authorizations"} {
		st, ct, body := get(t, w.srv.URL+path, browser, "")
		if st != 200 || !strings.HasPrefix(ct, "text/html") || !strings.Contains(body, "/static/app.js") {
			t.Fatalf("%s as browser: %d %s", path, st, ct)
		}
		st, ct, _ = get(t, w.srv.URL+path, "", w.ada)
		if st != 200 || !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s as API: %d %s", path, st, ct)
		}
		st, ct, _ = get(t, w.srv.URL+path, "application/json", "")
		if st != 401 || !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s API without token: %d %s", path, st, ct)
		}
	}
	// query strictness still applies to API calls
	wantErr(t, do(t, w.srv, "GET", "/requests?limit=0", w.ada, ""), 422, "validation_failed")
}

func TestHTMLRoutesAndAssets(t *testing.T) {
	w := newWorld(t)
	titles := map[string]string{"/": "Wallet", "/split": "Split a bill", "/signup": "Create account", "/login": "Log in"}
	for path, title := range titles {
		st, ct, body := get(t, w.srv.URL+path, "", "")
		if st != 200 || !strings.HasPrefix(ct, "text/html") || !strings.Contains(body, "<title>"+title+" · Pocketful</title>") ||
			!strings.Contains(body, `lang="en"`) || !strings.Contains(body, `name="viewport"`) {
			t.Fatalf("%s: %d %s", path, st, ct)
		}
		if b := strings.ReplaceAll(body, "http://www.w3.org/2000/svg", ""); strings.Contains(b, "http://") || strings.Contains(b, "https://") {
			t.Fatalf("%s references an external URL", path)
		}
	}
	for path, want := range map[string]string{"/static/app.js": "text/javascript", "/static/app.css": "text/css"} {
		st, ct, body := get(t, w.srv.URL+path, "", "")
		if st != 200 || !strings.HasPrefix(ct, want) || len(body) < 1000 {
			t.Fatalf("%s: %d %s", path, st, ct)
		}
		if strings.Contains(body, "https://") || strings.Contains(body, "@import") || strings.Contains(body, "url(http") {
			t.Fatalf("%s references an external resource", path)
		}
	}
	wantErr(t, do(t, w.srv, "GET", "/static/nope.js", "", ""), 404, "not_found")
	wantErr(t, do(t, w.srv, "POST", "/split", "", "{}"), 405, "method_not_allowed")
}
