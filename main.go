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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"cyclecount/internal/analyze"
	"cyclecount/internal/asm"
	"cyclecount/internal/isa"
)

var (
	flVerbose = flag.Bool("v", false, "print a per-instruction listing with word/cycle costs")
	flJSON    = flag.Bool("json", false, "emit machine-readable JSON instead of a text report")
	flClock   = flag.Float64("clock", 20.0, "CPU clock in MHz for wall-clock time (0 disables)")
	flMCU     = flag.String("mcu", "", "target part number, e.g. attiny3217, atmega328p, avr128da48")
	flCore    = flag.String("core", "", "CPU variant: avr, avre, avre+, avrxm, avrxt, avrrc")
	flPC      = flag.Int("pc", 0, "program-counter width in bytes: 2 (16-bit) or 3 (22-bit)")
	flFrom    = flag.String("from", "", "start label for an explicit range analysis")
	flTo      = flag.String("to", "", "end label for an explicit range analysis")
	flIter    = flag.Int("iter", 1, "loop trip count applied to the -from/-to range")
	flVS      = flag.String("vs", "", "baseline file to diff the target against")
	flObjdump = flag.String("objdump", "avr-objdump", "objdump binary used for ELF/.o/.hex input")

	flMaxCycles = flag.Int("max-cycles", 0, "fail (exit 3) if worst-case cycles exceed N (0 disables)")
	flMaxFlash  = flag.Int("max-flash", 0, "fail (exit 3) if flash bytes exceed N (0 disables)")
	flMaxSRAM   = flag.Int("max-sram", 0, "fail (exit 3) if static SRAM bytes exceed N (0 disables)")

	flCPP = flag.Bool("cpp", false, "run the C preprocessor (#include/#define/#if) over .S/.s source before parsing")
	flCC  = flag.String("cc", "avr-gcc", "compiler driver used for -cpp")

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
    cyclecount -mcu attiny3217 -max-cycles 40 -from loop -to done hot.S
    cyclecount -mcu atmega328p -cpp -D F_CPU=16000000 blink.S
`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "cyclecount:", err)
	os.Exit(1)
}

func resolveTarget() (analyze.Target, error) {
	t := analyze.Target{Name: "ATtiny3217", Variant: isa.VarAVRxt, PCBytes: 2}
	if *flMCU != "" {
		d, ok := isa.LookupDevice(*flMCU)
		if !ok {
			return t, fmt.Errorf("unknown device %q — pass -core {avr|avre|avre+|avrxm|avrxt|avrrc} (and -pc 3 for >128 KB parts) instead", *flMCU)
		}
		t = analyze.Target{Name: d.Name, Variant: d.Variant, PCBytes: d.PCBytes}
	}
	if *flCore != "" {
		v, ok := isa.ParseVariant(*flCore)
		if !ok {
			return t, fmt.Errorf("unknown core %q (use avr, avre, avre+, avrxm, avrxt, or avrrc)", *flCore)
		}
		t.Variant = v
		if *flMCU == "" {
			t.Name = v.String()
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

func pcBits(t analyze.Target) int {
	if t.PCBytes >= 3 {
		return 22
	}
	return 16
}

func main() {
	flag.Usage = usage
	flag.Var(&flDefine, "D", "preprocessor define NAME[=VAL] for -cpp (repeatable)")
	flag.Var(&flInclude, "I", "preprocessor include directory for -cpp (repeatable)")
	flag.Parse()
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

	target, err := resolveTarget()
	if err != nil {
		fail(err)
	}

	lines, err := load(path, *flObjdump)
	if err != nil {
		fail(err)
	}
	res := analyze.Analyze(lines, target)

	var rng *analyze.Metrics
	if *flFrom != "" || *flTo != "" {
		if *flFrom == "" || *flTo == "" {
			fail(errors.New("-from and -to must be supplied together"))
		}
		m, err := analyze.RangeMetrics(lines, *flFrom, *flTo, *flIter, target)
		if err != nil {
			fail(err)
		}
		rng = &m
	}

	// Budgets gate the target file's cost; evaluate them before the -vs return
	// so a CI run that combines -vs with -max-* still fails on the exit status.
	checks := evalBudgets(budgetsFromFlags(), res, rng)

	if *flVS != "" {
		blines, err := load(*flVS, *flObjdump)
		if err != nil {
			fail(err)
		}
		bres := analyze.Analyze(blines, target)
		if *flJSON {
			emitCompareJSON(res, bres, path, *flVS, checks)
		} else {
			renderCompare(os.Stdout, res, bres, path, *flVS, *flClock)
			renderBudgets(os.Stdout, checks)
		}
		if budgetsExceeded(checks) {
			os.Exit(3)
		}
		return
	}

	if *flJSON {
		emitJSON(res, rng, path, checks)
	} else {
		renderReport(os.Stdout, res, rng, path, *flClock, *flVerbose)
		renderBudgets(os.Stdout, checks)
	}
	if budgetsExceeded(checks) {
		os.Exit(3)
	}
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
			fmt.Fprintf(w, ", %d call(s) (+%d B return address while nested)", m.Calls, m.CallBytes)
		}
		fmt.Fprintln(w)
	}

	warnSet(w, "✘ unavailable ", m.Unavailable, fmt.Sprintf("not implemented on %s — will not assemble", t.Variant))
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
			case !isa.Available(lm.Line.Mnemonic, t.Variant, t.PCBytes):
				cyc, note = "n/a", "not on "+t.Variant.String()
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
func evalBudgets(b budgets, res analyze.Result, rng *analyze.Metrics) []budgetCheck {
	if !b.any() {
		return nil
	}
	var out []budgetCheck
	if b.cycles > 0 {
		m, scope := res.File, "whole file"
		if rng != nil {
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
		PeakStackBytes: m.PeakStackBytes,
		Pushes:         m.Pushes, Pops: m.Pops, Calls: m.Calls,
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

func emitJSON(res analyze.Result, rng *analyze.Metrics, path string, budgets []budgetCheck) {
	out := struct {
		File       string        `json:"file"`
		Target     any           `json:"target"`
		WholeFile  jsonMetrics   `json:"whole_file"`
		Regions    []jsonMetrics `json:"regions,omitempty"`
		Range      *jsonMetrics  `json:"range,omitempty"`
		SRAMStatic int           `json:"sram_static_bytes"`
		Budgets    []budgetCheck `json:"budgets,omitempty"`
	}{
		File: path, Target: targetJSON(res.Target),
		WholeFile: toJSON(res.File), SRAMStatic: res.SRAMStatic,
		Budgets: budgets,
	}
	for _, r := range res.Regions {
		out.Regions = append(out.Regions, toJSON(r))
	}
	if rng != nil {
		j := toJSON(*rng)
		out.Range = &j
	}
	writeJSON(out)
}

func emitCompareJSON(neu, base analyze.Result, np, bp string, budgets []budgetCheck) {
	out := struct {
		New      string      `json:"new_file"`
		Baseline string      `json:"baseline_file"`
		Target   any         `json:"target"`
		NewM     jsonMetrics `json:"new"`
		BaseM    jsonMetrics `json:"baseline"`
		Delta    struct {
			Instructions int `json:"instructions"`
			CyclesMin    int `json:"cycles_min"`
			CyclesMax    int `json:"cycles_max"`
			FlashBytes   int `json:"flash_bytes"`
			SRAMStatic   int `json:"sram_static_bytes"`
		} `json:"delta"`
		Budgets []budgetCheck `json:"budgets,omitempty"`
	}{New: np, Baseline: bp, Target: targetJSON(neu.Target),
		NewM: toJSON(neu.File), BaseM: toJSON(base.File), Budgets: budgets}
	out.Delta.Instructions = neu.File.InstrCount - base.File.InstrCount
	out.Delta.CyclesMin = neu.File.CyclesMin - base.File.CyclesMin
	out.Delta.CyclesMax = neu.File.CyclesMax - base.File.CyclesMax
	out.Delta.FlashBytes = neu.File.FlashBytes() - base.File.FlashBytes()
	out.Delta.SRAMStatic = neu.SRAMStatic - base.SRAMStatic
	writeJSON(out)
}

func writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fail(err)
	}
}
