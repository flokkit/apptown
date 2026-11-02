package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// HostSection preserves the evidence time independently of later failed attempts.
// A nil Value means unread, including an unread empty collection.
type HostSection[T any] struct {
	Value      *T
	ObservedAt time.Time
	Reason     string
}

type hostInformation struct {
	ID            string
	MemTotal      int64
	NCPU          int
	DockerRootDir string
	CgroupVersion string
}

type HostContainer struct {
	ID     string `json:"Id"`
	Name   string
	Image  string
	Config struct {
		Image  string
		Labels map[string]string
	}
	State struct {
		Status                string
		Running               bool
		ExitCode              int
		OOMKilled             bool
		StartedAt, FinishedAt time.Time
		Health                *struct{ Status string }
	}
	RestartCount int
	HostConfig   struct {
		Memory, NanoCpus, CpuQuota, CpuPeriod int64
		CpusetCpus                            string
		PortBindings                          map[string][]HostPort
	}
	Mounts          []HostMount
	NetworkSettings struct{ Ports map[string][]HostPort }
}
type HostPort struct{ HostIP, HostPort string }
type HostMount struct{ Type, Name, Destination string }
type hostResources struct{ Volumes, Networks []deploy.Resource }
type hostUse struct{ Disk diskspace.Space }

// HostFacts is a single collection with independently successful sections.
type HostFacts struct {
	Host       HostSection[hostInformation]
	Containers HostSection[[]HostContainer]
	Resources  HostSection[hostResources]
	Images     HostSection[[]string]
	Use        HostSection[hostUse]
}

// HostQuery selects sections. Container names skip the fleet listing for acts.
// Use currently collects disk; cgroup use is added with the capacity consumer.
type HostQuery struct {
	Host, Containers, Resources, Images, Use bool
	Names                                    []string
	ImageRefs                                []string
	ReaderImage                              string
}

const hostFormat = "{{json .}}"

// ReadHost performs one bounded collection. A section failure does not discard
// another section's evidence; callers must retain section times when merging.
func ReadHost(ctx context.Context, dk Scope, q HostQuery) (HostFacts, error) {
	var f HostFacts
	var errs []error
	now := clock.Or(dk.Now)
	record := func(err error) string {
		if err == nil {
			return ""
		}
		errs = append(errs, err)
		return err.Error()
	}
	if q.Host || q.Use {
		out, err := dk.Run(ctx, "info", "--format", "{{json .}}")
		var info hostInformation
		if err == nil {
			err = json.Unmarshal(out, &info)
		}
		if err == nil && (info.ID == "" || info.MemTotal <= 0 || info.NCPU <= 0 || info.DockerRootDir == "") {
			err = errors.New("Docker did not identify the host and its capacity")
		}
		f.Host.Reason = record(err)
		if err == nil {
			f.Host.Value, f.Host.ObservedAt = &info, now()
		}
	}
	if q.Containers {
		cs, err := readHostContainers(ctx, dk, q.Names)
		f.Containers.Reason = record(err)
		if err == nil {
			f.Containers.Value, f.Containers.ObservedAt = &cs, now()
		}
	}
	if q.Resources {
		var resources hostResources
		var sectionErrors []error
		for _, kind := range []string{"volume", "network"} {
			args := []string{kind, "ls"}
			if kind == "network" {
				args = append(args, "--filter", "name="+string(Current)+"net-")
			}
			out, err := dk.Run(ctx, append(args, "--format", "{{.Name}}\t"+ownerLabel+"\t{{.Label \"com.docker.compose.project\"}}\t"+instanceLabel)...)
			if err != nil {
				sectionErrors = append(sectionErrors, fmt.Errorf("listing %ss: %w", kind, err))
				continue
			}
			for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
				cols := strings.Split(line, "\t")
				if cols[0] == "" {
					continue
				}
				r := deploy.Resource{Kind: kind, Name: cols[0], Status: "present"}
				if len(cols) >= 3 {
					r.Owner, r.Stack = cols[1], cols[2]
				}
				if len(cols) < 4 || !dk.installs(cols[3]) || kind == "volume" && !isNamedVolume(r.Name) {
					r.Tags = []string{deploy.TagConfirmsOnly}
				}
				if kind == "volume" {
					resources.Volumes = append(resources.Volumes, r)
				} else {
					resources.Networks = append(resources.Networks, r)
				}
			}
		}
		err := errors.Join(sectionErrors...)
		f.Resources.Reason = record(err)
		if err == nil {
			f.Resources.Value, f.Resources.ObservedAt = &resources, now()
		}
	}
	if q.Images {
		refs, err := recordedImages(ctx, dk, q.ImageRefs)
		f.Images.Reason = record(err)
		if err == nil {
			// A successful empty list must survive JSON persistence as a value,
			// rather than becoming indistinguishable from an unread section.
			if refs == nil {
				refs = []string{}
			}
			f.Images.Value, f.Images.ObservedAt = &refs, now()
		}
	}
	if q.Use {
		var use hostUse
		var err error
		if f.Host.Value == nil {
			err = errors.New("the data root was not read")
		} else {
			use.Disk, err = ReadDisk(ctx, dk, q.ReaderImage, f.Host.Value.DockerRootDir)
		}
		f.Use.Reason = record(err)
		if err == nil {
			f.Use.Value, f.Use.ObservedAt = &use, now()
		}
	}
	return f, errors.Join(errs...)
}

