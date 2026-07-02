# cyclecount

[![Go](https://github.com/garyPenhook/avr_cycle_counter/actions/workflows/go.yml/badge.svg)](https://github.com/garyPenhook/avr_cycle_counter/actions/workflows/go.yml)

A small console tool that helps you **prove an AVR assembly optimisation from
the instruction listing** before you trust it.

> Assembly optimisation is useful only when it is measured. AVR cores are
> predictable enough that many improvements can be proven from the instruction
> listing alone: count the instructions on the hot path, apply the cycle counts
> from Microchip's *AVR Instruction Set Manual*, then compare the result with
> the flash and SRAM cost.

`cyclecount` reads `.S`/`.s` GNU-assembler source, counts the instructions on a
hot path, applies the per-instruction cycle counts **for the AVR core you
select**, and reports cycles next to the flash and SRAM cost — so a change is
judged by numbers, not by belief.

## Supports every AVR

Every AVR ever made uses one of six documented CPU variants, and the manual
gives a cycle column for each. `cyclecount` implements all of them:

| Variant | Typical parts | Notable timing |
|---------|---------------|----------------|
| `avr`   | ATtiny11/12/15/26 (original core) | no MOVW/MUL |
| `avre`  | classic ATtiny (13/25/45/85, 2313 …) | same timing as AVR, + MOVW |
| `avre+` | classic ATmega / AT90 (328P, 2560, 32U4 …) | + multiply |
| `avrxm` | XMEGA (ATxmega…) | RMW (XCH/LAx), faster ST/PUSH |
| `avrxt` | tinyAVR 0/1/2, megaAVR 0, AVR Dx/Ex/Du | 1-cycle ST/PUSH/SBI, 3-cycle CALL |
| `avrrc` | Reduced Core (ATtiny4/5/9/10/20/40/102/104) | 16 registers, no ADIW/MUL/CALL |

`CALL`/`JMP` are left out of the "notable timing" column above only because
they don't distinguish the *enhanced* cores from each other. The manual still
treats them as a core-level rule: **Table 7-1** lists `CALL`/`JMP` as present on
AVRe/AVRe+/AVRxm/AVRxt and **absent on both the original `avr` core and the
Reduced Core (`avrrc`)**. On top of that, many individual parts list `CALL`/`JMP`
as missing in Appendix A, so `cyclecount` applies both — the core-level absence
on `avr`/`avrrc` and the per-device overrides.

When you know the exact MCU, prefer `-mcu` over bare `-core`: that lets
`cyclecount` apply the device-specific missing-instruction tables from Appendix
A (for example parts that lack `CALL`, `ELPM`, `EIJMP`, `EICALL`, `SPM`, or
`BREAK` even though the broader core family supports them).

Pick a target three ways (default is **ATtiny3217 / AVRxt**):

```sh
cyclecount -mcu atmega328p   file.S   # a known part number
cyclecount -core avrrc       file.S   # a CPU variant directly — works for ANY AVR
cyclecount -core avre+ -pc 3 file.S   # 22-bit PC override for >128 KB parts
cyclecount -mcu attiny3217,atmega328p file.S   # compare multiple MCUs in one run
```

The same source, three cores — note the cycle count and instruction set both
change:

```
ATtiny3217 (AVRxt) : 17 – 18 cycles
ATmega328P (AVRe+) : 18 – 19 cycles   (PUSH costs 2, not 1)
ATtiny10   (AVRrc) : 19 – 20 cycles   + ✘ SBIW: not implemented on AVRrc
```

If a part isn't in the built-in device list, `cyclecount` tells you to use
`-core` directly — so no AVR is left out.

The lookup is no longer exact-name only: besides curated entries, `cyclecount`
also recognizes several common family naming patterns such as modern `AVR128DA*`
/ `AVR64EA*`, many classic `ATmega*` suffix variants, and broad `ATxmega*`
families, so routine package/suffix variants resolve without adding every exact
part string by hand.

## Cycle data provenance

Every cycle count, word size, and per-core availability flag comes from the
**Microchip AVR Instruction Set Manual, DS40002198C**:

- `#Clocks AVRe / AVRxm / AVRxt / AVRrc` columns of the Instruction Set Summary
  (Tables 5-2 … 5-6) and the per-instruction `Cycles` tables.
- **Table 7-1 Core Description** for the core-level instruction families
  (including the `CALL`/`JMP` core-level rule noted above).
- **§7.2 device tables** (Appendix A) for the part-number → core / PC-width map
  and the per-device missing-instruction overrides.

The data lives in plain, readable Go — [`internal/isa/isa.go`](internal/isa/isa.go)
(timing + availability) and [`internal/isa/device.go`](internal/isa/device.go)
(device map). It was cross-checked against Microchip's documentation server.

Call/return/stack figures depend on the **Program Counter width**: ≤128 KB
parts use a 16-bit PC (2-byte return address); larger parts (e.g. ATmega2560)
use a 22-bit PC, which adds one cycle to `CALL`/`RCALL`/`ICALL`/`RET`/`RETI`.

## Requirements

| to do this | you need |
|------------|----------|
| build `cyclecount` | **Go ≥ 1.26** (see `go.mod`) |
| analyze `.S`/`.s` **source** | nothing else — pure Go, no external tools |
| analyze a compiled **ELF / `.o` / `.hex`** | **AVR binutils** providing `avr-objdump` on your `$PATH` |

`avr-objdump` ships with the AVR GNU toolchain (the same package that gives you
`avr-gcc`). Install it with your system's package manager — e.g. Debian/Ubuntu
`sudo apt install binutils-avr gcc-avr`, Arch `pacman -S avr-binutils avr-gcc`,
or Microchip's official AVR/GNU toolchain.

`cyclecount` finds `avr-objdump` via your `$PATH` (it hard-codes no paths); if
it lives somewhere unusual, point at it with `-objdump /path/to/avr-objdump`.
Source-mode analysis needs none of this.

### Platforms

`cyclecount` is **pure Go** — only the standard library, no cgo, no
platform-specific code or build tags. It builds and runs anywhere the Go
toolchain targets:

| OS | notes |
|----|-------|
| **Linux** | x86-64, arm64, and every other Go arch; static `CGO_ENABLED=0` builds work |
| **macOS** | Intel and Apple Silicon (arm64) |
| **Windows** | native `.exe`; `avr-objdump.exe` / `avr-gcc.exe` are found on `%PATH%` |
| **\*BSD, etc.** | any `GOOS`/`GOARCH` pair `go` supports |

Source-mode analysis is the same on every OS. The optional binary front end
(`avr-objdump`) and `-cpp` (`avr-gcc`) just need those executables on your path
for the OS you run on — there is nothing OS-specific in `cyclecount` itself.

## Install

### Prebuilt binary (recommended)

Grab the latest release from the
[releases page](https://github.com/garyPenhook/avr_cycle_counter/releases).
The current release is **v0.2.3**, with builds for linux, macOS (darwin), and
windows on amd64/arm64 (no windows/arm64). For example, Linux x86-64:

```sh
curl -fL https://github.com/garyPenhook/avr_cycle_counter/releases/download/v0.2.3/cyclecount-linux-amd64.tar.gz | tar -xz
./cyclecount -version          # prints: cyclecount 0.2.3
```

Swap `linux-amd64` for `linux-arm64`, `darwin-amd64`, `darwin-arm64`, or
`windows-amd64` (a `.zip`) as needed. Verify your download against
`checksums.txt` from the same release.

### Build from source

With a Go toolchain (**Go ≥ 1.26**, see `go.mod`), build from a checkout:

```sh
git clone <repo-url> && cd Count_Cycles
go install .          # → $GOBIN (or $GOPATH/bin, default ~/go/bin)
```

`go install .` drops a `cyclecount` (or `cyclecount.exe` on Windows) binary in
your Go bin directory; add that directory to `PATH` if it isn't already. The
module is named `cyclecount` locally, so the remote `go install <path>@latest`
form does not apply — clone first, then `go install .` (or just `go build`,
below, and copy the binary wherever you like).

## Build

```sh
go build -trimpath -ldflags="-s -w" -o cyclecount .   # stripped release binary
go test ./...                                          # run the unit tests
go vet ./...                                           # static checks
```

Cross-compile for another OS/arch by setting `GOOS`/`GOARCH` — no extra
toolchain needed because there is no cgo:

```sh
GOOS=windows GOARCH=amd64 go build -o cyclecount.exe .
GOOS=darwin  GOARCH=arm64 go build -o cyclecount-macos-arm64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o cyclecount-linux-arm64 .
```

(If a stray `.git` in a parent directory makes `go build` complain about VCS
stamping, prefix with `GOFLAGS=-buildvcs=false`.)

## Usage

```
cyclecount [flags] <file>
```

`<file>` is a single input — assembler source, or a compiled object the tool
disassembles (see [Input](#input-source-or-compiled-binary)). Flags **must
precede** the file (Go's flag parser stops at the first operand), so
`cyclecount delay.S -v` is wrong; write `cyclecount -v delay.S`.

### Quick start

```sh
# 1. analyze a source file with the default target (ATtiny3217 / AVRxt)
cyclecount examples/delay.S

# 2. pick your real part and add a clock for wall-clock time
cyclecount -mcu atmega328p -clock 16 examples/delay.S

# 3. see the per-instruction breakdown
cyclecount -mcu atmega328p -v examples/delay.S

# 4. focus on a hot loop and give its trip count
cyclecount -from .L_inner -to .L_inner_end -iter 1000 examples/delay.S

# 5. analyze one function/symbol directly
cyclecount -func delay_ticks examples/delay.S
cyclecount -mcu attiny3217 -symbol toggle_pin firmware.o

# 6. compare the same code across multiple targets
cyclecount -mcu attiny3217,atmega328p,attiny10 examples/delay.S

# 7. prove an optimisation against a baseline
cyclecount -vs examples/scale_mul.S examples/scale_shift.S

# 8. gate it in CI — non-zero exit if the loop blows its cycle budget
cyclecount -from loop -to done -max-cycles 40 hot.S
```

### Flags

The full set (all optional; sensible defaults shown in parentheses):

| flag         | meaning                                                       |
|--------------|---------------------------------------------------------------|
| `-mcu P`     | target part number (e.g. `attiny3217`, `atmega328p`, `avr128da48`) |
| `-core V`    | CPU variant directly: `avr`, `avre`, `avre+`, `avrxm`, `avrxt`, `avrrc` |
| `-pc N`      | program-counter width in bytes: `2` (16-bit) or `3` (22-bit)  |
| `-format F`  | output format: `text`, `json`, `csv`, `md`, `gha`, `sarif`     |
| `-v`         | per-instruction listing with word/cycle costs                 |
| `-clock MHz` | also report wall-clock time (default `20`; `0` disables)      |
| `-from L` / `-to L` / `-iter N` | analyze an explicit label range × N iterations |
| `-branches M` | conditional timing mode: `bounds`, `best`, `worst`, `taken`, `not-taken` |
| `-branch-scenario S` | explicit branch choices/trips: `key=taken`, `key=not-taken`, or `key=N` |
| `-path-visits N` | max visits per instruction in pruned paths (default `1`) |
| `-call-target S` | explicit unresolved/indirect call targets: `key=symbol` |
| `-rank N` / `-rank-by M` | rank top `N` symbol/region spans by `cycles`, `flash`, or `stack` |
| `-symbol L` / `-func L` | analyze one symbol/function from its label to the next non-local symbol |
| `-vs FILE`   | diff the target file against a baseline (same core)           |
| `-json`      | machine-readable output                                       |
| `-objdump P` | objdump binary for binary input (default `avr-objdump`)       |
| `-max-cycles N` / `-max-flash N` / `-max-sram N` | CI budget gate: exit 3 if a cost exceeds the limit |
| `-cpp`       | run the C preprocessor over `.S`/`.s` source before parsing   |
| `-cc P`      | compiler driver used for `-cpp` (default `avr-gcc`)           |
| `-D NAME=VAL` / `-I DIR` | preprocessor define / include dir for `-cpp` (repeatable) |
| `-list-mcus` | print the known part numbers and family fallbacks, then exit  |
| `-version`   | print the build version, then exit                            |

`-list-mcus` and `-version` exit without reading a file. `-list-mcus` shows the
curated part table (part → core → PC width) plus the naming-pattern fallbacks;
any AVR it does not list can still be analyzed with `-core` (and `-pc 3` for
parts over 128 KB of flash).

`-mcu` and `-core` also accept comma-separated lists for a multi-target
comparison run, for example `-mcu attiny3217,atmega328p,attiny10` or
`-core avre+,avrxt,avrrc`. In that mode the same file/span is analyzed once per
target and printed as a comparison matrix. Multi-target mode does not combine
with `-vs`, `-v`, or `-max-*` budgets.

`-format` defaults to `text`. `json` matches the existing machine-readable
object mode (and `-json` remains as a compatibility alias). `csv` emits flat
rows for spreadsheets or CI parsing, `md` emits Markdown tables, and `gha`
emits GitHub Actions group/error annotations around a Markdown summary.
`sarif` emits SARIF 2.1.0 findings for code-scanning style consumers.
Non-text formats do not support `-v`.

`-branches` defaults to `bounds`, which preserves the existing min/max report.
`best` and `worst` force each conditional branch/skip to its cheaper or more
expensive timing; `taken` and `not-taken` force the taken/fallthrough timing of
conditional instructions. `taken` / `not-taken` now also follow direct,
resolvable branches/jumps and stop on `RET` or a repeated instruction. On
acyclic control flow, `best` / `worst` now also prune to the cheaper or more
expensive downstream path; on cyclic spans they fall back to timing-only
selection rather than pretending to prove an unbounded loop path.

`-branch-scenario` overrides the preset for specific branches/skips. Keys can be
a branch's nearest label, its source line as `line:N`, or a branch target label.
Values are `taken`, `not-taken`, or a positive trip count. A trip count on a
loop target, for example `-branch-scenario loop=10`, takes the branch nine times
and falls through once; cycles follow that bounded path while instruction count
and flash remain the static reachable footprint. `-path-visits N` is a coarse
guard for repeated visits in pruned paths.

`-call-target` resolves an otherwise unresolved or indirect call site when you
know the callee, for example `-call-target dispatch=handler` or
`-call-target line:42=handler`. Recursive or cyclic call edges are detected and
reported as unbounded diagnostics; they are not silently converted into a false
finite stack proof.

`-rank N` surfaces the top `N` costly top-level symbol spans and annotated
regions in the current file. `-rank-by cycles` sorts by worst-case cycle total,
`flash` by flash bytes, and `stack` by peak stack bytes. Ranking is single-file,
single-target only, and does not combine with `-vs`.

### Reading the report

A plain run prints a header, one block per analyzed span, the static-SRAM
tally, and footnotes:

```
$ cyclecount examples/delay.S
AVR cycle & size report — examples/delay.S
Target: ATtiny3217 (core AVRxt, 16-bit PC).      ← resolved target + PC width
Cycle data: AVR Instruction Set Manual DS40002198C.
Clock: 20 MHz.                                    ← from -clock (omit with -clock 0)

== (whole file) ==                               ← every span gets a block
  Instructions   : 8                             ← instructions valid on this core
  Flash          : 18 bytes (9 words)            ← code (+ inline flash data, if any)
  Cycles/pass    : 17 – 18  (min–max)            ← honest bounds; branches vary
  Time @20MHz    : 850.0 ns – 900.0 ns           ← cycles ÷ clock
  Stack (SRAM)   : peak 1 B (1 PUSH / 1 POP)     ← local push depth; call sites add return-address bytes

== inner ==                                      ← an @begin/@end annotated region
  Instructions   : 2
  Flash          : 4 bytes (2 words)
  Cycles/pass    : 3 – 4  (min–max)
  Cycles × 1000  : 3000 – 4000                   ← × the region's iter=1000
  Time @20MHz    : 150.000 µs – 200.000 µs

== static SRAM (.data/.bss/.noinit) ==
  Allocated      : 32 bytes                      ← deterministic RAM (not stack)
    .bss       : 32 B                            ← per-section breakdown

Notes: branch/skip cycles are min–max; the exact path depends on data.
LD/LDD/ST/STD/LDS/STS add 1 cycle when the access targets NVM (manual note 2).
```

Add `-v` for a per-instruction table (line, mnemonic, operands, words, cycles,
and a note such as which cores differ):

```
$ cyclecount -v examples/scale_mul.S
  line  mnemonic  operands  words  cycles  note
  ----  --------  --------  -----  ------  ----
  7     LDI       r25, 10   1      1
  8     MUL       r24, r25  1      2       hardware multiplier
  9     MOV       r24, r0   1      1
  10    CLR       r1        1      1
  11    RET                 1      4       16-bit PC; +1 cycle on 22-bit-PC parts
```

Instructions are classified per target and flagged when they need attention:
**✘ unavailable** (exists on other cores, not this one — won't assemble),
**● not modeled** (recognised, but the cycle count is data/programming
dependent, e.g. `SPM`), **⚠ unrecognised** (not in the ISA table — check syntax).

If you select `-branches best|worst|taken|not-taken`, the report header records
that mode. `taken` / `not-taken` prune the executed path for direct resolvable
branches/jumps. `best` / `worst` do the same on acyclic spans, but they still
are not a full control-flow-graph proof.

### Exit codes

`cyclecount` uses its exit status so scripts and CI can branch on it:

| code | meaning |
|------|---------|
| `0`  | success — analysis printed (and all budgets, if any, within limits) |
| `1`  | tool error — file not found, `avr-objdump`/`avr-gcc` missing or failed, bad label |
| `2`  | usage error — no file given, or a flag placed after the file |
| `3`  | a cost **budget was exceeded** (see below) — analysis still printed |

### Gate a build on a cost budget

For CI, set a ceiling and let a regression fail the build. Any exceeded limit
prints an `EXCEEDED` line and makes `cyclecount` exit **3** (distinct from `1`
for tool errors and `2` for usage), so a Makefile or workflow step stops:

```sh
# the hot loop must stay within 40 cycles per pass, the image within 8 KB flash
cyclecount -mcu attiny3217 -from loop -to done -max-cycles 40 hot.S
cyclecount -mcu atmega328p -max-flash 8192 -max-sram 2048 firmware.o
```

`-max-cycles` gates the worst-case total of the primary span — `-symbol` /
`-func` when given, else the `-from`/`-to` range (× `iter`) when given,
otherwise the whole file. `-max-flash` and
`-max-sram` gate the whole-file flash footprint and static `.data`/`.bss` use.
The budget result is also included in `-json` output under `"budgets"`.
Budgets work alongside `-vs` too: they gate the target (new) file's cost while
the diff is reported, so a comparison run still fails the build on exit `3`.

A budget run still prints the full report, then a `== budget ==` summary:

```
== budget ==
  cycles   : 44 / 40 limit — EXCEEDED (range loop:done)
  Verdict: budget exceeded (exit 3).
```

Drop it into CI — the non-zero exit fails the job automatically:

```yaml
# .github/workflows/timing.yml
- name: enforce hot-path budget
  run: |
    go build -o cyclecount .
    ./cyclecount -mcu attiny3217 -from loop -to done \
      -iter 1000 -max-cycles 4000 firmware.S
```

### Expand the C preprocessor on source

By default the source parser skips `#`-lines, so a `.S` that relies on
`#include`, `#define`, or `#if` is analyzed with those unresolved (and **both**
arms of an `#if/#else` counted). Pass `-cpp` to run the source through the
compiler driver's preprocessor (`avr-gcc -E -x assembler-with-cpp`) first:

```sh
cyclecount -mcu atmega328p -cpp -D F_CPU=16000000 blink.S
cyclecount -mcu attiny3217 -cpp -I ./include firmware.S
```

`-cpp` passes `-mmcu=` so `<avr/io.h>` and register macros resolve to the right
device: the `-mcu` part when given, otherwise the default target's part
(`attiny3217`) so the documented default still works. A bare `-core` (no
specific device) passes no `-mmcu`. This covers the **C** preprocessor only —
GNU-as `.macro`/`.include` are expanded by the assembler, not cpp, so for those
keep analyzing a compiled `.o`/ELF.

### Input: source *or* compiled binary

The input file is auto-detected, so the same flags work either way:

| input | how it's read |
|-------|---------------|
| `.S` / `.s` | parsed as GNU-assembler source (incl. `avr-gcc -S` output) |
| ELF / `.o` | disassembled with `avr-objdump -h -d` |
| `.hex` | disassembled with `avr-objdump -h -D -b ihex` |
| saved `avr-objdump -h -d` text | parsed as disassembly directly |

Analyzing the **compiled** object sidesteps the source parser's blind spots —
macros, `.include`, and the C preprocessor are already resolved — and the
objdump `-h` section table gives real `.data`/`.bss` sizes:

```sh
avr-gcc -mmcu=attiny3217 -O2 -c firmware.c -o firmware.o
cyclecount -mcu attiny3217 firmware.o          # exact instruction stream
cyclecount -mcu attiny3217 -func toggle_pin firmware.o
cyclecount -mcu attiny3217 -from main -to loop firmware.o
```

Hot-path `@begin`/`@end` annotations are a source-mode feature (they live in
comments the assembler strips); for binaries use `-from`/`-to` with the symbol
names objdump prints, or analyze the whole file.

`-symbol` / `-func` selects the span that starts at the named label and ends at
the next non-local symbol (or EOF). That matches objdump function symbols and
typical top-level source labels while ignoring interior `.L...` labels.

### Mark a hot path

```asm
        ; @begin inner iter=1000
.L_inner:
        sbiw    r24, 1
        brne    .L_inner
        ; @end inner
```

```
$ cyclecount examples/delay.S
== inner ==
  Cycles/pass    : 3 – 4  (min–max)
  Cycles × 1000  : 3000 – 4000
  Time @20MHz    : 150.000 µs – 200.000 µs
```

### Compare two implementations

```
$ cyclecount -vs examples/scale_mul.S examples/scale_shift.S
  Verdict: costs 1 more cycles (worst case), costs 4 more flash bytes, same SRAM bytes.
```

### Compare multiple targets in one run

```sh
cyclecount -mcu attiny3217,atmega328p,attiny10 examples/delay.S
cyclecount -core avre+,avrxt,avrrc -func delay_ticks firmware.o
```

This prints one row per target for the selected scope (whole file, `-from`/`-to`
range, or `-symbol` / `-func` span), so timing and availability differences are
visible side by side.

### Machine-readable output (`-json`)

`-json` prints the same numbers as a single JSON object instead of text — for
dashboards, regression tracking, or piping into `jq`. It carries the resolved
`target`, the `whole_file` metrics, any annotated `regions`, an optional
`range` (with `-from`/`-to`), an optional `symbol` (with `-symbol` / `-func`),
`sram_static_bytes`, and a `budgets` array when
limits are set. With `-vs` it emits a comparison object (`new`, `baseline`,
`delta`) instead. In multi-target mode it emits a `"mode": "multi-target"`
object with one entry per target.

```sh
cyclecount -json -mcu atmega328p firmware.o | jq '.whole_file.cycles_max'
```

```json
{
  "file": "examples/scale_mul.S",
  "target": { "name": "ATtiny3217", "core": "AVRxt", "pc_bits": 16 },
  "whole_file": {
    "name": "(whole file)", "iter": 1,
    "instructions": 5, "flash_words": 5, "flash_bytes": 10,
    "cycles_min": 9, "cycles_max": 9,
    "cycles_min_total": 9, "cycles_max_total": 9,
    "peak_stack_bytes": 0, "pushes": 0, "pops": 0, "calls": 0
  },
  "sram_static_bytes": 0,
  "budgets": [
    { "name": "flash", "limit": 16, "got": 10, "unit": " B",
      "scope": "whole file", "ok": true }
  ]
}
```

When a budget is exceeded the JSON is still printed in full (with `"ok": false`)
and the process exits `3`, so CI sees both the data and the failure.

### Other output formats

```sh
cyclecount -format csv firmware.o
cyclecount -format md -func toggle_pin firmware.o
cyclecount -format gha -max-cycles 40 hot.S
cyclecount -format sarif -max-cycles 40 hot.S
cyclecount -rank 5 -rank-by cycles firmware.o
```

- `csv` is flat and script-friendly: single-target runs emit one row per
  whole-file/region/range/symbol span; comparisons and multi-target runs emit
  one row per metric or target.
- `md` emits Markdown headings and tables suitable for issue comments, docs, or
  pasted benchmark notes.
- `gha` wraps the Markdown summary in GitHub Actions log groups and emits
  `::error` annotations for exceeded budgets.
- `sarif` emits findings for exceeded budgets plus unavailable, unmodeled, and
  unknown instructions with target/scope metadata. It is aimed at code scanning
  and machine ingestion rather than full metric presentation.

### Rank costly spans

```sh
cyclecount -rank 5 firmware.o
cyclecount -rank 10 -rank-by flash examples/delay.S
cyclecount -rank 5 -rank-by stack firmware.o
```

Ranking walks top-level symbol spans plus any annotated `@begin`/`@end` regions
already present in the file and reports the costliest entries by the selected
metric.

## What it measures

- **Instructions** valid for the selected core (whole file, each annotated
  region, or a `-from`/`-to` label range).
- **Cycles** per pass as a `min – max` range — branches/skips are
  data-dependent, so bounds are reported honestly.
- **Flash**: instruction words × 2 (per-core; `LDS`/`STS` are 1 word on the
  Reduced Core) plus inline data in a flash section.
- **SRAM (static)**: `.data`/`.bss`/`.noinit` allocations.
- **SRAM (stack)**: peak local `PUSH` depth, plus return-address/callee stack
  bytes at the deepest direct intra-file call site (2 or 3 return-address
  bytes, per PC width).

Each instruction is also classified per target (`✘` unavailable, `●` not
modeled, `⚠` unrecognised) — see [Reading the report](#reading-the-report).

## Limitations

- It is a listing analyser, not a simulator. Branch presets and
  `-branch-scenario` select bounded paths through direct, resolvable control
  flow; data-dependent paths still need an explicit scenario or remain bounds.
- Call-stack reporting follows direct intra-file calls into known top-level
  symbols and local labels, accepts explicit `-call-target` mappings for
  unresolved/indirect call sites, and reports recursive/cyclic call edges as
  unbounded diagnostics rather than expanding indefinitely.
- `LD`/`ST`/`LDD` addressing-mode timing on AVRxm and AVRrc is given as a range
  (the manual splits it by mode); AVRe and AVRxt are exact.
- In **source** mode, the C preprocessor is applied only with `-cpp`; GNU-as
  `.macro`/`.include` are never expanded — feed a compiled `.o`/ELF (which
  `cyclecount` disassembles) when you need assembler macros resolved.
- The device list is curated, not exhaustive — any unlisted AVR works via
  `-core` (and `-pc` for >128 KB parts).

## Layout

```
main.go                  CLI, target selection, reports, JSON, comparison
internal/isa/isa.go      per-core instruction timing + availability (DS40002198C)
internal/isa/device.go   device helpers + family fallbacks (`go generate` entrypoint)
internal/isa/device_gen.go generated exact-device database from Appendix A
internal/asm/asm.go      .S parser: lines, sections, data sizes, annotations
internal/asm/objdump.go  binary front end: ELF/.o/.hex via avr-objdump
internal/asm/cpp.go      C-preprocessor front end (-cpp) via avr-gcc -E
internal/analyze/        instruction/cycle/flash/SRAM accounting per target
examples/                delay.S, scale_mul.S, scale_shift.S
cmd/gen-avr-device-db/   Appendix-A PDF/text → generated device database
```
