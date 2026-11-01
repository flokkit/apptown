package docker

import "github.com/flokkit/apptown/internal/deploy"

// IntranetNeeds carries the resolved Docker emulators into the release renderer.
func IntranetNeeds(resolved []resolvedNeed) []deploy.IntranetNeed {
	needs := make([]deploy.IntranetNeed, 0, len(resolved))
	for _, n := range resolved {
		needs = append(needs, deploy.IntranetNeed{Name: n.Name, Image: n.Image, Command: n.Command, Entrypoint: n.Entrypoint, Env: n.Env, Health: n.Health, Data: n.Data, Resources: n.Resources})
	}
	return needs
}
