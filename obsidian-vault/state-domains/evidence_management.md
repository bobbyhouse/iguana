---
tags:
  - confidence-high
  - state-domain
---

# evidence_management

Management of code analysis evidence bundles containing package metadata, symbols, and call graphs

**Confidence**: 0.90
**Owners**: evidence

## Aggregate

[[symbols/EvidenceBundle|EvidenceBundle]]

## Representations

- [[symbols/FileMeta|FileMeta]]
- [[symbols/PackageMeta|PackageMeta]]
- [[symbols/Symbols|Symbols]]

## Primary Mutators

- [[symbols/CreateEvidenceBundle|CreateEvidenceBundle]]
- [[symbols/WriteEvidenceBundle|WriteEvidenceBundle]]

## Primary Readers

- [[symbols/CreateEvidenceBundle|CreateEvidenceBundle]]

## Effects

| Kind | Via |
|------|-----|
| fs_read | `internal/evidence/generate.go` |
| fs_write | `internal/evidence/generate.go` |
