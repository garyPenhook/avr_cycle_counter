package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"cyclecount/internal/analyze"
	"cyclecount/internal/isa"
)

func TestEvalBudgets(t *testing.T) {
	res := analyze.Result{
		File:       analyze.Metrics{Name: "(whole file)", Iter: 1, CyclesMax: 18, FlashWords: 9},
		SRAMStatic: 32,
	}

	// No limits set: nothing to check.
	if cs := evalBudgets(budgets{}, res, nil, nil); cs != nil {
		t.Fatalf("expected no checks with empty budgets, got %v", cs)
	}

	// Cycles within limit, flash and SRAM over.
	cs := evalBudgets(budgets{cycles: 20, flash: 10, sram: 16}, res, nil, nil)
	if len(cs) != 3 {
		t.Fatalf("expected 3 checks, got %d: %v", len(cs), cs)
	}
	want := map[string]struct {
		got int
		ok  bool
	}{
		"cycles": {18, true},  // 18 <= 20
		"flash":  {18, false}, // 9 words*2 = 18 > 10
		"sram":   {32, false}, // 32 > 16
	}
	for _, c := range cs {
		w, found := want[c.Name]
		if !found {
			t.Errorf("unexpected check %q", c.Name)
			continue
		}
		if c.Got != w.got || c.OK != w.ok {
			t.Errorf("%s: got=%d ok=%v, want got=%d ok=%v", c.Name, c.Got, c.OK, w.got, w.ok)
		}
	}
	if !budgetsExceeded(cs) {
		t.Error("budgetsExceeded should be true when flash/sram exceed limits")
	}
}

func TestCPPMCU(t *testing.T) {
	cases := []struct {
		mcu, core, want string
	}{
		{"", "", defaultMCU},              // pure default target → default part's headers
		{"atmega328p", "", "atmega328p"},  // explicit -mcu wins
		{"ATmega328P", "", "atmega328p"},  // normalized to lowercase for avr-gcc
		{"", "avrrc", ""},                 // bare -core has no single device
		{"attiny10", "avrrc", "attiny10"}, // -mcu still wins over -core
	}
	for _, c := range cases {
		if got := cppMCU(c.mcu, c.core); got != c.want {
			t.Errorf("cppMCU(%q,%q)=%q, want %q", c.mcu, c.core, got, c.want)
		}
	}
}

func TestEvalBudgetsCyclesScopeAndIter(t *testing.T) {
	res := analyze.Result{File: analyze.Metrics{Name: "(whole file)", Iter: 1, CyclesMax: 18}}
	// A range with a trip count: the budget gates CyclesMax * iter.
	rng := analyze.Metrics{Name: "inner", Iter: 1000, CyclesMax: 4}

	cs := evalBudgets(budgets{cycles: 3500}, res, &rng, nil)
	if len(cs) != 1 {
		t.Fatalf("expected 1 check, got %d", len(cs))
	}
	c := cs[0]
	if c.Got != 4000 { // 4 * 1000, not the whole-file 18
		t.Errorf("got=%d, want 4000 (range total)", c.Got)
	}
	if c.OK { // 4000 > 3500
		t.Error("expected EXCEEDED for 4000 > 3500")
	}
	if c.Scope != "range inner" {
		t.Errorf("scope=%q, want %q", c.Scope, "range inner")
	}
}

func TestEvalBudgetsSymbolScope(t *testing.T) {
	res := analyze.Result{File: analyze.Metrics{Name: "(whole file)", Iter: 1, CyclesMax: 18}}
	sym := analyze.Metrics{Name: "toggle_pin", Iter: 5, CyclesMax: 7}

	cs := evalBudgets(budgets{cycles: 34}, res, nil, &sym)
	if len(cs) != 1 {
		t.Fatalf("expected 1 check, got %d", len(cs))
	}
	c := cs[0]
	if c.Got != 35 {
		t.Fatalf("got=%d, want 35", c.Got)
	}
	if c.Scope != "symbol toggle_pin" {
		t.Fatalf("scope=%q, want symbol toggle_pin", c.Scope)
	}
	if c.OK {
		t.Fatal("expected budget to be exceeded")
	}
}

