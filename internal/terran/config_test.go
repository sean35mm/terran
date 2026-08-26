package terran

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testOpenCodeConfig = `{"$schema":"https://opencode.ai/config.json","default_agent":"naru-orchestrator","mcp":{"service":{"headers":{"Authorization":"{env:SERVICE_AUTHORIZATION_HEADER}"}}}}`

func configEnvironment(t *testing.T, withInstruction bool) (string, string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	var instructions []Instruction
	if withInstruction {
		instructions = []Instruction{{Target: "opencode-global", Source: "instructions/AGENTS.md"}}
	}
	writeCatalogWithConfigs(t, repo, instructions, []Config{{Target: "opencode-config", Source: "config/opencode.json"}})
	return home, repo
}

func writeCatalogWithConfigs(t *testing.T, repo string, instructions []Instruction, configs []Config) {
	t.Helper()
	for _, instruction := range instructions {
		path := filepath.Join(repo, filepath.FromSlash(instruction.Source))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# "+instruction.Target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, config := range configs {
		path := filepath.Join(repo, filepath.FromSlash(config.Source))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(path, []byte(testOpenCodeConfig), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: "test-catalog", Version: "0.1.0", Projections: []Projection{}, Instructions: instructions, Configs: configs}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeConfigSecurityValidation(t *testing.T) {
	valid := []string{
		testOpenCodeConfig,
		`{"token":"{env:OPENCODE_TOKEN}"}`,
		`{"privateKey":"{env:SERVICE_PRIVATE_KEY}"}`,
		`{"sessionCookie":"{env:SERVICE_COOKIE}"}`,
		`{"githubAuth":"{env:GITHUB_AUTH}"}`,
		`{"bearer":"{env:SERVICE_BEARER}"}`,
		`{"githubPat":"{env:GITHUB_PAT}"}`,
		`{"clientId":"public-client-id"}`,
		`{"oauth":{}}`,
		`{"command":["tool --url=https://example.com/mcp","Push / PR workflow"]}`,
		`{"description":"prefixhttp://` + "local" + `host is malformed prose, not a URL"}`,
	}
	for _, data := range valid {
		if err := validateOpenCodeConfig([]byte(data)); err != nil {
			t.Fatalf("valid config rejected: %v", err)
		}
	}
	placeholder := "NOT_" + "A_" + "CREDENTIAL"
	invalid := map[string][]byte{
		"non-object":        []byte(`[]`),
		"trailing":          []byte(`{} {}`),
		"duplicate":         []byte(`{"agent":{},"agent":{}}`),
		"nested credential": configJSON(t, "Authorization", map[string]string{"value": placeholder}),
		"bad env reference": configJSON(t, "token", "{env:"+"lowercase}"),
	}
	for _, key := range []string{"accessKey", "accessKeyId", "privateKeyPEM", "clientKey", "clientSecret", "signingKey", "secretAccessKey"} {
		invalid["credential key "+key] = configJSON(t, key, placeholder)
	}
	for _, key := range []string{"Cookie", "auth", "githubAuth", "bearer", "pat", "githubPat", "session_cookie"} {
		invalid["credential alias "+key] = configJSON(t, key, placeholder)
		invalid["credential query "+key] = configJSON(t, "url", "https://example.com/mcp?"+key+"="+placeholder)
	}
	personalPath := strings.Join([]string{"", "Users", "example", "config.json"}, "/")
	windowsPath := "C:" + `\` + strings.Join([]string{"Users", "example", "config.json"}, `\`)
	invalid["unix path"] = configJSON(t, "command", []string{personalPath})
	invalid["embedded unix path"] = configJSON(t, "command", []string{"--config=" + personalPath})
	invalid["whitespace unix path"] = configJSON(t, "command", []string{"tool --config " + personalPath})
	invalid["windows path"] = configJSON(t, "command", []string{windowsPath})
	invalid["embedded windows path"] = configJSON(t, "command", []string{"--config=" + windowsPath})
	invalid["UNC path"] = configJSON(t, "command", []string{strings.Repeat(`\`, 2) + `server\share\config.json`})
	invalid["slash UNC path"] = configJSON(t, "command", []string{"//server/share/config.json"})
	invalid["file URL"] = configJSON(t, "url", "file:"+"///tmp/config.json")
	invalid["embedded file URL"] = configJSON(t, "command", []string{"tool --config " + "file:" + "///tmp/config.json"})
	invalid["localhost URL"] = configJSON(t, "url", "http://"+"local"+"host/mcp")
	invalid["loopback URL"] = configJSON(t, "url", "http://"+strings.Join([]string{"127", "0", "0", "1"}, ".")+":3000/mcp")
	invalid["IPv6 loopback URL"] = configJSON(t, "url", "http://["+":"+":1]/mcp")
	invalid["trailing-dot loopback URL"] = configJSON(t, "url", "http://"+strings.Join([]string{"127", "0", "0", "1"}, ".")+".:3000/mcp")
	invalid["trailing-dot localhost"] = configJSON(t, "url", "https://"+"local"+"host."+"/mcp")
	invalid["embedded localhost URL"] = configJSON(t, "command", []string{"tool --url=http://" + "local" + "host:3000/mcp"})
	invalid["private URL"] = configJSON(t, "url", "http://"+strings.Join([]string{"192", "168", "1", "10"}, ".")+"/mcp")
	invalid["local hostname"] = configJSON(t, "url", "https://service."+"local/mcp")
	invalid["URL userinfo"] = configJSON(t, "url", "https://"+"user:"+placeholder+"@example.com/mcp")
	invalid["credential query"] = configJSON(t, "url", "https://example.com/mcp?"+"secretAccess"+"Key="+placeholder)
	for name, data := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := validateOpenCodeConfig(data); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func configJSON(t *testing.T, key string, value any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{key: value})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestConfigManifestFingerprintValidationAndDestinations(t *testing.T) {
	home, repo := configEnvironment(t, false)
	first, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConfigSources["opencode-config"] == "" || first.ConfigHashes["opencode-config"] == "" {
		t.Fatal("config source metadata missing")
	}
	writeCatalogWithConfigs(t, repo, nil, []Config{{Target: "opencode-config", Source: "config/opencode.json"}})
	second, err := LoadManifest(repo)
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatalf("stable config changed fingerprint: %v", err)
	}
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("opencode")
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Kind != "config" || plan.Actions[0].Destination != filepath.Join(home, "config", "opencode", "opencode.json") {
		t.Fatalf("XDG config plan: %#v %v", plan, err)
	}

	base := t.TempDir()
	home = filepath.Join(base, "home")
	repo = filepath.Join(base, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	writeCatalogWithConfigs(t, repo, nil, []Config{{Target: "opencode-config", Source: "config/opencode.json"}})
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	plan, err = Plan("opencode")
	if err != nil || plan.Actions[0].Destination != filepath.Join(home, ".config", "opencode", "opencode.json") {
		t.Fatalf("default config plan: %#v %v", plan, err)
	}
}

func TestConfigManifestRejectsInvalidEntriesAndCanonicalSourceIsSafe(t *testing.T) {
	t.Run("canonical source", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("..", "..", "config", "opencode", "opencode.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateOpenCodeConfig(data); err != nil {
			t.Fatalf("canonical config is unsafe: %v", err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*Manifest, string)
	}{
		{"unknown target", func(manifest *Manifest, _ string) { manifest.Configs[0].Target = "other" }},
		{"duplicate target", func(manifest *Manifest, _ string) { manifest.Configs = append(manifest.Configs, manifest.Configs[0]) }},
		{"absolute source", func(manifest *Manifest, repo string) {
			manifest.Configs[0].Source = filepath.Join(repo, "config", "opencode.json")
		}},
		{"escaping source", func(manifest *Manifest, _ string) { manifest.Configs[0].Source = "../opencode.json" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := configEnvironment(t, false)
			loaded, err := LoadManifest(repo)
			if err != nil {
				t.Fatal(err)
			}
			manifest := loaded.Manifest
			tc.mutate(&manifest, repo)
			data, _ := json.Marshal(manifest)
			if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(repo); err == nil {
				t.Fatal("invalid config manifest entry accepted")
			}
		})
	}

	t.Run("unsafe source data", func(t *testing.T) {
		_, repo := configEnvironment(t, false)
		unsafe := configJSON(t, "token", "NOT_"+"A_"+"CREDENTIAL")
		if err := os.WriteFile(filepath.Join(repo, "config", "opencode.json"), unsafe, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(repo); err == nil {
			t.Fatal("unsafe config source accepted by manifest loader")
		}
	})
}

func TestOpenCodeFilterSelectsInstructionAndConfig(t *testing.T) {
	_, repo := configEnvironment(t, true)
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("opencode")
	if err != nil || len(plan.Actions) != 2 || actionFor(plan, "config", "opencode-config").Target == "" || actionFor(plan, "instruction", "opencode-global").Target == "" {
		t.Fatalf("opencode filter did not select both files: %#v %v", plan, err)
	}
}

func TestFilteredSkillApplyPreservesConfigReceipt(t *testing.T) {
	_, repo := configEnvironment(t, false)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("opencode", "config-build"); err != nil {
		t.Fatal(err)
	}
	paths, _ := ResolvePaths()
	before, err := LoadReceipt(paths)
	if err != nil || len(before.Configs) != 1 {
		t.Fatalf("initial config receipt: %#v %v", before, err)
	}

	skillDir := filepath.Join(repo, "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: example\ndescription: test\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := loaded.Manifest
	manifest.Projections = []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("agents", "skill-build"); err != nil {
		t.Fatal(err)
	}
	after, err := LoadReceipt(paths)
	if err != nil || len(after.Configs) != 1 || after.Configs[0] != before.Configs[0] {
		t.Fatalf("filtered apply changed config ownership metadata: before=%#v after=%#v err=%v", before.Configs, after.Configs, err)
	}
}

func TestConfigCreateUpdateNoopDriftRemoveStatusDoctorAndReceipt(t *testing.T) {
	_, repo := configEnvironment(t, false)
	paths, _ := ResolvePaths()
	destination, _ := configDestination(paths, "opencode-config")
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	if plan, err := Plan("opencode"); err != nil || actionCount(plan, "create") != 1 {
		t.Fatalf("create plan: %#v %v", plan, err)
	}
	if _, err := Apply("opencode", "0.1.0-test"); err != nil {
		t.Fatal(err)
	}
	if fileMode(t, destination) != 0o600 {
		t.Fatalf("created config mode is %o", fileMode(t, destination))
	}
	receipt, err := LoadReceipt(paths)
	if err != nil || len(receipt.Configs) != 1 || len(receipt.Instructions) != 0 || receipt.Configs[0].Target != "opencode-config" {
		t.Fatalf("config receipt is not distinct: %#v %v", receipt, err)
	}
	if plan, err := Plan("opencode"); err != nil || !plan.Clean || actionCount(plan, "noop") != 1 {
		t.Fatalf("noop plan: %#v %v", plan, err)
	}
	if status, err := Status("opencode"); err != nil || !status.Clean || status.Items[0].Kind != "config" {
		t.Fatalf("config status: %#v %v", status, err)
	}
	doctor := Doctor("0.1.0-test")
	foundDoctorConfig := false
	for _, check := range doctor.Checks {
		if check.Name == "config_opencode-config" && check.Status == "ok" {
			foundDoctorConfig = true
		}
	}
	if !foundDoctorConfig {
		t.Fatalf("doctor config check missing: %#v", doctor)
	}
	source := filepath.Join(repo, "config", "opencode.json")
	updated := `{"default_agent":"naru-orchestrator","shell":"zsh"}`
	if err := os.WriteFile(source, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if plan, err := Plan("opencode"); err != nil || actionCount(plan, "update") != 1 {
		t.Fatalf("update plan: %#v %v", plan, err)
	}
	if _, err := Apply("opencode", "test"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(destination); string(data) != updated || fileMode(t, destination) != 0o600 {
		t.Fatal("config update did not preserve private mode and bytes")
	}
	if err := os.WriteFile(destination, []byte(`{"drift":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if plan, err := Plan("opencode"); err != nil || actionCount(plan, "blocked_drift") != 1 {
		t.Fatalf("config drift not blocked: %#v %v", plan, err)
	}
	if err := os.WriteFile(destination, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCatalogWithConfigs(t, repo, nil, nil)
	if plan, err := Plan("opencode"); err != nil || actionCount(plan, "remove") != 1 {
		t.Fatalf("remove plan: %#v %v", plan, err)
	}
	if _, err := Apply("opencode", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created config was not removed: %v", err)
	}
}

func TestConfigAdoptRestoreCollisionAndReceiptValidation(t *testing.T) {
	t.Run("adopt and restore", func(t *testing.T) {
		_, repo := configEnvironment(t, false)
		paths, _ := ResolvePaths()
		destination, _ := configDestination(paths, "opencode-config")
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		original := []byte(testOpenCodeConfig)
		if err := os.WriteFile(destination, original, 0o640); err != nil {
			t.Fatal(err)
		}
		_, _, _ = Enroll(repo, "test", false)
		if plan, err := Plan("opencode"); err != nil || actionCount(plan, "adopt") != 1 {
			t.Fatalf("adopt plan: %#v %v", plan, err)
		}
		if _, err := Apply("opencode", "test"); err != nil {
			t.Fatal(err)
		}
		receipt, _ := LoadReceipt(paths)
		if len(receipt.Configs) != 1 || receipt.Configs[0].Origin != "adopted" || receipt.Configs[0].OriginalMode != 0o640 || fileMode(t, receipt.Configs[0].Backup) != 0o600 {
			t.Fatalf("adopted config receipt/backup invalid: %#v", receipt)
		}
		if err := os.WriteFile(filepath.Join(repo, "config", "opencode.json"), []byte(`{"default_agent":"naru-orchestrator"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = Apply("opencode", "test")
		writeCatalogWithConfigs(t, repo, nil, nil)
		if plan, err := Plan("opencode"); err != nil || actionCount(plan, "restore") != 1 {
			t.Fatalf("restore plan: %#v %v", plan, err)
		}
		if _, err := Apply("opencode", "test"); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(destination); string(data) != string(original) || fileMode(t, destination) != 0o640 {
			t.Fatal("adopted config was not restored exactly")
		}
	})

	t.Run("collision", func(t *testing.T) {
		_, repo := configEnvironment(t, false)
		paths, _ := ResolvePaths()
		destination, _ := configDestination(paths, "opencode-config")
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, []byte(`{"different":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _ = Enroll(repo, "test", false)
		if plan, err := Plan("opencode"); err != nil || actionCount(plan, "blocked_collision") != 1 {
			t.Fatalf("config collision not blocked: %#v %v", plan, err)
		}
	})

	t.Run("receipt destination", func(t *testing.T) {
		_, repo := configEnvironment(t, false)
		_, _, _ = Enroll(repo, "test", false)
		_, _ = Apply("opencode", "test")
		paths, _ := ResolvePaths()
		receipt, _ := LoadReceipt(paths)
		receipt.Configs[0].Destination = filepath.Join(paths.Home, "outside")
		if err := atomicJSON(paths.Receipt, receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadReceipt(paths); err == nil {
			t.Fatal("unsafe config receipt destination accepted")
		}
	})
}

func TestConfigRollbackAndEnrollmentReplacement(t *testing.T) {
	_, repo := configEnvironment(t, true)
	prepareInstructionParents(t)
	_, _, _ = Enroll(repo, "test", false)
	count := 0
	beforeInstructionMutation = func(Action) error {
		count++
		if count == 2 {
			return errors.New("forced managed-file failure")
		}
		return nil
	}
	t.Cleanup(func() { beforeInstructionMutation = nil })
	if _, err := Apply("opencode", "test"); err == nil || !strings.Contains(err.Error(), "forced") {
		t.Fatalf("forced rollback failure missing: %v", err)
	}
	paths, _ := ResolvePaths()
	for _, destination := range []string{filepath.Join(paths.ConfigBase, "opencode", "AGENTS.md"), filepath.Join(paths.ConfigBase, "opencode", "opencode.json")} {
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback left managed file %s: %v", destination, err)
		}
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback wrote receipt")
	}

	beforeInstructionMutation = nil
	if _, err := Apply("opencode", "test"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeCatalogWithConfigs(t, other, nil, nil)
	if _, _, err := Enroll(other, "other", true); err == nil || !strings.Contains(err.Error(), "decommission") {
		t.Fatalf("managed config enrollment replacement accepted: %v", err)
	}
}
