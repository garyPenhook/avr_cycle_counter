// Package analyze turns parsed assembly into cycle, flash, and SRAM metrics
// for a chosen AVR target (CPU variant + Program Counter width).
package analyze

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"cyclecount/internal/asm"
	"cyclecount/internal/isa"
)

var reObjdumpTarget = regexp.MustCompile(`<([^>]+)>`)
var reNumericLocalRef = regexp.MustCompile(`^([0-9]+)([bf])$`)
var reObjdumpAddress = regexp.MustCompile(`(?:^|\s)(0x[0-9a-fA-F]+)(?:\s|$)`)

type BranchMode int

const (
	BranchBounds BranchMode = iota
	BranchBest
	BranchWorst
	BranchTaken
	BranchNotTaken
)

func (m BranchMode) String() string {
	switch m {
	case BranchBest:
		return "best"
	case BranchWorst:
		return "worst"
	case BranchTaken:
		return "taken"
	case BranchNotTaken:
		return "not-taken"
	default:
		return "bounds"
	}
}

func ParseBranchMode(s string) (BranchMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "bounds", "minmax":
		return BranchBounds, true
	case "best", "min":
		return BranchBest, true
	case "worst", "max":
		return BranchWorst, true
	case "taken":
		return BranchTaken, true
	case "not-taken", "nottaken", "fallthrough":
		return BranchNotTaken, true
	default:
		return BranchBounds, false
	}
}

type BranchPlan struct {
	Decisions map[string]bool
	Trips     map[string]int
	MaxVisits int
}

type Options struct {
	BranchMode  BranchMode
	BranchPlan  BranchPlan
	CallTargets map[string]string
}

// Target is the AVR device being analyzed.
type Target struct {
	Name    string // display name (device part number or core)
	Variant isa.Variant
	PCBytes int // 2 (16-bit PC) or 3 (22-bit PC)
	FlashKB int // program-memory size in KiB; 0 = unknown
	Missing isa.MissingSet
}

// LineMetric pairs a source instruction line with its ISA timing info.
type LineMetric struct {
	Line  *asm.Line
	Info  isa.Info
	Known bool
}

// Metrics is the cost of a span of code (whole file, a region, or a range).
type Metrics struct {
	Name            string
	Iter            int
	InstrCount      int
	FlashWords      int
	FlashDataBytes  int
	CyclesMin       int
	CyclesMax       int
	PeakStackBytes  int
	PeakPushBytes   int
	PeakCallBytes   int
	Pushes          int
	Pops            int
	Calls           int
	UnresolvedCalls int
	RecursiveCalls  int
	StackUnbounded  bool
	CallBytes       int // PC width: stack cost of each call's return address
	Hist            map[string]int
	Unknown         map[string]int // not in the ISA table at all
	Unavailable     map[string]int // exist on other variants, not this target
	Unmodeled       map[string]int // recognized but not cycle-counted (e.g. SPM)
	Lines           []LineMetric
}

// FlashBytes is the total flash footprint of the span (code + inline data).
func (m Metrics) FlashBytes() int { return m.FlashWords*2 + m.FlashDataBytes }

// Result is the full analysis of one file for one target.
type Result struct {
	Target     Target
	File       Metrics
	SRAMStatic int
	Sections   map[string]int
	Regions    []Metrics
	Symbols    []Metrics
	Warnings   []string // non-fatal issues: malformed @begin/@end regions, etc.
}

func isFlashSection(sec string) bool {
	return sec == "" ||
		strings.HasPrefix(sec, ".text") ||
		strings.HasPrefix(sec, ".data") ||
		strings.HasPrefix(sec, ".rodata") ||
		strings.HasPrefix(sec, ".progmem")
}

func isDataSection(sec string) bool {
	return strings.HasPrefix(sec, ".data") ||
		strings.HasPrefix(sec, ".bss") ||
		strings.HasPrefix(sec, ".noinit")
}

func isCondBranch(mn string) bool {
	// All AVR conditional branches are BRxx; BREAK shares the prefix but is not
	// a branch, so exclude it explicitly.
	return strings.HasPrefix(mn, "BR") && mn != "BREAK"
}

func isSkip(mn string) bool {
	switch mn {
	case "CPSE", "SBRC", "SBRS", "SBIC", "SBIS":
		return true
	default:
		return false
	}
}

func nextInstrWordCount(lines []*asm.Line, idx int, t Target) (int, bool) {
	for i := idx + 1; i < len(lines); i++ {
		ln := lines[i]
		if ln.Mnemonic == "" {
			continue
		}
		info, ok := isa.Lookup(ln.Mnemonic)
		if !ok || !isa.AvailableOnTargetForm(ln.Mnemonic, ln.Operands, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
			return 0, false
		}
		return info.WordCount(t.Variant), true
	}
	return 0, false
}

func targetOperand(operands string) string {
	target, _, _ := strings.Cut(strings.TrimSpace(operands), ",")
	return strings.TrimSpace(target)
}

