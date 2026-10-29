package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
)

// inspectStorage decodes one daemon observation. A successful CLI command with
// incomplete output is not evidence that an engine may open retained data.
func inspectStorage(ctx context.Context, dk Scope, dst any, args ...string) error {
	out, err := dk.Command(ctx, Command{Args: args, Timeout: 30 * time.Second})
	if err != nil {
		return fmt.Errorf("reading storage evidence: %w", err)
	}
	if err := json.Unmarshal(out, dst); err != nil {
		return fmt.Errorf("reading storage evidence: %w", err)
	}
	return nil
}

func volumeIncarnation(ctx context.Context, dk Scope, volume string) (time.Time, string, error) {
	var v struct {
		Name      string
		CreatedAt time.Time
		Driver    string
	}
	if err := inspectStorage(ctx, dk, &v, "volume", "inspect", "--format", "{{json .}}", volume); err != nil {
		return time.Time{}, "", err
	}
	if v.Name != volume || v.CreatedAt.IsZero() || v.Driver == "" {
		return time.Time{}, "", fmt.Errorf("volume %s has no complete incarnation evidence", volume)
	}
	return v.CreatedAt.UTC(), v.Driver, nil
}

func imageEvidence(ctx context.Context, dk Scope, image string) (string, string, error) {
	var v struct {
		Id          string
		RepoDigests []string
	}
	if err := inspectStorage(ctx, dk, &v, "image", "inspect", "--format", "{{json .}}", image); err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(v.Id, "sha256:") || len(v.Id) != 71 {
		return "", "", fmt.Errorf("image %s has no immutable image identity", image)
	}
	digest := ""
	for _, d := range v.RepoDigests {
		if strings.Contains(d, "@sha256:") {
			digest = d
			break
		}
	}
	return v.Id, digest, nil
}

// bindService must run before configuration or limit reconciliation can remove
// a container, and before even an existing stopped engine is started.
func bindService(ctx context.Context, dk Scope, stackID, cname, path string, svc deploy.ServiceSpec, there bool, own Ownership) (string, error) {
	volume := Current.NeedVolume(stackID, svc.Name)
	binding, found, err := own.Storage.Get(ctx, svc.Name, volume)
	if err != nil {
		return "", err
	}
	exists, err := dk.Exists(ctx, "volume", "inspect", volume)
	if err != nil {
		return "", err
	}
	if exists && own.RestoreStorage != nil {
		return "", fmt.Errorf("restore refuses preexisting engine-data volume %s; recover it explicitly before retrying", volume)
	}
	restoreID, restoreDigest := "", ""
	if own.RestoreStorage != nil {
		var source *deploy.StorageBinding
		if found {
			source = &binding
		}
		restoreID, restoreDigest, err = restoreEngine(ctx, dk, svc, path, source, own.RestoreStorage)
		if err != nil {
			return "", err
		}
	}
	if !exists {
		if (found && own.RestoreStorage == nil) || there {
			return "", fmt.Errorf("the %s engine's recorded volume %s is missing", svc.Name, volume)
		}
		if own.RestoreStorage == nil {
			restoreID, restoreDigest, err = resolveNewEngine(ctx, dk, svc.Image)
			if err != nil {
				return "", err
			}
		}
		// The absence above, creation, and subsequent inspect form new-volume
		// evidence under the deployment's existing attempt serialization.
		if err := createVolume(ctx, dk, volume, stackID, svc.Name, own); err != nil {
			return "", err
		}
	}
	created, driver, err := volumeIncarnation(ctx, dk, volume)
	if err != nil {
		return "", err
	}
	if there {
		var current *deploy.StorageBinding
		if found {
			current = &binding
		}
		observed, err := ObserveExistingStorage(ctx, dk, stackID, path, svc, current)
		if err != nil {
			return "", err
		}
		if !found {
			if err := own.Storage.Establish(ctx, observed); err != nil {
				return "", err
			}
		}
		return observed.ImageID, nil
	}
	if found && own.RestoreStorage == nil {
		if !sameDataPath(binding.DataPath, path) || binding.DeclaredImage != svc.Image || !binding.VolumeCreated.Equal(created) || binding.VolumeDriver != driver {
			return "", fmt.Errorf("the %s service's engine declaration or volume incarnation differs from its storage binding", svc.Name)
		}
		id, _, err := imageEvidence(ctx, dk, binding.ImageID)
		if err != nil {
			return "", fmt.Errorf("the %s service's bound engine is unavailable: %w", svc.Name, err)
		}
		if id != binding.ImageID {
			return "", fmt.Errorf("the %s service's bound image identity differs", svc.Name)
		}
		return binding.ImageID, nil
	}
	if exists {
		return "", fmt.Errorf("the %s service has retained data without witnessed engine evidence", svc.Name)
	}
	id, digest := restoreID, restoreDigest
	var source *deploy.StorageBinding
	if found {
		old := binding
		source = &old
	}
	binding = deploy.StorageBinding{Need: svc.Name, Volume: volume, DataPath: path, DeclaredImage: svc.Image, ImageID: id, RepoDigest: digest, VolumeCreated: created, VolumeDriver: driver, Origin: "created"}
	if own.RestoreStorage != nil {
		if err := own.RestoreStorage.Restore(ctx, source, binding); err != nil {
			return "", err
		}
		return id, nil
	}
	if err := own.Storage.Establish(ctx, binding); err != nil {
		return "", err
	}
	return id, nil
}

