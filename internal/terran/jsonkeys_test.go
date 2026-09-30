package terran

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// writeJSONKeysCatalog writes a catalog whose json-keys sources hold the given
// JSON per target; instruction sources contain "# <target>\n".
func writeJSONKeysCatalog(t *testing.T, repo, id string, instructions []Instruction, sources map[string]string) {
	t.Helper()
	write := func(source, content string) {
		path := filepath.Join(repo, filepath.FromSlash(source))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, instruction := range instructions {
		write(instruction.Source, "# "+instruction.Target+"\n")
	}
	var items []JSONKeysItem
	for target, content := range sources {
		source := "settings/" + target + ".json"
		write(source, content)
		items = append(items, JSONKeysItem{Target: target, Source: source})
	}
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: id, Version: "0.1.0", Projections: []Projection{}, Instructions: instructions, JSONKeys: items}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write("terran.json", string(data)+"\n")
}

// claudeSettings creates ~/.claude and, unless content is empty, settings.json.
func claudeSettings(t *testing.T, home, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func decodedJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return value
}

func jsonKeyActions(t *testing.T, plan PlanResult) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, action := range plan.Actions {
		if action.Kind == "json-keys" {
			got[action.Name] = action.Action
		}
	}
	return got
}

func assertJSONKeyPlan(t *testing.T, want map[string]string) PlanResult {
	t.Helper()
	plan, err := Plan("all")
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonKeyActions(t, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("json key actions %v, want %v: %#v", got, want, plan)
	}
	return plan
}

func loadTestReceipt(t *testing.T) Receipt {
	t.Helper()
	paths, _ := ResolvePaths()
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadReceipt(paths, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestJSONKeysLifecycleKeepsUnownedKeys(t *testing.T) {
	home, repo := fileEnvironment(t)
	unowned := `{"theme":"dark","numbers":[1,2.50,1e3],"hooks":{"Stop":[{"command":"a && b <x>"}]},"model":"opus"}`
	settings := claudeSettings(t, home, unowned, 0o600)
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus","permissions":{"allow":["Bash(ls)"]}}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"model": "adopt", "permissions": "create"})
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	before := map[string]any{}
	_ = json.Unmarshal([]byte(unowned), &before)
	after := decodedJSON(t, settings)
	for _, key := range []string{"theme", "numbers", "hooks", "model"} {
		if !reflect.DeepEqual(after[key], before[key]) {
			t.Fatalf("unowned key %s changed: %#v -> %#v", key, before[key], after[key])
		}
	}
	if !reflect.DeepEqual(after["permissions"], map[string]any{"allow": []any{"Bash(ls)"}}) || fileMode(t, settings) != 0o600 {
		t.Fatalf("owned key not written or mode lost: %#v %04o", after, fileMode(t, settings))
	}
	if data, _ := os.ReadFile(settings); !bytes.Contains(data, []byte("a && b <x>")) || !bytes.HasSuffix(data, []byte("}\n")) {
		t.Fatalf("settings not written as unescaped indented JSON: %s", data)
	}
	receipt := loadTestReceipt(t)
	if len(receipt.JSONKeys) != 2 || receipt.JSONKeys[0].Key != "model" || receipt.JSONKeys[0].Origin != "adopted" || string(receipt.JSONKeys[0].OriginalValue) != `"opus"` || receipt.JSONKeys[1].Key != "permissions" || receipt.JSONKeys[1].Origin != "created" || receipt.JSONKeys[1].Catalog != "test-catalog" {
		t.Fatalf("json key receipt: %#v", receipt.JSONKeys)
	}
	if plan := assertJSONKeyPlan(t, map[string]string{"model": "noop", "permissions": "noop"}); !plan.Clean {
		t.Fatalf("applied plan not clean: %#v", plan)
	}

	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus","permissions":{"allow":["Bash(ls)","Read"]}}`})
	assertJSONKeyPlan(t, map[string]string{"model": "noop", "permissions": "update"})
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if got := decodedJSON(t, settings)["permissions"]; !reflect.DeepEqual(got, map[string]any{"allow": []any{"Bash(ls)", "Read"}}) {
		t.Fatalf("owned key not updated: %#v", got)
	}

	// A harness rewriting the file with other formatting is not drift; a changed owned value is.
	current := decodedJSON(t, settings)
	compact, _ := json.Marshal(current)
	if err := os.WriteFile(settings, compact, 0o600); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"model": "noop", "permissions": "noop"})
	current["permissions"] = map[string]any{"allow": []any{"everything"}}
	edited, _ := json.Marshal(current)
	if err := os.WriteFile(settings, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"model": "noop", "permissions": "blocked_drift"})
	if err := os.WriteFile(settings, compact, 0o600); err != nil {
		t.Fatal(err)
	}

	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"permissions":{"allow":["Bash(ls)","Read"]}}`})
	plan := assertJSONKeyPlan(t, map[string]string{"model": "release", "permissions": "noop"})
	if action := actionByIDOrFail(t, plan, "json-keys/claude-settings/model"); !strings.Contains(action.Reason, "keep its value") {
		t.Fatalf("release reason %q", action.Reason)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if decodedJSON(t, settings)["model"] != "opus" || len(loadTestReceipt(t).JSONKeys) != 1 {
		t.Fatal("released key was not kept in the file and dropped from the receipt")
	}

	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{}`})
	assertJSONKeyPlan(t, map[string]string{"permissions": "remove"})
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	after = decodedJSON(t, settings)
	if _, ok := after["permissions"]; ok || len(loadTestReceipt(t).JSONKeys) != 0 {
		t.Fatalf("created key not removed: %#v", after)
	}
	for _, key := range []string{"theme", "numbers", "hooks", "model"} {
		if !reflect.DeepEqual(after[key], before[key]) {
			t.Fatalf("unowned key %s changed by removal", key)
		}
	}
}