func targetLabel(operands, comment string, valid map[string]int) string {
	if m := reObjdumpTarget.FindStringSubmatch(comment); m != nil {
		if _, ok := valid[m[1]]; ok {
			return m[1]
		}
	}
	target := targetOperand(operands)
	if _, ok := valid[target]; ok {
		return target
	}
	return ""
}

func resolveNumericLocalTarget(lines []*asm.Line, idx int, target string) int {
	m := reNumericLocalRef.FindStringSubmatch(target)
	if m == nil {
		return -1
	}
	label, dir := m[1], m[2]
	if dir == "b" {
		for i := idx - 1; i >= 0; i-- {
			if lines[i].Label == label {
				return i
			}
		}
		return -1
	}
	for i := idx + 1; i < len(lines); i++ {
		if lines[i].Label == label {
			return i
		}
	}
	return -1
}

func targetIndex(lines []*asm.Line, idx int, operands, comment string, labelIndex map[string]int) (int, bool) {
	if idx >= 0 && idx < len(lines) && lines[idx].RelocTarget != "" {
		if pos, ok := labelIndex[lines[idx].RelocTarget]; ok {
			return pos, true
		}
		if pos := resolveRelocSectionOffset(lines, lines[idx].RelocTarget); pos >= 0 {
			return pos, true
		}
	}
	if tgt := targetLabel(operands, comment, labelIndex); tgt != "" {
		return labelIndex[tgt], true
	}
	if pos := resolveNumericLocalTarget(lines, idx, targetOperand(operands)); pos >= 0 {
		return pos, true
	}
	if pos := resolveObjdumpAddressTarget(lines, idx, targetOperand(operands), comment); pos >= 0 {
		return pos, true
	}
	return 0, false
}

func resolveRelocSectionOffset(lines []*asm.Line, target string) int {
	_, off, ok := strings.Cut(target, "+0x")
	if !ok {
		return -1
	}
	address, err := strconv.ParseUint(off, 16, 64)
	if err != nil {
		return -1
	}
	for i, ln := range lines {
		if ln.HasAddress && ln.Address == address {
			return i
		}
	}
	return -1
}

// resolveObjdumpAddressTarget handles stripped disassemblies where branch
// targets are printed only as an absolute comment ("0x90") or as a relative
// operand (".-8" / ".+20") and no symbol label is available.
func resolveObjdumpAddressTarget(lines []*asm.Line, idx int, operand, comment string) int {
	if idx < 0 || idx >= len(lines) || !lines[idx].HasAddress {
		return -1
	}
	var target uint64
	found := false
	if m := reObjdumpAddress.FindStringSubmatch(comment); m != nil {
		if v, err := strconv.ParseUint(m[1], 0, 64); err == nil {
			target, found = v, true
		}
	}
	if !found && len(operand) >= 3 && operand[0] == '.' && (operand[1] == '+' || operand[1] == '-') {
		if delta, err := strconv.ParseInt(operand[1:], 10, 64); err == nil {
			base := int64(lines[idx].Address)
			if base+delta >= 0 {
				target, found = uint64(base+delta), true
			}
		}
	}
	if !found {
		return -1
	}
	for i, ln := range lines {
		if ln.HasAddress && ln.Address == target {
			return i
		}
	}
	return -1
}

type pathState struct {
	counts      map[int]int
	taken       map[int]bool
	cycleTotals map[int]int
	trace       []int
}

func nextInstructionAtOrAfter(lines []*asm.Line, idx int) int {
	for i := idx; i < len(lines); i++ {
		if lines[i].Mnemonic != "" {
			return i
		}
	}
	return len(lines)
}

func skipTargetIndex(lines []*asm.Line, idx int) int {
	next := nextInstructionIndex(lines, idx)
	if next >= len(lines) {
		return len(lines)
	}
	return nextInstructionIndex(lines, next)
}

func modeledCycles(ln *asm.Line, t Target) (isa.CC, bool) {
	info, ok := isa.Lookup(ln.Mnemonic)
	if !ok || !isa.AvailableOnTargetForm(ln.Mnemonic, ln.Operands, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
		return isa.CC{}, false
	}
	return info.Cycles(t.Variant, t.PCBytes)
}

