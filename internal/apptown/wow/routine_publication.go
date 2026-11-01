package wow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// RoutinePublicationTransition selects existing immutable publications, never
// supplied content or a moving recommended version.
type RoutinePublicationTransition struct {
	Kind string `json:"kind"`
	ID   ids.ID `json:"id"`
	From int64  `json:"from"`
	To   int64  `json:"to"`
}

// RoutinePublicationQuestion retains the complete material the person judges.
// Before and After are owner-produced publication snapshots, not request input.
type RoutinePublicationQuestion struct {
	Transition RoutinePublicationTransition `json:"transition"`
	Before     json.RawMessage              `json:"before"`
	After      json.RawMessage              `json:"after"`
	Eligible   bool                         `json:"eligible"`
	Reason     string                       `json:"reason"`
	Shown      string                       `json:"shown"`
	Digest     string                       `json:"digest"`
}

type routinePublication struct {
	Kind          string               `json:"kind"`
	ID            ids.ID               `json:"id"`
	PublicationID ids.ID               `json:"publicationId"`
	Version       int64                `json:"version"`
	Name          string               `json:"name"`
	Source        string               `json:"source"`
	Signer        string               `json:"signer"`
	Body          string               `json:"body,omitempty"`
	Checks        json.RawMessage      `json:"checks,omitempty"`
	Spec          json.RawMessage      `json:"spec,omitempty"`
	Declaration   json.RawMessage      `json:"declaration,omitempty"`
	Pins          PinSet               `json:"pins"`
	References    []routinePublication `json:"references,omitempty"`
}

func routineJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("publication has trailing JSON")
	}
	return nil
}
func routineJSONBytes(raw []byte) (json.RawMessage, error) {
	var value any
	if err := routineJSON(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// All reads use the caller's transaction. Historical absence is never repaired
// by reading today's latest version or current shipped content.
func routinePublicationIn(ctx context.Context, q database.Queryer, kind string, id ids.ID, version int64, depth int) (routinePublication, error) {
	p := routinePublication{Kind: kind, ID: id, Version: version}
	if id.IsZero() || version <= 0 || depth > 32 {
		return p, fault.New(fault.Conflict, "publication identity or inclusion depth is unsupported")
	}
	queries := wowsql.New(q.SQL())
	switch kind {
	case "block":
		owner, err := queries.BlockByID(ctx, id)
		if err != nil {
			return p, database.Missing(err)
		}
		row, err := queries.BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: id, Version: version})
		if err != nil {
			return p, database.Missing(err)
		}
		p.Name, p.PublicationID, p.Source, p.Signer, p.Body = owner.Name, row.ID, row.Source, row.Signer, row.BodyMd
		p.Checks, err = routineJSONBytes(row.ChecksJson)
		if err != nil {
			return p, err
		}
	case "workflow":
		// Workflow owner IDs are observed through the exact named reference below;
		// the stored ID is checked again rather than accepting a name replacement.
		return p, fault.New(fault.Conflict, "standalone workflow transitions are unsupported")
	case "pack":
		owner, err := queries.PackByID(ctx, id)
		if err != nil {
			return p, database.Missing(err)
		}
		row, err := queries.PackVersion(ctx, wowsql.PackVersionParams{PackID: id, Version: version})
		if err != nil {
			return p, database.Missing(err)
		}
		p.Name, p.PublicationID, p.Source, p.Signer = owner.Name, row.ID, row.Source, row.Signer
		var spec PackSpec
		if err = routineJSON(row.SpecJson, &spec); err != nil {
			return p, err
		}
		if err = routineJSON(row.PinsJson, &p.Pins); err != nil {
			return p, err
		}
		if len(p.Pins.Policies) != len(spec.Policies) {
			return p, fault.New(fault.Conflict, "publication policy inventory is incomplete")
		}
		for _, policy := range spec.Policies {
			if pinned, ok := p.Pins.Policies[policy.Name]; !ok || canonJSON(pinned) != policyPin(policy) {
				return p, fault.New(fault.Conflict, "publication policy content is not pinned")
			}
		}
		p.Spec, err = routineJSONBytes(row.SpecJson)
		if err != nil {
			return p, err
		}
		p.Declaration, err = routineJSONBytes(row.DeclarationJson)
		if err != nil {
			return p, err
		}
		if len(p.Pins.Blocks) != len(spec.Guidance) || len(p.Pins.Workflows) != len(spec.Workflows) {
			return p, fault.New(fault.Conflict, "publication has unresolved or extra component pins")
		}
		for _, name := range spec.Guidance {
			v, ok := p.Pins.Blocks[name]
			if !ok || v <= 0 {
				return p, fault.New(fault.Conflict, "publication guidance is not pinned")
			}
			b, e := queries.BlockByName(ctx, name)
			if e != nil {
				return p, database.Missing(e)
			}
			child, e := routinePublicationIn(ctx, q, "block", b.ID, int64(v), depth+1)
			if e != nil {
				return p, e
			}
			p.References = append(p.References, child)
		}
		for _, name := range spec.Workflows {
			v, ok := p.Pins.Workflows[name]
			if !ok || v <= 0 {
				return p, fault.New(fault.Conflict, "publication workflow is not pinned")
			}
			owner, e := queries.WorkflowByName(ctx, name)
			if e != nil {
				return p, database.Missing(e)
			}
			row, e := queries.WorkflowVersion(ctx, wowsql.WorkflowVersionParams{Name: name, Version: int64(v)})
			if e != nil {
				return p, database.Missing(e)
			}
			if row.LibID != owner.ID || row.Version != int64(v) {
				return p, fault.New(fault.Conflict, "workflow publication identity changed")
			}
			if row.Source == "" || (row.Source != sourceHere && row.Signer == "") {
				return p, fault.New(fault.Conflict, "workflow publication provenance is unavailable")
			}
			p.References = append(p.References, routinePublication{Kind: "workflow", ID: owner.ID, PublicationID: row.ID, Version: row.Version, Name: name, Source: row.Source, Signer: row.Signer, Body: row.Yaml})
		}
		sort.Slice(p.References, func(i, j int) bool {
			a, b := p.References[i], p.References[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return a.Name < b.Name
		})
	default:
		return p, fault.New(fault.Conflict, "publication kind is unsupported")
	}
	if p.PublicationID.IsZero() || p.Source == "" || (p.Source != sourceHere && p.Signer == "") {
		return p, fault.New(fault.Conflict, "publication provenance is unavailable")
	}
	return p, nil
}

func routinePublicationDelta(before, after routinePublication) (bool, string) {
	if before.Kind != after.Kind || before.ID != after.ID || before.Name != after.Name || before.Source != after.Source {
		return false, "publication owner or source changed"
	}
	if before.Version > after.Version {
		return false, "publication moved backwards"
	}
	if before.Version == after.Version {
		if !reflect.DeepEqual(before, after) {
			return false, "an unchanged publication has different material"
		}
		return false, ""
	}
	switch before.Kind {
	case "block":
		if !bytes.Equal(before.Checks, after.Checks) {
			return false, "executable check definitions changed"
		}
		return before.Body != after.Body, ""
	case "pack":
		if !bytes.Equal(before.Spec, after.Spec) || !bytes.Equal(before.Declaration, after.Declaration) {
			return false, "pack declaration, membership, runtime defaults or authority changed"
		}
		if !reflect.DeepEqual(before.Pins.Workflows, after.Pins.Workflows) || !reflect.DeepEqual(before.Pins.Policies, after.Pins.Policies) {
			return false, "workflow or policy pins changed"
		}
		if len(before.References) != len(after.References) {
			return false, "publication component inventory changed"
		}
		changed := false
		for i, b := range before.References {
			c, reason := routinePublicationDelta(b, after.References[i])
			if reason != "" {
				return false, reason
			}
			changed = changed || c
		}
		return changed, ""
	default:
		return false, "workflow executable or approval publication changed"
	}
}

func routineQuestionIn(ctx context.Context, q database.Queryer, in RoutinePublicationTransition) (RoutinePublicationQuestion, error) {
	out := RoutinePublicationQuestion{Transition: in}
	if (in.Kind != "block" && in.Kind != "pack") || in.ID.IsZero() || in.From <= 0 || in.To <= in.From {
		return out, fault.New(fault.Conflict, "choose two forward versions of one guidance block or pack")
	}
	before, err := routinePublicationIn(ctx, q, in.Kind, in.ID, in.From, 0)
	if err != nil {
		return out, err
	}
	after, err := routinePublicationIn(ctx, q, in.Kind, in.ID, in.To, 0)
	if err != nil {
		return out, err
	}
	out.Before, err = json.Marshal(before)
	if err != nil {
		return out, err
	}
	out.After, err = json.Marshal(after)
	if err != nil {
		return out, err
	}
	out.Eligible, out.Reason = routinePublicationDelta(before, after)
	if !out.Eligible && out.Reason == "" {
		out.Reason = "no supported guidance-body change was observed"
	}
	out.Shown = fmt.Sprintf("Classify the displayed %s %s v%d to v%d publication transition as routine guidance. I have reviewed the complete old and new text. This does not approve a project change, recommend a pack, land work or deploy software. The classification is revocable; a claimed effect may finish after revocation.", in.Kind, before.Name, in.From, in.To)
	out.Digest, err = routineQuestionDigest(out)
	return out, err
}

func routineQuestionDigest(question RoutinePublicationQuestion) (string, error) {
	question.Digest = ""
	raw, err := json.Marshal(question)
	if err != nil {
		return "", err
	}
	raw, err = routineJSONBytes(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
