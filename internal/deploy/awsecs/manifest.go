package awsecs

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// DatabaseManifest is part of the backup set, uploaded by its listing task and
// verified in the same store as its dumps. It is never a temporary result object.
type DatabaseManifest struct {
	Instance      string          `json:"instance"`
	Databases     []string        `json:"databases"`
	ProviderOwned []string        `json:"providerOwned"`
	Empty         map[string]bool `json:"empty"`
}

func ParseDatabaseManifest(body []byte, instance string) (DatabaseManifest, error) {
	var m DatabaseManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return m, err
	}
	if m.Instance != instance || len(m.Databases) == 0 {
		return m, errors.New("database manifest names another instance or no databases")
	}
	if !slices.Equal(m.ProviderOwned, []string{ProviderOwnedDatabase}) {
		return m, errors.New("database manifest must name only rdsadmin as provider-owned")
	}
	seen := map[string]bool{}
	for _, db := range m.Databases {
		if db == "" || seen[db] {
			return m, errors.New("database manifest contains an empty or repeated database")
		}
		seen[db] = true
		if db != ProviderOwnedDatabase {
			if _, ok := m.Empty[db]; !ok {
				return m, fmt.Errorf("database manifest has no table count for %s", db)
			}
		}
	}
	if !seen[ProviderOwnedDatabase] {
		return m, errors.New("database manifest did not list the provider-owned rdsadmin database")
	}
	return m, nil
}

// Covered requires a verified dump for every listed database except RDS's own.
// An extra discovered database can never become an implicit exclusion.
func (m DatabaseManifest) Covered(copies map[string]bool) error {
	for _, db := range m.Databases {
		if db != ProviderOwnedDatabase && !copies[db] {
			return fmt.Errorf("the final set has no verified dump of database %s", db)
		}
	}
	return nil
}

// These queries run against the app's remote PostgreSQL catalogue, not the
// AppTown schema. Scripts stay with the provider job that executes them.
//
//go:embed scripts/user-table-count.sql
var userTableCount string

//go:embed scripts/list-databases.sh
var listDatabasesScript string

//go:embed scripts/check-empty.sh
var checkEmptyScript string

func listingCommand() string {
	return strings.ReplaceAll(listDatabasesScript, "__USER_TABLE_COUNT__", strings.TrimSpace(userTableCount))
}
func checkEmptyCommand() string {
	return strings.ReplaceAll(checkEmptyScript, "__USER_TABLE_COUNT__", strings.TrimSpace(userTableCount))
}