func pathStateFor(lines []*asm.Line, t Target, opts Options) *pathState {
	mode := opts.BranchMode
	plan := opts.BranchPlan
	if mode == BranchBounds && !branchPlanMatches(lines, plan) {
		return nil
	}
	labelIndex := map[string]int{}
	lineLabels := map[int]string{}
	lastLabel := ""
	for i, ln := range lines {
		if ln.Label != "" {
			labelIndex[ln.Label] = i
			lastLabel = ln.Label
		}
		lineLabels[i] = lastLabel
	}
	maxVisits := max(plan.MaxVisits, 1)
	for _, trip := range plan.Trips {
		if trip > maxVisits {
			maxVisits = trip
		}
	}

	decisions := map[int]bool{}
	tripVisits := map[string]int{}
	memo := map[int]int{}
	visiting := map[int]bool{}
	sawCycle := false

	var costFrom func(int) int
	costFrom = func(idx int) int {
		idx = nextInstructionAtOrAfter(lines, idx)
		if idx >= len(lines) {
			return 0
		}
		if v, ok := memo[idx]; ok {
			return v
		}
		if visiting[idx] {
			sawCycle = true
			return 0 // repeated instruction: stop this path here
		}
		visiting[idx] = true
		defer delete(visiting, idx)

		ln := lines[idx]
		cc, ok := modeledCycles(ln, t)
		if !ok {
			v := costFrom(idx + 1)
			memo[idx] = v
			return v
		}

		base := cc.Min
		if cc.Max > base {
			base = cc.Max
		}

		switch {
		case ln.Mnemonic == "RET" || ln.Mnemonic == "RETI":
			base = cc.Max
		case ln.Mnemonic == "RJMP" || ln.Mnemonic == "JMP":
			if next, ok := targetIndex(lines, idx, ln.Operands, ln.Comment, labelIndex); ok {
				base = cc.Max + costFrom(next)
			} else {
				base = cc.Max
			}
		case isCondBranch(ln.Mnemonic):
			fallIdx := nextInstructionIndex(lines, idx)
			target, ok := targetIndex(lines, idx, ln.Operands, ln.Comment, labelIndex)
			if planned, plannedOK := branchPlanDecision(plan, nil, lines, idx, target, ok, lineLabels); plannedOK {
				decisions[idx] = planned
				if planned && ok {
					base = cc.Max + costFrom(target)
				} else {
					base = cc.Min + costFrom(fallIdx)
				}
			} else {
				switch mode {
				case BranchTaken:
					decisions[idx] = true
					if ok {
						base = cc.Max + costFrom(target)
					} else {
						base = cc.Max
					}
				case BranchNotTaken:
					decisions[idx] = false
					base = cc.Min + costFrom(fallIdx)
				case BranchBest, BranchWorst:
					bestFall := cc.Min + costFrom(fallIdx)
					bestTake := -1
					if ok {
						bestTake = cc.Max + costFrom(target)
					}
					take := ok
					if mode == BranchBest {
						take = ok && bestTake < bestFall
						base = bestFall
						if take {
							base = bestTake
						}
					} else {
						take = ok && bestTake >= bestFall
						base = bestFall
						if take {
							base = bestTake
						}
					}
					decisions[idx] = take
				}
			}
		case isSkip(ln.Mnemonic):
			fallIdx := nextInstructionIndex(lines, idx)
			skipTo := skipTargetIndex(lines, idx)
			takenCost := cc.Max
			if nextWords, ok := nextInstrWordCount(lines, idx, t); ok {
				takenCost = cc.Min + nextWords
			}
			if planned, plannedOK := branchPlanDecision(plan, nil, lines, idx, 0, false, lineLabels); plannedOK {
				decisions[idx] = planned
				if planned {
					base = takenCost + costFrom(skipTo)
				} else {
					base = cc.Min + costFrom(fallIdx)
				}
			} else {
				switch mode {
				case BranchTaken:
					decisions[idx] = true
					base = takenCost + costFrom(skipTo)
				case BranchNotTaken:
					decisions[idx] = false
					base = cc.Min + costFrom(fallIdx)
				case BranchBest, BranchWorst:
					noSkip := cc.Min + costFrom(fallIdx)
					doSkip := takenCost + costFrom(skipTo)
					take := false
					if mode == BranchBest {
						take = doSkip < noSkip
						base = noSkip
						if take {
							base = doSkip
						}
					} else {
						take = doSkip >= noSkip
						base = noSkip
						if take {
							base = doSkip
						}
					}
					decisions[idx] = take
				}
			}
		default:
			base = cc.Max + costFrom(idx+1)
		}

		memo[idx] = base
		return base
	}

	counts := map[int]int{}
	cycleTotals := map[int]int{}
	var trace []int
	done := false
	for idx := nextInstructionAtOrAfter(lines, 0); idx < len(lines); {
		if counts[idx] >= maxVisits {
			break
		}
		counts[idx]++
		trace = append(trace, idx)
		ln := lines[idx]
		cc, modeled := modeledCycles(ln, t)
		if modeled {
			cycleTotals[idx] += cc.Max
		}
		switch {
		case ln.Mnemonic == "RET" || ln.Mnemonic == "RETI":
			done = true
		case ln.Mnemonic == "RJMP" || ln.Mnemonic == "JMP":
			if next, ok := targetIndex(lines, idx, ln.Operands, ln.Comment, labelIndex); ok {
				idx = nextInstructionAtOrAfter(lines, next)
				continue
			}
			done = true
		case ln.Mnemonic == "IJMP" || ln.Mnemonic == "EIJMP":
			// Indirect jump through Z (or EIND:Z): the target is data-dependent
			// and cannot be resolved statically, so the path walk stops here.
			// Marking instructions after an indirect jump as executed would count
			// code that is, in general, unreachable from this point.
			done = true
		case isCondBranch(ln.Mnemonic):
			fallIdx := nextInstructionIndex(lines, idx)
			target, resolved := targetIndex(lines, idx, ln.Operands, ln.Comment, labelIndex)
			take := false
			if planned, plannedOK := branchPlanDecision(plan, tripVisits, lines, idx, target, resolved, lineLabels); plannedOK {
				take = planned
			} else {
				switch mode {
				case BranchTaken:
					take = true
				case BranchNotTaken:
					take = false
				default:
					_ = costFrom(idx)
					take = decisions[idx]
				}
			}
			decisions[idx] = take
			if modeled {
				cycleTotals[idx] -= cc.Max
				if take {
					cycleTotals[idx] += cc.Max
				} else {
					cycleTotals[idx] += cc.Min
				}
			}
			if take {
				if resolved {
					idx = nextInstructionAtOrAfter(lines, target)
					continue
				}
				done = true
			} else {
				idx = nextInstructionAtOrAfter(lines, fallIdx)
				continue
			}
		case isSkip(ln.Mnemonic):
			take := false
			if planned, plannedOK := branchPlanDecision(plan, tripVisits, lines, idx, 0, false, lineLabels); plannedOK {
				take = planned
			} else {
				switch mode {
				case BranchTaken:
					take = true
				case BranchNotTaken:
					take = false
				default:
					_ = costFrom(idx)
					take = decisions[idx]
				}
			}
			decisions[idx] = take
			if modeled {
				cycleTotals[idx] -= cc.Max
				if take {
					taken := cc.Max
					if nextWords, ok := nextInstrWordCount(lines, idx, t); ok {
						taken = cc.Min + nextWords
					}
					cycleTotals[idx] += taken
				} else {
					cycleTotals[idx] += cc.Min
				}
			}
			if take {
				idx = nextInstructionAtOrAfter(lines, skipTargetIndex(lines, idx))
				continue
			}
		}
		if done {
			break
		}
		idx = nextInstructionAtOrAfter(lines, idx+1)
	}
	if sawCycle && (mode == BranchBest || mode == BranchWorst) {
		return nil
	}
	return &pathState{counts: counts, taken: decisions, cycleTotals: cycleTotals, trace: trace}
}

