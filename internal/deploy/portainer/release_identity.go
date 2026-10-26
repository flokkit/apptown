package portainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

const (
	releaseDeployment = "io.apptown.deployment"
	releaseGeneration = "io.apptown.generation"
	releaseArtifact   = "io.apptown.artifact"
	releaseComponent  = "io.apptown.component"
)

// expectedRelease is checkpointed before apply. SubmittedHash identifies the
// labeled execution bytes; ArtifactHash identifies the original admitted bytes.
type expectedRelease struct {
	Readiness     Readiness `json:"readiness"`
	Generation    int64     `json:"generation"`
	ArtifactHash  string    `json:"artifactHash"`
	SubmittedHash string    `json:"submittedHash"`
	Scope         Scope     `json:"scope"`
}

func stampRelease(compose string, target ids.ID, generation int64, scope Scope) (string, *expectedRelease, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return "", nil, err
	}
	if len(doc.Content) != 1 {
		return "", nil, errors.New("Portainer artifact is not a document")
	}
	root := doc.Content[0]
	node := mapValue(root, readinessKey)
	if node == nil {
		return compose, nil, nil
	} // historical artifact: never health evidence
	var r Readiness
	if err := node.Decode(&r); err != nil {
		return "", nil, err
	}
	if err := r.validate(); err != nil {
		return "", nil, err
	}
	if r.Target != target || generation <= 0 || scope.Version != 1 || scope.EndpointID <= 0 || scope.DaemonID == "" {
		return "", nil, errors.New("Portainer release execution scope differs from admission")
	}
	marker, err := composeDeployment(compose)
	if err != nil || marker != r.Deployment.String() {
		return "", nil, errors.New("Portainer release deployment differs from admission")
	}
	expected := &expectedRelease{Readiness: r, Generation: generation, ArtifactHash: deploy.ConfigHash(strings.TrimSpace(compose)), Scope: scope}
	services := mapValue(root, "services")
	for _, c := range r.Components {
		service := mapValue(services, c.Service)
		image := mapValue(service, "image")
		if image == nil || image.Value != c.Image {
			return "", nil, errors.New("Portainer release component image differs from admission")
		}
		labels := mapValue(service, "labels")
		if labels == nil {
			labels = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			service.Content = append(service.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "labels"}, labels)
		}
		if labels.Kind != yaml.MappingNode {
			return "", nil, errors.New("Portainer release labels must be a mapping")
		}
		for _, kv := range [][2]string{{releaseDeployment, r.Deployment.String()}, {releaseGeneration, strconv.FormatInt(generation, 10)}, {releaseArtifact, expected.ArtifactHash}, {releaseComponent, c.Service}} {
			if mapValue(labels, kv[0]) != nil {
				return "", nil, errors.New("Portainer artifact already contains execution labels")
			}
			labels.Content = append(labels.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv[0]}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv[1]})
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", nil, err
	}
	if err := enc.Close(); err != nil {
		return "", nil, err
	}
	expected.SubmittedHash = deploy.ConfigHash(strings.TrimSpace(b.String()))
	return b.String(), expected, nil
}

func savedRelease(handle map[string]any) (*expectedRelease, error) {
	raw, ok := handle["releaseReadiness"]
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var expected expectedRelease
	if err := json.Unmarshal(b, &expected); err != nil {
		return nil, err
	}
	if err := expected.Readiness.validate(); err != nil {
		return nil, err
	}
	if expected.Generation <= 0 || expected.ArtifactHash == "" || expected.SubmittedHash == "" || expected.Scope.Version != 1 || expected.Scope.DaemonID == "" {
		return nil, errors.New("Portainer release checkpoint is incomplete")
	}
	return &expected, nil
}
