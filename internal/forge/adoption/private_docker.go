package adoption

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/strs"
	yamlv3 "gopkg.in/yaml.v3"
)

// Private Docker is declared by a step (docker: true in its with:), no longer
// given to every step using skill/implementation. DeclarePrivateDocker is the
// one-time migration: it adds docker: true where that rule applied and no
// docker key is written, to the library's workflows (stock and custom) and to
// the versions projects' current adoptions pin. A pack publication a project
// selects that pins an amended workflow gets a publication pinning the amended
// version (wow.Service.AmendPack), so a later adoption keeping the pack keeps
// the declaration. Each project's move, receipt and pack memberships, is one
// revision by MigrationActor. A definition with a docker key is left as it
// is, so running it again changes nothing. Run definitions are never
// rewritten; their steps are refused instead (workflow.StartedUnderOldDockerRule).

// MigrationActor is who a migration's revisions are recorded as made by.
const MigrationActor = "system/migration"

// Amender records an amended workflow version or pack publication (wow.Service).
type Amender interface {
	AmendWorkflow(ctx context.Context, name string, from int, yamlSrc, who string) (int, error)
	AmendPack(ctx context.Context, name string, from int64, pins, who string) (int64, error)
}

func (s *Service) DeclarePrivateDocker(ctx context.Context, ways Amender) error {
	if s.deps.Ways == nil {
		return ErrCatalogUnavailable
	}
	amended := map[string]int{} // name@version → its amendment, 0 when none is needed
	amend := func(name string, version int) (int, error) {
		key := fmt.Sprintf("%s@%d", name, version)
		if to, done := amended[key]; done {
			return to, nil
		}
		w, err := s.deps.Ways.Workflow(ctx, wow.WorkflowRef{Name: name, Version: version})
		if err != nil {
			return 0, err
		}
		declared, changed, err := declarePrivateDocker(w.YAML)
		to := 0
		if err == nil && changed {
			to, err = ways.AmendWorkflow(ctx, name, version, declared, MigrationActor)
		}
		if err != nil {
			return 0, fmt.Errorf("workflow %s v%d: %w", name, version, err)
		}
		amended[key] = to
		if to != 0 {
			log.Printf("private Docker: workflow %s v%d declared as v%d", name, version, to)
		}
		return to, nil
	}
	amendedPacks := map[string]int64{} // name@version → the publication pinning amended workflows
	amendPack := func(name string, version int64) (int64, error) {
		key := fmt.Sprintf("%s@%d", name, version)
		if to, done := amendedPacks[key]; done {
			return to, nil
		}
		p, err := s.deps.Ways.PackPublication(ctx, name, version)
		var pins wow.PinSet
		if err == nil {
			err = json.Unmarshal([]byte(p.PinsJSON), &pins)
		}
		changed := false
		for w, v := range pins.Workflows {
			to, e := amend(w, v)
			if err = errors.Join(err, e); to != 0 {
				pins.Workflows[w], changed = to, true
			}
		}
		to := version
		if err == nil && changed {
			var amended string
			if amended, err = strs.JSON(pins); err == nil {
				to, err = ways.AmendPack(ctx, name, version, canonJSON(amended), MigrationActor)
			}
		}
		if err != nil {
			return 0, fmt.Errorf("pack %s v%d: %w", name, version, err)
		}
		amendedPacks[key] = to
		if to != version {
			log.Printf("private Docker: pack %s v%d pins the declared workflows as v%d", name, version, to)
		}
		return to, nil
	}
	latest, err := s.deps.Ways.Workflows(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, w := range latest {
		if _, err := amend(w.Name, w.Version); err != nil {
			errs = append(errs, err)
		}
	}
	projects, err := s.ProjectsWithRevisions(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, id := range projects {
		current, err := s.adoptions.Current(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("project %d: %w", id, err))
			continue
		}
		repin := map[string][2]int{} // name → [pinned, amended]
		var names []string
		for _, w := range current.Receipt.Workflows {
			to, err := amend(w.Name, w.Version)
			if err != nil {
				errs = append(errs, fmt.Errorf("project %d: %w", id, err))
			} else if to != 0 {
				repin[w.Name] = [2]int{w.Version, to}
				names = append(names, fmt.Sprintf("%s v%d→v%d", w.Name, w.Version, to))
			}
		}
		memberships, err := ProjectPacksIn(ctx, s.db, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("project %d: %w", id, err))
			continue
		}
		var picks []Membership
		moved := false
		for _, m := range memberships {
			to, err := amendPack(m.Pack, m.Version)
			if err != nil {
				errs = append(errs, fmt.Errorf("project %d: %w", id, err))
				to = m.Version
			} else if to != m.Version {
				moved = true
				names = append(names, fmt.Sprintf("pack %s v%d→v%d", m.Pack, m.Version, to))
			}
			picks = append(picks, Membership{Pack: m.Pack, Version: to})
		}
		if len(repin) == 0 && !moved {
			continue
		}
		reason := "private Docker is declared: docker: true added where skill/implementation gave it (" + strings.Join(names, ", ") + ")"
		if _, err := s.Record(ctx, id, AdoptionEdit, reason, MigrationActor, func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
			if prior == nil {
				return Receipt{}, fmt.Errorf("project %d has no current adoption", id)
			}
			if moved {
				if err := SetMembershipIn(ctx, tx, id, picks, reason, MigrationActor, now); err != nil {
					return Receipt{}, err
				}
			}
			r := cloneReceipt(prior.Receipt)
			for i, w := range r.Workflows {
				p, ok := repin[w.Name]
				if !ok || w.Version != p[0] {
					continue
				}
				amendedVersion, err := s.deps.Ways.Workflow(ctx, wow.WorkflowRef{Name: w.Name, Version: p[1]})
				if err != nil {
					return Receipt{}, err
				}
				r.Workflows[i].Version, r.Workflows[i].YAMLHash = p[1], HashText(amendedVersion.YAML)
				if at := strings.LastIndex(w.Origin, "@"); at > 0 {
					r.Workflows[i].Origin = fmt.Sprintf("%s@%d", w.Origin[:at], p[1])
				}
			}
			return r, nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("project %d: %w", id, err))
			continue
		}
		log.Printf("private Docker: project %d: %s", id, reason)
	}
	return errors.Join(errs...)
}

