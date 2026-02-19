// system_model.baml — BAML types and function for system model inference.
//
// The LLM receives compact per-package summaries derived from evidence bundles
// and returns structured inferences: state domains, trust zones, open questions.

class PackageSignals {
  fs_reads bool
  fs_writes bool
  db_calls bool
  net_calls bool
  concurrency bool
}

class PackageSummary {
  name string          // Go package name (e.g. "auth")
  files string[]       // file paths relative to root
  types string[]       // exported struct/interface names (top 10)
  functions string[]   // exported function names (top 10)
  signals PackageSignals
  imports string[]     // distinct imported packages (top 10)
}

class StateDomainSpec {
  id string
  description string
  owners string[]           // package names that own this domain
  aggregate string          // single primary concept name (e.g. "Catalog", "Session")
  representations string[]  // 1-3 related type/interface names beyond the aggregate
  primary_mutators string[] // deduped function names that write/modify this domain
  primary_readers string[]  // deduped function names that read this domain
  @description("0.0-1.0: 1.0=direct fact, 0.8=strong, 0.6=probable, <0.4=open_question")
  confidence float
}

class TrustZoneSpec {
  id string
  packages string[]     // package names in this zone
  external_via string[] // symbols that cross trust boundary (e.g. HTTP clients)
}

class OpenQuestionSpec {
  question string
  related_domain string
  missing_evidence string[]
}

class SystemModelInference {
  state_domains StateDomainSpec[]
  trust_zones TrustZoneSpec[]
  open_questions OpenQuestionSpec[]
}

function InferSystemModel(packages: PackageSummary[]) -> SystemModelInference {
  client "CustomSonnet4"
  prompt #"
  You are a software architect analyzing a Go codebase through static analysis.

  Given package summaries derived from evidence bundles, infer the system's
  logical architecture.

  For STATE DOMAINS: cluster related types into cohesive logical domains (e.g.
  "catalog_management", "session_state"). For each domain:
  - Choose ONE primary aggregate concept name (e.g. "Catalog", "Session")
  - List 1-3 closely related representation types (not the aggregate itself)
  - List primary_mutators: deduplicated functions that write/modify this domain
  - List primary_readers: deduplicated functions that read this domain
  - Confidence < 0.4 → move to open_questions instead

  For TRUST ZONES: group packages by security boundary. "internal" = core
  business logic. "external" = packages making outbound network calls.

  For OPEN QUESTIONS: note what static analysis cannot determine (missing
  schema definitions, unclear data flows, ambiguous ownership).

  Rules:
  - Aim for 1-3 aggregates per domain, not flat lists of 15 symbols
  - Only infer what the evidence supports
  - Sort all arrays alphabetically
  - All type names and function names must exist in the provided summaries
  - Confidence must be between 0.0 and 1.0

  Packages:
  {{ packages }}

  {{ ctx.output_format }}
  "#
}
