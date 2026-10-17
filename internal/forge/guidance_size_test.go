package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/mcpapi"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/strs"

	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/complaint"

	"flokkit.io/forge/internal/forge/httpapi"

	"flokkit.io/forge/internal/forge/wow"
)

// Guidance sizes: every surface that names a block says
// what it costs an agent's context, and the MCP offers the ways of working
// sized rather than pushing them into the agent.
func TestGuidanceSizesAndMCPOffer(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	plainTestProject(t, a.DB, "p", "P")
	short, err := a.Ways.CreateBlock(t.Context(), "house/short", "be brief", "", "bls")
	if err != nil {
		t.Fatal(err)
	}
	long, err := a.Ways.CreateBlock(t.Context(), "house/long", strings.Repeat("x", 3000), "", "bls")
	if err != nil {
		t.Fatal(err)
	}
	off, err := a.Ways.CreateBlock(t.Context(), "house/off", strings.Repeat("y", 500), "", "bls")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Adoption.PutImports(t.Context(), idstest.N(1), []wow.ResolvedPin{
		{BlockID: short.ID, Name: short.Name, Version: short.LatestVersion},
		{BlockID: long.ID, Name: long.Name, Version: long.LatestVersion},
		{BlockID: off.ID, Name: off.Name, Version: off.LatestVersion, Disabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	// The library list carries each block's latest-version size in bytes.
	rec := httptest.NewRecorder()
	httpapi.NewGuidanceBlocks(a.Ways, a.Adoption, a.Access).ListBlocks(rec, httptest.NewRequest(http.MethodGet, "/api/v1/guidance/blocks", nil))
	var listed []wow.Block
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int{}
	for _, b := range listed {
		sizes[b.Name] = b.Bytes
	}
	if sizes["house/short"] != len("be brief") || sizes["house/long"] != 3000 {
		t.Fatalf("list sizes: %v", sizes)
	}

	// Imports carry the pinned version's size; the pack helper sums by name.
	imports, err := a.Adoption.Imports(t.Context(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	if imports[1].Bytes != 3000 || imports[2].Bytes != 500 {
		t.Fatalf("import sizes: %+v", imports)
	}
	if got := a.Ways.BlockBytesByName(t.Context(), []string{"house/short", "house/long", "nope"}); got["house/long"] != 3000 || len(got) != 2 {
		t.Fatalf("bytes by name: %v", got)
	}

	// The MCP offer: enabled blocks only, sized, with the recommendation —
	// and no text.
	testAccounts(t, a.DB, "bls")
	c := &mcpapi.Call{Context: callerMCPContext(context.Background(), &authIdentity{User: "bls", Role: "member", Via: "token"}, "bls via mcp/test"), BaseURL: "http://forge.test"}
	card, err := mcpToolsFor(a).ByName("get_project").Run(c, map[string]any{"slug": "p"})
	if err != nil {
		t.Fatal(err)
	}
	offer := card.(map[string]any)
	if offer["guidanceBytes"] != 3008 {
		t.Fatalf("offer bytes: %v", offer["guidanceBytes"])
	}
	if blocks := offer["guidance"].([]map[string]any); len(blocks) != 2 {
		t.Fatalf("offer blocks: %v", blocks)
	}
	note, _ := offer["guidanceNote"].(string)
	if !strings.Contains(note, "get_project_guidance") || !strings.Contains(note, "2.9k") {
		t.Fatalf("offer note: %q", note)
	}
	if _, has := offer["text"]; has {
		t.Fatal("the offer must not carry the text")
	}

	// The tool hands over the compiled text on request, at the pinned hash.
	out, err := mcpToolsFor(a).ByName("get_project_guidance").Run(c, map[string]any{"slug": "p"})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)
	text, hash, _, _ := a.Adoption.Compile(c.Context, idstest.N(1))
	if got["text"] != text || got["hash"] != hash || got["bytes"] != len(text) {
		t.Fatalf("tool result: bytes=%v hash=%v", got["bytes"], got["hash"])
	}
	if !strings.Contains(text, "house/long") || strings.Contains(text, "house/off") {
		t.Fatalf("compiled text has the wrong blocks: %q", strs.Truncate(text, 80))
	}

	// The generated sections are counted too: every step
	// carries the complaints rule and the hygiene note at least, and the
	// project view totals blocks + always-on generated sections.
	type generatedSection struct {
		Name, Source, BodyMD, Note string
		Bytes                      int
		Always                     bool
	}
	generatedRec := httptest.NewRecorder()
	// A named project is read for a verified caller who may read it.
	testAccounts(t, a.DB, "reader")
	generatedReq := httptest.NewRequest("GET", "/api/v1/guidance/generated?project=p", nil).WithContext(asPerson("reader"))
	httpapi.NewGuidance(a.Ways, a.Adoption, a.Projects, a.lifecycle, httpapi.NewSteeringInventory(a.steering, piHomeFor(a), httpapi.PlatformSteering{Complaints: complaint.AgentInstructions, Hygiene: agentenv.WorkspaceHygiene, Runtime: agentenv.RuntimeContractInstructions, Docker: agentenv.DockerInstructions})).GeneratedGuidance(generatedRec, generatedReq)
	var inventory struct {
		Generated                     []generatedSection
		AlwaysBytes, ConditionalBytes int
	}
	if err := json.Unmarshal(generatedRec.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	secs := inventory.Generated
	names := map[string]generatedSection{}
	for _, s := range secs {
		names[s.Name] = s
	}
	if s := names["platform/complaints-box"]; !s.Always || s.Bytes != len(complaint.AgentInstructions) {
		t.Fatalf("complaints section: %+v", s)
	}
	if s := names["platform/workspace-hygiene"]; !s.Always || s.Bytes != len(agentenv.WorkspaceHygiene) {
		t.Fatalf("hygiene section: %+v", s)
	}
	if s := names["platform/runtime-contract"]; s.Always || s.Bytes != len(agentenv.RuntimeContractInstructions) {
		t.Fatalf("runtime-contract section: %+v", s)
	}
	always, conditional := 0, 0
	for _, section := range secs {
		if section.Bytes != len(section.BodyMD) {
			t.Fatalf("section %s reports %d bytes for %d bytes of text", section.Name, section.Bytes, len(section.BodyMD))
		}
		if section.Always {
			always += len(section.BodyMD)
		} else {
			conditional += len(section.BodyMD)
		}
	}
	if inventory.AlwaysBytes != always || inventory.ConditionalBytes != conditional {
		t.Fatalf("inventory totals: %+v, want %d always and %d conditional", inventory, always, conditional)
	}
	if always < len(complaint.AgentInstructions)+len(agentenv.WorkspaceHygiene) || conditional < len(agentenv.RuntimeContractInstructions) {
		t.Fatalf("overhead: always=%d conditional=%d", always, conditional)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p/guidance", nil)
	req.SetPathValue("slug", "p")
	httpapi.NewGuidance(a.Ways, a.Adoption, a.Projects, a.lifecycle, httpapi.NewSteeringInventory(a.steering, piHomeFor(a), httpapi.PlatformSteering{Complaints: complaint.AgentInstructions, Hygiene: agentenv.WorkspaceHygiene, Runtime: agentenv.RuntimeContractInstructions, Docker: agentenv.DockerInstructions})).GetProjectGuidance(rec, req.WithContext(callerContext(req)))
	var view struct {
		Generated []generatedSection `json:"generated"`
		Effective struct {
			Bytes, GeneratedBytes, ConditionalBytes, ContextBytes int
		} `json:"effective"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Effective.Bytes != len(text) || view.Effective.GeneratedBytes != always ||
		view.Effective.ConditionalBytes != conditional || view.Effective.ContextBytes != len(text)+always {
		t.Fatalf("effective totals: %+v (text %d, always %d)", view.Effective, len(text), always)
	}
	if len(view.Generated) != len(secs) || view.Generated[0].BodyMD != "" {
		t.Fatalf("generated in the project view: %d sections, first body %q", len(view.Generated), strs.Truncate(view.Generated[0].BodyMD, 20))
	}

	// A project with nothing adopted makes no recommendation.
	plainTestProject(t, a.DB, "bare", "Bare")
	bareCard, err := mcpToolsFor(a).ByName("get_project").Run(c, map[string]any{"slug": "bare"})
	if err != nil {
		t.Fatal(err)
	}
	if bare := bareCard.(map[string]any); bare["guidanceNote"] != nil || bare["guidanceBytes"] != 0 {
		t.Fatalf("bare offer: %v", bare)
	}
}
