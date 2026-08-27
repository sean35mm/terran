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

type failOnWriteWriter struct {
	writes int
	failAt int
}

func (w *failOnWriteWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("writer failed")
	}
	return len(data), nil
}

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
	if err := json.Unmarshal(out.Bytes(), &value); err != nil || value["schema_version"] != float64(1) || value["version"] != "0.1.0-test" {
		t.Fatalf("invalid JSON: %s %v", out.String(), err)
	}
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
	commands := []string{"version", "enroll", "plan", "apply", "status", "doctor"}
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
	assertJSONError(t, out.Bytes(), "status_failed", "status failed")
	_ = os.MkdirAll(filepath.Join(repo, "skills", "example"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "skills", "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "terran.json"), []byte(`{"schema_version":1,"id":"test-catalog","version":"0.1.0","projections":[{"skill":"example","source":"skills/example","targets":["agents"]}]}`), 0o644)
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
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
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["schema_version"] != float64(1) {
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
	assertJSONError(t, out.Bytes(), "enroll_failed", "enrollment failed")
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
	if err := os.WriteFile(filepath.Join(repo, "config", "opencode.json"), []byte(`{"default_agent":"naru-orchestrator"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"id":"test-catalog","version":"0.1.0","projections":[],"instructions":[{"target":"opencode-global","source":"instructions/AGENTS.md"}],"configs":[{"target":"opencode-config","source":"config/opencode.json"}]}`
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
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

func TestCLIInteractiveCollisionChoicesAndPromptStreams(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantCode      int
		wantAction    string
		wantInvalid   bool
		wantInstalled bool
		wantReadError bool
	}{
		{name: "replace", input: "RePlAcE\n", wantCode: 0, wantAction: "replace", wantInstalled: true},
		{name: "keep", input: "k\n", wantCode: 0, wantAction: "skip"},
		{name: "abort", input: "a\n", wantCode: 1},
		{name: "invalid reprompt", input: "nope\nkeep\n", wantCode: 0, wantAction: "skip", wantInvalid: true},
		{name: "empty", input: "\n", wantCode: 1},
		{name: "EOF", input: "", wantCode: 1},
		{name: "overlong", input: strings.Repeat("x", 256) + "\n", wantCode: 1, wantReadError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			destination, source, original := cliConfigCollisionEnvironment(t)
			var stdout, stderr bytes.Buffer
			code := runWithIO([]string{"apply", "--target", "opencode"}, strings.NewReader(tc.input), &stdout, &stderr, true)
			if code != tc.wantCode {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "private restoration backup") || strings.Contains(stdout.String(), "private restoration backup") {
				t.Fatalf("prompt stream separation failed: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if strings.Contains(stderr.String(), destination) {
				t.Fatalf("prompt displayed private destination path: %q", stderr.String())
			}
			if strings.Contains(stderr.String(), string(original)) {
				t.Fatalf("prompt displayed file contents: %q", stderr.String())
			}
			if tc.wantReadError != strings.Contains(stderr.String(), "read collision choice") {
				t.Fatalf("read error=%v stderr=%q", tc.wantReadError, stderr.String())
			}
			if tc.wantReadError {
				paths, err := terran.ResolvePaths()
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{paths.Receipt, filepath.Join(paths.BackupDir, "opencode-config", "original")} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("scanner error mutated %s: %v", path, err)
					}
				}
			}
			if tc.wantInvalid != strings.Contains(stderr.String(), "Please enter") {
				t.Fatalf("invalid reprompt=%v stderr=%q", tc.wantInvalid, stderr.String())
			}
			if tc.wantAction != "" && !strings.Contains(stdout.String(), tc.wantAction) {
				t.Fatalf("result missing action %q: %q", tc.wantAction, stdout.String())
			}
			got, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantInstalled {
				want, _ := os.ReadFile(source)
				if !bytes.Equal(got, want) {
					t.Fatal("replace did not install source")
				}
			} else if !bytes.Equal(got, original) {
				t.Fatal("keep/abort changed collision")
			}
		})
	}
}

func TestCLINoninteractiveAndJSONNeverPrompt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		interactive bool
		json        bool
	}{
		{name: "noninteractive", args: []string{"apply", "--target", "opencode"}},
		{name: "json", args: []string{"apply", "--target", "opencode", "--json"}, interactive: true, json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destination, _, original := cliConfigCollisionEnvironment(t)
			var stdout, stderr bytes.Buffer
			if code := runWithIO(tc.args, strings.NewReader("replace\n"), &stdout, &stderr, tc.interactive); code != 3 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if strings.Contains(stderr.String(), "private restoration backup") {
				t.Fatalf("unexpected prompt: %q", stderr.String())
			}
			if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
				t.Fatal("noninteractive apply replaced collision")
			}
			if tc.json {
				var result terran.PlanResult
				dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
				if err := dec.Decode(&result); err != nil || len(result.Actions) != 1 || result.Actions[0].Action != "blocked_collision" {
					t.Fatalf("invalid JSON result: %#v %v", result, err)
				}
				if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
					t.Fatalf("JSON output was not exactly one object: %v", err)
				}
			}
		})
	}
}

func TestCLIConsentOutputFailurePreventsMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		failAt int
	}{
		{name: "initial prompt", input: "replace\n", failAt: 1},
		{name: "invalid choice diagnostic", input: "invalid\nreplace\n", failAt: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destination, _, original := cliConfigCollisionEnvironment(t)
			var stdout bytes.Buffer
			stderr := &failOnWriteWriter{failAt: tc.failAt}
			if code := runWithIO([]string{"apply", "--target", "opencode"}, strings.NewReader(tc.input), &stdout, stderr, true); code != 1 {
				t.Fatalf("write failure exit=%d", code)
			}
			if got, err := os.ReadFile(destination); err != nil || !bytes.Equal(got, original) {
				t.Fatalf("consent output failure changed destination: %q %v", got, err)
			}
			paths, err := terran.ResolvePaths()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{paths.Receipt, filepath.Join(paths.BackupDir, "opencode-config", "original")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("consent output failure mutated %s: %v", path, err)
				}
			}
		})
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
	if err := os.WriteFile(source, []byte(`{"default_agent":"naru-orchestrator"}`), 0o644); err != nil {
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
	if _, _, err := terran.Enroll(repo, "test", false); err != nil {
		t.Fatal(err)
	}
	return destination, source, original
}

func assertJSONError(t *testing.T, data []byte, code, message string) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	var result struct {
		SchemaVersion int `json:"schema_version"`
		Error         struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("invalid JSON error %q: %v", data, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing JSON in %q: %v", data, err)
	}
	if result.SchemaVersion != 1 || result.Error.Code != code || result.Error.Message != message {
		t.Fatalf("unexpected JSON error: %#v", result)
	}
}
