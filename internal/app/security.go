package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

var commonPasswordBlocklist = map[string]struct{}{
	"password":                  {},
	"123456789012345":           {},
	"correcthorsebatterystaple": {},
	"qwertyqwertyqwerty":        {},
	"aaaaaaaaaaaaaaa":           {},
	"letmeinletmeinletmein":     {},
	"passwordpassword":          {},
	"password password":         {},
}

func canonicalEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validatePassword(password string) error {
	runes := []rune(password)
	if len(runes) < 15 || len(runes) > 128 {
		return errors.New("password length must be 15-128 characters")
	}
	if _, blocked := commonPasswordBlocklist[strings.ToLower(strings.ReplaceAll(password, " ", ""))]; blocked {
		return errors.New("password is blocked")
	}
	if _, blocked := commonPasswordBlocklist[strings.ToLower(password)]; blocked {
		return errors.New("password is blocked")
	}
	return nil
}

func hashPassword(password string) (string, error) {
	salt, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=1,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func newOpaqueID(prefix string) (string, error) {
	raw, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func newToken() (string, error) {
	raw, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func subtleConstantEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func requestHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) ([]byte, error) {
	raw := make([]byte, n)
	_, err := rand.Read(raw)
	return raw, err
}