func TestJSONKeysSyncedEditRecordsReceiptOnly(t *testing.T) {
	home, repo := fileEnvironment(t)
	settings := claudeSettings(t, home, `{"theme":"dark"}`, 0o600)
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus"}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"model": "blocked_drift"})
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"sonnet"}`})
	plan := assertJSONKeyPlan(t, map[string]string{"model": "update"})
	if action := actionByIDOrFail(t, plan, "json-keys/claude-settings/model"); action.Reason != matchesCatalogReason {
		t.Fatalf("reason %q", action.Reason)
	}
	edited, _ := os.ReadFile(settings)
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(settings); !bytes.Equal(after, edited) {
		t.Fatalf("recording a synced edit rewrote the file: %s", after)
	}
	if plan := assertJSONKeyPlan(t, map[string]string{"model": "noop"}); !plan.Clean {
		t.Fatalf("plan after recording: %#v", plan)
	}
}

func TestJSONKeysOpenCodeSettingsTarget(t *testing.T) {
	home, repo := fileEnvironment(t)
	config := filepath.Join(home, "config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"agents":{"naru":{"mode":"primary"}},"shell":"zsh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"opencode-settings": `{"shell":"zsh","permission":{"bash":"ask"}}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	plan := assertJSONKeyPlan(t, map[string]string{"shell": "adopt", "permission": "create"})
	if action := actionByIDOrFail(t, plan, "json-keys/opencode-settings/permission"); action.Destination != config {
		t.Fatalf("destination %q", action.Destination)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	after := decodedJSON(t, config)
	if !reflect.DeepEqual(after["agents"], map[string]any{"naru": map[string]any{"mode": "primary"}}) || !reflect.DeepEqual(after["permission"], map[string]any{"bash": "ask"}) || fileMode(t, config) != 0o600 {
		t.Fatalf("opencode settings: %#v %04o", after, fileMode(t, config))
	}

	// The whole-file config target and the settings keys cannot share opencode.json.
	if err := os.WriteFile(filepath.Join(repo, "config.json"), []byte(`{"shell":"zsh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	data, _ := os.ReadFile(filepath.Join(repo, "terran.json"))
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Configs = []Config{{Target: "opencode-config", Source: "config.json"}}
	data, _ = json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan("all"); err == nil || !strings.Contains(err.Error(), "both resolve to") {
		t.Fatalf("shared destination not rejected: %v", err)
	}
}

func TestOpenCodeConfigToSettingsKeysReleasesWholeFile(t *testing.T) {
	home, repo := fileEnvironment(t)
	writeCatalogWithConfigs(t, repo, nil, []Config{{Target: "opencode-config", Source: "config/opencode.json"}})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "config", "opencode", "opencode.json")
	// Naru adds its own key after the whole-file apply.
	current := decodedJSON(t, config)
	current["agents"] = map[string]any{"naru": map[string]any{"mode": "primary"}}
	data, _ := json.Marshal(current)
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"opencode-settings": `{"default_agent":"naru","shell":"zsh"}`})
	plan, err := Plan("all")
	if err != nil {
		t.Fatal(err)
	}
	if action := actionByIDOrFail(t, plan, "config/opencode-config"); action.Action != "release" {
		t.Fatalf("whole-file config not released: %#v", action)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	after := decodedJSON(t, config)
	if after["shell"] != "zsh" || after["default_agent"] != "naru" || !reflect.DeepEqual(after["agents"], current["agents"]) {
		t.Fatalf("opencode.json after release: %#v", after)
	}
	if receipt := loadTestReceipt(t); len(receipt.Managed) != 0 || len(receipt.JSONKeys) != 2 {
		t.Fatalf("receipt after release: %#v", receipt)
	}
	if plan, _ := Plan("all"); !plan.Clean {
		t.Fatalf("plan after release: %#v", plan)
	}
}

// The receipt is written indented; an adopted object value must still compare
// equal to its applied hash so removal releases it instead of restoring.
func TestJSONKeysAdoptedObjectValueReleases(t *testing.T) {
	home, repo := fileEnvironment(t)
	claudeSettings(t, home, `{"a":{"b":[1,2],"c":"<x>"}}`, 0o644)
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"a":{"c":"<x>","b":[1,2]}}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"a": "adopt"})
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if keys := loadTestReceipt(t).JSONKeys; len(keys) != 1 || string(keys[0].OriginalValue) != `{"b":[1,2],"c":"<x>"}` {
		t.Fatalf("receipt original value not canonical: %#v", keys)
	}
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{}`})
	assertJSONKeyPlan(t, map[string]string{"a": "release"})
}

