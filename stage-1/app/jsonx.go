package app

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
)

// apiErr is every 4xx/5xx the service returns; it renders as the §5 error envelope.
type apiErr struct {
	status int
	code   string
	msg    string
}

func errf(status int, code, msg string) *apiErr { return &apiErr{status, code, msg} }

func malformed(msg string) *apiErr { return errf(400, "malformed_request", msg) }
func invalid(msg string) *apiErr   { return errf(422, "validation_failed", msg) }
func notFound(msg string) *apiErr  { return errf(404, "not_found", msg) }
func forbidden(msg string) *apiErr { return errf(403, "forbidden", msg) }
func unauthed() *apiErr {
	return errf(401, "unauthenticated", "missing, malformed or unknown bearer token")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeErr(w http.ResponseWriter, e *apiErr) {
	writeJSON(w, e.status, map[string]any{"error": map[string]any{"code": e.code, "message": e.msg}})
}

const maxBody = 1 << 20       // ordinary API bodies
const maxTestBody = 256 << 20 // reset fixtures and imports

func readBody(r *http.Request, limit int64) ([]byte, *apiErr) {
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, malformed("could not read request body")
	}
	if int64(len(b)) > limit {
		return nil, malformed("request body too large")
	}
	return b, nil
}

// parseJSON decodes exactly one JSON value, keeping numbers exact (json.Number).
func parseJSON(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false // trailing data
	}
	return v, true
}

// parseObject: unparseable or a non-object JSON value is 400 malformed_request (ER-02, ER-20).
func parseObject(b []byte) (map[string]any, *apiErr) {
	v, ok := parseJSON(b)
	if !ok {
		return nil, malformed("request body is not valid JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, malformed("request body must be a JSON object")
	}
	return m, nil
}

// optString returns the string at key k. A present non-string value is the generic
// wrong-type error (400 malformed_request).
func optString(m map[string]any, k string) (string, bool, *apiErr) {
	v, ok := m[k]
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", true, malformed(k + " must be a string")
	}
	return s, true, nil
}

func reqString(m map[string]any, k string) (string, *apiErr) {
	s, present, e := optString(m, k)
	if e != nil {
		return "", e
	}
	if !present {
		return "", invalid(k + " is required")
	}
	return s, nil
}

// integral returns the exact integer value of a JSON number (1000, 1000.0 and 1e3 alike).
// Strings, booleans, null and non-integral numbers are not integral (MD-02, MD-03).
func integral(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	s := string(n)
	if len(s) > 400 {
		return 0, false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		// bound the exponent so big.Rat never builds a giant power of ten
		exp, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		if err != nil || exp > 400 || exp < -400 {
			return 0, false
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || !r.IsInt() {
		return 0, false
	}
	i := r.Num()
	if !i.IsInt64() {
		return 0, false
	}
	return i.Int64(), true
}
