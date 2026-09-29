package terran

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func captureIDs(t *testing.T, target string) []string {
	t.Helper()
	result, err := Capture(target)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestCaptureRequiresEnrollment(t *testing.T) {
	testEnvironment(t)
	if _, err := Capture("all"); err == nil {
		t.Fatal("capture succeeded without enrollment")
	} else if code, _ := ErrorCode(err); code != CodeNotEnrolled {
		t.Fatalf("code = %q", code)
	}
}

func TestCaptureListsOnlyUnmanagedEntries(t *testing.T) {
	home, repo := testEnvironment(t)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "test"); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(home, ".agents", "skills")
	for _, dir := range []string{"mine", "naru-plan", ".trash", "Not Valid"} {
		if err := os.MkdirAll(filepath.Join(skills, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(home, ".claude", "agents", "helper.md"), "secret body", 0o644)
	mustWrite(t, filepath.Join(home, ".claude", "agents", "notes.txt"), "x", 0o644)
	mustWrite(t, filepath.Join(home, ".claude", "agents", "naru-a.md"), "x", 0o644)
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), "codex secret", 0o644)
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"top-secret-model","theme":"dark"}`, 0o644)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "hooks", "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	want := []string{"file/claude-agent/helper.md", "instruction/codex-global", "json-keys/claude-settings/model", "json-keys/claude-settings/theme", "skill/agents/mine"}
	if got := captureIDs(t, "all"); !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	if got := captureIDs(t, "codex"); !reflect.DeepEqual(got, []string{"instruction/codex-global"}) {
		t.Fatalf("codex items = %v", got)
	}
	result, _ := Capture("all")
	data, _ := json.Marshal(result)
	for _, secret := range []string{"top-secret-model", "codex secret", "secret body"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("capture output leaked %q", secret)
		}
	}

	if _, err := Hold("skill/agents/example"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(skills, "example")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skills, "example"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := captureIDs(t, "agents"); !reflect.DeepEqual(got, []string{"skill/agents/mine"}) {
		t.Fatalf("held or owned id listed: %v", got)
	}
}

func TestCaptureReportsUnreadableSettings(t *testing.T) {
	home, repo := testEnvironment(t)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".claude", "settings.json"), `{"a":1,"a":2}`, 0o644)
	result, err := Capture("claude")
	if err != nil || len(result.Items) != 1 || result.Items[0].Reason != "unreadable json" || result.Items[0].Name != "" || result.Items[0].Kind != "unmanaged_entry" {
		t.Fatalf("items = %#v err = %v", result.Items, err)
	}
}

func writeToolCatalog(t *testing.T, repo, id string, tools []Tool) {
	t.Helper()
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: id, Version: "0.1.0", Projections: []Projection{}, Tools: tools}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	mustWrite(t, filepath.Join(repo, "terran.json"), string(data)+"\n", 0o644)
}

func TestDoctorChecksRequiredTools(t *testing.T) {
	_, repo := testEnvironment(t)
	writeToolCatalog(t, repo, "test-catalog", []Tool{{Name: "present-tool"}, {Name: "missing-tool"}, {Name: "mac-only-tool", Platforms: []string{"darwin"}}})
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	mustWrite(t, filepath.Join(bin, "present-tool"), "#!/bin/sh\n", 0o755)
	t.Setenv("PATH", bin)
	previous := currentPlatform
	currentPlatform = "linux"
	t.Cleanup(func() { currentPlatform = previous })
	result := Doctor("test")
	if !doctorCheck(result, "tool:present-tool", "ok") || !doctorCheck(result, "tool:missing-tool", "fail") {
		t.Fatalf("checks = %#v", result.Checks)
	}
	for _, check := range result.Checks {
		if check.Name == "tool:mac-only-tool" {
			t.Fatal("darwin-only tool checked on linux")
		}
		if check.Name == "tool:missing-tool" && !strings.Contains(check.Message, "missing-tool not found on PATH") {
			t.Fatalf("message = %q", check.Message)
		}
	}
	if result.Healthy {
		t.Fatal("doctor healthy with a missing tool")
	}
}

func TestDuplicateToolAcrossCatalogsIsInvalid(t *testing.T) {
	_, repo := testEnvironment(t)
	overlay := filepath.Join(filepath.Dir(repo), "overlay")
	writeToolCatalog(t, repo, "test-catalog", []Tool{{Name: "git"}})
	writeToolCatalog(t, overlay, "private-catalog", []Tool{{Name: "git"}})
	// Enroll loads both catalogs, so the duplicate is rejected there.
	if _, _, err := Enroll(repo, "test", overlay, false); err == nil {
		t.Fatal("duplicate tool accepted")
	} else if code, _ := ErrorCode(err); code != CodeManifestInvalid {
		t.Fatalf("err = %v", err)
	}
}
