package digitalocean

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

const appSpecV2 = "do-app-spec-v2"

type readinessSnapshot struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
	Expect  int    `json:"expect"`
}

type appArtifactEnvelope struct {
	Spec      json.RawMessage `json:"spec"`
	Readiness json.RawMessage `json:"readiness"`
}

type decodedAppArtifact struct {
	Spec      AppSpec
	Wire      json.RawMessage
	Readiness readinessSnapshot
}

// readinessFor selects the public question independently of service order.
// Private services retain native platform probes; their exact status cannot
// be witnessed through the public ingress.
func readinessFor(c *appcontract.Contract, gated bool) (readinessSnapshot, error) {
	out := readinessSnapshot{Version: 1}
	if c == nil {
		return out, nil
	}
	for _, part := range c.Parts {
		if gated && !part.Public {
			continue
		}
		if part.Intent == nil || part.Intent.Readiness == nil {
			continue
		}
		probe := part.Intent.Readiness
		if probe.Expect != 0 && (!part.Public || probe.Expect < 200 || probe.Expect > 299 || probe.HTTP == "") {
			return out, deploy.Terminal(fmt.Errorf("part %s readiness: App Platform supports exact status only for a public HTTP probe expecting 200 through 299", part.Name))
		}
		if part.Public {
			out.Path = probe.HTTP
			out.Expect = probe.Expect
		}
	}
	return out, nil
}

func encodeAppArtifact(spec AppSpec, question readinessSnapshot) (deploy.Artifact, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return deploy.Artifact{}, err
	}
	probe, err := json.Marshal(question)
	if err != nil {
		return deploy.Artifact{}, err
	}
	envelope := appArtifactEnvelope{Spec: body, Readiness: probe}
	body, err = json.Marshal(envelope)
	if err != nil {
		return deploy.Artifact{}, err
	}
	envelope.Spec = json.RawMessage(blankSecrets(spec))
	redacted, err := json.Marshal(envelope)
	if err != nil {
		return deploy.Artifact{}, err
	}
	return deploy.Artifact{Kind: appSpecV2, Body: body, Hash: configHash(string(redacted))}, nil
}

func decodeAppArtifact(art deploy.Artifact) (decodedAppArtifact, error) {
	var out decodedAppArtifact
	switch art.Kind {
	case "do-app-spec":
		out.Wire = art.Body
	case appSpecV2:
		var envelope appArtifactEnvelope
		if err := strictRead(art.Body, &envelope); err != nil {
			return out, err
		}
		var err error
		out.Readiness, err = decodeReadiness(envelope.Readiness)
		if err != nil {
			return out, err
		}
		out.Wire = envelope.Spec
	default:
		return out, fmt.Errorf("the App Platform driver deploys app specs, not %s", art.Kind)
	}
	if len(out.Wire) == 0 || bytes.Equal(bytes.TrimSpace(out.Wire), []byte("null")) {
		return out, errors.New("App Platform artifact has no provider spec")
	}
	if err := json.Unmarshal(out.Wire, &out.Spec); err != nil {
		return out, err
	}
	if out.Spec.Name == "" {
		return out, errors.New("App Platform artifact has no app name")
	}
	if err := oneVersionPerRepository(out.Spec); err != nil {
		return out, err
	}
	if art.Kind == "do-app-spec" {
		out.Readiness.Path = readinessPathOf(out.Spec)
	}
	return out, nil
}

func strictRead(body []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing App Platform artifact data")
	}
	return nil
}

func decodeReadiness(body []byte) (readinessSnapshot, error) {
	var fields struct {
		Version *int    `json:"version"`
		Path    *string `json:"path"`
		Expect  *int    `json:"expect"`
	}
	var out readinessSnapshot
	if err := strictRead(body, &fields); err != nil {
		return out, fmt.Errorf("recorded readiness: %w", err)
	}
	if fields.Version == nil || fields.Path == nil || fields.Expect == nil {
		return out, errors.New("recorded readiness is incomplete")
	}
	out = readinessSnapshot{Version: *fields.Version, Path: *fields.Path, Expect: *fields.Expect}
	if out.Version != 1 || (out.Path != "" && !strings.HasPrefix(out.Path, "/")) || (out.Expect != 0 && (out.Expect < 200 || out.Expect > 299 || out.Path == "")) {
		return out, errors.New("recorded readiness is unsupported")
	}
	return out, nil
}

func checkpointReadiness(noted map[string]any) (readinessSnapshot, error) {
	kind, present := noted["artifactKind"]
	if present && kind != appSpecV2 && kind != "do-app-spec" {
		return readinessSnapshot{}, errors.New("recorded App Platform artifact kind is unsupported")
	}
	value, hasQuestion := noted["readiness"]
	if kind == appSpecV2 {
		if !hasQuestion {
			return readinessSnapshot{}, errors.New("recorded App Platform readiness is missing")
		}
		body, err := json.Marshal(value)
		if err != nil {
			return readinessSnapshot{}, err
		}
		return decodeReadiness(body)
	}
	if hasQuestion {
		return readinessSnapshot{}, errors.New("recorded readiness has no matching artifact format")
	}
	path, _ := noted["readinessPath"].(string)
	return readinessSnapshot{Path: path}, nil
}
