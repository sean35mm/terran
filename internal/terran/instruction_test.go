package terran

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	receipt, err := LoadReceipt(paths, Enrollment{})
	if err != nil || len(receipt.Managed) != 1 || receipt.Managed[0].Kind != "instruction" || receipt.Managed[0].Origin != "adopted" || receipt.Managed[0].OriginalMode != 0o640 {
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
			if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	receipt, _ := LoadReceipt(paths, Enrollment{})
	if len(receipt.Managed) != 1 || receipt.Managed[0].Kind != "instruction" || receipt.Managed[0].Target != "opencode-global" {
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

	// Syncing the local edit into the catalog records it without rewriting the file.
	if err := os.WriteFile(source, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ = Plan("claude")
	if action := actionByIDOrFail(t, plan, "instruction/claude-global"); action.Action != "update" || action.Reason != matchesCatalogReason {
		t.Fatalf("synced edit not recorded: %#v", action)
	}
	edited, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	if current, err := os.Stat(destination); err != nil || !os.SameFile(current, edited) {
		t.Fatal("recording a synced edit rewrote the destination")
	}
	if plan, _ = Plan("claude"); !plan.Clean {
		t.Fatalf("plan after recording synced edit: %#v", plan)
	}
}

func TestAddingInstructionsLeavesExistingSkillsNoop(t *testing.T) {
	home, repo := testEnvironment(t)
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
		_, _, _ = Enroll(repo, "test", "", false)
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
		_, _, _ = Enroll(repo, "test", "", false)
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
	_, _, _ = Enroll(repo, "test", "", false)
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
			_, _, _ = Enroll(repo, "test", "", false)
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
	_, _, _ = Enroll(repo, "test", "", false)
	_, _ = Apply("claude", "test")
	paths, _ := ResolvePaths()
	receipt, _ := LoadReceipt(paths, Enrollment{})
	receipt.Managed[0].Destination = filepath.Join(paths.Home, "outside")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths, Enrollment{}); err == nil {
		t.Fatal("malicious destination accepted")
	}
	receipt.Managed[0].Destination, _ = instructionDestination(paths, "claude-global")
	receipt.Managed[0].Source = filepath.Join(paths.Home, "outside")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths, Enrollment{}); err == nil {
		t.Fatal("malicious source accepted")
	}
	receipt.Managed[0].Source = filepath.Join(repo, "instructions", "claude-global.md")
	receipt.Managed[0].Backup = filepath.Join(paths.Home, "outside")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths, Enrollment{}); err == nil {
		t.Fatal("malicious backup accepted")
	}

	legacy := `{"schema_version":1,"repository_id":"test-catalog","repository_path":` + string(mustJSON(t, repo)) + `,"repository_version":"0.1.0","manifest_fingerprint":"legacy","projections":[]}`
	if err := os.WriteFile(paths.Receipt, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReceipt(paths, Enrollment{})
	if err != nil || loaded.Managed != nil {
		t.Fatalf("legacy receipt failed: %#v %v", loaded, err)
	}
}

