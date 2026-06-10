package isa

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
	missBreakOnly           = NewMissingSet("BREAK")
	missCallJmpElpm         = NewMissingSet("CALL", "JMP", "ELPM")
	missElpmEijmpEicall     = NewMissingSet("ELPM", "EIJMP", "EICALL")
	missEijmpEicall         = NewMissingSet("EIJMP", "EICALL")
	missCallJmpElpmEijEic   = NewMissingSet("CALL", "JMP", "ELPM", "EIJMP", "EICALL")
	missModernXTCallGroup   = NewMissingSet("CALL", "JMP", "ELPM", "SPM", "SPM Z+", "EIJMP", "EICALL")
	missModernXTNoCallGroup = NewMissingSet("ELPM", "SPM", "SPM Z+", "EIJMP", "EICALL")
	missTiny11Family        = NewMissingSet("BREAK", "LPM", "LPM Z+", "ADIW", "SBIW", "IJMP", "ICALL", "LD X", "LD Y", "LD -Z", "LD Z+", "LD")
	missXmegaRMW            = NewMissingSet("LAC", "LAT", "LAS", "XCH")
)

// familyFallbacks cover common AVR naming families so routine suffix variants
// do not all need hand-curated exact entries. More specific rules go first.
// Each entry is flash-homogeneous (constant kb) or derives flash from the part
// number (firstNum), and may also derive device-specific missing instructions.
var familyFallbacks = []deviceFamily{
	// Modern AVR Dx/Ex/Du/Sd naming (≥16 KB), 16-bit PC / AVRxt core. The
	// leading number is the flash size in KiB.
	{regexp.MustCompile(`^AVR\d+(DA|DB|DD|DU|EA|EB|SD)\d*$`), VarAVRxt, 2, firstNum,
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

// devices is a curated lookup seeded from the manual's device tables. It is not
// exhaustive — any AVR can still be analyzed by passing -core (and -pc for
// >128 KB parts) directly.
var devices = map[string]Device{}

func reg(v Variant, pc, flashKB int, names ...string) {
	regMissing(v, pc, flashKB, nil, names...)
}

func regMissing(v Variant, pc, flashKB int, missing MissingSet, names ...string) {
	for _, n := range names {
		N := strings.ToUpper(n)
		devices[N] = Device{Name: N, Variant: v, PCBytes: pc, FlashKB: flashKB, Missing: missing.Clone()}
	}
}

func init() {
	// tinyAVR 0/1/2-series — AVRxt, 16-bit PC. Flash = leading number (KiB).
	regMissing(VarAVRxt, 2, 2, missModernXTCallGroup,
		"ATtiny202", "ATtiny204", "ATtiny212", "ATtiny214")
	regMissing(VarAVRxt, 2, 4, missModernXTCallGroup,
		"ATtiny402", "ATtiny404", "ATtiny406", "ATtiny412", "ATtiny414",
		"ATtiny416", "ATtiny417", "ATtiny424", "ATtiny426", "ATtiny427")
	regMissing(VarAVRxt, 2, 8, missModernXTCallGroup,
		"ATtiny804", "ATtiny806", "ATtiny807", "ATtiny814", "ATtiny816", "ATtiny817",
		"ATtiny824", "ATtiny826", "ATtiny827")
	regMissing(VarAVRxt, 2, 16, missModernXTNoCallGroup,
		"ATtiny1604", "ATtiny1606", "ATtiny1607",
		"ATtiny1614", "ATtiny1616", "ATtiny1617",
		"ATtiny1624", "ATtiny1626", "ATtiny1627")
	regMissing(VarAVRxt, 2, 32, missModernXTNoCallGroup,
		"ATtiny3216", "ATtiny3217", "ATtiny3224", "ATtiny3226", "ATtiny3227")
	// megaAVR 0-series — AVRxt, 16-bit PC.
	regMissing(VarAVRxt, 2, 8, missModernXTCallGroup, "ATmega808", "ATmega809")
	regMissing(VarAVRxt, 2, 16, missModernXTNoCallGroup, "ATmega1608", "ATmega1609")
	regMissing(VarAVRxt, 2, 32, missModernXTNoCallGroup, "ATmega3208", "ATmega3209")
	regMissing(VarAVRxt, 2, 48, missModernXTNoCallGroup, "ATmega4808", "ATmega4809")

	// Classic megaAVR / AT90 — AVRe+, 16-bit PC.
	regMissing(VarAVRePlus, 2, 4, missCallJmpElpmEijEic, "ATmega48")
	regMissing(VarAVRePlus, 2, 8, NewMissingSet("BREAK", "CALL", "JMP", "ELPM", "EIJMP", "EICALL"), "ATmega8")
	regMissing(VarAVRePlus, 2, 8, missCallJmpElpmEijEic, "ATmega88")
	regMissing(VarAVRePlus, 2, 16, missElpmEijmpEicall,
		"ATmega16", "ATmega162", "ATmega164P", "ATmega168",
		"ATmega16U4", "ATmega16U2")
	regMissing(VarAVRePlus, 2, 32, missElpmEijmpEicall,
		"ATmega32", "ATmega324P", "ATmega328", "ATmega328P", "ATmega328PB",
		"ATmega32U4", "ATmega32U2")
	regMissing(VarAVRePlus, 2, 64, missElpmEijmpEicall, "ATmega64", "ATmega644", "ATmega644P")
	regMissing(VarAVRePlus, 2, 128, missEijmpEicall,
		"ATmega128", "ATmega128A", "ATmega1284P", "ATmega1280", "ATmega1281",
		"AT90USB1286", "AT90USB1287", "AT90CAN128")
	// 256 KB classic megaAVR — AVRe+, 22-bit PC.
	reg(VarAVRePlus, 3, 256,
		"ATmega2560", "ATmega2561", "ATmega2564RFR2", "ATmega256RFR2")

	// Classic tinyAVR — AVRe (no multiply), 16-bit PC.
	regMissing(VarAVRe, 2, 1, missCallJmpElpm, "ATtiny13", "ATtiny13A")
	regMissing(VarAVRe, 2, 2, missCallJmpElpm, "ATtiny24", "ATtiny25", "ATtiny261", "ATtiny2313")
	regMissing(VarAVRe, 2, 4, missCallJmpElpm, "ATtiny44", "ATtiny45", "ATtiny461", "ATtiny4313", "ATtiny48")
	regMissing(VarAVRe, 2, 8, missCallJmpElpm, "ATtiny84", "ATtiny861", "ATtiny88")
	reg(VarAVRe, 2, 16, "ATtiny167", "ATtiny1634")
	// Earliest tinyAVR — original AVR core.
	regMissing(VarAVR, 2, 1, missTiny11Family, "ATtiny11", "ATtiny12", "ATtiny15")
	regMissing(VarAVR, 2, 2, missBreakOnly, "ATtiny26")

	// Reduced Core tinyAVR — AVRrc, 16-bit PC.
	regMissing(VarAVRrc, 2, 1, missBreakOnly, "ATtiny4", "ATtiny5", "ATtiny9", "ATtiny10",
		"ATtiny102", "ATtiny104")
	regMissing(VarAVRrc, 2, 2, missBreakOnly, "ATtiny20")
	reg(VarAVRrc, 2, 4, "ATtiny40")

	// XMEGA — AVRxm. Parts up to 128 KB use a 16-bit PC; 256/384 KB use 22-bit.
	regMissing(VarAVRxm, 2, 16, missElpmEijmpEicall, "ATxmega16A4U", "ATxmega16C4", "ATxmega16E5")
	regMissing(VarAVRxm, 2, 16, NewMissingSet("LAC", "LAT", "LAS", "XCH", "ELPM", "EIJMP", "EICALL"), "ATxmega16D4")
	regMissing(VarAVRxm, 2, 32, missElpmEijmpEicall, "ATxmega32A4U", "ATxmega32C4", "ATxmega32E5")
	regMissing(VarAVRxm, 2, 32, NewMissingSet("LAC", "LAT", "LAS", "XCH", "ELPM", "EIJMP", "EICALL"), "ATxmega32C3", "ATxmega32D4")
	regMissing(VarAVRxm, 2, 64, missEijmpEicall, "ATxmega64A3U", "ATxmega64A4U", "ATxmega64B1")
	regMissing(VarAVRxm, 2, 64, NewMissingSet("LAC", "LAT", "LAS", "XCH", "EIJMP", "EICALL"), "ATxmega64A1")
	reg(VarAVRxm, 2, 128, "ATxmega128A1U", "ATxmega128A3U", "ATxmega128A4U")
	regMissing(VarAVRxm, 2, 128, missXmegaRMW, "ATxmega128D3")
	reg(VarAVRxm, 3, 192, "ATxmega192A3U", "ATxmega192C3")
	reg(VarAVRxm, 3, 256, "ATxmega256A3U", "ATxmega256A3BU", "ATxmega256C3")
	reg(VarAVRxm, 3, 384, "ATxmega384C3")
	regMissing(VarAVRxm, 3, 384, missXmegaRMW, "ATxmega384D3")
}

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
