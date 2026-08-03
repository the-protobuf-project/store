package gorm

import "testing"

// TestCELStringsToGo covers the mistranslation that a live compile caught: CEL
// accepts single-quoted strings, Go reserves single quotes for runes, so a CEL
// 'free' emitted verbatim is an illegal rune literal — the generated tree would
// not compile at the consumer's end.
func TestCELStringsToGo(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`this.tier != 'free'`, `this.tier != "free"`, true},
		{`this.tier != "free"`, `this.tier != "free"`, true},
		{`this.a == 'x' || this.b == 'y'`, `this.a == "x" || this.b == "y"`, true},
		{`this.n <= 100`, `this.n <= 100`, true},
		// An apostrophe inside a double-quoted literal must survive.
		{`this.s == "it's"`, `this.s == "it's"`, true},
		// A double quote inside a single-quoted literal must be escaped for Go.
		{`this.s == 'say "hi"'`, `this.s == "say \"hi\""`, true},
		// An unterminated literal is rejected rather than guessed at.
		{`this.tier != 'free`, "", false},
	}
	for _, c := range cases {
		got, ok := celStringsToGo(c.in)
		if ok != c.ok {
			t.Errorf("celStringsToGo(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("celStringsToGo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
