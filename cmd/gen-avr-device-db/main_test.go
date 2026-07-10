package main

import "testing"

func TestParseRowsKeepsUnknownDeviceAndRejectsFooter(t *testing.T) {
	text := `
Table 7-2. Devices
ATmega256RFR2
AVRe+
Migration Guide
AT90CAN32
AVRe+
ELPM, EIJMP, EICALL
`
	rows, err := parseRows(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	byName := map[string]row{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if r, ok := byName["AT90CAN32"]; !ok || r.PCBytes != 2 || r.FlashKB != 0 {
		t.Fatalf("AT90CAN32 missing or wrong fallback: %+v", r)
	}
	if r := byName["ATMEGA256RFR2"]; len(r.Missing) != 0 {
		t.Fatalf("footer parsed as missing instruction: %v", r.Missing)
	}
}

func TestApplyManualErrataRemovesStaleLPMEntries(t *testing.T) {
	got := applyManualErrata("ATtiny11", []string{"BREAK", "LPM", "LPM Z+", "ADIW"})
	if len(got) != 2 || got[0] != "BREAK" || got[1] != "ADIW" {
		t.Fatalf("errata result = %v, want [BREAK ADIW]", got)
	}
}
