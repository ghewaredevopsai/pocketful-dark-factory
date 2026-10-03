package app

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Passwords are stored as PBKDF2-HMAC-SHA256 with a per-user random salt (AU-11).
// Hashing and verification run outside the state lock so they never block other
// requests (cost is a few milliseconds per call on one core).
const pbkdf2Iter = 20000

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key, _ := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter, hex.EncodeToString(salt), hex.EncodeToString(key))
}

func checkPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := hex.DecodeString(parts[2])
	want, err2 := hex.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "t_" + hex.EncodeToString(b)
}

// validEmail: exactly one '@' with a non-empty local part and domain, no whitespace (AU-05).
func validEmail(e string) bool {
	if strings.Count(e, "@") != 1 || strings.ContainsAny(e, " \t\r\n") {
		return false
	}
	at := strings.IndexByte(e, '@')
	return at > 0 && at < len(e)-1
}

// deriveHandle implements MD-07: local part, lowercased, every char outside
// [a-z0-9_] replaced by '_', truncated to 20 characters.
func deriveHandle(email string) string {
	local := strings.ToLower(email[:strings.IndexByte(email, '@')])
	var b strings.Builder
	n := 0
	for _, r := range local {
		if n == 20 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		n++
	}
	return b.String()
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
