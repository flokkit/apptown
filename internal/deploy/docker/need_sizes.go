package docker

import (
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"slices"
)

// sizedServices is shared by prepared worker values and direct host execution.
// In particular an older worker still receives the resolved command and cap.
func sizedServices(c *appcontract.Contract, in []deploy.ServiceSpec) ([]deploy.ServiceSpec, error) {
	out := slices.Clone(in)
	for i := range out {
		out[i].Resources = c.NeedResources[out[i].Name]
		if size, ok := c.NeedSizes[out[i].Name]; ok {
			if len(size.Command) == 0 {
				return nil, fmt.Errorf("need %s size %s was not resolved", out[i].Name, size.Name)
			}
			out[i].Command = slices.Clone(size.Command)
		}
	}
	for _, need := range c.Needs {
		_, resources := c.NeedResources[need]
		_, size := c.NeedSizes[need]
		if (resources || size) && !slices.ContainsFunc(out, func(s deploy.ServiceSpec) bool { return s.Name == need }) {
			declaration := "resources"
			if size {
				declaration = "size"
			}
			return nil, fmt.Errorf("needs: %s declares %s, but %s runs no service on a Docker host to hold to them", need, declaration, need)
		}
	}
	return out, nil
}