// ValidateOptions checks user-addressable plan keys against the branches in
// this input. A misspelled key must not silently turn Bounds mode into an exact
// all-fallthrough path.
func ValidateOptions(lines []*asm.Line, opts Options) error {
	valid := validBranchPlanKeys(lines)
	for key := range opts.BranchPlan.Decisions {
		if !valid[key] {
			return fmt.Errorf("branch-scenario key %q does not match any branch or skip", key)
		}
	}
	for key := range opts.BranchPlan.Trips {
		if !valid[key] {
			return fmt.Errorf("branch-scenario key %q does not match any branch or skip", key)
		}
	}
	return nil
}

func branchPlanMatches(lines []*asm.Line, plan BranchPlan) bool {
	valid := validBranchPlanKeys(lines)
	for key := range plan.Decisions {
		if valid[key] {
			return true
		}
	}
	for key := range plan.Trips {
		if valid[key] {
			return true
		}
	}
	return false
}

func validBranchPlanKeys(lines []*asm.Line) map[string]bool {
	labelIndex := map[string]int{}
	lineLabels := map[int]string{}
	lastLabel := ""
	for i, ln := range lines {
		if ln.Label != "" {
			labelIndex[ln.Label] = i
			lastLabel = ln.Label
		}
		lineLabels[i] = lastLabel
	}
	valid := map[string]bool{}
	for idx, ln := range lines {
		if !isCondBranch(ln.Mnemonic) && !isSkip(ln.Mnemonic) {
			continue
		}
		target, ok := targetIndex(lines, idx, ln.Operands, ln.Comment, labelIndex)
		for _, key := range branchDecisionKeys(lines, idx, target, ok, lineLabels) {
			valid[key] = true
		}
	}
	return valid
}

func branchPlanDecision(plan BranchPlan, tripVisits map[string]int, lines []*asm.Line, idx, target int, targetOK bool, lineLabels map[int]string) (bool, bool) {
	for _, key := range branchDecisionKeys(lines, idx, target, targetOK, lineLabels) {
		if trip, ok := plan.Trips[key]; ok && trip > 0 {
			if tripVisits == nil {
				return true, true
			}
			visit := tripVisits[key]
			tripVisits[key] = visit + 1
			return visit < trip-1, true
		}
		if v, ok := plan.Decisions[key]; ok {
			return v, true
		}
	}
	return false, false
}

func branchDecisionKeys(lines []*asm.Line, idx, target int, targetOK bool, lineLabels map[int]string) []string {
	keys := []string{
		fmt.Sprintf("line:%d", lines[idx].Num),
		fmt.Sprintf("%d", lines[idx].Num),
	}
	if lbl := lineLabels[idx]; lbl != "" {
		keys = append(keys, "label:"+lbl, lbl)
	}
	if targetOK && target >= 0 && target < len(lines) {
		if lbl := lines[target].Label; lbl != "" {
			keys = append(keys, "target:"+lbl)
			keys = append(keys, lbl)
		}
	}
	return keys
}

