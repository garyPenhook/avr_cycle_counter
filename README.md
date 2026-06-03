# cyclecount

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
| `avr`   | ATtiny11/12/15/26 (original 1995 core) | no MOVW/MUL/CALL/JMP |
| `avre`  | classic ATtiny (13/25/45/85, 2313 …) | same timing as AVR, + MOVW |
| `avre+` | classic ATmega / AT90 (328P, 2560, 32U4 …) | + multiply |
| `avrxm` | XMEGA (ATxmega…) | RMW (XCH/LAx), faster ST/PUSH |
| `avrxt` | tinyAVR 0/1/2, megaAVR 0, AVR Dx/Ex/Du | 1-cycle ST/PUSH/SBI, 3-cycle CALL |
| `avrrc` | Reduced Core (ATtiny4/5/9/10/20/40/102/104) | 16 registers, no ADIW/MUL/CALL |

Pick a target three ways (default is **ATtiny3217 / AVRxt**):

```sh
cyclecount -mcu atmega328p   file.S   # a known part number
cyclecount -core avrrc       file.S   # a CPU variant directly — works for ANY AVR
cyclecount -core avre+ -pc 3 file.S   # 22-bit PC override for >128 KB parts
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

## Cycle data provenance

Every cycle count, word size, and per-core availability flag comes from the
**Microchip AVR Instruction Set Manual, DS40002198C**:

- `#Clocks AVRe / AVRxm / AVRxt / AVRrc` columns of the Instruction Set Summary
  (Tables 5-2 … 5-6) and the per-instruction `Cycles` tables.
- **Table 7-1 Core Descriptions** for which instructions exist on each variant.
- **§7.2 device tables** (Appendix A) for the part-number → core / PC-width map.

The data lives in plain, readable Go — [`internal/isa/isa.go`](internal/isa/isa.go)
(timing + availability) and [`internal/isa/device.go`](internal/isa/device.go)
(device map). It was cross-checked against Microchip's documentation server.

Call/return/stack figures depend on the **Program Counter width**: ≤128 KB
parts use a 16-bit PC (2-byte return address); larger parts (e.g. ATmega2560)
use a 22-bit PC, which adds one cycle to `CALL`/`RCALL`/`ICALL`/`RET`/`RETI`.

## Build

```sh
go build -trimpath -ldflags="-s -w" -o cyclecount .   # stripped release binary
go test ./...
```

(If a stray `.git` in a parent directory makes `go build` complain about VCS
stamping, prefix with `GOFLAGS=-buildvcs=false`.)

## Usage

```
cyclecount [flags] <file.S>
```

Flags **must precede** the file (Go's flag parser stops at the first operand):

| flag         | meaning                                                       |
|--------------|---------------------------------------------------------------|
| `-mcu P`     | target part number (e.g. `attiny3217`, `atmega328p`, `avr128da48`) |
| `-core V`    | CPU variant directly: `avr`, `avre`, `avre+`, `avrxm`, `avrxt`, `avrrc` |
| `-pc N`      | program-counter width in bytes: `2` (16-bit) or `3` (22-bit)  |
| `-v`         | per-instruction listing with word/cycle costs                 |
| `-clock MHz` | also report wall-clock time (default `20`; `0` disables)      |
| `-from L` / `-to L` / `-iter N` | analyze an explicit label range × N iterations |
| `-vs FILE`   | diff the target file against a baseline `.S` (same core)      |
| `-json`      | machine-readable output                                       |

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

## What it measures

- **Instructions** valid for the selected core (whole file, each annotated
  region, or a `-from`/`-to` label range).
- **Cycles** per pass as a `min – max` range — branches/skips are
  data-dependent, so bounds are reported honestly.
- **Flash**: instruction words × 2 (per-core; `LDS`/`STS` are 1 word on the
  Reduced Core) plus inline data in a flash section.
- **SRAM (static)**: `.data`/`.bss`/`.noinit` allocations.
- **SRAM (stack)**: peak `PUSH` depth and call return-address bytes (2 or 3,
  per PC width).

Instructions are classified per target: **✘ unavailable** (exists on other
cores but not this one — won't assemble), **● not modeled** (recognised but
cycle count is data/programming dependent, e.g. `SPM`), **⚠ unrecognised**.

## Limitations

- It is a listing analyser, not a simulator: it does not follow calls or
  branches, so loop totals come from your `iter=` annotation and cycle ranges
  are bounds, not one executed path.
- `LD`/`ST`/`LDD` addressing-mode timing on AVRxm and AVRrc is given as a range
  (the manual splits it by mode); AVRe and AVRxt are exact.
- Macros and `.include` are not expanded; `#`-preprocessor lines are skipped.
- The device list is curated, not exhaustive — any unlisted AVR works via
  `-core` (and `-pc` for >128 KB parts).

## Layout

```
main.go                  CLI, target selection, reports, JSON, comparison
internal/isa/isa.go      per-core instruction timing + availability (DS40002198C)
internal/isa/device.go   part-number → core / PC-width map
internal/asm/asm.go      .S parser: lines, sections, data sizes, annotations
internal/analyze/        instruction/cycle/flash/SRAM accounting per target
examples/                delay.S, scale_mul.S, scale_shift.S
```
