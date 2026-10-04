package app

import (
	"embed"
	"net/http"
	"strings"
)

// The browser UI: one HTML shell, one stylesheet and one script, embedded in the binary.
// The script renders every screen client-side from the JSON API. Nothing is fetched from
// outside the service (system font stack, inline SVG icon).

//go:embed ui/index.html ui/app.css ui/app.js
var uiFS embed.FS

// rawBody is a non-JSON response (HTML, CSS, JS).
type rawBody struct {
	ctype string
	b     []byte
}

var pageTitles = map[string]string{
	"/": "Wallet", "/requests": "Requests", "/split": "Split a bill",
	"/signup": "Create account", "/login": "Log in", "/authorizations": "Holds",
}

func (s *Server) page(r *http.Request, _ []string) (int, any, *apiErr) {
	shell, _ := uiFS.ReadFile("ui/index.html")
	title := pageTitles[r.URL.Path]
	if title == "" {
		title = "Pocketful"
	}
	html := strings.Replace(string(shell), "%TITLE%", title, 1)
	return 200, rawBody{"text/html; charset=utf-8", []byte(html)}, nil
}

func (s *Server) static(r *http.Request, params []string) (int, any, *apiErr) {
	types := map[string]string{"app.css": "text/css; charset=utf-8", "app.js": "text/javascript; charset=utf-8"}
	ct, ok := types[params[0]]
	if !ok {
		return 0, nil, notFound("no such asset")
	}
	b, _ := uiFS.ReadFile("ui/" + params[0])
	return 200, rawBody{ct, b}, nil
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// negotiate serves the UI shell to browsers (Accept: text/html) and the API otherwise.
func (s *Server) negotiate(api handler) handler {
	return func(r *http.Request, params []string) (int, any, *apiErr) {
		if wantsHTML(r) {
			return s.page(r, params)
		}
		return api(r, params)
	}
}