func TestValidateSelectionFlags(t *testing.T) {
	oldFrom, oldTo, oldIter := *flFrom, *flTo, *flIter
	oldBranches := *flBranches
	oldSymbol, oldFunc := *flSymbol, *flFunc
	t.Cleanup(func() {
		*flFrom, *flTo, *flIter = oldFrom, oldTo, oldIter
		*flBranches = oldBranches
		*flSymbol, *flFunc = oldSymbol, oldFunc
	})

	*flFrom, *flTo, *flIter = "", "", 1
	*flBranches = "bounds"
	*flSymbol, *flFunc = "foo", ""
	if err := validateSelectionFlags(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	*flFrom, *flTo = "start", "end"
	if err := validateSelectionFlags(); err == nil {
		t.Fatal("expected conflict for -symbol with -from/-to")
	}

	*flFrom, *flTo = "", ""
	*flSymbol, *flFunc = "foo", "bar"
	if err := validateSelectionFlags(); err == nil {
		t.Fatal("expected conflict for mismatched -symbol/-func")
	}

	*flSymbol, *flFunc, *flIter = "", "", 0
	if err := validateSelectionFlags(); err == nil {
		t.Fatal("expected -iter validation error")
	}

	*flIter = 1
	*flBranches = "bogus"
	if err := validateSelectionFlags(); err == nil {
		t.Fatal("expected invalid -branches error")
	}
}

func TestCSVList(t *testing.T) {
	got := csvList(" attiny3217, atmega328p ,, attiny10 ")
	want := []string{"attiny3217", "atmega328p", "attiny10"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveTargetsMultiMCU(t *testing.T) {
	oldMCU, oldCore, oldPC := *flMCU, *flCore, *flPC
	t.Cleanup(func() {
		*flMCU, *flCore, *flPC = oldMCU, oldCore, oldPC
	})

	*flMCU, *flCore, *flPC = "attiny3217,atmega2560", "", 0
	ts, err := resolveTargets()
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(ts) != 2 {
		t.Fatalf("len=%d, want 2", len(ts))
	}
	if ts[0].Name != "ATTINY3217" || ts[1].Name != "ATMEGA2560" {
		t.Fatalf("unexpected targets: %+v", ts)
	}
	if ts[1].PCBytes != 3 {
		t.Fatalf("pc bytes = %d, want 3", ts[1].PCBytes)
	}
}

func TestResolveTargetsRejectsMixedMatrix(t *testing.T) {
	oldMCU, oldCore := *flMCU, *flCore
	t.Cleanup(func() {
		*flMCU, *flCore = oldMCU, oldCore
	})

	*flMCU, *flCore = "attiny3217,atmega328p", "avrxt"
	if _, err := resolveTargets(); err == nil {
		t.Fatal("expected mixed multi-target error")
	}
}

func TestResolveTargetsRejectsCPPMultiMCU(t *testing.T) {
	oldMCU, oldCore, oldCPP := *flMCU, *flCore, *flCPP
	t.Cleanup(func() {
		*flMCU, *flCore, *flCPP = oldMCU, oldCore, oldCPP
	})

	*flMCU, *flCore, *flCPP = "attiny3217,atmega328p", "", true
	if _, err := resolveTargets(); err == nil {
		t.Fatal("expected -cpp multi-mcu error")
	}
}

func TestValidateModeFlags(t *testing.T) {
	oldVS, oldVerbose := *flVS, *flVerbose
	oldCycles, oldFlash, oldSRAM := *flMaxCycles, *flMaxFlash, *flMaxSRAM
	oldFrom, oldTo := *flFrom, *flTo
	oldFormat := *flFormat
	t.Cleanup(func() {
		*flVS, *flVerbose = oldVS, oldVerbose
		*flMaxCycles, *flMaxFlash, *flMaxSRAM = oldCycles, oldFlash, oldSRAM
		*flFrom, *flTo = oldFrom, oldTo
		*flFormat = oldFormat
	})

	*flVS, *flVerbose = "", false
	*flFormat = "text"
	*flMaxCycles, *flMaxFlash, *flMaxSRAM = 0, 0, 0
	*flFrom, *flTo = "a", "b"
	if err := validateModeFlags(false, fmtText); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	*flVS = "base.S"
	if err := validateModeFlags(true, fmtText); err == nil {
		t.Fatal("expected -vs conflict in matrix mode")
	}
	*flVS = ""

	*flMaxCycles = 1
	if err := validateModeFlags(true, fmtText); err == nil {
		t.Fatal("expected budget conflict in matrix mode")
	}
	*flMaxCycles = 0

	*flVerbose = true
	if err := validateModeFlags(true, fmtText); err == nil {
		t.Fatal("expected -v conflict in matrix mode")
	}
	*flVerbose = false

	*flFrom, *flTo = "only-from", ""
	if err := validateModeFlags(false, fmtText); err == nil {
		t.Fatal("expected incomplete range error")
	}

	*flFrom, *flTo = "a", "b"
	*flVerbose = true
	if err := validateModeFlags(false, fmtCSV); err == nil {
		t.Fatal("expected -v conflict with non-text format")
	}
}

func TestRenderMatrix(t *testing.T) {
	runs := []analysisRun{
		{
			Target: isaTarget("ATtiny3217", isa.VarAVRxt, 2),
			Result: analyze.Result{SRAMStatic: 8},
			Symbol: &analyze.Metrics{Name: "toggle_pin", CyclesMin: 7, CyclesMax: 7, InstrCount: 5, PeakStackBytes: 1},
		},
		{
			Target: isaTarget("ATmega328P", isa.VarAVRePlus, 2),
			Result: analyze.Result{SRAMStatic: 8},
			Symbol: &analyze.Metrics{Name: "toggle_pin", CyclesMin: 8, CyclesMax: 8, InstrCount: 5, PeakStackBytes: 1},
		},
	}
	var buf bytes.Buffer
	renderMatrix(&buf, runs, "firmware.o", 20)
	out := buf.String()
	for _, s := range []string{"Multi-target comparison", "ATtiny3217", "ATmega328P", "symbol toggle_pin"} {
		if !bytes.Contains(buf.Bytes(), []byte(s)) {
			t.Fatalf("output missing %q:\n%s", s, out)
		}
	}
}

func isaTarget(name string, v isa.Variant, pc int) analyze.Target {
	return analyze.Target{Name: name, Variant: v, PCBytes: pc}
}

func TestResolveFormat(t *testing.T) {
	oldFormat, oldJSON := *flFormat, *flJSON
	t.Cleanup(func() {
		*flFormat, *flJSON = oldFormat, oldJSON
	})

	*flFormat, *flJSON = "csv", false
	got, err := resolveFormat()
	if err != nil || got != fmtCSV {
		t.Fatalf("resolveFormat() = %q, %v; want csv, nil", got, err)
	}

	*flFormat, *flJSON = "sarif", false
	got, err = resolveFormat()
	if err != nil || got != fmtSARIF {
		t.Fatalf("resolveFormat() = %q, %v; want sarif, nil", got, err)
	}

	*flFormat, *flJSON = "text", true
	got, err = resolveFormat()
	if err != nil || got != fmtJSON {
		t.Fatalf("json alias = %q, %v; want json, nil", got, err)
	}

	*flFormat, *flJSON = "md", true
	if _, err := resolveFormat(); err == nil {
		t.Fatal("expected -json/-format conflict")
	}
}

func TestRankedEntries(t *testing.T) {
	oldRank, oldRankBy := *flRank, *flRankBy
	t.Cleanup(func() {
		*flRank, *flRankBy = oldRank, oldRankBy
	})
	*flRank, *flRankBy = 2, "cycles"
	res := analyze.Result{
		Symbols: []analyze.Metrics{
			{Name: "slow", CyclesMax: 10},
			{Name: "fast", CyclesMax: 3},
		},
		Regions: []analyze.Metrics{
			{Name: "hot", CyclesMax: 8},
		},
	}
	rs := topRanked(res)
	if len(rs) != 2 {
		t.Fatalf("len=%d, want 2", len(rs))
	}
	if rs[0].Metrics.Name != "slow" || rs[1].Metrics.Name != "hot" {
		t.Fatalf("unexpected order: %s, %s", rs[0].Metrics.Name, rs[1].Metrics.Name)
	}
}

func TestRenderSingleCSV(t *testing.T) {
	run := analysisRun{
		Target: isaTarget("ATtiny3217", isa.VarAVRxt, 2),
		Result: analyze.Result{
			File:       analyze.Metrics{Name: "(whole file)", InstrCount: 3, CyclesMin: 7, CyclesMax: 8, PeakStackBytes: 3, PeakPushBytes: 1, PeakCallBytes: 2},
			SRAMStatic: 32,
			Regions:    []analyze.Metrics{{Name: "inner", InstrCount: 2, CyclesMin: 3, CyclesMax: 4}},
			Symbols:    []analyze.Metrics{{Name: "toggle_pin", InstrCount: 5, CyclesMin: 7, CyclesMax: 9}},
		},
	}
	oldBranches, oldRank, oldRankBy := *flBranches, *flRank, *flRankBy
	t.Cleanup(func() {
		*flBranches, *flRank, *flRankBy = oldBranches, oldRank, oldRankBy
	})
	*flBranches = "bounds"
	*flRank, *flRankBy = 1, "cycles"
	var buf bytes.Buffer
	renderSingleCSV(&buf, run, "firmware.o")
	out := buf.String()
	for _, s := range []string{"file,scope_type,scope_name", "firmware.o,whole_file,(whole file)", "firmware.o,region,inner", "firmware.o,ranked_symbol,toggle_pin"} {
		if !strings.Contains(out, s) {
			t.Fatalf("csv missing %q:\n%s", s, out)
		}
	}
}

func TestRenderMatrixMD(t *testing.T) {
	runs := []analysisRun{
		{Target: isaTarget("ATtiny3217", isa.VarAVRxt, 2), Result: analyze.Result{SRAMStatic: 8}, Symbol: &analyze.Metrics{Name: "toggle_pin", CyclesMin: 7, CyclesMax: 7, InstrCount: 5}},
		{Target: isaTarget("ATmega328P", isa.VarAVRePlus, 2), Result: analyze.Result{SRAMStatic: 8}, Symbol: &analyze.Metrics{Name: "toggle_pin", CyclesMin: 8, CyclesMax: 8, InstrCount: 5}},
	}
	oldBranches := *flBranches
	t.Cleanup(func() { *flBranches = oldBranches })
	*flBranches = "worst"
	var buf bytes.Buffer
	renderMatrixMD(&buf, runs, "firmware.o", 20)
	out := buf.String()
	for _, s := range []string{"# AVR Multi-Target Comparison", "| Target | Core |", "ATtiny3217", "worst"} {
		if !strings.Contains(out, s) {
			t.Fatalf("markdown missing %q:\n%s", s, out)
		}
	}
}

func TestRenderSingleGHA(t *testing.T) {
	run := analysisRun{
		Target: isaTarget("ATtiny3217", isa.VarAVRxt, 2),
		Result: analyze.Result{
			File:       analyze.Metrics{Name: "(whole file)", InstrCount: 3, CyclesMin: 7, CyclesMax: 8},
			SRAMStatic: 32,
			Symbols:    []analyze.Metrics{{Name: "toggle_pin", CyclesMax: 8}},
		},
		Budgets: []budgetCheck{{Name: "cycles", Got: 44, Limit: 40, Scope: "whole file", OK: false}},
	}
	oldBranches, oldRank, oldRankBy := *flBranches, *flRank, *flRankBy
	t.Cleanup(func() {
		*flBranches, *flRank, *flRankBy = oldBranches, oldRank, oldRankBy
	})
	*flBranches = "bounds"
	*flRank, *flRankBy = 1, "cycles"
	var buf bytes.Buffer
	renderSingleGHA(&buf, run, "firmware.o", 20)
	out := buf.String()
	for _, s := range []string{"::group::cyclecount summary", "::error title=cyclecount budget exceeded::cycles 44 exceeds limit 40", "::notice title=cyclecount ranking::symbol toggle_pin ranks at 8 worst-case cycles", "# AVR Cycle Report"} {
		if !strings.Contains(out, s) {
			t.Fatalf("gha missing %q:\n%s", s, out)
		}
	}
}

func TestEmitSingleSARIF(t *testing.T) {
	run := analysisRun{
		Target: isaTarget("ATtiny3217", isa.VarAVRxt, 2),
		Result: analyze.Result{
			File: analyze.Metrics{
				Name:        "(whole file)",
				Unavailable: map[string]int{"CALL": 1},
				Unmodeled:   map[string]int{"SPM": 1},
				Unknown:     map[string]int{"FOO": 2},
			},
		},
		Budgets: []budgetCheck{{Name: "cycles", Got: 44, Limit: 40, Scope: "whole file", OK: false}},
	}
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	oldBranches := *flBranches
	*flBranches = "bounds"
	t.Cleanup(func() {
		os.Stdout = oldStdout
		*flBranches = oldBranches
	})

	emitSingleSARIF(run, "firmware.o")
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	for _, s := range []string{`"version": "2.1.0"`, `"ruleId": "budget-exceeded"`, `"ruleId": "instruction-unavailable"`, `"ruleId": "instruction-unmodeled"`, `"ruleId": "instruction-unknown"`, `"uri": "firmware.o"`} {
		if !strings.Contains(out, s) {
			t.Fatalf("sarif missing %q:\n%s", s, out)
		}
	}
}

func TestEmitJSONIncludesRankingAndSymbols(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	oldBranches, oldRank, oldRankBy := *flBranches, *flRank, *flRankBy
	*flBranches, *flRank, *flRankBy = "bounds", 1, "cycles"
	t.Cleanup(func() {
		os.Stdout = oldStdout
		*flBranches, *flRank, *flRankBy = oldBranches, oldRank, oldRankBy
	})

	res := analyze.Result{
		Target: isaTarget("ATtiny3217", isa.VarAVRxt, 2),
		File:   analyze.Metrics{Name: "(whole file)", CyclesMax: 10},
		Symbols: []analyze.Metrics{
			{Name: "toggle_pin", CyclesMax: 8},
		},
	}
	emitJSON(res, nil, nil, "firmware.o", nil)
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	for _, s := range []string{`"symbols": [`, `"name": "toggle_pin"`, `"ranking": [`, `"metric": "cycles"`} {
		if !strings.Contains(out, s) {
			t.Fatalf("json missing %q:\n%s", s, out)
		}
	}
}