func readHostContainers(ctx context.Context, dk Scope, names []string) ([]HostContainer, error) {
	if len(names) == 0 {
		out, err := dk.Run(ctx, "ps", "-aq", "--no-trunc")
		if err != nil {
			return nil, err
		}
		names = strings.Fields(string(out))
	}
	containers := []HostContainer{}
	if len(names) == 0 {
		return containers, nil
	}
	var stdout, stderr bytes.Buffer
	_, runErr := dk.Command(ctx, Command{Args: append([]string{"inspect", "--type", "container", "--format", hostFormat}, names...), Stdout: &stdout, Stderr: &stderr})
	answered := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var c HostContainer
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		if c.ID == "" || c.Name == "" {
			return nil, errors.New("Docker returned an unidentified container")
		}
		c.Name = strings.TrimPrefix(c.Name, "/")
		answered[c.ID], answered[c.Name] = true, true
		containers = append(containers, c)
	}
	missing := false
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if line == "" {
			continue
		}
		_, name, ok := strings.Cut(line, "No such container: ")
		if !ok {
			_, name, ok = strings.Cut(line, "No such object: ")
		}
		if !ok {
			return nil, fmt.Errorf("reading containers: %s", line)
		}
		answered[strings.TrimSpace(name)] = true
		missing = true
	}
	if runErr != nil && !missing {
		return nil, runErr
	}
	for _, name := range names {
		if !answered[name] {
			return nil, fmt.Errorf("Docker did not answer about container %s", name)
		}
	}
	return containers, nil
}

// HostInventory derives only from witnessed facts. The owner decides coverage
// and age before using an absent resource as evidence of a missing record.
func HostInventory(f HostFacts, instance string, also []string) []deploy.Resource {
	var out []deploy.Resource
	if f.Containers.Value != nil {
		for _, c := range *f.Containers.Value {
			named := false
			for _, prefix := range []string{string(Current) + keyStackPrefix, string(Current) + "svc-", string(Current) + "preview-", string(Current) + "init-"} {
				named = named || strings.HasPrefix(c.Name, prefix)
			}
			for _, name := range also {
				named = named || c.Name == name
			}
			if !named {
				continue
			}
			r := deploy.Resource{Kind: "container", Name: c.Name, Version: c.Config.Image, Status: "stopped", Detail: c.State.Status, Owner: c.Config.Labels[dockerlabel.Owner], Stack: c.Config.Labels["com.docker.compose.project"]}
			if c.State.Running {
				r.Status = "active"
			}
			if instance == "" || c.Config.Labels[dockerlabel.Instance] != instance {
				r.Tags = []string{deploy.TagConfirmsOnly}
			}
			out = append(out, r)
		}
	}
	if f.Resources.Value != nil {
		out = append(out, f.Resources.Value.Volumes...)
		out = append(out, f.Resources.Value.Networks...)
	}
	if f.Images.Value != nil {
		for _, ref := range *f.Images.Value {
			out = append(out, deploy.Resource{Kind: "image", Name: ref, ID: ref, Status: "present"})
		}
	}
	if f.Host.Value != nil {
		for i := range out {
			out[i].ObservedDaemonID = f.Host.Value.ID
		}
	}
	return out
}
