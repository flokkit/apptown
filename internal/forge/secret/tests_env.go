package secret

import "sort"

// TestsEnv is what an app's tests step is given of the secrets the app
// declares in ws (path is the contract's SecretSpecPath; empty is the
// default file). The tests are not a deployment and have no bindings: a
// secret the forge would generate (generate = true) gets a throwaway value
// minted for this call alone, never the deployed value and never stored;
// a secret with a declared default gets that default, which is in the tree
// already; anything else is a credential the tests are not given, named in
// withheld. Settings that load a generated key (Django's SECRET_KEY) load.
// The profile is the one a deployment without a choice takes: default,
// else the first declared. No declaration gives nothing.
func TestsEnv(ws, path string) (env, provided, withheld []string, err error) {
	spec, present, err := ReadSpec(ws, path)
	if err != nil || !present {
		return nil, nil, nil, err
	}
	profile := "default"
	if _, ok := spec.Profiles[profile]; !ok {
		profile = spec.ProfileNames()[0]
	}
	for _, s := range spec.SecretsIn(profile) {
		var value string
		switch {
		case s.Generate:
			if value, err = GenerateValue(); err != nil {
				return nil, nil, nil, err
			}
		case s.Default != "":
			value = s.Default
		default:
			withheld = append(withheld, s.Name)
			continue
		}
		env = append(env, s.Name+"="+value)
		provided = append(provided, s.Name)
	}
	sort.Strings(env)
	return env, provided, withheld, nil
}
