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
	if r.PeakStackBytes != 1 || r.PeakPushBytes != 1 || r.PeakCallBytes != 0 || r.Pushes != 1 || r.Pops != 1 {
		t.Errorf("stack: peak=%d pushpeak=%d callpeak=%d push=%d pop=%d, want 1/1/0/1/1",
			r.PeakStackBytes, r.PeakPushBytes, r.PeakCallBytes, r.Pushes, r.Pops)
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

func TestAnalyzeExtractsTopLevelSymbols(t *testing.T) {
	src := `first:
	nop
	ret
.L_inner:
	nop
second:
	push r16
	pop r16
	ret
`
	res := analyze.Analyze(asm.Parse(src), attiny3217)
	if len(res.Symbols) != 2 {
		t.Fatalf("symbols = %d, want 2", len(res.Symbols))
	}
	if res.Symbols[0].Name != "first" || res.Symbols[1].Name != "second" {
		t.Fatalf("unexpected symbols: %+v", []string{res.Symbols[0].Name, res.Symbols[1].Name})
	}
}

func TestSymbolMetricsStopsAtNextNonLocalLabel(t *testing.T) {
	src := `first:
	push r16
.L_loop:
	dec r16
	brne .L_loop
second:
	ret
`
	m, err := analyze.SymbolMetrics(asm.Parse(src), "first", 1, attiny3217)
	if err != nil {
		t.Fatalf("SymbolMetrics: %v", err)
	}
	if m.InstrCount != 3 {
		t.Fatalf("instructions = %d, want 3", m.InstrCount)
	}
	if _, ok := m.Hist["RET"]; ok {
		t.Fatal("symbol span should stop before next non-local label")
	}
}

func TestSymbolMetricsIncludesToEOFWhenLastSymbol(t *testing.T) {
	src := `delay_ticks:
	sbiw r24, 1
	brne delay_ticks
	ret
`
	m, err := analyze.SymbolMetrics(asm.Parse(src), "delay_ticks", 10, attiny3217)
	if err != nil {
		t.Fatalf("SymbolMetrics: %v", err)
	}
	if m.Iter != 10 {
		t.Fatalf("iter = %d, want 10", m.Iter)
	}
	if m.InstrCount != 3 {
		t.Fatalf("instructions = %d, want 3", m.InstrCount)
	}
}

func TestBranchModeConditionalBranch(t *testing.T) {
	src := `loop:
	brne loop
	ret
`
	lines := asm.Parse(src)

	best := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchBest).File
	if best.CyclesMin != 5 || best.CyclesMax != 5 {
		t.Fatalf("best = %d-%d, want 5", best.CyclesMin, best.CyclesMax)
	}

	worst := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchWorst).File
	if worst.CyclesMin != 6 || worst.CyclesMax != 6 {
		t.Fatalf("worst = %d-%d, want 6", worst.CyclesMin, worst.CyclesMax)
	}

	taken := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchTaken).File
	if taken.CyclesMin != 2 || taken.CyclesMax != 2 {
		t.Fatalf("taken = %d-%d, want 2", taken.CyclesMin, taken.CyclesMax)
	}

	notTaken := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchNotTaken).File
	if notTaken.CyclesMin != 5 || notTaken.CyclesMax != 5 {
		t.Fatalf("not-taken = %d-%d, want 5", notTaken.CyclesMin, notTaken.CyclesMax)
	}
}

func TestBranchModeNotTakenPrunesUnreachableTail(t *testing.T) {
	src := `start:
	brne skip
	ret
skip:
	push r16
	pop r16
	ret
`
	m := analyze.AnalyzeMode(asm.Parse(src), attiny3217, analyze.BranchNotTaken).File
	if m.InstrCount != 2 {
		t.Fatalf("instructions = %d, want 2", m.InstrCount)
	}
	if m.CyclesMin != 5 || m.CyclesMax != 5 {
		t.Fatalf("cycles = %d-%d, want 5", m.CyclesMin, m.CyclesMax)
	}
}

func TestBranchModeTakenFollowsForwardBranch(t *testing.T) {
	src := `start:
	brne taken
	ret
taken:
	push r16
	pop r16
	ret
`
	m := analyze.AnalyzeMode(asm.Parse(src), attiny3217, analyze.BranchTaken).File
	if m.InstrCount != 4 {
		t.Fatalf("instructions = %d, want 4", m.InstrCount)
	}
	if m.CyclesMin != 9 || m.CyclesMax != 9 {
		t.Fatalf("cycles = %d-%d, want 9", m.CyclesMin, m.CyclesMax)
	}
}

