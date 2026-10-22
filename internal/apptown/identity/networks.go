package identity

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

const settingAdministratorNetworks = "admin_networks"

// AdministratorNetworks is the recorded sign-in policy, not proxy trust.
type AdministratorNetworks []string

func (p AdministratorNetworks) Allows(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() {
		return true
	}
	valid := 0
	for _, raw := range p {
		if _, network, err := net.ParseCIDR(raw); err == nil {
			valid++
			if network.Contains(ip) {
				return true
			}
			continue
		}
		if addr := net.ParseIP(raw); addr != nil {
			valid++
			if addr.Equal(ip) {
				return true
			}
		}
	}
	return valid == 0
}
func (p AdministratorNetworks) Denial(ip net.IP) string {
	return fmt.Sprintf("administrators may only sign in from %s; this connection is from %s", strings.Join(p, ", "), ip)
}
func (s *Service) AdministratorNetworks(ctx context.Context) (AdministratorNetworks, error) {
	return administratorNetworksIn(ctx, s.db)
}

func administratorNetworksIn(ctx context.Context, q database.Queryer) (AdministratorNetworks, error) {
	value, err := InstanceSettingIn(ctx, q, settingAdministratorNetworks)
	if err != nil {
		return nil, err
	}
	var out AdministratorNetworks
	for _, entry := range strings.Split(value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out, nil
}

// NetworkInputError identifies an invalid address or CIDR declaration.
type NetworkInputError struct{ Entry string }

func (e *NetworkInputError) FaultKind() fault.Kind { return fault.Invalid }

func (e *NetworkInputError) Error() string {
	return fmt.Sprintf("%q is not a network (use CIDR form, e.g. 192.168.1.0/24)", e.Entry)
}

type NetworkLockoutError struct{ From, Networks string }

func (e *NetworkLockoutError) FaultKind() fault.Kind { return fault.Conflict }

func (e *NetworkLockoutError) Error() string {
	return fmt.Sprintf("that list would lock you out: you are connecting from %s, which is in none of %s", e.From, e.Networks)
}

func (p AdministratorNetworks) prepare(from net.IP) (AdministratorNetworks, error) {
	var clean AdministratorNetworks
	for _, entry := range p {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err != nil && net.ParseIP(entry) == nil {
			return nil, &NetworkInputError{Entry: entry}
		}
		clean = append(clean, entry)
	}
	if !clean.Allows(from) {
		return nil, &NetworkLockoutError{From: from.String(), Networks: strings.Join(clean, ", ")}
	}
	return clean, nil
}

var (
	ErrAdministratorNetworksExpected = fault.New(fault.Invalid, "send the list of networks the edit started from (expected)")
	// ErrAdministratorNetworksMoved carries a code so a client can offer the
	// person the current list.
	ErrAdministratorNetworksMoved error = networksMoved{}
)

type networksMoved struct{}

func (networksMoved) Error() string {
	return "the administrator networks changed since this edit started; review the current list before saving again"
}
func (networksMoved) FaultKind() fault.Kind { return fault.Conflict }
func (networksMoved) FaultCode() string     { return "settings-moved" }

// SetAdministratorNetworks accepts the caller address from a trusted adapter,
// never from the request body. expected is the list the edit started from; it
// is required, and a list that has changed since is refused before anything
// is written.
func (s *Service) SetAdministratorNetworks(ctx context.Context, pr access.Principal, expected *AdministratorNetworks, networks AdministratorNetworks, from net.IP) (AdministratorNetworks, error) {
	if err := s.requireConfiguration(ctx, pr, "access.set-administrator-networks"); err != nil {
		return nil, err
	}
	if expected == nil {
		return nil, ErrAdministratorNetworksExpected
	}
	clean, err := networks.prepare(from)
	if err != nil {
		return nil, err
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := queries(tx)
		if err := q.LockInstanceSetting(ctx, settingAdministratorNetworks); err != nil {
			return err
		}
		before, err := administratorNetworksIn(ctx, tx)
		if err != nil {
			return err
		}
		if !slices.Equal([]string(before), []string(*expected)) {
			return ErrAdministratorNetworksMoved
		}
		value := strings.Join(clean, ",")
		if _, err := q.PutInstanceSetting(ctx, accesssql.PutInstanceSettingParams{Key: settingAdministratorNetworks, Value: value}); err != nil {
			return err
		}
		return audit.At(clock.Or(s.now)()).RecordIn(ctx, tx, pr.Name, ids.Nil, "auth.admin-networks", value, map[string]any{"before": before, "from": from.String()})
	})
	if err != nil {
		return nil, err
	}
	return clean, nil
}
