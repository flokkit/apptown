package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Scope records the endpoint AppTown observed at one configured API base. It is
// not a globally unique Portainer server identity or evidence of stack ownership.
type Scope struct {
	Version    int    `json:"version"`
	APIBase    string `json:"apiBase"`
	EndpointID int64  `json:"endpointId"`
	DaemonID   string `json:"daemonId"`
}

// canonicalAPIBase compares configured destinations without resolving DNS or
// sending credentials. Base paths remain significant; aliases are not equivalent.
func canonicalAPIBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid Portainer API base")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return "", errors.New("Portainer API base must be an HTTP(S) origin and optional path without credentials, query, fragment or ambiguous path escaping")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Host = host
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}

// ObserveScope observes the explicitly selected standalone Docker endpoint using
// one already resolved credential snapshot. Initial association authorizes this
// probe; existing associations must call VerifyScope instead.
func ObserveScope(ctx context.Context, cfg Target, endpointID int64) (Scope, error) {
	base, err := canonicalAPIBase(cfg.PortainerURL)
	if err != nil {
		return Scope{}, err
	}
	if endpointID <= 0 {
		return Scope{}, errors.New("Portainer endpoint ID is required")
	}
	cfg.PortainerURL = base
	code, raw, err := JSON(ctx, &cfg, "GET", fmt.Sprintf("/api/endpoints/%d", endpointID), nil)
	if err != nil {
		return Scope{}, fmt.Errorf("observing Portainer endpoint: %w", err)
	}
	if code != 200 {
		return Scope{}, fmt.Errorf("observing Portainer endpoint: HTTP %d", code)
	}
	var endpoint struct {
		ID   int64 `json:"Id"`
		Type int   `json:"Type"`
	}
	if err := json.Unmarshal(raw, &endpoint); err != nil {
		return Scope{}, errors.New("Portainer endpoint observation is malformed")
	}
	// Portainer types 1 and 2 are Docker reached directly or through an agent.
	// Edge and Kubernetes environments do not provide this synchronous scope.
	if endpoint.ID != endpointID || (endpoint.Type != 1 && endpoint.Type != 2) {
		return Scope{}, errors.New("verified Portainer scope requires the selected standalone Docker endpoint")
	}
	code, raw, err = JSON(ctx, &cfg, "GET", fmt.Sprintf("/api/endpoints/%d/docker/info", endpointID), nil)
	if err != nil {
		return Scope{}, fmt.Errorf("observing Portainer Docker identity: %w", err)
	}
	if code != 200 {
		return Scope{}, fmt.Errorf("observing Portainer Docker identity: HTTP %d", code)
	}
	var info struct {
		ID    string
		Swarm struct {
			NodeID           string
			LocalNodeState   string
			ControlAvailable bool
		}
	}
	if err := json.Unmarshal(raw, &info); err != nil || strings.TrimSpace(info.ID) == "" {
		return Scope{}, errors.New("Portainer returned no usable Docker identity")
	}
	if info.Swarm.NodeID != "" || info.Swarm.ControlAvailable || (info.Swarm.LocalNodeState != "" && info.Swarm.LocalNodeState != "inactive") {
		return Scope{}, errors.New("verified Portainer scope requires standalone Docker; the selected daemon belongs to a Swarm cluster")
	}
	return Scope{Version: 1, APIBase: base, EndpointID: endpointID, DaemonID: info.ID}, nil
}

// VerifyScope refuses a changed destination before authenticated I/O, then
// verifies remote facts with the same snapshot the caller must use for effects.
func VerifyScope(ctx context.Context, cfg Target, expected Scope) error {
	if expected.Version != 1 || expected.EndpointID <= 0 || expected.DaemonID == "" {
		return errors.New("Portainer destination has no verified scope")
	}
	base, err := canonicalAPIBase(cfg.PortainerURL)
	if err != nil {
		return err
	}
	if base != expected.APIBase {
		return errors.New("Portainer API base differs from its recorded scope")
	}
	observed, err := ObserveScope(ctx, cfg, expected.EndpointID)
	if err != nil {
		return err
	}
	if observed != expected {
		return errors.New("Portainer endpoint differs from its recorded scope")
	}
	return nil
}