func TestJSONKeysCreateFileOnlyUnderExistingParent(t *testing.T) {
	home, repo := fileEnvironment(t)
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"t3-settings": `{"theme":"dark"}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	plan := assertJSONKeyPlan(t, map[string]string{"theme": "blocked_collision"})
	if action := actionByIDOrFail(t, plan, "json-keys/t3-settings/theme"); action.Reason != "parent directory missing" {
		t.Fatalf("reason %q", action.Reason)
	}
	settings := filepath.Join(home, ".t3", "userdata", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"theme": "create"})
	if _, err := Apply("t3", "test"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(settings); string(data) != "{\n  \"theme\": \"dark\"\n}\n" || fileMode(t, settings) != 0o644 {
		t.Fatalf("settings file %q mode %04o", data, fileMode(t, settings))
	}
}

func TestJSONKeysBlockUnsafeDestinations(t *testing.T) {
	cases := map[string]func(t *testing.T, settings string){
		"duplicate key": func(t *testing.T, settings string) {
			_ = os.WriteFile(settings, []byte(`{"theme":"dark","theme":"light"}`), 0o644)
		},
		"not an object": func(t *testing.T, settings string) { _ = os.WriteFile(settings, []byte(`["theme"]`), 0o644) },
		"trailing data": func(t *testing.T, settings string) { _ = os.WriteFile(settings, []byte(`{} {}`), 0o644) },
		"symlink": func(t *testing.T, settings string) {
			target := filepath.Join(filepath.Dir(settings), "real.json")
			_ = os.WriteFile(target, []byte(`{}`), 0o644)
			if err := os.Symlink(target, settings); err != nil {
				t.Fatal(err)
			}
		},
		"different unowned value": func(t *testing.T, settings string) {
			_ = os.WriteFile(settings, []byte(`{"theme":"light"}`), 0o644)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			home, repo := fileEnvironment(t)
			settings := claudeSettings(t, home, "", 0)
			setup(t, settings)
			writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"theme":"dark"}`})
			if _, _, err := Enroll(repo, "test", "", false); err != nil {
				t.Fatal(err)
			}
			info, _ := os.Lstat(settings)
			before, _ := os.ReadFile(settings)
			assertJSONKeyPlan(t, map[string]string{"theme": "blocked_collision"})
			if _, err := Apply("all", "test"); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(settings)
			afterInfo, _ := os.Lstat(settings)
			if !bytes.Equal(before, after) || afterInfo.Mode() != info.Mode() {
				t.Fatal("blocked settings file was modified")
			}
		})
	}
}

