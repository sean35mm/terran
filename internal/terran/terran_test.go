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
	"time"
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

func TestTargetSpecsHaveUniqueSafeDestinations(t *testing.T) {
	_, _ = testEnvironment(t)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, spec := range targetSpecs {
		key := spec.Kind + "\x00" + spec.ID
		if seen[key] {
			t.Fatalf("duplicate target spec %s/%s", spec.Kind, spec.ID)
		}
		seen[key] = true
		name := ""
		if spec.Kind == "skill" {
			name = "x"
		}
		destination, err := spec.Dest(paths, name)
		if err != nil {
			t.Fatalf("resolve %s/%s: %v", spec.Kind, spec.ID, err)
		}
		if !contained(paths.Home, destination) && !contained(paths.ConfigBase, destination) {
			t.Fatalf("destination for %s/%s escapes test roots: %s", spec.Kind, spec.ID, destination)
		}
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

func TestErrorCodeClassification(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T) error
		code string
		next string
	}{
		{
			name: "no enrollment",
			run: func(t *testing.T) error {
				_, _ = testEnvironment(t)
				_, err := Plan("all")
				return err
			},
			code: CodeNotEnrolled,
			next: nextEnroll,
		},
		{
			name: "invalid manifest",
			run: func(t *testing.T) error {
				_, repo := testEnvironment(t)
				if err := os.WriteFile(filepath.Join(repo, "terran.json"), []byte(`{"schema_version":1,"id":"test-catalog","version":"0.1.0","projections":[],"extra":true}`), 0o644); err != nil {
					t.Fatal(err)
				}
				_, err := LoadManifest(repo)
				return err
			},
			code: CodeManifestInvalid,
			next: nextManifest,
		},
		{
			name: "tampered receipt",
			run: func(t *testing.T) error {
				_, repo := testEnvironment(t)
				if _, _, err := Enroll(repo, "test", "", false); err != nil {
					t.Fatal(err)
				}
				if _, err := Apply("all", "test"); err != nil {
					t.Fatal(err)
				}
				paths, err := ResolvePaths()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.Receipt, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err = Plan("all")
				return err
			},
			code: CodeReceiptInvalid,
			next: nextState,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t)
			if got, next := ErrorCode(err); got != tc.code || next != tc.next {
				t.Fatalf("ErrorCode()=(%q, %q), want (%q, %q); err=%v", got, next, tc.code, tc.next, err)
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
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
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
	first, changed, err := Enroll(repo, "test center", "", false)
	if err != nil || !changed {
		t.Fatalf("enroll: %v", err)
	}
	second, changed, err := Enroll(repo, "test center", "", false)
	if err != nil || changed || !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent enroll failed: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeCatalog(t, other, []Projection{{Skill: "other", Source: "skills/other", Targets: []string{"agents"}}})
	if _, _, err := Enroll(other, "other", "", false); err == nil {
		t.Fatal("different repository did not require replace")
	}
	replaced, changed, err := Enroll(other, "other", "", true)
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
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("agents", "test"); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := Enroll(repo, "test", "", true); err != nil || changed {
		t.Fatalf("identical enrollment was not idempotent: changed=%v err=%v", changed, err)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeCatalog(t, other, []Projection{{Skill: "other", Source: "skills/other", Targets: []string{"agents"}}})
	if _, _, err := Enroll(other, "other", "", true); err == nil || !strings.Contains(err.Error(), "decommission") || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("managed replacement was not clearly refused: %v", err)
	}
}

func TestEnrollReplaceRetiresTrustedEmptyReceipt(t *testing.T) {
	_, repo := testEnvironment(t)
	first, _, err := Enroll(repo, "test", "", false)
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
	replaced, changed, err := Enroll(other, "other", "", true)
	if err != nil || !changed || replaced.RepositoryPath != canonicalOther {
		t.Fatalf("empty-receipt replacement failed: %#v changed=%v err=%v", replaced, changed, err)
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty receipt was not retired: %v", err)
	}
	if _, err := Plan("all"); err != nil {
		t.Fatalf("replacement was not immediately usable: %v", err)
	}
	if _, changed, err := Enroll(other, "other", "", true); err != nil || changed {
		t.Fatalf("replacement was not idempotent: changed=%v err=%v", changed, err)
	}
}

func TestEnrollReplaceConfigFailureRestoresPriorEmptyEnrollment(t *testing.T) {
	_, repo := testEnvironment(t)
	first, _, _ := Enroll(repo, "test", "", false)
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
	if _, _, err := Enroll(other, "other", "", true); err == nil || !strings.Contains(err.Error(), "forced config write failure") {
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
				_, _, _ = Enroll(repo, "test", "", false)
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
					_, err = LoadReceipt(paths, Enrollment{})
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
			_, _, _ = Enroll(repo, "test", "", false)
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
	_, _, _ = Enroll(repo, "test", "", false)
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
	_, _, _ = Enroll(repo, "test", "", false)
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
	receipt, _ := LoadReceipt(paths, Enrollment{})
	if len(receipt.Projections) != 1 || receipt.Projections[0].Target != "claude" {
		t.Fatalf("unexpected filtered receipt: %#v", receipt)
	}
	if mode := fileMode(t, paths.Receipt); mode != 0o600 {
		t.Fatalf("receipt mode %o", mode)
	}
	if _, err := Apply("agents", "test"); err != nil {
		t.Fatal(err)
	}
	receipt, _ = LoadReceipt(paths, Enrollment{})
	if len(receipt.Projections) != 2 {
		t.Fatalf("other target receipt not preserved: %#v", receipt)
	}
}

func TestMaliciousReceiptCannotAuthorizeRemoval(t *testing.T) {
	home, repo := testEnvironment(t)
	_, _, _ = Enroll(repo, "test", "", false)
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
	_, _, _ = Enroll(repo, "test", "", false)
	_, _ = Apply("agents", "test")
	paths, _ := ResolvePaths()
	receipt, _ := LoadReceipt(paths, Enrollment{})
	receipt.Projections[0].Destination = filepath.Join(paths.Home, "outside", "example")
	if err := atomicJSON(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReceipt(paths, Enrollment{}); err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("tampered skill destination accepted: %v", err)
	}
	doctor := Doctor("test")
	if doctor.Healthy {
		t.Fatal("doctor accepted a tampered skill destination")
	}
}

func TestSchemaV1Upgrades(t *testing.T) {
	timestamp := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	t.Run("manifest", func(t *testing.T) {
		old := manifestV1{
			SchemaVersion: 1,
			ID:            "test-catalog",
			Version:       "0.3.0",
			Projections:   []projectionV1{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}},
			Instructions:  []instructionV1{{Target: "claude-global", Source: "instructions/CLAUDE.md"}},
			Configs:       []configV1{{Target: "opencode-config", Source: "config/opencode.json"}},
		}
		data, _ := json.Marshal(old)
		got, err := decodeManifest(data)
		want := Manifest{
			SchemaVersion: SchemaVersion,
			ID:            old.ID,
			Version:       old.Version,
			Projections:   []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}},
			Instructions:  []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}},
			Configs:       []Config{{Target: "opencode-config", Source: "config/opencode.json"}},
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("manifest upgrade: got=%#v want=%#v err=%v", got, want, err)
		}
	})

	t.Run("enrollment", func(t *testing.T) {
		old := enrollmentV1{1, "test-catalog", "/catalog", "cc-test", "Test Center"}
		data, _ := json.Marshal(old)
		got, err := decodeEnrollment(data)
		want := Enrollment{SchemaVersion: SchemaVersion, RepositoryID: old.RepositoryID, RepositoryPath: old.RepositoryPath, CommandCenterID: old.CommandCenterID, DisplayName: old.DisplayName}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("enrollment upgrade: got=%#v want=%#v err=%v", got, want, err)
		}
	})

	t.Run("receipt", func(t *testing.T) {
		managed := receiptInstructionV1{
			Target: "claude-global", Source: "/catalog/instructions/CLAUDE.md", Destination: "/home/.claude/CLAUDE.md", Strategy: "copy",
			SourceHash: strings.Repeat("a", 64), AppliedHash: strings.Repeat("a", 64), Origin: "created", AppliedAt: timestamp, TerranBuildVersion: "0.3.0",
		}
		old := receiptV1{
			SchemaVersion: 1, RepositoryID: "test-catalog", RepositoryPath: "/catalog", RepositoryVersion: "0.3.0", ManifestFingerprint: "fingerprint",
			Projections:  []receiptProjectionV1{{Skill: "example", Target: "agents", Source: "/catalog/skills/example", Destination: "/home/.agents/skills/example", Strategy: "symlink", AppliedAt: timestamp, TerranBuildVersion: "0.3.0"}},
			Instructions: []receiptInstructionV1{managed},
			Configs:      []receiptConfigV1{receiptConfigV1(managed)},
		}
		old.Configs[0].Target = "opencode-config"
		data, _ := json.Marshal(old)
		got, err := decodeReceipt(data)
		if err != nil || got.SchemaVersion != SchemaVersion || len(got.Projections) != 1 || got.Projections[0].Catalog != old.RepositoryID || len(got.Managed) != 2 {
			t.Fatalf("receipt upgrade: %#v err=%v", got, err)
		}
		if got.Managed[0].Kind != "instruction" || got.Managed[1].Kind != "config" || got.Managed[0].Catalog != old.RepositoryID || got.Managed[1].Catalog != old.RepositoryID {
			t.Fatalf("receipt managed upgrade: %#v", got.Managed)
		}
	})
}

