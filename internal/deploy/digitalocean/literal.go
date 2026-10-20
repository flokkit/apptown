package digitalocean

import (
	"fmt"
	"sort"
	"strings"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
)

// unrewritable refuses a release whose declared environment or secrets
// hold a value App Platform could rewrite. The platform replaces ${NAME}
// in every environment value, secret or not, when NAME is one of its
// bindable variables, and has no escape: neither $$ nor \ keeps it literal.
// Which names it knows is the platform's and grows, so any ${ closed by a
// } is refused: the app would otherwise read a value nobody wrote. The
// message names the variable and its consumer, never the value. The
// driver's own values (need bindings, APPTOWN_ORIGINS, the gate's) use the
// platform's variables on purpose and are not checked here.
func unrewritable(c *appcontract.Contract, secrets deploy.ConsumerSecrets) error {
	if c == nil {
		return nil
	}
	check := func(what, consumer string, env map[string]string) error {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if rewritable(env[k]) {
				return deploy.Terminal(fmt.Errorf("the %s %s for %s contains ${…}, which App Platform reads as one of its own variables and may replace; it cannot be sent there as written. Change the value, or deploy to a target that delivers it literally (a Docker host or Portainer)", what, k, consumer))
			}
		}
		return nil
	}
	type consumer struct {
		name string
		env  map[string]string
	}
	var all []consumer
	for _, p := range c.Parts {
		all = append(all, consumer{p.Name, p.Env})
	}
	for _, s := range c.Init {
		all = append(all, consumer{"init-" + s.Name, s.Env})
	}
	for _, cs := range all {
		if err := check("environment value", cs.name, cs.env); err != nil {
			return err
		}
		if secrets != nil {
			if err := check("secret", cs.name, secrets.EnvFor(cs.name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// rewritable: a ${ with a } after it, the shape of every bindable
// reference.
func rewritable(v string) bool {
	i := strings.Index(v, "${")
	return i >= 0 && strings.Contains(v[i+2:], "}")
}
