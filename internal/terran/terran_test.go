package terran

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testEnvironment(t *testing.T) (home, repo string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home with spaces")
	repo = filepath.Join(base, "repo with spaces")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	writeCatalog(t, repo, []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}})
	return home, repo
}

func writeCatalog(t *testing.T, repo string, projections []Projection) {
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
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: "test-catalog", Version: "0.1.0", Projections: projections}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManifestStrictValidationAndStableFingerprint(t *testing.T) {
	_, repo := testEnvironment(t)
	first, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(repo, "terran.json"))
	var generic any
	_ = json.Unmarshal(data, &generic)
	reformatted, _ := json.Marshal(generic)
	_ = os.WriteFile(filepath.Join(repo, "terran.json"), append(reformatted, '\n'), 0o644)
	second, err := LoadManifest(repo)
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatalf("formatting changed fingerprint: %v", err)
	}
	_ = os.WriteFile(filepath.Join(repo, "terran.json"), []byte(`{"schema_version":1,"id":"test-catalog","version":"1","projections":[],"extra":true}`), 0o644)
	if _, err := LoadManifest(repo); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestManifestRejectsTrailingJSONDuplicatePairsAndOversize(t *testing.T) {
	_, repo := testEnvironment(t)
	manifestPath := filepath.Join(repo, "terran.json")
	valid, _ := os.ReadFile(manifestPath)
	for name, data := range map[string][]byte{
		"trailing":  append(append([]byte{}, valid...), []byte("\n{}")...),
		"duplicate": []byte(`{"schema_version":1,"id":"test-catalog","version":"1","projections":[{"skill":"example","source":"skills/example","targets":["agents","agents"]}]}`),
		"oversize":  append(valid, make([]byte, manifestLimit)...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(repo); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestManifestRejectsMismatchAndEscapingSymlink(t *testing.T) {
	_, repo := testEnvironment(t)
	md := filepath.Join(repo, "skills", "example", "SKILL.md")
	_ = os.WriteFile(md, []byte("---\nname: wrong\n---\n"), 0o644)
	if _, err := LoadManifest(repo); err == nil {
		t.Fatal("frontmatter mismatch accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644)
	_ = os.RemoveAll(filepath.Join(repo, "skills", "example"))
	_ = os.Symlink(outside, filepath.Join(repo, "skills", "example"))
	if _, err := LoadManifest(repo); err == nil {
		t.Fatal("escaping source symlink accepted")
	}
}

func TestManifestRejectsUnsafeControlFiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "manifest symlink",
			mutate: func(t *testing.T, repo string) {
				path := filepath.Join(repo, "terran.json")
				target := filepath.Join(repo, "manifest-target.json")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "group writable manifest",
			mutate: func(t *testing.T, repo string) {
				if err := os.Chmod(filepath.Join(repo, "terran.json"), 0o664); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "world writable manifest",
			mutate: func(t *testing.T, repo string) {
				if err := os.Chmod(filepath.Join(repo, "terran.json"), 0o646); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "SKILL.md symlink",
			mutate: func(t *testing.T, repo string) {
				path := filepath.Join(repo, "skills", "example", "SKILL.md")
				target := filepath.Join(repo, "skills", "example", "content.md")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "group writable SKILL.md",
			mutate: func(t *testing.T, repo string) {
				if err := os.Chmod(filepath.Join(repo, "skills", "example", "SKILL.md"), 0o664); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "world writable SKILL.md",
			mutate: func(t *testing.T, repo string) {
				if err := os.Chmod(filepath.Join(repo, "skills", "example", "SKILL.md"), 0o646); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := testEnvironment(t)
			tc.mutate(t, repo)
			if _, err := LoadManifest(repo); err == nil {
				t.Fatal("unsafe control file accepted")
			}
		})
	}
}

func TestManifestRejectsForeignOwnedControlFilesWhenPortable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing files to foreign ownership requires root")
	}
	for _, relative := range []string{"terran.json", filepath.Join("skills", "example", "SKILL.md")} {
		t.Run(relative, func(t *testing.T) {
			_, repo := testEnvironment(t)
			if err := os.Chown(filepath.Join(repo, relative), 1, -1); err != nil {
				t.Skipf("foreign ownership is unavailable: %v", err)
			}
			if _, err := LoadManifest(repo); err == nil || !strings.Contains(err.Error(), "owned") {
				t.Fatalf("foreign-owned control file accepted: %v", err)
			}
		})
	}
}

func TestManifestRejectsMultiplyLinkedControlFiles(t *testing.T) {
	for _, relative := range []string{"terran.json", filepath.Join("skills", "example", "SKILL.md")} {
		t.Run(relative, func(t *testing.T) {
			_, repo := testEnvironment(t)
			path := filepath.Join(repo, relative)
			if err := os.Link(path, path+".hardlink"); err != nil {
				if errors.Is(err, syscall.EPERM) {
					t.Skipf("hard links unavailable: %v", err)
				}
				t.Fatal(err)
			}
			if _, err := LoadManifest(repo); err == nil || !strings.Contains(err.Error(), "hard link") {
				t.Fatalf("multiply linked control file accepted: %v", err)
			}
		})
	}
}

func TestApplyRejectsChangedEnrolledIDWithoutProjectionMutation(t *testing.T) {
	home, repo := testEnvironment(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repo, "terran.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"test-catalog"`, `"changed-catalog"`, 1))
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err == nil || !strings.Contains(err.Error(), "id changed") {
		t.Fatalf("changed id was not refused: %v", err)
	}
	for _, root := range []string{filepath.Join(home, ".agents"), filepath.Join(home, ".claude")} {
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("apply mutated target path %s: %v", root, err)
		}
	}
	paths, _ := ResolvePaths()
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("apply wrote receipt after changed id: %v", err)
	}
}

func TestMutationSourceRevalidationRejectsEscapedSwap(t *testing.T) {
	_, repo := testEnvironment(t)
	loaded, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	source := loaded.Sources["example"]
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedSource(loaded.Repository, source); err == nil {
		t.Fatal("mutation-time source revalidation accepted an escaped symlink swap")
	}
}

func TestRejectsUnsafeRepositorySourceAndTargetPermissions(t *testing.T) {
	home, repo := testEnvironment(t)
	if err := os.Chmod(filepath.Join(repo, "skills", "example"), 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(repo); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("group-writable source accepted: %v", err)
	}
	if err := os.Chmod(filepath.Join(repo, "skills", "example"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("agents")
	if err != nil || actionCount(plan, "blocked_collision") != 1 {
		t.Fatalf("unsafe writable target root was not blocked: %#v %v", plan, err)
	}
}

func TestRejectsForeignOwnedRepositoryWhenPortable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a directory to foreign ownership requires root")
	}
	_, repo := testEnvironment(t)
	if err := os.Chown(repo, 1, -1); err != nil {
		t.Skipf("foreign ownership is unavailable: %v", err)
	}
	if _, err := LoadManifest(repo); err == nil || !strings.Contains(err.Error(), "owned") {
		t.Fatalf("foreign-owned repository accepted: %v", err)
	}
}

func TestLockRejectsSymlinkAndUnsafeMetadata(t *testing.T) {
	_, _ = testEnvironment(t)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, paths.Lock); err != nil {
		t.Fatal(err)
	}
	if err := withLock(paths.Lock, func() error { return nil }); err == nil {
		t.Fatal("lock symlink accepted")
	}
	if err := os.Remove(paths.Lock); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Lock, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.Lock, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := withLock(paths.Lock, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("unsafe lock permissions accepted: %v", err)
	}
	if err := os.Chmod(paths.Lock, 0o600); err != nil {
		t.Fatal(err)
	}
	hardlink := paths.Lock + ".hardlink"
	if err := os.Link(paths.Lock, hardlink); err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skipf("hard links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := withLock(paths.Lock, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "single-link") {
		t.Fatalf("multiply linked lock accepted: %v", err)
	}
}

func TestPathSafetyAndEnrollment(t *testing.T) {
	_, repo := testEnvironment(t)
	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err := ResolvePaths(); err == nil {
		t.Fatal("relative XDG path accepted")
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(os.Getenv("HOME"), "state"))
	first, changed, err := Enroll(repo, "test center", false)
	if err != nil || !changed {
		t.Fatalf("enroll: %v", err)
	}
	second, changed, err := Enroll(repo, "ignored new name", false)
	if err != nil || changed || first != second {
		t.Fatalf("idempotent enroll failed: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeCatalog(t, other, []Projection{{Skill: "other", Source: "skills/other", Targets: []string{"agents"}}})
	if _, _, err := Enroll(other, "other", false); err == nil {
		t.Fatal("different repository did not require replace")
	}
	replaced, changed, err := Enroll(other, "other", true)
	if err != nil || !changed || replaced.CommandCenterID != first.CommandCenterID {
		t.Fatalf("replace failed: %v", err)
	}
	paths, _ := ResolvePaths()
	if mode := fileMode(t, paths.ConfigFile); mode != 0o600 {
		t.Fatalf("config mode %o", mode)
	}
	if mode := fileMode(t, paths.ConfigDir); mode != 0o700 {
		t.Fatalf("config dir mode %o", mode)
	}
}

func TestEnrollReplaceRefusesManagedReceiptButIdenticalIsIdempotent(t *testing.T) {
	_, repo := testEnvironment(t)
	if _, _, err := Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("agents", "test"); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := Enroll(repo, "ignored", true); err != nil || changed {
		t.Fatalf("identical enrollment was not idempotent: changed=%v err=%v", changed, err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeCatalog(t, other, []Projection{{Skill: "other", Source: "skills/other", Targets: []string{"agents"}}})
	if _, _, err := Enroll(other, "other", true); err == nil || !strings.Contains(err.Error(), "decommission") || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("managed replacement was not clearly refused: %v", err)
	}
}

func TestEnrollReplaceRetiresTrustedEmptyReceipt(t *testing.T) {
	_, repo := testEnvironment(t)
	first, _, err := Enroll(repo, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := ResolvePaths()
	empty := Receipt{SchemaVersion: SchemaVersion, RepositoryID: first.RepositoryID, RepositoryPath: first.RepositoryPath, RepositoryVersion: "0.1.0", Projections: []ReceiptProjection{}}
	if err := atomicJSON(paths.Receipt, empty); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeCatalog(t, other, []Projection{{Skill: "other", Source: "skills/other", Targets: []string{"agents"}}})
	canonicalOther, _ := filepath.EvalSymlinks(other)
	replaced, changed, err := Enroll(other, "other", true)
	if err != nil || !changed || replaced.RepositoryPath != canonicalOther {
		t.Fatalf("empty-receipt replacement failed: %#v changed=%v err=%v", replaced, changed, err)
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty receipt was not retired: %v", err)
	}
	if _, err := Plan("all"); err != nil {
		t.Fatalf("replacement was not immediately usable: %v", err)
	}
	if _, changed, err := Enroll(other, "ignored", true); err != nil || changed {
		t.Fatalf("replacement was not idempotent: changed=%v err=%v", changed, err)
	}
}

func TestEnrollReplaceConfigFailureRestoresPriorEmptyEnrollment(t *testing.T) {
	_, repo := testEnvironment(t)
	first, _, _ := Enroll(repo, "test", false)
	paths, _ := ResolvePaths()
	empty := Receipt{SchemaVersion: SchemaVersion, RepositoryID: first.RepositoryID, RepositoryPath: first.RepositoryPath, RepositoryVersion: "0.1.0", Projections: []ReceiptProjection{}}
	if err := atomicJSON(paths.Receipt, empty); err != nil {
		t.Fatal(err)
	}
	configBefore, _ := os.ReadFile(paths.ConfigFile)
	receiptBefore, _ := os.ReadFile(paths.Receipt)
	other := filepath.Join(t.TempDir(), "other")
	writeCatalog(t, other, nil)
	beforeEnrollmentConfigWrite = func() error { return errors.New("forced config write failure") }
	t.Cleanup(func() { beforeEnrollmentConfigWrite = nil })
	if _, _, err := Enroll(other, "other", true); err == nil || !strings.Contains(err.Error(), "forced config write failure") {
		t.Fatalf("forced config failure missing: %v", err)
	}
	configAfter, configErr := os.ReadFile(paths.ConfigFile)
	receiptAfter, receiptErr := os.ReadFile(paths.Receipt)
	if configErr != nil || receiptErr != nil || string(configAfter) != string(configBefore) || string(receiptAfter) != string(receiptBefore) {
		t.Fatalf("prior enrollment was not recovered: config=%v receipt=%v", configErr, receiptErr)
	}
	loaded, err := LoadEnrollment(paths)
	if err != nil || loaded.RepositoryPath != first.RepositoryPath {
		t.Fatalf("prior enrollment is unusable: %#v %v", loaded, err)
	}
}

func TestTrustedStateFilesRejectSymlinkHardlinkAndUnsafeMode(t *testing.T) {
	for _, state := range []string{"config", "receipt"} {
		for _, mutation := range []string{"symlink", "hardlink", "unsafe mode"} {
			t.Run(state+"/"+mutation, func(t *testing.T) {
				_, repo := testEnvironment(t)
				_, _, _ = Enroll(repo, "test", false)
				if _, err := Apply("agents", "test"); err != nil {
					t.Fatal(err)
				}
				paths, _ := ResolvePaths()
				path := paths.ConfigFile
				if state == "receipt" {
					path = paths.Receipt
				}
				switch mutation {
				case "symlink":
					target := path + ".target"
					if err := os.Rename(path, target); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path, path+".link"); err != nil {
						if errors.Is(err, syscall.EPERM) {
							t.Skipf("hard links unavailable: %v", err)
						}
						t.Fatal(err)
					}
				case "unsafe mode":
					if err := os.Chmod(path, 0o640); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if state == "config" {
					_, err = LoadEnrollment(paths)
				} else {
					_, err = LoadReceipt(paths)
				}
				if err == nil {
					t.Fatal("unsafe trusted state file was parsed")
				}
				if Doctor("test").Healthy {
					t.Fatal("doctor accepted unsafe trusted state file")
				}
			})
		}
	}
}

func TestTrustedStateFilesRejectForeignOwnerWhenPortable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing state ownership requires root")
	}
	for _, state := range []string{"config", "receipt"} {
		t.Run(state, func(t *testing.T) {
			_, repo := testEnvironment(t)
			_, _, _ = Enroll(repo, "test", false)
			_, _ = Apply("agents", "test")
			paths, _ := ResolvePaths()
			path := paths.ConfigFile
			if state == "receipt" {
				path = paths.Receipt
			}
			if err := os.Chown(path, 1, -1); err != nil {
				t.Skipf("foreign ownership unavailable: %v", err)
			}
			if Doctor("test").Healthy {
				t.Fatal("doctor accepted foreign-owned state file")
			}
		})
	}
}

func TestCreateAdoptNoopCollisionDriftAndRemoval(t *testing.T) {
	home, repo := testEnvironment(t)
	_, _, _ = Enroll(repo, "test", false)
	plan, err := Plan("all")
	if err != nil || actionCount(plan, "create") != 2 {
		t.Fatalf("create plan: %#v %v", plan, err)
	}
	agentRoot := filepath.Join(home, ".agents", "skills")
	_ = os.MkdirAll(agentRoot, 0o755)
	source := filepath.Join(repo, "skills", "example")
	canonicalSource, _ := filepath.EvalSymlinks(source)
	relativeSource, _ := filepath.Rel(agentRoot, canonicalSource)
	_ = os.Symlink(relativeSource, filepath.Join(agentRoot, "example"))
	plan, _ = Plan("all")
	if actionCount(plan, "adopt") != 1 || actionCount(plan, "create") != 1 {
		t.Fatalf("adopt plan: %#v", plan)
	}
	applied, err := Apply("all", "test")
	if err != nil || blocked(applied) {
		t.Fatalf("apply: %v %#v", err, applied)
	}
	plan, _ = Plan("all")
	if !plan.Clean || actionCount(plan, "noop") != 2 || !exactSymlink(filepath.Join(agentRoot, "example"), canonicalSource) {
		t.Fatalf("noop plan: %#v", plan)
	}
	_ = os.Remove(filepath.Join(agentRoot, "example"))
	_ = os.WriteFile(filepath.Join(agentRoot, "example"), []byte("collision"), 0o600)
	plan, _ = Plan("all")
	if actionCount(plan, "blocked_drift") != 1 {
		t.Fatalf("drift not detected: %#v", plan)
	}
	before := filepath.Join(home, ".claude", "skills", "example")
	_, _ = Apply("all", "test")
	if !exactSymlink(before, canonicalSource) {
		t.Fatal("blocked apply mutated another projection")
	}
	_ = os.Remove(filepath.Join(agentRoot, "example"))
	_ = os.Symlink(canonicalSource, filepath.Join(agentRoot, "example"))
	writeCatalog(t, repo, nil)
	_ = os.RemoveAll(source)
	plan, err = Plan("agents")
	if err != nil || actionCount(plan, "remove") != 1 {
		t.Fatalf("removal plan: %#v %v", plan, err)
	}
	if _, err := Apply("agents", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(agentRoot, "example")); !os.IsNotExist(err) {
		t.Fatal("removed projection remains")
	}
}

func TestCollisionWithoutReceiptAndTargetFiltering(t *testing.T) {
	home, repo := testEnvironment(t)
	_, _, _ = Enroll(repo, "test", false)
	root := filepath.Join(home, ".agents", "skills")
	_ = os.MkdirAll(root, 0o755)
	_ = os.WriteFile(filepath.Join(root, "example"), []byte("owned elsewhere"), 0o600)
	plan, _ := Plan("agents")
	if actionCount(plan, "blocked_collision") != 1 {
		t.Fatalf("collision not detected: %#v", plan)
	}
	if applied, err := Apply("all", "test"); err != nil || !blocked(applied) {
		t.Fatalf("blocked apply result: %#v %v", applied, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", "example")); !os.IsNotExist(err) {
		t.Fatal("blocked apply created another projection")
	}
	_ = os.Remove(filepath.Join(root, "example"))
	if _, err := Apply("claude", "test"); err != nil {
		t.Fatal(err)
	}
	paths, _ := ResolvePaths()
	receipt, _ := LoadReceipt(paths)
	if len(receipt.Projections) != 1 || receipt.Projections[0].Target != "claude" {
		t.Fatalf("unexpected filtered receipt: %#v", receipt)
	}
	if mode := fileMode(t, paths.Receipt); mode != 0o600 {
		t.Fatalf("receipt mode %o", mode)
	}
	if _, err := Apply("agents", "test"); err != nil {
		t.Fatal(err)
	}
	receipt, _ = LoadReceipt(paths)
	if len(receipt.Projections) != 2 {
		t.Fatalf("other target receipt not preserved: %#v", receipt)
	}
}

func TestMaliciousReceiptCannotAuthorizeRemoval(t *testing.T) {
	home, repo := testEnvironment(t)
	_, _, _ = Enroll(repo, "test", false)
	paths, _ := ResolvePaths()
	enrollment, _ := LoadEnrollment(paths)
	outside := filepath.Join(home, "outside")
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("x"), 0o600)
	receipt := Receipt{SchemaVersion: SchemaVersion, RepositoryID: "test-catalog", RepositoryPath: enrollment.RepositoryPath, Projections: []ReceiptProjection{{Skill: "evil", Target: "agents", Source: outside, Destination: outside, Strategy: "symlink"}}}
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan("all"); err == nil || !strings.Contains(err.Error(), "receipt") {
		t.Fatalf("malicious receipt accepted: %v", err)
	}
}

func TestSkillReceiptDestinationMustMatchFixedLeaf(t *testing.T) {
	_, repo := testEnvironment(t)
	_, _, _ = Enroll(repo, "test", false)
	_, _ = Apply("agents", "test")
	paths, _ := ResolvePaths()
	receipt, _ := LoadReceipt(paths)
	receipt.Projections[0].Destination = filepath.Join(paths.Home, "outside", "example")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths); err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("tampered skill destination accepted: %v", err)
	}
	doctor := Doctor("test")
	if doctor.Healthy {
		t.Fatal("doctor accepted a tampered skill destination")
	}
}

func actionCount(plan PlanResult, action string) int {
	n := 0
	for _, item := range plan.Actions {
		if item.Action == action {
			n++
		}
	}
	return n
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
