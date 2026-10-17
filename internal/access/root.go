package access

func RootGrant(grants []Grant) (Grant, bool) {
	for _, g := range grants {
		if g.RoleName == RoleInstanceAdmin && g.Capability == "" && g.ScopeType == ScopeInstance {
			return g, true
		}
	}
	return Grant{}, false
}

// RootIn observes current grants; the account's stored properties never confer
// authority. Disabling an account is checked separately before admission.

func rootOnly(c Capability) bool {
	switch c {
	case CapIdentityUsers, CapIdentityGroups, CapIdentityAuth, CapAuthzGrants:
		return true
	}
	return false
}

// preserveRoot serializes changes that can remove direct or group-derived root
// authority. It protects the resulting authority, not a second account flag.
