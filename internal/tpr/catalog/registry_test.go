package catalog_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// The registry loads its embedded catalogs through the TPR hooks at init, so
// the bundled models must be visible to every paid Codex tier and to Codex
// clients whether or not upstream's catalog lists them yet.
func TestRegistryServesBundledCodexModels(t *testing.T) {
	want := []string{"gpt-6.2-sol", "gpt-6.2-astra"}
	for _, tier := range []struct {
		name   string
		models func() []*registry.ModelInfo
	}{
		{"team", registry.GetCodexTeamModels},
		{"plus", registry.GetCodexPlusModels},
		{"pro", registry.GetCodexProModels},
	} {
		have := map[string]bool{}
		for _, m := range tier.models() {
			if m != nil {
				have[m.ID] = true
			}
		}
		for _, id := range want {
			if !have[id] {
				t.Errorf("%s tier is missing %s", tier.name, id)
			}
		}
	}
	for _, m := range registry.GetCodexFreeModels() {
		if m != nil && (m.ID == want[0] || m.ID == want[1]) {
			t.Errorf("free tier lists %s, which OpenAI offers only on paid plans", m.ID)
		}
	}

	client := ojson.GetBytes(registry.GetCodexClientModelsJSON(), "models").Array()
	if len(client) < 2 || client[0].Get("slug").String() != want[0] || client[1].Get("slug").String() != want[1] {
		t.Fatalf("Codex client catalog does not start with %v", want)
	}
	for _, id := range want {
		if !registry.CodexClientModelUsesResponsesLite(id) {
			t.Errorf("%s is not marked use_responses_lite", id)
		}
		if levels := registry.CodexClientModelThinkingLevels(id); len(levels) == 0 {
			t.Errorf("%s has no reasoning levels", id)
		}
	}
	if err := registry.ValidateCodexClientModelsJSON(registry.GetCodexClientModelsJSON()); err != nil {
		t.Errorf("Codex client catalog with additions is invalid: %v", err)
	}
}

// A hub replaces its embedded catalogs with upstream's while it runs, and the
// release workflow swaps upstream's in before building. Upstream's checked-in
// catalogs stand in for both: loaded through the runtime refresh path, they
// must still carry the bundled models.
func TestRefreshedUpstreamCatalogKeepsBundledModels(t *testing.T) {
	dir, errAbs := filepath.Abs("../../registry/models")
	if errAbs != nil {
		t.Fatal(errAbs)
	}
	sources := registry.CatalogSources{
		Catalog:      filepath.Join(dir, "models.json"),
		CodexCatalog: filepath.Join(dir, "codex_client_models.json"),
		DevinCatalog: filepath.Join(dir, "devin_models.json"),
	}
	result, errRefresh := registry.RefreshModelCatalogsWithPolicy(context.Background(), sources, false)
	if errRefresh != nil {
		t.Fatalf("refresh: %v (%+v)", errRefresh, result.Catalogs)
	}
	for name, status := range result.Catalogs {
		if status.Error != "" {
			t.Fatalf("%s refresh failed: %s", name, status.Error)
		}
	}
	TestRegistryServesBundledCodexModels(t)
}
