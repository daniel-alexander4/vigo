# Roadmap: Isogov

## Milestones

- [x] **v1.0 MVP** - Phases 1-4 (shipped 2026-03-06)
- [x] **v1.1 Traits Visibility & Custom Traits** - Phases 5-6 (shipped 2026-03-07)
- [ ] **v1.2 Structured Conditionals** - Phases 7-10 (in progress)

## Phases

<details>
<summary>v1.0 MVP (Phases 1-4) - SHIPPED 2026-03-06</summary>

- [x] Phase 1: Foundation (5/5 plans) — completed 2026-03-06
- [x] Phase 2: Convergence (5/5 plans) — completed 2026-03-06
- [x] Phase 3: Operations (5/5 plans) — completed 2026-03-06
- [x] Phase 4: Web UI & Deployment (4/4 plans) — completed 2026-03-06

</details>

<details>
<summary>v1.1 Traits Visibility & Custom Traits (Phases 5-6) - SHIPPED 2026-03-07</summary>

- [x] Phase 5: Trait Inspection (2/2 plans) — completed 2026-03-07
- [x] Phase 6: Custom Traits (2/2 plans) — completed 2026-03-07

</details>

### v1.2 Structured Conditionals

- [ ] **Phase 7: Conditional Vars** - Define named conditions in module YAML with trait-based operators
- [ ] **Phase 8: Resource Conditionals** - Apply conditional fields to resources, replacing `when:`
- [ ] **Phase 9: Conditional Blocks** - Group resources under shared conditionals with `conditional_block:`
- [ ] **Phase 10: Module Conditionals** - Apply conditional fields to module references in roles and clients.yaml

## Phase Details

### Phase 7: Conditional Vars
**Goal**: Admins can define named boolean conditions in module YAML that evaluate traits with typed operators
**Depends on**: Phase 6 (v1.1 delivered traits and custom traits that conditionals evaluate against)
**Requirements**: COND-01, COND-02, COND-03, COND-04
**Success Criteria** (what must be TRUE):
  1. Admin can define a conditional var with `only:` or `not:` list operators and it evaluates correctly against the agent's traits (e.g., `{ trait: os.distro, only: [debian, ubuntu] }`)
  2. Admin can define a conditional var with numeric operators (`gt`, `lt`, `gte`, `lte`) and it evaluates correctly against numeric trait values
  3. Admin can define a conditional var with `match:` regex operator and it evaluates correctly against string trait values
  4. Conditional vars are referenceable by name from any of the four conditional fields (`go_if_all_pass:`, `go_if_any_pass:`, `skip_if_all_pass:`, `skip_if_any_pass:`) and server resolves them to true/false before sending to agent
**Plans**: TBD

Plans:
- [ ] 07-01: TBD
- [ ] 07-02: TBD

### Phase 8: Resource Conditionals
**Goal**: Resources use the four structured conditional fields to control execution, fully replacing freeform `when:` expressions
**Depends on**: Phase 7
**Requirements**: COND-05, COND-06, COND-07, COND-08, COND-09, COND-10
**Success Criteria** (what must be TRUE):
  1. A resource with `go_if_all_pass:` runs only when every listed condition is true, and a resource with `go_if_any_pass:` runs when at least one is true
  2. A resource with `skip_if_all_pass:` is skipped when every listed condition is true, and a resource with `skip_if_any_pass:` is skipped when any is true
  3. A resource with multiple conditional fields applied simultaneously evaluates all of them (all constraints must hold for the resource to run)
  4. The `when:` field no longer exists in code or config -- all references are removed and replaced by conditional fields
  5. String shorthand works for single conditions (e.g., `go_if_all_pass: is_debian`) alongside list form
**Plans**: TBD

Plans:
- [ ] 08-01: TBD
- [ ] 08-02: TBD

### Phase 9: Conditional Blocks
**Goal**: Admins can group multiple resources under shared conditionals using `conditional_block:` inline wrappers
**Depends on**: Phase 8
**Requirements**: COND-11, COND-12, COND-13
**Success Criteria** (what must be TRUE):
  1. A `conditional_block:` entry in the resources list applies its conditional fields to all child resources, skipping the entire group when conditions fail
  2. Resources inside a conditional block can declare their own conditional fields, and both the block-level and resource-level conditions must pass for the resource to run
  3. `depends_on` references work across `conditional_block:` boundaries -- a resource outside a block can depend on a resource inside, and vice versa
**Plans**: TBD

Plans:
- [ ] 09-01: TBD

### Phase 10: Module Conditionals
**Goal**: Admins can conditionally skip entire modules at the role and client level using the same conditional fields
**Depends on**: Phase 9 (conditional fields and var evaluation fully working)
**Requirements**: COND-14, COND-15
**Success Criteria** (what must be TRUE):
  1. A module reference in a role YAML with conditional fields is skipped entirely when conditions evaluate to skip, and all its resources are excluded from the module list sent to the agent
  2. A module reference in clients.yaml with conditional fields is skipped entirely when conditions evaluate to skip, and all its resources are excluded from the module list sent to the agent
**Plans**: TBD

Plans:
- [ ] 10-01: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 7 -> 8 -> 9 -> 10

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Foundation | v1.0 | 5/5 | Complete | 2026-03-06 |
| 2. Convergence | v1.0 | 5/5 | Complete | 2026-03-06 |
| 3. Operations | v1.0 | 5/5 | Complete | 2026-03-06 |
| 4. Web UI & Deployment | v1.0 | 4/4 | Complete | 2026-03-06 |
| 5. Trait Inspection | v1.1 | 2/2 | Complete | 2026-03-07 |
| 6. Custom Traits | v1.1 | 2/2 | Complete | 2026-03-07 |
| 7. Conditional Vars | v1.2 | 0/0 | Not started | - |
| 8. Resource Conditionals | v1.2 | 0/0 | Not started | - |
| 9. Conditional Blocks | v1.2 | 0/0 | Not started | - |
| 10. Module Conditionals | v1.2 | 0/0 | Not started | - |
