package isa

//go:generate go run ../../cmd/gen-avr-device-db -out device_gen.go

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Device maps a part number to its CPU variant, Program Counter width, flash
// size, and any device-specific missing instructions listed in Appendix A of
// the AVR Instruction Set Manual (DS40002198C §7.2).
//
// PCBytes is 3 for parts with more than 128 KB flash (22-bit PC) and 2
// otherwise (16-bit PC). FlashKB is the program-memory size in KiB. FlashKB ==
// 0 means "unknown" (e.g. a target specified only by -core).
type Device struct {
	Name    string
	Variant Variant
	PCBytes int
	FlashKB int
	Missing MissingSet
}

type deviceFamily struct {
	re      *regexp.Regexp
	variant Variant
	pcBytes int
	flash   func(name string) int // flash size in KiB for a matched part
	missing func(name string) MissingSet
}

// firstNum returns the first run of digits in s as an int (0 if none). For the
// modern AVR-Dx and XMEGA families the leading number is the flash size in KiB
// (AVR128DA48 → 128, ATxmega32A4U → 32).
func firstNum(s string) int {
	n, _ := strconv.Atoi(numRe.FindString(s))
	return n
}

var numRe = regexp.MustCompile(`\d+`)

// kb returns a constant flash-size function, for families whose alternation
// members all share one flash size.
func kb(n int) func(string) int { return func(string) int { return n } }

func noneMissing(string) MissingSet { return nil }

func exactMissing(mnemonics ...string) func(string) MissingSet {
	set := NewMissingSet(mnemonics...)
	return func(string) MissingSet { return set.Clone() }
}

func missingByFlash(kbToMissing map[int]MissingSet) func(string) MissingSet {
	return func(name string) MissingSet {
		if set, ok := kbToMissing[firstNum(name)]; ok {
			return set.Clone()
		}
		return nil
	}
}

var (
	missElpmEijmpEicall = NewMissingSet("ELPM", "EIJMP", "EICALL")
	missEijmpEicall     = NewMissingSet("EIJMP", "EICALL")
)

