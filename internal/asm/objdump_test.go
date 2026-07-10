package asm

import (
	"strings"
	"testing"
)

// A trimmed but faithful `avr-objdump -h -d` dump (attiny3217 build of the
// delay.S example): section table, a symbol header, instructions with the
// address/bytes/mnemonic/operands/comment layout, and a 4-byte STS.
const sampleObjdump = `
d.o:     file format elf32-avr

Sections:
Idx Name          Size      VMA       LMA       File off  Algn
  0 .text         00000012  00000000  00000000  00000034  2**0
                  CONTENTS, ALLOC, LOAD, RELOC, READONLY, CODE
  1 .data         00000000  00000000  00000000  00000046  2**0
                  CONTENTS, ALLOC, LOAD, DATA
  2 .bss          00000020  00000000  00000000  00000046  2**0
                  ALLOC
  3 .rodata       00000004  00000000  00000000  00000046  2**0
                  CONTENTS, ALLOC, LOAD, READONLY, DATA

Disassembly of section .text:

00000000 <delay_ticks>:
   0:	01 97       	sbiw	r24, 0x01	; 1
   2:	01 f4       	brne	.+0      	; 0x4 <delay_ticks+0x4>
   4:	08 95       	ret

00000006 <toggle_pin>:
   6:	8f 93       	push	r24
   8:	80 e2       	ldi	r24, 0x20	; 32
   a:	80 93 06 04 	sts	0x0406, r24	; 0x800406
   e:	8f 91       	pop	r24
  10:	08 95       	ret
`

func TestLooksLikeObjdump(t *testing.T) {
	if !LooksLikeObjdump(sampleObjdump) {
		t.Fatal("disassembly not recognized as objdump output")
	}
	if LooksLikeObjdump("ldi r24, 1\nret\n") {
		t.Fatal("plain source misclassified as objdump output")
	}
	if LooksLikeObjdump("; Reference: Disassembly of section .text:\nnop\nret\n") {
		t.Fatal("source comment misclassified as objdump output")
	}
}

func TestParseObjdumpInstructions(t *testing.T) {
	lines := ParseObjdump(sampleObjdump)

	type want struct {
		mnem, ops string
	}
	expect := []want{
		{"SBIW", "r24, 0x01"},
		{"BRNE", ".+0"},
		{"RET", ""},
		{"PUSH", "r24"},
		{"LDI", "r24, 0x20"},
		{"STS", "0x0406, r24"},
		{"POP", "r24"},
		{"RET", ""},
	}

	var got []want
	for _, ln := range lines {
		if ln.Mnemonic != "" {
			got = append(got, want{ln.Mnemonic, ln.Operands})
		}
	}
	if len(got) != len(expect) {
		t.Fatalf("instruction count = %d, want %d (%v)", len(got), len(expect), got)
	}
	for i, w := range expect {
		if got[i] != w {
			t.Errorf("instr %d = %q %q, want %q %q", i, got[i].mnem, got[i].ops, w.mnem, w.ops)
		}
	}
}

func TestParseObjdumpPreservesAddresses(t *testing.T) {
	lines := ParseObjdump(sampleObjdump)
	for _, ln := range lines {
		if ln.Mnemonic == "STS" {
			if !ln.HasAddress || ln.Address != 0x0a {
				t.Fatalf("STS address = %#x, present=%t; want 0x0a,true", ln.Address, ln.HasAddress)
			}
			return
		}
	}
	t.Fatal("STS not found")
}

func TestParseObjdumpPreservesRelocationTarget(t *testing.T) {
	lines := ParseObjdump(`
Disassembly of section .text:
   0:  01 f4        brne .+0 ; 0x2
            0: R_AVR_7_PCREL .Ldone
00000004 <.Ldone>:
   4:  08 95        ret
`)
	for _, ln := range lines {
		if ln.Mnemonic == "BRNE" {
			if ln.RelocTarget != ".Ldone" {
				t.Fatalf("relocation target = %q, want .Ldone", ln.RelocTarget)
			}
			return
		}
	}
	t.Fatal("BRNE not found")
}

func TestParseObjdumpHandlesOverlongLine(t *testing.T) {
	src := "Disassembly of section .text:\n" + strings.Repeat("x", 2<<20) + "\n   0:\t08 95       \tret\n"
	lines := ParseObjdump(src)
	if got := countMnemonic(lines, "RET"); got != 1 {
		t.Fatalf("RET count = %d, want 1 after overlong line", got)
	}
}

func TestParseObjdumpLabelsAndComments(t *testing.T) {
	lines := ParseObjdump(sampleObjdump)

	labels := map[string]bool{}
	for _, ln := range lines {
		if ln.Label != "" {
			labels[ln.Label] = true
		}
	}
	for _, name := range []string{"delay_ticks", "toggle_pin"} {
		if !labels[name] {
			t.Errorf("symbol %q not parsed as a label", name)
		}
	}

	// The trailing "; 32" on the LDI must become a comment, not an operand.
	for _, ln := range lines {
		if ln.Mnemonic == "LDI" && ln.Comment != "32" {
			t.Errorf("LDI comment = %q, want %q", ln.Comment, "32")
		}
	}
}

func TestParseObjdumpSectionSizes(t *testing.T) {
	lines := ParseObjdump(sampleObjdump)

	// .bss (0x20) and .rodata (0x04) must surface as .space directives; .data
	// size 0 and .text must not produce data directives.
	var bss, rodata int
	for _, ln := range lines {
		if ln.Directive == ".space" {
			n, _ := DataBytes(ln.Directive, ln.DirectiveArgs)
			switch ln.Section {
			case ".bss":
				bss += n
			case ".rodata":
				rodata += n
			default:
				t.Errorf(".space emitted for unexpected section %q", ln.Section)
			}
		}
	}
	if bss != 32 {
		t.Errorf("static SRAM from sections = %d, want 32", bss)
	}
	if rodata != 4 {
		t.Errorf("flash data from sections = %d, want 4", rodata)
	}
}
