package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
)

// ByID resolves immutable connection identity; a reused machine name cannot
// supply configuration for the deleted connection's resources.
func (s *Bindings) ByID(ctx context.Context, id ids.ID) (*Binding, error) {
	if id.IsZero() {
		return nil, ErrNoBinding
	}
	row, err := integrationsql.New(s.db.SQL()).BindingByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoBinding
	}
	if err != nil {
		return nil, err
	}
	binding := bindingRecord(row)
	return &binding, nil
}

// PortainerConnection resolves exactly one connection and one vault snapshot.
// Destination verification belongs to Providers before these credentials are used.
func (s *Service) PortainerConnection(ctx context.Context, id ids.ID) (*portainer.Target, error) {
	binding, err := s.Bindings.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if binding.Capability != "deploy" || binding.Provider != "portainer" {
		return nil, errors.New("connection is not a Portainer deployment provider")
	}
	var refs map[string]any
	if err := json.Unmarshal([]byte(binding.ConfigJSON), &refs); err != nil {
		return nil, errors.New("Portainer connection configuration is malformed")
	}
	urlRef, _ := refs["urlFrom"].(string)
	keyRef, _ := refs["apiKeyFrom"].(string)
	if urlRef == "" || keyRef == "" {
		return nil, errors.New("Portainer connection must name its URL and API-key secret references")
	}
	values, err := s.secrets.WithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading Portainer connection credentials: %w", err)
	}
	cfg := &portainer.Target{PortainerURL: strings.TrimRight(values[urlRef], "/"), PortainerKey: values[keyRef]}
	if cfg.PortainerURL == "" || cfg.PortainerKey == "" {
		return nil, errors.New("Portainer connection credentials are unavailable")
	}
	return cfg, nil
}

// CreatePortainerConnectionIn materializes the legacy target's explicit secret
// references in its caller's transaction. It never retargets an existing name.
func CreatePortainerConnectionIn(ctx context.Context, tx *database.Tx, name, urlRef, keyRef, who string, at time.Time) (*Binding, error) {
	if name == "" || urlRef == "" || keyRef == "" {
		return nil, errors.New("Portainer connection name and credential references are required")
	}
	config, err := json.Marshal(map[string]string{"urlFrom": urlRef, "apiKeyFrom": keyRef})
	if err != nil {
		return nil, err
	}
	q := integrationsql.New(tx.SQL())
	row, err := q.BindingByNameForUpdate(ctx, name)
	if err == nil {
		if row.Capability != "deploy" || row.Provider != "portainer" || !sameJSON(string(row.ConfigJson), string(config)) {
			return nil, errors.New("connection name already identifies different configuration")
		}
		binding := bindingRecord(row)
		return &binding, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	row, err = q.InsertBinding(ctx, integrationsql.InsertBindingParams{ID: ids.New(at), Name: name, Capability: "deploy", Provider: "portainer", ConfigJson: config, CreatedAt: at, UpdatedAt: at})
	if err != nil {
		return nil, err
	}
	if err := audit.At(at).RecordIn(ctx, tx, who, ids.Nil, "integration.saved", name, map[string]any{"capability": "deploy", "provider": "portainer"}); err != nil {
		return nil, err
	}
	binding := bindingRecord(row)
	return &binding, nil
}

// MatchConnectionIn keeps connection configuration stable while its observed
// association commits. The preceding network observation runs outside the lock.
func MatchConnectionIn(ctx context.Context, tx *database.Tx, id ids.ID, version int) error {
	row, err := integrationsql.New(tx.SQL()).BindingByIDForShare(ctx, id)
	if err != nil {
		return err
	}
	if int(row.Version) != version {
		return errors.New("connection changed during provider observation; try again")
	}
	return nil
}

// ConnectorConfigByID resolves the retained connection identity, refusing a
// missing or differently capable binding rather than following a reused name.
func (s *Service) ConnectorConfigByID(ctx context.Context, id ids.ID, capability string) (map[string]any, error) {
	binding, err := s.Bindings.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if binding.Capability != capability {
		return nil, errors.New("connection capability changed")
	}
	return strictJSONMap(binding.ConfigJSON)
}
