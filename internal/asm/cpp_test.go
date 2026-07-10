package asm

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// findCC returns a working C compiler driver for the preprocessor test, or ""
// if none is installed (the test then skips rather than fails).
func findCC() string {
	for _, c := range []string{"avr-gcc", "cc", "gcc", "clang"} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return ""
}

func TestPreprocessResolvesConditionals(t *testing.T) {
	cc := findCC()
	if cc == "" {
		t.Skip("no C compiler available for -cpp")
	}

	src := "#define COUNT 3\n" +
		"#if COUNT > 2\n" +
		"        ldi r24, COUNT\n" +
		"#else\n" +
		"        ldi r24, 1\n" +
		"#endif\n" +
		"loop:\n" +
		"        dec r24\n" +
		"        brne loop\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "t.S")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// Raw parse keeps both #if and #else bodies (it only skips the #-lines),
	// so it sees two LDI. The preprocessor drops the untaken branch.
	raw := countMnemonic(Parse(src), "LDI")
	if raw != 2 {
		t.Fatalf("raw parse: want 2 LDI (both branches), got %d", raw)
	}

	out, err := Preprocess(path, CPPOptions{CC: cc})
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	if got := countMnemonic(Parse(out), "LDI"); got != 1 {
		t.Fatalf("after cpp: want 1 LDI (taken branch only), got %d", got)
	}
}

func TestPreprocessPreservesRegionComments(t *testing.T) {
	cc := findCC()
	if cc == "" {
		t.Skip("no C compiler available for -cpp")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "regions.S")
	src := "// @begin hot\nnop\nret\n// @end hot\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Preprocess(path, CPPOptions{CC: cc})
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	lines := Parse(out)
	var begins, ends int
	for _, ln := range lines {
		begins += len(ln.RegionBegins)
		ends += len(ln.RegionEnds)
	}
	if begins != 1 || ends != 1 {
		t.Fatalf("annotations after cpp = %d begin/%d end, want 1/1", begins, ends)
	}
}

func TestLoadFileCPPIgnoresObjdumpText(t *testing.T) {
	// Disassembly input must not be sent through the preprocessor.
	dir := t.TempDir()
	path := filepath.Join(dir, "dis.txt")
	disasm := "Disassembly of section .text:\n\n" +
		"00000000 <main>:\n" +
		"   0:\t08 95       \tret\n"
	if err := os.WriteFile(path, []byte(disasm), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := LoadFileCPP(path, "avr-objdump", CPPOptions{CC: "/does/not/exist"})
	if err != nil {
		t.Fatalf("LoadFileCPP on disassembly: %v", err)
	}
	if got := countMnemonic(lines, "RET"); got != 1 {
		t.Fatalf("want 1 RET from disassembly, got %d", got)
	}
}

func countMnemonic(lines []*Line, mn string) int {
	n := 0
	for _, ln := range lines {
		if ln.Mnemonic == mn {
			n++
		}
	}
	return n
}