func lineLabelAt(lines []*asm.Line, idx int) string {
	for i := idx; i >= 0; i-- {
		if lines[i].Label != "" {
			return lines[i].Label
		}
	}
	return ""
}

func callSiteKeys(lines []*asm.Line, idx int) []string {
	keys := []string{
		fmt.Sprintf("line:%d", lines[idx].Num),
		fmt.Sprintf("%d", lines[idx].Num),
	}
	if lbl := lineLabelAt(lines, idx); lbl != "" {
		keys = append(keys, "label:"+lbl, lbl)
	}
	return keys
}

func plannedCallTarget(opts Options, lines []*asm.Line, idx int) string {
	for _, key := range callSiteKeys(lines, idx) {
		if target := strings.TrimSpace(opts.CallTargets[key]); target != "" {
			return target
		}
	}
	return ""
}

func symbolSpans(lines []*asm.Line) map[string][]*asm.Line {
	out := map[string][]*asm.Line{}
	for i, ln := range lines {
		if ln.Label == "" || isLocalLabel(ln.Label) {
			continue
		}
		end := nextSymbolBoundary(lines, i)
		out[ln.Label] = lines[i:end]
	}
	return out
}

func callTarget(ln *asm.Line, symbols map[string][]*asm.Line) string {
	valid := map[string]int{}
	for k := range symbols {
		valid[k] = 1
	}
	return targetLabel(ln.Operands, ln.Comment, valid)
}

func localCallTarget(lines []*asm.Line, idx int) (string, []*asm.Line, bool) {
	labelIndex := map[string]int{}
	for i, ln := range lines {
		if ln.Label != "" {
			labelIndex[ln.Label] = i
		}
	}
	pos, ok := targetIndex(lines, idx, lines[idx].Operands, lines[idx].Comment, labelIndex)
	if !ok {
		return "", nil, false
	}
	name := lines[pos].Label
	if name == "" {
		name = fmt.Sprintf("@local:%d", pos)
	}
	end := nextSymbolBoundary(lines, pos)
	for i := pos; i < end; i++ {
		if lines[i].Mnemonic == "RET" || lines[i].Mnemonic == "RETI" {
			end = i + 1
			break
		}
	}
	if end <= pos {
		return "", nil, false
	}
	return "@local:" + name, lines[pos:end], true
}

type stackPeak struct {
	push       int
	call       int
	total      int
	unresolved int
	recursive  int
}

// stackPeaks measures the deepest stack use of a span. trace, when non-nil,
// gives the selected path in execution order and may contain repeated indices,
// so stack growth across bounded loops is modeled. Callees execute in full and
// therefore pass nil. A path-specific frame bypasses the shared cache.
func stackPeaks(name string, lines []*asm.Line, t Target, trace []int, symbols map[string][]*asm.Line, opts Options, cache map[string]stackPeak, visiting map[string]bool) stackPeak {
	if name != "" {
		if sp, ok := cache[name]; ok && trace == nil {
			return sp
		}
		if visiting[name] {
			return stackPeak{recursive: 1}
		}
		visiting[name] = true
		defer delete(visiting, name)
	}

	runningPush, peakPush, peakTotal, unresolvedCalls, recursiveCalls := 0, 0, 0, 0, 0
	order := trace
	if order == nil {
		order = make([]int, len(lines))
		for i := range lines {
			order[i] = i
		}
	}
	for _, idx := range order {
		if idx < 0 || idx >= len(lines) {
			continue
		}
		ln := lines[idx]
		if ln.Mnemonic == "" {
			continue
		}
		info, ok := isa.Lookup(ln.Mnemonic)
		if !ok || !isa.AvailableOnTargetForm(ln.Mnemonic, ln.Operands, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
			continue
		}
		switch info.Mnemonic {
		case "PUSH":
			runningPush++
			if runningPush > peakPush {
				peakPush = runningPush
			}
			if runningPush > peakTotal {
				peakTotal = runningPush
			}
		case "POP":
			runningPush--
			if runningPush < 0 {
				runningPush = 0
			}
		case "CALL", "RCALL", "ICALL", "EICALL":
			total := runningPush + t.PCBytes
			if tgt := callTarget(ln, symbols); tgt != "" {
				if callee, ok := symbols[tgt]; ok {
					csp := stackPeaks(tgt, callee, t, nil, symbols, opts, cache, visiting)
					total += csp.total
					unresolvedCalls += csp.unresolved
					recursiveCalls += csp.recursive
					if total > peakTotal {
						peakTotal = total
					}
					continue
				}
			}
			if tgt := plannedCallTarget(opts, lines, idx); tgt != "" {
				if callee, ok := symbols[tgt]; ok {
					csp := stackPeaks(tgt, callee, t, nil, symbols, opts, cache, visiting)
					total += csp.total
					unresolvedCalls += csp.unresolved
					recursiveCalls += csp.recursive
					if total > peakTotal {
						peakTotal = total
					}
					continue
				}
			}
			if tgt, callee, ok := localCallTarget(lines, idx); ok {
				csp := stackPeaks(tgt, callee, t, nil, symbols, opts, cache, visiting)
				total += csp.total
				unresolvedCalls += csp.unresolved
				recursiveCalls += csp.recursive
				if total > peakTotal {
					peakTotal = total
				}
				continue
			}
			// Keep the return-address bytes in the total, but remember that the
			// callee-local stack depth is unknown for this call edge.
			if total > peakTotal {
				peakTotal = total
			}
			unresolvedCalls++
			continue
		}
	}
	sp := stackPeak{push: peakPush, call: peakTotal - peakPush, total: peakTotal, unresolved: unresolvedCalls, recursive: recursiveCalls}
	if sp.call < 0 {
		sp.call = 0
	}
	if name != "" {
		cache[name] = sp
	}
	return sp
}

