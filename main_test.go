package main

import (
	"testing"

	"cyclecount/internal/analyze"
)

func TestEvalBudgets(t *testing.T) {
	res := analyze.Result{
		File:       analyze.Metrics{Name: "(whole file)", Iter: 1, CyclesMax: 18, FlashWords: 9},
		SRAMStatic: 32,
	}

	// No limits set: nothing to check.
	if cs := evalBudgets(budgets{}, res, nil); cs != nil {
		t.Fatalf("expected no checks with empty budgets, got %v", cs)
	}

	// Cycles within limit, flash and SRAM over.
	cs := evalBudgets(budgets{cycles: 20, flash: 10, sram: 16}, res, nil)
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

func TestEvalBudgetsCyclesScopeAndIter(t *testing.T) {
	res := analyze.Result{File: analyze.Metrics{Name: "(whole file)", Iter: 1, CyclesMax: 18}}
	// A range with a trip count: the budget gates CyclesMax * iter.
	rng := analyze.Metrics{Name: "inner", Iter: 1000, CyclesMax: 4}

	cs := evalBudgets(budgets{cycles: 3500}, res, &rng)
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
