// Package asm is a lightweight parser for GNU-assembler (.S/.s) AVR source.
//
// It is not a full assembler: it classifies each source line as label,
// instruction, directive, or comment, tracks the current section, and reads
// region annotations placed in comments. That is enough to count instructions
// on a hot path and size data allocations without assembling the program.
package asm

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Region is a hot-path span opened with a "@begin" annotation.
type Region struct {
	Name string
	Iter int // loop trip count applied to the span's cycle/instruction totals
}

// Line is one parsed source line. Fields are empty when not applicable; a
// single line may carry both a Label and an instruction or directive.
type Line struct {
	Num           int
	Raw           string
	Section       string // section in effect for this line (default ".text")
	Label         string
	Mnemonic      string // upper-cased, for instructions
	Operands      string
	Directive     string // e.g. ".byte", ".section"
	DirectiveArgs string
	Comment       string
	RegionBegins  []Region // "@begin" annotations on this line
	RegionEnds    []string // "@end" annotations on this line (region names)
}

// ParseFile reads and parses an assembly file.
func ParseFile(path string) ([]*Line, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(string(data)), nil
}

// Parse parses assembly source text into a slice of lines.
func Parse(src string) []*Line {
	src = stripBlockComments(src)
	section := ".text"
	var out []*Line

	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	num := 0
	for sc.Scan() {
		num++
		raw := sc.Text()
		ln := &Line{Num: num, Raw: raw}

		code, comment := splitComment(raw)
		ln.Comment = strings.TrimSpace(comment)
		if ln.Comment != "" {
			ln.RegionBegins, ln.RegionEnds = parseAnnotations(ln.Comment)
		}
		parseCode(ln, code, &section)
		ln.Section = section
		out = append(out, ln)
	}
	return out
}