func nextInstructionIndex(lines []*asm.Line, idx int) int {
	for i := idx + 1; i < len(lines); i++ {
		if lines[i].Mnemonic != "" {
			return i
		}
	}
	return len(lines)
}

func branchCycles(info isa.Info, cc isa.CC, lines []*asm.Line, idx int, t Target, mode BranchMode, path *pathState) isa.CC {
	mn := info.Mnemonic
	switch {
	case isCondBranch(mn):
		if path != nil {
			if path.taken[idx] {
				return isa.CC{Min: cc.Max, Max: cc.Max}
			}
			return isa.CC{Min: cc.Min, Max: cc.Min}
		}
		switch mode {
		case BranchBest, BranchNotTaken:
			return isa.CC{Min: cc.Min, Max: cc.Min}
		case BranchWorst, BranchTaken:
			return isa.CC{Min: cc.Max, Max: cc.Max}
		default:
			return cc
		}
	case isSkip(mn):
		if path != nil {
			if !path.taken[idx] {
				return isa.CC{Min: cc.Min, Max: cc.Min}
			}
			if nextWords, ok := nextInstrWordCount(lines, idx, t); ok {
				taken := cc.Min + nextWords
				return isa.CC{Min: taken, Max: taken}
			}
			return isa.CC{Min: cc.Max, Max: cc.Max}
		}
		switch mode {
		case BranchBounds:
			// The ISA table's cc.Max assumes the worst case of skipping a 2-word
			// instruction. Tighten it to the cost of skipping the actual next
			// instruction (1 cycle + its word count) so CyclesMax is a reachable
			// bound. The skipped instruction is still counted toward the totals
			// on its own line; without this refinement CyclesMax can exceed any
			// path the code can actually take (e.g. reporting 8 when the true
			// worst case is 6), which can falsely fail a -max-cycles budget.
			if nextWords, ok := nextInstrWordCount(lines, idx, t); ok {
				return isa.CC{Min: cc.Min, Max: cc.Min + nextWords}
			}
			return cc
		case BranchBest, BranchNotTaken:
			return isa.CC{Min: cc.Min, Max: cc.Min}
		case BranchWorst:
			return isa.CC{Min: cc.Max, Max: cc.Max}
		case BranchTaken:
			nextWords, ok := nextInstrWordCount(lines, idx, t)
			if !ok {
				return isa.CC{Min: cc.Max, Max: cc.Max}
			}
			taken := cc.Min + nextWords
			return isa.CC{Min: taken, Max: taken}
		default:
			return cc
		}
	default:
		return cc
	}
}

// linearCycleBounds preserves the listing analyzer's historical treatment of
// ordinary branches while correlating skip instructions with the instruction
// they conditionally suppress. Summing both independently creates unreachable
// maxima and misses the taken path's lower bound.
func linearCycleBounds(lines []*asm.Line, t Target) (int, int) {
	var instrs []int
	for i, ln := range lines {
		if ln.Mnemonic != "" {
			instrs = append(instrs, i)
		}
	}
	type bounds struct{ min, max int }
	memo := map[int]bounds{}
	var from func(int) bounds
	from = func(pos int) bounds {
		if pos >= len(instrs) {
			return bounds{}
		}
		if b, ok := memo[pos]; ok {
			return b
		}
		idx := instrs[pos]
		ln := lines[idx]
		info, known := isa.Lookup(ln.Mnemonic)
		if !known || !isa.AvailableOnTargetForm(ln.Mnemonic, ln.Operands, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
			b := from(pos + 1)
			memo[pos] = b
			return b
		}
		cc, modeled := info.Cycles(t.Variant, t.PCBytes)
		if !modeled {
			b := from(pos + 1)
			memo[pos] = b
			return b
		}
		if isSkip(info.Mnemonic) {
			fall := from(pos + 1)
			noSkip := bounds{min: cc.Min + fall.min, max: cc.Min + fall.max}
			takenCost := cc.Max
			if words, ok := nextInstrWordCount(lines, idx, t); ok {
				takenCost = cc.Min + words
			}
			after := from(pos + 2)
			doSkip := bounds{min: takenCost + after.min, max: takenCost + after.max}
			b := bounds{min: min(noSkip.min, doSkip.min), max: max(noSkip.max, doSkip.max)}
			memo[pos] = b
			return b
		}
		cc = branchCycles(info, cc, lines, idx, t, BranchBounds, nil)
		rest := from(pos + 1)
		b := bounds{min: cc.Min + rest.min, max: cc.Max + rest.max}
		memo[pos] = b
		return b
	}
	b := from(0)
	return b.min, b.max
}