func TestLeftoverQuarantineBlocksEveryItemForItsDirectory(t *testing.T) {
	home, repo := fileEnvironment(t)
	claudeSettings(t, home, `{}`, 0o644)
	writeJSONKeysCatalog(t, repo, "test-catalog", []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}}, map[string]string{"claude-settings": `{"model":"opus","theme":"dark"}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(home, ".claude", ".terran-quarantine-123")
	if err := os.Mkdir(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, "all")
	for _, id := range []string{"instruction/claude-global", "json-keys/claude-settings/model", "json-keys/claude-settings/theme"} {
		action := actionByIDOrFail(t, plan, id)
		if action.Action != "blocked_collision" || !strings.Contains(action.Reason, "leftover Terran quarantine found at "+quarantine) {
			t.Fatalf("%s not blocked by the leftover quarantine: %#v", id, action)
		}
	}
	decisions := map[string]CollisionDecision{"instruction/claude-global": CollisionReplace, "json-keys/claude-settings/model": CollisionReplace}
	if applied, err := ApplyWithOptions("all", "test", ApplyOptions{Decisions: decisions}); err != nil || !blocked(applied) {
		t.Fatalf("replace decision bypassed the quarantine: %#v %v", applied, err)
	}
	if err := os.Remove(quarantine); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"model": "create", "theme": "create"})
}

func TestJSONKeysSourceValidation(t *testing.T) {
	cases := map[string]string{
		"credential key":  `{"apiToken":"literal-value"}`,
		"local URL":       `{"endpoint":"http://127.0.0.1:8080"}`,
		"machine path":    `{"statusLine":{"command":"/opt/tool/bin/status"}}`,
		"duplicate key":   `{"theme":"dark","theme":"light"}`,
		"not an object":   `["theme"]`,
		"item id key":     `{"a/b":true}`,
		"whitespace key":  `{"a b":true}`,
		"trailing object": `{}{}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			_, repo := fileEnvironment(t)
			writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": source})
			if _, err := LoadManifest(repo); !hasCode(err, CodeManifestInvalid) {
				t.Fatalf("unsafe json-keys source accepted: %v", err)
			}
		})
	}
	t.Run("duplicate target", func(t *testing.T) {
		_, repo := fileEnvironment(t)
		writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"theme":"dark"}`})
		loaded, err := LoadManifest(repo)
		if err != nil {
			t.Fatal(err)
		}
		manifest := loaded.Manifest
		manifest.JSONKeys = append(manifest.JSONKeys, JSONKeysItem{Target: "claude-settings", Source: "settings/claude-settings.json"})
		data, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(repo); !hasCode(err, CodeManifestInvalid) {
			t.Fatalf("duplicate json-keys target accepted: %v", err)
		}
	})
}

func TestJSONKeysHeldKeyIsUntouched(t *testing.T) {
	home, repo := fileEnvironment(t)
	settings := claudeSettings(t, home, `{"model":"sonnet"}`, 0o644)
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus","theme":"dark"}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"model": "blocked_collision", "theme": "create"})
	if _, err := Hold("json-keys/claude-settings/model"); err != nil {
		t.Fatal(err)
	}
	assertJSONKeyPlan(t, map[string]string{"model": "held", "theme": "create"})
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	if got := decodedJSON(t, settings); got["model"] != "sonnet" || got["theme"] != "dark" {
		t.Fatalf("held key changed or other key missing: %#v", got)
	}
	if keys := loadTestReceipt(t).JSONKeys; len(keys) != 1 || keys[0].Key != "theme" {
		t.Fatalf("held key recorded: %#v", keys)
	}
}

func TestJSONKeysFromOverlayCatalog(t *testing.T) {
	home, repo := fileEnvironment(t)
	overlay := filepath.Join(filepath.Dir(repo), "private")
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"theme":"dark"}`})
	writeJSONKeysCatalog(t, overlay, "private", nil, map[string]string{"t3-settings": `{"theme":"light"}`})
	claudeSettings(t, home, "", 0)
	t3 := filepath.Join(home, ".t3", "userdata", "settings.json")
	if err := os.MkdirAll(filepath.Dir(t3), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", overlay, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	keys := loadTestReceipt(t).JSONKeys
	catalogs := []string{}
	for _, key := range keys {
		catalogs = append(catalogs, key.Target+"="+key.Catalog)
	}
	sort.Strings(catalogs)
	if !reflect.DeepEqual(catalogs, []string{"claude-settings=test-catalog", "t3-settings=private"}) || decodedJSON(t, t3)["theme"] != "light" {
		t.Fatalf("overlay json keys: %v", catalogs)
	}

	// The same target in both catalogs is rejected.
	writeJSONKeysCatalog(t, overlay, "private", nil, map[string]string{"claude-settings": `{"model":"opus"}`})
	if _, err := Plan("all"); err == nil || !strings.Contains(err.Error(), "json-keys target claude-settings") {
		t.Fatalf("duplicate overlay json-keys target: %v", err)
	}
}

