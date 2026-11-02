package connect

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

const (
	codexProviderTable = "model_providers.tpr"
	codexProfileTable  = "profiles.tpr"
	codexEnvKey        = "TPR_API_KEY"
	codexDefaultKey    = "model_provider"

	claudeBaseURL = "ANTHROPIC_BASE_URL"
	claudeToken   = "ANTHROPIC_AUTH_TOKEN"

	t3CodexInstance  = "tpr-codex"
	t3ClaudeInstance = "tpr-claude"
)

// CodexTOML returns the config blocks `tpr connect --codex` adds.
func CodexTOML(hub string) string {
	return fmt.Sprintf(`[model_providers.tpr]
name = "TokenProxyRouter"
base_url = %s
wire_api = "responses"
env_key = %s

[profiles.tpr]
model_provider = "tpr"
`, strconv.Quote(hub+"/v1"), strconv.Quote(codexEnvKey))
}

// tomlLines splits a file for line-based editing, remembering its line ending.
type tomlLines struct {
	lines []string
	eol   string
	// finalNewline records whether the file ended with a line ending.
	finalNewline bool
}

func splitLines(raw []byte) tomlLines {
	text := string(raw)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	parsed := tomlLines{eol: eol, finalNewline: strings.HasSuffix(normalized, "\n")}
	if normalized != "" {
		parsed.lines = strings.Split(strings.TrimSuffix(normalized, "\n"), "\n")
	}
	return parsed
}

func (t tomlLines) join() []byte {
	text := strings.Join(t.lines, t.eol)
	if len(t.lines) > 0 && t.finalNewline {
		text += t.eol
	}
	return []byte(text)
}

// tableHeader returns the table a line opens, or "" when it is not a table header.
func tableHeader(line string) string {
	trimmed := strings.TrimSpace(line)
	if comment := strings.Index(trimmed, "#"); comment > 0 {
		trimmed = strings.TrimSpace(trimmed[:comment])
	}
	if strings.HasPrefix(trimmed, "[[") || !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
			return strings.TrimSpace(trimmed[2 : len(trimmed)-2])
		}
		return ""
	}
	return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
}

// headerIndexes returns the line numbers of all table headers, skipping anything
// inside a multi-line string.
func headerIndexes(lines []string) []int {
	var headers []int
	inString := ""
	for i, line := range lines {
		for _, quote := range []string{`"""`, `'''`} {
			if inString != "" && inString != quote {
				continue
			}
			if strings.Count(line, quote)%2 == 1 {
				if inString == "" {
					inString = quote
				} else {
					inString = ""
				}
			}
		}
		if inString == "" && tableHeader(line) != "" {
			headers = append(headers, i)
		}
	}
	return headers
}

// sectionRange returns the lines of one table, or ok=false when it is absent.
func sectionRange(lines []string, table string) (start, end int, ok bool) {
	headers := headerIndexes(lines)
	for index, line := range headers {
		if tableHeader(lines[line]) != table {
			continue
		}
		end = len(lines)
		if index+1 < len(headers) {
			end = headers[index+1]
		}
		// Comments and blank lines just above the next table belong to that table.
		for end > line+1 && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#")) {
			end--
		}
		return line, end, true
	}
	return 0, 0, false
}

func replaceSection(lines []string, table string, block []string) []string {
	start, end, ok := sectionRange(lines, table)
	if ok {
		out := append([]string{}, lines[:start]...)
		out = append(out, block...)
		return append(out, lines[end:]...)
	}
	out := append([]string{}, lines...)
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	return append(out, block...)
}

func removeSection(lines []string, table string) []string {
	start, end, ok := sectionRange(lines, table)
	if !ok {
		return lines
	}
	out := append([]string{}, lines[:start]...)
	rest := lines[end:]
	// Drop the blank line that separated the section from what came before.
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" && (len(rest) == 0 || strings.TrimSpace(rest[0]) == "") {
		out = out[:len(out)-1]
	}
	return append(out, rest...)
}

func firstHeader(lines []string) int {
	if headers := headerIndexes(lines); len(headers) > 0 {
		return headers[0]
	}
	return len(lines)
}

func rootKeyLine(lines []string, key string) int {
	for i := 0; i < firstHeader(lines); i++ {
		name, _, ok := strings.Cut(lines[i], "=")
		if ok && strings.TrimSpace(name) == key {
			return i
		}
	}
	return -1
}

func setRootKey(lines []string, key, line string) []string {
	if index := rootKeyLine(lines, key); index >= 0 {
		out := append([]string{}, lines...)
		out[index] = line
		return out
	}
	at := firstHeader(lines)
	// Keep the key above any blank line that separates the root from the first table.
	for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	out := append([]string{}, lines[:at]...)
	out = append(out, line)
	return append(out, lines[at:]...)
}