// familyFallbacks cover common AVR naming families so routine suffix variants
// do not all need hand-curated exact entries. More specific rules go first.
// Each entry is flash-homogeneous (constant kb) or derives flash from the part
// number (firstNum), and may also derive device-specific missing instructions.
var familyFallbacks = []deviceFamily{
	// AVR SD (functional safety) parts keep ELPM at every flash size: Appendix A
	// lists only EIJMP/EICALL as missing (DS40002198C Table 7-4).
	{regexp.MustCompile(`^AVR\d+SD\d*$`), VarAVRxt, 2, firstNum, exactMissing("EIJMP", "EICALL")},

	// Modern AVR Dx/Ex/Du naming (≥16 KB), 16-bit PC / AVRxt core. The
	// leading number is the flash size in KiB.
	{regexp.MustCompile(`^AVR\d+(DA|DB|DD|DU|EA|EB)\d*$`), VarAVRxt, 2, firstNum,
		missingByFlash(map[int]MissingSet{
			16:  missElpmEijmpEicall,
			32:  missElpmEijmpEicall,
			64:  missElpmEijmpEicall,
			128: missEijmpEicall,
		})},

	// XMEGA family fallbacks stay conservative: exact curated entries below carry
	// the appendix-derived missing-instruction overrides.
	{regexp.MustCompile(`^ATXMEGA(16|32|64|128)[A-Z0-9]*$`), VarAVRxm, 2, firstNum, noneMissing},
	{regexp.MustCompile(`^ATXMEGA(192|256|384)[A-Z0-9]*$`), VarAVRxm, 3, firstNum, noneMissing},

	// Common classic megaAVR suffix variants.
	{regexp.MustCompile(`^ATMEGA48(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(4), exactMissing("CALL", "JMP", "ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA88(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(8), exactMissing("CALL", "JMP", "ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA168(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(16), exactMissing("ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA328(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(32), exactMissing("ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA164(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(16), exactMissing("ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA324(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(32), exactMissing("ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA644(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(64), exactMissing("ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA1284(A|P|PA|PB)?$`), VarAVRePlus, 2, kb(128), exactMissing("EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA64(A)?$`), VarAVRePlus, 2, kb(64), exactMissing("ELPM", "EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA128(A)?$`), VarAVRePlus, 2, kb(128), exactMissing("EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA(1280|1281)$`), VarAVRePlus, 2, kb(128), exactMissing("EIJMP", "EICALL")},
	{regexp.MustCompile(`^ATMEGA(2560|2561)$`), VarAVRePlus, 3, kb(256), noneMissing},

	// Common classic tinyAVR suffix variants.
	{regexp.MustCompile(`^ATTINY13(A)?$`), VarAVRe, 2, kb(1), exactMissing("CALL", "JMP", "ELPM")},
	{regexp.MustCompile(`^ATTINY(24|25)(A)?$`), VarAVRe, 2, kb(2), exactMissing("CALL", "JMP", "ELPM")},
	{regexp.MustCompile(`^ATTINY(44|45)(A)?$`), VarAVRe, 2, kb(4), exactMissing("CALL", "JMP", "ELPM")},
	{regexp.MustCompile(`^ATTINY(84|85)(A)?$`), VarAVRe, 2, kb(8), exactMissing("CALL", "JMP", "ELPM")},
	{regexp.MustCompile(`^ATTINY2313(A)?$`), VarAVRe, 2, kb(2), exactMissing("CALL", "JMP", "ELPM")},
	{regexp.MustCompile(`^ATTINY4313$`), VarAVRe, 2, kb(4), exactMissing("CALL", "JMP", "ELPM")},
	{regexp.MustCompile(`^ATTINY167(A)?$`), VarAVRe, 2, kb(16), noneMissing},
	{regexp.MustCompile(`^ATTINY1634(A)?$`), VarAVRe, 2, kb(16), noneMissing},

	// Reduced-core tinyAVR family: the core omits many instructions, and most
	// devices also miss BREAK except ATtiny40.
	{regexp.MustCompile(`^ATTINY(4|5|9|10|102|104)$`), VarAVRrc, 2, kb(1), exactMissing("BREAK")},
	{regexp.MustCompile(`^ATTINY20$`), VarAVRrc, 2, kb(2), exactMissing("BREAK")},
	{regexp.MustCompile(`^ATTINY40$`), VarAVRrc, 2, kb(4), noneMissing},
}

// devices is the exact-device database generated from Appendix A of the
// instruction-set manual. Family fallbacks below still cover common suffix
// variants that are convenient to resolve without enumerating every package.
var devices = generatedDevices

// Devices returns the curated part-number table sorted by name. It is not
// exhaustive — parts matched only by a family fallback are not listed here.
func Devices() []Device {
	out := make([]Device, 0, len(devices))
	for _, d := range devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Family describes one naming-pattern fallback used when a part has no exact
// curated entry.
type Family struct {
	Pattern string // the regular expression matched against the upper-cased part
	Variant Variant
	PCBytes int
}

// Families returns the family-pattern fallbacks, most specific first (the order
// they are tried in).
func Families() []Family {
	out := make([]Family, 0, len(familyFallbacks))
	for _, f := range familyFallbacks {
		out = append(out, Family{Pattern: f.re.String(), Variant: f.variant, PCBytes: f.pcBytes})
	}
	return out
}

// LookupDevice resolves a part number (case-insensitive) to its Device entry.
func LookupDevice(name string) (Device, bool) {
	N := strings.ToUpper(strings.TrimSpace(name))
	if d, ok := devices[N]; ok {
		return d, true
	}
	for _, f := range familyFallbacks {
		if f.re.MatchString(N) {
			return Device{Name: N, Variant: f.variant, PCBytes: f.pcBytes, FlashKB: f.flash(N), Missing: f.missing(N)}, true
		}
	}
	return Device{}, false
}