func TestBranchModeSkipInstructionUsesNextWordSize(t *testing.T) {
	src := `start:
	cpse r16, r17
	jmp done
done:
	ret
`
	lines := asm.Parse(src)

	taken := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchTaken).File
	if taken.CyclesMin != 7 || taken.CyclesMax != 7 {
		t.Fatalf("taken = %d-%d, want 7", taken.CyclesMin, taken.CyclesMax)
	}

	notTaken := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchNotTaken).File
	if notTaken.CyclesMin != 8 || notTaken.CyclesMax != 8 {
		t.Fatalf("not-taken = %d-%d, want 8", notTaken.CyclesMin, notTaken.CyclesMax)
	}
}

// In not-taken mode the pushes on the unreachable (taken) path must not count
// toward peak stack, matching the pruned instruction/cycle accounting.
func TestStackPeakRespectsBranchPruning(t *testing.T) {
	src := `start:
	brne heavy
	ret
heavy:
	push r16
	push r17
	pop r17
	pop r16
	ret
`
	lines := asm.Parse(src)
	bounds := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchBounds).File
	if bounds.PeakStackBytes != 2 {
		t.Fatalf("bounds peak stack = %d, want 2", bounds.PeakStackBytes)
	}
	notTaken := analyze.AnalyzeMode(lines, attiny3217, analyze.BranchNotTaken).File
	if notTaken.PeakStackBytes != 0 {
		t.Fatalf("not-taken peak stack = %d, want 0 (heavy path pruned)", notTaken.PeakStackBytes)
	}
}

func TestCallSiteAddsReturnAddressToPeakStack(t *testing.T) {
	src := `f:
	push r16
	call g
	pop r16
	ret
g:
	ret
`
	m := analyze.Analyze(asm.Parse(src), attiny3217).File
	if m.PeakPushBytes != 1 {
		t.Fatalf("peak push = %d, want 1", m.PeakPushBytes)
	}
	if m.PeakCallBytes != 2 {
		t.Fatalf("peak call bytes = %d, want 2", m.PeakCallBytes)
	}
	if m.PeakStackBytes != 3 {
		t.Fatalf("peak stack = %d, want 3", m.PeakStackBytes)
	}
}

func TestCallSiteUsesPCWidthForReturnAddressPeak(t *testing.T) {
	src := `f:
	push r16
	call g
	pop r16
	ret
g:
	ret
`
	pc22 := analyze.Analyze(asm.Parse(src),
		analyze.Target{Variant: isa.VarAVRePlus, PCBytes: 3}).File
	if pc22.PeakCallBytes != 3 {
		t.Fatalf("peak call bytes = %d, want 3", pc22.PeakCallBytes)
	}
	if pc22.PeakStackBytes != 4 {
		t.Fatalf("peak stack = %d, want 4", pc22.PeakStackBytes)
	}
}

func TestDirectCallIncludesCalleeLocalStack(t *testing.T) {
	src := `f:
	push r16
	call g
	pop r16
	ret
g:
	push r17
	push r18
	pop r18
	pop r17
	ret
`
	m := analyze.Analyze(asm.Parse(src), attiny3217).File
	if m.PeakPushBytes != 2 {
		t.Fatalf("peak push = %d, want 2", m.PeakPushBytes)
	}
	if m.PeakCallBytes != 3 {
		t.Fatalf("peak call bytes = %d, want 3", m.PeakCallBytes)
	}
	if m.PeakStackBytes != 5 {
		t.Fatalf("peak stack = %d, want 5", m.PeakStackBytes)
	}
}

func TestRecursiveCallDoesNotLoopForever(t *testing.T) {
	src := `f:
	push r16
	call f
	pop r16
	ret
`
	m, err := analyze.SymbolMetrics(asm.Parse(src), "f", 1, attiny3217)
	if err != nil {
		t.Fatalf("SymbolMetrics: %v", err)
	}
	if m.PeakStackBytes != 3 {
		t.Fatalf("peak stack = %d, want 3", m.PeakStackBytes)
	}
}

func TestDeviceLookup(t *testing.T) {
	cases := map[string]struct {
		v  isa.Variant
		pc int
	}{
		"attiny3217":     {isa.VarAVRxt, 2},
		"atmega328p":     {isa.VarAVRePlus, 2},
		"atmega328pa":    {isa.VarAVRePlus, 2}, // family fallback
		"atmega1284pa":   {isa.VarAVRePlus, 2}, // family fallback
		"atmega2560":     {isa.VarAVRePlus, 3},
		"attiny85":       {isa.VarAVRe, 2},
		"attiny84a":      {isa.VarAVRe, 2}, // family fallback
		"attiny10":       {isa.VarAVRrc, 2},
		"avr128da48":     {isa.VarAVRxt, 2}, // modern AVR family fallback
		"atxmega128a4u":  {isa.VarAVRxm, 2}, // family fallback
		"atxmega256a3bu": {isa.VarAVRxm, 3}, // family fallback
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