func TestSchemaStrictVersionedDecoding(t *testing.T) {
	tests := []struct {
		name   string
		decode func([]byte) error
		data   string
	}{
		{"manifest v2 field under v1", func(data []byte) error { _, err := decodeManifest(data); return err }, `{"schema_version":1,"id":"x","version":"1","projections":[],"tools":[]}`},
		{"enrollment v2 field under v1", func(data []byte) error { _, err := decodeEnrollment(data); return err }, `{"schema_version":1,"repository_id":"x","repository_path":"/x","command_center_id":"cc-x","display_name":"x","holds":[]}`},
		{"receipt v2 field under v1", func(data []byte) error { _, err := decodeReceipt(data); return err }, `{"schema_version":1,"repository_id":"x","repository_path":"/x","repository_version":"1","manifest_fingerprint":"x","projections":[],"managed":[]}`},
		{"manifest v1 unknown", func(data []byte) error { _, err := decodeManifest(data); return err }, `{"schema_version":1,"id":"x","version":"1","projections":[],"unknown":true}`},
		{"manifest v2 unknown", func(data []byte) error { _, err := decodeManifest(data); return err }, `{"schema_version":2,"id":"x","version":"1","projections":[],"unknown":true}`},
		{"enrollment v1 unknown", func(data []byte) error { _, err := decodeEnrollment(data); return err }, `{"schema_version":1,"unknown":true}`},
		{"enrollment v2 unknown", func(data []byte) error { _, err := decodeEnrollment(data); return err }, `{"schema_version":2,"unknown":true}`},
		{"receipt v1 unknown", func(data []byte) error { _, err := decodeReceipt(data); return err }, `{"schema_version":1,"unknown":true}`},
		{"receipt v2 unknown", func(data []byte) error { _, err := decodeReceipt(data); return err }, `{"schema_version":2,"unknown":true}`},
		{"manifest schema 3", func(data []byte) error { _, err := decodeManifest(data); return err }, `{"schema_version":3}`},
		{"enrollment schema 3", func(data []byte) error { _, err := decodeEnrollment(data); return err }, `{"schema_version":3}`},
		{"receipt schema 3", func(data []byte) error { _, err := decodeReceipt(data); return err }, `{"schema_version":3}`},
		{"duplicate schema version", func(data []byte) error { _, err := decodeManifest(data); return err }, `{"schema_version":1,"schema_version":2}`},
		{"trailing JSON", func(data []byte) error { _, err := decodeManifest(data); return err }, `{"schema_version":2}{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.decode([]byte(tc.data)); err == nil {
				t.Fatal("invalid versioned JSON accepted")
			}
		})
	}
}

func TestSchemaV2FieldsRoundTrip(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: SchemaVersion, ID: "catalog", Version: "1", Projections: []Projection{{Skill: "skill", Source: "skills/skill", Targets: []string{"agents"}, Platforms: []string{"darwin"}}},
		Instructions: []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md", Platforms: []string{"linux"}}},
		Configs:      []Config{{Target: "opencode-config", Source: "config/opencode.json", Platforms: []string{"darwin", "linux"}}},
		Files:        []FileItem{{Target: "future-file", Name: "worker.md", Source: "files/worker.md", Platforms: []string{"linux"}}},
		JSONKeys:     []JSONKeysItem{{Target: "future-json", Source: "config/hooks.json", Platforms: []string{"darwin"}}},
		Tools:        []Tool{{Name: "go", Platforms: []string{"darwin", "linux"}}},
	}
	enrollment := Enrollment{SchemaVersion: SchemaVersion, RepositoryID: "catalog", RepositoryPath: "/catalog", CommandCenterID: "cc-test", DisplayName: "test", OverlayID: "overlay", OverlayPath: "/overlay", Holds: []string{"skill/agents/skill"}}
	receipt := Receipt{SchemaVersion: SchemaVersion, RepositoryID: "catalog", RepositoryPath: "/catalog", Projections: []ReceiptProjection{}, Managed: []ReceiptManaged{{Kind: "instruction", Catalog: "catalog", Target: "claude-global", Source: "/catalog/instructions/CLAUDE.md"}}}

	manifestData, _ := json.Marshal(manifest)
	gotManifest, manifestErr := decodeManifest(manifestData)
	enrollmentData, _ := json.Marshal(enrollment)
	gotEnrollment, enrollmentErr := decodeEnrollment(enrollmentData)
	receiptData, _ := json.Marshal(receipt)
	gotReceipt, receiptErr := decodeReceipt(receiptData)
	if manifestErr != nil || !reflect.DeepEqual(gotManifest, manifest) || enrollmentErr != nil || !reflect.DeepEqual(gotEnrollment, enrollment) || receiptErr != nil || !reflect.DeepEqual(gotReceipt, receipt) {
		t.Fatalf("v2 round trip failed: manifest=%v enrollment=%v receipt=%v", manifestErr, enrollmentErr, receiptErr)
	}
}

func TestManifestV2FutureFieldValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"file target", func(manifest *Manifest) {
			manifest.Files = []FileItem{{Target: "claude-agent", Name: "worker.md", Source: "files/worker.md"}}
		}},
		{"json-keys target", func(manifest *Manifest) {
			manifest.JSONKeys = []JSONKeysItem{{Target: "claude-settings", Source: "config/hooks.json"}}
		}},
		{"invalid tool", func(manifest *Manifest) { manifest.Tools = []Tool{{Name: "Bad Tool"}} }},
		{"empty platforms", func(manifest *Manifest) { manifest.Tools = []Tool{{Name: "go", Platforms: []string{"linux"}}} }},
		{"duplicate platforms", func(manifest *Manifest) { manifest.Tools = []Tool{{Name: "go", Platforms: []string{"linux", "linux"}}} }},
		{"unsupported platform", func(manifest *Manifest) { manifest.Tools = []Tool{{Name: "go", Platforms: []string{"windows"}}} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := testEnvironment(t)
			loaded, err := LoadManifest(repo)
			if err != nil {
				t.Fatal(err)
			}
			manifest := loaded.Manifest
			tc.mutate(&manifest)
			data, _ := json.Marshal(manifest)
			if tc.name == "empty platforms" {
				data = bytes.Replace(data, []byte(`"platforms":["linux"]`), []byte(`"platforms":[]`), 1)
			}
			if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(repo); err == nil {
				t.Fatal("invalid future manifest field accepted")
			}
		})
	}

	_, repo := testEnvironment(t)
	loaded, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest := loaded.Manifest
	manifest.Projections[0].Platforms = []string{"linux", "darwin"}
	manifest.Tools = []Tool{{Name: "go_1.24", Platforms: []string{"linux", "darwin"}}}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadManifest(repo)
	if err != nil || !reflect.DeepEqual(loaded.Manifest.Projections[0].Platforms, []string{"darwin", "linux"}) || !reflect.DeepEqual(loaded.Manifest.Tools[0].Platforms, []string{"darwin", "linux"}) {
		t.Fatalf("valid platforms/tools not normalized: %#v %v", loaded.Manifest, err)
	}
}

func TestItemIDRoundTripAndValidation(t *testing.T) {
	valid := []struct{ kind, target, name string }{
		{"skill", "claude", "herdr"},
		{"instruction", "claude-global", ""},
		{"config", "naru-runtime", ""},
		{"file", "claude-agent", "opus-worker.md"},
		{"json-keys", "claude-settings", "hooks"},
	}
	for _, tc := range valid {
		id := ItemID(tc.kind, tc.target, tc.name)
		kind, target, name, err := ParseItemID(id)
		if err != nil || kind != tc.kind || target != tc.target || name != tc.name {
			t.Fatalf("ParseItemID(%q)=(%q,%q,%q,%v)", id, kind, target, name, err)
		}
	}

	invalid := []string{"skill/../name", "skill//name", "skill/agents/name/extra", "instruction/claude-global/name", "skill/agents", "config/", "skill/agents/white space", "unknown/target/name"}
	for _, id := range invalid {
		if _, _, _, err := ParseItemID(id); err == nil {
			t.Fatalf("invalid item id %q accepted", id)
		} else if code, _ := ErrorCode(err); code != CodeUnknownItem {
			t.Fatalf("invalid item id %q code=%q", id, code)
		}
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

func TestPlatformFilterExcludesAndRemovesOwned(t *testing.T) {
	home, repo := testEnvironment(t)
	writeCatalog(t, repo, []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}, Platforms: []string{"darwin"}}})
	_, _, _ = Enroll(repo, "test", "", false)
	previous := currentPlatform
	t.Cleanup(func() { currentPlatform = previous })
	link := filepath.Join(home, ".agents", "skills", "example")

	currentPlatform = "linux"
	plan, err := Plan("all")
	if err != nil || actionCount(plan, "excluded") != 2 || !plan.Clean {
		t.Fatalf("excluded plan: %#v %v", plan, err)
	}
	if plan.Actions[0].Reason != "darwin-only" || plan.Actions[0].ID != "skill/agents/example" {
		t.Fatalf("excluded action: %#v", plan.Actions[0])
	}
	if status, err := Status("all"); err != nil || !status.Clean || status.Items[0].Status != "excluded" {
		t.Fatalf("excluded status: %#v %v", status, err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("excluded skill was projected")
	}

	currentPlatform = "darwin"
	if plan, _ := Plan("all"); actionCount(plan, "create") != 2 {
		t.Fatalf("darwin plan: %#v", plan)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal(err)
	}

	currentPlatform = "linux"
	if plan, _ := Plan("all"); actionCount(plan, "remove") != 2 || actionCount(plan, "excluded") != 0 {
		t.Fatalf("owned excluded plan: %#v", plan)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned excluded skill was not removed")
	}
	if plan, _ := Plan("all"); actionCount(plan, "excluded") != 2 {
		t.Fatalf("plan after removal: %#v", plan)
	}
}

// writeOverlay writes a catalog with id "private" (or keeps the primary id when
// sameID is set) and returns its canonical path.
func writeOverlay(t *testing.T, dir string, projections []Projection, instructions []Instruction, sameID bool) string {
	t.Helper()
	writeCatalogWithInstructions(t, dir, projections, instructions)
	if !sameID {
		path := filepath.Join(dir, "terran.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.Replace(data, []byte(`"id": "test-catalog"`), []byte(`"id": "private"`), 1), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func overlayEnrollment(t *testing.T, repo, overlay string) Enrollment {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return Enrollment{RepositoryID: "test-catalog", RepositoryPath: canonical, OverlayID: "private", OverlayPath: overlay}
}

func TestOverlayMergeRejectsDuplicatesSameIDAndEscapes(t *testing.T) {
	_, repo := testEnvironment(t)
	base := filepath.Dir(repo)
	secret := []Projection{{Skill: "secret", Source: "skills/secret", Targets: []string{"agents"}}}
	claude := []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}}
	cases := map[string]func() string{
		"duplicate skill": func() string {
			return writeOverlay(t, filepath.Join(base, "dup-skill"), []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}}, nil, false)
		},
		"duplicate instruction": func() string {
			writeCatalogWithInstructions(t, repo, []Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents", "claude"}}}, claude)
			return writeOverlay(t, filepath.Join(base, "dup-instruction"), nil, claude, false)
		},
		"same id": func() string { return writeOverlay(t, filepath.Join(base, "same-id"), secret, nil, true) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			overlay := setup()
			enrollment := overlayEnrollment(t, repo, overlay)
			if name == "same id" {
				enrollment.OverlayID = "test-catalog"
			}
			_, err := LoadCatalogs(enrollment)
			if !hasCode(err, CodeManifestInvalid) || (name != "same id" && (!strings.Contains(err.Error(), "test-catalog") || !strings.Contains(err.Error(), "private"))) {
				t.Fatalf("LoadCatalogs: %v", err)
			}
			if _, _, err := Enroll(repo, "test", overlay, false); !hasCode(err, CodeManifestInvalid) {
				t.Fatalf("enroll accepted invalid overlay: %v", err)
			}
		})
	}
	t.Run("escaping source", func(t *testing.T) {
		overlay := writeOverlay(t, filepath.Join(base, "escape"), secret, nil, false)
		outside := filepath.Join(t.TempDir(), "outside")
		writeCatalog(t, outside, []Projection{{Skill: "secret", Source: "secret", Targets: []string{"agents"}}})
		if err := os.RemoveAll(filepath.Join(overlay, "skills", "secret")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(overlay, "skills", "secret")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Enroll(repo, "test", overlay, false); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("escaping overlay source accepted: %v", err)
		}
		if _, err := LoadCatalogs(overlayEnrollment(t, repo, overlay)); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("escaping overlay source loaded: %v", err)
		}
	})
}

func TestOverlayItemsCarryCatalogAndAreRemovedFromOverlay(t *testing.T) {
	home, repo := testEnvironment(t)
	overlayDir := filepath.Join(filepath.Dir(repo), "overlay")
	overlay := writeOverlay(t, overlayDir, []Projection{{Skill: "secret", Source: "skills/secret", Targets: []string{"agents"}}}, []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}}, false)
	enrollment, changed, err := Enroll(repo, "test", overlay, false)
	if err != nil || !changed || enrollment.OverlayID != "private" || enrollment.OverlayPath != overlay {
		t.Fatalf("enroll overlay: %#v %v", enrollment, err)
	}
	plan, err := Plan("all")
	if err != nil || actionCount(plan, "create") != 4 {
		t.Fatalf("plan: %#v %v", plan, err)
	}
	for _, action := range plan.Actions {
		want := "test-catalog"
		if action.Skill == "secret" || action.Kind == "instruction" {
			want = "private"
		}
		if action.Catalog != want {
			t.Fatalf("action %s catalog %q, want %q", action.ID, action.Catalog, want)
		}
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".agents", "skills", "secret")
	if target, err := os.Readlink(link); err != nil || target != filepath.Join(overlay, "skills", "secret") {
		t.Fatalf("overlay link: %q %v", target, err)
	}
	paths, _ := ResolvePaths()
	receipt, err := LoadReceipt(paths, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	catalogs := map[string]string{}
	for _, projection := range receipt.Projections {
		catalogs[projection.Skill+"/"+projection.Target] = projection.Catalog
	}
	for _, managed := range receipt.Managed {
		catalogs[managed.Kind] = managed.Catalog
	}
	if catalogs["secret/agents"] != "private" || catalogs["instruction"] != "private" || catalogs["example/agents"] != "test-catalog" || receipt.RepositoryID != "test-catalog" {
		t.Fatalf("receipt catalogs: %#v", catalogs)
	}
	if _, err := LoadReceipt(paths, Enrollment{}); err == nil {
		t.Fatal("overlay receipt entries accepted without an enrolled overlay")
	}
	writeOverlay(t, overlayDir, nil, nil, false)
	plan, err = Plan("all")
	if err != nil || actionCount(plan, "remove") != 2 {
		t.Fatalf("removal plan: %#v %v", plan, err)
	}
	for _, action := range plan.Actions {
		if action.Action == "remove" && action.Catalog != "private" {
			t.Fatalf("removal catalog: %#v", action)
		}
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlay link was not removed: %v", err)
	}
}

func TestUnavailableOverlayNeverPlansRemoval(t *testing.T) {
	home, repo := testEnvironment(t)
	overlayDir := filepath.Join(filepath.Dir(repo), "overlay")
	overlay := writeOverlay(t, overlayDir, []Projection{{Skill: "secret", Source: "skills/secret", Targets: []string{"agents"}}}, []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}}, false)
	if _, _, err := Enroll(repo, "test", overlay, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	paths, _ := ResolvePaths()
	link := filepath.Join(home, ".agents", "skills", "secret")
	instruction, _ := instructionDestination(paths, "claude-global")
	snapshot := func() string {
		receipt, err := os.ReadFile(paths.Receipt)
		if err != nil {
			t.Fatal(err)
		}
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(instruction)
		if err != nil {
			t.Fatal(err)
		}
		return string(receipt) + "\x00" + target + "\x00" + string(copied)
	}
	before := snapshot()
	for name, breakOverlay := range map[string]func(){
		"missing": func() {
			if err := os.RemoveAll(overlayDir); err != nil {
				t.Fatal(err)
			}
		},
		"invalid": func() {
			writeOverlay(t, overlayDir, nil, nil, false)
			if err := os.WriteFile(filepath.Join(overlayDir, "terran.json"), []byte(`{"schema_version":2}`), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			breakOverlay()
			if _, err := Plan("all"); !hasCode(err, CodeOverlayUnavailable) {
				t.Fatalf("plan: %v", err)
			}
			if _, err := Apply("all", "test"); !hasCode(err, CodeOverlayUnavailable) {
				t.Fatalf("apply: %v", err)
			}
			if _, err := Status("all"); !hasCode(err, CodeOverlayUnavailable) {
				t.Fatalf("status: %v", err)
			}
			if after := snapshot(); after != before {
				t.Fatal("destination or receipt changed while overlay was unavailable")
			}
		})
	}
}

func hasCode(err error, code string) bool {
	got, _ := ErrorCode(err)
	return err != nil && got == code
}

func TestReEnrollKeepsHoldsAndAddsOverlay(t *testing.T) {
	_, repo := testEnvironment(t)
	first, _, err := Enroll(repo, "test", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Hold("skill/agents/example"); err != nil {
		t.Fatal(err)
	}
	renamed, changed, err := Enroll(repo, "renamed", "", false)
	if err != nil || !changed || renamed.DisplayName != "renamed" || renamed.CommandCenterID != first.CommandCenterID || !reflect.DeepEqual(renamed.Holds, []string{"skill/agents/example"}) {
		t.Fatalf("rename: %#v changed=%v %v", renamed, changed, err)
	}
	overlay := writeOverlay(t, filepath.Join(filepath.Dir(repo), "overlay"), []Projection{{Skill: "secret", Source: "skills/secret", Targets: []string{"agents"}}}, nil, false)
	added, changed, err := Enroll(repo, "", overlay, false)
	if err != nil || !changed || added.OverlayID != "private" || added.DisplayName != "renamed" || !reflect.DeepEqual(added.Holds, renamed.Holds) {
		t.Fatalf("add overlay: %#v changed=%v %v", added, changed, err)
	}
	again, changed, err := Enroll(repo, "again", "", false)
	if err != nil || !changed || again.OverlayID != "private" || again.OverlayPath != overlay || !reflect.DeepEqual(again.Holds, renamed.Holds) {
		t.Fatalf("rename kept overlay: %#v changed=%v %v", again, changed, err)
	}
	paths, _ := ResolvePaths()
	if loaded, err := LoadEnrollment(paths); err != nil || !reflect.DeepEqual(loaded, again) {
		t.Fatalf("persisted enrollment: %#v %v", loaded, err)
	}
}

func TestOverlayChangeRefusedWhileItOwnsItems(t *testing.T) {
	_, repo := testEnvironment(t)
	base := filepath.Dir(repo)
	secret := []Projection{{Skill: "secret", Source: "skills/secret", Targets: []string{"agents"}}}
	overlayDir := filepath.Join(base, "overlay")
	overlay := writeOverlay(t, overlayDir, secret, nil, false)
	other := writeOverlay(t, filepath.Join(base, "other-overlay"), secret, nil, false)
	if _, _, err := Enroll(repo, "test", overlay, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "", other, false); !hasCode(err, CodeRepositoryMismatch) {
		t.Fatalf("overlay path change while owning items: %v", err)
	}
	if _, _, err := Enroll(repo, "", "", true); !hasCode(err, CodeRepositoryMismatch) {
		t.Fatalf("overlay removal while owning items: %v", err)
	}
	paths, _ := ResolvePaths()
	if loaded, err := LoadEnrollment(paths); err != nil || loaded.OverlayPath != overlay {
		t.Fatalf("refused change was persisted: %#v %v", loaded, err)
	}
	writeOverlay(t, overlayDir, nil, nil, false)
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	changed, _, err := Enroll(repo, "", other, false)
	if err != nil || changed.OverlayPath != other {
		t.Fatalf("overlay change without owned items: %#v %v", changed, err)
	}
	if removed, _, err := Enroll(repo, "", "", true); err != nil || removed.OverlayID != "" || removed.OverlayPath != "" {
		t.Fatalf("overlay removal without owned items: %#v %v", removed, err)
	}
}

func TestApplyRevalidatesOverlayCatalog(t *testing.T) {
	home, repo := testEnvironment(t)
	overlay := writeOverlay(t, filepath.Join(filepath.Dir(repo), "overlay"), []Projection{{Skill: "secret", Source: "skills/secret", Targets: []string{"agents"}}}, []Instruction{{Target: "claude-global", Source: "instructions/CLAUDE.md"}}, false)
	if _, _, err := Enroll(repo, "test", overlay, false); err != nil {
		t.Fatal(err)
	}
	beforeInstructionMutation = func(Action) error {
		path := filepath.Join(overlay, "terran.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, bytes.Replace(data, []byte(`"version": "0.1.0"`), []byte(`"version": "0.2.0"`), 1), 0o644)
	}
	t.Cleanup(func() { beforeInstructionMutation = nil })
	if _, err := Apply("all", "test"); err == nil || !strings.Contains(err.Error(), "changed during apply") {
		t.Fatalf("overlay change during apply was not detected: %v", err)
	}
	paths, _ := ResolvePaths()
	if _, err := os.Lstat(filepath.Join(home, ".agents", "skills", "secret")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlay skill mutation was not rolled back: %v", err)
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt written after overlay change: %v", err)
	}
}
