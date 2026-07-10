// Package isa holds AVR instruction timing, size, and availability data for
// every AVR CPU variant, taken from the Microchip AVR Instruction Set Manual
// (DS40002198C): the #Clocks columns of the Instruction Set Summary
// (Tables 5-2 … 5-6) and the Core Descriptions / device tables in Appendix A.
//
// The manual documents six variants. Three of them — AVR, AVRe and AVRe+ —
// share identical timing and differ only in which instructions exist, so the
// timing table carries four cycle columns (AVRe, AVRxm, AVRxt, AVRrc) while
// availability is tracked per variant.
//
// Call/return timing and stack usage depend on the Program Counter width:
// devices with ≤128 KB flash use a 16-bit PC (2-byte return address); larger
// parts use a 22-bit PC (3-byte return address), which the manual notes adds
// one cycle to CALL/RCALL/ICALL/RET/RETI. (EICALL exists only on extended-PC
// parts and has a single fixed cycle count, so it is not in that +1 set.)
package isa

import "strings"

// Variant is one of the six documented AVR CPU flavors.
type Variant int

const (
	VarAVR      Variant = iota // original core (AT90S, first shipped 1997)
	VarAVRe                    // + MOVW, enhanced LPM
	VarAVRePlus                // + multiply / extended-range
	VarAVRxm                   // XMEGA
	VarAVRxt                   // tinyAVR 0/1/2, megaAVR 0, AVR Dx/Ex/Du
	VarAVRrc                   // Reduced Core (16-register)
)

var variantNames = map[string]Variant{
	"avr": VarAVR, "avre": VarAVRe,
	"avre+": VarAVRePlus, "avrep": VarAVRePlus, "avreplus": VarAVRePlus,
	"avrxm": VarAVRxm, "xmega": VarAVRxm,
	"avrxt": VarAVRxt,
	"avrrc": VarAVRrc, "reduced": VarAVRrc,
}

var variantLabel = map[Variant]string{
	VarAVR: "AVR", VarAVRe: "AVRe", VarAVRePlus: "AVRe+",
	VarAVRxm: "AVRxm", VarAVRxt: "AVRxt", VarAVRrc: "AVRrc",
}

func (v Variant) String() string { return variantLabel[v] }

// MissingSet is the device-specific set of instructions Appendix A lists as
// absent on a resolved part.
type MissingSet map[string]bool

// NewMissingSet builds a case-normalized missing-instruction set.
func NewMissingSet(mnemonics ...string) MissingSet {
	if len(mnemonics) == 0 {
		return nil
	}
	set := make(MissingSet, len(mnemonics))
	for _, mn := range mnemonics {
		set[strings.ToUpper(strings.TrimSpace(mn))] = true
	}
	return set
}

