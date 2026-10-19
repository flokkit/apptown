package mcpapi

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
)

// Call contains transport-established identity and attribution, never services.
type Call struct {
	Context context.Context
	BaseURL string
}

func (c *Call) principal() access.Principal { v, _ := access.CallerFrom(c.Context); return v.Principal }
func (c *Call) actor() string               { v, _ := access.CallerFrom(c.Context); return v.Receipt }

type Refusal string

func (r Refusal) Error() string { return string(r) }

type Tool struct {
	order             int // stable descriptor order, declared with the tool rather than a second catalog
	Name, Description string
	InputSchema       map[string]any
	command           toolCommand
}

// toolCommand binds its policy declaration to the implementation. No exported
// raw function lets the transport invoke a policy-less tool.
type toolCommand struct {
	kind, contract string
	run            func(*Call, map[string]any) (any, error)
}

func ownerCommand(contract string, run func(*Call, map[string]any) (any, error)) toolCommand {
	return toolCommand{"owner", contract, run}
}
func filteredCommand(contract string, run func(*Call, map[string]any) (any, error)) toolCommand {
	return toolCommand{"filtered", contract, run}
}
func publicCommand(contract string, run func(*Call, map[string]any) (any, error)) toolCommand {
	return toolCommand{"public", contract, run}
}
func (t Tool) validate() error {
	if t.Name == "" || t.command.run == nil || strings.TrimSpace(t.command.contract) == "" {
		return fmt.Errorf("tool %q requires a name, command and policy contract", t.Name)
	}
	switch t.command.kind {
	case "owner", "filtered", "public":
		return nil
	}
	return fmt.Errorf("tool %q has no explicit policy", t.Name)
}
func (t Tool) Run(c *Call, args map[string]any) (any, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	return t.command.run(c, args)
}

// Policy identifies the reviewed command boundary, not an inferred permission.
func (t Tool) Policy() (kind, contract string) { return t.command.kind, t.command.contract }

type Catalog []Tool

func (c Catalog) ByName(name string) *Tool {
	for i := range c {
		if c[i].Name == name {
			return &c[i]
		}
	}
	return nil
}
func (c Catalog) Descriptors() []map[string]any {
	out := make([]map[string]any, 0, len(c))
	for _, t := range c {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema})
	}
	return out
}
func refuse(format string, args ...any) error { return Refusal(fmt.Sprintf(format, args...)) }
func (t *Tool) CheckArgs(args map[string]any) string {
	props, _ := t.InputSchema["properties"].(map[string]any)
	for k := range args {
		if _, ok := props[k]; !ok {
			return fmt.Sprintf("unknown argument %q", k)
		}
	}
	if req, ok := t.InputSchema["required"].([]string); ok {
		for _, k := range req {
			if _, ok := args[k]; !ok {
				return fmt.Sprintf("%q is required", k)
			}
		}
	}
	return ""
}
func schemaObject(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	} else {
		s["required"] = []string{}
	}
	return s
}
func schemaString(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func schemaInt(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func schemaID(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}
func argID(args map[string]any, key string) (ids.ID, error) {
	switch v := args[key].(type) {
	case string:
		id, err := ids.Parse(strings.TrimSpace(v))
		if err != nil || id.IsZero() {
			return ids.Nil, refuse("%s must be an id, got %q", key, v)
		}
		return id, nil
	case nil:
		return ids.Nil, refuse("%s is required", key)
	}
	return ids.Nil, refuse("%s must be an id (a string)", key)
}

// argNumber reads a positive whole number: a version, not a record.
func argNumber(args map[string]any, key string) (int64, error) {
	switch v := args[key].(type) {
	case float64:
		if v != float64(int64(v)) || v <= 0 {
			return 0, refuse("%s must be a positive whole number", key)
		}
		return int64(v), nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n <= 0 {
			return 0, refuse("%s must be a positive whole number, got %q", key, v)
		}
		return n, nil
	case nil:
		return 0, refuse("%s is required", key)
	}
	return 0, refuse("%s must be a number", key)
}

var slugArg = schemaString("The project's slug — the short name in its URL, e.g. \"reading-list\".")

// Assemble preserves published descriptor order across independently bound groups.
func Assemble(groups ...[]Tool) Catalog {
	var out Catalog
	for _, group := range groups {
		out = append(out, group...)
	}
	seen := make(map[string]bool)
	for _, t := range out {
		if err := t.validate(); err != nil {
			panic(err)
		}
		if seen[t.Name] {
			panic(fmt.Errorf("duplicate MCP tool %q", t.Name))
		}
		seen[t.Name] = true
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}
func Instructions() string {
	return strings.TrimSpace(`
This is AppTown's user surface for a coding agent. You act as the person who connected you, within what they allowed.
Read: projects, Changes, diffs, evidence, runs, what is waiting on the person (get_waiting_on_you), what a pack version's checks would say about projects (get_what_if, hypothetical), how a model or pack version did against what projects run now (get_eval), and how a long operation such as a data copy or take-down stands (get_operation).
Before writing code on a project, get_project_guidance is strongly recommended: it returns the ways of working its landing checks hold every Change to, at the pinned versions AppTown's own agents are steered by. It is offered, sized (get_project shows the size), and never pushed into your context.
Do: create a project (create_project), open a Change (create_change), put AppTown's own agent on it (start_change_lane), run its checks, boot its preview, hand it off for approval (request_change_landing), send it back with a note, or abandon it.
Work locally: checkout_change gives you a repository URL and a short-lived credential for one Change branch; push with git, then call sync_change. If AppTown itself gets in your way (a tool fails, a documented promise does not hold, you had to guess), file_complaint — even if you found a workaround. You cannot approve, land, administer, or write main; the person does those in AppTown.
Every answer carries a webUrl you can hand the person.`)
}
