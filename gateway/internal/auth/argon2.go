// Package auth (argon2.go): API-key generation + argon2id wrappers.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"

	"github.com/alexedwards/argon2id"
)

// KeyPrefix is the fixed prefix of every gateway API key. The prefix is
// deliberately distinctive so secret-scanning tools (GitHub, GitLab) and
// log redactors can spot leaks (CONTEXT.md D-A1).
const KeyPrefix = "ifix_sk_"

// keyBodyLen is the number of base32 chars after the prefix. 32 chars of
// rfc4648 base32 encode 20 random bytes = 160 bits of entropy, well above
// the 128-bit floor OWASP calls for on bearer tokens.
const keyBodyLen = 32

// keyTotalLen is len("ifix_sk_") + keyBodyLen = 40.
const keyTotalLen = len(KeyPrefix) + keyBodyLen

// DefaultParams are OWASP 2026 recommendations for argon2id bearer tokens.
// Tuned so verification on a 4 vCPU VPS takes ~30-50ms; cache TTL of 60s
// (cache.go) keeps that off the hot path.
var DefaultParams = &argon2id.Params{
	Memory:      64 * 1024, // 64 MiB
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

// keyEncoding uses lowercase rfc4648 base32 with no padding. base32.StdEncoding
// produces uppercase by default; we lowercase post-encode.
var keyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateAPIKey produces a fresh (raw, argon2id-hash, sha256-lookup-hash,
// prefix-for-display) tuple. Call from gatewayctl admin CLI; the raw value
// is returned to the operator ONCE and never stored in the database.
// lookupHash is stored in api_keys.key_lookup_hash (BYTEA, UNIQUE index) for
// fast indexed lookup on the hot path (Codex review [HIGH] 02-03).
func GenerateAPIKey() (raw string, hash string, lookupHash []byte, prefix string, err error) {
	b := make([]byte, 20) // 20 bytes = 32 base32 chars
	if _, err = rand.Read(b); err != nil {
		return "", "", nil, "", err
	}
	raw = KeyPrefix + strings.ToLower(keyEncoding.EncodeToString(b))
	hash, err = argon2id.CreateHash(raw, DefaultParams)
	if err != nil {
		return "", "", nil, "", err
	}
	lookupHash = LookupHash(raw)
	prefix = KeyPrefix + "****" + raw[len(raw)-4:]
	return raw, hash, lookupHash, prefix, nil
}

// LookupHash returns the SHA-256 of the raw key as a 32-byte slice. The
// value is stored in api_keys.key_lookup_hash (UNIQUE index) and used by
// Verifier.GetActiveKeyByLookupHash to narrow the candidate set to 0-or-1
// row. SHA-256 is sufficient here because the lookup is already keyed by
// the full 160-bit raw secret — no HMAC rotation is needed to protect
// against pre-image attacks. Codex review [HIGH] 02-03.
func LookupHash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// IsWellFormedKey returns true iff s syntactically matches
// ifix_sk_<32 lowercase base32>. Called from Verify BEFORE any DB query so
// malformed keys consume no resources.
func IsWellFormedKey(s string) bool {
	if len(s) != keyTotalLen {
		return false
	}
	if !strings.HasPrefix(s, KeyPrefix) {
		return false
	}
	body := s[len(KeyPrefix):]
	for i := 0; i < len(body); i++ {
		c := body[i]
		// rfc4648 base32 lowercase: a-z (minus 0/1/8/9 not in alphabet) + 2-7
		if !((c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
			return false
		}
	}
	return true
}

// VerifyHash returns (true, nil) when raw matches hash; (false, nil) when
// it does not; and (false, err) on hash-parse errors.
func VerifyHash(raw, hash string) (bool, error) {
	match, err := argon2id.ComparePasswordAndHash(raw, hash)
	if err != nil {
		// Corrupt hash in DB — treat as mismatch after logging upstream.
		return false, err
	}
	return match, nil
}

// HashKey is a convenience wrapper used by tests that need a known
// argon2id hash for a known raw key.
func HashKey(raw string) (string, error) {
	return argon2id.CreateHash(raw, DefaultParams)
}
