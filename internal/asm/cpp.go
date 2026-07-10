package asm

// This file adds an optional C-preprocessor pass for assembler source. The
// hand-written source parser does not expand `#include`, `#define`, or `#if`
// conditionals (it skips `#`-lines), so a `.S` that relies on the preprocessor
// — device headers like <avr/io.h>, register macros, conditional builds — is
// analyzed with those lines missing. Running the source through the compiler
// driver's preprocessor first (`avr-gcc -E -x assembler-with-cpp`) resolves all
// of that into a flat instruction stream the existing parser can read, without
// having to assemble to an object file.
//
// This handles the *C* preprocessor only. GNU-assembler `.macro`/`.include`
// directives are expanded by the assembler, not cpp; for those, analyze a
// compiled `.o`/ELF (the objdump front end) instead.

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CPPOptions configures the C-preprocessor pass run by Preprocess.
type CPPOptions struct {
	CC       string   // compiler driver (default "avr-gcc")
	MMCU     string   // value for -mmcu= (empty to omit); selects device headers
	Defines  []string // -D entries, e.g. "F_CPU=16000000"
	Includes []string // -I entries: header search directories
}

func (o CPPOptions) compiler() string {
	if o.CC == "" {
		return "avr-gcc"
	}
	return o.CC
}

// Preprocess runs the C preprocessor over an assembler source file and returns
// the expanded text. `#include`, `#define`, and `#if` are resolved; the result
// is still GNU-assembler source, ready for Parse.
func Preprocess(path string, o CPPOptions) (string, error) {
	cc := o.compiler()
	// -C preserves source comments so // and /* */ @begin/@end annotations remain
	// available to the assembly parser after preprocessing.
	args := []string{"-E", "-C", "-x", "assembler-with-cpp"}
	if o.MMCU != "" {
		args = append(args, "-mmcu="+o.MMCU)
	}
	for _, d := range o.Defines {
		args = append(args, "-D"+d)
	}
	for _, inc := range o.Includes {
		args = append(args, "-I"+inc)
	}
	args = append(args, path)

	cmd := exec.Command(cc, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%s not found in PATH — install the AVR GNU toolchain or pass -cc <path>", cc)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s -E failed: %s", cc, msg)
	}
	return stdout.String(), nil
}
