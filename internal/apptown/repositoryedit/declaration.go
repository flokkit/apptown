package repositoryedit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/flokkit/apptown/internal/apptown/scm"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"gopkg.in/yaml.v3"
)

// editDeclaration returns the project's .apptown/app.yaml bytes with its
// `needs:` set to the given picks (design/decisions/forge-native-contract.md).
// It edits needs only; the app's parts are the app's and change through a
// Change like any other source (an editor that wrote raw image parts could
// delete every non-public image part it had not been sent, removing an
// app's worker). A need already declared
// keeps its entry as written, including value types, except for the params
// sent with a value different from what the contract reads now; a need not sent is removed. With no contract it authors a minimal
// public part when the user declares a first need.
func editDeclaration(repo string, needs []NeedSelection) ([]byte, error) {
	var root map[string]any
	current := &appcontract.Contract{}
	// Read without following a link the repository holds: what is read is
	// committed back into the repository (scm.ReadInTree).
	if raw, err := scm.ReadInTree(repo, filepath.Join(".apptown", "app.yaml")); err == nil {
		if current, err = appcontract.Parse(raw); err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("the existing app.yaml did not parse: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if len(needs) == 0 {
		return nil, fmt.Errorf("this project has no runtime contract yet — build the app first")
	} else {
		// No contract, but a dependency is being DECLARED — this will be a
		// server. Synthesize a minimal public part the agent fleshes out.
		root = map[string]any{"app": map[string]any{
			"web": map[string]any{"build": ".", "ports": []any{"8080"}, "public": true},
		}}
	}
	existing, _ := root["needs"].(map[string]any)
	needMap := map[string]any{}
	for _, n := range needs {
		if _, dup := needMap[n.Capability]; dup {
			return nil, fmt.Errorf("need %q is declared twice", n.Capability)
		}
		was, _ := existing[n.Capability].(map[string]any)
		entry := maps.Clone(was)
		if entry == nil {
			entry = map[string]any{}
		}
		for k, v := range n.Params {
			if was, ok := current.NeedParams[n.Capability][k]; ok && was == fmt.Sprint(v) {
				continue
			}
			entry[k] = v
		}
		needMap[n.Capability] = entry
	}
	if existing != nil && reflect.DeepEqual(needMap, existing) || existing == nil && len(needMap) == 0 {
		return nil, ErrNeedsUnchanged
	}
	if len(needMap) > 0 {
		root["needs"] = needMap
	} else {
		delete(root, "needs")
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	edited, err := appcontract.Parse(out)
	if err != nil {
		return nil, fmt.Errorf("the edited app.yaml is not valid: %w", err)
	}
	// What was asked for is what the contract now says.
	for _, n := range needs {
		for k, v := range n.Params {
			if got := edited.NeedParams[n.Capability][k]; got != fmt.Sprint(v) {
				return nil, fmt.Errorf("need %s: %s would read %q, not %q; edit .apptown/app.yaml through a change", n.Capability, k, got, fmt.Sprint(v))
			}
		}
	}
	return out, nil
}

// ErrNeedsUnchanged refuses an edit that would declare what is declared now.
var ErrNeedsUnchanged = errors.New("these are the services declared now; nothing to change")

// DeclarationVersion names the app.yaml an edit starts from: a hash of its
// bytes, or "" when there is none.
func DeclarationVersion(repo string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(repo, ".apptown", "app.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func needNames(needs []NeedSelection) []string {
	out := make([]string, 0, len(needs))
	for _, n := range needs {
		out = append(out, n.Capability)
	}
	sort.Strings(out)
	return out
}
