---
tags:
  - confidence-high
  - state-domain
---

# configuration_management

Application settings and permissions configuration

**Confidence**: 0.80
**Owners**: settings

## Aggregate

[[symbols/Settings|Settings]]

## Representations

- [[symbols/Permissions|Permissions]]

## Primary Mutators

- [[symbols/LoadSettings|LoadSettings]]

## Primary Readers

- [[symbols/IsDenied|IsDenied]]
- [[symbols/LoadSettings|LoadSettings]]

## Effects

| Kind | Via |
|------|-----|
| fs_read | `internal/settings/settings.go` |
