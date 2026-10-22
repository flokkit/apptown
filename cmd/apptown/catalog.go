package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// The change catalog (design/decisions/change-catalog.md): named change
// definitions the release ships. `apptown catalog` lists them, `apptown
// catalog <name>` shows one, and `apptown apply` runs one on a project.

func cmdCatalog(args []string) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return cmdCatalogShow(args[0])
	}
	raw, err := api.request("GET", "/catalog", nil)
	if err != nil {
		return err
	}
	var out struct {
		Catalog []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if len(out.Catalog) == 0 {
		fmt.Println("the change catalog is empty")
		return nil
	}
	for _, c := range out.Catalog {
		fmt.Printf("%-28s %s\n", c.Name, c.Description)
	}
	return nil
}

func cmdCatalogShow(name string) error {
	raw, err := api.request("GET", "/catalog/"+name, nil)
	if err != nil {
		return err
	}
	var out struct {
		Change struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"change"`
		Definition struct {
			Params []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Required    bool   `json:"required"`
				Default     string `json:"default"`
			} `json:"params"`
			Workflow       string `json:"workflow"`
			Model          string `json:"model"`
			Thinking       string `json:"thinking"`
			TimeoutMinutes int    `json:"timeoutMinutes"`
			Prompt         string `json:"prompt"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	c, d := out.Change, out.Definition
	fmt.Printf("%s — %s\n", c.Name, c.Description)
	fmt.Printf("workflow: %s", d.Workflow)
	if d.Model != "" {
		fmt.Printf(" · model pin: %s", d.Model)
	}
	if d.Thinking != "" {
		fmt.Printf(" · thinking: %s", d.Thinking)
	}
	if d.TimeoutMinutes > 0 {
		fmt.Printf(" · timeout: %dm", d.TimeoutMinutes)
	}
	fmt.Println()
	for _, p := range d.Params {
		req := "optional"
		if p.Required {
			req = "required"
		}
		if p.Default != "" {
			req += ", default " + p.Default
		}
		fmt.Printf("param --%s (%s): %s\n", p.Name, req, p.Description)
	}
	if strings.TrimSpace(d.Prompt) != "" {
		fmt.Printf("\n--- prompt template ---\n%s\n", strings.TrimSpace(d.Prompt))
	}
	return nil
}

// paramFlags collects repeated --param k=v flags.
type paramFlags map[string]string

func (p paramFlags) String() string { return "" }

func (p paramFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return usagef("--param wants k=v, got %q", v)
	}
	p[k] = val
	return nil
}

func cmdApply(args []string) error {
	fs := newFlags("apply")
	project := fs.String("p", "", "project slug")
	params := paramFlags{}
	fs.Var(params, "param", "parameter k=v (repeatable)")
	args = parseArgs(fs, args)
	if len(args) == 0 {
		return usagef("usage: apptown apply <catalog-change> [-p proj] [--param k=v]")
	}
	slug, err := resolveProject(*project)
	if err != nil {
		return err
	}
	// Inside a console session the read-only port only sanctions the
	// session's own actuator — catalog applies route through it (kicking off
	// catalog changes is one of the project chat's jobs).
	path := "/catalog-apply/" + args[0]
	body := map[string]any{"project": slug, "params": map[string]string(params)}
	if sid := os.Getenv("APPTOWN_SESSION_ID"); sid != "" && os.Getenv("APPTOWN_SESSION_TOKEN") != "" {
		path = "/console/" + sid + "/invoke"
		body = map[string]any{"catalog": args[0], "params": map[string]string(params)}
	}
	raw, err := api.request("POST", path, body)
	if err != nil {
		return err
	}
	var out struct {
		Change struct {
			ID     ids.ID
			Number int64
		}
		Run     struct{ ID ids.ID }
		Catalog string `json:"catalog"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	fmt.Printf("▶ %s on %s · change %s · run %s\n", args[0], slug, changeName(slug, out.Change.Number, out.Change.ID), out.Run.ID)
	if out.URL != "" {
		fmt.Printf("  link: %s\n", out.URL)
	}
	fmt.Printf("  follow: apptown follow %d\n", out.Change.ID)
	return nil
}
