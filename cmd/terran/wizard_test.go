package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sean35mm/terran/internal/terran"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("reader failed") }

type hookReader struct {
	input  *strings.Reader
	hook   func()
	called bool
}

func (r *hookReader) Read(data []byte) (int, error) {
	if !r.called {
		r.called = true
		r.hook()
	}
	return r.input.Read(data)
}

func TestWizardFirstRunCatalogPathsAndApplyCancel(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input func(string) string
		cwd   func(string, string) string
	}{
		{name: "cwd default", input: func(string) string { return "\ny\n\n" }, cwd: func(_ string, repo string) string { return repo }},
		{name: "relative", input: func(string) string { return filepath.Join("home", "repo") + "\ny\n\n" }, cwd: func(base, _ string) string { return base }},
		{name: "tilde", input: func(string) string { return "~/repo\ny\n\n" }, cwd: func(base, _ string) string { return filepath.Join(base, "home") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, home, repo := wizardEnvironment(t)
			oldCWD, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(tc.cwd(base, repo)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(oldCWD) })
			var stdout, stderr bytes.Buffer
			code := runWithIO(nil, strings.NewReader(tc.input(repo)), &stdout, &stderr, true)
			if code != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			paths, _ := terran.ResolvePaths()
			enrollment, err := terran.LoadEnrollment(paths)
			canonicalRepo, canonicalErr := filepath.EvalSymlinks(repo)
			if err != nil || canonicalErr != nil || enrollment.RepositoryPath != canonicalRepo {
				t.Fatalf("enrollment=%#v err=%v", enrollment, err)
			}
			if !filepath.IsAbs(enrollment.RepositoryPath) || !strings.Contains(stdout.String(), "Catalog test-catalog 0.2.0") || !strings.Contains(stdout.String(), "Repository: "+canonicalRepo) || !strings.Contains(stdout.String(), "Nothing has been projected yet") {
				t.Fatalf("first-run summary/enrollment missing: %q %#v", stdout.String(), enrollment)
			}
			for _, path := range []string{paths.Receipt, filepath.Join(home, ".agents", "skills", "example")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("apply cancel created %s: %v", path, err)
				}
			}
		})
	}
}

func TestWizardFirstRunAcceptsLongCatalogPath(t *testing.T) {
	base, home, _ := wizardEnvironment(t)
	longRepo := filepath.Join(home, strings.Repeat("a", 80), strings.Repeat("b", 80), "catalog")
	if len(longRepo) <= 128 {
		t.Fatalf("test catalog path is not long enough: %d", len(longRepo))
	}
	createWizardCatalog(t, longRepo)
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	var stdout, stderr bytes.Buffer
	if code := runWithIO(nil, strings.NewReader(longRepo+"\ny\nq\n"), &stdout, &stderr, true); code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	canonicalRepo, err := filepath.EvalSymlinks(longRepo)
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := terran.ResolvePaths()
	enrollment, err := terran.LoadEnrollment(paths)
	if err != nil || enrollment.RepositoryPath != canonicalRepo || !strings.Contains(stdout.String(), "Repository: "+canonicalRepo) {
		t.Fatalf("enrollment=%#v stdout=%q err=%v", enrollment, stdout.String(), err)
	}
}

func TestWizardDisclosesCanonicalCatalogPathBeforeTrust(t *testing.T) {
	base, _, repo := wizardEnvironment(t)
	alias := filepath.Join(base, "catalog-link")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runWithIO(nil, strings.NewReader("catalog-link\ny\nq\n"), &stdout, &stderr, true); code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	paths, _ := terran.ResolvePaths()
	enrollment, err := terran.LoadEnrollment(paths)
	if err != nil || enrollment.RepositoryPath != canonicalRepo {
		t.Fatalf("enrollment=%#v err=%v", enrollment, err)
	}
	if !strings.Contains(stdout.String(), "Repository: "+canonicalRepo) || strings.Contains(stdout.String(), "Repository: "+alias) {
		t.Fatalf("canonical repository was not disclosed: %q", stdout.String())
	}
}

