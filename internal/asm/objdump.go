package asm

// This file adds a second front end: instead of hand-written GNU-assembler
// source, it consumes the disassembly produced by `avr-objdump -h -d` (or a
// pre-saved copy of it) and emits the same []*Line the source parser does, so
// the analyzer treats both inputs identically.
//
// Why a disassembly front end at all: the source parser does not expand macros,
// .include, or the C preprocessor. avr-objdump operates on already-assembled
// .o/.elf output, where all of that is resolved — so feeding it a compiled
// object gives an exact instruction stream with no parser blind spots, for any
// part avr-gcc supports. The `-h` section table also yields real .data/.bss
// sizes, which a -d listing alone cannot provide.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// One disassembled instruction, e.g. "   a:\t80 93 06 04 \tsts\t0x0406, r24\t; ..".
// Group 1 is the address, group 2 the encoded bytes, group 3 the rest (mnemonic,
// operands, and any trailing "; comment").
var reObjInsn = regexp.MustCompile(`^\s*([0-9a-fA-F]+):\s+((?:[0-9a-fA-F]{2}\s)+)\s*(.*)$`)

// A symbol header, e.g. "00000006 <toggle_pin>:".
var reObjLabel = regexp.MustCompile(`^[0-9a-fA-F]+\s+<([^>]+)>:\s*$`)

// "Disassembly of section .text:".
var reObjSection = regexp.MustCompile(`^Disassembly of section (\S+):`)

// A section-header table row, e.g. "  2 .bss          00000020  00000000 ..".
var reObjHeader = regexp.MustCompile(`^\s*\d+\s+(\.\S+)\s+([0-9a-fA-F]+)\s+`)

// LooksLikeObjdump reports whether text is avr-objdump disassembly output rather
// than assembler source.
func LooksLikeObjdump(s string) bool {
	return strings.Contains(s, "Disassembly of section ")
}

func isELF(b []byte) bool {
	return len(b) >= 4 && b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F'
}

func isIHex(s string) bool {
	t := strings.TrimLeft(s, " \t\r\n")
	return strings.HasPrefix(t, ":")
}

func isDataSectionName(name string) bool {
	return strings.HasPrefix(name, ".data") ||
		strings.HasPrefix(name, ".bss") ||
		strings.HasPrefix(name, ".noinit")
}

// LoadFile reads path and parses it the right way: an ELF object/executable is
// disassembled with objdumpBin; an Intel-HEX file likewise; text that already
// looks like objdump output is parsed as disassembly; anything else is treated
// as assembler source. objdumpBin defaults to "avr-objdump" when empty.
func LoadFile(path, objdumpBin string) ([]*Line, error) {
	return loadFile(path, objdumpBin, nil)
}

// LoadFileCPP behaves like LoadFile but, for assembler-source input, first runs
// the C preprocessor (see Preprocess) so `#include`/`#define`/`#if` are
// resolved before parsing. Binary/disassembly inputs ignore cpp (the
// preprocessor does not apply to already-assembled code).
func LoadFileCPP(path, objdumpBin string, cpp CPPOptions) ([]*Line, error) {
	return loadFile(path, objdumpBin, &cpp)
}

func loadFile(path, objdumpBin string, cpp *CPPOptions) ([]*Line, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if isELF(data) {
		txt, err := runObjdump(objdumpBin, path, false)
		if err != nil {
			return nil, err
		}
		return ParseObjdump(txt), nil
	}
	s := string(data)
	if LooksLikeObjdump(s) {
		return ParseObjdump(s), nil
	}
	if isIHex(s) && strings.EqualFold(filepath.Ext(path), ".hex") {
		txt, err := runObjdump(objdumpBin, path, true)
		if err != nil {
			return nil, err
		}
		return ParseObjdump(txt), nil
	}
	if cpp != nil {
		txt, err := Preprocess(path, *cpp)
		if err != nil {
			return nil, err
		}
		return Parse(txt), nil
	}
	return Parse(s), nil
}

func runObjdump(bin, path string, ihex bool) (string, error) {
	if bin == "" {
		bin = "avr-objdump"
	}
	var args []string
	if ihex {
		// ihex carries no format/arch metadata, and its single section is not
		// flagged executable — so supply the arch/format and use -D (disassemble
		// everything) rather than -d (code sections only).
		args = []string{"-h", "-D", "-m", "avr", "-b", "ihex"}
	} else {
		args = []string{"-h", "-d"}
	}
	args = append(args, path)

	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%s not found in PATH — install AVR binutils or pass -objdump <path>", bin)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s failed: %s", bin, msg)
	}
	return stdout.String(), nil
}

// ParseObjdump parses `avr-objdump -h -d` text into the same []*Line the source
// parser produces. Instructions become Mnemonic/Operands lines, symbol headers
// become Label lines, and each allocated data section from the `-h` table
// becomes a synthetic ".space N" directive so static-SRAM accounting works.
func ParseObjdump(src string) []*Line {
	var out []*Line
	section := ".text"
	inHeaders := false

	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	num := 0
	for sc.Scan() {
		num++
		raw := sc.Text()

		if strings.HasPrefix(strings.TrimSpace(raw), "Sections:") {
			inHeaders = true
			continue
		}
		if m := reObjSection.FindStringSubmatch(raw); m != nil {
			section = m[1]
			inHeaders = false
			continue
		}
		if inHeaders {
			if m := reObjHeader.FindStringSubmatch(raw); m != nil {
				name := m[1]
				if sz, err := strconv.ParseInt(m[2], 16, 64); err == nil && sz > 0 && isDataSectionName(name) {
					out = append(out, &Line{
						Num: num, Raw: raw, Section: name,
						Directive: ".space", DirectiveArgs: strconv.FormatInt(sz, 10),
					})
				}
			}
			continue
		}
		if m := reObjLabel.FindStringSubmatch(raw); m != nil {
			out = append(out, &Line{Num: num, Raw: raw, Section: section, Label: m[1]})
			continue
		}
		if m := reObjInsn.FindStringSubmatch(raw); m != nil {
			out = append(out, objLine(num, raw, section, m[3]))
			continue
		}
		// file-format banner, blank lines, "..." gaps: nothing to account for.
	}
	return out
}

// objLine turns the post-bytes remainder of a disassembly line ("mnemonic
// operands ; comment") into a parsed Line for the given section.
func objLine(num int, raw, section, rest string) *Line {
	ln := &Line{Num: num, Raw: raw, Section: section}
	code, comment, hasComment := strings.Cut(rest, ";")
	if hasComment {
		ln.Comment = strings.TrimSpace(comment)
	}
	first, ops := splitFirstField(code)
	switch {
	case first == "":
		// no mnemonic (rare); leave as a bare line
	case strings.HasPrefix(first, "."):
		// undecodable bytes objdump renders as ".word 0x...." — count as data.
		ln.Directive = first
		ln.DirectiveArgs = ops
	default:
		ln.Mnemonic = strings.ToUpper(first)
		ln.Operands = ops
	}
	return ln
}
