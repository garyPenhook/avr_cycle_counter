package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const defaultPDF = "https://ww1.microchip.com/downloads/en/DeviceDoc/AVR-InstructionSet-Manual-DS40002198.pdf"

type row struct {
	Name    string
	Variant string
	Missing []string
	FlashKB int
	PCBytes int
}

var (
	reDevice  = regexp.MustCompile(`^(AT|AVR)[A-Za-z0-9]+$`)
	reVariant = regexp.MustCompile(`^AVR(e\+?|xm|xt|rc)?$`)
	reDigits  = regexp.MustCompile(`\d+`)
)

func main() {
	pdfURL := flag.String("pdf-url", defaultPDF, "Microchip AVR instruction-set manual PDF URL")
	textPath := flag.String("text", "", "optional pre-extracted pdftotext input")
	outPath := flag.String("out", "internal/isa/device_gen.go", "generated Go output path")
	flag.Parse()

	text, err := loadText(*pdfURL, *textPath)
	if err != nil {
		die(err)
	}
	rows, err := parseRows(text)
	if err != nil {
		die(err)
	}
	src, err := render(rows)
	if err != nil {
		die(err)
	}
	if err := os.WriteFile(*outPath, src, 0o644); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func loadText(pdfURL, textPath string) (string, error) {
	if textPath != "" {
		b, err := os.ReadFile(textPath)
		return string(b), err
	}
	tmpDir, err := os.MkdirTemp("", "avr-isa-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	pdfPath := filepath.Join(tmpDir, "manual.pdf")
	resp, err := http.Get(pdfURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", pdfURL, resp.Status)
	}
	f, err := os.Create(pdfPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	cmd := exec.Command("pdftotext", pdfPath, "-")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func parseRows(text string) ([]row, error) {
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "Table 7-2.") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("appendix table 7-2 not found")
	}

	var rows []row
	for i := start; i < len(lines); {
		s := strings.TrimSpace(lines[i])
		if !reDevice.MatchString(s) {
			i++
			continue
		}
		name := s
		j := nextNonEmpty(lines, i+1)
		if j >= len(lines) || !reVariant.MatchString(strings.TrimSpace(lines[j])) {
			i++
			continue
		}
		variant := strings.TrimSpace(lines[j])
		k := nextNonEmpty(lines, j+1)
		missing := []string{}
		if k < len(lines) {
			next := strings.TrimSpace(lines[k])
			if next != "" && !reDevice.MatchString(next) && !reVariant.MatchString(next) &&
				!strings.HasPrefix(next, "Table ") && !strings.HasPrefix(next, "Appendix ") &&
				!strings.HasPrefix(next, "Device") && !strings.HasPrefix(next, "Core") &&
				!strings.HasPrefix(next, "Missing Instructions") && !strings.HasPrefix(next, "©") &&
				!strings.HasPrefix(next, "Manual") && !strings.HasPrefix(next, "DS40002198") &&
				!regexp.MustCompile(`^7\.\d`).MatchString(next) {
				for _, mn := range normalizeMissing(next) {
					mn = strings.TrimSpace(mn)
					if mn != "" {
						missing = append(missing, mn)
					}
				}
				k++
			}
		}
		flashKB, pcBytes, ok := deriveFlashAndPC(name)
		if ok {
			rows = append(rows, row{
				Name:    strings.ToUpper(name),
				Variant: variant,
				Missing: missing,
				FlashKB: flashKB,
				PCBytes: pcBytes,
			})
		}
		i = k
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return dedupe(rows), nil
}

func nextNonEmpty(lines []string, start int) int {
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return len(lines)
}

func dedupe(rows []row) []row {
	out := make([]row, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		out = append(out, r)
	}
	return out
}

func normalizeMissing(s string) []string {
	s = strings.ReplaceAll(s, "LPM Z+ ADIW", "LPM Z+, ADIW")
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

func firstNum(s string) int {
	n, _ := strconv.Atoi(reDigits.FindString(s))
	return n
}

func deriveFlashAndPC(name string) (int, int, bool) {
	u := strings.ToUpper(name)
	if strings.HasPrefix(u, "AVR") {
		kb := firstNum(u)
		if kb > 0 {
			return kb, 2, true
		}
	}
	if strings.HasPrefix(u, "ATXMEGA") {
		kb := firstNum(u)
		if kb > 0 {
			pc := 2
			if kb > 128 {
				pc = 3
			}
			return kb, pc, true
		}
	}
	switch {
	case regexp.MustCompile(`^ATMEGA(808|809)$`).MatchString(u):
		return 8, 2, true
	case regexp.MustCompile(`^ATMEGA(1608|1609|16|16U2|16U4|162|164[A-Z0-9]*)$`).MatchString(u):
		return 16, 2, true
	case regexp.MustCompile(`^ATMEGA(3208|3209|32|32U2|32U4|324[A-Z0-9]*|328[A-Z0-9]*)$`).MatchString(u):
		return 32, 2, true
	case regexp.MustCompile(`^ATMEGA48[APB]*$`).MatchString(u) || u == "ATMEGA48":
		return 4, 2, true
	case regexp.MustCompile(`^ATMEGA88[APB]*$`).MatchString(u) || u == "ATMEGA88" || u == "ATMEGA8" || strings.HasPrefix(u, "ATMEGA8A"):
		if u == "ATMEGA8A" || u == "ATMEGA8" {
			return 8, 2, true
		}
		return 8, 2, true
	case regexp.MustCompile(`^ATMEGA64([A-Z0-9]*)?$`).MatchString(u) || strings.HasPrefix(u, "ATMEGA644"):
		return 64, 2, true
	case regexp.MustCompile(`^ATMEGA128([A-Z0-9]*)?$`).MatchString(u) || strings.HasPrefix(u, "ATMEGA1280") || strings.HasPrefix(u, "ATMEGA1281") || strings.HasPrefix(u, "ATMEGA1284"):
		return 128, 2, true
	case strings.HasPrefix(u, "ATMEGA2560") || strings.HasPrefix(u, "ATMEGA2561") || strings.HasPrefix(u, "ATMEGA2564") || strings.HasPrefix(u, "ATMEGA256RFR2"):
		return 256, 3, true
	case strings.HasPrefix(u, "ATMEGA4808") || strings.HasPrefix(u, "ATMEGA4809"):
		return 48, 2, true
	case strings.HasPrefix(u, "AT90USB1286") || strings.HasPrefix(u, "AT90USB1287") || strings.HasPrefix(u, "AT90CAN128"):
		return 128, 2, true
	}
	switch {
	case regexp.MustCompile(`^ATTINY(202|204|212|214)$`).MatchString(u):
		return 2, 2, true
	case regexp.MustCompile(`^ATTINY(402|404|406|412|414|416|417|424|426|427)$`).MatchString(u):
		return 4, 2, true
	case regexp.MustCompile(`^ATTINY(804|806|807|814|816|817|824|826|827)$`).MatchString(u):
		return 8, 2, true
	case regexp.MustCompile(`^ATTINY(1604|1606|1607|1614|1616|1617|1624|1626|1627)$`).MatchString(u):
		return 16, 2, true
	case regexp.MustCompile(`^ATTINY(3216|3217|3224|3226|3227)$`).MatchString(u):
		return 32, 2, true
	case regexp.MustCompile(`^ATTINY13A?$`).MatchString(u):
		return 1, 2, true
	case regexp.MustCompile(`^ATTINY(24|25|2313A?|261)$`).MatchString(u):
		return 2, 2, true
	case regexp.MustCompile(`^ATTINY(44|45|461|4313|48)$`).MatchString(u):
		return 4, 2, true
	case regexp.MustCompile(`^ATTINY(84|85|861|88)$`).MatchString(u):
		return 8, 2, true
	case regexp.MustCompile(`^ATTINY(167|1634)$`).MatchString(u):
		return 16, 2, true
	case regexp.MustCompile(`^ATTINY(11|12|15|4|5|9|10|102|104)$`).MatchString(u):
		return 1, 2, true
	case u == "ATTINY20":
		return 2, 2, true
	case u == "ATTINY40":
		return 4, 2, true
	case u == "ATTINY26":
		return 2, 2, true
	}
	return 0, 0, false
}

func variantConst(v string) string {
	switch v {
	case "AVR":
		return "VarAVR"
	case "AVRe":
		return "VarAVRe"
	case "AVRe+":
		return "VarAVRePlus"
	case "AVRxm":
		return "VarAVRxm"
	case "AVRxt":
		return "VarAVRxt"
	case "AVRrc":
		return "VarAVRrc"
	default:
		panic(v)
	}
}

func render(rows []row) ([]byte, error) {
	var b strings.Builder
	b.WriteString("// Code generated by cmd/gen-avr-device-db; DO NOT EDIT.\n")
	b.WriteString("package isa\n\n")
	b.WriteString("var generatedDevices = map[string]Device{\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("\t%q: {Name: %q, Variant: %s, PCBytes: %d, FlashKB: %d",
			r.Name, r.Name, variantConst(r.Variant), r.PCBytes, r.FlashKB))
		if len(r.Missing) > 0 {
			b.WriteString(", Missing: NewMissingSet(")
			for i, mn := range r.Missing {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(fmt.Sprintf("%q", mn))
			}
			b.WriteString(")")
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return format.Source([]byte(b.String()))
}
