package shareapi

import (
	"testing"
)

func TestGenerateCode(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		c, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		if len(c) != CodeLength {
			t.Fatalf("length = %d, want %d", len(c), CodeLength)
		}
		if !ValidCode(c) {
			t.Fatalf("generated code %q not accepted by ValidCode", c)
		}
		if seen[c] {
			t.Fatalf("duplicate code %q after %d iterations — bias in RNG?", c, i)
		}
		seen[c] = true
	}
}

func TestValidCode(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"AbCd1234", true},
		{"00000000", true},
		{"zzzzzzzz", true},
		{"", false},
		{"short", false},
		{"toolongstring", false},
		{"AbCd123!", false},
		{"AbCd 234", false},
		{"AbCd-234", false},
		{"AbCd_234", false},
		{"AbCdабвг", false},
	}
	for _, tc := range cases {
		if got := ValidCode(tc.in); got != tc.want {
			t.Errorf("ValidCode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
