package portainer

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/deploy"
)

// StackLayout is what a stack's compose file says about its data: the
// volumes it declares and which service mounts each at what path, and
// which services are needs (AppTown renders a need as a service named
// for it, with its volume key <prefix>need-<slug>-<need>).
type StackLayout struct {
	Stack   string
	Volumes []stackVolume
	mounts  map[string]mount // volume name -> its mount
	needs   map[string]bool
}

// mount is a volume's place in a service.
type mount struct{ Service, Path string }

// MountOf is where the stack mounts a volume, if anywhere.
func (l StackLayout) MountOf(volume string) (mount, bool) {
	m, ok := l.mounts[volume]
	return m, ok
}

// IsNeed says a service is one of the deployment's needs.
func (l StackLayout) IsNeed(service string) bool { return l.needs[service] }

// ReadLayout reads a stack's compose file.
func ReadLayout(stack, base, compose string) (StackLayout, error) {
	vols, err := ComposeVolumes(stack, base, compose)
	if err != nil {
		return StackLayout{}, err
	}
	var file struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &file); err != nil {
		return StackLayout{}, err
	}
	byKey := map[string]stackVolume{}
	l := StackLayout{Stack: stack, Volumes: vols, mounts: map[string]mount{}, needs: map[string]bool{}}
	for _, v := range vols {
		byKey[v.Key] = v
		if v.Need != "" {
			l.needs[v.Need] = true
		}
	}
	for service, svc := range file.Services {
		for _, spec := range svc.Volumes {
			key, path, ok := strings.Cut(spec, ":")
			if !ok {
				continue
			}
			path, _, _ = strings.Cut(path, ":") // a mode (:ro) after the path
			// AppTown writes each $ in a path as $$ (deploy.ComposeLiteral);
			// the path the app was given is the one Compose reads.
			path = deploy.ComposeRead(path)
			if v, ok := byKey[key]; ok {
				l.mounts[v.Name] = mount{Service: service, Path: path}
			}
		}
	}
	return l, nil
}