func computeMetrics(name string, iter int, lines []*asm.Line, t Target, opts Options, symbols map[string][]*asm.Line, symbolScope bool) Metrics {
	m := Metrics{Name: name, Iter: iter, CallBytes: t.PCBytes,
		Hist: map[string]int{}, Unknown: map[string]int{},
		Unavailable: map[string]int{}, Unmodeled: map[string]int{}}
	path := pathStateFor(lines, t, opts)
	for idx, ln := range lines {
		if ln.Directive != "" && !asm.AllocatesBSS(ln.Directive) {
			// .comm/.lcomm reserve SRAM, not flash, so they never count here even
			// when they appear in a flash section.
			if b, ok := asm.DataBytes(ln.Directive, ln.DirectiveArgs); ok && isFlashSection(ln.Section) {
				m.FlashDataBytes += b
			}
		}
		if ln.Mnemonic == "" {
			continue
		}
		info, ok := isa.Lookup(ln.Mnemonic)
		m.Lines = append(m.Lines, LineMetric{Line: ln, Info: info, Known: ok})
		if !ok {
			m.Unknown[ln.Mnemonic]++
			continue
		}
		if !isa.AvailableOnTargetForm(ln.Mnemonic, ln.Operands, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
			m.Unavailable[info.Mnemonic]++
			continue
		}
		// Flash is a static property of the span. Branch/path selection affects
		// executed instruction and cycle counts, not the assembled bytes.
		m.FlashWords += info.WordCount(t.Variant)
		visits := 1
		if path != nil {
			visits = path.counts[idx]
		}
		if visits <= 0 {
			continue
		}
		// Available and present on the selected path: count it as executed.
		m.InstrCount++
		m.Hist[info.Mnemonic]++

		if cc, ok := info.Cycles(t.Variant, t.PCBytes); ok {
			if path != nil {
				cycles := path.cycleTotals[idx]
				m.CyclesMin += cycles
				m.CyclesMax += cycles
			} else {
				cc = branchCycles(info, cc, lines, idx, t, opts.BranchMode, nil)
				m.CyclesMin += cc.Min
				m.CyclesMax += cc.Max
			}
		} else {
			m.Unmodeled[info.Mnemonic]++ // e.g. SPM: programming-time dependent
		}
		switch info.Mnemonic {
		case "PUSH":
			m.Pushes++
		case "POP":
			m.Pops++
		case "CALL", "RCALL", "ICALL", "EICALL":
			m.Calls++
		}
	}
	if path == nil && opts.BranchMode == BranchBounds {
		m.CyclesMin, m.CyclesMax = linearCycleBounds(lines, t)
	}
	seed := ""
	if _, ok := symbols[name]; ok {
		if symbolScope {
			// Analyzing the symbol itself: key the top frame by its bare name so
			// genuine (mutual-)recursion is detected and cut via `visiting`.
			seed = name
		} else {
			// A region/range/file scope that merely shares a name with a callee
			// symbol must NOT collide with that callee: namespace the key so the
			// top frame cannot falsely cut the callee's stack analysis.
			seed = "@scope:" + name
		}
	}
	var trace []int
	if path != nil {
		trace = path.trace
	}
	sp := stackPeaks(seed, lines, t, trace, symbols, opts, map[string]stackPeak{}, map[string]bool{})
	m.PeakPushBytes = sp.push
	m.PeakCallBytes = sp.call
	m.PeakStackBytes = sp.total
	m.UnresolvedCalls = sp.unresolved
	m.RecursiveCalls = sp.recursive
	m.StackUnbounded = sp.recursive > 0
	return m
}

// Analyze produces whole-file metrics, static SRAM totals, and per-region
// metrics for every @begin/@end span, all for the given target.
func Analyze(lines []*asm.Line, t Target) Result {
	return AnalyzeMode(lines, t, BranchBounds)
}

// AnalyzeMode produces metrics using the requested conditional-branch policy.
func AnalyzeMode(lines []*asm.Line, t Target, mode BranchMode) Result {
	return AnalyzeOptions(lines, t, Options{BranchMode: mode})
}

// AnalyzeOptions produces metrics using explicit branch/path options.
func AnalyzeOptions(lines []*asm.Line, t Target, opts Options) Result {
	symbols := symbolSpans(lines)
	res := Result{Target: t, Sections: map[string]int{}}
	res.File = computeMetrics("(whole file)", 1, lines, t, opts, symbols, false)
	for _, ln := range lines {
		if ln.Directive == "" {
			continue
		}
		b, ok := asm.DataBytes(ln.Directive, ln.DirectiveArgs)
		if !ok {
			continue
		}
		sec := ln.Section
		if asm.AllocatesBSS(ln.Directive) {
			sec = ".bss" // .comm/.lcomm reserve BSS storage regardless of the current section
		} else if !isDataSection(sec) {
			continue
		}
		res.SRAMStatic += b
		res.Sections[sec] += b
	}
	res.Regions, res.Warnings = extractRegions(lines, t, opts, symbols)
	res.Symbols = extractSymbols(lines, t, opts, symbols)
	return res
}

