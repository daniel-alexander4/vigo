# Requirements: Isogov

**Defined:** 2026-03-07
**Core Value:** Managed nodes converge to their declared desired state reliably, idempotently, and securely — with full audit visibility into what changed, when, and why.

## v1.2 Requirements

Requirements for v1.2 Structured Conditionals. Each maps to roadmap phases.

### Conditional Vars

- [ ] **COND-01**: Admin can define conditional vars with list matching (`only`, `not`)
- [ ] **COND-02**: Admin can define conditional vars with numeric comparisons (`gt`, `lt`, `gte`, `lte`)
- [ ] **COND-03**: Admin can define conditional vars with regex matching (`match`)
- [ ] **COND-04**: Conditional vars evaluate to true/false and can be referenced by name in all four conditional fields

### Resource Conditionals

- [ ] **COND-05**: `go_if_all_pass:` — resource runs when ALL listed conditions are true (string or list)
- [ ] **COND-06**: `go_if_any_pass:` — resource runs when at least one listed condition is true (list)
- [ ] **COND-07**: `skip_if_all_pass:` — resource skipped when ALL listed conditions are true (string or list)
- [ ] **COND-08**: `skip_if_any_pass:` — resource skipped when ANY listed condition is true (list)
- [ ] **COND-09**: All four fields combinable on a single resource (all constraints must hold)
- [ ] **COND-10**: Existing `when:` field is removed and replaced by the new conditional fields

### Conditional Blocks

- [ ] **COND-11**: `conditional_block:` wraps resources with shared conditional fields
- [ ] **COND-12**: Resources inside blocks can have their own conditionals (both block and resource must pass)
- [ ] **COND-13**: `depends_on` works across `conditional_block:` boundaries

### Module Conditionals

- [ ] **COND-14**: Conditional fields on module references in roles YAML to skip entire modules
- [ ] **COND-15**: Conditional fields on module references in clients.yaml to skip entire modules

## Future Requirements

None planned.

## Out of Scope

| Feature | Reason |
|---------|--------|
| Freeform Go template `when:` expressions | Replaced by structured conditional fields — no arbitrary template logic |
| Nested conditional blocks | Single level of block nesting is sufficient; deeper nesting adds complexity |
| OR logic at the var definition level mixing traits | Use multiple vars and `go_if_any_pass:` instead |
| Conditional fields on individual role references | Roles are organizational groupings, not execution units — condition modules instead |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| COND-01 | TBD | Pending |
| COND-02 | TBD | Pending |
| COND-03 | TBD | Pending |
| COND-04 | TBD | Pending |
| COND-05 | TBD | Pending |
| COND-06 | TBD | Pending |
| COND-07 | TBD | Pending |
| COND-08 | TBD | Pending |
| COND-09 | TBD | Pending |
| COND-10 | TBD | Pending |
| COND-11 | TBD | Pending |
| COND-12 | TBD | Pending |
| COND-13 | TBD | Pending |
| COND-14 | TBD | Pending |
| COND-15 | TBD | Pending |

**Coverage:**
- v1.2 requirements: 15 total
- Mapped to phases: 0
- Unmapped: 15

---
*Requirements defined: 2026-03-07*
*Last updated: 2026-03-07 after initial definition*