// Clone returns a copy suitable for attaching to a specific Device/Target.
func (m MissingSet) Clone() MissingSet {
	if len(m) == 0 {
		return nil
	}
	out := make(MissingSet, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ParseVariant resolves a core name (case-insensitive) to a Variant.
func ParseVariant(s string) (Variant, bool) {
	v, ok := variantNames[strings.ToLower(strings.TrimSpace(s))]
	return v, ok
}

// CC is a cycle count: a Min–Max range, or N/A on this variant.
type CC struct {
	Min, Max int
	NA       bool
}

func c1(n int) CC    { return CC{Min: n, Max: n} }
func cr(a, b int) CC { return CC{Min: a, Max: b} }
func na() CC         { return CC{NA: true} }

type cyc struct{ e, xm, xt, rc CC } // AVRe / AVRxm / AVRxt / AVRrc columns

// Info describes one mnemonic across all variants.
type Info struct {
	Mnemonic string
	Words    int    // program-memory words (1 word = 2 bytes of flash)
	WordsRC  int    // word override for AVRrc (0 = same as Words)
	t        cyc    // per-variant cycle counts
	CallLike bool   // +1 cycle and uses PC width for stack on 22-bit-PC parts
	Special  string // non-empty: recognized but not cycle-modeled (data/NVM dependent)
	Note     string
}

var table = map[string]Info{}

func add(words int, e, xm, xt, rc CC, note string, mnemonics ...string) {
	for _, m := range mnemonics {
		M := strings.ToUpper(m)
		table[M] = Info{Mnemonic: M, Words: words, t: cyc{e, xm, xt, rc}, Note: note}
	}
}

func init() {
	one := c1(1)

	// --- Arithmetic / logic (Table 5-2) -------------------------------
	add(1, one, one, one, one, "",
		"ADD", "ADC", "SUB", "SUBI", "SBC", "SBCI", "AND", "ANDI",
		"OR", "ORI", "EOR", "COM", "NEG", "SBR", "CBR", "INC", "DEC",
		"TST", "CLR", "SER", "CP", "CPC", "CPI", "MOV")
	add(1, c1(2), c1(2), c1(2), na(), "", "ADIW", "SBIW")
	add(1, c1(2), c1(2), c1(2), na(), "hardware multiplier",
		"MUL", "MULS", "MULSU", "FMUL", "FMULS", "FMULSU")

	// --- Bit / bit-test (Table 5-5) -----------------------------------
	add(1, one, one, one, one, "",
		"LSL", "LSR", "ROL", "ROR", "ASR", "SWAP", "BST", "BLD", "BSET", "BCLR",
		"SEC", "CLC", "SEN", "CLN", "SEZ", "CLZ", "SEI", "CLI",
		"SES", "CLS", "SEV", "CLV", "SET", "CLT", "SEH", "CLH")
	add(1, c1(2), c1(1), c1(1), c1(1), "AVRxt/xm: 1 cycle (2 on AVRe)", "SBI", "CBI")

	// --- Data transfer (Table 5-4) ------------------------------------
	add(1, one, one, one, na(), "", "MOVW")
	add(1, one, one, one, one, "", "LDI", "IN", "OUT")
	addRC(2, 1, c1(2), c1(3), c1(3), c1(2), "+1 cycle for NVM access", "LDS")
	add(1, c1(2), cr(2, 3), c1(2), cr(1, 3), "timing varies by addressing mode", "LD")
	add(1, c1(2), c1(3), c1(2), na(), "", "LDD")
	add(1, c1(2), cr(1, 2), c1(1), cr(1, 2), "AVRxt/xm: faster than AVRe", "ST")
	add(1, c1(2), c1(2), c1(1), na(), "AVRxt: 1 cycle (2 on AVRe)", "STD")
	addRC(2, 1, c1(2), c1(2), c1(2), c1(1), "+1 cycle for NVM access", "STS")
	add(1, c1(3), c1(3), c1(3), na(), "", "LPM")
	add(1, c1(3), c1(3), c1(3), na(), "extended program-memory load", "ELPM")
	add(1, c1(2), c1(1), c1(1), c1(1), "AVRxt/xm: 1 cycle (2 on AVRe)", "PUSH")
	add(1, c1(2), c1(2), c1(2), c1(3), "", "POP")

	// --- Change of flow (Table 5-3); call/ret are CallLike -------------
	add(1, c1(2), c1(2), c1(2), c1(2), "", "RJMP", "IJMP")
	add(1, c1(2), c1(2), c1(2), na(), "", "EIJMP")
	add(2, c1(3), c1(3), c1(3), na(), "", "JMP")
	addCall(2, c1(4), c1(3), c1(3), na(), "CALL")
	addCall(1, c1(3), c1(2), c1(2), c1(3), "RCALL", "ICALL")
	// EICALL exists only on extended-PC (>128 KB) parts; the manual gives a
	// single fixed cycle count (Table 6-51: AVRe 4, AVRxm 3, AVRxt 3) with no
	// 16-/22-bit split, so it is NOT CallLike — the +1 is already baked in.
	add(1, c1(4), c1(3), c1(3), na(), "extended indirect call", "EICALL")
	addCall(1, c1(4), c1(4), c1(4), c1(6), "RET", "RETI")
	add(1, cr(1, 3), cr(1, 3), cr(1, 3), cr(1, 2),
		"skip: 1 no-skip / 2 skip 1-word / 3 skip 2-word",
		"CPSE", "SBRC", "SBRS")
	add(1, cr(1, 3), cr(2, 4), cr(1, 3), cr(1, 2),
		"skip: depends on the skipped instruction", "SBIC", "SBIS")
	add(1, cr(1, 2), cr(1, 2), cr(1, 2), cr(1, 2),
		"1 if condition false, 2 if branch taken",
		"BRBS", "BRBC", "BREQ", "BRNE", "BRCS", "BRCC", "BRSH", "BRLO",
		"BRMI", "BRPL", "BRGE", "BRLT", "BRHS", "BRHC", "BRTS", "BRTC",
		"BRVS", "BRVC", "BRIE", "BRID")

	// --- MCU control (Table 5-6) --------------------------------------
	add(1, one, one, one, one, "", "NOP", "SLEEP", "WDR", "BREAK")

	// --- Recognized but not cycle-modeled (NVM-timing dependent) -----------
	addSpecial(1, "programming-time dependent", "SPM")
	add(1, na(), c1(2), na(), na(), "AVRxm read-modify-write", "XCH", "LAC", "LAS", "LAT")
	add(1, na(), cr(1, 2), na(), na(), "AVRxm DES round: 1 cycle after DES, otherwise 2", "DES")
}

func addRC(words, wordsRC int, e, xm, xt, rc CC, note string, mnemonics ...string) {
	for _, m := range mnemonics {
		M := strings.ToUpper(m)
		table[M] = Info{Mnemonic: M, Words: words, WordsRC: wordsRC,
			t: cyc{e, xm, xt, rc}, Note: note}
	}
}

func addCall(words int, e, xm, xt, rc CC, mnemonics ...string) {
	for _, m := range mnemonics {
		M := strings.ToUpper(m)
		table[M] = Info{Mnemonic: M, Words: words, t: cyc{e, xm, xt, rc},
			CallLike: true, Note: "16-bit PC; +1 cycle on 22-bit-PC parts"}
	}
}

func addSpecial(words int, why string, mnemonics ...string) {
	for _, m := range mnemonics {
		M := strings.ToUpper(m)
		table[M] = Info{Mnemonic: M, Words: words,
			t: cyc{na(), na(), na(), na()}, Special: why}
	}
}

// Lookup returns timing/size info for a mnemonic (case-insensitive).
func Lookup(mnemonic string) (Info, bool) {
	i, ok := table[strings.ToUpper(strings.TrimSpace(mnemonic))]
	return i, ok
}

// All returns the complete instruction table.
func All() map[string]Info { return table }

// WordCount returns the flash words this instruction occupies on a variant.
func (i Info) WordCount(v Variant) int {
	if v == VarAVRrc && i.WordsRC > 0 {
		return i.WordsRC
	}
	return i.Words
}

// Cycles returns the cycle count for a variant and PC width (2 or 3 bytes).
// ok is false when the instruction does not exist (or isn't cycle-modeled).
func (i Info) Cycles(v Variant, pcBytes int) (CC, bool) {
	if i.Special != "" {
		return CC{}, false
	}
	var cc CC
	switch v {
	case VarAVRxm:
		cc = i.t.xm
	case VarAVRxt:
		cc = i.t.xt
	case VarAVRrc:
		cc = i.t.rc
	default: // AVR, AVRe, AVRe+ share timing
		cc = i.t.e
	}
	if cc.NA {
		return CC{}, false
	}
	if i.CallLike && pcBytes >= 3 {
		cc.Min++
		cc.Max++
	}
	return cc, true
}

// --- Availability (Appendix A, Table 7-1 Core Descriptions) -------------
//
// Only instructions whose presence varies are listed; anything absent here is
// available on every variant. Each entry maps the mnemonic to the set of
// variants that include it.

var availability = map[string]map[Variant]bool{}

func avail(mnemonics []string, variants ...Variant) {
	set := map[Variant]bool{}
	for _, v := range variants {
		set[v] = true
	}
	for _, m := range mnemonics {
		availability[strings.ToUpper(m)] = set
	}
}

func init() {
	// Present on AVR..AVRxt but not the Reduced Core. ADIW/SBIW also save a
	// flash word over SUBI+SBCI but cost the same 2 cycles (size, not speed):
	avail([]string{"ADIW", "SBIW", "LDD", "STD", "LPM"},
		VarAVR, VarAVRe, VarAVRePlus, VarAVRxm, VarAVRxt)
	// MOVW/SPM are enhanced-core features (AVRe upward, not original AVR/rc).
	// Device-specific omissions such as CALL/JMP/ELPM on some parts are handled
	// by Appendix-A overlays via AvailableOnTarget, not by this core-level table.
	avail([]string{"MOVW", "SPM"},
		VarAVRe, VarAVRePlus, VarAVRxm, VarAVRxt)
	// SPM Z+ (auto-increment self-programming) is present only on AVRxm/AVRxt
	// (DS40002198C Table 7-1, p.134), unlike bare SPM which is also on
	// AVRe/AVRe+. Modeled as a form-qualified entry so AvailableOnTargetForm
	// distinguishes "spm" from "spm Z+".
	avail([]string{"SPM Z+"}, VarAVRxm, VarAVRxt)
	// The operandless LPM exists on the original AVR core, but the enhanced
	// destination-register forms begin with AVRe.
	avail([]string{"LPM Z", "LPM Z+"},
		VarAVRe, VarAVRePlus, VarAVRxm, VarAVRxt)
	// CALL/JMP are absent on both the original AVR core and the Reduced Core
	// (Table 7-1 Core Description: present only on AVRe/AVRe+/AVRxm/AVRxt).
	// Device-specific omissions on enhanced parts are layered on via Appendix A.
	avail([]string{"CALL", "JMP"},
		VarAVRe, VarAVRePlus, VarAVRxm, VarAVRxt)
	avail([]string{"BREAK"}, VarAVRe, VarAVRePlus, VarAVRxm, VarAVRxt, VarAVRrc)
	// Multiply / extended-range / ELPM: AVRe+ upward:
	avail([]string{"MUL", "MULS", "MULSU", "FMUL", "FMULS", "FMULSU", "ELPM"},
		VarAVRePlus, VarAVRxm, VarAVRxt)
	// EICALL/EIJMP are extended-range instructions and only apply to targets
	// with an extended program counter.
	avail([]string{"EICALL", "EIJMP"}, VarAVRePlus, VarAVRxm)
	// XMEGA-only:
	avail([]string{"DES", "XCH", "LAC", "LAS", "LAT"}, VarAVRxm)
}

// Available reports whether a mnemonic exists on the given core variant and PC
// width. It intentionally does not model device-specific omissions from
// Appendix A; use AvailableOnTarget when a concrete MCU has been resolved.
func Available(mnemonic string, v Variant, pcBytes, flashKB int) bool {
	M := strings.ToUpper(strings.TrimSpace(mnemonic))
	if M == "EICALL" || M == "EIJMP" {
		if v == VarAVRxt || v == VarAVRePlus || v == VarAVRxm {
			return pcBytes >= 3
		}
	}
	set, ok := availability[M]
	if !ok {
		return true // present on all variants
	}
	return set[v]
}

// AvailableOnTarget overlays the generic core-level availability with the
// device-specific missing-instruction table from Appendix A. When no device is
// resolved (for example, -core without -mcu), missing may be nil and the
// result falls back to Available.
func AvailableOnTarget(mnemonic string, v Variant, pcBytes, flashKB int, missing MissingSet) bool {
	M := strings.ToUpper(strings.TrimSpace(mnemonic))
	if missing != nil && missing[M] {
		return false
	}
	return Available(M, v, pcBytes, flashKB)
}

// AvailableOnTargetForm is like AvailableOnTarget, but also considers
// operand-qualified missing-instruction entries from Appendix A such as
// "LD X" or "SPM Z+".
func AvailableOnTargetForm(mnemonic, operands string, v Variant, pcBytes, flashKB int, missing MissingSet) bool {
	M := strings.ToUpper(strings.TrimSpace(mnemonic))
	form := formKey(M, operands)
	if missing != nil {
		if missing[M] || missing[form] {
			return false
		}
	}
	if form != "" {
		if _, ok := availability[form]; ok {
			return Available(form, v, pcBytes, flashKB)
		}
	}
	return Available(M, v, pcBytes, flashKB)
}

func formKey(mnemonic, operands string) string {
	ops := strings.ToUpper(strings.TrimSpace(operands))
	switch mnemonic {
	case "LD":
		if _, src, ok := strings.Cut(ops, ","); ok {
			return mnemonic + " " + strings.TrimSpace(src)
		}
	case "LPM", "ELPM":
		if _, src, ok := strings.Cut(ops, ","); ok {
			return mnemonic + " " + strings.TrimSpace(src)
		}
		if ops != "" {
			return mnemonic + " " + strings.Fields(ops)[0]
		}
	case "SPM":
		if ops != "" {
			return mnemonic + " " + strings.Fields(ops)[0]
		}
	}
	return ""
}