func TestWizardFirstRunTrustAndCatalogFailures(t *testing.T) {
	t.Run("trust defaults no", func(t *testing.T) {
		_, _, repo := wizardEnvironment(t)
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader(repo+"\n\n"), &stdout, &stderr, true); code != 1 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		paths, _ := terran.ResolvePaths()
		if _, err := os.Lstat(paths.ConfigFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("default-No trust created enrollment: %v", err)
		}
	})

	t.Run("invalid catalog", func(t *testing.T) {
		base, _, _ := wizardEnvironment(t)
		invalid := filepath.Join(base, "invalid")
		if err := os.MkdirAll(invalid, 0o755); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader(invalid+"\n"), &stdout, &stderr, true); code != 1 || !strings.Contains(stderr.String(), "catalog is not valid") {
			t.Fatalf("code/output: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "Trust and enroll") {
			t.Fatalf("invalid catalog reached trust prompt: %q", stderr.String())
		}
	})

	t.Run("corrupt enrollment is not missing", func(t *testing.T) {
		_, _, _ = wizardEnvironment(t)
		paths, _ := terran.ResolvePaths()
		if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.ConfigFile, []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader("anything\n"), &stdout, &stderr, true); code != 1 || strings.Contains(stderr.String(), "Catalog path") || !strings.Contains(stderr.String(), "load enrollment") {
			t.Fatalf("code/output: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})

	t.Run("unsafe enrollment path is not missing", func(t *testing.T) {
		base, _, _ := wizardEnvironment(t)
		paths, _ := terran.ResolvePaths()
		if err := os.MkdirAll(filepath.Dir(paths.ConfigDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(base, "missing-target"), paths.ConfigDir); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader("anything\n"), &stdout, &stderr, true); code != 1 || strings.Contains(stderr.String(), "Catalog path") || !strings.Contains(stderr.String(), "enrollment directory") {
			t.Fatalf("code/output: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

func TestWizardReturningCleanDoesNotPromptOrRewriteReceipt(t *testing.T) {
	_, _, repo := wizardEnvironment(t)
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := terran.Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	paths, _ := terran.ResolvePaths()
	before, err := os.ReadFile(paths.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, _ := os.Stat(paths.Receipt)
	var stdout, stderr bytes.Buffer
	if code := runWithIO(nil, failingReader{}, &stdout, &stderr, true); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	after, _ := os.ReadFile(paths.Receipt)
	infoAfter, _ := os.Stat(paths.Receipt)
	if !bytes.Equal(before, after) || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("clean guided run rewrote receipt")
	}
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "is up to date") {
		t.Fatalf("clean output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestWizardPendingDetailsFinalConfirmationAndSuccess(t *testing.T) {
	t.Run("details then final cancel", func(t *testing.T) {
		_, home, repo := wizardEnvironment(t)
		if _, _, err := terran.Enroll(repo, "test", false); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader("d\na\n\n"), &stdout, &stderr, true); code != 1 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		for _, want := range []string{"Skills: 1", "source:", "destination:", "reason:", "Final validated plan (locked):", "Exact locked actions:"} {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("output missing %q: %q", want, stdout.String())
			}
		}
		if !strings.Contains(stderr.String(), "[d] Details") || !strings.Contains(stderr.String(), "Apply this exact plan? [y] Apply, [N] Quit") {
			t.Fatalf("prompts missing: %q", stderr.String())
		}
		paths, _ := terran.ResolvePaths()
		for _, path := range []string{paths.Receipt, filepath.Join(home, ".agents", "skills", "example")} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final cancel created %s: %v", path, err)
			}
		}
	})

	t.Run("apply and verify", func(t *testing.T) {
		_, _, repo := wizardEnvironment(t)
		if _, _, err := terran.Enroll(repo, "test", false); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader("a\ny\n"), &stdout, &stderr, true); code != 0 || !strings.Contains(stdout.String(), "Applied and verified") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		status, err := terran.Status("all")
		if err != nil || !status.Clean {
			t.Fatalf("status=%#v err=%v", status, err)
		}
	})
}

func TestWizardRedisplaysChangedExactLockedPlan(t *testing.T) {
	_, home, repo := wizardEnvironment(t)
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	newSource := filepath.Join(repo, "skills", "replacement")
	if err := os.MkdirAll(newSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newSource, "SKILL.md"), []byte("---\nname: example\ndescription: replacement\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	input := &hookReader{input: strings.NewReader("a\ny\n"), hook: func() {
		if !strings.Contains(stdout.String(), "Changes are ready for review:") {
			t.Fatalf("catalog changed before the earlier view was displayed: %q", stdout.String())
		}
		writeWizardManifest(t, repo, terran.Manifest{SchemaVersion: 1, ID: "test-catalog", Version: "0.2.0", Projections: []terran.Projection{{Skill: "example", Source: "skills/replacement", Targets: []string{"agents"}}}})
	}}
	if code := runWithIO(nil, input, &stdout, &stderr, true); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	lockedAt := strings.Index(stdout.String(), "Final validated plan (locked):")
	if lockedAt < 0 {
		t.Fatalf("locked plan was not displayed: %q", stdout.String())
	}
	locked := stdout.String()[lockedAt:]
	canonicalSource, err := filepath.EvalSymlinks(newSource)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(home, ".agents", "skills", "example")
	for _, want := range []string{"Exact locked actions:", "create example (skill)", "source: " + canonicalSource, "destination: " + destination, "reason: target root will be created"} {
		if !strings.Contains(locked, want) {
			t.Fatalf("changed locked plan missing %q: %q", want, locked)
		}
	}
	oldSource, err := filepath.EvalSymlinks(filepath.Join(repo, "skills", "example"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(locked, "source: "+oldSource) {
		t.Fatalf("locked display retained the earlier source: %q", locked)
	}
}

func TestWizardCollisionChoicesShareReaderAndKeepIsPartialSuccess(t *testing.T) {
	base, home, repo := wizardEnvironment(t)
	_ = base
	configSource := filepath.Join(repo, "config", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(configSource), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configSource, []byte(`{"default_agent":"naru-orchestrator"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := terran.Manifest{SchemaVersion: 1, ID: "test-catalog", Version: "0.2.0", Projections: []terran.Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}}, Configs: []terran.Config{{Target: "opencode-config", Source: "config/opencode.json"}}}
	writeWizardManifest(t, repo, manifest)
	destination := filepath.Join(home, "config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"theme":"user"}`)
	if err := os.WriteFile(destination, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runWithIO(nil, strings.NewReader("a\nk\ny\n"), &stdout, &stderr, true); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "[k] Keep") || !strings.Contains(stdout.String(), "Kept items remain unmanaged") || !bytes.Equal(mustRead(t, destination), original) {
		t.Fatalf("keep result stdout=%q stderr=%q destination=%q", stdout.String(), stderr.String(), mustRead(t, destination))
	}
}

func TestWizardUnsafeBlockHasNoApplyPrompt(t *testing.T) {
	_, home, repo := wizardEnvironment(t)
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "example"), []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runWithIO(nil, strings.NewReader("a\ny\n"), &stdout, &stderr, true); code != 3 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "Apply") || !strings.Contains(stdout.String(), "Terran is blocked") || !strings.Contains(stdout.String(), "terran doctor") {
		t.Fatalf("blocked guidance stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestWizardDriftBlockHasNoConfirmation(t *testing.T) {
	_, home, repo := wizardEnvironment(t)
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := terran.Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(home, ".agents", "skills", "example")
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runWithIO(nil, strings.NewReader("a\ny\n"), &stdout, &stderr, true); code != 3 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "Apply") || !strings.Contains(stdout.String(), "changed or became unsafe") {
		t.Fatalf("drift guidance stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestWizardNonTTYDevNullAndPromptIOFailures(t *testing.T) {
	t.Run("non-TTY bare is help", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader("y\n"), &stdout, &stderr, false); code != 0 || !strings.Contains(stdout.String(), "Start here:") || stderr.Len() != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("dev null is non-TTY help", func(t *testing.T) {
		_, _, _ = wizardEnvironment(t)
		input, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, input, &stdout, &stderr, false); code != 0 || !strings.Contains(stdout.String(), "Start here:") || stderr.Len() != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("read failure", func(t *testing.T) {
		_, _, _ = wizardEnvironment(t)
		var stdout, stderr bytes.Buffer
		if code := runWithIO(nil, failingReader{}, &stdout, &stderr, true); code != 1 || !strings.Contains(stderr.String(), "read catalog path") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("prompt write failure", func(t *testing.T) {
		_, _, _ = wizardEnvironment(t)
		var stdout bytes.Buffer
		if code := runWithIO(nil, strings.NewReader(""), &stdout, failingWriter{}, true); code != 1 {
			t.Fatalf("code=%d", code)
		}
	})

	t.Run("summary write failure", func(t *testing.T) {
		_, _, repo := wizardEnvironment(t)
		if _, _, err := terran.Enroll(repo, "test", false); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		if code := runWithIO(nil, strings.NewReader(""), failingWriter{}, &stderr, true); code != 1 || !strings.Contains(stderr.String(), "write plan summary") {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})
}

func wizardEnvironment(t *testing.T) (base, home, repo string) {
	t.Helper()
	base = t.TempDir()
	home = filepath.Join(base, "home")
	repo = filepath.Join(home, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	createWizardCatalog(t, repo)
	return base, home, repo
}

func createWizardCatalog(t *testing.T, repo string) {
	t.Helper()
	skill := filepath.Join(repo, "skills", "example")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: example\ndescription: test\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeWizardManifest(t, repo, terran.Manifest{SchemaVersion: 1, ID: "test-catalog", Version: "0.2.0", Projections: []terran.Projection{{Skill: "example", Source: "skills/example", Targets: []string{"agents"}}}})
}

func writeWizardManifest(t *testing.T, repo string, manifest terran.Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return data
}
