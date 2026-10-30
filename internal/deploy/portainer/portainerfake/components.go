// Package portainerfake projects external provider facts for tests using real
// AppTown deployment owners. It does not choose or validate release outcomes.
package portainerfake

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"gopkg.in/yaml.v3"
	"net/http"
	"strings"
)

// ServeComponents projects only the exact Compose file the fake provider last
// applied. Component IDs and image IDs are stable independent of enumeration.
func ServeComponents(w http.ResponseWriter, r *http.Request, endpoint int, stacks map[string]string) bool {
	prefix := fmt.Sprintf("/api/endpoints/%d/docker/", endpoint)
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path != "containers/json" && !strings.HasPrefix(path, "containers/") && !strings.HasPrefix(path, "images/") {
		return false
	}
	if path == "containers/json" && strings.Contains(r.URL.Query().Get("filters"), dockerlabel.Class) {
		fmt.Fprint(w, `[]`) // no readiness readers: the fake's containers are the stacks' own
		return true
	}
	var rows []map[string]any
	for stackName, compose := range stacks {
		var doc struct {
			Services map[string]struct {
				Image  string            `yaml:"image"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
			http.Error(w, err.Error(), 500)
			return true
		}
		for name, service := range doc.Services {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(stackName+"/"+name)))
			imageID := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(service.Image)))
			labels := map[string]string{"com.docker.compose.project": stackName, "com.docker.compose.service": name}
			for k, v := range service.Labels {
				labels[k] = v
			}
			state := map[string]any{"StartedAt": "2026-01-01T00:00:00Z", "Running": true, "Status": "running", "ExitCode": 0, "Health": map[string]string{"Status": "healthy"}}
			if strings.HasPrefix(name, "init-") {
				state["Running"] = false
				state["Status"] = "exited"
			}
			rows = append(rows, map[string]any{"Id": id, "Labels": labels, "State": state["Status"], "Image": service.Image})
			if path == "containers/"+id+"/json" {
				json.NewEncoder(w).Encode(map[string]any{"Id": id, "Image": imageID, "Config": map[string]any{"Image": service.Image, "Labels": labels}, "State": state})
				return true
			}
			if path == "images/"+imageID+"/json" {
				json.NewEncoder(w).Encode(map[string]any{"Id": imageID, "RepoDigests": []string{service.Image}})
				return true
			}
		}
	}
	if path == "containers/json" {
		if rows == nil {
			rows = []map[string]any{}
		}
		json.NewEncoder(w).Encode(rows)
		return true
	}
	http.NotFound(w, r)
	return true
}
