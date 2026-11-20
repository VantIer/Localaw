package mcphost

import (
	"strings"
	"testing"
)

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"read_file":    "read_file",
		"a.b/c d":      "a_b_c_d",
		"a__..__b":     "a_b",
		"":             "tool",
		"中文":           "tool",
		"Mixed-Case_1": "Mixed-Case_1",
	}
	for in, want := range cases {
		if got := sanitizeSegment(in); got != want {
			t.Errorf("sanitizeSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeNameStripsControlCharacters(t *testing.T) {
	if got := SafeName("a\x1b[31m\nb"); strings.ContainsAny(got, "\x1b\n") {
		t.Fatalf("control characters survived SafeName: %q", got)
	}
}

func TestFitNameLengthAndStability(t *testing.T) {
	long := "mcp__server__" + strings.Repeat("x", 100)
	got := fitName(long, "server", strings.Repeat("x", 100))
	if len(got) > maxToolName {
		t.Fatalf("name too long: %d (%q)", len(got), got)
	}
	if got != fitName(long, "server", strings.Repeat("x", 100)) {
		t.Fatal("fitName is not deterministic")
	}
	if !strings.HasPrefix(got, "mcp__server__") {
		t.Fatalf("prefix lost: %q", got)
	}
}

func TestHashedNameDistinguishesCollisions(t *testing.T) {
	a := hashedName("mcp__s__tool", "s", "a.b")
	b := hashedName("mcp__s__tool", "s", "a_b")
	if a == b {
		t.Fatalf("collision names are equal: %q", a)
	}
	if len(a) > maxToolName || len(b) > maxToolName {
		t.Fatalf("hashed names too long: %d, %d", len(a), len(b))
	}
}
