# Tasks: fix-contracts-hydration-residuals

## 1. Registry hydration parity

- [x] 1.1 Reproduce with a pg parity test: seed contract nodes as the indexer persists them; assert the store-backed fallback registry materializes every entry (writer-vs-follower field parity)
- [x] 1.2 Fix the hydration (kind/meta filter or unbound repo scoping) so follower `contracts {all_repos:true}` totals the store's contract-node count
- [x] 1.3 Live check: follower `contracts` reports 125 on the shared Albatros store

## 2. Inspections payload consistency

- [x] 2.1 Reproduce the cap=200 drop (block absent, summary counts 125) and pin the elision point
- [x] 2.2 Build `summary` from emitted blocks + explicit truncation counts so blocks and summary can never diverge; regression test across cap values (cap < total, cap == total, cap > total)

## 3. Orphans trustworthiness

- [x] 3.1 Gate `contracts_orphans` on registry health (zero-entry registry with existing contract nodes → skipped/unavailable, per the previous change's requirement)
- [x] 3.2 Re-baseline orphan results on the populated registry against the live store; verify matched provider/consumer pairs are not flagged