func TestV03StateUpgradeDoesNotRewriteManagedDestinations(t *testing.T) {
	home, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	projections := []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}}
	instructions := []Instruction{
		{Target: "claude-global", Source: "instructions/claude-global.md"},
		{Target: "opencode-global", Source: "instructions/opencode-global.md"},
	}
	writeCatalogWithInstructions(t, repo, projections, instructions)
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "v0.3", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "0.3.0"); err != nil {
		t.Fatal(err)
	}
	// v0.3 projected skills as live symlinks.
	receipt := writeLegacySkillState(t)

	paths, _ := ResolvePaths()
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		t.Fatal(err)
	}
	v1Manifest := manifestV1{
		SchemaVersion: 1,
		ID:            "test-catalog",
		Version:       "0.1.0",
		Projections:   []projectionV1{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}},
		Instructions: []instructionV1{
			{Target: "claude-global", Source: "instructions/claude-global.md"},
			{Target: "opencode-global", Source: "instructions/opencode-global.md"},
		},
	}
	v1Enrollment := enrollmentV1{
		SchemaVersion:   1,
		RepositoryID:    enrollment.RepositoryID,
		RepositoryPath:  enrollment.RepositoryPath,
		CommandCenterID: enrollment.CommandCenterID,
		DisplayName:     enrollment.DisplayName,
	}
	v1NormalizedManifest, _ := json.Marshal(v1Manifest)
	v1Receipt := receiptV1{
		SchemaVersion:       1,
		RepositoryID:        receipt.RepositoryID,
		RepositoryPath:      receipt.RepositoryPath,
		RepositoryVersion:   receipt.RepositoryVersion,
		ManifestFingerprint: hashBytes(v1NormalizedManifest),
	}
	for _, projection := range receipt.Projections {
		v1Receipt.Projections = append(v1Receipt.Projections, receiptProjectionV1{
			Skill: projection.Skill, Target: projection.Target, Source: projection.Source, Destination: projection.Destination,
			Strategy: projection.Strategy, AppliedAt: projection.AppliedAt, TerranBuildVersion: projection.TerranBuildVersion,
		})
	}
	for _, managed := range receipt.Managed {
		entry := receiptInstructionV1{
			Target: managed.Target, Source: managed.Source, Destination: managed.Destination, Strategy: managed.Strategy,
			SourceHash: managed.SourceHash, AppliedHash: managed.AppliedHash, Origin: managed.Origin, OriginalHash: managed.OriginalHash,
			OriginalMode: managed.OriginalMode, Backup: managed.Backup, AppliedAt: managed.AppliedAt, TerranBuildVersion: managed.TerranBuildVersion,
		}
		if managed.Kind == "instruction" {
			v1Receipt.Instructions = append(v1Receipt.Instructions, entry)
		}
	}

	manifestBytes, _ := marshalJSON(v1Manifest)
	enrollmentBytes, _ := marshalJSON(v1Enrollment)
	receiptBytes, _ := marshalJSON(v1Receipt)
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, enrollmentBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Receipt, receiptBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	skills := []string{
		filepath.Join(home, ".agents", "skills", "example"),
		filepath.Join(home, ".claude", "skills", "example"),
	}
	destinations := []string{
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, "config", "opencode", "AGENTS.md"),
	}
	before := make(map[string]managedDestinationSnapshot, len(destinations))
	for _, destination := range destinations {
		before[destination] = snapshotManagedDestination(t, destination)
	}

	loaded, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint == v1Receipt.ManifestFingerprint {
		t.Fatal("test fixture did not exercise a changed normalized v2 fingerprint")
	}
	// Instruction files stay noop; the one-time skill conversion is the only change.
	plan, err := Plan("all")
	if err != nil || len(plan.Actions) != len(destinations)+len(skills) || actionCount(plan, "noop") != len(destinations) {
		t.Fatalf("v0.3 instruction plan was not noop: %#v %v", plan, err)
	}
	for _, action := range plan.Actions {
		if action.Kind == "skill" && (action.Action != "update" || action.Reason != "convert live symlink to managed copy") {
			t.Fatalf("v0.3 skill was not planned for conversion: %#v", action)
		}
	}
	if _, err := Apply("all", "0.4.0"); err != nil {
		t.Fatal(err)
	}
	for _, destination := range destinations {
		assertManagedDestinationUnchanged(t, destination, before[destination], snapshotManagedDestination(t, destination))
	}
	for _, destination := range skills {
		if !skillCopied(destination, loaded.Sources["example"]) {
			t.Fatalf("legacy link was not converted to an identical copy: %s", destination)
		}
	}

	afterManifest, _ := os.ReadFile(filepath.Join(repo, "terran.json"))
	afterEnrollment, _ := os.ReadFile(paths.ConfigFile)
	afterReceipt, _ := os.ReadFile(paths.Receipt)
	if !bytes.Equal(afterManifest, manifestBytes) || !bytes.Equal(afterEnrollment, enrollmentBytes) {
		t.Fatal("apply rewrote the v1 manifest or enrollment")
	}
	if bytes.Equal(afterReceipt, receiptBytes) {
		t.Fatal("apply did not upgrade receipt.json")
	}
	upgraded, err := LoadReceipt(paths, Enrollment{})
	if err != nil || upgraded.SchemaVersion != SchemaVersion || len(upgraded.Projections) != 2 || len(upgraded.Managed) != 2 {
		t.Fatalf("upgraded receipt invalid: %#v %v", upgraded, err)
	}
	for _, projection := range upgraded.Projections {
		if projection.Catalog != upgraded.RepositoryID || projection.Strategy != "copy" {
			t.Fatalf("projection catalog not set: %#v", projection)
		}
	}
	for _, managed := range upgraded.Managed {
		if managed.Kind != "instruction" || managed.Catalog != upgraded.RepositoryID {
			t.Fatalf("managed catalog not set: %#v", managed)
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(afterReceipt, &raw); err != nil || raw["schema_version"] == nil || raw["managed"] == nil || raw["instructions"] != nil || raw["configs"] != nil {
		t.Fatalf("receipt did not use the v2 wire shape: keys=%v err=%v", raw, err)
	}
}

type managedDestinationSnapshot struct {
	info os.FileInfo
	link string
	hash string
}

func snapshotManagedDestination(t *testing.T, path string) managedDestinationSnapshot {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := managedDestinationSnapshot{info: info}
	if info.Mode()&os.ModeSymlink != 0 {
		snapshot.link, err = os.Readlink(path)
	} else {
		snapshot.hash, err = fileHash(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertManagedDestinationUnchanged(t *testing.T, path string, before, after managedDestinationSnapshot) {
	t.Helper()
	if !os.SameFile(before.info, after.info) || before.info.Mode() != after.info.Mode() || before.info.Size() != after.info.Size() || !before.info.ModTime().Equal(after.info.ModTime()) || before.link != after.link || before.hash != after.hash {
		t.Fatalf("managed destination changed during schema upgrade: %s", path)
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
	_, _, _ = Enroll(repo, "test", "", false)
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
	for _, action := range []string{"create", "update", "remove"} {
		for _, failure := range failures {
			t.Run(action+"/"+failure.name, func(t *testing.T) {
				home, repo := instructionEnvironment(t)
				projection := Projection{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}
				instruction := Instruction{Target: "claude-global", Source: "instructions/claude.md"}
				prepareInstructionParents(t)
				writeCatalogWithInstructions(t, repo, []Projection{projection}, []Instruction{instruction})
				_, _, _ = Enroll(repo, "test", "", false)
				paths, _ := ResolvePaths()
				destination := filepath.Join(home, ".agents", "skills", "example")
				if action != "create" {
					if _, err := Apply("all", "test"); err != nil {
						t.Fatal(err)
					}
					if action == "update" {
						if err := os.WriteFile(filepath.Join(repo, "skills", "example", "SKILL.md"), []byte("---\nname: example\ndescription: v2\n---\n"), 0o644); err != nil {
							t.Fatal(err)
						}
					} else {
						writeCatalogWithInstructions(t, repo, nil, []Instruction{instruction})
					}
					if err := os.WriteFile(filepath.Join(repo, "instructions", "claude.md"), []byte("# changed\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if plan, _ := Plan("all"); actionByID(plan, "skill/agents/example").Action != action {
					t.Fatalf("plan: %#v", plan)
				}
				beforeReceipt, receiptErr := os.ReadFile(paths.Receipt)
				beforeHash, hashErr := skillTreeHash(destination)
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
				} else if afterHash, err := skillTreeHash(destination); err != nil || afterHash != beforeHash || hashErr != nil {
					t.Fatalf("skill did not return to its prior copy: %v %v", err, hashErr)
				}
				assertNoSkillTemporaries(t, home)
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
			_, _, _ = Enroll(repo, "test", "", false)
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
	_, _, _ = Enroll(repo, "test", "", false)
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
	_, _, _ = Enroll(repo, "test", "", false)
	_, _ = Apply("claude", "test")
	other := filepath.Join(t.TempDir(), "other")
	writeCatalogWithInstructions(t, other, nil, nil)
	if _, _, err := Enroll(other, "other", "", true); err == nil || !strings.Contains(err.Error(), "decommission") {
		t.Fatalf("managed instruction replacement accepted: %v", err)
	}
}

func TestInstructionSourceRaceRollsBackAllSelectedMutations(t *testing.T) {
	home, repo := instructionEnvironment(t, "claude-global")
	projection := Projection{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}
	instruction := Instruction{Target: "claude-global", Source: "instructions/claude-global.md"}
	writeCatalogWithInstructions(t, repo, []Projection{projection}, []Instruction{instruction})
	prepareInstructionParents(t)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	_, _, _ = Enroll(repo, "test", "", false)
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
	if _, err := LoadReceipt(paths, Enrollment{}); err != nil {
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
	_, _, _ = Enroll(repo, "test", "", false)
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

func TestHoldPersistsAndIsIdempotent(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	if _, err := Hold("instruction/claude-global"); err == nil {
		t.Fatal("hold succeeded before enrollment")
	}
	_, _, _ = Enroll(repo, "test", "", false)
	for _, id := range []string{"instruction/nope", "skill/agents/missing"} {
		if _, err := Hold(id); err == nil {
			t.Fatalf("unknown id %q held", id)
		} else if code, next := ErrorCode(err); code != CodeUnknownItem || next != "run terran plan --json to list item ids" {
			t.Fatalf("unknown id code=%q next=%q", code, next)
		}
	}
	for _, id := range []string{"instruction/opencode-global", "instruction/claude-global", "instruction/claude-global"} {
		if _, err := Hold(id); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := ResolvePaths()
	enrollment, err := LoadEnrollment(paths)
	if err != nil || !reflect.DeepEqual(enrollment.Holds, []string{"instruction/claude-global", "instruction/opencode-global"}) {
		t.Fatalf("holds: %#v %v", enrollment.Holds, err)
	}
	for _, id := range []string{"instruction/claude-global", "instruction/claude-global", "skill/agents/never-held"} {
		if _, err := Unhold(id); err != nil {
			t.Fatal(err)
		}
	}
	if enrollment, err = LoadEnrollment(paths); err != nil || !reflect.DeepEqual(enrollment.Holds, []string{"instruction/opencode-global"}) {
		t.Fatalf("holds after unhold: %#v %v", enrollment.Holds, err)
	}
}

func TestHeldDriftedInstructionDoesNotBlockAndSkillHoldIsNotProjected(t *testing.T) {
	home, repo := instructionEnvironment(t, "claude-global")
	writeCatalogWithInstructions(t, repo, []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}}, []Instruction{{Target: "claude-global", Source: "instructions/claude-global.md"}})
	prepareInstructionParents(t)
	_, _, _ = Enroll(repo, "test", "", false)
	if _, err := Hold("skill/agents/example"); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".agents", "skills", "example")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("held skill was projected")
	}
	paths, _ := ResolvePaths()
	receipt, err := LoadReceipt(paths, Enrollment{})
	if err != nil || len(receipt.Projections) != 1 || receipt.Projections[0].Target != "claude" {
		t.Fatalf("receipt: %#v %v", receipt, err)
	}
	destination, _ := instructionDestination(paths, "claude-global")
	_ = os.WriteFile(destination, []byte("drifted\n"), 0o644)
	if plan, _ := Plan("all"); actionCount(plan, "blocked_drift") != 1 {
		t.Fatalf("drift not detected: %#v", plan)
	}
	if _, err := Hold("instruction/claude-global"); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("all")
	if err != nil || blocked(plan) || actionCount(plan, "held") != 2 {
		t.Fatalf("held plan: %#v %v", plan, err)
	}
	status, err := Status("all")
	if err != nil || !status.Clean {
		t.Fatalf("held status: %#v %v", status, err)
	}
	for _, check := range Doctor("test").Checks {
		if check.Name == "holds" && check.Status != "info" || check.Name == "instruction_receipt" && check.Status == "fail" {
			t.Fatalf("doctor check: %#v", check)
		}
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(destination); string(data) != "drifted\n" {
		t.Fatal("held instruction was modified")
	}
}

func TestHeldOwnedInstructionRemovedFromCatalogStaysUntouched(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	original := []byte("# claude-global\n")
	_ = os.WriteFile(destination, original, 0o640)
	_, _, _ = Enroll(repo, "test", "", false)
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, "instructions", "claude-global.md"), []byte("# managed change\n"), 0o644)
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Hold("instruction/claude-global"); err != nil {
		t.Fatal(err)
	}
	writeCatalogWithInstructions(t, repo, nil, nil)
	backup := instructionBackup(paths, "claude-global")
	before := snapshotManagedDestination(t, destination)
	backupBefore := snapshotManagedDestination(t, backup)
	receiptBefore, err := LoadReceipt(paths, Enrollment{})
	if err != nil || len(receiptBefore.Managed) != 1 || receiptBefore.Managed[0].Origin != "adopted" {
		t.Fatalf("receipt: %#v %v", receiptBefore, err)
	}
	plan, err := Plan("all")
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Action != "held" || blocked(plan) {
		t.Fatalf("held removal plan: %#v %v", plan, err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	assertManagedDestinationUnchanged(t, destination, before, snapshotManagedDestination(t, destination))
	assertManagedDestinationUnchanged(t, backup, backupBefore, snapshotManagedDestination(t, backup))
	receiptAfter, err := LoadReceipt(paths, Enrollment{})
	if err != nil || !reflect.DeepEqual(receiptBefore.Managed, receiptAfter.Managed) {
		t.Fatalf("receipt entry changed: %#v %v", receiptAfter.Managed, err)
	}
	if _, err := Unhold("instruction/claude-global"); err != nil {
		t.Fatal(err)
	}
	if plan, _ := Plan("all"); actionCount(plan, "restore") != 1 {
		t.Fatalf("unheld plan: %#v", plan)
	}
}

func fileEnvironment(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("CODEX_HOME", "")
	return home, filepath.Join(base, "repo")
}

// writeCatalogWithFiles writes a catalog whose instruction and file sources
// contain "# <target or name>\n".
func writeCatalogWithFiles(t *testing.T, repo, id string, instructions []Instruction, files []FileItem) {
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
	for _, file := range files {
		write(file.Source, "# "+file.Name+"\n")
	}
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: id, Version: "0.1.0", Projections: []Projection{}, Instructions: instructions, Files: files}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func actionByIDOrFail(t *testing.T, plan PlanResult, id string) Action {
	t.Helper()
	action := actionByID(plan, id)
	if action.ID == "" {
		t.Fatalf("no action %s in %#v", id, plan)
	}
	return action
}

func TestCodexHomeAndMiseDestinations(t *testing.T) {
	home, _ := fileEnvironment(t)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"codex-global": filepath.Join(home, ".codex", "AGENTS.md"),
		"mise-config":  filepath.Join(home, "config", "mise", "config.toml"),
		"mise-lock":    filepath.Join(home, "config", "mise", "mise.lock"),
	}
	for target, destination := range want {
		kind := "config"
		if target == "codex-global" {
			kind = "instruction"
		}
		if got, err := managedFileDestination(paths, kind, target, ""); err != nil || got != destination {
			t.Fatalf("%s destination %q %v, want %q", target, got, err, destination)
		}
	}
	codexHome := filepath.Join(home, "elsewhere", "codex")
	t.Setenv("CODEX_HOME", codexHome+"/")
	paths, err = ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := instructionDestination(paths, "codex-global"); got != filepath.Join(codexHome, "AGENTS.md") {
		t.Fatalf("CODEX_HOME destination %q", got)
	}
	t.Setenv("CODEX_HOME", "relative/codex")
	if _, err := ResolvePaths(); err == nil {
		t.Fatal("relative CODEX_HOME accepted")
	}
}

func TestDuplicateDestinationsAndChangedCodexHome(t *testing.T) {
	instructions := []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}, {Target: "codex-global", Source: "instructions/codex.md"}, {Target: "opencode-global", Source: "instructions/opencode.md"}}
	t.Run("duplicate destination", func(t *testing.T) {
		home, repo := fileEnvironment(t)
		t.Setenv("CODEX_HOME", filepath.Join(home, "config", "opencode"))
		writeCatalogWithFiles(t, repo, "test-catalog", instructions, nil)
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		if _, err := Plan("all"); !hasCode(err, CodeManifestInvalid) || !strings.Contains(err.Error(), "both resolve to") {
			t.Fatalf("duplicate destination planned: %v", err)
		}
	})
	t.Run("CODEX_HOME changed", func(t *testing.T) {
		home, repo := fileEnvironment(t)
		t.Setenv("CODEX_HOME", filepath.Join(home, "codex-a"))
		writeCatalogWithFiles(t, repo, "test-catalog", instructions[:2], nil)
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		if _, err := Apply("all", "test"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CODEX_HOME", filepath.Join(home, "codex-b"))
		plan, err := Plan("all")
		if err != nil {
			t.Fatalf("changed CODEX_HOME failed the whole plan: %v", err)
		}
		if action := actionByIDOrFail(t, plan, "instruction/codex-global"); action.Action != "blocked_drift" || action.Reason != "CODEX_HOME changed" {
			t.Fatalf("codex action %#v", action)
		}
		if action := actionByIDOrFail(t, plan, "instruction/claude-global"); action.Action != "noop" {
			t.Fatalf("unrelated item affected: %#v", action)
		}
	})
}

func TestSymlinkedDestinationAncestorBlocksManagedFile(t *testing.T) {
	home, repo := fileEnvironment(t)
	writeCatalogWithFiles(t, repo, "test-catalog", nil, []FileItem{{Target: "opencode-plugin", Name: "p.js", Source: "files/p.js"}})
	elsewhere := filepath.Join(filepath.Dir(home), "elsewhere")
	if err := os.MkdirAll(filepath.Join(elsewhere, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "config", "opencode")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	id := "file/opencode-plugin/p.js"
	if action := actionByIDOrFail(t, mustPlan(t, "all"), id); action.Action != "blocked_collision" || action.Reason != "destination path contains a symlink" {
		t.Fatalf("symlinked ancestor action %#v", action)
	}
	result, err := ApplyWithOptions("all", "test", ApplyOptions{Decisions: map[string]CollisionDecision{id: CollisionReplace}})
	if err != nil || !blocked(result) || actionByIDOrFail(t, result, id).Reason != "replace not possible: destination path contains a symlink" {
		t.Fatalf("apply: %#v %v", result, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(elsewhere, "plugins")); len(entries) != 0 {
		t.Fatalf("wrote through symlinked ancestor: %v", entries)
	}
}

func TestCodexGlobalCreateAdoptDriftAndRestore(t *testing.T) {
	codex := []Instruction{{Target: "codex-global", Source: "instructions/codex.md"}}
	t.Run("created", func(t *testing.T) {
		home, repo := fileEnvironment(t)
		codexHome := filepath.Join(home, "codex home")
		t.Setenv("CODEX_HOME", codexHome)
		writeCatalogWithFiles(t, repo, "test-catalog", codex, nil)
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		if plan, err := Plan("codex"); err != nil || len(plan.Actions) != 1 || actionCount(plan, "create") != 1 {
			t.Fatalf("create plan: %#v %v", plan, err)
		}
		if _, err := Apply("codex", "test"); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(codexHome, "AGENTS.md")
		if data, _ := os.ReadFile(destination); string(data) != "# codex-global\n" || fileMode(t, destination) != 0o644 {
			t.Fatalf("codex instruction not created: %q", data)
		}
		if err := os.WriteFile(destination, []byte("external edit"), 0o644); err != nil {
			t.Fatal(err)
		}
		if plan, _ := Plan("all"); actionCount(plan, "blocked_drift") != 1 {
			t.Fatalf("drift not blocked: %#v", plan)
		}
	})
	t.Run("adopted", func(t *testing.T) {
		home, repo := fileEnvironment(t)
		writeCatalogWithFiles(t, repo, "test-catalog", codex, nil)
		destination := filepath.Join(home, ".codex", "AGENTS.md")
		original := []byte("# codex-global\n")
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, original, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		if plan, err := Plan("codex"); err != nil || actionCount(plan, "adopt") != 1 {
			t.Fatalf("adopt plan: %#v %v", plan, err)
		}
		if _, err := Apply("codex", "test"); err != nil {
			t.Fatal(err)
		}
		paths, _ := ResolvePaths()
		if data, err := os.ReadFile(instructionBackup(paths, "codex-global")); err != nil || !bytes.Equal(data, original) {
			t.Fatalf("adoption backup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repo, "instructions", "codex.md"), []byte("# managed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Apply("codex", "test"); err != nil {
			t.Fatal(err)
		}
		writeCatalogWithFiles(t, repo, "test-catalog", nil, nil)
		if plan, err := Plan("codex"); err != nil || actionCount(plan, "restore") != 1 {
			t.Fatalf("restore plan: %#v %v", plan, err)
		}
		if _, err := Apply("codex", "test"); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(destination); !bytes.Equal(data, original) || fileMode(t, destination) != 0o640 {
			t.Fatalf("adopted codex instruction not restored: %q", data)
		}
	})
}

func TestNamedFileNamesRejectedAtManifestLoad(t *testing.T) {
	for _, name := range []string{"../x", "a/b", "Agent.md", "x.txt", "a..md", ".md"} {
		t.Run(name, func(t *testing.T) {
			_, repo := fileEnvironment(t)
			writeCatalogWithFiles(t, repo, "test-catalog", nil, []FileItem{{Target: "claude-agent", Name: "ok.md", Source: "files/agent.md"}})
			data, _ := os.ReadFile(filepath.Join(repo, "terran.json"))
			data = bytes.Replace(data, []byte(`"ok.md"`), []byte(`"`+name+`"`), 1)
			if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(repo); err == nil {
				t.Fatalf("file name %q accepted", name)
			}
		})
	}
	_, repo := fileEnvironment(t)
	writeCatalogWithFiles(t, repo, "test-catalog", nil, []FileItem{{Target: "opencode-tool", Name: "x.js", Source: "files/x.js"}})
	if _, err := LoadManifest(repo); err == nil {
		t.Fatal("opencode-tool accepted a .js name")
	}
	writeCatalogWithFiles(t, repo, "test-catalog", nil, []FileItem{{Target: "claude-agent", Name: "a.md", Source: "files/a.md"}, {Target: "claude-agent", Name: "a.md", Source: "files/b.md"}})
	if _, err := LoadManifest(repo); err == nil {
		t.Fatal("duplicate file accepted")
	}
}

func TestNamedFilesModesIndependenceAndReceipt(t *testing.T) {
	home, repo := fileEnvironment(t)
	files := []FileItem{
		{Target: "claude-agent", Name: "a.md", Source: "files/a.md"},
		{Target: "claude-agent", Name: "b.md", Source: "files/b.md"},
		{Target: "claude-hook", Name: "pre-tool.sh", Source: "files/pre-tool.sh"},
		{Target: "opencode-plugin", Name: "p.ts", Source: "files/p.ts"},
	}
	writeCatalogWithFiles(t, repo, "test-catalog", nil, files)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("all")
	if err != nil || actionCount(plan, "create") != 4 {
		t.Fatalf("create plan: %#v %v", plan, err)
	}
	if action := actionByIDOrFail(t, plan, "file/claude-agent/a.md"); action.Name != "a.md" || action.Destination != filepath.Join(home, ".claude", "agents", "a.md") {
		t.Fatalf("file action: %#v", action)
	}
	if plan, _ := Plan("opencode"); len(plan.Actions) != 1 || plan.Actions[0].Target != "opencode-plugin" {
		t.Fatalf("opencode filter: %#v", plan)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	agentA := filepath.Join(home, ".claude", "agents", "a.md")
	agentB := filepath.Join(home, ".claude", "agents", "b.md")
	hook := filepath.Join(home, ".claude", "hooks", "pre-tool.sh")
	plugin := filepath.Join(home, "config", "opencode", "plugins", "p.ts")
	for path, mode := range map[string]os.FileMode{agentA: 0o644, agentB: 0o644, hook: 0o755, plugin: 0o644} {
		if got := fileMode(t, path); got != mode {
			t.Fatalf("%s mode %04o, want %04o", path, got, mode)
		}
	}
	paths, _ := ResolvePaths()
	receipt, err := LoadReceipt(paths, Enrollment{})
	if err != nil || len(receipt.Managed) != 4 {
		t.Fatalf("receipt: %#v %v", receipt, err)
	}
	for _, managed := range receipt.Managed {
		if managed.Kind != "file" || managed.Name == "" || managed.Catalog != "test-catalog" || managed.Origin != "created" {
			t.Fatalf("file receipt entry: %#v", managed)
		}
	}
	if plan, _ := Plan("all"); !plan.Clean || actionCount(plan, "noop") != 4 {
		t.Fatalf("noop plan: %#v", plan)
	}
	if err := os.WriteFile(filepath.Join(repo, "files", "a.md"), []byte("# a changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentB, []byte("external edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, _ = Plan("all")
	if actionByIDOrFail(t, plan, "file/claude-agent/a.md").Action != "update" || actionByIDOrFail(t, plan, "file/claude-agent/b.md").Action != "blocked_drift" || actionByIDOrFail(t, plan, "file/claude-hook/pre-tool.sh").Action != "blocked_drift" || actionByIDOrFail(t, plan, "file/opencode-plugin/p.ts").Action != "noop" {
		t.Fatalf("independent file actions: %#v", plan)
	}
	if err := os.WriteFile(agentB, []byte("# b.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(agentA); string(data) != "# a changed\n" {
		t.Fatalf("a.md not updated: %q", data)
	}
	if doctor := Doctor("0.1.0"); !doctorCheck(doctor, "file_receipt", "ok") || !doctorCheck(doctor, "file_claude-hook/pre-tool.sh", "ok") {
		t.Fatalf("doctor: %#v", doctor)
	}
	writeCatalogWithFiles(t, repo, "test-catalog", nil, files[1:])
	plan, _ = Plan("all")
	if actionByIDOrFail(t, plan, "file/claude-agent/a.md").Action != "remove" || actionCount(plan, "noop") != 3 {
		t.Fatalf("removal plan: %#v", plan)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(agentA); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created file was not removed")
	}
	if _, err := os.Lstat(agentB); err != nil {
		t.Fatal("sibling file was removed")
	}
}

func doctorCheck(result DoctorResult, name, status string) bool {
	for _, check := range result.Checks {
		if check.Name == name {
			return check.Status == status
		}
	}
	return false
}

func TestNamedFileCollisionAdoptRestoreAndOverlay(t *testing.T) {
	home, repo := fileEnvironment(t)
	writeCatalogWithFiles(t, repo, "test-catalog", nil, []FileItem{{Target: "claude-command", Name: "c.md", Source: "files/c.md"}, {Target: "claude-hook", Name: "h", Source: "files/h"}, {Target: "claude-agent", Name: "a.md", Source: "files/a.md"}})
	overlay := filepath.Join(filepath.Dir(repo), "overlay")
	writeCatalogWithFiles(t, overlay, "private", nil, []FileItem{{Target: "opencode-command", Name: "o.md", Source: "files/o.md"}})
	overlay, _ = filepath.EvalSymlinks(overlay)
	command := filepath.Join(home, ".claude", "commands", "c.md")
	hook := filepath.Join(home, ".claude", "hooks", "h")
	agent := filepath.Join(home, ".claude", "agents", "a.md")
	for path, content := range map[string]string{command: "unowned", hook: "# h\n", agent: "# a.md\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	enrollment, _, err := Enroll(repo, "test", overlay, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("all")
	if err != nil || actionByIDOrFail(t, plan, "file/claude-command/c.md").Action != "blocked_collision" || actionByIDOrFail(t, plan, "file/claude-hook/h").Action != "blocked_collision" || actionByIDOrFail(t, plan, "file/claude-agent/a.md").Action != "adopt" || actionByIDOrFail(t, plan, "file/opencode-command/o.md").Catalog != "private" {
		t.Fatalf("collision plan: %#v %v", plan, err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(command); string(data) != "unowned" {
		t.Fatal("blocked apply changed an unowned file")
	}
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	paths, _ := ResolvePaths()
	backup := filepath.Join(paths.BackupDir, "file", "claude-agent", "a.md", "original")
	if data, err := os.ReadFile(backup); err != nil || string(data) != "# a.md\n" || fileMode(t, backup) != 0o600 {
		t.Fatalf("per-item backup: %v", err)
	}
	receipt, err := LoadReceipt(paths, enrollment)
	if err != nil || len(receipt.Managed) != 4 {
		t.Fatalf("receipt: %#v %v", receipt, err)
	}
	for _, managed := range receipt.Managed {
		want := "test-catalog"
		if managed.Target == "opencode-command" {
			want = "private"
		}
		if managed.Catalog != want {
			t.Fatalf("receipt catalog: %#v", managed)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "files", "a.md"), []byte("# managed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	writeCatalogWithFiles(t, repo, "test-catalog", nil, nil)
	writeCatalogWithFiles(t, overlay, "private", nil, nil)
	plan, _ = Plan("all")
	if actionByIDOrFail(t, plan, "file/claude-agent/a.md").Action != "restore" || actionByIDOrFail(t, plan, "file/claude-hook/h").Action != "restore" || actionByIDOrFail(t, plan, "file/opencode-command/o.md").Action != "remove" {
		t.Fatalf("removal plan: %#v", plan)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(agent); string(data) != "# a.md\n" || fileMode(t, agent) != 0o600 {
		t.Fatalf("adopted file not restored: %q", data)
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored backup not removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "config", "opencode", "command", "o.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("overlay file not removed")
	}
}

func TestNamedFileNeverFollowsSymlinkedParent(t *testing.T) {
	home, repo := fileEnvironment(t)
	writeCatalogWithFiles(t, repo, "test-catalog", nil, []FileItem{{Target: "claude-hook", Name: "h", Source: "files/h"}})
	elsewhere := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".claude", "hooks")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	if plan, err := Plan("claude"); err != nil || actionCount(plan, "blocked_collision") != 1 {
		t.Fatalf("symlinked parent not blocked: %#v %v", plan, err)
	}
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatal("apply wrote through a symlinked parent")
	}
}
