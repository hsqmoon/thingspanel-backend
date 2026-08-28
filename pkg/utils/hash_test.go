package utils

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestBcryptHashAndCheck(t *testing.T) {
	hash, err := BcryptHash("valid-password")
	if err != nil {
		t.Fatalf("BcryptHash returned error: %v", err)
	}
	matches, err := BcryptCheck("valid-password", hash)
	if err != nil || !matches {
		t.Fatalf("expected password match, matches=%v err=%v", matches, err)
	}
	matches, err = BcryptCheck("wrong-password", hash)
	if err != nil || matches {
		t.Fatalf("expected password mismatch without internal error, matches=%v err=%v", matches, err)
	}
}

func TestBcryptRejectsPasswordsOver72Bytes(t *testing.T) {
	password := strings.Repeat("密", 25)
	if _, err := BcryptHash(password); !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("expected ErrPasswordTooLong from hash, got %v", err)
	}
	if _, err := BcryptCheck(password, "$2a$10$invalid"); !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("expected ErrPasswordTooLong from check, got %v", err)
	}
}

func TestBcryptCheckPropagatesMalformedHash(t *testing.T) {
	if _, err := BcryptCheck("valid-password", "not-a-bcrypt-hash"); err == nil {
		t.Fatal("expected malformed hash error")
	}
}
