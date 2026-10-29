package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/strs"
)

// AppTownActor is the actor of AppTown's own model calls when the caller
// names none: the call is AppTown's, made for a project or for no one.
const AppTownActor = "apptown-system"

// settleWithin bounds a settlement after the call has ended. The spend line
// stays pending for the gateway's call limit plus this, so a settlement that
// starts in time also ends in time.
const settleWithin = 5 * time.Minute

// Ask answers one of AppTown's own calls (modelcall.Call) through the same
// provider resolution and admission as a runtime's request: the binding the
// model names, the model's eligibility in it, the eval model choice (or,
// for Judge, the judge's door), the concurrency limit, a pending spend
// line written before anything is sent, cancellation through
// ctx, and the spend line settled once, on its own context, when the call
// ends. It skips the network check and the bearer token: the caller is AppTown
// code, already authorized by the route or job that asked.
//
// The answer carries a Usage once the call may have reached the provider,
// even when it failed: unknown spend is marked Incomplete, never dropped. A
// refusal before sending carries none and writes nothing. A provider's
// non-2xx answer carries none either, and its line settles at zero.
func (g *Gateway) Ask(ctx context.Context, call modelcall.Call) (string, *modelcall.Usage, error) {
	name, id, ok := strings.Cut(call.Model, "/")
	if !ok || id == "" || strings.ContainsAny(call.Model, "*~") {
		return "", nil, fmt.Errorf("the model %q does not name one model of a configured provider", call.Model)
	}
	if call.Purpose == "" {
		return "", nil, errors.New("an AppTown model call must say what it is for")
	}
	ps, err := g.providers(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("model provider configuration is unavailable: %w", err)
	}
	p := ps[name]
	if p == nil {
		return "", nil, fmt.Errorf("no model binding is named %q", name)
	}
	if !p.allows(id) {
		return "", nil, p.refusal(id)
	}
	if err := g.choose(ctx, call); err != nil {
		return "", nil, err
	}
	key := fmt.Sprintf("forge/%s/%d", call.Purpose, call.ProjectID)
	if !g.acquire(key) {
		return "", nil, errors.New("too many model calls are in progress; try again shortly")
	}
	defer g.release(key)
	route, body := completionRequest(p.api, id, call.System, call.User)
	path, _, err := p.inferencePath(route, nil, id)
	if err != nil {
		return "", nil, fmt.Errorf("the %q model binding cannot answer a single question: %w", name, err)
	}
	raw, _ := json.Marshal(body)
	actor := call.Actor
	if actor == "" {
		actor = AppTownActor
	}
	who := identity.Workload{Kind: "apptown", Actor: actor, ProjectID: call.ProjectID, RunID: call.RunID}
	receipt, err := g.open(ctx, who, call.Purpose, p.name, id, g.timeout+settleWithin)
	if err != nil {
		return "", nil, fmt.Errorf("the model call was not sent: its spend could not be recorded first: %w", err)
	}

	started := g.now()
	spanCtx, span := g.tracer.Start(ctx, "inference.ask", trace.WithAttributes(
		attribute.String("apptown.inference.provider", p.name), attribute.String("apptown.inference.model", id), attribute.String("apptown.inference.api", p.api),
		attribute.String("apptown.workload.kind", who.Kind), attribute.String("apptown.inference.purpose", call.Purpose), attribute.String("apptown.project", who.ProjectID.String()), attribute.String("apptown.run", who.RunID.String())))
	callCtx, cancel := context.WithTimeout(spanCtx, g.timeout)
	defer cancel()
	a := g.send(callCtx, p, path, raw)
	if a.outcome == "" {
		a.outcome = "upstream_error"
	}

	// Settle on a context of its own: the caller's cancellation ends the
	// call, not the accounting for it.
	settleCtx, done := context.WithTimeout(context.WithoutCancel(ctx), settleWithin)
	defer done()
	if settled, err := g.settle(settleCtx, receipt, a.usage, a.incomplete); err != nil || !settled {
		// The line stays pending and counts as unknown spend once its time
		// runs out (or is already locked in as unknown).
		log.Printf("inference: AppTown call %d's spend was not settled (settled=%v): %v", receipt, settled, err)
	}
	g.observe(spanCtx, span, p.name, id, a.status, a.outcome, a.usage, g.now().Sub(started))
	if a.rejected {
		return "", nil, a.err
	}
	u := &modelcall.Usage{Prompt: a.usage.Input, Completion: a.usage.Output, Cost: a.usage.Cost, Model: a.served, Incomplete: a.incomplete}
	return a.text, u, a.err
}

// choose is the eval model choice for a call made for a project: the judge's
// door for an eval comparison's rubric, otherwise the runtime's own choice.
// The judge's door admits only a copy's recorded judge, so it is refused for
// a call made for no project.
func (g *Gateway) choose(ctx context.Context, call modelcall.Call) error {
	if call.Judge {
		if g.judge == nil || call.ProjectID.IsZero() {
			return errors.New("could not assess: the judge's door admits only an eval's recorded judge for its copies")
		}
		return g.judge(ctx, call.ProjectID, call.Model)
	}
	if call.ProjectID.IsZero() || g.models == nil {
		return nil
	}
	return g.models(ctx, call.ProjectID, call.Model)
}

