package isa_test

import (
	"strings"
	"testing"

	"cyclecount/internal/isa"
)

func TestParseVariant(t *testing.T) {
	cases := map[string]struct {
		v  isa.Variant
		ok bool
	}{
		"avr":     {isa.VarAVR, true},
		"avre":    {isa.VarAVRe, true},
		"AVRe+":   {isa.VarAVRePlus, true},
		"avrep":   {isa.VarAVRePlus, true},
		"xmega":   {isa.VarAVRxm, true},
		"avrxt":   {isa.VarAVRxt, true},
		"reduced": {isa.VarAVRrc, true},
		"bogus":   {0, false},
	}
	for s, want := range cases {
		v, ok := isa.ParseVariant(s)
		if ok != want.ok || (ok && v != want.v) {
			t.Errorf("ParseVariant(%q) = %v,%t; want %v,%t", s, v, ok, want.v, want.ok)
		}
	}
}

func TestCycles(t *testing.T) {
	cases := []struct {
		mn       string
		v        isa.Variant
		pc       int
		min, max int
		ok       bool
	}{
		{"ADD", isa.VarAVRxt, 2, 1, 1, true},
		{"CALL", isa.VarAVRe, 2, 4, 4, true},
		{"CALL", isa.VarAVRe, 3, 5, 5, true}, // 22-bit PC adds a cycle
		{"CALL", isa.VarAVRxt, 2, 3, 3, true},
		{"RET", isa.VarAVRrc, 2, 6, 6, true},
		{"RET", isa.VarAVRePlus, 3, 5, 5, true},
		// EICALL has a single fixed cost (Table 6-51): no +1 on 22-bit PC,
		// since it only exists on extended-PC parts in the first place.
		{"EICALL", isa.VarAVRePlus, 3, 4, 4, true},
		{"EICALL", isa.VarAVRxt, 3, 3, 3, true},
		{"PUSH", isa.VarAVRe, 2, 2, 2, true},
		{"PUSH", isa.VarAVRxt, 2, 1, 1, true},
		{"BRNE", isa.VarAVRxt, 2, 1, 2, true},
		{"MUL", isa.VarAVRrc, 2, 0, 0, false}, // no multiplier on reduced core
		{"SPM", isa.VarAVRe, 2, 0, 0, false},  // recognized but not cycle-modeled
		{"XCH", isa.VarAVRxm, 2, 2, 2, true},
		{"LAC", isa.VarAVRxm, 2, 2, 2, true},
		{"LAS", isa.VarAVRxm, 2, 2, 2, true},
		{"LAT", isa.VarAVRxm, 2, 2, 2, true},
		{"DES", isa.VarAVRxm, 2, 1, 2, true},
	}
	for _, c := range cases {
		info, ok := isa.Lookup(c.mn)
		if !ok {
			t.Errorf("Lookup(%q) not found", c.mn)
			continue
		}
		cc, ok := info.Cycles(c.v, c.pc)
		if ok != c.ok {
			t.Errorf("%s.Cycles(%v,pc%d) ok = %t; want %t", c.mn, c.v, c.pc, ok, c.ok)
			continue
		}
		if ok && (cc.Min != c.min || cc.Max != c.max) {
			t.Errorf("%s.Cycles(%v,pc%d) = %d-%d; want %d-%d", c.mn, c.v, c.pc, cc.Min, cc.Max, c.min, c.max)
		}
	}
}