// declarePrivateDocker adds docker: true to every with: the old rule gave
// private Docker, and says whether it added any. A with: that is an alias
// becomes a copy of its mapping with the declaration, so the alias's other
// uses are unchanged. The result is read again to confirm nothing is left.
func declarePrivateDocker(src string) (string, bool, error) {
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal([]byte(src), &doc); err != nil {
		return "", false, err
	}
	if oldDockerWiths(&doc, true) == 0 {
		return src, false, nil
	}
	var out bytes.Buffer
	enc := yamlv3.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", false, err
	}
	if err := enc.Close(); err != nil {
		return "", false, err
	}
	var again yamlv3.Node
	if err := yamlv3.Unmarshal(out.Bytes(), &again); err != nil {
		return "", false, err
	}
	if n := oldDockerWiths(&again, false); n != 0 {
		return "", false, fmt.Errorf("%d with: still undeclared after adding docker: true", n)
	}
	return out.String(), true, nil
}

// oldDockerWiths counts the with: mappings the old rule applies to, in
// document order, declaring each when declare is set; an alias used as a
// with: is declared on a copy of its mapping.
func oldDockerWiths(n *yamlv3.Node, declare bool) int {
	count := 0
	for i := 0; n.Kind == yamlv3.MappingNode && i+1 < len(n.Content); i += 2 {
		var with map[string]any
		if n.Content[i].Value != "with" || n.Content[i+1].Decode(&with) != nil || !schema.UsesOldDockerRule(with) {
			continue
		}
		count++
		if !declare {
			continue
		}
		if v := n.Content[i+1]; v.Kind == yamlv3.AliasNode && v.Alias != nil {
			copied := *v.Alias
			copied.Anchor, copied.Content = "", slices.Clone(copied.Content)
			n.Content[i+1] = &copied
		}
		n.Content[i+1].Content = append(n.Content[i+1].Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: "docker"},
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "true"})
	}
	for _, c := range n.Content {
		count += oldDockerWiths(c, declare)
	}
	return count
}
