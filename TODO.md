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
