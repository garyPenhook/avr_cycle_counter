package isa

import (
	"regexp"
	"strings"
)

// Device maps a part number to its CPU variant and Program Counter width.
// Data comes from the AVR Instruction Set Manual Appendix A device tables
// (DS40002198C §7.2). PCBytes is 3 for parts with more than 128 KB flash
// (22-bit PC) and 2 otherwise (16-bit PC).
type Device struct {
	Name    string
	Variant Variant
	PCBytes int
}

// dxFamily matches the cleanly-named modern parts (AVR Dx/Ex/Du/Sd), all AVRxt
// with ≤128 KB flash → 16-bit PC.
var dxFamily = regexp.MustCompile(`^AVR\d+(DA|DB|DD|DU|EA|EB|SD)\d*$`)

// devices is a curated lookup seeded from the manual's device tables. It is not
// exhaustive — any AVR can still be analyzed by passing -core (and -pc for
// >128 KB parts) directly.
var devices = map[string]Device{}

func reg(v Variant, pc int, names ...string) {
	for _, n := range names {
		N := strings.ToUpper(n)
		devices[N] = Device{Name: N, Variant: v, PCBytes: pc}
	}
}

func init() {
	// tinyAVR 0/1/2-series — AVRxt, 16-bit PC.
	reg(VarAVRxt, 2,
		"ATtiny202", "ATtiny204", "ATtiny212", "ATtiny214",
		"ATtiny402", "ATtiny404", "ATtiny406", "ATtiny412", "ATtiny414",
		"ATtiny416", "ATtiny417", "ATtiny424", "ATtiny426", "ATtiny427",
		"ATtiny804", "ATtiny806", "ATtiny807", "ATtiny814", "ATtiny816", "ATtiny817",
		"ATtiny824", "ATtiny826", "ATtiny827",
		"ATtiny1604", "ATtiny1606", "ATtiny1607",
		"ATtiny1614", "ATtiny1616", "ATtiny1617",
		"ATtiny1624", "ATtiny1626", "ATtiny1627",
		"ATtiny3216", "ATtiny3217", "ATtiny3224", "ATtiny3226", "ATtiny3227")
	// megaAVR 0-series — AVRxt, 16-bit PC.
	reg(VarAVRxt, 2,
		"ATmega808", "ATmega809", "ATmega1608", "ATmega1609",
		"ATmega3208", "ATmega3209", "ATmega4808", "ATmega4809")

	// Classic megaAVR / AT90 — AVRe+, 16-bit PC.
	reg(VarAVRePlus, 2,
		"ATmega8", "ATmega16", "ATmega32", "ATmega48", "ATmega88",
		"ATmega168", "ATmega328", "ATmega328P", "ATmega328PB",
		"ATmega162", "ATmega164P", "ATmega324P", "ATmega644", "ATmega644P",
		"ATmega1284P", "ATmega1280", "ATmega1281",
		"ATmega16U4", "ATmega32U4", "ATmega16U2", "ATmega32U2",
		"ATmega64", "ATmega128", "ATmega128A",
		"AT90USB1286", "AT90USB1287", "AT90CAN128")
	// 256 KB classic megaAVR — AVRe+, 22-bit PC.
	reg(VarAVRePlus, 3,
		"ATmega2560", "ATmega2561", "ATmega2564RFR2", "ATmega256RFR2")

	// Classic tinyAVR — AVRe (no multiply), 16-bit PC.
	reg(VarAVRe, 2,
		"ATtiny13", "ATtiny13A", "ATtiny24", "ATtiny25", "ATtiny44", "ATtiny45",
		"ATtiny84", "ATtiny85", "ATtiny261", "ATtiny461", "ATtiny861",
		"ATtiny2313", "ATtiny4313", "ATtiny48", "ATtiny88",
		"ATtiny167", "ATtiny1634")
	// Earliest tinyAVR — original AVR core.
	reg(VarAVR, 2, "ATtiny11", "ATtiny12", "ATtiny15", "ATtiny26")

	// Reduced Core tinyAVR — AVRrc, 16-bit PC.
	reg(VarAVRrc, 2,
		"ATtiny4", "ATtiny5", "ATtiny9", "ATtiny10",
		"ATtiny20", "ATtiny40", "ATtiny102", "ATtiny104")

	// XMEGA — AVRxm. Parts up to 128 KB use a 16-bit PC; 256/384 KB use 22-bit.
	reg(VarAVRxm, 2,
		"ATxmega16A4U", "ATxmega16C4", "ATxmega16D4", "ATxmega16E5",
		"ATxmega32A4U", "ATxmega32C3", "ATxmega32C4", "ATxmega32D4", "ATxmega32E5",
		"ATxmega64A1", "ATxmega64A3U", "ATxmega64A4U", "ATxmega64B1",
		"ATxmega128A1U", "ATxmega128A3U", "ATxmega128A4U", "ATxmega128D3")
	reg(VarAVRxm, 3,
		"ATxmega192A3U", "ATxmega192C3", "ATxmega256A3U", "ATxmega256A3BU",
		"ATxmega256C3", "ATxmega384C3", "ATxmega384D3")
}

// LookupDevice resolves a part number (case-insensitive) to its Device entry.
func LookupDevice(name string) (Device, bool) {
	N := strings.ToUpper(strings.TrimSpace(name))
	if d, ok := devices[N]; ok {
		return d, true
	}
	if dxFamily.MatchString(N) {
		return Device{Name: N, Variant: VarAVRxt, PCBytes: 2}, true
	}
	return Device{}, false
}