func TestAvailable(t *testing.T) {
	cases := []struct {
		mn      string
		v       isa.Variant
		pc      int
		flashKB int
		want    bool
	}{
		{"ADD", isa.VarAVRrc, 2, 1, true}, // present on every variant
		{"MUL", isa.VarAVRe, 2, 8, false}, // AVRe+ and up only
		{"MUL", isa.VarAVRePlus, 2, 32, true},
		{"CALL", isa.VarAVRrc, 2, 4, false}, // Reduced Core never has CALL/JMP
		{"CALL", isa.VarAVRe, 2, 0, true},   // core-level availability only
		{"MOVW", isa.VarAVR, 2, 128, false}, // MOVW is core-gated, not flash-gated
		{"MOVW", isa.VarAVRe, 2, 1, true},   // ATtiny13: 1 KB but has MOVW
		{"BREAK", isa.VarAVR, 2, 1, false},
		{"BREAK", isa.VarAVRrc, 2, 1, true},
		{"EICALL", isa.VarAVRxt, 2, 32, false}, // AVRxt only for >128 KB parts
		{"EICALL", isa.VarAVRxt, 3, 256, true},
		{"EICALL", isa.VarAVRePlus, 2, 64, false},
		{"EICALL", isa.VarAVRePlus, 3, 256, true},
		{"DES", isa.VarAVRxm, 2, 64, true},
		{"DES", isa.VarAVRxt, 2, 32, false},
	}
	for _, c := range cases {
		if got := isa.Available(c.mn, c.v, c.pc, c.flashKB); got != c.want {
			t.Errorf("Available(%q,%v,pc%d,flash%d) = %t; want %t", c.mn, c.v, c.pc, c.flashKB, got, c.want)
		}
	}
}

func TestAvailableOnTarget(t *testing.T) {
	cases := []struct {
		device string
		mn     string
		want   bool
	}{
		{"attiny85", "CALL", false},
		{"attiny85", "MOVW", true},
		{"attiny1634", "CALL", true},
		{"atmega48p", "JMP", false},
		{"atmega328p", "JMP", true},
		{"atmega328p", "ELPM", false},
		{"atmega2560", "EICALL", true},
		{"attiny3217", "CALL", true},
		{"attiny804", "CALL", false},
		{"attiny804", "SPM", false},
		{"avr128da48", "ELPM", true},
		{"avr128da48", "EICALL", false},
		{"avr64da48", "ELPM", false},
		{"attiny11", "LPM", true},
		{"attiny26", "CALL", false}, // original AVR core lacks CALL/JMP (Table 7-1)
		{"attiny40", "BREAK", true},
		{"attiny10", "BREAK", false},
		{"at90can32", "ELPM", false},
	}
	for _, c := range cases {
		d, ok := isa.LookupDevice(c.device)
		if !ok {
			t.Fatalf("LookupDevice(%q) failed", c.device)
		}
		if got := isa.AvailableOnTarget(c.mn, d.Variant, d.PCBytes, d.FlashKB, d.Missing); got != c.want {
			t.Errorf("AvailableOnTarget(%q on %s) = %t; want %t", c.mn, d.Name, got, c.want)
		}
	}
}

func TestAvailableOnTargetForm(t *testing.T) {
	d, ok := isa.LookupDevice("attiny11")
	if !ok {
		t.Fatal("LookupDevice(attiny11) failed")
	}
	if isa.AvailableOnTargetForm("LD", "r16, X", d.Variant, d.PCBytes, d.FlashKB, d.Missing) {
		t.Fatal("LD r16, X should be unavailable on ATtiny11")
	}
	if !isa.AvailableOnTargetForm("LPM", "", d.Variant, d.PCBytes, d.FlashKB, d.Missing) {
		t.Fatal("LPM should be available on ATtiny11 per DS40002198C")
	}
	if isa.AvailableOnTargetForm("LPM", "r16, Z+", d.Variant, d.PCBytes, d.FlashKB, d.Missing) {
		t.Fatal("LPM r16,Z+ should be unavailable on original AVR/ATtiny11")
	}
	if !isa.AvailableOnTargetForm("LPM", "r16, Z+", isa.VarAVRe, 2, 8, nil) {
		t.Fatal("LPM r16,Z+ should be available on AVRe")
	}

	if isa.AvailableOnTargetForm("SPM", "Z+", isa.VarAVRe, 2, 8, nil) {
		t.Fatal("SPM Z+ should be unavailable on AVRe")
	}
	if !isa.AvailableOnTargetForm("SPM", "Z+", isa.VarAVRxt, 2, 32, nil) {
		t.Fatal("SPM Z+ should be available on AVRxt")
	}
	if !isa.AvailableOnTargetForm("SPM", "", isa.VarAVRe, 2, 8, nil) {
		t.Fatal("bare SPM should remain available on AVRe")
	}
}

