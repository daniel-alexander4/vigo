// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"fmt"

	"git.alexander4.io/vigo/server/conditionals"
	"gopkg.in/yaml.v3"
)

// LintRolesFile validates a roles.vgo (the single fleet-wide file
// that defines named roles, each grouping modules into a reusable
// profile).
//
// Checks applied:
//   - Each role has a non-empty `name:`.
//   - Role names are unique within the file.
//   - `when:` parses (both role-level and per-case).
//   - `case:` entries each have a `when:`.
//   - `includes:` is a list of strings (caught at parse time by yaml).
//   - A role isn't entirely empty (no modules AND no includes AND
//     no case entries — the role would be a no-op).
//
// Cross-file resolution (does the included role exist? do listed
// modules exist in stacks/modules/?) is Phase 6.
func LintRolesFile(input []byte, _ Options) *Result {
	r := &Result{}
	repaired := repair(input, r)

	rf, err := parseRolesFile(repaired)
	if err != nil {
		r.Findings = append(r.Findings, Finding{
			Severity: Error,
			RuleID:   "rolesfile-parse",
			Message:  fmt.Sprintf("roles file parse error: %s", err),
		})
		finalizeFindings(r)
		return r
	}

	r.Findings = append(r.Findings, applyRolesFileRules(rf)...)
	r.Output = string(repaired)

	finalizeFindings(r)
	return r
}

type roleCase struct {
	When         string
	Configcrates []string
	Line         int
	Column       int
}

type roleEntry struct {
	Name            string
	When            string
	Includes        []string
	HasConfigcrates bool // whether the modules: key was present (even if empty)
	NumConfigcrates int
	Cases           []roleCase
	Line            int
	Column          int
}

type rolesFile struct {
	Roles []roleEntry
}

func parseRolesFile(data []byte) (*rolesFile, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping at root")
	}

	rf := &rolesFile{}
	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]
		if key != "roles" || val.Kind != yaml.SequenceNode {
			continue
		}
		for _, blk := range val.Content {
			if blk.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("roles[]: expected mapping")
			}
			role := roleEntry{Line: blk.Line, Column: blk.Column}
			for j := 0; j < len(blk.Content)-1; j += 2 {
				k := blk.Content[j].Value
				v := blk.Content[j+1]
				switch k {
				case "name":
					role.Name = v.Value
				case "when":
					role.When = v.Value
				case "includes":
					if v.Kind == yaml.SequenceNode {
						for _, item := range v.Content {
							role.Includes = append(role.Includes, item.Value)
						}
					}
				case "configcrates":
					role.HasConfigcrates = true
					if v.Kind == yaml.SequenceNode {
						role.NumConfigcrates = len(v.Content)
					}
				case "case":
					if v.Kind != yaml.SequenceNode {
						continue
					}
					for _, c := range v.Content {
						if c.Kind != yaml.MappingNode {
							continue
						}
						rc := roleCase{Line: c.Line, Column: c.Column}
						for k2 := 0; k2 < len(c.Content)-1; k2 += 2 {
							ck := c.Content[k2].Value
							cv := c.Content[k2+1]
							switch ck {
							case "when":
								rc.When = cv.Value
							case "configcrates":
								if cv.Kind == yaml.SequenceNode {
									for _, m := range cv.Content {
										rc.Configcrates = append(rc.Configcrates, m.Value)
									}
								}
							}
						}
						role.Cases = append(role.Cases, rc)
					}
				}
			}
			rf.Roles = append(rf.Roles, role)
		}
	}
	return rf, nil
}

func applyRolesFileRules(rf *rolesFile) []Finding {
	var out []Finding

	seenNames := make(map[string]int)
	for i, role := range rf.Roles {
		// Required: name.
		if role.Name == "" {
			out = append(out, Finding{
				Severity: Error,
				Line:     role.Line,
				Column:   role.Column,
				RuleID:   "rolesfile-name-required",
				Message:  fmt.Sprintf("roles[%d]: 'name' is required", i),
			})
			continue // can't check duplicates without a name
		}

		// Duplicate name.
		if firstIdx, dup := seenNames[role.Name]; dup {
			out = append(out, Finding{
				Severity: Error,
				Line:     role.Line,
				Column:   role.Column,
				RuleID:   "rolesfile-duplicate-name",
				Message: fmt.Sprintf(
					"roles[%d]: duplicate role name %q (first seen at roles[%d])",
					i, role.Name, firstIdx,
				),
			})
		} else {
			seenNames[role.Name] = i
		}

		// when: parses.
		if role.When != "" {
			if _, err := conditionals.ParseWhenExpr(role.When); err != nil {
				out = append(out, Finding{
					Severity: Error,
					Line:     role.Line,
					Column:   role.Column,
					RuleID:   "rolesfile-when-syntax",
					Message: fmt.Sprintf(
						"role %q: when: expression is malformed — %s",
						role.Name, err.Error(),
					),
				})
			}
		}

		// case[]: each requires when:.
		for ci, c := range role.Cases {
			if c.When == "" {
				out = append(out, Finding{
					Severity: Error,
					Line:     c.Line,
					Column:   c.Column,
					RuleID:   "rolesfile-case-when-required",
					Message: fmt.Sprintf(
						"role %q: case[%d] missing 'when:' — every case branch needs a guard",
						role.Name, ci,
					),
				})
				continue
			}
			if _, err := conditionals.ParseWhenExpr(c.When); err != nil {
				out = append(out, Finding{
					Severity: Error,
					Line:     c.Line,
					Column:   c.Column,
					RuleID:   "rolesfile-case-when-syntax",
					Message: fmt.Sprintf(
						"role %q: case[%d] when: expression is malformed — %s",
						role.Name, ci, err.Error(),
					),
				})
			}
		}

		// Empty role: no modules, no includes, no case → the role is
		// a no-op. Probably an authoring mistake.
		if role.NumConfigcrates == 0 && len(role.Includes) == 0 && len(role.Cases) == 0 {
			out = append(out, Finding{
				Severity: Warning,
				Line:     role.Line,
				Column:   role.Column,
				RuleID:   "rolesfile-empty-role",
				Message: fmt.Sprintf(
					"role %q is empty — no modules, no includes, no case entries; assigning this role does nothing",
					role.Name,
				),
			})
		}
	}
	return out
}
