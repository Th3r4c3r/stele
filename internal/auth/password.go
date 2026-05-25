// Package auth handles password hashing, sessions, and password reset.
//
// See docs/adr/0009-auth-admin.md.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters per ADR-009 D1 (OWASP-aligned).
const (
	argonMemoryKiB    = 64 * 1024 // 64 MiB
	argonTime         = 3
	argonParallelism  = 2
	argonSaltLen      = 16
	argonKeyLen       = 32
	MinPasswordLength = 10
)

// ErrInvalidPassword is returned when password is too short / empty.
var ErrInvalidPassword = errors.New("password does not meet policy")

// ErrPasswordMismatch is returned when verification fails.
var ErrPasswordMismatch = errors.New("password mismatch")

// HashPassword returns a PHC-format string for an Argon2id hash of pwd.
func HashPassword(pwd string) (string, error) {
	if len(pwd) < MinPasswordLength {
		return "", fmt.Errorf("%w: minimum %d chars", ErrInvalidPassword, MinPasswordLength)
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	hash := argon2.IDKey([]byte(pwd), salt, argonTime, argonMemoryKiB, argonParallelism, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword returns nil iff pwd hashes to the same value as stored.
// Constant-time compare on the hash bytes.
func VerifyPassword(stored, pwd string) error {
	p, err := decodePHC(stored)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(pwd), p.salt, p.time, p.memory, p.parallelism, uint32(len(p.hash)))
	if subtle.ConstantTimeCompare(got, p.hash) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}

type phc struct {
	memory      uint32
	time        uint32
	parallelism uint8
	salt        []byte
	hash        []byte
}

func decodePHC(s string) (phc, error) {
	// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return phc{}, fmt.Errorf("auth: not an argon2id PHC string")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return phc{}, fmt.Errorf("auth: argon2 version mismatch")
	}
	var p phc
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.parallelism); err != nil {
		return phc{}, fmt.Errorf("auth: cannot parse argon2 params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return phc{}, fmt.Errorf("auth: bad salt: %w", err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return phc{}, fmt.Errorf("auth: bad hash: %w", err)
	}
	p.salt, p.hash = salt, hash
	return p, nil
}
