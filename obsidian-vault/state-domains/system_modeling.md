---
tags:
  - confidence-high
  - state-domain
---

# system_modeling

System architecture models derived from evidence analysis including domains, boundaries, and trust zones

**Confidence**: 0.90
**Owners**: model

## Aggregate

[[symbols/SystemModel|SystemModel]]

## Representations

- [[symbols/Boundaries|Boundaries]]
- [[symbols/Inventory|Inventory]]
- [[symbols/StateDomain|StateDomain]]

## Primary Mutators

- [[symbols/GenerateSystemModel|GenerateSystemModel]]
- [[symbols/WriteSystemModel|WriteSystemModel]]

## Primary Readers

- [[symbols/ReadSystemModel|ReadSystemModel]]
- [[symbols/SystemModelUpToDate|SystemModelUpToDate]]

## Effects

| Kind | Via |
|------|-----|
| fs_read | `internal/model/model.go` |
| fs_write | `internal/model/model.go` |
