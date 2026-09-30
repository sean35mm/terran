package terran

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fleetEnvironment enrolls "cc1" with an overlay whose inventory lists the given entries.
func fleetEnvironment(t *testing.T, inventory string) {
	t.Helper()
	_, repo := testEnvironment(t)
	overlay := writeOverlay(t, filepath.Join(filepath.Dir(repo), "overlay"), nil, nil, false)
	if inventory != "" {
		if err := os.WriteFile(filepath.Join(overlay, inventoryFile), []byte(inventory), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := Enroll(repo, "cc1", overlay, false); err != nil {
		t.Fatal(err)
	}
	previous := gitHead
	gitHead = func(string) (string, error) { return "5aeb5d4c0ffee", nil }
	t.Cleanup(func() { gitHead = previous })
}

func stubSSH(t *testing.T, stub func(ctx context.Context, target CommandCenter, args ...string) ([]byte, error)) {
	t.Helper()
	previous := runSSH
	runSSH = stub
	t.Cleanup(func() { runSSH = previous })
}

func exitError(t *testing.T, code string) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+code).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("sh exit %s: %v", code, err)
	}
	return err
}

const fleetInventory = `{"schema_version":1,"command_centers":[
 {"name":"cc10","platform":"linux","ssh":"cc10"},
 {"name":"cc3","platform":"linux","ssh":"cc3"},
 {"name":"cc2","platform":"linux","ssh":"cc2"},
 {"name":"cc4","platform":"darwin","ssh":"cc4"},
 {"name":"cc5","platform":"darwin","ssh":"cc5"},
 {"name":"cc1","platform":"darwin","ssh":"cc1"}]}`