// answer is how one sent call ended.
type answer struct {
	status           int
	outcome          string
	usage            inferenceUsage
	incomplete, text string
	served           string
	rejected         bool
	err              error
}

// send makes the upstream call and reads its answer whole.
func (g *Gateway) send(ctx context.Context, p *inferenceProvider, path string, raw []byte) answer {
	var a answer
	protocol := http.Header{}
	if p.api == "anthropic-messages" {
		protocol.Set("Anthropic-Version", "2023-06-01")
	}
	resp, err := g.dispatch(ctx, p, path, raw, "application/json", protocol)
	var rejected upstreamRejected
	switch {
	case errors.Is(err, errUnsendable):
		// Nothing was sent: a configuration the resolver accepted cannot
		// make a request. Nothing is owed either.
		a.rejected, a.err = true, fmt.Errorf("invalid inference provider configuration")
		return a
	case errors.As(err, &rejected):
		// Upstream errors may echo credentials: keep the status, never the body.
		a.status, a.outcome, a.rejected = rejected.status, "rejected", true
		a.err = fmt.Errorf("the %s provider rejected the call (HTTP %d)", p.name, a.status)
		return a
	case err != nil:
		if ctx.Err() != nil {
			a.outcome = "cancelled"
		}
		a.incomplete = "the call ended after it was sent, before the provider answered"
		// The transport's error may carry the provider's URL, never a key.
		a.err = fmt.Errorf("the %s model call failed: %s", p.name, strs.Truncate(err.Error(), 160))
		return a
	}
	defer resp.Body.Close()
	a.status = resp.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	reader := newInferenceUsageReader()
	_, _ = reader.Write(body)
	a.usage = reader.result()
	var served struct{ Model, ModelVersion string }
	_ = json.Unmarshal(body, &served)
	a.served = served.Model
	if a.served == "" {
		a.served = served.ModelVersion
	}
	if readErr != nil {
		a.outcome = "interrupted"
		if ctx.Err() != nil {
			a.outcome = "cancelled"
		}
		a.incomplete = "the provider's answer was cut off"
		a.err = fmt.Errorf("the %s model's answer was cut off", p.name)
		return a
	}
	a.outcome = "complete"
	a.text, a.err = completionText(p.api, body)
	switch {
	case a.err != nil:
		a.incomplete = "the provider's answer could not be read"
	case !a.usage.Observed:
		a.incomplete = "the provider reported no usage"
	case !a.usage.Priced && a.usage.Input+a.usage.Output > 0:
		a.incomplete = "the provider reported tokens with no price"
	}
	return a
}

// completionRequest is one system and one user message in the provider's
// own dialect, with no tools.
func completionRequest(api, model, system, prompt string) (string, map[string]any) {
	switch api {
	case "anthropic-messages":
		return "v1/messages", map[string]any{"model": model, "max_tokens": 8192, "system": system,
			"messages": []map[string]string{{"role": "user", "content": prompt}}}
	case "openai-responses":
		return "responses", map[string]any{"model": model, "instructions": system, "input": prompt}
	case "google-generative-ai":
		text := func(s string) []map[string]string { return []map[string]string{{"text": s}} }
		return "models/" + model + ":generateContent", map[string]any{"systemInstruction": map[string]any{"parts": text(system)},
			"contents": []map[string]any{{"role": "user", "parts": text(prompt)}}}
	}
	return "chat/completions", map[string]any{"model": model, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": prompt}}}
}

// completionText reads the model's text from a non-streaming response.
func completionText(api string, body []byte) (string, error) {
	var text strings.Builder
	var err error
	switch api {
	case "anthropic-messages":
		var r struct {
			Content []struct{ Type, Text string }
		}
		err = json.Unmarshal(body, &r)
		for _, c := range r.Content {
			if c.Type == "text" {
				text.WriteString(c.Text)
			}
		}
	case "openai-responses":
		var r struct {
			Output []struct {
				Content []struct{ Type, Text string }
			}
		}
		err = json.Unmarshal(body, &r)
		for _, o := range r.Output {
			for _, c := range o.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
		}
	case "google-generative-ai":
		var r struct {
			Candidates []struct {
				Content struct{ Parts []struct{ Text string } }
			}
		}
		err = json.Unmarshal(body, &r)
		if len(r.Candidates) > 0 {
			for _, part := range r.Candidates[0].Content.Parts {
				text.WriteString(part.Text)
			}
		}
	default:
		var r struct {
			Choices []struct{ Message struct{ Content string } }
		}
		err = json.Unmarshal(body, &r)
		if len(r.Choices) > 0 {
			text.WriteString(r.Choices[0].Message.Content)
		}
	}
	if err != nil || text.Len() == 0 {
		return "", fmt.Errorf("the model's response had no text")
	}
	return text.String(), nil
}