type openRegion struct {
	name  string
	iter  int
	start int
}

func extractRegions(lines []*asm.Line, t Target, opts Options, symbols map[string][]*asm.Line) ([]Metrics, []string) {
	var stack []openRegion
	var out []Metrics
	var warns []string
	for idx, ln := range lines {
		for _, b := range ln.RegionBegins {
			stack = append(stack, openRegion{b.Name, b.Iter, idx})
		}
		for _, name := range ln.RegionEnds {
			matched := false
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].name != name {
					continue
				}
				or := stack[j]
				stack = append(stack[:j], stack[j+1:]...)
				// The region body is the instructions strictly between the
				// @begin and @end anchor lines. Clamp the start so an @begin
				// and @end that share one line (or sit on adjacent lines) yields
				// an empty span rather than panicking on a backwards slice
				// (lines[start+1 : idx] with start+1 > idx).
				lo := or.start + 1
				if lo > idx {
					lo = idx
				}
				sub := lines[lo:idx]
				out = append(out, computeMetrics(or.name, or.iter, sub, t, opts, symbols, false))
				matched = true
				break
			}
			if !matched {
				warns = append(warns, fmt.Sprintf("@end %s has no matching @begin; ignored", name))
			}
		}
	}
	for _, or := range stack {
		warns = append(warns, fmt.Sprintf("@begin %s (line %d) has no matching @end; region dropped", or.name, lines[or.start].Num))
	}
	return out, warns
}

func findLabel(lines []*asm.Line, label string) int {
	for i, ln := range lines {
		if ln.Label == label {
			return i
		}
	}
	return -1
}

func isLocalLabel(label string) bool {
	if strings.HasPrefix(label, ".") {
		return true
	}
	for _, r := range label {
		if r < '0' || r > '9' {
			return false
		}
	}
	return label != ""
}

func nextSymbolBoundary(lines []*asm.Line, start int) int {
	for i := start + 1; i < len(lines); i++ {
		if lines[i].Label != "" && !isLocalLabel(lines[i].Label) {
			return i
		}
	}
	return len(lines)
}

func extractSymbols(lines []*asm.Line, t Target, opts Options, symbols map[string][]*asm.Line) []Metrics {
	var out []Metrics
	for i, ln := range lines {
		if ln.Label == "" || isLocalLabel(ln.Label) {
			continue
		}
		end := nextSymbolBoundary(lines, i)
		out = append(out, computeMetrics(ln.Label, 1, lines[i:end], t, opts, symbols, true))
	}
	return out
}

// RangeMetrics analyzes the inclusive span between two labels.
func RangeMetrics(lines []*asm.Line, from, to string, iter int, t Target) (Metrics, error) {
	return RangeMetricsMode(lines, from, to, iter, t, BranchBounds)
}

func RangeMetricsMode(lines []*asm.Line, from, to string, iter int, t Target, mode BranchMode) (Metrics, error) {
	return RangeMetricsOptions(lines, from, to, iter, t, Options{BranchMode: mode})
}

func RangeMetricsOptions(lines []*asm.Line, from, to string, iter int, t Target, opts Options) (Metrics, error) {
	symbols := symbolSpans(lines)
	fi, ti := findLabel(lines, from), findLabel(lines, to)
	if fi < 0 {
		return Metrics{}, fmt.Errorf("start label %q not found", from)
	}
	if ti < 0 {
		return Metrics{}, fmt.Errorf("end label %q not found", to)
	}
	if ti < fi {
		fi, ti = ti, fi
	}
	return computeMetrics(from+":"+to, iter, lines[fi:ti+1], t, opts, symbols, false), nil
}

// SymbolMetrics analyzes the span that starts at label and runs until the next
// non-local symbol boundary (or EOF). This matches objdump function symbols
// and source-level top-level labels while ignoring interior .L... labels.
func SymbolMetrics(lines []*asm.Line, label string, iter int, t Target) (Metrics, error) {
	return SymbolMetricsMode(lines, label, iter, t, BranchBounds)
}

func SymbolMetricsMode(lines []*asm.Line, label string, iter int, t Target, mode BranchMode) (Metrics, error) {
	return SymbolMetricsOptions(lines, label, iter, t, Options{BranchMode: mode})
}

func SymbolMetricsOptions(lines []*asm.Line, label string, iter int, t Target, opts Options) (Metrics, error) {
	symbols := symbolSpans(lines)
	start := findLabel(lines, label)
	if start < 0 {
		return Metrics{}, fmt.Errorf("symbol %q not found", label)
	}
	end := nextSymbolBoundary(lines, start)
	return computeMetrics(label, iter, lines[start:end], t, opts, symbols, true), nil
}
