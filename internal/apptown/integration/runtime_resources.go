package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// ErrorsEnvironment: the runtime contribution — the variables the error
// reporting kind's definition (stock/dependencies/errors.yaml, inject)
// gives an attached project's app containers, with its DSN. Empty when
// unattached (opt-in by attachment), and when the catalog's definition
// injects nothing, which the log names. An attachment or a catalog that
// cannot be read is an error.
func (s *Service) ErrorsEnvironment(ctx context.Context, projectID ids.ID) ([]string, error) {
	dsn, err := s.errorsDSN(ctx, projectID)
	if err != nil || dsn == "" {
		return nil, err
	}
	inject, err := s.errorsInject(ctx)
	if errors.Is(err, errNoErrorsDefinition) {
		log.Printf("error reporting for project %s: %v; its app is given no error-reporting variables", projectID, err)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(inject)) {
		out = append(out, name+"="+strings.ReplaceAll(inject[name], dsnRef, dsn))
	}
	return out, nil
}

// dsnRef is where an inject template takes the DSN.
const dsnRef = "${dsn}"

// errNoErrorsDefinition: the catalog holds no error-reporting definition
// that gives an app anything; a configuration state, not a failed read.
var errNoErrorsDefinition = errors.New("no error-reporting definition gives an app anything")

// errorsInject is the error-reporting kind's inject list from the catalog.
// A missing catalog or definition is errNoErrorsDefinition; a catalog that
// could not be read is another error.
func (s *Service) errorsInject(ctx context.Context) (map[string]string, error) {
	if s.kinds == nil {
		return nil, fmt.Errorf("%w: this AppTown has no dependency catalog", errNoErrorsDefinition)
	}
	k, err := s.kinds.ByCapability(ctx, "errors")
	if errors.Is(err, dependency.ErrUnknown) {
		return nil, fmt.Errorf("%w: %v", errNoErrorsDefinition, err)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the error-reporting definition: %w", err)
	}
	if k.Register == nil || len(k.Register.Inject) == 0 {
		return nil, errNoErrorsDefinition
	}
	return k.Register.Inject, nil
}

// errorsDSN is the attached error-reporting DSN; "" when none is attached.
func (s *Service) errorsDSN(ctx context.Context, projectID ids.ID) (string, error) {
	res, err := Resource(ctx, s.Bindings.db, projectID, "errors")
	if found, err := database.Lookup(err); err != nil || !found {
		return "", err
	}
	if strings.TrimSpace(res) == "" || res == "null" {
		return "", nil // attached, nothing provisioned yet
	}
	var r struct {
		DSN string `json:"dsn"`
	}
	if err := json.Unmarshal([]byte(res), &r); err != nil {
		return "", fmt.Errorf("reading the error-reporting attachment: %w", err)
	}
	return r.DSN, nil
}

// Feedback is the attached feedback service's address and app key:
// nothing attached is ("", ""), an unreadable resource an error.
func (s *Service) Feedback(ctx context.Context, projectID ids.ID) (url, key string, err error) {
	res, err := Resource(ctx, s.Bindings.db, projectID, "feedback")
	if found, err := database.Lookup(err); err != nil || !found {
		return "", "", err
	}
	if strings.TrimSpace(res) == "" || res == "null" {
		return "", "", nil
	}
	var r struct {
		AppKey string `json:"appKey"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal([]byte(res), &r); err != nil {
		return "", "", fmt.Errorf("reading the feedback attachment: %w", err)
	}
	return r.URL, r.AppKey, nil
}

type Registry struct {
	Host     string // registry.example.com
	Repo     string // registry.example.com/group/project
	Username string
	Token    string
}

// Registry derives the project's repository from the instance hub. Eval copies
// receive no general registry credential; EvalPublication is scoped to their
// own published images. An absent hub is nil; unreadable settings are errors.
func (s *Service) Registry(ctx context.Context, projectID ids.ID) (*Registry, error) {
	purpose, err := project.PurposeIn(ctx, s.Bindings.db, projectID)
	if err != nil {
		return nil, err
	}
	if !purpose.Integrations() {
		return nil, nil
	}
	return s.hubRegistry(ctx, projectID)
}

// registryToken is the first of from the secrets hold, "" when they hold
// none; secrets that cannot be read are an error.
func (s *Service) registryToken(ctx context.Context, from []string) (string, error) {
	if s.secrets == nil {
		return "", nil
	}
	values, err := s.secrets.WithContext(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the registry credential: %w", err)
	}
	for _, name := range from {
		if v := values[name]; v != "" {
			return v, nil
		}
	}
	return "", nil
}

// publicDSN is the form of a DSN any visitor may read: the public key a
// browser SDK needs, never a password. A DSN typed in by hand can carry the
// old secret key as its password; AppTown-provisioned ones never do. Servers
// still get the DSN as it was stored.
func publicDSN(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" || u.User == nil || u.User.Username() == "" {
		return "", fmt.Errorf("the error-reporting DSN is not a DSN a browser SDK can use (scheme://key@host/project)")
	}
	if _, has := u.User.Password(); has {
		u.User = url.User(u.User.Username())
	}
	return u.String(), nil
}
