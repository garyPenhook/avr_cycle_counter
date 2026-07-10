package asm_test

import (
	"strings"
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
		// GNU common symbols: the length is the second comma-separated field.
		{".comm", "buf, 32, 1", 32, true},
		{".comm", "buf,32", 32, true},
		{".lcomm", "scratch, 16", 16, true},
		{".comm", "buf", 0, false}, // no size field
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

func TestAllocatesBSS(t *testing.T) {
	for _, d := range []string{".comm", ".lcomm", ".COMM", " .lcomm "} {
		if !asm.AllocatesBSS(d) {
			t.Errorf("AllocatesBSS(%q) = false; want true", d)
		}
	}
	for _, d := range []string{".byte", ".space", ".text", ""} {
		if asm.AllocatesBSS(d) {
			t.Errorf("AllocatesBSS(%q) = true; want false", d)
		}
	}
}

// A "/*" inside a string literal or a line comment must not open a block
// comment, so the quoted bytes survive parsing intact.
func TestParsePreservesBlockMarkersInStringsAndComments(t *testing.T) {
	src := "" +
		"\t.ascii \"a/*b*/c\"\n" + // 7 bytes; /*b*/ must NOT be stripped
		"\tnop ; keep /* this */ comment\n" +
		"\tret\n"
	lines := asm.Parse(src)
	if got, ok := asm.DataBytes(lines[0].Directive, lines[0].DirectiveArgs); !ok || got != 7 {
		t.Errorf("ascii bytes = %d,%t; want 7,true (block markers stripped from string)", got, ok)
	}
	if lines[1].Mnemonic != "NOP" {
		t.Errorf("line 2 mnemonic = %q; want NOP", lines[1].Mnemonic)
	}
}

// Parse must not silently truncate at a very long line the way bufio.Scanner
// would when a line exceeds its buffer.
func TestParseHandlesOverlongLine(t *testing.T) {
	long := "\t; " + strings.Repeat("x", 2<<20) + "\n"
	src := long + "\tret\n"
	lines := asm.Parse(src)
	if len(lines) != 2 {
		t.Fatalf("got %d lines; want 2 (overlong line must not drop the rest)", len(lines))
	}
	if lines[1].Mnemonic != "RET" {
		t.Errorf("line 2 mnemonic = %q; want RET", lines[1].Mnemonic)
	}
}

func TestParseCommentAfterEvenBackslashes(t *testing.T) {
	lines := asm.Parse(".ascii \"\\\\\\\\\" // @begin hot\n")
	if len(lines) != 1 || len(lines[0].RegionBegins) != 1 {
		t.Fatalf("region begin lost after escaped string: %+v", lines)
	}
}
