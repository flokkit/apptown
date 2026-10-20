package agentenv

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"strings"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
)

// Instructions describes the live services and registry environment shared by
// steps and conversations. It neither chooses a harness nor changes work.
type Instructions struct {
	db           *database.DB
	integrations *integration.Service
}

func NewInstructions(db *database.DB, integrations *integration.Service) *Instructions {
	return &Instructions{db: db, integrations: integrations}
}

// Registry generates the "what is ambient HERE" steering text from
// the live bindings — norms live in guidance (durable prose); the mechanism
// inventory is instance state and must never be hand-written (it would lie
// on any instance with different bindings). Empty when nothing is bound.
func (s *Instructions) Registry(ctx context.Context) string {
	return RegistrySteering(integration.RegistryBindingsIn(ctx, s.db))
}

// Integrations is the generated-steering contribution: per attached
// integration, what exists and how the product is expected to use it.
func (s *Instructions) Integrations(ctx context.Context, projectID ids.ID) string {
	var b strings.Builder
	b.WriteString(s.errorsDocSection(ctx, projectID))
	b.WriteString(s.feedbackDocSection(ctx, projectID))
	b.WriteString(s.llmDocSection(ctx, projectID))
	var p *project.Project
	if found, err := project.ByIDIn(ctx, s.db, projectID); err == nil {
		p = &found
	}
	for _, attached := range s.integrations.AttachedServices(ctx, projectID) {
		b.WriteString("\n## " + attached.Kind.Title + " (attached, generated)\n\n")
		if len(attached.Kind.Env) > 0 {
			b.WriteString("Injected into app containers: ")
			keys := make([]string, 0, len(attached.Kind.Env))
			for _, e := range attached.Kind.Env {
				keys = append(keys, e.Key)
			}
			b.WriteString(strings.Join(keys, ", ") + ".\n\n")
		}
		if attached.Kind.Steering != "" {
			b.WriteString(strings.TrimSpace(s.integrations.RenderTemplate(ctx, attached.Kind.Steering, attached.Config, attached.Resource, p, false)) + "\n")
		}
		if attached.Kind.Guidance != "" {
			b.WriteString("\n" + strings.TrimSpace(attached.Kind.Guidance) + "\n")
		}
	}
	return b.String()
}

// errorsDocSection: the generated-steering contribution, per project.
func (s *Instructions) errorsDocSection(ctx context.Context, projectID ids.ID) string {
	if len(s.integrations.ErrorsEnvironment(ctx, projectID)) == 0 {
		return ""
	}
	return "\n## Error reporting (attached, generated)\n\n" +
		"This project has Sentry-compatible error reporting. The DSN is injected\n" +
		"into app containers as SENTRY_DSN / GLITCHTIP_DSN — initialize the SDK\n" +
		"from that env var (never hardcode it). See the observability guidance.\n" +
		"A static site cannot read env vars: its page loads /_apptown/env.js and\n" +
		"reads window.__APPTOWN_ENV__.SENTRY_DSN (guidance deploy/browser-values);\n" +
		"AppTown checks the page loads it.\n" +
		"Test hooks are part of the wiring, not optional: a server app serves\n" +
		"GET /_apptown/boom (captures a tagged \"forge test boom\" error, returns\n" +
		"500, no auth); a browser app throws the same on the typed sequence\n" +
		"boom!!! . After wiring, PROVE it: your step has the same SENTRY_DSN\n" +
		"the app containers get. Run the app in your private docker and hit\n" +
		"/_apptown/boom (a server app), or for a browser app send one tagged\n" +
		"\"forge test boom\" event from node with @sentry/node and that DSN.\n" +
		"Then run `apptown integrations` — it shows when the provider last saw an\n" +
		"event from this project. Report what it said. That is what \"wired\n" +
		"in\" means. The developer guide: /docs/errors/glitchtip\n"
}

// llmDocSection: the steering paragraph for a project that attached AI
// models. The keys are made per deployment and per preview, so the step
// itself holds none.
func (s *Instructions) llmDocSection(ctx context.Context, projectID ids.ID) string {
	if attached, err := s.integrations.Attachments.IsAttached(ctx, projectID, integration.CapLLM); err != nil || !attached {
		return ""
	}
	return "\n## AI models (attached, generated)\n\n" +
		"This project may give its app AI models through OpenRouter. Declare the\n" +
		"need in .apptown/app.yaml: `needs: { llm: { limit: 5 } }` (US dollars;\n" +
		"optional `limitReset: daily|weekly|monthly`; the connector sets the\n" +
		"most allowed). Each deployment and each preview then gets its own key,\n" +
		"injected as OPENROUTER_API_KEY and OPENROUTER_BASE_URL. Use an\n" +
		"OpenAI-compatible client with exactly those two. Never log, print,\n" +
		"return or commit the key, and call models from the server only.\n" +
		"Absent variables mean AI is off; HTTP 402 means the allowance is used\n" +
		"up: say so to the person. Your own step holds no key: prove the\n" +
		"wiring in the change's preview. See guidance deps/ai-models.\n"
}

// feedbackDocSection: the steering paragraph — agents get the exact embed.
func (s *Instructions) feedbackDocSection(ctx context.Context, projectID ids.ID) string {
	url, key := s.integrations.FeedbackResource(ctx, projectID)
	if url == "" || key == "" {
		return ""
	}
	return "\n## Customer feedback (attached, generated)\n\n" +
		"This project collects user feedback with a widget (floating button +\n" +
		"dialog, shadow-DOM isolated). Its address and key are never written\n" +
		"into the code. A static site loads /_apptown/env.js (guidance\n" +
		"deploy/browser-values) and adds the widget from it:\n\n" +
		"    <script src=\"/_apptown/env.js\"></script>\n" +
		"    <script>\n" +
		"      (function (env) {\n" +
		"        if (!env.FEEDBACK_URL || !env.FEEDBACK_APP_KEY) return;\n" +
		"        var s = document.createElement('script');\n" +
		"        s.src = env.FEEDBACK_URL + '/widget.js';\n" +
		"        s.dataset.appKey = env.FEEDBACK_APP_KEY;\n" +
		"        document.body.appendChild(s);\n" +
		"      })(window.__APPTOWN_ENV__ || {});\n" +
		"    </script>\n\n" +
		"A server app receives FEEDBACK_URL and FEEDBACK_APP_KEY in its\n" +
		"environment: render the same widget from them, or POST\n" +
		"$FEEDBACK_URL/api/feedback {appKey, rating?, nps?, category?, message?, page?}.\n"
}