func parseTOML(raw []byte) (map[string]any, error) {
	parsed := map[string]any{}
	if err := toml.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// withoutTPR returns a parsed Codex config minus everything TPR owns.
func withoutTPR(parsed map[string]any, ignoreDefault bool) map[string]any {
	out := map[string]any{}
	for key, value := range parsed {
		out[key] = value
	}
	for _, table := range []string{"model_providers", "profiles"} {
		section, ok := out[table].(map[string]any)
		if !ok {
			continue
		}
		copied := map[string]any{}
		for key, value := range section {
			if key != "tpr" {
				copied[key] = value
			}
		}
		if len(copied) == 0 {
			delete(out, table)
		} else {
			out[table] = copied
		}
	}
	if ignoreDefault {
		delete(out, codexDefaultKey)
	}
	return out
}

// MergeCodex adds or updates TPR's tables in a Codex config.toml, leaving every
// other line alone. It parses before and after and refuses to return a result in
// which anything else changed meaning.
func MergeCodex(original []byte, hub string, setDefault bool) ([]byte, error) {
	before, err := parseTOML(original)
	if err != nil {
		return nil, fmt.Errorf("the existing file is not valid TOML, so it was left alone: %w", err)
	}
	parts := splitLines(original)
	block := strings.Split(strings.TrimRight(CodexTOML(hub), "\n"), "\n")
	var provider, profile []string
	for i, line := range block {
		if line == "" {
			provider, profile = block[:i], block[i+1:]
			break
		}
	}
	lines := replaceSection(parts.lines, codexProviderTable, provider)
	if _, _, ok := sectionRange(parts.lines, codexProfileTable); !ok {
		lines = append(lines, "")
	}
	lines = replaceSection(lines, codexProfileTable, profile)
	if setDefault {
		lines = setRootKey(lines, codexDefaultKey, codexDefaultKey+` = "tpr"`)
	}
	parts.lines = lines
	parts.finalNewline = true
	merged := parts.join()

	after, err := parseTOML(merged)
	if err != nil {
		return nil, fmt.Errorf("could not add the tpr tables without breaking this file (is tpr already defined some other way?): %w", err)
	}
	if !reflect.DeepEqual(withoutTPR(before, setDefault), withoutTPR(after, setDefault)) {
		return nil, errors.New("adding the tpr tables would change other settings in this file, so it was left alone. Add them by hand; `tpr connect --dry-run` shows what is needed")
	}
	providerTable, ok := nestedMap(after, "model_providers", "tpr")
	if !ok || providerTable["base_url"] != hub+"/v1" || providerTable["env_key"] != codexEnvKey || providerTable["wire_api"] != "responses" {
		return nil, errors.New("the tpr provider did not come out as expected, so the file was left alone")
	}
	return merged, nil
}

func nestedMap(root map[string]any, keys ...string) (map[string]any, bool) {
	current := root
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// UnmergeCodex removes TPR's tables from a file edited since connect ran. The
// default provider goes back to its value in the original file.
func UnmergeCodex(current, original []byte) ([]byte, error) {
	parts := splitLines(current)
	lines := removeSection(parts.lines, codexProviderTable)
	lines = removeSection(lines, codexProfileTable)
	if index := rootKeyLine(lines, codexDefaultKey); index >= 0 && strings.Contains(lines[index], `"tpr"`) {
		originalLines := splitLines(original).lines
		if originalIndex := rootKeyLine(originalLines, codexDefaultKey); originalIndex >= 0 {
			lines = append([]string{}, lines...)
			lines[index] = originalLines[originalIndex]
		} else {
			lines = append(append([]string{}, lines[:index]...), lines[index+1:]...)
		}
	}
	parts.lines = lines
	result := parts.join()
	if _, err := parseTOML(result); err != nil {
		return nil, fmt.Errorf("removing the tpr tables would break this file: %w", err)
	}
	return result, nil
}

func jsonValue(value any) (json.RawMessage, error) {
	encoded, err := encodeJSON(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// MergeClaude sets the hub and key in the env block of Claude Code's settings.json.
func MergeClaude(original []byte, hub, key string) ([]byte, error) {
	root, err := parseObject(original)
	if err != nil {
		return nil, fmt.Errorf("the existing file is not valid JSON, so it was left alone: %w", err)
	}
	env := &object{}
	if raw, ok := root.get("env"); ok {
		if env, err = parseObject(raw); err != nil {
			return nil, fmt.Errorf(`the "env" setting is not an object, so the file was left alone: %w`, err)
		}
	}
	baseURL, err := jsonValue(hub)
	if err != nil {
		return nil, err
	}
	token, err := jsonValue(key)
	if err != nil {
		return nil, err
	}
	env.set(claudeBaseURL, baseURL)
	env.set(claudeToken, token)
	rawEnv, err := env.raw()
	if err != nil {
		return nil, err
	}
	root.set("env", rawEnv)
	return root.render(original)
}

// UnmergeClaude removes the hub and key from a file edited since connect ran. Any
// value the two settings held before connect comes back from the original file.
func UnmergeClaude(current, original []byte) ([]byte, error) {
	root, err := parseObject(current)
	if err != nil {
		return nil, fmt.Errorf("the file is no longer valid JSON: %w", err)
	}
	rawEnv, ok := root.get("env")
	if !ok {
		return current, nil
	}
	env, err := parseObject(rawEnv)
	if err != nil {
		return nil, err
	}
	originalEnv := &object{}
	if originalRoot, errOriginal := parseObject(original); errOriginal == nil {
		if raw, found := originalRoot.get("env"); found {
			if parsed, errEnv := parseObject(raw); errEnv == nil {
				originalEnv = parsed
			}
		}
	}
	for _, name := range []string{claudeBaseURL, claudeToken} {
		if value, found := originalEnv.get(name); found {
			env.set(name, value)
		} else {
			env.remove(name)
		}
	}
	if env.empty() {
		root.remove("env")
	} else {
		updated, err := env.raw()
		if err != nil {
			return nil, err
		}
		root.set("env", updated)
	}
	return root.render(current)
}

// t3EnvVar is one entry of a T3 Code provider instance's environment.
type t3EnvVar struct {
	Name          string `json:"name"`
	Value         string `json:"value"`
	Sensitive     bool   `json:"sensitive"`
	ValueRedacted bool   `json:"valueRedacted,omitempty"`
}

type t3Instance struct {
	Driver      string            `json:"driver"`
	DisplayName string            `json:"displayName"`
	Enabled     bool              `json:"enabled"`
	Environment []t3EnvVar        `json:"environment"`
	Config      map[string]string `json:"config,omitempty"`
}

func t3Instances(hub string) map[string]t3Instance {
	launchArgs := strings.Join([]string{
		"-c model_provider=tpr",
		"-c model_providers.tpr.name=TokenProxyRouter",
		"-c model_providers.tpr.base_url=" + hub + "/v1",
		"-c model_providers.tpr.env_key=" + codexEnvKey,
		"-c model_providers.tpr.wire_api=responses",
	}, " ")
	return map[string]t3Instance{
		t3CodexInstance: {
			Driver:      "codex",
			DisplayName: "Router: Codex",
			Enabled:     true,
			Environment: []t3EnvVar{{Name: codexEnvKey, Sensitive: true, ValueRedacted: true}},
			Config:      map[string]string{"launchArgs": launchArgs},
		},
		t3ClaudeInstance: {
			Driver:      "claudeAgent",
			DisplayName: "Router: Claude",
			Enabled:     true,
			Environment: []t3EnvVar{
				{Name: claudeBaseURL, Value: hub},
				{Name: claudeToken, Sensitive: true, ValueRedacted: true},
			},
		},
	}
}

// MergeT3 adds TPR's two provider instances to T3 Code's settings.json, leaving
// every other instance and setting alone.
func MergeT3(original []byte, hub string) ([]byte, error) {
	root, err := parseObject(original)
	if err != nil {
		return nil, fmt.Errorf("the existing file is not valid JSON, so it was left alone: %w", err)
	}
	instances := &object{}
	if raw, ok := root.get("providerInstances"); ok {
		if instances, err = parseObject(raw); err != nil {
			return nil, fmt.Errorf(`"providerInstances" is not an object, so the file was left alone: %w`, err)
		}
	}
	definitions := t3Instances(hub)
	for _, id := range []string{t3CodexInstance, t3ClaudeInstance} {
		encoded, err := jsonValue(definitions[id])
		if err != nil {
			return nil, err
		}
		instances.set(id, encoded)
	}
	rawInstances, err := instances.raw()
	if err != nil {
		return nil, err
	}
	root.set("providerInstances", rawInstances)
	return root.render(original)
}

// UnmergeT3 removes TPR's two instances from a file edited since connect ran.
func UnmergeT3(current []byte) ([]byte, error) {
	root, err := parseObject(current)
	if err != nil {
		return nil, fmt.Errorf("the file is no longer valid JSON: %w", err)
	}
	raw, ok := root.get("providerInstances")
	if !ok {
		return current, nil
	}
	instances, err := parseObject(raw)
	if err != nil {
		return nil, err
	}
	instances.remove(t3CodexInstance)
	instances.remove(t3ClaudeInstance)
	updated, err := instances.raw()
	if err != nil {
		return nil, err
	}
	root.set("providerInstances", updated)
	return root.render(current)
}

// T3SecretPath returns where T3 Code keeps one sensitive variable. The names use
// unpadded base64url, the form Node's Buffer "base64url" encoding produces.
func T3SecretPath(userdata, instanceID, varName string) string {
	encode := base64.RawURLEncoding.EncodeToString
	return filepath.Join(userdata, "secrets", "provider-env-"+encode([]byte(instanceID))+"-"+encode([]byte(varName))+".bin")
}
