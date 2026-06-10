// Package analyze turns parsed assembly into cycle, flash, and SRAM metrics
// for a chosen AVR target (CPU variant + Program Counter width).
package analyze

import (
	"fmt"
	"regexp"
	"strings"

	"cyclecount/internal/asm"
	"cyclecount/internal/isa"
)

var reObjdumpTarget = regexp.MustCompile(`<([^>]+)>`)
var reNumericLocalRef = regexp.MustCompile(`^([0-9]+)([bf])$`)

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
}

func isFlashSection(sec string) bool {
	return sec == "" ||
		strings.HasPrefix(sec, ".text") ||
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
		if !ok || !isa.AvailableOnTarget(ln.Mnemonic, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
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
	if tgt := targetLabel(operands, comment, labelIndex); tgt != "" {
		return labelIndex[tgt], true
	}
	if pos := resolveNumericLocalTarget(lines, idx, targetOperand(operands)); pos >= 0 {
		return pos, true
	}
	return 0, false
}

type pathState struct {
	executed map[int]bool
	taken    map[int]bool
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
	if !ok || !isa.AvailableOnTarget(ln.Mnemonic, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
		return isa.CC{}, false
	}
	return info.Cycles(t.Variant, t.PCBytes)
}

func pathStateFor(lines []*asm.Line, t Target, mode BranchMode) *pathState {
	if mode == BranchBounds {
		return nil
	}
	labelIndex := map[string]int{}
	for i, ln := range lines {
		if ln.Label != "" {
			labelIndex[ln.Label] = i
		}
	}

	decisions := map[int]bool{}
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
		case isSkip(ln.Mnemonic):
			fallIdx := nextInstructionIndex(lines, idx)
			skipTo := skipTargetIndex(lines, idx)
			takenCost := cc.Max
			if nextWords, ok := nextInstrWordCount(lines, idx, t); ok {
				takenCost = cc.Min + nextWords
			}
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
		default:
			base = cc.Max + costFrom(idx+1)
		}

		memo[idx] = base
		return base
	}

	executed := map[int]bool{}
	done := false
	for idx := nextInstructionAtOrAfter(lines, 0); idx < len(lines); {
		if executed[idx] {
			break
		}
		executed[idx] = true
		ln := lines[idx]
		switch {
		case ln.Mnemonic == "RET" || ln.Mnemonic == "RETI":
			done = true
		case ln.Mnemonic == "RJMP" || ln.Mnemonic == "JMP":
			if next, ok := targetIndex(lines, idx, ln.Operands, ln.Comment, labelIndex); ok {
				idx = nextInstructionAtOrAfter(lines, next)
				continue
			}
			done = true
		case isCondBranch(ln.Mnemonic):
			take := false
			switch mode {
			case BranchTaken:
				take = true
			case BranchNotTaken:
				take = false
			default:
				_ = costFrom(idx)
				take = decisions[idx]
			}
			decisions[idx] = take
			if take {
				if next, ok := targetIndex(lines, idx, ln.Operands, ln.Comment, labelIndex); ok {
					idx = nextInstructionAtOrAfter(lines, next)
					continue
				}
				done = true
			}
		case isSkip(ln.Mnemonic):
			take := false
			switch mode {
			case BranchTaken:
				take = true
			case BranchNotTaken:
				take = false
			default:
				_ = costFrom(idx)
				take = decisions[idx]
			}
			decisions[idx] = take
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
	return &pathState{executed: executed, taken: decisions}
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
}

// stackPeaks measures the deepest stack use of a span. executed, when non-nil,
// restricts the walk to lines on the selected branch path (taken/not-taken
// pruning) and applies only to this top frame — callees execute in full, so
// recursive calls pass nil. A pruned frame is path-specific, so it bypasses the
// shared cache to avoid contaminating an unpruned caller's result.
func stackPeaks(name string, lines []*asm.Line, t Target, executed map[int]bool, symbols map[string][]*asm.Line, cache map[string]stackPeak, visiting map[string]bool) stackPeak {
	if name != "" {
		if sp, ok := cache[name]; ok && executed == nil {
			return sp
		}
		if visiting[name] {
			return stackPeak{}
		}
		visiting[name] = true
		defer delete(visiting, name)
	}

	runningPush, peakPush, peakTotal, unresolvedCalls := 0, 0, 0, 0
	for idx, ln := range lines {
		if ln.Mnemonic == "" {
			continue
		}
		if executed != nil && !executed[idx] {
			continue
		}
		info, ok := isa.Lookup(ln.Mnemonic)
		if !ok || !isa.AvailableOnTarget(ln.Mnemonic, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
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
					csp := stackPeaks(tgt, callee, t, nil, symbols, cache, visiting)
					total += csp.total
					unresolvedCalls += csp.unresolved
					if total > peakTotal {
						peakTotal = total
					}
					continue
				}
			}
			if tgt, callee, ok := localCallTarget(lines, idx); ok {
				csp := stackPeaks(tgt, callee, t, nil, symbols, cache, visiting)
				total += csp.total
				unresolvedCalls += csp.unresolved
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
	sp := stackPeak{push: peakPush, call: peakTotal - peakPush, total: peakTotal, unresolved: unresolvedCalls}
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

func computeMetrics(name string, iter int, lines []*asm.Line, t Target, mode BranchMode, symbols map[string][]*asm.Line) Metrics {
	m := Metrics{Name: name, Iter: iter, CallBytes: t.PCBytes,
		Hist: map[string]int{}, Unknown: map[string]int{},
		Unavailable: map[string]int{}, Unmodeled: map[string]int{}}
	path := pathStateFor(lines, t, mode)
	var executed map[int]bool
	if path != nil {
		executed = path.executed
	}
	for idx, ln := range lines {
		if ln.Directive != "" {
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
		if !isa.AvailableOnTarget(ln.Mnemonic, t.Variant, t.PCBytes, t.FlashKB, t.Missing) {
			m.Unavailable[info.Mnemonic]++
			continue
		}
		if executed != nil && !executed[idx] {
			continue
		}
		// Available on this target: count it toward instructions and flash.
		m.InstrCount++
		m.FlashWords += info.WordCount(t.Variant)
		m.Hist[info.Mnemonic]++

		if cc, ok := info.Cycles(t.Variant, t.PCBytes); ok {
			cc = branchCycles(info, cc, lines, idx, t, mode, path)
			m.CyclesMin += cc.Min
			m.CyclesMax += cc.Max
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
	seed := ""
	if _, ok := symbols[name]; ok {
		seed = name
	}
	sp := stackPeaks(seed, lines, t, executed, symbols, map[string]stackPeak{}, map[string]bool{})
	m.PeakPushBytes = sp.push
	m.PeakCallBytes = sp.call
	m.PeakStackBytes = sp.total
	m.UnresolvedCalls = sp.unresolved
	return m
}

// Analyze produces whole-file metrics, static SRAM totals, and per-region
// metrics for every @begin/@end span, all for the given target.
func Analyze(lines []*asm.Line, t Target) Result {
	return AnalyzeMode(lines, t, BranchBounds)
}

// AnalyzeMode produces metrics using the requested conditional-branch policy.
func AnalyzeMode(lines []*asm.Line, t Target, mode BranchMode) Result {
	symbols := symbolSpans(lines)
	res := Result{Target: t, Sections: map[string]int{}}
	res.File = computeMetrics("(whole file)", 1, lines, t, mode, symbols)
	for _, ln := range lines {
		if ln.Directive == "" {
			continue
		}
		if b, ok := asm.DataBytes(ln.Directive, ln.DirectiveArgs); ok && isDataSection(ln.Section) {
			res.SRAMStatic += b
			res.Sections[ln.Section] += b
		}
	}
	res.Regions = extractRegions(lines, t, mode, symbols)
	res.Symbols = extractSymbols(lines, t, mode, symbols)
	return res
}

type openRegion struct {
	name  string
	iter  int
	start int
}

func extractRegions(lines []*asm.Line, t Target, mode BranchMode, symbols map[string][]*asm.Line) []Metrics {
	var stack []openRegion
	var out []Metrics
	for idx, ln := range lines {
		for _, b := range ln.RegionBegins {
			stack = append(stack, openRegion{b.Name, b.Iter, idx})
		}
		for _, name := range ln.RegionEnds {
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j].name != name {
					continue
				}
				or := stack[j]
				stack = append(stack[:j], stack[j+1:]...)
				sub := lines[or.start+1 : idx]
				out = append(out, computeMetrics(or.name, or.iter, sub, t, mode, symbols))
				break
			}
		}
	}
	return out
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

func extractSymbols(lines []*asm.Line, t Target, mode BranchMode, symbols map[string][]*asm.Line) []Metrics {
	var out []Metrics
	for i, ln := range lines {
		if ln.Label == "" || isLocalLabel(ln.Label) {
			continue
		}
		end := nextSymbolBoundary(lines, i)
		out = append(out, computeMetrics(ln.Label, 1, lines[i:end], t, mode, symbols))
	}
	return out
}

// RangeMetrics analyzes the inclusive span between two labels.
func RangeMetrics(lines []*asm.Line, from, to string, iter int, t Target) (Metrics, error) {
	return RangeMetricsMode(lines, from, to, iter, t, BranchBounds)
}

func RangeMetricsMode(lines []*asm.Line, from, to string, iter int, t Target, mode BranchMode) (Metrics, error) {
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
	return computeMetrics(from+":"+to, iter, lines[fi:ti+1], t, mode, symbols), nil
}

// SymbolMetrics analyzes the span that starts at label and runs until the next
// non-local symbol boundary (or EOF). This matches objdump function symbols
// and source-level top-level labels while ignoring interior .L... labels.
func SymbolMetrics(lines []*asm.Line, label string, iter int, t Target) (Metrics, error) {
	return SymbolMetricsMode(lines, label, iter, t, BranchBounds)
}

func SymbolMetricsMode(lines []*asm.Line, label string, iter int, t Target, mode BranchMode) (Metrics, error) {
	symbols := symbolSpans(lines)
	start := findLabel(lines, label)
	if start < 0 {
		return Metrics{}, fmt.Errorf("symbol %q not found", label)
	}
	end := nextSymbolBoundary(lines, start)
	return computeMetrics(label, iter, lines[start:end], t, mode, symbols), nil
}
