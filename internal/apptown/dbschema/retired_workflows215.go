package dbschema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

// These shapes and the removed step set are frozen migration inputs. They do
// not use today's workflow reader or receipt types: those contracts can change.
type publication215 struct {
	name    string
	version int
}
type workflow215 struct {
	name, origin, yaml string
	version, latest    int
}
type retirement215 struct {
	publications map[publication215]bool
	names        map[string]bool
}

func (r retirement215) contains(name string, version int) bool {
	return r.names[name] || r.publications[publication215{name, version}]
}

type step215 struct {
	Uses string `yaml:"uses"`
	With struct {
		Workflow string `yaml:"workflow"`
	} `yaml:"with"`
}

func removedWorkflows215(definitions []workflow215) (retirement215, error) {
	removed := retirement215{publications: map[publication215]bool{}, names: map[string]bool{}}
	calls := map[publication215][]string{}
	current := map[string]publication215{}
	for _, w := range definitions {
		key := publication215{w.name, w.version}
		if w.version == w.latest {
			current[w.name] = key
		}
		var doc struct {
			Spec struct {
				Stages []struct {
					OnEnter []step215 `yaml:"onEnter"`
					Steps   []step215 `yaml:"steps"`
				} `yaml:"stages"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(w.yaml), &doc); err != nil {
			return removed, fmt.Errorf("workflow %q version %d during retirement: %w", w.name, w.version, err)
		}
		for _, stage := range doc.Spec.Stages {
			for _, step := range append(stage.OnEnter, stage.Steps...) {
				// deploy is the only uses removed between the 1.0.0-rc.7 baseline and #385.
				if step.Uses == "deploy" {
					removed.publications[key] = true
				}
				if step.Uses == "workflow/call" {
					calls[key] = append(calls[key], step.With.Workflow)
				}
			}
		}
	}
	// Calls resolve the current publication, not the union of a callee's history.
	for changed := true; changed; {
		changed = false
		for key, callees := range calls {
			if removed.publications[key] {
				continue
			}
			for _, callee := range callees {
				if removed.publications[current[callee]] {
					removed.publications[key] = true
					changed = true
					break
				}
			}
		}
	}
	var affected []string
	supported := map[string]bool{}
	for _, w := range definitions {
		if !removed.publications[publication215{w.name, w.version}] {
			supported[w.name] = true
		}
		if w.origin != "stock" && w.version == w.latest && removed.publications[publication215{w.name, w.version}] {
			affected = append(affected, w.name)
		}
	}
	if len(affected) > 0 {
		sort.Strings(affected)
		return removed, fmt.Errorf("cannot upgrade: current authored workflows use the removed deploy step or call an affected workflow: %s. Edit these workflows to use build and repair their calls, or delete them, then upgrade again", strings.Join(affected, ", "))
	}
	for _, w := range definitions {
		if w.origin == "stock" && !supported[w.name] {
			removed.names[w.name] = true
		}
	}
	return removed, nil
}

type receiptWorkflow215 struct {
	Name     string `json:"name"`
	Version  int    `json:"version"`
	Origin   string `json:"origin"`
	YAMLHash string `json:"yamlHash"`
	System   bool   `json:"system,omitempty"`
}
type receipt215 struct {
	Resolver int      `json:"resolver"`
	Packs    []string `json:"packs,omitempty"`
	Guidance []struct {
		Position int    `json:"position"`
		Name     string `json:"name"`
		BlockID  ids.ID `json:"blockId"`
		Version  int    `json:"version"`
		Disabled bool   `json:"disabled,omitempty"`
		Tier     string `json:"tier,omitempty"`
	} `json:"guidance"`
	Workflows []receiptWorkflow215 `json:"workflows"`
	Policies  []struct {
		Name       string `json:"name"`
		Rule       string `json:"rule"`
		ConfigHash string `json:"configHash"`
		Origin     string `json:"origin,omitempty"`
	} `json:"policies"`
	Extensions []string `json:"extensions"`
}
type receiptPatch215 struct {
	id     ids.ID
	body   json.RawMessage
	digest string
}

func cleanReceipt215(body json.RawMessage, removed retirement215) (json.RawMessage, string, bool, error) {
	var r receipt215
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, "", false, err
	}
	keep := make([]receiptWorkflow215, 0, len(r.Workflows))
	for _, w := range r.Workflows {
		if !removed.contains(w.Name, w.Version) {
			keep = append(keep, w)
		}
	}
	if len(keep) == len(r.Workflows) {
		return body, "", false, nil
	}
	r.Workflows = keep
	canonical, err := json.Marshal(r)
	if err != nil {
		return nil, "", false, err
	}
	digest := sha256.Sum256(canonical)
	// Preserve older, otherwise unrelated receipt fields in storage. Digest uses
	// the receipt's supported v1 projection, exactly as the adoption owner does.
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(body, &stored); err != nil {
		return nil, "", false, err
	}
	stored["workflows"], err = json.Marshal(keep)
	if err != nil {
		return nil, "", false, err
	}
	out, err := json.Marshal(stored)
	return out, hex.EncodeToString(digest[:]), true, err
}

func receiptPatches215(ctx context.Context, tx *database.Tx, removed retirement215) ([]receiptPatch215, error) {
	rows, err := tx.SQL().QueryContext(ctx, `SELECT id,receipt_json FROM project_adoptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var patches []receiptPatch215
	for rows.Next() {
		var p receiptPatch215
		if err := rows.Scan(&p.id, &p.body); err != nil {
			return nil, err
		}
		body, digest, changed, err := cleanReceipt215(p.body, removed)
		if err != nil {
			return nil, fmt.Errorf("adoption %s: %w", p.id, err)
		}
		if changed {
			p.body, p.digest = body, digest
			patches = append(patches, p)
		}
	}
	return patches, rows.Err()
}
func candidatePatches215(ctx context.Context, tx *database.Tx, removed retirement215) ([]receiptPatch215, error) {
	rows, err := tx.SQL().QueryContext(ctx, `SELECT id,target_candidate_json FROM changes WHERE target_candidate_json ? 'receipt'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var patches []receiptPatch215
	for rows.Next() {
		var p receiptPatch215
		if err := rows.Scan(&p.id, &p.body); err != nil {
			return nil, err
		}
		var candidate map[string]json.RawMessage
		if err := json.Unmarshal(p.body, &candidate); err != nil {
			return nil, err
		}
		var original receipt215
		if err := json.Unmarshal(candidate["receipt"], &original); err != nil {
			return nil, err
		}
		removedNames := map[string]bool{}
		for _, w := range original.Workflows {
			if removed.contains(w.Name, w.Version) {
				removedNames[w.Name] = true
			}
		}
		body, digest, changed, err := cleanReceipt215(candidate["receipt"], removed)
		if err != nil {
			return nil, fmt.Errorf("change %s candidate: %w", p.id, err)
		}
		if changed {
			candidate["receipt"] = body
			candidate["digest"], err = json.Marshal(digest)
			if err != nil {
				return nil, err
			}
		}
		for _, field := range []string{"lanesAdded", "lanesRemoved", "kept"} {
			if raw, ok := candidate[field]; ok {
				var names []string
				if err := json.Unmarshal(raw, &names); err != nil {
					return nil, err
				}
				kept := make([]string, 0, len(names))
				for _, name := range names {
					key := name
					if field == "kept" {
						key = strings.TrimPrefix(name, "workflow ")
					}
					if !removed.names[key] && !removedNames[key] {
						kept = append(kept, name)
					}
				}
				if len(kept) != len(names) {
					changed = true
					candidate[field], err = json.Marshal(kept)
					if err != nil {
						return nil, err
					}
				}
			}
		}
		if changed {
			p.body, err = json.Marshal(candidate)
			if err != nil {
				return nil, err
			}
			patches = append(patches, p)
		}
	}
	return patches, rows.Err()
}
