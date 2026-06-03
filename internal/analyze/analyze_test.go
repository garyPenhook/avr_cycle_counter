package analyze_test

import (
	"testing"

	"cyclecount/internal/analyze"
	"cyclecount/internal/asm"
	"cyclecount/internal/isa"
)

var attiny3217 = analyze.Target{Name: "ATtiny3217", Variant: isa.VarAVRxt, PCBytes: 2}

func TestDelayLoopAVRxt(t *testing.T) {
	src := `loop:
	sbiw r24, 1   ; 2
	brne loop     ; 1-2
	ret           ; 4 on AVRxt
`
	m := analyze.Analyze(asm.Parse(src), attiny3217).File
	if m.InstrCount != 3 {
		t.Errorf("instructions = %d, want 3", m.InstrCount)
	}
	if m.FlashWords != 3 || m.FlashBytes() != 6 {
		t.Errorf("flash = %d words / %d bytes, want 3 / 6", m.FlashWords, m.FlashBytes())
	}
	// min: 2 + 1 + 4 = 7 ; max: 2 + 2 + 4 = 8
	if m.CyclesMin != 7 || m.CyclesMax != 8 {
		t.Errorf("cycles = %d–%d, want 7–8", m.CyclesMin, m.CyclesMax)
	}
}

// Same source, classic AVRe core: RET costs 4 too, but PUSH/ST/etc. differ.
// Here we check that core selection actually changes the numbers where it must.
func TestCoreAffectsTiming(t *testing.T) {
	src := "f:\n\tpush r16\n\tst X, r16\n\tpop r16\n\tret\n"
	xt := analyze.Analyze(asm.Parse(src), attiny3217).File
	avre := analyze.Analyze(asm.Parse(src),
		analyze.Target{Name: "AVRe", Variant: isa.VarAVRe, PCBytes: 2}).File

	// AVRxt: push 1 + st 1 + pop 2 + ret 4 = 8
	if xt.CyclesMin != 8 {
		t.Errorf("AVRxt cycles = %d, want 8", xt.CyclesMin)
	}
	// AVRe: push 2 + st 2 + pop 2 + ret 4 = 10
	if avre.CyclesMin != 10 {
		t.Errorf("AVRe cycles = %d, want 10", avre.CyclesMin)
	}
}

// On the 22-bit-PC ATmega2560, CALL/RET cost one extra cycle each.
func TestPCWidthAddsCycle(t *testing.T) {
	src := "f:\n\tcall g\n\tret\n"
	pc16 := analyze.Analyze(asm.Parse(src),
		analyze.Target{Variant: isa.VarAVRePlus, PCBytes: 2}).File
	pc22 := analyze.Analyze(asm.Parse(src),
		analyze.Target{Variant: isa.VarAVRePlus, PCBytes: 3}).File
	// AVRe+ 16-bit: call 4 + ret 4 = 8 ; 22-bit: 5 + 5 = 10
	if pc16.CyclesMin != 8 || pc22.CyclesMin != 10 {
		t.Errorf("call+ret = %d (16-bit) / %d (22-bit), want 8 / 10", pc16.CyclesMin, pc22.CyclesMin)
	}
}

// CALL does not exist on the Reduced Core; it must be flagged, not counted.
func TestUnavailableOnReducedCore(t *testing.T) {
	src := "f:\n\tcall g\n\tnop\n"
	m := analyze.Analyze(asm.Parse(src),
		analyze.Target{Variant: isa.VarAVRrc, PCBytes: 2}).File
	if m.Unavailable["CALL"] != 1 {
		t.Errorf("CALL should be unavailable on AVRrc, got %v", m.Unavailable)
	}
	if m.InstrCount != 1 { // only NOP counts
		t.Errorf("instr count = %d, want 1", m.InstrCount)
	}
}

func TestRegionIterAndStack(t *testing.T) {
	src := `	; @begin hot iter=100
	push r16
	dec r16
	pop r16
	; @end hot
`
	res := analyze.Analyze(asm.Parse(src), attiny3217)
	if len(res.Regions) != 1 {
		t.Fatalf("regions = %d, want 1", len(res.Regions))
	}
	r := res.Regions[0]
	if r.Iter != 100 {
		t.Errorf("iter = %d, want 100", r.Iter)
	}
	if r.CyclesMin != 4 || r.CyclesMax != 4 { // push 1 + dec 1 + pop 2
		t.Errorf("cycles/pass = %d–%d, want 4", r.CyclesMin, r.CyclesMax)
	}
	if r.PeakStackBytes != 1 || r.Pushes != 1 || r.Pops != 1 {
		t.Errorf("stack: peak=%d push=%d pop=%d, want 1/1/1", r.PeakStackBytes, r.Pushes, r.Pops)
	}
}

func TestStaticSRAM(t *testing.T) {
	src := `	.section .bss
buf:	.space 32
	.section .data
tab:	.word 1, 2, 3
	.section .text
	nop
`
	res := analyze.Analyze(asm.Parse(src), attiny3217)
	if res.SRAMStatic != 38 {
		t.Errorf("sram static = %d, want 38", res.SRAMStatic)
	}
}

func TestDeviceLookup(t *testing.T) {
	cases := map[string]struct {
		v  isa.Variant
		pc int
	}{
		"attiny3217": {isa.VarAVRxt, 2},
		"atmega328p": {isa.VarAVRePlus, 2},
		"atmega2560": {isa.VarAVRePlus, 3},
		"attiny85":   {isa.VarAVRe, 2},
		"attiny10":   {isa.VarAVRrc, 2},
		"avr128da48": {isa.VarAVRxt, 2}, // matched by Dx regex
	}
	for name, want := range cases {
		d, ok := isa.LookupDevice(name)
		if !ok {
			t.Errorf("%s: not found", name)
			continue
		}
		if d.Variant != want.v || d.PCBytes != want.pc {
			t.Errorf("%s: got %s/pc%d, want %s/pc%d", name, d.Variant, d.PCBytes, want.v, want.pc)
		}
	}
}
