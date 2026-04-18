package auth

import (
	"regexp"
	"strings"
	"testing"
)

var keyRegex = regexp.MustCompile(`^ifix_sk_[a-z2-7]{32}$`)

func TestGenerateAPIKey_Format(t *testing.T) {
	for i := 0; i < 100; i++ {
		raw, hash, lookup, prefix, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if !keyRegex.MatchString(raw) {
			t.Fatalf("iter %d: raw %q not matching %s", i, raw, keyRegex)
		}
		if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
			t.Fatalf("iter %d: hash %q missing argon2id v=19/m=65536/t=3/p=2 header", i, hash)
		}
		if len(lookup) != 32 {
			t.Fatalf("iter %d: lookup hash len = %d want 32", i, len(lookup))
		}
		// Prefix shape: ifix_sk_****<last4>.
		want := KeyPrefix + "****" + raw[len(raw)-4:]
		if prefix != want {
			t.Fatalf("iter %d: prefix %q want %q", i, prefix, want)
		}
	}
}

func TestGenerateAPIKey_UniquePer1000(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		raw, _, _, _, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if _, dup := seen[raw]; dup {
			t.Fatalf("duplicate raw key after %d iterations: %s", i, raw)
		}
		seen[raw] = struct{}{}
	}
}

func TestGenerateAPIKey_HashVerifies(t *testing.T) {
	raw, hash, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyHash(raw, hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatal("VerifyHash returned false for raw produced by GenerateAPIKey")
	}
}

func TestGenerateAPIKey_HashRejectsOthers(t *testing.T) {
	rawA, hashA, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	rawB, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if rawA == rawB {
		t.Fatal("two consecutive GenerateAPIKey calls returned identical raw — broken RNG")
	}
	ok, err := VerifyHash(rawB, hashA)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Fatal("VerifyHash matched a different raw against hashA")
	}
}

func TestIsWellFormedKey_Positive(t *testing.T) {
	raw, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !IsWellFormedKey(raw) {
		t.Fatalf("IsWellFormedKey rejected a freshly generated key: %s", raw)
	}
}

func TestIsWellFormedKey_Negative(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"wrong_prefix", "ifix_pk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"too_short", "ifix_sk_aaaa"},
		{"too_long", "ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"uppercase", "ifix_sk_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"contains_one", "ifix_sk_1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"contains_zero", "ifix_sk_0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"contains_eight", "ifix_sk_8aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"contains_nine", "ifix_sk_9aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if IsWellFormedKey(c.in) {
				t.Fatalf("IsWellFormedKey accepted invalid input %q", c.in)
			}
		})
	}
}

func TestDefaultParams(t *testing.T) {
	if DefaultParams.Memory != 64*1024 {
		t.Errorf("Memory=%d want 65536", DefaultParams.Memory)
	}
	if DefaultParams.Iterations != 3 {
		t.Errorf("Iterations=%d want 3", DefaultParams.Iterations)
	}
	if DefaultParams.Parallelism != 2 {
		t.Errorf("Parallelism=%d want 2", DefaultParams.Parallelism)
	}
	if DefaultParams.SaltLength != 16 {
		t.Errorf("SaltLength=%d want 16", DefaultParams.SaltLength)
	}
	if DefaultParams.KeyLength != 32 {
		t.Errorf("KeyLength=%d want 32", DefaultParams.KeyLength)
	}
}
