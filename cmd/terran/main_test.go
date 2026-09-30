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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }

func TestHelpVersionJSONAndUsage(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	version, commit, date = "0.1.0-test", "abc", "today"
	defer func() { version, commit, date = oldVersion, oldCommit, oldDate }()
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--help"}, 0, "Terran manages"},
		{[]string{"help", "apply"}, 0, "Usage: terran apply"},
		{[]string{"apply", "--help"}, 0, "Usage: terran apply"},
		{[]string{"--version"}, 0, "0.1.0-test"},
		{[]string{"unknown"}, 2, ""},
		{[]string{"plan", "--target", "wrong"}, 2, ""},
		{[]string{"plan", "--help"}, 0, "opencode"},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		if got := run(tc.args, &out, &errOut); got != tc.code {
			t.Fatalf("%v code %d, want %d; stderr=%s", tc.args, got, tc.code, errOut.String())
		}
		if tc.want != "" && !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%v output %q", tc.args, out.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"version", "--json"}, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	var value map[string]any
	if err := json.Unmarshal(out.Bytes(), &value); err != nil || value["schema_version"] != float64(terran.SchemaVersion) || value["version"] != "0.1.0-test" {
		t.Fatalf("invalid JSON: %s %v", out.String(), err)
	}
}

func TestBareTerranShowsHelpWhenNotEnrolledAndFleetWhenEnrolled(t *testing.T) {
	base := t.TempDir()
	home, repo := filepath.Join(base, "home"), filepath.Join(base, "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	_ = os.MkdirAll(home, 0o755)
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Terran manages") {
		t.Fatalf("not enrolled: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	_ = os.MkdirAll(filepath.Join(repo, "skills", "example"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "skills", "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "terran.json"), []byte(`{"schema_version":1,"id":"test-catalog","version":"0.1.0","projections":[{"skill":"example","source":"skills/example","targets":["agents"]}]}`), 0o644)
	if _, _, err := terran.Enroll(repo, "cc1", "", false); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := run(nil, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "CC    PLATFORM") || !strings.Contains(out.String(), "cc1*") {
		t.Fatalf("enrolled: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, args := range [][]string{{"status", "--summary", "--local"}, {"status", "--target", "agents"}, {"status", "--local", "cc2"}, {"status", "a", "b"}, {"status", "nope"}} {
		out.Reset()
		errOut.Reset()
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}
	out.Reset()
	if code := run([]string{"status", "--summary", "--json"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), `"local":true`) {
		t.Fatalf("summary: code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"status", "--local", "--target", "agents", "--json"}, &out, &errOut); code != 1 || !strings.Contains(out.String(), `"items"`) {
		t.Fatalf("local: code=%d stdout=%q", code, out.String())
	}
}

func TestFleetTableRendersMixedRows(t *testing.T) {
	rows := []terran.MachineSummary{
		{Name: "cc1", Platform: "darwin", Local: true, Reachable: true, TerranVersion: "0.4.0", CatalogCommit: "5aeb5d4", OverlayCommit: "1c2d3e4", Clean: true, Healthy: true, Held: 5},
		{Name: "cc2", Platform: "linux", Reachable: true, TerranVersion: "0.4.0", CatalogCommit: "5aeb5d4", OverlayCommit: "1c2d3e4", Drifted: 2},
		{Name: "cc3", Platform: "linux", Error: "offline"},
		{Name: "cc4", Platform: "linux", Reachable: true, TerranVersion: "0.4.0", Blocked: 1, Drifted: 1},
		{Name: "cc5", Platform: "linux", Reachable: true, TerranVersion: "0.4.0", Clean: true},
		{Name: "cc6", Platform: "linux", Reachable: true, TerranVersion: "0.4.0", Clean: true, Healthy: true, ToolsMissing: 2},
	}
	var out bytes.Buffer
	if err := writeFleetTable(&out, rows); err != nil {
		t.Fatal(err)
	}
	want := "CC    PLATFORM  TERRAN  CATALOG  OVERLAY  STATE\n" +
		"cc1*  darwin    0.4.0   5aeb5d4  1c2d3e4  clean (5 held)\n" +
		"cc2   linux     0.4.0   5aeb5d4  1c2d3e4  drift: 2\n" +
		"cc3   linux     -       -        -        offline\n" +
		"cc4   linux     0.4.0   -        -        blocked: 1\n" +
		"cc5   linux     0.4.0   -        -        unhealthy\n" +
		"cc6   linux     0.4.0   -        -        clean, 2 tools missing\n"
	if out.String() != want {
		t.Fatalf("table:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestCLIJSONUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"plan", "--target", "wrong", "--json"}, &out, &errOut); code != 2 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	assertJSONError(t, out.Bytes(), terran.CodeUsage, "target must be all, agents, claude, opencode, codex, mise, ssh, or t3", "")
}

func TestCLIJSONWriteFailureIsNonzero(t *testing.T) {
	var errOut bytes.Buffer
	if code := run([]string{"version", "--json"}, failingWriter{}, &errOut); code != 1 {
		t.Fatalf("write failure code=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "write output") {
		t.Fatalf("write failure was not reported: %q", errOut.String())
	}
}

func TestCommandHelpIncludesFlagsDefaultsBehaviorAndExitCodes(t *testing.T) {
	for _, args := range [][]string{{"help", "apply"}, {"apply", "--help"}, {"apply", "-h"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 0 {
			t.Fatalf("%v code=%d stderr=%q", args, code, errOut.String())
		}
		if errOut.Len() != 0 {
			t.Fatalf("%v successful help wrote stderr: %q", args, errOut.String())
		}
		text := out.String()
		for _, want := range []string{"Mutates only", "-target", `default "all"`, "Exit:"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%v help missing %q: %q", args, want, text)
			}
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"apply", "--not-a-flag"}, &out, &errOut); code != 2 || out.Len() != 0 || errOut.Len() == 0 {
		t.Fatalf("invalid flag code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestSubcommandHelpOutputAndTrailingArgument(t *testing.T) {
	commands := []string{"version", "enroll", "plan", "apply", "status", "capture", "doctor"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run([]string{command, "--help"}, &out, &errOut); code != 0 {
				t.Fatalf("help code=%d stderr=%q", code, errOut.String())
			}
			if out.Len() == 0 || errOut.Len() != 0 {
				t.Fatalf("help stdout=%q stderr=%q", out.String(), errOut.String())
			}

			out.Reset()
			errOut.Reset()
			if code := run([]string{command, "--help", "extra"}, &out, &errOut); code != 2 {
				t.Fatalf("trailing argument code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("trailing argument stdout=%q stderr=%q", out.String(), errOut.String())
			}
		})
	}
}

func TestRootHelpOutputAndTrailingArgument(t *testing.T) {
	for _, help := range []string{"--help", "-h"} {
		t.Run(help, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run([]string{help}, &out, &errOut); code != 0 {
				t.Fatalf("help code=%d stderr=%q", code, errOut.String())
			}
			if out.Len() == 0 || errOut.Len() != 0 {
				t.Fatalf("help stdout=%q stderr=%q", out.String(), errOut.String())
			}

			out.Reset()
			errOut.Reset()
			if code := run([]string{help, "extra"}, &out, &errOut); code != 2 {
				t.Fatalf("trailing argument code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("trailing argument stdout=%q stderr=%q", out.String(), errOut.String())
			}
		})
	}
}

func TestCLIJSONOperationalAndBlockedExitCodes(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	_ = os.MkdirAll(home, 0o755)
	var out, errOut bytes.Buffer
	if code := run([]string{"status", "--json"}, &out, &errOut); code != 1 {
		t.Fatalf("operational code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	assertJSONError(t, out.Bytes(), terran.CodeNotEnrolled, "status failed", "run terran enroll --repo <path> --name <ccN>; see README Agent guide")
	out.Reset()
	errOut.Reset()
	if code := run([]string{"capture", "--json"}, &out, &errOut); code != 1 {
		t.Fatalf("capture code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	assertJSONError(t, out.Bytes(), terran.CodeNotEnrolled, "capture failed", "run terran enroll --repo <path> --name <ccN>; see README Agent guide")
	out.Reset()
	errOut.Reset()
	if code := run([]string{"status"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "next: run terran enroll --repo <path> --name <ccN>; see README Agent guide") {
		t.Fatalf("text error next step missing: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	_ = os.MkdirAll(filepath.Join(repo, "skills", "example"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "skills", "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "terran.json"), []byte(`{"schema_version":1,"id":"test-catalog","version":"0.1.0","projections":[{"skill":"example","source":"skills/example","targets":["agents"]}]}`), 0o644)
	if _, _, err := terran.Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".agents", "skills")
	_ = os.MkdirAll(root, 0o755)
	_ = os.WriteFile(filepath.Join(root, "example"), []byte("collision"), 0o600)
	out.Reset()
	errOut.Reset()
	if code := run([]string{"plan", "--json"}, &out, &errOut); code != 3 {
		t.Fatalf("blocked code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["schema_version"] != float64(terran.SchemaVersion) {
		t.Fatalf("blocked JSON invalid: %q %v", out.String(), err)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"plan"}, &out, &errOut); code != 3 {
		t.Fatalf("blocked human plan code=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "destination="+filepath.Join(root, "example")) || !strings.Contains(out.String(), "reason=destination exists") {
		t.Fatalf("human plan omitted destination or reason: %q", out.String())
	}
}

func TestCLIJSONOperationalEnrollmentFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	var out, errOut bytes.Buffer
	if code := run([]string{"enroll", "--repo", filepath.Join(home, "missing"), "--json"}, &out, &errOut); code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	assertJSONError(t, out.Bytes(), terran.CodeManifestInvalid, "enrollment failed", "fix terran.json in the catalog, then run terran plan")
	if errOut.Len() == 0 {
		t.Fatal("operational diagnostic missing from stderr")
	}
}

func TestCLIInstructionJSONHumanAndOpenCodeTarget(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	if err := os.MkdirAll(filepath.Join(home, "config", "opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "instructions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "instructions", "AGENTS.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "config", "opencode.json"), []byte(`{"default_agent":"naru"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"id":"test-catalog","version":"0.1.0","projections":[],"instructions":[{"target":"opencode-global","source":"instructions/AGENTS.md"}],"configs":[{"target":"opencode-config","source":"config/opencode.json"}]}`
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := terran.Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"plan", "--target", "opencode", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("JSON plan code=%d stderr=%q", code, errOut.String())
	}
	var result terran.PlanResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Actions) != 2 || result.Actions[0].Kind != "config" || result.Actions[0].Target != "opencode-config" || result.Actions[1].Kind != "instruction" || result.Actions[1].Target != "opencode-global" {
		t.Fatalf("OpenCode managed-file JSON missing fields: %#v %v", result, err)
	}
	for _, action := range result.Actions {
		if action.Source == "" || action.Destination == "" {
			t.Fatalf("managed-file JSON missing paths: %#v", result)
		}
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"plan", "--target", "opencode"}, &out, &errOut); code != 0 {
		t.Fatalf("human plan code=%d stderr=%q", code, errOut.String())
	}
	for _, want := range []string{"kind=config", "target=opencode-config", "kind=instruction", "target=opencode-global", "source=", "destination=", "reason="} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("human instruction output missing %q: %q", want, out.String())
		}
	}
}

func TestCLIApplyCollisionIsBlockedWithoutPrompt(t *testing.T) {
	destination, _, original := cliConfigCollisionEnvironment(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", "--target", "opencode"}, &stdout, &stderr); code != 3 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "Existing config opencode-config differs") {
		t.Fatalf("unexpected prompt: %q", stderr.String())
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
		t.Fatal("apply replaced collision")
	}
	if !strings.Contains(stdout.String(), "blocked_collision") {
		t.Fatalf("blocked action missing: %q", stdout.String())
	}
}

func cliConfigCollisionEnvironment(t *testing.T) (destination, source string, original []byte) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	source = filepath.Join(repo, "config", "opencode.json")
	destination = filepath.Join(home, "config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"default_agent":"naru"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"id":"test-catalog","version":"0.1.0","projections":[],"configs":[{"target":"opencode-config","source":"config/opencode.json"}]}`
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	original = []byte(`{"theme":"user"}`)
	if err := os.WriteFile(destination, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := terran.Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	return destination, source, original
}

func assertJSONError(t *testing.T, data []byte, code, message, next string) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var result struct {
		SchemaVersion int `json:"schema_version"`
		Error         struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Next    string `json:"next"`
		} `json:"error"`
	}
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("invalid JSON error %q: %v", data, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing JSON in %q: %v", data, err)
	}
	// Operational messages are "<context>: <error text>"; usage messages are exact.
	if result.SchemaVersion != terran.SchemaVersion || result.Error.Code != code || (result.Error.Message != message && !strings.HasPrefix(result.Error.Message, message+": ")) || result.Error.Next != next {
		t.Fatalf("unexpected JSON error: %#v", result)
	}
}

func TestCLIHoldAndUnhold(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	if err := os.MkdirAll(filepath.Join(repo, "instructions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "instructions", "CLAUDE.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":2,"id":"test-catalog","version":"0.1.0","projections":[],"instructions":[{"target":"claude-global","source":"instructions/CLAUDE.md"}]}`
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := terran.Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"hold", "instruction/claude-global", "--json"}, 0, `{"schema_version":2,"holds":["instruction/claude-global"]}`},
		{[]string{"hold", "--json", "instruction/claude-global"}, 0, `{"schema_version":2,"holds":["instruction/claude-global"]}`},
		{[]string{"unhold", "instruction/claude-global", "--json"}, 0, `{"schema_version":2,"holds":[]}`},
		{[]string{"unhold", "instruction/claude-global", "--json"}, 0, `{"schema_version":2,"holds":[]}`},
		{[]string{"hold", "--json"}, 2, ""},
		{[]string{"hold", "a", "b"}, 2, ""},
		{[]string{"hold", "--help"}, 0, "Usage: terran hold"},
		{[]string{"hold", "instruction/other", "--json"}, 1, ""},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		if got := run(tc.args, &out, &errOut); got != tc.code {
			t.Fatalf("%v code %d, want %d; stdout=%q stderr=%q", tc.args, got, tc.code, out.String(), errOut.String())
		}
		if tc.want != "" && !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%v output %q", tc.args, out.String())
		}
	}
	var out, errOut bytes.Buffer
	run([]string{"hold", "instruction/other", "--json"}, &out, &errOut)
	assertJSONError(t, out.Bytes(), terran.CodeUnknownItem, "hold failed", "run terran plan --json to list item ids")
}

func TestCLIEnrollOverlayJSONAndUnavailableOverlay(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	_ = os.MkdirAll(home, 0o755)
	for id, dir := range map[string]string{"test-catalog": "repo", "private": "overlay"} {
		skill := filepath.Join(base, dir, "skills", id)
		_ = os.MkdirAll(skill, 0o755)
		_ = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: "+id+"\n---\n"), 0o644)
		_ = os.WriteFile(filepath.Join(base, dir, "terran.json"), []byte(`{"schema_version":2,"id":"`+id+`","version":"0.1.0","projections":[{"skill":"`+id+`","source":"skills/`+id+`","targets":["agents"]}]}`), 0o644)
	}
	overlay, _ := filepath.EvalSymlinks(filepath.Join(base, "overlay"))
	var out, errOut bytes.Buffer
	if code := run([]string{"enroll", "--repo", filepath.Join(base, "repo"), "--name", "cc1", "--overlay", overlay, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("enroll code=%d stderr=%q", code, errOut.String())
	}
	var enrolled struct {
		Changed    bool              `json:"changed"`
		Enrollment terran.Enrollment `json:"enrollment"`
	}
	if err := json.Unmarshal(out.Bytes(), &enrolled); err != nil || !enrolled.Changed || enrolled.Enrollment.OverlayID != "private" || enrolled.Enrollment.OverlayPath != overlay {
		t.Fatalf("enroll JSON: %q %v", out.String(), err)
	}
	if err := os.RemoveAll(overlay); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"plan", "--json"}, &out, &errOut); code != 1 {
		t.Fatalf("plan code=%d stdout=%q", code, out.String())
	}
	assertJSONError(t, out.Bytes(), terran.CodeOverlayUnavailable, "plan failed", "clone the private catalog to "+overlay+" or re-enroll")
}

func TestCLIApplyDecideAndExpect(t *testing.T) {
	destination, _, original := cliConfigCollisionEnvironment(t)
	id := "config/opencode-config"
	for _, args := range [][]string{
		{"apply", "--decide", id},
		{"apply", "--decide", id + "=abort"},
		{"apply", "--decide", "=replace"},
		{"apply", "--decide", id + "=keep", "--decide", id + "=replace"},
		{"apply", "--decide", "skill/agents/none=keep", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	run([]string{"apply", "--decide", "skill/agents/none=keep", "--json"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), `"code":"usage"`) || !strings.Contains(stdout.String(), "skill/agents/none") {
		t.Fatalf("usage error: %q", stdout.String())
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
		t.Fatal("rejected decision changed the destination")
	}

	stdout.Reset()
	if code := run([]string{"plan"}, &stdout, &stderr); code != 3 {
		t.Fatalf("plan code=%d", code)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	digest, ok := strings.CutPrefix(lines[len(lines)-1], "digest=")
	if !ok || len(digest) != 64 {
		t.Fatalf("plan output must end with the digest: %q", stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"apply", "--expect", "0000", "--decide", id + "=replace", "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("stale digest code=%d stdout=%q", code, stdout.String())
	}
	assertJSONError(t, stdout.Bytes(), terran.CodePlanChanged, "apply failed", "run terran plan --json again and review")
	if !strings.Contains(stdout.String(), `"message":"apply failed: plan digest `) || !strings.Contains(stdout.String(), "does not match the expected digest") {
		t.Fatalf("JSON error message lacks the error text: %q", stdout.String())
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
		t.Fatal("stale digest changed the destination")
	}
	stdout.Reset()
	if code := run([]string{"apply", "--expect", digest, "--decide", id + "=replace", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("decided apply code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got, _ := os.ReadFile(destination); !bytes.Contains(got, []byte("naru")) {
		t.Fatalf("catalog config not installed: %q", got)
	}
}
