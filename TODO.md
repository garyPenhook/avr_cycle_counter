# TODO

- Done: add symbol/function-scoped analysis for source and binary input via `-func foo` / `-symbol foo`.
- Done: add multi-target comparison in one run via comma-separated `-mcu` or `-core` lists.
- Partly done: add branch-assumption modes via `-branches bounds|best|worst|taken|not-taken`.
- Partly done: `taken` / `not-taken` now prune direct resolvable branches/jumps.
- Still open: explicit scenario selection for branch behavior beyond the current mode presets.
- Still open: fuller CFG/path pruning beyond direct resolvable branch/jump heuristics.
- Still open: better loop handling in pruned-path modes beyond the current repeated-instruction stop rule.
- Partly done: model peak local push depth plus direct intra-file callee stack at call sites.
- Still open: full interprocedural stack modeling across arbitrary callees / nested call chains.
- Still open: better indirect/dynamic call handling for stack estimation (`ICALL` / unresolved targets).
- Done: add `-format csv|md|gha|sarif` alongside existing text/json output.
- Partly done: broaden device coverage with family-pattern fallback matching.
- Still open: generate or import the MCU-to-core/PC-width database instead of maintaining curated/fallback rules only.
- Done: add `-rank N` / `-rank-by cycles|flash|stack` for symbol/region ranking.

## Audit findings (2026-06-08) — all fixed 2026-06-08

### Correctness
- [x] **`-vs` baseline ignores `-branches` mode.** Baseline now analyzed with
      `AnalyzeMode(blines, target, mode)` using the parsed mode, so identical
      files report Δ 0 under any branch mode.
- [x] **`isCondBranch` misclassified `BREAK`.** Now excludes `BREAK` explicitly.
- [x] **Stack peak ignored branch pruning.** `stackPeaks` takes an `executed` set
      applied to the top frame; PUSH/CALL on pruned paths no longer count.
      Regression test: `TestStackPeakRespectsBranchPruning`.
- [x] **`-vs` + `-symbol`/`-from`/`-to` now rejected** (comparison is whole-file
      only) instead of silently mixing scopes with budgets.

### Robustness / validation
- [x] **`-clock` < 0 rejected**; `firstInt` rejects negative reservations so
      `.space -1` no longer yields negative SRAM.
- [x] **`stringBytes` escape sizing fixed.** `\xHH`, octal `\NNN`, and `\c` each
      count as one byte. Test: `TestDataBytes`.
- [x] **Budgets now shown in compare MD/CSV** (MD budget table; CSV `budget:*`
      rows with limit/got/delta), matching JSON/GHA.

### Testing
- [x] **`internal/isa` tests added** (cycles, availability, word counts, variant
      parsing) — coverage 0% → ~91%. asm 47% → 62%.
