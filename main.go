// cyclecount — an AVR assembly cost analyzer.
//
// It counts instructions on the hot path, applies the cycle counts from the
// Microchip AVR Instruction Set Manual (DS40002198C) for the selected CPU
// variant, and reports the result next to the flash and SRAM cost so an
// optimization can be judged by measurement rather than belief.
//
// Any AVR is supported: pick a part with -mcu, or a core directly with
// -core {avr|avre|avre+|avrxm|avrxt|avrrc} (+ -pc for >128 KB parts).
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"text/tabwriter"

	"cyclecount/internal/analyze"
	"cyclecount/internal/asm"
	"cyclecount/internal/isa"
)

var (
	flVerbose  = flag.Bool("v", false, "print a per-instruction listing with word/cycle costs")
	flJSON     = flag.Bool("json", false, "emit machine-readable JSON instead of a text report")
	flFormat   = flag.String("format", "text", "output format: text, json, csv, md, gha, sarif")
	flClock    = flag.Float64("clock", 20.0, "CPU clock in MHz for wall-clock time (0 disables)")
	flMCU      = flag.String("mcu", "", "target part number, e.g. attiny3217, atmega328p, avr128da48")
	flCore     = flag.String("core", "", "CPU variant: avr, avre, avre+, avrxm, avrxt, avrrc")
	flPC       = flag.Int("pc", 0, "program-counter width in bytes: 2 (16-bit) or 3 (22-bit)")
	flFrom     = flag.String("from", "", "start label for an explicit range analysis")
	flTo       = flag.String("to", "", "end label for an explicit range analysis")
	flIter     = flag.Int("iter", 1, "loop trip count applied to the -from/-to range")
	flBranches = flag.String("branches", "bounds", "conditional control-flow mode: bounds, best, worst, taken, not-taken")
	flRank     = flag.Int("rank", 0, "show the top N costly symbol/region spans (0 disables)")
	flRankBy   = flag.String("rank-by", "cycles", "ranking metric: cycles, flash, stack")
	flSymbol   = flag.String("symbol", "", "analyze one symbol/function from its label to the next non-local symbol")
	flFunc     = flag.String("func", "", "alias for -symbol")
	flVS       = flag.String("vs", "", "baseline file to diff the target against")
	flObjdump  = flag.String("objdump", "avr-objdump", "objdump binary used for ELF/.o/.hex input")

	flMaxCycles = flag.Int("max-cycles", 0, "fail (exit 3) if worst-case cycles exceed N (0 disables)")
	flMaxFlash  = flag.Int("max-flash", 0, "fail (exit 3) if flash bytes exceed N (0 disables)")
	flMaxSRAM   = flag.Int("max-sram", 0, "fail (exit 3) if static SRAM bytes exceed N (0 disables)")

	flCPP = flag.Bool("cpp", false, "run the C preprocessor (#include/#define/#if) over .S/.s source before parsing")
	flCC  = flag.String("cc", "avr-gcc", "compiler driver used for -cpp")

	flVersion  = flag.Bool("version", false, "print version and exit")
	flListMCUs = flag.Bool("list-mcus", false, "list known MCU part numbers and core/PC mapping, then exit")

	flDefine  multiFlag // -D NAME[=VAL], repeatable; passed to -cpp
	flInclude multiFlag // -I DIR, repeatable; passed to -cpp
)

// multiFlag collects a repeatable string flag into a slice.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `cyclecount — AVR assembly cost analyzer

usage: cyclecount [flags] <file>

Input is auto-detected:
    .S/.s assembler source (incl. avr-gcc -S output), OR
    a compiled ELF/.o, an Intel-HEX .hex, or saved 'avr-objdump -h -d' text —
    these are disassembled (objdump binary set with -objdump) so macros,
    .include, and the C preprocessor are already resolved.

Select the target (default: ATtiny3217 / AVRxt):
    -mcu attiny3217        a known part number, or
    -core avrxt            a CPU variant directly (works for any AVR)
    -pc 3                  22-bit PC override for >128 KB parts

Mark a hot path inside the source with comment annotations:
    ; @begin <name> iter=<N>
    ...instructions...
    ; @end <name>

Gate a build in CI with cost budgets (exit 3 when any limit is exceeded):
    -max-cycles N   -max-flash N   -max-sram N

