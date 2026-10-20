package forge

import (
	"context"
	"os/exec"
	"strconv"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/strs"

	models "flokkit.io/forge/internal/forge/model"
	"flokkit.io/forge/internal/forge/secret"
)

type connectorHealth struct {
	secrets                *secret.Service
	docker                 *docker.Scope
	capacity               *models.Scheduler
	deployment             *deployment.Service
	providers              *deployment.Providers
	integrations           *integration.Service
	agentImage, agentModel string
	// With no local Docker, the worker answers for the agent image and
	// pulls a scanner's image.
	worker *execution.WorkerTasks
	images verification.CheckImage
	now    func() time.Time // the forge's clock, which dates a reading
}

func connectorHealthFor(a *App) connectorHealth {
	return connectorHealth{secrets: a.Secrets, docker: &a.docker, capacity: a.capacity, deployment: a.Deployment, providers: a.targetProviders, integrations: a.Integrations, agentImage: a.cfg.agentImage, agentModel: a.cfg.agentModel,
		worker: a.cfg.worker, images: checksFor(a).Image, now: a.cfg.clock.Now}
}

// Health is an integration's reading: the kind's check, run now, or an
// unchecked reading for a kind the forge has no check for.
func (h connectorHealth) Health(ctx context.Context, capability, provider string, cfgSecretEnv []string) integration.Health {
	return h.reading(h.check(ctx, capability, provider, cfgSecretEnv))
}

// Test runs a provider's connectivity/validity check against a DRAFT
// config — the Harness trick: find out before saving.
func (h connectorHealth) Test(ctx context.Context, capability, provider string, config map[string]any) integration.Health {
	return h.reading(h.test(ctx, capability, provider, config))
}

// reading stamps a check's result with the time it ran; a kind with no
// check gets an unchecked reading.
func (h connectorHealth) reading(got integration.Health, checked bool) integration.Health {
	if !checked {
		return integration.Health{}
	}
	return got.At(clock.Or(h.now)())
}

// check is the kind's live check; checked is false when there is none.
func (h connectorHealth) check(ctx context.Context, capability, provider string, cfgSecretEnv []string) (_ integration.Health, checked bool) {
	switch capability {
	case integration.CapSCM:
		gl, err := configuredGitLab(ctx, h.secrets)
		if err != nil {
			return integration.Health{OK: false, Detail: err.Error()}, true
		}
		var user struct {
			Username string `json:"username"`
		}
		if err := gl.Do(ctx, "GET", "/user", nil, &user); err != nil {
			return integration.Health{OK: false, Detail: strs.Truncate(err.Error(), 120)}, true
		}
		return integration.Health{OK: true, Detail: "authenticated as " + user.Username}, true
	case integration.CapSecrets:
		// Each store answers for itself: a count of everything AppTown
		// resolves said a store with no credentials was working.
		switch provider {
		case secret.StoreInfisical:
			if err := h.secrets.CheckInfisical(ctx); err != nil {
				return integration.Health{OK: false, Detail: strs.Truncate(err.Error(), 200)}, true
			}
			return integration.Health{OK: true, Detail: "read its secrets just now"}, true
		case secret.StoreInternal:
			if err := h.secrets.CheckBuiltIn(ctx); err != nil {
				return integration.Health{OK: false, Detail: strs.Truncate(err.Error(), 200)}, true
			}
			return integration.Health{OK: true, Detail: "its key is readable in AppTown's own database"}, true
		}
		return integration.Health{}, false
	case integration.CapExecutor, integration.CapDeploy:
		if capability == integration.CapDeploy && provider != "docker" {
			// Another provider's deploys (Portainer) are not Docker's:
			// the forge's daemon says nothing about them.
			return integration.Health{}, false
		}
		if h.docker.Absent() {
			return h.onWorker(ctx, capability), true
		}
		if _, err := exec.LookPath("docker"); err != nil || !h.docker.OK(ctx, "info", "--format", "ok") {
			return integration.Health{OK: false, Detail: "docker daemon unreachable"}, true
		}
		detail := "daemon ok"
		if capability == integration.CapExecutor {
			if !h.docker.ImageExists(ctx, h.agentImage) {
				return integration.Health{OK: false, Detail: "image " + h.agentImage + " missing"}, true
			}
			detail = "daemon ok · image " + h.agentImage + " present"
		}
		return integration.Health{OK: true, Detail: detail}, true
	case integration.CapModel:
		if len(cfgSecretEnv) > 0 {
			return h.integrations.CheckModelCredentials(ctx, cfgSecretEnv), true
		}
		if h.agentModel == "" {
			return integration.Health{OK: true, Detail: "pi default (no override)"}, true
		}
		return integration.Health{OK: true, Detail: h.agentModel + " · " + strconv.Itoa(h.capacity.Capacity(ctx, models.BindingOfPattern(h.agentModel))) + " slot(s)"}, true
	}
	return integration.Health{}, false
}

// onWorker is the executor's and Docker deploys' health on a forge with no
// Docker of its own: the worker reports the agent image, and Docker
// deploys are as healthy as the attached Docker hosts they go to.
func (h connectorHealth) onWorker(ctx context.Context, capability string) integration.Health {
	if capability == integration.CapDeploy {
		attached := h.deployment.AttachedDockerHostsHealth(ctx)
		return integration.Health{OK: attached.OK, Detail: attached.Detail}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	image, err := h.worker.AgentImage(ctx)
	if err != nil {
		return integration.Health{OK: false, Detail: "the worker did not report its agent image: " + strs.Truncate(err.Error(), 120)}
	}
	return integration.Health{OK: true, Detail: "worker · image " + image.Name + " present"}
}

// test is the draft config's check; checked is false when the kind has none.
func (h connectorHealth) test(ctx context.Context, capability, provider string, config map[string]any) (_ integration.Health, checked bool) {
	if !draftTested[capability] {
		return h.check(ctx, capability, provider, nil)
	}
	switch capability {
	case digitalocean.CapCloudAccount:
		h := h.providers.CheckCloudAccount(ctx, config)
		return integration.Health{OK: h.OK, Detail: h.Detail}, true
	case integration.CapErrors:
		return h.integrations.TestErrorsBinding(ctx, config), true
	case integration.CapLLM:
		return h.integrations.TestLLMBinding(ctx, config), true
	case integration.CapFeedback:
		return integration.TestFeedbackBinding(config), true
	case integration.CapRegistry:
		format, _ := config["format"].(string)
		url, _ := config["url"].(string)
		return agentenv.ProbeRegistry(format, url), true
	case verification.ScannerCapability:
		image, _ := config["image"].(string)
		return verification.TestScannerImage(ctx, image, h.docker, h.images), true
	case integration.CapModel:
		return h.integrations.TestModelBinding(ctx, config), true
	}
	return integration.Health{}, false
}

// draftTested are the kinds whose Test connection asks the service with
// the configuration given; any other kind's test is its live check. A kind
// here may have no live check of its own (the forge does not call it on
// every reading), and its page says Test connection asks it.
var draftTested = map[string]bool{
	digitalocean.CapCloudAccount: true, integration.CapErrors: true, integration.CapLLM: true,
	integration.CapFeedback: true, integration.CapRegistry: true, verification.ScannerCapability: true, integration.CapModel: true,
}

// Testable says whether Test connection asks a kind's service with its
// configuration.
func (h connectorHealth) Testable(capability string) bool { return draftTested[capability] }
