package catalog

import (
	"bytes"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestWithModelsAddsMissingEntriesOnly(t *testing.T) {
	data := []byte(`{"claude":[{"id":"c"}],"codex-pro":[{"id":"a","description":"upstream"}],"codex-free":[{"id":"f"}]}`)
	additions := []byte(`{"codex-pro":[{"id":"a","description":"tpr"},{"id":"b"}],"codex-plus":[{"id":"b"}]}`)

	got := withModels(data, additions)

	pro := ojson.GetBytes(got, "codex-pro").Array()
	if len(pro) != 2 || pro[0].Get("id").String() != "a" || pro[1].Get("id").String() != "b" {
		t.Fatalf("codex-pro = %s, want upstream a then added b", ojson.GetBytes(got, "codex-pro").Raw)
	}
	if desc := pro[0].Get("description").String(); desc != "upstream" {
		t.Errorf("existing entry description = %q, want upstream's", desc)
	}
	if ojson.GetBytes(got, "codex-plus").Exists() {
		t.Errorf("a section missing from the catalog was created: %s", got)
	}
	if n := len(ojson.GetBytes(got, "codex-free").Array()); n != 1 {
		t.Errorf("codex-free has %d entries, want 1", n)
	}
}

func TestWithModelsReturnsInputWhenNothingChanges(t *testing.T) {
	data := []byte(`{"codex-pro":[{"id":"a"}]}`)
	for _, additions := range [][]byte{
		[]byte(`{"codex-pro":[{"id":"a"}]}`),
		[]byte(`not json`),
		[]byte(`[]`),
	} {
		if got := withModels(data, additions); !bytes.Equal(got, data) {
			t.Errorf("withModels(%s) = %s, want input unchanged", additions, got)
		}
	}
	for _, bad := range [][]byte{[]byte(`not json`), []byte(`[]`), nil, []byte(`{"codex-pro":[]}`)} {
		if got := withModels(bad, []byte(`{"codex-pro":[{"id":"b"}]}`)); !bytes.Equal(got, bad) {
			t.Errorf("withModels on %q = %q, want input unchanged", bad, got)
		}
	}
}

func TestWithCodexClientModelsPlacesAdditionsFirst(t *testing.T) {
	data := []byte(`{"models":[{"slug":"x","priority":1},{"slug":"y","priority":2}],"etag":"e"}`)
	additions := []byte(`{"models":[{"slug":"new","priority":1},{"slug":"y","priority":9}]}`)

	got := withCodexClientModels(data, additions)

	models := ojson.GetBytes(got, "models").Array()
	var slugs []string
	for _, m := range models {
		slugs = append(slugs, m.Get("slug").String())
	}
	if len(slugs) != 3 || slugs[0] != "new" || slugs[1] != "x" || slugs[2] != "y" {
		t.Fatalf("slugs = %v, want [new x y]", slugs)
	}
	if p := models[2].Get("priority").Int(); p != 2 {
		t.Errorf("upstream y priority = %d, want 2", p)
	}
	if etag := ojson.GetBytes(got, "etag").String(); etag != "e" {
		t.Errorf("etag = %q, want other fields kept", etag)
	}
}

func TestWithCodexClientModelsReturnsInputWhenNothingChanges(t *testing.T) {
	data := []byte(`{"models":[{"slug":"x"}]}`)
	if got := withCodexClientModels(data, []byte(`{"models":[{"slug":"x"}]}`)); !bytes.Equal(got, data) {
		t.Errorf("got %s, want input unchanged", got)
	}
	for _, bad := range [][]byte{[]byte(`{}`), []byte(`not json`), nil, []byte(`{"models":[]}`)} {
		if got := withCodexClientModels(bad, additionalCodexClientModels); !bytes.Equal(got, bad) {
			t.Errorf("withCodexClientModels on %q = %q, want input unchanged", bad, got)
		}
	}
}

func TestBundledAdditionsParse(t *testing.T) {
	// Both files may be empty once upstream lists every addition.
	sections := ojson.ParseBytes(additionalModels)
	if !sections.IsObject() {
		t.Fatal("additions/models.json is not an object")
	}
	if !ojson.GetBytes(additionalCodexClientModels, "models").IsArray() {
		t.Fatal("additions/codex_client_models.json has no models array")
	}
	for name, entries := range sections.Map() {
		for _, entry := range entries.Array() {
			if entry.Get("id").String() == "" {
				t.Errorf("%s has an entry without an id", name)
			}
		}
	}
	for _, entry := range ojson.GetBytes(additionalCodexClientModels, "models").Array() {
		if entry.Get("slug").String() == "" {
			t.Error("additions/codex_client_models.json has an entry without a slug")
		}
	}
}