func TestFleetStatusMixedResults(t *testing.T) {
	fleetEnvironment(t, fleetInventory)
	stubSSH(t, func(ctx context.Context, target CommandCenter, args ...string) ([]byte, error) {
		alias := target.SSH
		if strings.Join(args, " ") != "status --summary --json" {
			t.Errorf("unexpected ssh args %v", args)
		}
		switch alias {
		case "cc1":
			t.Error("local machine must not be queried over ssh")
		case "cc2", "cc10":
			return []byte(`{"name":"liar","platform":"darwin","local":true,"reachable":true,"terran_version":"0.4.0","catalog_commit":"5aeb5d4","overlay_commit":"1c2d3e4","clean":false,"healthy":false,"held":0,"drifted":2,"blocked":0}`), nil
		case "cc3":
			return nil, context.DeadlineExceeded
		case "cc4":
			return nil, exitError(t, "127")
		case "cc5":
			return []byte(`{"schema_version":2,"error":{}}`), exitError(t, "2")
		}
		return nil, errors.New("unexpected alias " + alias)
	})
	rows, err := FleetStatus("0.4.0", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows {
		got = append(got, row.Name+"/"+row.Error)
	}
	want := "cc1/ cc2/ cc3/offline cc4/terran not found cc5/incompatible terran cc10/"
	if strings.Join(got, " ") != want {
		t.Fatalf("rows %q, want %q", strings.Join(got, " "), want)
	}
	if !rows[0].Local || !rows[0].Reachable || rows[0].Platform != currentPlatform || rows[0].CatalogCommit != "5aeb5d4" || rows[0].OverlayCommit != "5aeb5d4" || rows[0].TerranVersion != "0.4.0" {
		t.Fatalf("local row %#v", rows[0])
	}
	if cc2 := rows[1]; cc2.Local || !cc2.Reachable || cc2.Platform != "linux" || cc2.Drifted != 2 || cc2.OverlayCommit != "1c2d3e4" {
		t.Fatalf("remote row must keep inventory identity and remote counts: %#v", cc2)
	}
	if rows[2].Reachable || rows[2].TerranVersion != "" {
		t.Fatalf("offline row %#v", rows[2])
	}
}

func TestFleetStatusRemoteErrorEnvelopeAndMissingTools(t *testing.T) {
	fleetEnvironment(t, `{"schema_version":1,"command_centers":[{"name":"cc2","platform":"linux","ssh":"cc2"},{"name":"cc3","platform":"linux","ssh":"cc3"}]}`)
	stubSSH(t, func(ctx context.Context, target CommandCenter, args ...string) ([]byte, error) {
		alias := target.SSH
		if alias == "cc2" {
			return []byte(`{"schema_version":2,"error":{"code":"not_enrolled","message":"status failed: lstat x:\nno such file","next":"enroll"}}`), exitError(t, "1")
		}
		return []byte(`{"name":"x","platform":"linux","local":false,"reachable":true,"terran_version":"0.4.0","clean":true,"healthy":true,"held":0,"drifted":0,"blocked":0,"tools_missing":2}`), nil
	})
	rows, err := FleetStatus("0.4.0", time.Second)
	if err != nil || len(rows) != 3 {
		t.Fatalf("%#v %v", rows, err)
	}
	if cc2 := rows[1]; cc2.Reachable || cc2.Error != "not_enrolled: status failed: lstat x: no such file" {
		t.Fatalf("error envelope row %#v", cc2)
	}
	if cc3 := rows[2]; !cc3.Reachable || !cc3.Healthy || cc3.ToolsMissing != 2 {
		t.Fatalf("tools row %#v", cc3)
	}
}

func TestLocalSummaryCountsMissingToolsWithoutUnhealthy(t *testing.T) {
	_, repo := testEnvironment(t)
	manifest := `{"schema_version":2,"id":"test-catalog","version":"0.4.0","projections":[{"skill":"example","source":"skills/example","targets":["agents"]}],"tools":[{"name":"terran-test-missing-tool"}]}`
	if err := os.WriteFile(filepath.Join(repo, "terran.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "cc1", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply("all", "0.4.0"); err != nil {
		t.Fatal(err)
	}
	previous := gitHead
	gitHead = func(string) (string, error) { return "5aeb5d4c0ffee", nil }
	t.Cleanup(func() { gitHead = previous })
	summary, err := LocalSummary("0.4.0")
	if err != nil || summary.ToolsMissing != 1 || !summary.Healthy || !summary.Clean {
		t.Fatalf("summary %#v %v; doctor %#v", summary, err, Doctor("0.4.0"))
	}
}

func TestFleetStatusWithoutInventoryOrOverlayIsLocalOnly(t *testing.T) {
	stubSSH(t, func(context.Context, CommandCenter, ...string) ([]byte, error) {
		t.Error("ssh must not run")
		return nil, errors.New("unreachable")
	})
	t.Run("no inventory", func(t *testing.T) {
		fleetEnvironment(t, "")
		if rows, err := FleetStatus("0.4.0", time.Second); err != nil || len(rows) != 1 || !rows[0].Local {
			t.Fatalf("%#v %v", rows, err)
		}
	})
	t.Run("no overlay", func(t *testing.T) {
		_, repo := testEnvironment(t)
		if _, _, err := Enroll(repo, "solo", "", false); err != nil {
			t.Fatal(err)
		}
		if rows, err := FleetStatus("0.4.0", time.Second); err != nil || len(rows) != 1 || rows[0].Name != "solo" {
			t.Fatalf("%#v %v", rows, err)
		}
	})
}

func TestFleetStatusUnenrolledFails(t *testing.T) {
	_, _ = testEnvironment(t)
	if _, err := FleetStatus("0.4.0", time.Second); !hasCode(err, CodeNotEnrolled) {
		t.Fatalf("got %v", err)
	}
}

func TestLoadInventoryRejectsInvalidBeforeAnyExec(t *testing.T) {
	called := false
	stubSSH(t, func(context.Context, CommandCenter, ...string) ([]byte, error) { called = true; return nil, nil })
	cc := func(name, platform, ssh string) string {
		return `{"schema_version":1,"command_centers":[{"name":"` + name + `","platform":"` + platform + `","ssh":"` + ssh + `"}]}`
	}
	cases := map[string]string{
		"leading dash":      cc("cc2", "linux", "-oProxyCommand=x"),
		"space":             cc("cc2", "linux", "cc2 evil"),
		"empty alias":       cc("cc2", "linux", ""),
		"unknown platform":  cc("cc2", "windows", "cc2"),
		"bad name":          cc(" cc2", "linux", "cc2"),
		"duplicate":         `{"schema_version":1,"command_centers":[{"name":"a","platform":"linux","ssh":"a"},{"name":"a","platform":"linux","ssh":"b"}]}`,
		"schema version 2":  `{"schema_version":2,"command_centers":[]}`,
		"unknown field":     `{"schema_version":1,"command_centers":[],"extra":1}`,
		"trailing document": `{"schema_version":1,"command_centers":[]} {}`,
		"option as user":    `{"schema_version":1,"command_centers":[{"name":"cc2","platform":"linux","ssh":"cc2","user":"-oProxyCommand=x"}]}`,
		"user with at":      `{"schema_version":1,"command_centers":[{"name":"cc2","platform":"linux","ssh":"cc2","user":"a@b"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, inventoryFile), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadInventory(dir); err == nil {
				t.Fatal("accepted invalid inventory")
			}
		})
	}
	if called {
		t.Fatal("runSSH was called while validating inventories")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, inventoryFile), []byte(cc("cc2", "linux", "cc-2.example_host")), 0o644); err != nil {
		t.Fatal(err)
	}
	if inventory, err := LoadInventory(dir); err != nil || len(inventory.CommandCenters) != 1 {
		t.Fatalf("valid inventory: %v", err)
	}
	withUser := `{"schema_version":1,"command_centers":[{"name":"cc2","platform":"linux","ssh":"omarchy","user":"sgil"}]}`
	if err := os.WriteFile(filepath.Join(dir, inventoryFile), []byte(withUser), 0o644); err != nil {
		t.Fatal(err)
	}
	if inventory, err := LoadInventory(dir); err != nil || inventory.CommandCenters[0].User != "sgil" {
		t.Fatalf("inventory with user: %#v %v", inventory, err)
	}
}

func TestInvalidInventoryFailsFleetBeforeExec(t *testing.T) {
	fleetEnvironment(t, `{"schema_version":1,"command_centers":[{"name":"cc2","platform":"linux","ssh":"-bad"}]}`)
	stubSSH(t, func(context.Context, CommandCenter, ...string) ([]byte, error) {
		t.Error("ssh must not run")
		return nil, nil
	})
	if _, err := FleetStatus("0.4.0", time.Second); !hasCode(err, CodeManifestInvalid) {
		t.Fatalf("got %v", err)
	}
}

func TestFleetStatusTimeoutMarksHungMachineOffline(t *testing.T) {
	fleetEnvironment(t, fleetInventory)
	stubSSH(t, func(ctx context.Context, target CommandCenter, args ...string) ([]byte, error) {
		alias := target.SSH
		if alias == "cc3" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(`{"name":"x","platform":"linux","local":false,"reachable":true,"terran_version":"0.4.0","catalog_commit":"5aeb5d4","overlay_commit":"1c2d3e4","clean":true,"healthy":true,"held":1,"drifted":0,"blocked":0}`), nil
	})
	start := time.Now()
	rows, err := FleetStatus("0.4.0", 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 1200*time.Millisecond {
		t.Fatalf("fleet status took %s", elapsed)
	}
	if len(rows) != 6 {
		t.Fatalf("rows %#v", rows)
	}
	for _, row := range rows {
		switch {
		case row.Name == "cc3":
			if row.Reachable || row.Error != "offline" {
				t.Fatalf("cc3 %#v", row)
			}
		case !row.Reachable || row.TerranVersion == "":
			t.Fatalf("row not populated: %#v", row)
		}
	}
}

func TestRemoteStatus(t *testing.T) {
	fleetEnvironment(t, fleetInventory)
	stubSSH(t, func(ctx context.Context, target CommandCenter, args ...string) ([]byte, error) {
		alias := target.SSH
		if strings.Join(args, " ") != "status --local --json" {
			t.Errorf("unexpected ssh args %v", args)
		}
		switch alias {
		case "cc2":
			return []byte(`{"schema_version":2,"clean":true,"items":[]}`), nil
		case "cc3":
			return []byte(` {"schema_version":2, "clean":false, "items":[{"id":"skill/agents/a","kind":"skill","target":"agents","status":"drift","source":"/s","destination":"/d"}]}`), exitError(t, "1")
		case "cc5":
			return []byte(`{"schema_version":2,"error":{"code":"not_enrolled","message":"status failed"}}`), exitError(t, "1")
		case "cc10":
			return []byte(`{"schema_version":2,"clean":true,"items":[],"extra":"\u001b[2J"}`), nil
		}
		return nil, errors.New("down")
	})
	if out, clean, err := RemoteStatus("cc2", time.Second); err != nil || !clean || string(out) != "{\"schema_version\":2,\"clean\":true,\"items\":[]}\n" {
		t.Fatalf("cc2: %s %v %v", out, clean, err)
	}
	if out, clean, err := RemoteStatus("cc3", time.Second); err != nil || clean || !strings.HasPrefix(string(out), `{"schema_version":2,"clean":false,"items":[{"id":"skill/agents/a"`) {
		t.Fatalf("cc3: %s %v %v", out, clean, err)
	}
	if out, clean, err := RemoteStatus("cc5", time.Second); err != nil || clean || string(out) != "{\"schema_version\":2,\"error\":{\"code\":\"not_enrolled\",\"message\":\"status failed\"}}\n" {
		t.Fatalf("cc5: %s %v %v", out, clean, err)
	}
	if out, _, err := RemoteStatus("cc10", time.Second); err == nil || !strings.Contains(err.Error(), "incompatible terran") {
		t.Fatalf("cc10 unknown field accepted: %s %v", out, err)
	}
	if _, _, err := RemoteStatus("cc4", time.Second); !hasCode(err, CodeUnreachable) {
		t.Fatalf("cc4: %v", err)
	}
	if _, _, err := RemoteStatus("nope", time.Second); !hasCode(err, CodeUsage) {
		t.Fatalf("unknown: %v", err)
	}
	if _, _, err := RemoteStatus("cc1", time.Second); !hasCode(err, CodeUsage) {
		t.Fatalf("local name: %v", err)
	}
}

func TestNaturalLess(t *testing.T) {
	for _, pair := range [][2]string{{"cc2", "cc10"}, {"cc1", "cc2"}, {"cc", "cc1"}, {"a9", "b1"}, {"cc02", "cc2x"}} {
		if !naturalLess(pair[0], pair[1]) || naturalLess(pair[1], pair[0]) {
			t.Fatalf("%q should sort before %q", pair[0], pair[1])
		}
	}
}

func TestFleetStatusOverFakeSSH(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	// Resolve the Go build cache location before HOME moves to a temporary directory.
	cache, err := exec.Command(goBin, "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	fleetEnvironment(t, `{"schema_version":1,"command_centers":[{"name":"cc1","platform":"`+runtime.GOOS+`","ssh":"cc1"},{"name":"cc2","platform":"`+runtime.GOOS+`","ssh":"cc2","user":"remote-user"},{"name":"cc3","platform":"linux","ssh":"cc3"}]}`)
	binary := filepath.Join(t.TempDir(), "terran")
	build := exec.Command(goBin, "build", "-o", binary, "github.com/sean35mm/terran/cmd/terran")
	build.Env = append(os.Environ(), "GOCACHE="+strings.TrimSpace(string(cache)))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	localHome := os.Getenv("HOME")
	localConfig, localState := os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_STATE_HOME")
	remoteHome := filepath.Join(t.TempDir(), "remote home")
	if err := os.MkdirAll(filepath.Join(remoteHome, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteHome, ".local", "bin", "terran"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := LoadEnrollment(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", remoteHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(remoteHome, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(remoteHome, "state"))
	if _, _, err := Enroll(enrolled.RepositoryPath, "cc2", enrolled.OverlayPath, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", localHome)
	t.Setenv("XDG_CONFIG_HOME", localConfig)
	t.Setenv("XDG_STATE_HOME", localState)

	// The fake ssh drops the -o options and the alias, then runs the built binary as the remote user.
	// cc3 simulates an unreachable host with ssh's connection-failure exit code; cc2 is only
	// reachable with the inventory's user.
	fakeDir := t.TempDir()
	script := "#!/bin/sh\nuser=\nwhile [ \"$1\" = -o ]; do case \"$2\" in User=*) user=${2#User=};; esac; shift 2; done\nalias=$1\nshift 2\n" +
		"[ \"$alias\" = cc3 ] && exit 255\n" +
		"[ \"$alias\" = cc2 ] && [ \"$user\" != remote-user ] && exit 255\n" +
		"HOME='" + remoteHome + "' XDG_CONFIG_HOME='" + filepath.Join(remoteHome, "config") + "' XDG_STATE_HOME='" + filepath.Join(remoteHome, "state") + "' exec '" + filepath.Join(remoteHome, ".local", "bin", "terran") + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	out, err := exec.Command(binary, "status", "--json").Output()
	if err != nil {
		t.Fatalf("terran status --json: %v\n%s", err, out)
	}
	var result struct {
		SchemaVersion  int              `json:"schema_version"`
		CommandCenters []MachineSummary `json:"command_centers"`
	}
	if err := json.Unmarshal(out, &result); err != nil || result.SchemaVersion != 2 || len(result.CommandCenters) != 3 {
		t.Fatalf("status output %s: %v", out, err)
	}
	local, remote, down := result.CommandCenters[0], result.CommandCenters[1], result.CommandCenters[2]
	if local.Name != "cc1" || !local.Local || !local.Reachable || remote.Name != "cc2" || remote.Local || !remote.Reachable || remote.TerranVersion == "" {
		t.Fatalf("rows %#v", result.CommandCenters)
	}
	if down.Name != "cc3" || down.Reachable || down.Error != "offline" {
		t.Fatalf("cc3 %#v", down)
	}

	// Item-level status over the same fake ssh, printed as the remote returned it.
	out, err = exec.Command(binary, "status", "cc2", "--json").Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		t.Fatalf("terran status cc2: %v\n%s", err, out)
	}
	var items StatusResult
	if err := json.Unmarshal(out, &items); err != nil || items.SchemaVersion != SchemaVersion {
		t.Fatalf("item status %s: %v", out, err)
	}
}