func restoreEngine(ctx context.Context, dk Scope, svc deploy.ServiceSpec, path string, source *deploy.StorageBinding, restore deploy.StorageRestore) (string, string, error) {
	selected, exact, err := restore.Image(svc.Name, svc.Image, source)
	if err != nil {
		return "", "", err
	}
	var id, digest string
	if exact {
		if source == nil || !sameDataPath(source.DataPath, path) {
			return "", "", fmt.Errorf("the %s physical restore data path differs from its source binding", svc.Name)
		}
		id, digest, err = imageEvidence(ctx, dk, selected)
	} else {
		id, digest, err = resolveNewEngine(ctx, dk, selected)
	}
	if err != nil {
		return "", "", fmt.Errorf("the %s restore engine is unavailable: %w", svc.Name, err)
	}
	if exact && id != selected {
		return "", "", fmt.Errorf("the %s physical restore engine identity differs", svc.Name)
	}
	return id, digest, nil
}

// ObserveStorageEngine witnesses a physical copy's exact source container,
// immutable engine and actual named mount. It grants no mutation authority.
func ObserveStorageEngine(ctx context.Context, dk Scope, container, path, volume string) (string, string, error) {
	var v struct {
		Id, Image string
		Mounts    []struct{ Type, Name, Destination string }
	}
	if err := inspectStorage(ctx, dk, &v, "inspect", "--format", "{{json .}}", container); err != nil {
		return "", "", err
	}
	if v.Id == "" || !strings.HasPrefix(v.Image, "sha256:") || len(v.Image) != 71 || path == "" || volume == "" {
		return "", "", fmt.Errorf("the %s physical copy has no complete source-engine evidence", container)
	}
	for _, m := range v.Mounts {
		if m.Type == "volume" && m.Name == volume && strings.TrimRight(m.Destination, "/") == strings.TrimRight(path, "/") {
			return v.Id, v.Image, nil
		}
	}
	return "", "", fmt.Errorf("the %s physical copy's source container does not mount recorded volume %s", container, volume)
}

// resolveNewEngine may fetch the requested reference only for a witnessed new
// empty target. Resolve before creating storage, so failed pulls leave no volume.
func resolveNewEngine(ctx context.Context, dk Scope, image string) (string, string, error) {
	exists, err := dk.Exists(ctx, "image", "inspect", image)
	if err != nil {
		return "", "", err
	}
	if !exists {
		if _, err := dk.Run(ctx, "pull", image); err != nil {
			return "", "", fmt.Errorf("pulling new storage engine %s: %w", image, err)
		}
	}
	return imageEvidence(ctx, dk, image)
}

// ObserveExistingStorage reads one existing service without starting, pulling,
// creating or deleting anything. Boot records what it observes for an
// existing engine that has no binding yet.
func ObserveExistingStorage(ctx context.Context, dk Scope, stackID, path string, svc deploy.ServiceSpec, current *deploy.StorageBinding) (deploy.StorageBinding, error) {
	volume := Current.NeedVolume(stackID, svc.Name)
	fail := func(err error) (deploy.StorageBinding, error) { return deploy.StorageBinding{}, err }
	created, driver, err := volumeIncarnation(ctx, dk, volume)
	if err != nil {
		return fail(err)
	}
	var actual struct {
		Image  string
		Config struct{ Image string }
		Mounts []struct{ Type, Name, Destination string }
	}
	if err := inspectStorage(ctx, dk, &actual, "inspect", "--format", "{{json .}}", Current.Service(stackID, svc.Name)); err != nil {
		return fail(err)
	}
	mounted := false
	for _, m := range actual.Mounts {
		if m.Type == "volume" && m.Name == volume && strings.TrimRight(m.Destination, "/") == strings.TrimRight(path, "/") {
			mounted = true
		}
	}
	if !mounted || actual.Image == "" || actual.Config.Image == "" {
		return fail(fmt.Errorf("the %s container does not witness its expected data volume and image", svc.Name))
	}
	if current != nil {
		if !sameDataPath(current.DataPath, path) || current.Need != svc.Name || current.Volume != volume || current.DeclaredImage != svc.Image || current.ImageID != actual.Image || !current.VolumeCreated.Equal(created) || current.VolumeDriver != driver {
			return fail(fmt.Errorf("the %s container conflicts with its storage binding", svc.Name))
		}
	} else if actual.Config.Image != svc.Image {
		return fail(fmt.Errorf("the %s service's existing engine declaration differs; establish its previous engine before recovery", svc.Name))
	}
	id, digest, err := imageEvidence(ctx, dk, actual.Image)
	if err != nil {
		return fail(err)
	}
	if id != actual.Image {
		return fail(fmt.Errorf("the %s container's image identity differs", svc.Name))
	}
	if current != nil {
		return *current, nil
	}
	return deploy.StorageBinding{Need: svc.Name, Volume: volume, DataPath: path, DeclaredImage: svc.Image, ImageID: id, RepoDigest: digest, VolumeCreated: created, VolumeDriver: driver, Origin: "observed"}, nil
}

func sameDataPath(a, b string) bool {
	return a != "" && b != "" && strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}