Expand the C preprocessor on .S/.s source before parsing (#include/#define/#if):
    -cpp [-cc avr-gcc] [-D NAME=VAL ...] [-I dir ...]

Discover what is supported (these exit without reading a file):
    -list-mcus             print the known part numbers and family fallbacks
    -version               print the build version

flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
flags must precede the file (Go flag parsing stops at the first operand).

examples:
    cyclecount delay.S
    cyclecount -mcu atmega328p -v delay.S
    cyclecount -core avrrc -clock 8 delay.S
    cyclecount -mcu attiny3217 -vs baseline.S opt.S
    cyclecount -mcu attiny3217 firmware.o      # analyze a compiled object
    cyclecount -mcu atmega328p firmware.hex     # analyze an Intel-HEX image
    cyclecount -mcu attiny3217 -func toggle_pin firmware.o
    cyclecount -mcu attiny3217 -max-cycles 40 -from loop -to done hot.S
    cyclecount -mcu atmega328p -cpp -D F_CPU=16000000 blink.S
`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "cyclecount:", err)
	os.Exit(1)
}

// version is overridable at build time with -ldflags "-X main.version=...".
var version = ""

// versionString reports the build version: the ldflags override when set,
// otherwise the VCS revision embedded by the Go toolchain, else "dev".
func versionString() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", ""
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return rev + dirty
		}
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
	}
	return "dev"
}

// pcBitsFor maps a PC byte width to its bit count for display.
func pcBitsFor(pcBytes int) int {
	if pcBytes >= 3 {
		return 22
	}
	return 16
}

// listMCUs prints the curated part table and the family-pattern fallbacks.
func listMCUs(w io.Writer) {
	devs := isa.Devices()
	fmt.Fprintf(w, "Known MCU part numbers (%d) — pass one with -mcu:\n\n", len(devs))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  part\tcore\tpc")
	fmt.Fprintln(tw, "  ----\t----\t--")
	for _, d := range devs {
		fmt.Fprintf(tw, "  %s\t%s\t%d-bit\n", d.Name, d.Variant, pcBitsFor(d.PCBytes))
	}
	tw.Flush()

	fmt.Fprintln(w, "\nFamily fallbacks (any part matching a pattern resolves without an exact entry):")
	ftw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(ftw, "  pattern\tcore\tpc")
	fmt.Fprintln(ftw, "  -------\t----\t--")
	for _, f := range isa.Families() {
		fmt.Fprintf(ftw, "  %s\t%s\t%d-bit\n", f.Pattern, f.Variant, pcBitsFor(f.PCBytes))
	}
	ftw.Flush()

	fmt.Fprintln(w, "\nAny AVR not listed can still be analyzed with -core {avr|avre|avre+|avrxm|avrxt|avrrc}")
	fmt.Fprintln(w, "(add -pc 3 for parts with more than 128 KB of flash).")
}

func csvList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func resolveTarget() (analyze.Target, error) {
	dflt, ok := isa.LookupDevice(defaultMCU)
	if !ok {
		return analyze.Target{}, fmt.Errorf("internal error: default device %q not found", defaultMCU)
	}
	t := analyze.Target{Name: dflt.Name, Variant: dflt.Variant, PCBytes: dflt.PCBytes, FlashKB: dflt.FlashKB, Missing: dflt.Missing}
	if *flMCU != "" {
		d, ok := isa.LookupDevice(*flMCU)
		if !ok {
			return t, fmt.Errorf("unknown device %q — pass -core {avr|avre|avre+|avrxm|avrxt|avrrc} (and -pc 3 for >128 KB parts) instead", *flMCU)
		}
		t = analyze.Target{Name: d.Name, Variant: d.Variant, PCBytes: d.PCBytes, FlashKB: d.FlashKB, Missing: d.Missing}
	}
	if *flCore != "" {
		v, ok := isa.ParseVariant(*flCore)
		if !ok {
			return t, fmt.Errorf("unknown core %q (use avr, avre, avre+, avrxm, avrxt, or avrrc)", *flCore)
		}
		t.Variant = v
		if *flMCU == "" {
			t.Name = v.String()
			t.FlashKB = 0
			t.Missing = nil
		}
	}
	if *flPC != 0 {
		if *flPC != 2 && *flPC != 3 {
			return t, errors.New("-pc must be 2 (16-bit PC) or 3 (22-bit PC)")
		}
		t.PCBytes = *flPC
	}
	return t, nil
}

func resolveTargets() ([]analyze.Target, error) {
	mcus, cores := csvList(*flMCU), csvList(*flCore)
	if len(mcus) > 1 && len(cores) > 0 {
		return nil, errors.New("comma-separated -mcu cannot be combined with -core; pick one target dimension")
	}
	if len(mcus) > 1 && *flCPP {
		return nil, errors.New("-cpp cannot be combined with comma-separated -mcu; preprocess each target separately")
	}
	if len(cores) > 1 && len(mcus) > 0 {
		return nil, errors.New("comma-separated -core cannot be combined with -mcu; pick one target dimension")
	}
	if len(mcus) > 1 {
		ts := make([]analyze.Target, 0, len(mcus))
		for _, mcu := range mcus {
			old := *flMCU
			*flMCU = mcu
			t, err := resolveTarget()
			*flMCU = old
			if err != nil {
				return nil, err
			}
			ts = append(ts, t)
		}
		return ts, nil
	}
	if len(cores) > 1 {
		ts := make([]analyze.Target, 0, len(cores))
		for _, core := range cores {
			old := *flCore
			*flCore = core
			t, err := resolveTarget()
			*flCore = old
			if err != nil {
				return nil, err
			}
			ts = append(ts, t)
		}
		return ts, nil
	}
	t, err := resolveTarget()
	if err != nil {
		return nil, err
	}
	return []analyze.Target{t}, nil
}

func pcBits(t analyze.Target) int {
	return pcBitsFor(t.PCBytes)
}

type analysisRun struct {
	Target  analyze.Target
	Result  analyze.Result
	Range   *analyze.Metrics
	Symbol  *analyze.Metrics
	Budgets []budgetCheck
}

type rankedEntry struct {
	Kind    string
	Metrics analyze.Metrics
}

type outputFormat string

const (
	fmtText  outputFormat = "text"
	fmtJSON  outputFormat = "json"
	fmtCSV   outputFormat = "csv"
	fmtMD    outputFormat = "md"
	fmtGHA   outputFormat = "gha"
	fmtSARIF outputFormat = "sarif"
)

func (r analysisRun) scopeLabel() string {
	if r.Symbol != nil {
		return "symbol " + r.Symbol.Name
	}
	if r.Range != nil {
		return "range " + r.Range.Name
	}
	return "whole file"
}

func (r analysisRun) scopeMetrics() analyze.Metrics {
	if r.Symbol != nil {
		return *r.Symbol
	}
	if r.Range != nil {
		return *r.Range
	}
	return r.Result.File
}

func analyzeForTarget(lines []*asm.Line, target analyze.Target) (analysisRun, error) {
	run := analysisRun{Target: target}
	mode, _ := analyze.ParseBranchMode(*flBranches)
	run.Result = analyze.AnalyzeMode(lines, target, mode)
	if *flFrom != "" || *flTo != "" {
		m, err := analyze.RangeMetricsMode(lines, *flFrom, *flTo, *flIter, target, mode)
		if err != nil {
			return run, err
		}
		run.Range = &m
	}
	if name := selectedSymbol(); name != "" {
		m, err := analyze.SymbolMetricsMode(lines, name, *flIter, target, mode)
		if err != nil {
			return run, err
		}
		run.Symbol = &m
	}
	run.Budgets = evalBudgets(budgetsFromFlags(), run.Result, run.Range, run.Symbol)
	return run, nil
}

func resolveFormat() (outputFormat, error) {
	f := outputFormat(strings.ToLower(strings.TrimSpace(*flFormat)))
	if *flJSON {
		if f != fmtText && f != fmtJSON {
			return "", errors.New("-json cannot be combined with -format other than json")
		}
		f = fmtJSON
	}
	switch f {
	case fmtText, fmtJSON, fmtCSV, fmtMD, fmtGHA, fmtSARIF:
		return f, nil
	default:
		return "", fmt.Errorf("unknown -format %q (use text, json, csv, md, gha, or sarif)", *flFormat)
	}
}

func rankMetricValue(m analyze.Metrics) int {
	switch strings.ToLower(strings.TrimSpace(*flRankBy)) {
	case "flash":
		return m.FlashBytes()
	case "stack":
		return m.PeakStackBytes
	default:
		return m.CyclesMax * max(m.Iter, 1)
	}
}

func rankMetricLabel() string {
	switch strings.ToLower(strings.TrimSpace(*flRankBy)) {
	case "flash":
		return "flash bytes"
	case "stack":
		return "peak stack bytes"
	default:
		return "worst-case cycles"
	}
}

func rankedEntries(res analyze.Result) []rankedEntry {
	var out []rankedEntry
	for _, m := range res.Symbols {
		out = append(out, rankedEntry{Kind: "symbol", Metrics: m})
	}
	for _, m := range res.Regions {
		out = append(out, rankedEntry{Kind: "region", Metrics: m})
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := rankMetricValue(out[i].Metrics), rankMetricValue(out[j].Metrics)
		if li != lj {
			return li > lj
		}
		return out[i].Metrics.Name < out[j].Metrics.Name
	})
	return out
}

func topRanked(res analyze.Result) []rankedEntry {
	rs := rankedEntries(res)
	if *flRank <= 0 || *flRank >= len(rs) {
		return rs
	}
	return rs[:*flRank]
}

func main() {
	flag.Usage = usage
	flag.Var(&flDefine, "D", "preprocessor define NAME[=VAL] for -cpp (repeatable)")
	flag.Var(&flInclude, "I", "preprocessor include directory for -cpp (repeatable)")
	flag.Parse()
	if *flVersion {
		fmt.Println("cyclecount", versionString())
		return
	}
	if *flListMCUs {
		listMCUs(os.Stdout)
		return
	}
	files := flag.Args()
	if len(files) == 0 {
		usage()
		os.Exit(2)
	}
	if len(files) > 1 {
		for _, a := range files[1:] {
			if strings.HasPrefix(a, "-") {
				fail(fmt.Errorf("unexpected argument %q — flags must come before the file", a))
			}
		}
		fail(fmt.Errorf("expected one file, got %d: %s", len(files), strings.Join(files, " ")))
	}
	path := files[0]

	if err := validateSelectionFlags(); err != nil {
		fail(err)
	}

	targets, err := resolveTargets()
	if err != nil {
		fail(err)
	}
	format, err := resolveFormat()
	if err != nil {
		fail(err)
	}
	matrixMode := len(targets) > 1
	if err := validateModeFlags(matrixMode, format); err != nil {
		fail(err)
	}

	lines, err := load(path, *flObjdump)
	if err != nil {
		fail(err)
	}
	runs := make([]analysisRun, 0, len(targets))
	for _, target := range targets {
		run, err := analyzeForTarget(lines, target)
		if err != nil {
			fail(err)
		}
		runs = append(runs, run)
	}

	// Budgets gate the target file's cost; evaluate them before the -vs return
	// so a CI run that combines -vs with -max-* still fails on the exit status.
	run := runs[0]

	if *flVS != "" {
		blines, err := load(*flVS, *flObjdump)
		if err != nil {
			fail(err)
		}
		// Analyze the baseline with the same conditional-branch policy as the new
		// file so the diff is apples-to-apples; otherwise -branches worst on the
		// new file would show a spurious Δ against a bounds-mode baseline.
		mode, _ := analyze.ParseBranchMode(*flBranches)
		bres := analyze.AnalyzeMode(blines, run.Target, mode)
		if format == fmtJSON {
			emitCompareJSON(run.Result, bres, path, *flVS, run.Budgets)
		} else if format == fmtCSV {
			renderCompareCSV(os.Stdout, run.Result, bres, path, *flVS, run.Budgets)
		} else if format == fmtMD {
			renderCompareMD(os.Stdout, run.Result, bres, path, *flVS, *flClock, run.Budgets)
		} else if format == fmtGHA {
			renderCompareGHA(os.Stdout, run.Result, bres, path, *flVS, run.Budgets, *flClock)
		} else if format == fmtSARIF {
			emitCompareSARIF(run, bres, path, *flVS)
		} else {
			renderCompare(os.Stdout, run.Result, bres, path, *flVS, *flClock)
			renderBudgets(os.Stdout, run.Budgets)
		}
		if budgetsExceeded(run.Budgets) {
			os.Exit(3)
		}
		return
	}

	if matrixMode {
		if format == fmtJSON {
			emitMatrixJSON(runs, path)
		} else if format == fmtCSV {
			renderMatrixCSV(os.Stdout, runs, path)
		} else if format == fmtMD {
			renderMatrixMD(os.Stdout, runs, path, *flClock)
		} else if format == fmtGHA {
			renderMatrixGHA(os.Stdout, runs, path, *flClock)
		} else if format == fmtSARIF {
			emitMatrixSARIF(runs, path)
		} else {
			renderMatrix(os.Stdout, runs, path, *flClock)
		}
		return
	}

	if format == fmtJSON {
		emitJSON(run.Result, run.Range, run.Symbol, path, run.Budgets)
	} else if format == fmtCSV {
		renderSingleCSV(os.Stdout, run, path)
	} else if format == fmtMD {
		renderSingleMD(os.Stdout, run, path, *flClock)
	} else if format == fmtGHA {
		renderSingleGHA(os.Stdout, run, path, *flClock)
	} else if format == fmtSARIF {
		emitSingleSARIF(run, path)
	} else {
		renderReport(os.Stdout, run.Result, run.Range, path, *flClock, *flVerbose)
		if run.Symbol != nil {
			renderMetrics(os.Stdout, *run.Symbol, run.Target, *flClock, *flVerbose)
		}
		if *flRank > 0 {
			renderRanking(os.Stdout, run.Result)
		}
		renderBudgets(os.Stdout, run.Budgets)
	}
	if budgetsExceeded(run.Budgets) {
		os.Exit(3)
	}
}

func selectedSymbol() string {
	switch {
	case *flSymbol != "":
		return *flSymbol
	case *flFunc != "":
		return *flFunc
	default:
		return ""
	}
}

func validateSelectionFlags() error {
	if *flIter < 1 {
		return errors.New("-iter must be >= 1")
	}
	if *flClock < 0 {
		return errors.New("-clock must be >= 0 (0 disables wall-clock time)")
	}
	if *flRank < 0 {
		return errors.New("-rank must be >= 0")
	}
	if _, ok := analyze.ParseBranchMode(*flBranches); !ok {
		return fmt.Errorf("unknown -branches mode %q (use bounds, best, worst, taken, or not-taken)", *flBranches)
	}
	switch strings.ToLower(strings.TrimSpace(*flRankBy)) {
	case "cycles", "flash", "stack":
	default:
		return fmt.Errorf("unknown -rank-by %q (use cycles, flash, or stack)", *flRankBy)
	}
	if *flSymbol != "" && *flFunc != "" && *flSymbol != *flFunc {
		return errors.New("-symbol and -func name different spans; use only one")
	}
	if selectedSymbol() != "" && (*flFrom != "" || *flTo != "") {
		return errors.New("-symbol/-func cannot be combined with -from/-to")
	}
	return nil
}

func validateModeFlags(matrix bool, format outputFormat) error {
	if (*flFrom != "" || *flTo != "") && (*flFrom == "" || *flTo == "") {
		return errors.New("-from and -to must be supplied together")
	}
	if matrix && *flVS != "" {
		return errors.New("multi-target mode cannot be combined with -vs")
	}
	if matrix && budgetsFromFlags().any() {
		return errors.New("multi-target mode cannot be combined with -max-* budgets")
	}
	if matrix && *flVerbose {
		return errors.New("multi-target mode cannot be combined with -v")
	}
	if format != fmtText && *flVerbose {
		return errors.New("-v is only supported with -format text")
	}
	if matrix && *flRank > 0 {
		return errors.New("-rank is only supported in single-target mode")
	}
	if *flVS != "" && *flRank > 0 {
		return errors.New("-rank cannot be combined with -vs")
	}
	// The comparison renderers diff whole-file metrics only; a narrowed scope
	// would silently apply to budgets but not the diff, so reject the mix.
	if *flVS != "" && (selectedSymbol() != "" || *flFrom != "" || *flTo != "") {
		return errors.New("-vs compares whole files; it cannot be combined with -symbol/-func or -from/-to")
	}
	return nil
}

// defaultMCU is the part behind the default target (resolveTarget's ATtiny3217);
// it is the -mmcu= passed to the preprocessor when neither -mcu nor -core is set,
// so <avr/io.h> still resolves for the documented default.
const defaultMCU = "attiny3217"

// cppMCU picks the -mmcu= value for the preprocessor: the explicit -mcu when
// given; otherwise the default target's part so device headers resolve. A bare
// -core (no specific device) has no single right -mmcu, so none is passed.
func cppMCU(mcu, core string) string {
	if mcu != "" {
		return strings.ToLower(mcu)
	}
	if core == "" {
		return defaultMCU
	}
	return ""
}

// load reads a file, optionally running the C preprocessor first (-cpp).
func load(path, objdumpBin string) ([]*asm.Line, error) {
	if *flCPP {
		return asm.LoadFileCPP(path, objdumpBin, asm.CPPOptions{
			CC: *flCC, MMCU: cppMCU(*flMCU, *flCore),
			Defines: flDefine, Includes: flInclude,
		})
	}
	return asm.LoadFile(path, objdumpBin)
}

// ---------------------------------------------------------------- rendering

func renderReport(w io.Writer, res analyze.Result, rng *analyze.Metrics, path string, clock float64, verbose bool) {
	t := res.Target
	fmt.Fprintf(w, "AVR cycle & size report — %s\n", path)
	fmt.Fprintf(w, "Target: %s (core %s, %d-bit PC).\n", t.Name, t.Variant, pcBits(t))
	fmt.Fprintln(w, `Cycle data: AVR Instruction Set Manual DS40002198C.`)
	fmt.Fprintf(w, "Branches: %s.\n", *flBranches)
	if clock > 0 {
		fmt.Fprintf(w, "Clock: %g MHz.\n", clock)
	}
	fmt.Fprintln(w)

	renderMetrics(w, res.File, t, clock, verbose)
	for _, rm := range res.Regions {
		renderMetrics(w, rm, t, clock, verbose)
	}
	if rng != nil {
		renderMetrics(w, *rng, t, clock, verbose)
	}

	fmt.Fprintln(w, "== static SRAM (.data/.bss/.noinit) ==")
	fmt.Fprintf(w, "  Allocated      : %d bytes\n", res.SRAMStatic)
	for _, k := range sortedKeys(res.Sections) {
		fmt.Fprintf(w, "    %-10s : %d B\n", k, res.Sections[k])
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Notes: branch/skip cycles are min–max; the exact path depends on data.")
	switch strings.ToLower(strings.TrimSpace(*flBranches)) {
	case "taken", "not-taken", "nottaken", "fallthrough":
		fmt.Fprintln(w, "Taken/not-taken mode follows direct resolvable branches/jumps and stops on repeated instructions or RET.")
	case "best", "worst", "min", "max":
		fmt.Fprintln(w, "Best/worst mode constrains conditional timing only; it does not prune later instructions from the listing.")
	}
	fmt.Fprintln(w, "LD/ST/LDS/STS add 1 cycle when the access targets NVM (manual note 2).")
}

func renderMetrics(w io.Writer, m analyze.Metrics, t analyze.Target, clock float64, verbose bool) {
	iter := max(m.Iter, 1)
	fmt.Fprintf(w, "== %s ==\n", m.Name)
	fmt.Fprintf(w, "  Instructions   : %d\n", m.InstrCount)
	fmt.Fprintf(w, "  Flash          : %d bytes (%d words)\n", m.FlashBytes(), m.FlashWords)
	if m.FlashDataBytes > 0 {
		fmt.Fprintf(w, "                   %d B code + %d B inline data\n", m.FlashWords*2, m.FlashDataBytes)
	}

	if m.CyclesMin == m.CyclesMax {
		fmt.Fprintf(w, "  Cycles/pass    : %d\n", m.CyclesMin)
	} else {
		fmt.Fprintf(w, "  Cycles/pass    : %d – %d  (min–max)\n", m.CyclesMin, m.CyclesMax)
	}
	if iter > 1 {
		fmt.Fprintf(w, "  Cycles × %-6d: %d – %d\n", iter, m.CyclesMin*iter, m.CyclesMax*iter)
	}
	if clock > 0 {
		lo := fmtTime(m.CyclesMin*iter, clock)
		hi := fmtTime(m.CyclesMax*iter, clock)
		if lo == hi {
			fmt.Fprintf(w, "  Time @%gMHz    : %s\n", clock, lo)
		} else {
			fmt.Fprintf(w, "  Time @%gMHz    : %s – %s\n", clock, lo, hi)
		}
	}

	if m.Pushes > 0 || m.Pops > 0 || m.Calls > 0 {
		fmt.Fprintf(w, "  Stack (SRAM)   : peak %d B (%d PUSH / %d POP)", m.PeakStackBytes, m.Pushes, m.Pops)
		if m.Calls > 0 {
			fmt.Fprintf(w, ", %d call(s)", m.Calls)
			if m.PeakPushBytes > 0 || m.PeakCallBytes > 0 {
				fmt.Fprintf(w, ", %d B push depth + %d B call-chain overhead at deepest call site", m.PeakPushBytes, m.PeakCallBytes)
			}
		}
		fmt.Fprintln(w)
	}

	warnSet(w, "✘ unavailable ", m.Unavailable, "not implemented on this target — will not assemble")
	warnSet(w, "● not modeled ", m.Unmodeled, "counted for size, cycles are data/programming dependent")
	warnSet(w, "⚠ unrecognized", m.Unknown, "not counted — check syntax or extend the ISA table")

	if verbose {
		renderListing(w, m, t)
	}
	fmt.Fprintln(w)
}

func warnSet(w io.Writer, label string, set map[string]int, why string) {
	if len(set) == 0 {
		return
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "  %s : %s (%s)\n", label, strings.Join(keys, ", "), why)
}

func renderListing(w io.Writer, m analyze.Metrics, t analyze.Target) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  line\tmnemonic\toperands\twords\tcycles\tnote")
	fmt.Fprintln(tw, "  ----\t--------\t--------\t-----\t------\t----")
	for _, lm := range m.Lines {
		words, cyc, note := "?", "(unknown)", ""
		if lm.Known {
			words = fmt.Sprintf("%d", lm.Info.WordCount(t.Variant))
			switch {
			case !isa.AvailableOnTarget(lm.Line.Mnemonic, t.Variant, t.PCBytes, t.FlashKB, t.Missing):
				cyc, note = "n/a", "not on "+t.Name
			case lm.Info.Special != "":
				cyc, note = "—", lm.Info.Special
			default:
				cc, _ := lm.Info.Cycles(t.Variant, t.PCBytes)
				if cc.Min == cc.Max {
					cyc = fmt.Sprintf("%d", cc.Min)
				} else {
					cyc = fmt.Sprintf("%d–%d", cc.Min, cc.Max)
				}
				note = lm.Info.Note
			}
		}
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%s\n",
			lm.Line.Num, lm.Line.Mnemonic, lm.Line.Operands, words, cyc, note)
	}
	tw.Flush()
}

func renderCompare(w io.Writer, neu, base analyze.Result, np, bp string, clock float64) {
	t := neu.Target
	fmt.Fprintf(w, "Comparison (whole file)\n  new      : %s\n  baseline : %s\n", np, bp)
	fmt.Fprintf(w, "Target: %s (core %s, %d-bit PC). Δ > 0 means the new version costs more.\n\n",
		t.Name, t.Variant, pcBits(t))

	n, b := neu.File, base.File
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  metric\tbaseline\tnew\tΔ")
	fmt.Fprintln(tw, "  ------\t--------\t---\t-")
	row := func(name string, bv, nv int, unit string) {
		fmt.Fprintf(tw, "  %s\t%d%s\t%d%s\t%+d%s\n", name, bv, unit, nv, unit, nv-bv, unit)
	}
	row("instructions", b.InstrCount, n.InstrCount, "")
	row("cycles (min)", b.CyclesMin, n.CyclesMin, "")
	row("cycles (max)", b.CyclesMax, n.CyclesMax, "")
	row("flash", b.FlashBytes(), n.FlashBytes(), " B")
	row("SRAM static", base.SRAMStatic, neu.SRAMStatic, " B")
	row("peak stack", b.PeakStackBytes, n.PeakStackBytes, " B")
	tw.Flush()
	fmt.Fprintln(w)

	dCyc := n.CyclesMax - b.CyclesMax
	dFlash := n.FlashBytes() - b.FlashBytes()
	dSRAM := neu.SRAMStatic - base.SRAMStatic
	fmt.Fprintln(w, "  Verdict:", verdict(dCyc, dFlash, dSRAM))
	if clock > 0 && dCyc != 0 {
		fmt.Fprintf(w, "  Δ worst-case time @%gMHz: %s\n", clock, signedTime(dCyc, clock))
	}
}

func renderMatrix(w io.Writer, runs []analysisRun, path string, clock float64) {
	if len(runs) == 0 {
		return
	}
	fmt.Fprintf(w, "Multi-target comparison — %s\n", path)
	fmt.Fprintln(w, `Cycle data: AVR Instruction Set Manual DS40002198C.`)
	fmt.Fprintf(w, "Scope: %s.\n", runs[0].scopeLabel())
	fmt.Fprintf(w, "Branches: %s.\n", *flBranches)
	if clock > 0 {
		fmt.Fprintf(w, "Clock: %g MHz.\n", clock)
	}
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  target\tcore\tpc\tinstructions\tcycles/pass\tflash\tpeak stack\tstatic SRAM\tnotes")
	fmt.Fprintln(tw, "  ------\t----\t--\t------------\t-----------\t-----\t----------\t-----------\t-----")
	for _, run := range runs {
		m := run.scopeMetrics()
		cycles := fmt.Sprintf("%d", m.CyclesMin)
		if m.CyclesMin != m.CyclesMax {
			cycles = fmt.Sprintf("%d-%d", m.CyclesMin, m.CyclesMax)
		}
		notes := matrixNotes(m)
		fmt.Fprintf(tw, "  %s\t%s\t%d-bit\t%d\t%s\t%d B\t%d B\t%d B\t%s\n",
			run.Target.Name, run.Target.Variant, pcBits(run.Target), m.InstrCount, cycles,
			m.FlashBytes(), m.PeakStackBytes, run.Result.SRAMStatic, notes)
	}
	tw.Flush()
	fmt.Fprintln(w)
	switch strings.ToLower(strings.TrimSpace(*flBranches)) {
	case "taken", "not-taken", "nottaken", "fallthrough":
		fmt.Fprintln(w, "Notes: taken/not-taken follows direct resolvable branches/jumps and stops on repeated instructions or RET.")
	default:
		fmt.Fprintln(w, "Notes: branch/skip cycles reflect the selected conditional-timing mode; later instructions remain part of the listing.")
	}
}

func renderRanking(w io.Writer, res analyze.Result) {
	rs := topRanked(res)
	if len(rs) == 0 {
		fmt.Fprintln(w, "== ranking ==")
		fmt.Fprintln(w, "  no top-level symbols or annotated regions found.")
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, "== ranking (top %d by %s) ==\n", len(rs), rankMetricLabel())
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  kind\tname\tvalue\tcycles/pass\tflash\tpeak stack")
	fmt.Fprintln(tw, "  ----\t----\t-----\t-----------\t-----\t----------")
	for _, r := range rs {
		m := r.Metrics
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\t%d B\t%d B\n",
			r.Kind, m.Name, rankMetricValue(m), cycleCell(m), m.FlashBytes(), m.PeakStackBytes)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func cycleCell(m analyze.Metrics) string {
	if m.CyclesMin == m.CyclesMax {
		return fmt.Sprintf("%d", m.CyclesMin)
	}
	return fmt.Sprintf("%d-%d", m.CyclesMin, m.CyclesMax)
}

func scopeName(run analysisRun) string {
	if run.Symbol != nil {
		return run.Symbol.Name
	}
	if run.Range != nil {
		return run.Range.Name
	}
	return run.Result.File.Name
}

func renderSingleCSV(w io.Writer, run analysisRun, path string) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"file", "scope_type", "scope_name", "target", "core", "pc_bits", "instructions", "cycles_min", "cycles_max", "flash_bytes", "peak_stack_bytes", "peak_push_bytes", "peak_call_bytes", "static_sram_bytes", "branch_mode", "rank_kind", "rank_value"})
	for _, row := range singleRows(run, path) {
		_ = cw.Write(row)
	}
	cw.Flush()
}

func singleRows(run analysisRun, path string) [][]string {
	var rows [][]string
	add := func(kind string, m analyze.Metrics) {
		rankKind, rankValue := "", ""
		if *flRank > 0 && (kind == "symbol" || kind == "region") {
			rankKind, rankValue = *flRankBy, fmt.Sprintf("%d", rankMetricValue(m))
		}
		rows = append(rows, []string{
			path, kind, m.Name, run.Target.Name, run.Target.Variant.String(), fmt.Sprintf("%d", pcBits(run.Target)),
			fmt.Sprintf("%d", m.InstrCount), fmt.Sprintf("%d", m.CyclesMin), fmt.Sprintf("%d", m.CyclesMax),
			fmt.Sprintf("%d", m.FlashBytes()), fmt.Sprintf("%d", m.PeakStackBytes), fmt.Sprintf("%d", m.PeakPushBytes),
			fmt.Sprintf("%d", m.PeakCallBytes), fmt.Sprintf("%d", run.Result.SRAMStatic), *flBranches, rankKind, rankValue,
		})
	}
	add("whole_file", run.Result.File)
	for _, r := range run.Result.Regions {
		add("region", r)
	}
	if run.Range != nil {
		add("range", *run.Range)
	}
	if run.Symbol != nil {
		add("symbol", *run.Symbol)
	}
	if *flRank > 0 {
		for _, r := range topRanked(run.Result) {
			add("ranked_"+r.Kind, r.Metrics)
		}
	}
	return rows
}

func renderMatrixCSV(w io.Writer, runs []analysisRun, path string) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"file", "scope", "target", "core", "pc_bits", "instructions", "cycles_min", "cycles_max", "flash_bytes", "peak_stack_bytes", "peak_push_bytes", "peak_call_bytes", "static_sram_bytes", "notes", "branch_mode"})
	for _, run := range runs {
		m := run.scopeMetrics()
		_ = cw.Write([]string{
			path, run.scopeLabel(), run.Target.Name, run.Target.Variant.String(), fmt.Sprintf("%d", pcBits(run.Target)),
			fmt.Sprintf("%d", m.InstrCount), fmt.Sprintf("%d", m.CyclesMin), fmt.Sprintf("%d", m.CyclesMax),
			fmt.Sprintf("%d", m.FlashBytes()), fmt.Sprintf("%d", m.PeakStackBytes), fmt.Sprintf("%d", m.PeakPushBytes),
			fmt.Sprintf("%d", m.PeakCallBytes), fmt.Sprintf("%d", run.Result.SRAMStatic), matrixNotes(m), *flBranches,
		})
	}
	cw.Flush()
}

func renderCompareCSV(w io.Writer, neu, base analyze.Result, np, bp string, budgets []budgetCheck) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"metric", "baseline_file", "baseline", "new_file", "new", "delta"})
	write := func(metric string, b, n int) {
		_ = cw.Write([]string{metric, bp, fmt.Sprintf("%d", b), np, fmt.Sprintf("%d", n), fmt.Sprintf("%+d", n-b)})
	}
	write("instructions", base.File.InstrCount, neu.File.InstrCount)
	write("cycles_min", base.File.CyclesMin, neu.File.CyclesMin)
	write("cycles_max", base.File.CyclesMax, neu.File.CyclesMax)
	write("flash_bytes", base.File.FlashBytes(), neu.File.FlashBytes())
	write("sram_static_bytes", base.SRAMStatic, neu.SRAMStatic)
	write("peak_stack_bytes", base.File.PeakStackBytes, neu.File.PeakStackBytes)
	// Budget rows reuse the columns: "baseline" carries the limit, "new" the
	// measured value, so a positive delta means the limit was exceeded.
	for _, bgt := range budgets {
		write("budget:"+bgt.Name, bgt.Limit, bgt.Got)
	}
	cw.Flush()
}

func renderSingleMD(w io.Writer, run analysisRun, path string, clock float64) {
	fmt.Fprintf(w, "# AVR Cycle Report\n\n")
	fmt.Fprintf(w, "- File: `%s`\n- Target: `%s` (%s, %d-bit PC)\n- Branches: `%s`\n", path, run.Target.Name, run.Target.Variant, pcBits(run.Target), *flBranches)
	if clock > 0 {
		fmt.Fprintf(w, "- Clock: `%g MHz`\n", clock)
	}
	fmt.Fprintln(w, "\n| Scope | Instructions | Cycles/pass | Flash | Peak Stack | Static SRAM |")
	fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: |")
	for _, row := range singleRows(run, path) {
		fmt.Fprintf(w, "| %s:%s | %s | %s-%s | %s B | %s B | %s B |\n",
			row[1], row[2], row[6], row[7], row[8], row[9], row[10], row[13])
	}
	if len(run.Budgets) > 0 {
		fmt.Fprintln(w, "\n## Budgets")
		fmt.Fprintln(w, "| Name | Got | Limit | Scope | OK |")
		fmt.Fprintln(w, "| --- | ---: | ---: | --- | --- |")
		for _, b := range run.Budgets {
			fmt.Fprintf(w, "| %s | %d%s | %d%s | %s | %t |\n", b.Name, b.Got, b.Unit, b.Limit, b.Unit, b.Scope, b.OK)
		}
	}
	if *flRank > 0 {
		fmt.Fprintf(w, "\n## Ranking\n\nTop %d by `%s`.\n\n", len(topRanked(run.Result)), *flRankBy)
		fmt.Fprintln(w, "| Kind | Name | Value | Cycles/pass | Flash | Peak Stack |")
		fmt.Fprintln(w, "| --- | --- | ---: | ---: | ---: | ---: |")
		for _, r := range topRanked(run.Result) {
			m := r.Metrics
			fmt.Fprintf(w, "| %s | %s | %d | %s | %d B | %d B |\n", r.Kind, m.Name, rankMetricValue(m), cycleCell(m), m.FlashBytes(), m.PeakStackBytes)
		}
	}
}

func renderMatrixMD(w io.Writer, runs []analysisRun, path string, clock float64) {
	fmt.Fprintf(w, "# AVR Multi-Target Comparison\n\n- File: `%s`\n- Scope: `%s`\n- Branches: `%s`\n", path, runs[0].scopeLabel(), *flBranches)
	if clock > 0 {
		fmt.Fprintf(w, "- Clock: `%g MHz`\n", clock)
	}
	fmt.Fprintln(w, "\n| Target | Core | PC | Instructions | Cycles/pass | Flash | Peak Stack | Static SRAM | Notes |")
	fmt.Fprintln(w, "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, run := range runs {
		m := run.scopeMetrics()
		fmt.Fprintf(w, "| %s | %s | %d | %d | %s | %d B | %d B | %d B | %s |\n",
			run.Target.Name, run.Target.Variant, pcBits(run.Target), m.InstrCount, cycleCell(m), m.FlashBytes(), m.PeakStackBytes, run.Result.SRAMStatic, matrixNotes(m))
	}
}

func renderCompareMD(w io.Writer, neu, base analyze.Result, np, bp string, clock float64, budgets []budgetCheck) {
	fmt.Fprintf(w, "# AVR Comparison\n\n- New: `%s`\n- Baseline: `%s`\n", np, bp)
	if clock > 0 {
		fmt.Fprintf(w, "- Clock: `%g MHz`\n", clock)
	}
	fmt.Fprintln(w, "\n| Metric | Baseline | New | Delta |")
	fmt.Fprintln(w, "| --- | ---: | ---: | ---: |")
	row := func(name string, b, n int, unit string) {
		fmt.Fprintf(w, "| %s | %d%s | %d%s | %+d%s |\n", name, b, unit, n, unit, n-b, unit)
	}
	row("instructions", base.File.InstrCount, neu.File.InstrCount, "")
	row("cycles min", base.File.CyclesMin, neu.File.CyclesMin, "")
	row("cycles max", base.File.CyclesMax, neu.File.CyclesMax, "")
	row("flash bytes", base.File.FlashBytes(), neu.File.FlashBytes(), "")
	row("static SRAM", base.SRAMStatic, neu.SRAMStatic, "")
	row("peak stack", base.File.PeakStackBytes, neu.File.PeakStackBytes, "")
	if len(budgets) > 0 {
		fmt.Fprintln(w, "\n## Budgets")
		fmt.Fprintln(w, "| Name | Got | Limit | Scope | OK |")
		fmt.Fprintln(w, "| --- | ---: | ---: | --- | --- |")
		for _, b := range budgets {
			fmt.Fprintf(w, "| %s | %d%s | %d%s | %s | %t |\n", b.Name, b.Got, b.Unit, b.Limit, b.Unit, b.Scope, b.OK)
		}
	}
}

func renderSingleGHA(w io.Writer, run analysisRun, path string, clock float64) {
	fmt.Fprintln(w, "::group::cyclecount summary")
	renderSingleMD(w, run, path, clock)
	fmt.Fprintln(w, "::endgroup::")
	for _, b := range run.Budgets {
		if !b.OK {
			fmt.Fprintf(w, "::error title=cyclecount budget exceeded::%s %d%s exceeds limit %d%s (%s)\n", b.Name, b.Got, b.Unit, b.Limit, b.Unit, b.Scope)
		}
	}
	for _, r := range topRanked(run.Result) {
		fmt.Fprintf(w, "::notice title=cyclecount ranking::%s %s ranks at %d %s\n", r.Kind, r.Metrics.Name, rankMetricValue(r.Metrics), rankMetricLabel())
	}
}

func renderMatrixGHA(w io.Writer, runs []analysisRun, path string, clock float64) {
	fmt.Fprintln(w, "::group::cyclecount multi-target")
	renderMatrixMD(w, runs, path, clock)
	fmt.Fprintln(w, "::endgroup::")
}

func renderCompareGHA(w io.Writer, neu, base analyze.Result, np, bp string, budgets []budgetCheck, clock float64) {
	fmt.Fprintln(w, "::group::cyclecount comparison")
	renderCompareMD(w, neu, base, np, bp, clock, budgets)
	fmt.Fprintln(w, "::endgroup::")
	for _, b := range budgets {
		if !b.OK {
			fmt.Fprintf(w, "::error title=cyclecount budget exceeded::%s %d%s exceeds limit %d%s (%s)\n", b.Name, b.Got, b.Unit, b.Limit, b.Unit, b.Scope)
		}
	}
}

func matrixNotes(m analyze.Metrics) string {
	var notes []string
	if len(m.Unavailable) > 0 {
		notes = append(notes, "unavailable="+strings.Join(sortedKeys(m.Unavailable), ","))
	}
	if len(m.Unmodeled) > 0 {
		notes = append(notes, "unmodeled="+strings.Join(sortedKeys(m.Unmodeled), ","))
	}
	if len(m.Unknown) > 0 {
		notes = append(notes, "unknown="+strings.Join(sortedKeys(m.Unknown), ","))
	}
	if len(notes) == 0 {
		return "-"
	}
	return strings.Join(notes, "; ")
}

func verdict(dCyc, dFlash, dSRAM int) string {
	if dCyc == 0 && dFlash == 0 && dSRAM == 0 {
		return "identical cost."
	}
	part := func(v int, noun string) string {
		switch {
		case v < 0:
			return fmt.Sprintf("saves %d %s", -v, noun)
		case v > 0:
			return fmt.Sprintf("costs %d more %s", v, noun)
		default:
			return fmt.Sprintf("same %s", noun)
		}
	}
	return strings.Join([]string{
		part(dCyc, "cycles (worst case)"),
		part(dFlash, "flash bytes"),
		part(dSRAM, "SRAM bytes"),
	}, ", ") + "."
}

// ---------------------------------------------------------------- budgets

// budgets are the CI gate limits; a zero field means "no limit".
type budgets struct {
	cycles, flash, sram int
}

func budgetsFromFlags() budgets {
	return budgets{cycles: *flMaxCycles, flash: *flMaxFlash, sram: *flMaxSRAM}
}

func (b budgets) any() bool { return b.cycles > 0 || b.flash > 0 || b.sram > 0 }

// budgetCheck is the outcome of one limit, ready for printing or JSON.
type budgetCheck struct {
	Name  string `json:"name"`
	Limit int    `json:"limit"`
	Got   int    `json:"got"`
	Unit  string `json:"unit"`
	Scope string `json:"scope"`
	OK    bool   `json:"ok"`
}

// evalBudgets measures each set limit against the analysis. Cycles gate the
// worst-case total of the primary span — the -from/-to range when present, else
// the whole file; flash and SRAM gate the whole-file footprint.
func evalBudgets(b budgets, res analyze.Result, rng, sym *analyze.Metrics) []budgetCheck {
	if !b.any() {
		return nil
	}
	var out []budgetCheck
	if b.cycles > 0 {
		m, scope := res.File, "whole file"
		if sym != nil {
			m, scope = *sym, "symbol "+sym.Name
		} else if rng != nil {
			m, scope = *rng, "range "+rng.Name
		}
		got := m.CyclesMax * max(m.Iter, 1)
		out = append(out, budgetCheck{"cycles", b.cycles, got, "", scope, got <= b.cycles})
	}
	if b.flash > 0 {
		got := res.File.FlashBytes()
		out = append(out, budgetCheck{"flash", b.flash, got, " B", "whole file", got <= b.flash})
	}
	if b.sram > 0 {
		got := res.SRAMStatic
		out = append(out, budgetCheck{"sram", b.sram, got, " B", "static .data/.bss/.noinit", got <= b.sram})
	}
	return out
}

func budgetsExceeded(cs []budgetCheck) bool {
	for _, c := range cs {
		if !c.OK {
			return true
		}
	}
	return false
}

func renderBudgets(w io.Writer, cs []budgetCheck) {
	if len(cs) == 0 {
		return
	}
	fmt.Fprintln(w, "== budget ==")
	for _, c := range cs {
		status := "OK"
		if !c.OK {
			status = "EXCEEDED"
		}
		fmt.Fprintf(w, "  %-8s : %d%s / %d%s limit — %s (%s)\n",
			c.Name, c.Got, c.Unit, c.Limit, c.Unit, status, c.Scope)
	}
	if budgetsExceeded(cs) {
		fmt.Fprintln(w, "  Verdict: budget exceeded (exit 3).")
	} else {
		fmt.Fprintln(w, "  Verdict: all within budget.")
	}
	fmt.Fprintln(w)
}

// ---------------------------------------------------------------- helpers

func fmtTime(cycles int, mhz float64) string {
	if mhz <= 0 {
		return ""
	}
	sec := float64(cycles) / (mhz * 1e6)
	switch {
	case sec < 1e-6:
		return fmt.Sprintf("%.1f ns", sec*1e9)
	case sec < 1e-3:
		return fmt.Sprintf("%.3f µs", sec*1e6)
	case sec < 1:
		return fmt.Sprintf("%.3f ms", sec*1e3)
	default:
		return fmt.Sprintf("%.3f s", sec)
	}
}

func signedTime(cycles int, mhz float64) string {
	sign := "+"
	if cycles < 0 {
		sign = "-"
		cycles = -cycles
	}
	return sign + fmtTime(cycles, mhz)
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---------------------------------------------------------------- JSON

type jsonMetrics struct {
	Name           string   `json:"name"`
	Iter           int      `json:"iter"`
	Instructions   int      `json:"instructions"`
	FlashWords     int      `json:"flash_words"`
	FlashBytes     int      `json:"flash_bytes"`
	CyclesMin      int      `json:"cycles_min"`
	CyclesMax      int      `json:"cycles_max"`
	CyclesMinTotal int      `json:"cycles_min_total"`
	CyclesMaxTotal int      `json:"cycles_max_total"`
	PeakStackBytes int      `json:"peak_stack_bytes"`
	PeakPushBytes  int      `json:"peak_push_bytes"`
	PeakCallBytes  int      `json:"peak_call_bytes"`
	Pushes         int      `json:"pushes"`
	Pops           int      `json:"pops"`
	Calls          int      `json:"calls"`
	Unavailable    []string `json:"unavailable,omitempty"`
	Unmodeled      []string `json:"unmodeled,omitempty"`
	Unknown        []string `json:"unknown,omitempty"`
}

func keysOf(m map[string]int) []string {
	if len(m) == 0 {
		return nil
	}
	return sortedKeys(m)
}

func toJSON(m analyze.Metrics) jsonMetrics {
	iter := max(m.Iter, 1)
	return jsonMetrics{
		Name: m.Name, Iter: iter,
		Instructions: m.InstrCount,
		FlashWords:   m.FlashWords, FlashBytes: m.FlashBytes(),
		CyclesMin: m.CyclesMin, CyclesMax: m.CyclesMax,
		CyclesMinTotal: m.CyclesMin * iter, CyclesMaxTotal: m.CyclesMax * iter,
		PeakStackBytes: m.PeakStackBytes, PeakPushBytes: m.PeakPushBytes, PeakCallBytes: m.PeakCallBytes,
		Pushes: m.Pushes, Pops: m.Pops, Calls: m.Calls,
		Unavailable: keysOf(m.Unavailable),
		Unmodeled:   keysOf(m.Unmodeled),
		Unknown:     keysOf(m.Unknown),
	}
}

func targetJSON(t analyze.Target) any {
	return struct {
		Name   string `json:"name"`
		Core   string `json:"core"`
		PCBits int    `json:"pc_bits"`
	}{t.Name, t.Variant.String(), pcBits(t)}
}

func emitJSON(res analyze.Result, rng, sym *analyze.Metrics, path string, budgets []budgetCheck) {
	type rankedJSON struct {
		Kind   string      `json:"kind"`
		Metric string      `json:"metric"`
		Value  int         `json:"value"`
		Span   jsonMetrics `json:"span"`
	}
	out := struct {
		File       string        `json:"file"`
		BranchMode string        `json:"branch_mode"`
		Target     any           `json:"target"`
		WholeFile  jsonMetrics   `json:"whole_file"`
		Regions    []jsonMetrics `json:"regions,omitempty"`
		Symbols    []jsonMetrics `json:"symbols,omitempty"`
		Range      *jsonMetrics  `json:"range,omitempty"`
		Symbol     *jsonMetrics  `json:"symbol,omitempty"`
		Ranking    []rankedJSON  `json:"ranking,omitempty"`
		SRAMStatic int           `json:"sram_static_bytes"`
		Budgets    []budgetCheck `json:"budgets,omitempty"`
	}{
		File: path, BranchMode: *flBranches, Target: targetJSON(res.Target),
		WholeFile: toJSON(res.File), SRAMStatic: res.SRAMStatic,
		Budgets: budgets,
	}
	for _, r := range res.Regions {
		out.Regions = append(out.Regions, toJSON(r))
	}
	for _, s := range res.Symbols {
		out.Symbols = append(out.Symbols, toJSON(s))
	}
	if rng != nil {
		j := toJSON(*rng)
		out.Range = &j
	}
	if sym != nil {
		j := toJSON(*sym)
		out.Symbol = &j
	}
	if *flRank > 0 {
		for _, r := range topRanked(res) {
			out.Ranking = append(out.Ranking, rankedJSON{
				Kind: r.Kind, Metric: strings.ToLower(strings.TrimSpace(*flRankBy)),
				Value: rankMetricValue(r.Metrics), Span: toJSON(r.Metrics),
			})
		}
	}
	writeJSON(out)
}

func emitCompareJSON(neu, base analyze.Result, np, bp string, budgets []budgetCheck) {
	out := struct {
		New        string      `json:"new_file"`
		Baseline   string      `json:"baseline_file"`
		BranchMode string      `json:"branch_mode"`
		Target     any         `json:"target"`
		NewM       jsonMetrics `json:"new"`
		BaseM      jsonMetrics `json:"baseline"`
		Delta      struct {
			Instructions int `json:"instructions"`
			CyclesMin    int `json:"cycles_min"`
			CyclesMax    int `json:"cycles_max"`
			FlashBytes   int `json:"flash_bytes"`
			SRAMStatic   int `json:"sram_static_bytes"`
		} `json:"delta"`
		Budgets []budgetCheck `json:"budgets,omitempty"`
	}{New: np, Baseline: bp, BranchMode: *flBranches, Target: targetJSON(neu.Target),
		NewM: toJSON(neu.File), BaseM: toJSON(base.File), Budgets: budgets}
	out.Delta.Instructions = neu.File.InstrCount - base.File.InstrCount
	out.Delta.CyclesMin = neu.File.CyclesMin - base.File.CyclesMin
	out.Delta.CyclesMax = neu.File.CyclesMax - base.File.CyclesMax
	out.Delta.FlashBytes = neu.File.FlashBytes() - base.File.FlashBytes()
	out.Delta.SRAMStatic = neu.SRAMStatic - base.SRAMStatic
	writeJSON(out)
}

func emitMatrixJSON(runs []analysisRun, path string) {
	type matrixEntry struct {
		Target     any         `json:"target"`
		Scope      string      `json:"scope"`
		Metrics    jsonMetrics `json:"metrics"`
		SRAMStatic int         `json:"sram_static_bytes"`
	}
	out := struct {
		File       string        `json:"file"`
		Mode       string        `json:"mode"`
		BranchMode string        `json:"branch_mode"`
		Entries    []matrixEntry `json:"entries"`
	}{
		File:       path,
		Mode:       "multi-target",
		BranchMode: *flBranches,
	}
	for _, run := range runs {
		out.Entries = append(out.Entries, matrixEntry{
			Target:     targetJSON(run.Target),
			Scope:      run.scopeLabel(),
			Metrics:    toJSON(run.scopeMetrics()),
			SRAMStatic: run.Result.SRAMStatic,
		})
	}
	writeJSON(out)
}

type sarifReport struct {
	Version string `json:"version"`
	Schema  string `json:"$schema"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name           string      `json:"name"`
				InformationURI string      `json:"informationUri,omitempty"`
				Rules          []sarifRule `json:"rules,omitempty"`
			} `json:"driver"`
		} `json:"tool"`
		Results []sarifResult `json:"results,omitempty"`
	} `json:"runs"`
}

