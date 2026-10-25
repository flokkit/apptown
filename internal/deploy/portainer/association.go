package portainer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

// StackAssociation is a present, explicit association of an existing stack. It
// confers no creation/deletion ownership and cannot replace deployment_owned.
type StackAssociation struct {
	DeploymentID ids.ID `json:"deploymentId"`
	Scope        Scope  `json:"scope"`
	StackID      int64  `json:"stackId"`
	StackName    string `json:"stackName"`
}

// MarkCompose binds a newly rendered stack to the deployment that admitted it.
// Compose ignores extension fields; the adapter reads this one before updates.
func MarkCompose(compose string, deployment ids.ID) (string, error) {
	if deployment.IsZero() {
		return "", errors.New("Portainer compose needs a deployment identity")
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
		return "", err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("Portainer compose is not a mapping")
	}
	root := document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "x-apptown-deployment" {
			return "", errors.New("Portainer compose already contains a deployment marker")
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "x-apptown-deployment"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: deployment.String()})
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return encoded.String(), nil
}

// VerifyStackAssociation verifies one already observed numeric stack against its
// endpoint and deployment marker, or an explicitly recorded legacy association.
// The caller still needs its saved creation receipt before deleting this stack.
func VerifyStackAssociation(ctx context.Context, cfg Target, stack Stack, deployment ids.ID, legacy *StackAssociation) error {
	if cfg.Scope == nil || cfg.Scope.Version != 1 || cfg.Scope.EndpointID != stack.EndpointID || stack.ID <= 0 || stack.Name == "" || deployment.IsZero() {
		return errors.New("Portainer stack has no scoped deployment identity")
	}
	content, err := StackFile(ctx, &cfg, fmt.Sprint(stack.ID))
	if err != nil {
		return fmt.Errorf("reading Portainer stack ownership: %w", err)
	}
	marker, err := composeDeployment(content)
	if err != nil {
		return fmt.Errorf("Portainer stack ownership is malformed: %w", err)
	}
	if marker != "" {
		if marker != deployment.String() {
			return errors.New("Portainer stack belongs to a different deployment")
		}
		return nil
	}
	if legacy != nil && legacy.DeploymentID == deployment && legacy.Scope == *cfg.Scope && legacy.StackID == stack.ID && legacy.StackName == stack.Name {
		return nil
	}
	return errors.New("Portainer stack requires explicit legacy association before mutation")
}

// SavedAssociations reads explicit legacy associations without interpreting a
// current stack name as historical evidence.
func SavedAssociations(handle map[string]any) ([]StackAssociation, error) {
	raw, err := json.Marshal(handle["portainerAssociations"])
	if err != nil {
		return nil, err
	}
	var out []StackAssociation
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("saved Portainer associations are malformed")
	}
	return out, nil
}

// composeDeployment distinguishes absent legacy markers from unreadable current
// evidence. A successful response alone does not establish stack ownership.
func composeDeployment(content string) (string, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(content), &document); err != nil {
		return "", err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("compose is not a mapping")
	}
	var marker string
	services := false
	seen := make(map[string]bool)
	root := document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if seen[key.Value] {
			return "", errors.New("compose contains duplicate fields")
		}
		seen[key.Value] = true
		switch key.Value {
		case "services":
			services = value.Kind == yaml.MappingNode
		case "x-apptown-deployment":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return "", errors.New("deployment marker is not a nonempty string")
			}
			marker = value.Value
		}
	}
	if !services {
		return "", errors.New("compose has no services mapping")
	}
	return marker, nil
}
