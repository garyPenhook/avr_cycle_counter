package asm_test

import (
	"testing"

	"cyclecount/internal/asm"
)

func TestDataBytes(t *testing.T) {
	cases := []struct {
		dir, args string
		want      int
		ok        bool
	}{
		{".byte", "1, 2, 3", 3, true},
		{".word", "1, 2", 4, true},
		{".long", "1", 4, true},
		{".space", "32", 32, true},
		{".zero", "8", 8, true},
		{".ds.w", "4", 8, true},
		{".fill", "4, 2", 8, true},
		{".fill", "4", 4, true},
		// Escapes each encode one byte: \n, \x41, \101 octal, \\, plus 3 plain.
		{".ascii", `"ab\ncd"`, 5, true},
		{".ascii", `"\x41\x42"`, 2, true},
		{".ascii", `"\101\102"`, 2, true},
		{".asciz", `"abc"`, 4, true}, // +NUL
		{".string", `"a\\b"`, 4, true},
		// Negative reservations are not sizeable.
		{".space", "-4", 0, false},
		// Unsizeable / non-allocating directives.
		{".text", "", 0, false},
	}
	for _, c := range cases {
		got, ok := asm.DataBytes(c.dir, c.args)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("DataBytes(%q, %q) = %d,%t; want %d,%t", c.dir, c.args, got, ok, c.want, c.ok)
		}
	}
}