type sarifRule struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ShortDescription struct {
		Text string `json:"text"`
	} `json:"shortDescription"`
	HelpURI string `json:"helpUri,omitempty"`
}

type sarifResult struct {
	RuleID  string `json:"ruleId"`
	Level   string `json:"level"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	Locations []struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
		} `json:"physicalLocation"`
	} `json:"locations,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

func emitSingleSARIF(run analysisRun, path string) {
	emitSARIF(buildSARIF(path, []analysisRun{run}, nil, "single"))
}

func emitMatrixSARIF(runs []analysisRun, path string) {
	emitSARIF(buildSARIF(path, runs, nil, "multi-target"))
}

func emitCompareSARIF(run analysisRun, base analyze.Result, path, baseline string) {
	props := map[string]any{
		"baseline_file":     baseline,
		"new_file":          path,
		"target":            run.Target.Name,
		"core":              run.Target.Variant.String(),
		"pc_bits":           pcBits(run.Target),
		"cycles_max_delta":  run.Result.File.CyclesMax - base.File.CyclesMax,
		"flash_bytes_delta": run.Result.File.FlashBytes() - base.File.FlashBytes(),
		"sram_static_delta": run.Result.SRAMStatic - base.SRAMStatic,
		"peak_stack_delta":  run.Result.File.PeakStackBytes - base.File.PeakStackBytes,
		"branch_mode":       *flBranches,
	}
	var extras []sarifResult
	if run.Result.File.CyclesMax > base.File.CyclesMax || run.Result.File.FlashBytes() > base.File.FlashBytes() || run.Result.SRAMStatic > base.SRAMStatic {
		extras = append(extras, sarifFinding("compare-regression", "warning",
			fmt.Sprintf("new file %s regresses cost versus %s", path, baseline), path, props))
	}
	emitSARIF(buildSARIF(path, []analysisRun{run}, extras, "compare"))
}

func buildSARIF(path string, runs []analysisRun, extras []sarifResult, mode string) sarifReport {
	report := sarifReport{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
	}
	run := struct {
		Tool struct {
			Driver struct {
				Name           string      `json:"name"`
				InformationURI string      `json:"informationUri,omitempty"`
				Rules          []sarifRule `json:"rules,omitempty"`
			} `json:"driver"`
		} `json:"tool"`
		Results []sarifResult `json:"results,omitempty"`
	}{}
	run.Tool.Driver.Name = "cyclecount"
	run.Tool.Driver.InformationURI = "https://github.com/"
	run.Tool.Driver.Rules = []sarifRule{
		sarifRuleDef("budget-exceeded", "Budget exceeded"),
		sarifRuleDef("instruction-unavailable", "Instruction unavailable on target"),
		sarifRuleDef("instruction-unmodeled", "Instruction cycles not modeled"),
		sarifRuleDef("instruction-unknown", "Instruction unknown to ISA table"),
		sarifRuleDef("compare-regression", "Comparison regression"),
	}
	for _, ar := range runs {
		run.Results = append(run.Results, sarifResultsForRun(ar, path, mode)...)
	}
	run.Results = append(run.Results, extras...)
	report.Runs = append(report.Runs, run)
	return report
}

func sarifRuleDef(id, text string) sarifRule {
	r := sarifRule{ID: id, Name: id}
	r.ShortDescription.Text = text
	return r
}

func sarifResultsForRun(run analysisRun, path, mode string) []sarifResult {
	var out []sarifResult
	for _, b := range run.Budgets {
		if !b.OK {
			out = append(out, sarifFinding("budget-exceeded", "error",
				fmt.Sprintf("%s %d%s exceeds limit %d%s (%s)", b.Name, b.Got, b.Unit, b.Limit, b.Unit, b.Scope),
				path, sarifProps(run, mode, map[string]any{
					"budget_name":  b.Name,
					"budget_scope": b.Scope,
					"got":          b.Got,
					"limit":        b.Limit,
					"unit":         b.Unit,
				})))
		}
	}
	collect := func(kind, level string, set map[string]int) {
		for name, count := range set {
			out = append(out, sarifFinding(kind, level,
				fmt.Sprintf("%s: %s appears %d time(s) in %s", kind, name, count, run.scopeLabel()),
				path, sarifProps(run, mode, map[string]any{
					"mnemonic": name,
					"count":    count,
					"scope":    run.scopeLabel(),
				})))
		}
	}
	metrics := run.scopeMetrics()
	collect("instruction-unavailable", "error", metrics.Unavailable)
	collect("instruction-unmodeled", "warning", metrics.Unmodeled)
	collect("instruction-unknown", "warning", metrics.Unknown)
	return out
}

func sarifProps(run analysisRun, mode string, extra map[string]any) map[string]any {
	props := map[string]any{
		"mode":        mode,
		"scope":       run.scopeLabel(),
		"target":      run.Target.Name,
		"core":        run.Target.Variant.String(),
		"pc_bits":     pcBits(run.Target),
		"branch_mode": *flBranches,
	}
	for k, v := range extra {
		props[k] = v
	}
	return props
}

func sarifFinding(ruleID, level, text, path string, props map[string]any) sarifResult {
	r := sarifResult{RuleID: ruleID, Level: level, Properties: props}
	r.Message.Text = text
	r.Locations = append(r.Locations, struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
		} `json:"physicalLocation"`
	}{})
	r.Locations[0].PhysicalLocation.ArtifactLocation.URI = path
	return r
}

func emitSARIF(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fail(err)
	}
}

func writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fail(err)
	}
}