// stripBlockComments removes /* ... */ comments while preserving line count.
func stripBlockComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i < len(s) && !(i+1 < len(s) && s[i] == '*' && s[i+1] == '/') {
				if s[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			i += 2 // skip closing */
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// splitComment separates code from a line comment (';' or '//'), respecting
// double-quoted strings so e.g. .ascii ";" is not truncated.
func splitComment(line string) (code, comment string) {
	inStr := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '"' && (i == 0 || line[i-1] != '\\') {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == ';' {
			return line[:i], line[i+1:]
		}
		if c == '/' && i+1 < len(line) && line[i+1] == '/' {
			return line[:i], line[i+2:]
		}
	}
	return line, ""
}

// parseAnnotations reads "@begin [name] [iter=N]" and "@end [name]" tokens.
func parseAnnotations(comment string) (begins []Region, ends []string) {
	toks := strings.Fields(comment)
	for i := 0; i < len(toks); {
		switch strings.ToLower(toks[i]) {
		case "@begin":
			r := Region{Iter: 1}
			i++
			for i < len(toks) && !strings.HasPrefix(toks[i], "@") {
				a := toks[i]
				if strings.HasPrefix(strings.ToLower(a), "iter=") {
					if n, err := strconv.Atoi(a[len("iter="):]); err == nil && n > 0 {
						r.Iter = n
					}
				} else if r.Name == "" {
					r.Name = a
				}
				i++
			}
			if r.Name == "" {
				r.Name = "hot"
			}
			begins = append(begins, r)
		case "@end":
			name := ""
			i++
			for i < len(toks) && !strings.HasPrefix(toks[i], "@") {
				if name == "" {
					name = toks[i]
				}
				i++
			}
			if name == "" {
				name = "hot"
			}
			ends = append(ends, name)
		default:
			i++
		}
	}
	return
}

func isLabelChar(b byte) bool {
	return b == '_' || b == '.' || b == '$' ||
		(b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// leadingLabel peels one "label:" off the front of code, if present.
func leadingLabel(code string) (label, rest string, ok bool) {
	i := 0
	for i < len(code) && isLabelChar(code[i]) {
		i++
	}
	if i > 0 && i < len(code) && code[i] == ':' {
		rest = strings.TrimPrefix(code[i+1:], ":") // tolerate global "::" labels
		return code[:i], strings.TrimSpace(rest), true
	}
	return "", code, false
}

func parseCode(ln *Line, code string, section *string) {
	code = strings.TrimSpace(code)
	for {
		label, rest, ok := leadingLabel(code)
		if !ok {
			break
		}
		ln.Label = label // last label on the line wins (multiple is rare)
		code = rest
	}
	if code == "" {
		return
	}
	if strings.HasPrefix(code, "#") {
		return // C preprocessor line; not an instruction
	}
	if strings.HasPrefix(code, ".") {
		dir, args := splitFirstField(code)
		ln.Directive = dir
		ln.DirectiveArgs = args
		updateSection(dir, args, section)
		return
	}
	mn, ops := splitFirstField(code)
	ln.Mnemonic = strings.ToUpper(mn)
	ln.Operands = ops
}

// splitFirstField returns the first whitespace-delimited token and the rest.
func splitFirstField(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	idx := strings.IndexFunc(s, unicode.IsSpace)
	if idx < 0 {
		return s, ""
	}
	return s[:idx], strings.TrimSpace(s[idx:])
}

func updateSection(dir, args string, section *string) {
	switch dir {
	case ".text", ".data", ".bss", ".rodata":
		*section = dir
	case ".section":
		name := args
		if j := strings.IndexAny(name, ", \t"); j >= 0 {
			name = name[:j]
		}
		if name != "" {
			*section = name
		}
	}
}

// DataBytes returns how many bytes a data-allocating directive reserves.
// ok is false for directives that do not allocate storage (or can't be sized).
func DataBytes(directive, args string) (int, bool) {
	d := strings.ToLower(strings.TrimSpace(directive))
	a := strings.TrimSpace(args)
	switch d {
	case ".byte":
		return countItems(a), true
	case ".word", ".short", ".hword", ".2byte":
		return 2 * countItems(a), true
	case ".long", ".4byte":
		return 4 * countItems(a), true
	case ".quad", ".8byte":
		return 8 * countItems(a), true
	case ".space", ".skip", ".zero", ".ds.b":
		if n, ok := firstInt(a); ok {
			return n, true
		}
	case ".ds.w":
		if n, ok := firstInt(a); ok {
			return 2 * n, true
		}
	case ".ds.l":
		if n, ok := firstInt(a); ok {
			return 4 * n, true
		}
	case ".ascii":
		return stringBytes(a, false)
	case ".asciz", ".string":
		return stringBytes(a, true)
	case ".fill":
		ps := strings.Split(a, ",")
		rep, ok := firstInt(ps[0])
		if !ok {
			return 0, false
		}
		size := 1
		if len(ps) > 1 {
			if s, ok := firstInt(ps[1]); ok {
				size = s
			}
		}
		return rep * size, true
	}
	return 0, false
}

func countItems(a string) int {
	if strings.TrimSpace(a) == "" {
		return 0
	}
	n := 0
	for p := range strings.SplitSeq(a, ",") {
		if strings.TrimSpace(p) != "" {
			n++
		}
	}
	return n
}

func firstInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if j := strings.IndexAny(s, ", \t"); j >= 0 {
		s = s[:j]
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return 0, false
	}
	return int(v), true
}

// stringBytes sums the lengths of quoted strings in a; escapes count as 1 byte
// (an approximation for \x.. / octal). terminated adds a NUL per string.
func stringBytes(a string, terminated bool) (int, bool) {
	total, count, i := 0, 0, 0
	for i < len(a) {
		if a[i] != '"' {
			i++
			continue
		}
		i++ // opening quote
		n := 0
		for i < len(a) && a[i] != '"' {
			if a[i] == '\\' && i+1 < len(a) {
				i += 2
			} else {
				i++
			}
			n++
		}
		i++ // closing quote
		total += n
		count++
		if terminated {
			total++
		}
	}
	if count == 0 {
		return 0, false
	}
	return total, true
}
