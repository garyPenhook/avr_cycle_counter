package isa_test

import (
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
		{"PUSH", isa.VarAVRe, 2, 2, 2, true},
		{"PUSH", isa.VarAVRxt, 2, 1, 1, true},
		{"BRNE", isa.VarAVRxt, 2, 1, 2, true},
		{"MUL", isa.VarAVRrc, 2, 0, 0, false}, // no multiplier on reduced core
		{"SPM", isa.VarAVRe, 2, 0, 0, false},  // recognized but not cycle-modeled
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
		mn   string
		v    isa.Variant
		pc   int
		want bool
	}{
		{"ADD", isa.VarAVRrc, 2, true}, // present on every variant
		{"MUL", isa.VarAVRe, 2, false}, // AVRe+ and up only
		{"MUL", isa.VarAVRePlus, 2, true},
		{"CALL", isa.VarAVR, 2, false}, // not on the original core
		{"CALL", isa.VarAVRe, 2, true},
		{"MOVW", isa.VarAVR, 2, false},
		{"BREAK", isa.VarAVR, 2, false},
		{"BREAK", isa.VarAVRrc, 2, true},
		{"EICALL", isa.VarAVRxt, 2, false}, // AVRxt only for >128 KB parts
		{"EICALL", isa.VarAVRxt, 3, true},
		{"EICALL", isa.VarAVRePlus, 2, true},
		{"DES", isa.VarAVRxm, 2, true},
		{"DES", isa.VarAVRxt, 2, false},
	}
	for _, c := range cases {
		if got := isa.Available(c.mn, c.v, c.pc); got != c.want {
			t.Errorf("Available(%q,%v,pc%d) = %t; want %t", c.mn, c.v, c.pc, got, c.want)
		}
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
		if !ok || got.Variant != d.Variant || got.PCBytes != d.PCBytes {
			t.Errorf("LookupDevice(%q) = %+v,%t; want %+v", d.Name, got, ok, d)
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
	if !ok || d.Variant != isa.VarAVRxt || d.PCBytes != 2 {
		t.Errorf("avr128da48 family fallback = %+v,%t; want AVRxt/pc2", d, ok)
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