func TestDevicesSortedAndConsistent(t *testing.T) {
	devs := isa.Devices()
	if len(devs) == 0 {
		t.Fatal("Devices() returned no entries")
	}
	for i := 1; i < len(devs); i++ {
		if devs[i-1].Name > devs[i].Name {
			t.Fatalf("Devices() not sorted: %q before %q", devs[i-1].Name, devs[i].Name)
		}
	}
	// Every listed device must round-trip through LookupDevice unchanged.
	for _, d := range devs {
		got, ok := isa.LookupDevice(d.Name)
		if !ok || got.Variant != d.Variant || got.PCBytes != d.PCBytes || got.FlashKB != d.FlashKB {
			t.Errorf("LookupDevice(%q) = %+v,%t; want %+v", d.Name, got, ok, d)
		}
	}
}

func TestGeneratedMissingInstructionsAreKnown(t *testing.T) {
	for _, d := range isa.Devices() {
		for form := range d.Missing {
			mn := strings.Fields(form)[0]
			if _, ok := isa.Lookup(mn); !ok {
				t.Errorf("%s has unknown missing-instruction entry %q", d.Name, form)
			}
		}
	}
}

func TestFamilies(t *testing.T) {
	fams := isa.Families()
	if len(fams) == 0 {
		t.Fatal("Families() returned no entries")
	}
	// A part matched only by a family pattern should resolve via the fallback.
	d, ok := isa.LookupDevice("avr128da48")
	if !ok || d.Variant != isa.VarAVRxt || d.PCBytes != 2 || d.FlashKB != 128 {
		t.Errorf("avr128da48 family fallback = %+v,%t; want AVRxt/pc2/128KB", d, ok)
	}
	// ATtiny85 (family fallback) has MOVW but no CALL/JMP.
	t85, _ := isa.LookupDevice("attiny85")
	if t85.FlashKB != 8 ||
		!isa.AvailableOnTarget("MOVW", t85.Variant, t85.PCBytes, t85.FlashKB, t85.Missing) ||
		isa.AvailableOnTarget("CALL", t85.Variant, t85.PCBytes, t85.FlashKB, t85.Missing) {
		t.Errorf("attiny85 = %+v; want MOVW but no CALL", t85)
	}
	// AVR SD parts keep ELPM: Appendix A (DS40002198C Table 7-4) lists only
	// EIJMP/EICALL as missing, unlike same-size DA/DB/DD/DU/EA/EB parts.
	sd, ok := isa.LookupDevice("avr64sd48")
	if !ok || sd.Variant != isa.VarAVRxt || sd.FlashKB != 64 ||
		!isa.AvailableOnTarget("ELPM", sd.Variant, sd.PCBytes, sd.FlashKB, sd.Missing) ||
		isa.AvailableOnTarget("EICALL", sd.Variant, sd.PCBytes, sd.FlashKB, sd.Missing) {
		t.Errorf("avr64sd48 = %+v,%t; want AVRxt/64KB with ELPM but no EICALL", sd, ok)
	}
	// A same-size EA part still lacks ELPM, proving the SD rule is separate.
	ea, ok := isa.LookupDevice("avr64ea48")
	if !ok || isa.AvailableOnTarget("ELPM", ea.Variant, ea.PCBytes, ea.FlashKB, ea.Missing) {
		t.Errorf("avr64ea48 = %+v,%t; want ELPM unavailable", ea, ok)
	}
}

func TestWordCount(t *testing.T) {
	lds, _ := isa.Lookup("LDS")
	if got := lds.WordCount(isa.VarAVRrc); got != 1 {
		t.Errorf("LDS.WordCount(AVRrc) = %d, want 1", got)
	}
	if got := lds.WordCount(isa.VarAVRxt); got != 2 {
		t.Errorf("LDS.WordCount(AVRxt) = %d, want 2", got)
	}
	call, _ := isa.Lookup("CALL")
	if got := call.WordCount(isa.VarAVRe); got != 2 {
		t.Errorf("CALL.WordCount(AVRe) = %d, want 2", got)
	}
}
