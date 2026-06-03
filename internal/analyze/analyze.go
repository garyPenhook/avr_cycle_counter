// Package analyze turns parsed assembly into cycle, flash, and SRAM metrics
// for a chosen AVR target (CPU variant + Program Counter width).
package analyze

import (
	"fmt"
	"strings"

	"cyclecount/internal/asm"
	"cyclecount/internal/isa"
)

// Target is the AVR device being analyzed.
type Target struct {
	Name    string // display name (device part number or core)
	Variant isa.Variant
	PCBytes int // 2 (16-bit PC) or 3 (22-bit PC)
}

// LineMetric pairs a source instruction line with its ISA timing info.
type LineMetric struct {
	Line  *asm.Line
	Info  isa.Info
	Known bool
}

// Metrics is the cost of a span of code (whole file, a region, or a range).
type Metrics struct {
	Name           string
	Iter           int
	InstrCount     int
	FlashWords     int
	FlashDataBytes int
	CyclesMin      int
	CyclesMax      int
	PeakStackBytes int
	Pushes         int
	Pops           int
	Calls          int
	CallBytes      int // PC width: stack cost of each call's return address
	Hist           map[string]int
	Unknown        map[string]int // not in the ISA table at all
	Unavailable    map[string]int // exist on other variants, not this target
	Unmodeled      map[string]int // recognized but not cycle-counted (e.g. SPM)
	Lines          []LineMetric
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

func computeMetrics(name string, iter int, lines []*asm.Line, t Target) Metrics {
	m := Metrics{Name: name, Iter: iter, CallBytes: t.PCBytes,
		Hist: map[string]int{}, Unknown: map[string]int{},
		Unavailable: map[string]int{}, Unmodeled: map[string]int{}}
	running, peak := 0, 0
	for _, ln := range lines {
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
		if !isa.Available(ln.Mnemonic, t.Variant, t.PCBytes) {
			m.Unavailable[info.Mnemonic]++
			continue
		}
		// Available on this target: count it toward instructions and flash.
		m.InstrCount++
		m.FlashWords += info.WordCount(t.Variant)
		m.Hist[info.Mnemonic]++

		if cc, ok := info.Cycles(t.Variant, t.PCBytes); ok {
			m.CyclesMin += cc.Min
			m.CyclesMax += cc.Max
		} else {
			m.Unmodeled[info.Mnemonic]++ // e.g. SPM: programming-time dependent
		}

		switch info.Mnemonic {
		case "PUSH":
			running++
			if running > peak {
				peak = running
			}
			m.Pushes++
		case "POP":
			running--
			m.Pops++
		case "CALL", "RCALL", "ICALL", "EICALL":
			m.Calls++
		}
	}
	m.PeakStackBytes = peak
	return m
}

// Analyze produces whole-file metrics, static SRAM totals, and per-region
// metrics for every @begin/@end span, all for the given target.
func Analyze(lines []*asm.Line, t Target) Result {
	res := Result{Target: t, Sections: map[string]int{}}
	res.File = computeMetrics("(whole file)", 1, lines, t)
	for _, ln := range lines {
		if ln.Directive == "" {
			continue
		}
		if b, ok := asm.DataBytes(ln.Directive, ln.DirectiveArgs); ok && isDataSection(ln.Section) {
			res.SRAMStatic += b
			res.Sections[ln.Section] += b
		}
	}
	res.Regions = extractRegions(lines, t)
	return res
}

type openRegion struct {
	name  string
	iter  int
	start int
}

func extractRegions(lines []*asm.Line, t Target) []Metrics {
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
				out = append(out, computeMetrics(or.name, or.iter, sub, t))
				break
			}
		}
	}
	return out
}

// RangeMetrics analyzes the inclusive span between two labels.
func RangeMetrics(lines []*asm.Line, from, to string, iter int, t Target) (Metrics, error) {
	fi, ti := -1, -1
	for i, ln := range lines {
		if ln.Label == from && fi < 0 {
			fi = i
		}
		if ln.Label == to {
			ti = i
		}
	}
	if fi < 0 {
		return Metrics{}, fmt.Errorf("start label %q not found", from)
	}
	if ti < 0 {
		return Metrics{}, fmt.Errorf("end label %q not found", to)
	}
	if ti < fi {
		fi, ti = ti, fi
	}
	return computeMetrics(from+":"+to, iter, lines[fi:ti+1], t), nil
}
