package terran

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func instructionEnvironment(t *testing.T, targets ...string) (string, string) {
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
	instructions := make([]Instruction, 0, len(targets))
	for _, target := range targets {
		source := filepath.ToSlash(filepath.Join("instructions", target+".md"))
		instructions = append(instructions, Instruction{Target: target, Source: source})
	}
	writeCatalogWithInstructions(t, repo, nil, instructions)
	return home, repo
}

func writeCatalogWithInstructions(t *testing.T, repo string, projections []Projection, instructions []Instruction) {
	t.Helper()
	for _, projection := range projections {
		dir := filepath.Join(repo, filepath.FromSlash(projection.Source))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+projection.Skill+"\ndescription: test\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, instruction := range instructions {
		path := filepath.Join(repo, filepath.FromSlash(instruction.Source))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# "+instruction.Target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: "test-catalog", Version: "0.1.0", Projections: projections, Instructions: instructions}
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

func prepareInstructionParents(t *testing.T) {
	t.Helper()
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"claude-global", "opencode-global"} {
		destination, _ := instructionDestination(paths, target)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstructionManifestValidationAndFingerprint(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	first, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := first.Manifest
	manifest.Instructions[0], manifest.Instructions[1] = manifest.Instructions[1], manifest.Instructions[0]
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := LoadManifest(repo)
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatalf("instruction ordering changed fingerprint: %v", err)
	}

	tests := []struct {
		name string
		data string
	}{
		{"unknown field", `{"schema_version":1,"id":"test-catalog","version":"1","projections":[],"instructions":[{"target":"claude-global","source":"instructions/claude-global.md","extra":true}]}`},
		{"unknown target", `{"schema_version":1,"id":"test-catalog","version":"1","projections":[],"instructions":[{"target":"other","source":"instructions/claude-global.md"}]}`},
		{"duplicate", `{"schema_version":1,"id":"test-catalog","version":"1","projections":[],"instructions":[{"target":"claude-global","source":"instructions/claude-global.md"},{"target":"claude-global","source":"instructions/claude-global.md"}]}`},
		{"absolute", `{"schema_version":1,"id":"test-catalog","version":"1","projections":[],"instructions":[{"target":"claude-global","source":"/tmp/file"}]}`},
		{"escape", `{"schema_version":1,"id":"test-catalog","version":"1","projections":[],"instructions":[{"target":"claude-global","source":"../file"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(repo, "terran.json"), []byte(tc.data), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(repo); err == nil {
				t.Fatal("invalid instruction manifest accepted")
			}
		})
	}
}

func TestInstructionSourceSafety(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"symlink", func(t *testing.T, path string) {
			target := path + ".target"
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, path string) {
			if err := os.Link(path, path+".link"); err != nil {
				if errors.Is(err, syscall.EPERM) {
					t.Skipf("hard links unavailable: %v", err)
				}
				t.Fatal(err)
			}
		}},
		{"unsafe mode", func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o666); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversize", func(t *testing.T, path string) {
			if err := os.WriteFile(path, make([]byte, instructionLimit+1), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := instructionEnvironment(t, "claude-global")
			path := filepath.Join(repo, "instructions", "claude-global.md")
			tc.mutate(t, path)
			if _, err := LoadManifest(repo); err == nil {
				t.Fatal("unsafe instruction source accepted")
			}
		})
	}
}

func TestInstructionDestinationsUseXDGAndDefault(t *testing.T) {
	home, repo := instructionEnvironment(t, "opencode-global")
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("opencode")
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Destination != filepath.Join(home, "config", "opencode", "AGENTS.md") {
		t.Fatalf("XDG destination: %#v %v", plan, err)
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
	writeCatalogWithInstructions(t, repo, nil, []Instruction{{Target: "opencode-global", Source: "instructions/opencode.md"}})
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	plan, err = Plan("opencode")
	if err != nil || plan.Actions[0].Destination != filepath.Join(home, ".config", "opencode", "AGENTS.md") {
		t.Fatalf("default destination: %#v %v", plan, err)
	}
}

func TestInstructionAdoptionPreservesFileAndCreatesBackup(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	source := filepath.Join(repo, "instructions", "claude-global.md")
	data, _ := os.ReadFile(source)
	if err := os.WriteFile(destination, data, 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(destination)
	beforeStat := before.Sys().(*syscall.Stat_t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("claude")
	if err != nil || actionCount(plan, "adopt") != 1 {
		t.Fatalf("adoption plan: %#v %v", plan, err)
	}
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(destination)
	afterStat := after.Sys().(*syscall.Stat_t)
	if beforeStat.Ino != afterStat.Ino || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("adoption changed active target inode or mtime")
	}
	backup := instructionBackup(paths, "claude-global")
	backupData, err := os.ReadFile(backup)
	if err != nil || string(backupData) != string(data) || fileMode(t, backup) != 0o600 {
		t.Fatalf("invalid backup: %v", err)
	}
	receipt, err := LoadReceipt(paths)
	if err != nil || len(receipt.Instructions) != 1 || receipt.Instructions[0].Origin != "adopted" || receipt.Instructions[0].OriginalMode != 0o640 {
		t.Fatalf("invalid adoption receipt: %#v %v", receipt, err)
	}
}

func TestInstructionCollisionsBlockAllSelectedActions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, []byte)
	}{
		{"different file", func(t *testing.T, path string, _ []byte) { _ = os.WriteFile(path, []byte("different"), 0o644) }},
		{"symlink", func(t *testing.T, path string, data []byte) {
			target := path + ".target"
			_ = os.WriteFile(target, data, 0o644)
			_ = os.Symlink(target, path)
		}},
		{"hardlink", func(t *testing.T, path string, data []byte) {
			target := path + ".target"
			_ = os.WriteFile(target, data, 0o644)
			if err := os.Link(target, path); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}
		}},
		{"directory", func(t *testing.T, path string, _ []byte) { _ = os.Mkdir(path, 0o755) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, repo := instructionEnvironment(t, "claude-global", "opencode-global")
			prepareInstructionParents(t)
			paths, _ := ResolvePaths()
			blockedDestination, _ := instructionDestination(paths, "claude-global")
			data, _ := os.ReadFile(filepath.Join(repo, "instructions", "claude-global.md"))
			tc.mutate(t, blockedDestination, data)
			if _, _, err := Enroll(repo, "test", false); err != nil {
				t.Fatal(err)
			}
			plan, _ := Plan("all")
			if actionCount(plan, "blocked_collision") != 1 {
				t.Fatalf("collision not blocked: %#v", plan)
			}
			applied, err := Apply("all", "test")
			if err != nil || !blocked(applied) {
				t.Fatalf("blocked apply: %#v %v", applied, err)
			}
			other := filepath.Join(home, "config", "opencode", "AGENTS.md")
			if _, err := os.Lstat(other); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("blocked apply mutated another instruction")
			}
		})
	}
}

func TestInstructionCreateUpdateNoopDriftAndFiltering(t *testing.T) {
	home, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	plan, _ := Plan("opencode")
	if len(plan.Actions) != 1 || actionCount(plan, "create") != 1 {
		t.Fatalf("opencode filter: %#v", plan)
	}
	if _, err := Apply("opencode", "test"); err != nil {
		t.Fatal(err)
	}
	paths, _ := ResolvePaths()
	receipt, _ := LoadReceipt(paths)
	if len(receipt.Instructions) != 1 || receipt.Instructions[0].Target != "opencode-global" {
		t.Fatalf("filtered receipt: %#v", receipt)
	}
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	plan, _ = Plan("all")
	if !plan.Clean || actionCount(plan, "noop") != 2 {
		t.Fatalf("noop plan: %#v", plan)
	}
	source := filepath.Join(repo, "instructions", "claude-global.md")
	if err := os.WriteFile(source, []byte("# changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ = Plan("claude")
	if actionCount(plan, "update") != 1 {
		t.Fatalf("update plan: %#v", plan)
	}
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(home, ".claude", "CLAUDE.md")
	if data, _ := os.ReadFile(destination); string(data) != "# changed\n" {
		t.Fatal("instruction was not updated")
	}
	if err := os.WriteFile(destination, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ = Plan("all")
	if actionCount(plan, "blocked_drift") != 1 {
		t.Fatalf("external drift not blocked: %#v", plan)
	}
	opencode := filepath.Join(home, "config", "opencode", "AGENTS.md")
	before, _ := os.ReadFile(opencode)
	_, _ = Apply("all", "test")
	after, _ := os.ReadFile(opencode)
	if string(before) != string(after) {
		t.Fatal("drifted apply changed another target")
	}
}

func TestAddingInstructionsLeavesExistingSkillsNoop(t *testing.T) {
	home, repo := testEnvironment(t)
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	projections := []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}}
	instructions := []Instruction{{Target: "opencode-global", Source: "instructions/opencode.md"}}
	writeCatalogWithInstructions(t, repo, projections, instructions)
	plan, err := Plan("all")
	if err != nil || actionCount(plan, "noop") != 2 || actionCount(plan, "create") != 1 {
		t.Fatalf("existing skills were not noop after instruction addition: %#v %v (home=%s)", plan, err, home)
	}
}

func TestInstructionRemovalCreatedAndAdopted(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		_, repo := instructionEnvironment(t, "claude-global")
		prepareInstructionParents(t)
		_, _, _ = Enroll(repo, "test", false)
		_, _ = Apply("claude", "test")
		writeCatalogWithInstructions(t, repo, nil, nil)
		plan, _ := Plan("claude")
		if actionCount(plan, "remove") != 1 {
			t.Fatalf("remove plan: %#v", plan)
		}
		paths, _ := ResolvePaths()
		destination, _ := instructionDestination(paths, "claude-global")
		if _, err := Apply("claude", "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created instruction was not removed")
		}
	})

	t.Run("adopted", func(t *testing.T) {
		_, repo := instructionEnvironment(t, "claude-global")
		prepareInstructionParents(t)
		paths, _ := ResolvePaths()
		destination, _ := instructionDestination(paths, "claude-global")
		original := []byte("# claude-global\n")
		_ = os.WriteFile(destination, original, 0o640)
		_, _, _ = Enroll(repo, "test", false)
		_, _ = Apply("claude", "test")
		source := filepath.Join(repo, "instructions", "claude-global.md")
		_ = os.WriteFile(source, []byte("# managed change\n"), 0o644)
		_, _ = Apply("claude", "test")
		writeCatalogWithInstructions(t, repo, nil, nil)
		plan, _ := Plan("claude")
		if actionCount(plan, "restore") != 1 {
			t.Fatalf("restore plan: %#v", plan)
		}
		if _, err := Apply("claude", "test"); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(destination)
		if string(data) != string(original) || fileMode(t, destination) != 0o640 {
			t.Fatal("adopted original was not restored")
		}
		backup := instructionBackup(paths, "claude-global")
		if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restored adoption backup was not removed: %v", err)
		}
		writeCatalogWithInstructions(t, repo, nil, []Instruction{{Target: "claude-global", Source: "instructions/claude-global.md"}})
		if plan, err := Plan("claude"); err != nil || actionCount(plan, "adopt") != 1 {
			t.Fatalf("restored instruction was not adoptable again: %#v %v", plan, err)
		}
		if _, err := Apply("claude", "test"); err != nil {
			t.Fatal(err)
		}
		if err := validateTrustedFile(backup, "instruction backup"); err != nil || fileMode(t, backup) != 0o600 {
			t.Fatalf("reintroduced backup invalid: %v", err)
		}
	})
}

func TestAdoptedRestoreCleanupFailureDoesNotBlockReintroduction(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	source, _ := os.ReadFile(filepath.Join(repo, "instructions", "claude-global.md"))
	_ = os.WriteFile(destination, source, 0o644)
	_, _, _ = Enroll(repo, "test", false)
	_, _ = Apply("claude", "test")
	writeCatalogWithInstructions(t, repo, nil, nil)
	removeInstructionBackup = func(string) error { return errors.New("forced cleanup failure") }
	t.Cleanup(func() { removeInstructionBackup = safelyRemoveInstructionBackup })
	result, err := Apply("claude", "test")
	if err != nil || !strings.Contains(result.Actions[0].Reason, "cleanup warning") {
		t.Fatalf("cleanup warning missing: %#v %v", result, err)
	}
	backup := instructionBackup(paths, "claude-global")
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("forced stale backup missing: %v", err)
	}
	writeCatalogWithInstructions(t, repo, nil, []Instruction{{Target: "claude-global", Source: "instructions/claude-global.md"}})
	plan, err := Plan("claude")
	if err != nil || actionCount(plan, "adopt") != 1 {
		t.Fatalf("safe stale backup blocked adoption: %#v %v", plan, err)
	}
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionRemovalBlocksOnDriftOrBadBackup(t *testing.T) {
	for _, mutation := range []string{"active", "missing backup", "tampered backup", "unsafe backup"} {
		t.Run(mutation, func(t *testing.T) {
			_, repo := instructionEnvironment(t, "claude-global")
			prepareInstructionParents(t)
			paths, _ := ResolvePaths()
			destination, _ := instructionDestination(paths, "claude-global")
			source, _ := os.ReadFile(filepath.Join(repo, "instructions", "claude-global.md"))
			_ = os.WriteFile(destination, source, 0o644)
			_, _, _ = Enroll(repo, "test", false)
			_, _ = Apply("claude", "test")
			backup := instructionBackup(paths, "claude-global")
			switch mutation {
			case "active":
				_ = os.WriteFile(destination, []byte("drift"), 0o644)
			case "missing backup":
				_ = os.Remove(backup)
			case "tampered backup":
				_ = os.WriteFile(backup, []byte("tampered"), 0o600)
			case "unsafe backup":
				_ = os.Chmod(backup, 0o666)
			}
			writeCatalogWithInstructions(t, repo, nil, nil)
			plan, _ := Plan("claude")
			if actionCount(plan, "blocked_drift") != 1 {
				t.Fatalf("unsafe removal accepted: %#v", plan)
			}
		})
	}
}

func TestInstructionReceiptSafetyAndLegacyCompatibility(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	_, _, _ = Enroll(repo, "test", false)
	_, _ = Apply("claude", "test")
	paths, _ := ResolvePaths()
	receipt, _ := LoadReceipt(paths)
	receipt.Instructions[0].Destination = filepath.Join(paths.Home, "outside")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths); err == nil {
		t.Fatal("malicious destination accepted")
	}
	receipt.Instructions[0].Destination, _ = instructionDestination(paths, "claude-global")
	receipt.Instructions[0].Source = filepath.Join(paths.Home, "outside")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths); err == nil {
		t.Fatal("malicious source accepted")
	}
	receipt.Instructions[0].Source = filepath.Join(repo, "instructions", "claude-global.md")
	receipt.Instructions[0].Backup = filepath.Join(paths.Home, "outside")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths); err == nil {
		t.Fatal("malicious backup accepted")
	}

	legacy := `{"schema_version":1,"repository_id":"test-catalog","repository_path":` + string(mustJSON(t, repo)) + `,"repository_version":"0.1.0","manifest_fingerprint":"legacy","projections":[]}`
	if err := os.WriteFile(paths.Receipt, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReceipt(paths)
	if err != nil || loaded.Instructions != nil {
		t.Fatalf("legacy receipt failed: %#v %v", loaded, err)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstructionTransactionRollback(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	prepareInstructionParents(t)
	_, _, _ = Enroll(repo, "test", false)
	count := 0
	beforeInstructionMutation = func(Action) error {
		count++
		if count == 2 {
			return errors.New("forced second instruction failure")
		}
		return nil
	}
	t.Cleanup(func() { beforeInstructionMutation = nil })
	if _, err := Apply("all", "test"); err == nil || !strings.Contains(err.Error(), "forced") {
		t.Fatalf("forced failure missing: %v", err)
	}
	paths, _ := ResolvePaths()
	for _, target := range []string{"claude-global", "opencode-global"} {
		destination, _ := instructionDestination(paths, target)
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback left %s: %v", destination, err)
		}
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback wrote receipt")
	}
}

func TestSkillMutationsRollbackOnInstructionAndReceiptFailures(t *testing.T) {
	failures := []struct {
		name   string
		inject func()
	}{
		{"instruction", func() {
			beforeInstructionMutation = func(Action) error { return errors.New("forced instruction failure") }
		}},
		{"receipt", func() { beforeReceiptWrite = func() error { return errors.New("forced receipt failure") } }},
	}
	for _, action := range []string{"create", "replace", "remove"} {
		for _, failure := range failures {
			t.Run(action+"/"+failure.name, func(t *testing.T) {
				home, repo := instructionEnvironment(t)
				projection := Projection{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}
				instruction := Instruction{Target: "claude-global", Source: "instructions/claude.md"}
				prepareInstructionParents(t)
				if action == "create" {
					writeCatalogWithInstructions(t, repo, []Projection{projection}, []Instruction{instruction})
				} else {
					writeCatalogWithInstructions(t, repo, []Projection{projection}, []Instruction{instruction})
				}
				_, _, _ = Enroll(repo, "test", false)
				paths, _ := ResolvePaths()
				destination := filepath.Join(home, ".agents", "skills", "example")
				if action != "create" {
					if _, err := Apply("all", "test"); err != nil {
						t.Fatal(err)
					}
					if action == "replace" {
						projection.Source = "skills/example-v2"
						writeCatalogWithInstructions(t, repo, []Projection{projection}, []Instruction{instruction})
					} else {
						writeCatalogWithInstructions(t, repo, nil, []Instruction{instruction})
					}
					if err := os.WriteFile(filepath.Join(repo, "instructions", "claude.md"), []byte("# changed\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				beforeReceipt, receiptErr := os.ReadFile(paths.Receipt)
				beforeLink, linkErr := os.Readlink(destination)
				failure.inject()
				t.Cleanup(func() {
					beforeInstructionMutation = nil
					beforeReceiptWrite = nil
				})
				if _, err := Apply("all", "test"); err == nil || !strings.Contains(err.Error(), "forced") {
					t.Fatalf("forced failure missing: %v", err)
				}
				if action == "create" {
					if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("created skill remained: %v", err)
					}
				} else if afterLink, err := os.Readlink(destination); err != nil || afterLink != beforeLink || linkErr != nil {
					t.Fatalf("skill did not return to prior link: before=%q after=%q err=%v", beforeLink, afterLink, err)
				}
				afterReceipt, afterErr := os.ReadFile(paths.Receipt)
				if receiptErr == nil {
					if afterErr != nil || string(afterReceipt) != string(beforeReceipt) {
						t.Fatalf("receipt changed: %v", afterErr)
					}
				} else if !errors.Is(afterErr, os.ErrNotExist) {
					t.Fatalf("receipt unexpectedly exists: %v", afterErr)
				}
			})
		}
	}
}

func TestInstructionPostRenameFailuresRollback(t *testing.T) {
	for _, stage := range []string{"parent sync", "post-write hash"} {
		t.Run(stage, func(t *testing.T) {
			_, repo := instructionEnvironment(t, "claude-global")
			prepareInstructionParents(t)
			_, _, _ = Enroll(repo, "test", false)
			paths, _ := ResolvePaths()
			destination, _ := instructionDestination(paths, "claude-global")
			if stage == "parent sync" {
				afterInstructionRename = func(string) error { return errors.New("forced parent sync failure") }
			} else {
				afterInstructionHash = func(string) error { return errors.New("forced hash verification failure") }
			}
			t.Cleanup(func() { afterInstructionRename, afterInstructionHash = nil, nil })
			if _, err := Apply("claude", "test"); err == nil || !strings.Contains(err.Error(), "forced") {
				t.Fatalf("forced post-rename failure missing: %v", err)
			}
			if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("post-rename failure left destination: %v", err)
			}
			if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("post-rename failure wrote receipt: %v", err)
			}
		})
	}
}

func TestInstructionRestorePostRenameFailureRollsBack(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	source, _ := os.ReadFile(filepath.Join(repo, "instructions", "claude-global.md"))
	_ = os.WriteFile(destination, source, 0o640)
	_, _, _ = Enroll(repo, "test", false)
	_, _ = Apply("claude", "test")
	_ = os.WriteFile(filepath.Join(repo, "instructions", "claude-global.md"), []byte("# managed\n"), 0o644)
	_, _ = Apply("claude", "test")
	managed, _ := os.ReadFile(destination)
	receiptBefore, _ := os.ReadFile(paths.Receipt)
	writeCatalogWithInstructions(t, repo, nil, nil)
	afterInstructionRename = func(string) error { return errors.New("forced restore sync failure") }
	t.Cleanup(func() { afterInstructionRename = nil })
	if _, err := Apply("claude", "test"); err == nil {
		t.Fatal("forced restore failure missing")
	}
	if after, _ := os.ReadFile(destination); string(after) != string(managed) {
		t.Fatal("restore failure did not roll active instruction back")
	}
	if after, _ := os.ReadFile(paths.Receipt); string(after) != string(receiptBefore) {
		t.Fatal("restore failure changed receipt")
	}
}

func TestEnrollReplaceRefusesManagedInstruction(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	_, _, _ = Enroll(repo, "test", false)
	_, _ = Apply("claude", "test")
	other := filepath.Join(t.TempDir(), "other")
	writeCatalogWithInstructions(t, other, nil, nil)
	if _, _, err := Enroll(other, "other", true); err == nil || !strings.Contains(err.Error(), "decommission") {
		t.Fatalf("managed instruction replacement accepted: %v", err)
	}
}

func TestInstructionSourceRaceRollsBackAllSelectedMutations(t *testing.T) {
	home, repo := instructionEnvironment(t, "claude-global")
	projection := Projection{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}
	instruction := Instruction{Target: "claude-global", Source: "instructions/claude-global.md"}
	writeCatalogWithInstructions(t, repo, []Projection{projection}, []Instruction{instruction})
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	beforeInstructionMutation = func(Action) error {
		return os.WriteFile(filepath.Join(repo, "instructions", "claude-global.md"), []byte("# raced\n"), 0o644)
	}
	t.Cleanup(func() { beforeInstructionMutation = nil })
	if _, err := Apply("all", "test"); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("source race was not rejected: %v", err)
	}
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	for _, path := range []string{destination, filepath.Join(home, ".agents", "skills", "example"), paths.Receipt} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source race left mutation at %s: %v", path, err)
		}
	}
}

func TestPostRenameReceiptSyncFailureCommitsVerifiedReceipt(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	_, _, _ = Enroll(repo, "test", false)
	afterReceiptRename = func() error { return errors.New("forced receipt directory sync failure") }
	t.Cleanup(func() { afterReceiptRename = nil })
	result, err := Apply("claude", "test")
	if err != nil {
		t.Fatalf("verified renamed receipt was treated as uncommitted: %v", err)
	}
	if len(result.Actions) != 1 || !strings.Contains(result.Actions[0].Reason, "durability sync failed") {
		t.Fatalf("durability limitation was not reported: %#v", result)
	}
	paths, _ := ResolvePaths()
	if _, err := LoadReceipt(paths); err != nil {
		t.Fatalf("committed receipt is invalid: %v", err)
	}
	plan, err := Plan("claude")
	if err != nil || !plan.Clean {
		t.Fatalf("committed state is inconsistent: %#v %v", plan, err)
	}
}

func TestReceiptRestorationFailureIsReportedAndKeepsOwnershipBytes(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	_, _, _ = Enroll(repo, "test", false)
	paths, _ := ResolvePaths()
	afterReceiptRename = func() error {
		if err := os.Chmod(paths.Receipt, 0o666); err != nil {
			return err
		}
		return errors.New("forced post-rename failure")
	}
	restoreReceiptFile = func(string, []byte, bool) error { return errors.New("forced receipt restoration failure") }
	t.Cleanup(func() {
		afterReceiptRename = nil
		restoreReceiptFile = restoreReceiptSnapshot
	})
	if _, err := Apply("claude", "test"); err == nil || !strings.Contains(err.Error(), "forced post-rename failure") || !strings.Contains(err.Error(), "forced receipt restoration failure") {
		t.Fatalf("receipt/restoration failures were not aggregated: %v", err)
	}
	data, err := os.ReadFile(paths.Receipt)
	if err != nil || !bytes.Contains(data, []byte(`"claude-global"`)) {
		t.Fatalf("ownership bytes were lost after restoration failure: %v %q", err, data)
	}
	destination, _ := instructionDestination(paths, "claude-global")
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("filesystem mutation was incorrectly rolled back against retained receipt: %v", err)
	}
	if Doctor("test").Healthy {
		t.Fatal("doctor did not report unsafe retained receipt")
	}
}
