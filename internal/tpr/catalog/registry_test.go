package catalog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/catalog"
)

const testModel = "tpr-test-model"

var paidCodexTiers = []struct {
	name   string
	models func() []*registry.ModelInfo
}{
	{"team", registry.GetCodexTeamModels},
	{"plus", registry.GetCodexPlusModels},
	{"pro", registry.GetCodexProModels},
}

func upstreamCatalogDir(t *testing.T) string {
	t.Helper()
	dir, errAbs := filepath.Abs("../../registry/models")
	if errAbs != nil {
		t.Fatal(errAbs)
	}
	return dir
}

// An addition copied from upstream's first entry, so the registry's validation
// accepts it, under a slug upstream will never list.
func testAdditions(t *testing.T, dir string) (models, client []byte) {
	t.Helper()
	rawModels, errModels := os.ReadFile(filepath.Join(dir, "models.json"))
	rawClient, errClient := os.ReadFile(filepath.Join(dir, "codex_client_models.json"))
	if errModels != nil || errClient != nil {
		t.Fatal(errModels, errClient)
	}
	sections := ojson.NewDocString(`{}`)
	for _, section := range []string{"codex-team", "codex-plus", "codex-pro"} {
		entry := ojson.NewDocString(ojson.GetBytes(rawModels, section+".0").Raw)
		if errSet := entry.Set("id", testModel); errSet != nil {
			t.Fatal(errSet)
		}
		if errSet := sections.SetRaw(section, "["+entry.String()+"]"); errSet != nil {
			t.Fatal(errSet)
		}
	}
	entry := ojson.NewDocString(ojson.GetBytes(rawClient, "models.0").Raw)
	if errSet := entry.Set("slug", testModel); errSet != nil {
		t.Fatal(errSet)
	}
	return sections.Bytes(), []byte(`{"models":[` + entry.String() + `]}`)
}

// The registry loads its embedded catalogs through the TPR hooks at init, so
// every bundled addition must be visible to paid Codex tiers and Codex clients.
// When additions/ is empty, the refresh test below still covers the hooks.
func TestRegistryServesBundledCodexModels(t *testing.T) {
	client := ojson.GetBytes(registry.GetCodexClientModelsJSON(), "models").Array()
	listed := map[string]bool{}
	for _, m := range client {
		listed[m.Get("slug").String()] = true
	}
	for _, tier := range paidCodexTiers {
		have := map[string]bool{}
		for _, m := range tier.models() {
			if m != nil {
				have[m.ID] = true
			}
		}
		for _, entry := range ojson.GetBytes(catalog.WithModels([]byte(`{"codex-pro":[{"id":"x"}]}`)), "codex-pro").Array() {
			if id := entry.Get("id").String(); id != "x" && !have[id] {
				t.Errorf("%s tier is missing %s", tier.name, id)
			}
		}
	}
	for _, entry := range ojson.GetBytes(catalog.WithCodexClientModels([]byte(`{"models":[{"slug":"x"}]}`)), "models").Array() {
		if slug := entry.Get("slug").String(); slug != "x" && !listed[slug] {
			t.Errorf("Codex client catalog is missing %s", slug)
		}
	}
	if err := registry.ValidateCodexClientModelsJSON(registry.GetCodexClientModelsJSON()); err != nil {
		t.Errorf("Codex client catalog with additions is invalid: %v", err)
	}
}

// A hub replaces its embedded catalogs with upstream's while it runs, and the
// release workflow swaps upstream's in before building. Upstream's checked-in
// catalogs stand in for both: loaded through the runtime refresh path, they
// must still carry TPR's additions.
func TestRefreshedUpstreamCatalogKeepsBundledModels(t *testing.T) {
	dir := upstreamCatalogDir(t)
	models, client := testAdditions(t, dir)
	catalog.SetAdditionsForTest(t.Cleanup, models, client)
	sources := registry.CatalogSources{
		Catalog:      filepath.Join(dir, "models.json"),
		CodexCatalog: filepath.Join(dir, "codex_client_models.json"),
		DevinCatalog: filepath.Join(dir, "devin_models.json"),
	}
	refresh := func() {
		result, errRefresh := registry.RefreshModelCatalogsWithPolicy(context.Background(), sources, false)
		if errRefresh != nil {
			t.Fatalf("refresh: %v (%+v)", errRefresh, result.Catalogs)
		}
		for name, status := range result.Catalogs {
			if status.Error != "" {
				t.Fatalf("%s refresh failed: %s", name, status.Error)
			}
		}
	}
	refresh()
	// Leave the registry as upstream's catalogs alone would, for later tests.
	t.Cleanup(func() {
		catalog.SetAdditionsForTest(func(func()) {}, []byte(`{}`), []byte(`{"models":[]}`))
		refresh()
	})

	for _, tier := range paidCodexTiers {
		found := false
		for _, m := range tier.models() {
			if m != nil && m.ID == testModel {
				found = true
			}
		}
		if !found {
			t.Errorf("%s tier is missing %s", tier.name, testModel)
		}
	}
	for _, m := range registry.GetCodexFreeModels() {
		if m != nil && m.ID == testModel {
			t.Errorf("free tier lists %s, which was added only to paid tiers", testModel)
		}
	}
	first := ojson.GetBytes(registry.GetCodexClientModelsJSON(), "models.0.slug").String()
	if first != testModel {
		t.Fatalf("Codex client catalog starts with %q, want %q", first, testModel)
	}
	if err := registry.ValidateCodexClientModelsJSON(registry.GetCodexClientModelsJSON()); err != nil {
		t.Errorf("Codex client catalog with additions is invalid: %v", err)
	}
}
