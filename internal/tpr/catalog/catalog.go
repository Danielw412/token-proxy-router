// Package catalog adds models that TPR ships ahead of upstream's model catalogs.
//
// Upstream's catalogs are embedded at build time and refreshed from upstream's
// servers while the hub runs, so a model OpenAI or Anthropic has released but
// upstream has not listed yet would be missing. The registry passes every
// catalog it loads through these functions, which add each bundled model that
// the catalog lacks. A model upstream already lists is left exactly as upstream
// has it, so an addition retires itself once upstream catches up and can then be
// deleted from additions/.
//
// Any parse problem returns the catalog unchanged, and an empty catalog or
// section stays empty, so the registry still rejects what it would reject
// without TPR. An addition must never cost the hub a catalog or hide a bad one.
package catalog

import (
	"bytes"
	_ "embed"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// additionalModels holds registry entries keyed by catalog section, in the
// shape of upstream's models.json.
//
//go:embed additions/models.json
var additionalModels []byte

// additionalCodexClientModels holds Codex client catalog entries, in the shape
// of upstream's codex_client_models.json.
//
//go:embed additions/codex_client_models.json
var additionalCodexClientModels []byte

// WithModels returns the models.json catalog data with TPR's additional models
// appended to each section that lacks them.
func WithModels(data []byte) []byte {
	return withModels(data, additionalModels)
}

// WithCodexClientModels returns the Codex client catalog data with TPR's
// additional models placed first, ahead of models with the same priority.
func WithCodexClientModels(data []byte) []byte {
	return withCodexClientModels(data, additionalCodexClientModels)
}

func withModels(data, additions []byte) []byte {
	sections := ojson.ParseBytes(additions)
	if !sections.IsObject() || !ojson.ParseBytes(data).IsObject() {
		return data
	}
	doc := ojson.NewDoc(data)
	changed := false
	for section, entries := range sections.Map() {
		if strings.ContainsAny(section, ".*?#|@\\") {
			continue
		}
		existing := ojson.GetBytesNoCopy(data, section)
		if !existing.IsArray() || len(existing.Array()) == 0 {
			// An empty section is invalid upstream; filling it would hide that.
			continue
		}
		present := ids(existing, "id")
		for _, entry := range entries.Array() {
			id := entry.Get("id").String()
			if id == "" || present[id] {
				continue
			}
			if errSet := doc.SetRaw(section+".-1", entry.Raw); errSet != nil {
				return data
			}
			present[id] = true
			changed = true
		}
	}
	if !changed {
		return data
	}
	return doc.Bytes()
}

func withCodexClientModels(data, additions []byte) []byte {
	existing := ojson.GetBytesNoCopy(data, "models")
	if !existing.IsArray() || len(existing.Array()) == 0 {
		// An empty catalog is invalid upstream; filling it would hide that.
		return data
	}
	present := ids(existing, "slug")
	var added []string
	for _, entry := range ojson.GetBytes(additions, "models").Array() {
		slug := entry.Get("slug").String()
		if slug == "" || present[slug] {
			continue
		}
		added = append(added, entry.Raw)
		present[slug] = true
	}
	if len(added) == 0 {
		return data
	}
	var models bytes.Buffer
	models.WriteByte('[')
	models.WriteString(strings.Join(added, ","))
	for _, entry := range existing.Array() {
		models.WriteByte(',')
		models.WriteString(entry.Raw)
	}
	models.WriteByte(']')
	doc := ojson.NewDoc(data)
	if errSet := doc.SetRawBytes("models", models.Bytes()); errSet != nil {
		return data
	}
	return doc.Bytes()
}

func ids(list ojson.Result, key string) map[string]bool {
	out := make(map[string]bool)
	for _, entry := range list.Array() {
		if id := entry.Get(key).String(); id != "" {
			out[id] = true
		}
	}
	return out
}