// REVIEW FOCUS: a harness writing an unowned key after apply planned must stop
// the whole apply before any mutation of any kind.
func TestJSONKeysUnownedChangeAfterPlanIsPlanChanged(t *testing.T) {
	home, repo := fileEnvironment(t)
	settings := claudeSettings(t, home, `{"theme":"dark"}`, 0o644)
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus"}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	claude := []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}}
	writeJSONKeysCatalog(t, repo, "test-catalog", claude, map[string]string{"claude-settings": `{"model":"opus","permissions":{"allow":["Read"]}}`})
	plan, err := Plan("all")
	if err != nil || actionByIDOrFail(t, plan, "instruction/claude-global").Action != "create" || actionByIDOrFail(t, plan, "json-keys/claude-settings/permissions").Action != "create" {
		t.Fatalf("plan: %#v %v", plan, err)
	}
	paths, _ := ResolvePaths()
	receiptBefore, err := os.ReadFile(paths.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	harnessWrite := []byte("{\"model\":\"opus\",\"theme\":\"light\"}\n")
	mutated := false
	beforeInstructionMutation = func(Action) error { mutated = true; return nil }
	t.Cleanup(func() { beforeInstructionMutation = nil })
	_, err = ApplyWithOptions("all", "test", ApplyOptions{ConfirmPlan: func(PlanResult) error {
		return os.WriteFile(settings, harnessWrite, 0o644)
	}})
	if !hasCode(err, CodePlanChanged) || mutated {
		t.Fatalf("apply after unowned change: %v (mutation started: %v)", err, mutated)
	}
	if code, next := ErrorCode(err); code != CodePlanChanged || next != "run terran plan again" {
		t.Fatalf("error code %q next %q", code, next)
	}
	if data, _ := os.ReadFile(settings); !bytes.Equal(data, harnessWrite) {
		t.Fatalf("settings changed by refused apply: %s", data)
	}
	if receiptAfter, _ := os.ReadFile(paths.Receipt); !bytes.Equal(receiptAfter, receiptBefore) {
		t.Fatal("receipt changed by refused apply")
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude", "CLAUDE.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("instruction mutated despite plan_changed: %v", err)
	}
}

// A harness writing the settings file after apply verified it, but before the
// replacement, must win: plan_changed, its bytes kept, the receipt unchanged.
func TestJSONKeysConcurrentWriteAfterVerifyIsPlanChanged(t *testing.T) {
	writers := map[string]func(path string, data []byte) error{
		"in place": func(path string, data []byte) error { return os.WriteFile(path, data, 0o644) },
		"rename": func(path string, data []byte) error {
			tmp := path + ".tmp"
			if err := os.WriteFile(tmp, data, 0o644); err != nil {
				return err
			}
			return os.Rename(tmp, path)
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			home, repo := fileEnvironment(t)
			settings := claudeSettings(t, home, `{"theme":"dark"}`, 0o644)
			writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus"}`})
			if _, _, err := Enroll(repo, "test", "", false); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply("all", "test"); err != nil {
				t.Fatal(err)
			}
			writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus","permissions":{"allow":["Read"]}}`})
			paths, _ := ResolvePaths()
			receiptBefore, err := os.ReadFile(paths.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			injected := []byte("{\"model\":\"opus\",\"theme\":\"light\"}\n")
			beforeJSONKeysWrite = func(destination string) error { return write(destination, injected) }
			t.Cleanup(func() { beforeJSONKeysWrite = nil })
			if _, err := Apply("all", "test"); !hasCode(err, CodePlanChanged) {
				t.Fatalf("concurrent write: %v", err)
			}
			if data, _ := os.ReadFile(settings); !bytes.Equal(data, injected) {
				t.Fatalf("concurrent writer's bytes lost: %s", data)
			}
			if receiptAfter, _ := os.ReadFile(paths.Receipt); !bytes.Equal(receiptAfter, receiptBefore) {
				t.Fatal("receipt changed by refused apply")
			}
			if entries, _ := os.ReadDir(filepath.Dir(settings)); len(entries) != 1 {
				t.Fatalf("quarantine or temp files left behind: %v", entries)
			}
		})
	}
}

func TestJSONKeysRollbackRestoresPreviousBytes(t *testing.T) {
	home, repo := fileEnvironment(t)
	original := []byte("{\"theme\": \"dark\"}\n")
	settings := claudeSettings(t, home, string(original), 0o644)
	writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus"}`})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	beforeReceiptWrite = func() error { return errors.New("injected receipt failure") }
	t.Cleanup(func() { beforeReceiptWrite = nil })
	if _, err := Apply("all", "test"); err == nil {
		t.Fatal("injected failure ignored")
	}
	if data, _ := os.ReadFile(settings); !bytes.Equal(data, original) {
		t.Fatalf("settings not rolled back: %s", data)
	}
}

func TestLoadReceiptRejectsInvalidJSONKeys(t *testing.T) {
	cases := map[string]func(*ReceiptJSONKey){
		"unknown target":     func(entry *ReceiptJSONKey) { entry.Target = "opencode-config" },
		"empty key":          func(entry *ReceiptJSONKey) { entry.Key = "" },
		"bad hash":           func(entry *ReceiptJSONKey) { entry.AppliedHash = "abc" },
		"unknown catalog":    func(entry *ReceiptJSONKey) { entry.Catalog = "other" },
		"bad origin":         func(entry *ReceiptJSONKey) { entry.Origin = "replaced" },
		"created with value": func(entry *ReceiptJSONKey) { entry.OriginalValue = json.RawMessage(`"x"`) },
		"duplicate":          nil,
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			home, repo := fileEnvironment(t)
			claudeSettings(t, home, "", 0)
			writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus"}`})
			if _, _, err := Enroll(repo, "test", "", false); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply("all", "test"); err != nil {
				t.Fatal(err)
			}
			paths, _ := ResolvePaths()
			receipt := loadTestReceipt(t)
			if mutate == nil {
				receipt.JSONKeys = append(receipt.JSONKeys, receipt.JSONKeys[0])
			} else {
				mutate(&receipt.JSONKeys[0])
			}
			if err := atomicJSON(paths.Receipt, receipt); err != nil {
				t.Fatal(err)
			}
			enrollment, _ := LoadEnrollment(paths)
			if _, err := LoadReceipt(paths, enrollment); !hasCode(err, CodeReceiptInvalid) {
				t.Fatalf("invalid json key receipt accepted: %v", err)
			}
		})
	}
}
