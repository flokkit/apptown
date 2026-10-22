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
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// ErrorsEnvironment: the runtime contribution — the variables the error
// reporting kind's definition (stock/dependencies/errors.yaml, inject)
// gives an attached project's app containers, with its DSN. Empty when
// unattached (opt-in by attachment), and when the definition cannot be
// read, which the log names.
func (s *Service) ErrorsEnvironment(ctx context.Context, projectID ids.ID) []string {
	dsn, err := s.errorsDSN(ctx, projectID)
	if err != nil || dsn == "" {
		return nil
	}
	inject, err := s.errorsInject(ctx)
	if err != nil {
		log.Printf("error reporting for project %d: %v; its app is given no error-reporting variables", projectID, err)
		return nil
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(inject)) {
		out = append(out, name+"="+strings.ReplaceAll(inject[name], dsnRef, dsn))
	}
	return out
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

// feedbackStrict is FeedbackResource for a caller that must not read a failure
// as absence: nothing attached is ("", ""), an unreadable resource an error.
func (s *Service) feedbackStrict(ctx context.Context, projectID ids.ID) (url, key string, err error) {
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

func (s *Service) FeedbackResource(ctx context.Context, projectID ids.ID) (url, key string) {
	res, err := Resource(ctx, s.Bindings.db, projectID, "feedback")
	if err != nil {
		return "", ""
	}
	var r struct {
		AppKey string `json:"appKey"`
		URL    string `json:"url"`
	}
	if json.Unmarshal([]byte(res), &r) != nil {
		return "", ""
	}
	return r.URL, r.AppKey
}

// FeedbackEnvironment: injected into app containers when attached.
func (s *Service) FeedbackEnvironment(ctx context.Context, projectID ids.ID) []string {
	url, key := s.FeedbackResource(ctx, projectID)
	if url == "" || key == "" {
		return nil
	}
	return []string{"FEEDBACK_URL=" + url, "FEEDBACK_APP_KEY=" + key}
}

type Registry struct {
	Host     string // registry.example.com
	Repo     string // registry.example.com/group/project
	Username string
	Token    string
}

// RegistryFor returns the project's registry wiring, or nil if the
// capability isn't attached (or is attached without a usable repo). A
// connector credential that does not resolve leaves Token empty: the
// registry refuses the login, which is an error where it is used.
func (s *Service) RegistryFor(ctx context.Context, projectID ids.ID) *Registry {
	ri, _ := s.Registry(ctx, projectID)
	return ri
}

// Registry is RegistryFor for a caller that must not take "could not
// read" or "the credential does not resolve" as an anonymous registry. The
// credential is the attachment's own (username, token), when it holds one,
// or else its connector's: username and the secret tokenFrom names,
// resolved from the secrets store when used and never stored in the
// attachment.
//
// An eval copy is attached nothing, so it has no registry here: nothing it
// declares is pulled with a credential. AppTown publishes a copy's
// releases itself (EvalPublication), to that copy's own repository only.
func (s *Service) Registry(ctx context.Context, projectID ids.ID) (*Registry, error) {
	binding, resJSON, err := s.Attachments.Attached(ctx, projectID, CapContainerRegistry)
	if errors.Is(err, ErrNotAttached) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res := parseJSONMap(resJSON)
	value := func(k string) string { v, _ := res[k].(string); return v }
	info := &Registry{Repo: value("repo"), Username: value("username"), Token: value("token")}
	cfg := s.ConnectorConfig(ctx, binding, CapContainerRegistry)
	if u, _ := cfg["url"].(string); u != "" {
		info.Host = u
	}
	if info.Host == "" {
		if i := strings.IndexByte(info.Repo, '/'); i > 0 {
			info.Host = info.Repo[:i]
		}
	}
	if info.Repo == "" {
		return nil, nil
	}
	if info.Token == "" {
		if from := ConfigStrings(cfg["tokenFrom"]); len(from) > 0 {
			if info.Username == "" {
				info.Username, _ = cfg["username"].(string)
			}
			if s.secrets != nil {
				info.Token = registryToken(s.secrets.All(ctx), from)
			}
			if info.Token == "" {
				return info, fmt.Errorf("the container-registry connector %s names its credential %s, which the secrets store does not hold", binding, strings.Join(from, ", "))
			}
		}
	}
	return info, nil
}

// registryToken is the first of from the secrets hold.
func registryToken(secrets map[string]string, from []string) string {
	for _, name := range from {
		if v := secrets[name]; v != "" {
			return v
		}
	}
	return ""
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
