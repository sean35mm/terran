package terran

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInteractiveReplacementAdoptsAndRestoresInstructionAndConfig(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*testing.T) (string, string)
		kind        string
		target      string
		destination func(Paths) string
		remove      func(*testing.T, string)
		wantActive  os.FileMode
	}{
		{
			name: "instruction", setup: func(t *testing.T) (string, string) { return instructionEnvironment(t, "claude-global") },
			kind: "instruction", target: "claude-global", destination: func(paths Paths) string { p, _ := instructionDestination(paths, "claude-global"); return p },
			remove: func(t *testing.T, repo string) { writeCatalogWithInstructions(t, repo, nil, nil) }, wantActive: 0o640,
		},
		{
			name: "config", setup: func(t *testing.T) (string, string) { return configEnvironment(t, false) },
			kind: "config", target: "opencode-config", destination: func(paths Paths) string { p, _ := configDestination(paths, "opencode-config"); return p },
			remove: func(t *testing.T, repo string) { writeCatalogWithConfigs(t, repo, nil, nil) }, wantActive: 0o600,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := tc.setup(t)
			prepareInstructionParents(t)
			paths, _ := ResolvePaths()
			destination := tc.destination(paths)
			original := []byte("user's complete original\n")
			if tc.kind == "config" {
				original = []byte(`{"theme":"user"}`)
			}
			if err := os.WriteFile(destination, original, 0o640); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Enroll(repo, "test", "", false); err != nil {
				t.Fatal(err)
			}
			calls := 0
			result, err := ApplyWithOptions("all", "test", ApplyOptions{ResolveCollision: func(action Action) (CollisionDecision, error) {
				calls++
				if action.Kind != tc.kind || action.Target != tc.target {
					t.Fatalf("unexpected callback: %#v", action)
				}
				return CollisionReplace, nil
			}})
			if err != nil || calls != 1 || actionFor(result, tc.kind, tc.target).Action != "replace" {
				t.Fatalf("replacement: %#v calls=%d err=%v", result, calls, err)
			}
			source, _ := managedSource(mustLoadedManifest(t, repo), tc.kind, tc.target)
			want, _ := os.ReadFile(source)
			if got, _ := os.ReadFile(destination); !bytes.Equal(got, want) || fileMode(t, destination) != tc.wantActive {
				t.Fatalf("managed file not installed with expected mode")
			}
			backup := instructionBackup(paths, tc.target)
			if got, _ := os.ReadFile(backup); !bytes.Equal(got, original) || fileMode(t, backup) != 0o600 {
				t.Fatal("private backup does not exactly preserve original")
			}
			receipt, err := LoadReceipt(paths, Enrollment{})
			if err != nil {
				t.Fatal(err)
			}
			managed := receipt.Managed[0]
			if managed.Kind != tc.kind || managed.Origin != "adopted" || managed.OriginalHash != hashBytes(original) || managed.OriginalMode != 0o640 || managed.Backup != backup {
				t.Fatalf("adopted metadata: %#v", managed)
			}
			tc.remove(t, repo)
			if _, err := Apply("all", "test"); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) || fileMode(t, destination) != 0o640 {
				t.Fatal("removal did not restore original bytes and mode")
			}
		})
	}
}

func mustLoadedManifest(t *testing.T, repo string) LoadedManifest {
	t.Helper()
	loaded, err := LoadManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestInteractiveSkipContinuesAndRemainsCollision(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	blockedDestination, _ := instructionDestination(paths, "claude-global")
	original := []byte("keep me\n")
	if err := os.WriteFile(blockedDestination, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyWithOptions("all", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { return CollisionSkip, nil }})
	if err != nil || actionFor(result, "instruction", "claude-global").Action != "skip" {
		t.Fatalf("skip result: %#v %v", result, err)
	}
	if got, _ := os.ReadFile(blockedDestination); !bytes.Equal(got, original) {
		t.Fatal("skip changed collision")
	}
	other, _ := instructionDestination(paths, "opencode-global")
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other selected action did not apply: %v", err)
	}
	receipt, err := LoadReceipt(paths, Enrollment{})
	if err != nil || len(receipt.Managed) != 1 || receipt.Managed[0].Kind != "instruction" || receipt.Managed[0].Target != "opencode-global" {
		t.Fatalf("skip gained ownership: %#v %v", receipt, err)
	}
	if plan, _ := Plan("all"); actionCount(plan, "blocked_collision") != 1 {
		t.Fatalf("later plan lost collision: %#v", plan)
	}
	if status, _ := Status("all"); status.Clean || status.Items[0].Status != "collision" {
		t.Fatalf("later status hid collision: %#v", status)
	}
}

func TestInteractiveAbortAndIneligibleCollisionsNeverMutate(t *testing.T) {
	t.Run("abort multiple", func(t *testing.T) {
		_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
		prepareInstructionParents(t)
		paths, _ := ResolvePaths()
		for _, target := range []string{"claude-global", "opencode-global"} {
			destination, _ := instructionDestination(paths, target)
			_ = os.WriteFile(destination, []byte(target+" original"), 0o640)
		}
		_, _, _ = Enroll(repo, "test", "", false)
		calls := 0
		_, err := ApplyWithOptions("all", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) {
			calls++
			if calls == 1 {
				return CollisionReplace, nil
			}
			return CollisionAbort, nil
		}})
		if !errors.Is(err, ErrApplyAborted) || calls != 2 {
			t.Fatalf("abort: calls=%d err=%v", calls, err)
		}
		for _, target := range []string{"claude-global", "opencode-global"} {
			destination, _ := instructionDestination(paths, target)
			got, _ := os.ReadFile(destination)
			if string(got) != target+" original" {
				t.Fatal("abort changed target")
			}
			if _, err := os.Lstat(instructionBackup(paths, target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("abort created backup")
			}
		}
		if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("abort wrote receipt")
		}
	})

	t.Run("drift and unsafe", func(t *testing.T) {
		_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
		prepareInstructionParents(t)
		paths, _ := ResolvePaths()
		_, _, _ = Enroll(repo, "test", "", false)
		if _, err := Apply("claude", "test"); err != nil {
			t.Fatal(err)
		}
		drift, _ := instructionDestination(paths, "claude-global")
		_ = os.WriteFile(drift, []byte("drift"), 0o644)
		unsafe, _ := instructionDestination(paths, "opencode-global")
		target := unsafe + ".target"
		_ = os.WriteFile(target, []byte("unsafe"), 0o644)
		_ = os.Symlink(target, unsafe)
		calls := 0
		result, err := ApplyWithOptions("all", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { calls++; return CollisionReplace, nil }})
		if err != nil || calls != 0 || !blocked(result) {
			t.Fatalf("ineligible resolver called or block lost: calls=%d result=%#v err=%v", calls, result, err)
		}
	})
}

func TestLockedPlanConfirmationRunsAfterChoicesBeforeMutation(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	original := []byte("user original")
	if err := os.WriteFile(destination, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	order := []string{}
	_, err := ApplyWithOptions("claude", "test", ApplyOptions{
		ResolveCollision: func(Action) (CollisionDecision, error) {
			order = append(order, "collision")
			return CollisionReplace, nil
		},
		ConfirmPlan: func(plan PlanResult) error {
			order = append(order, "confirm")
			if got := actionFor(plan, "instruction", "claude-global").Action; got != "replace" {
				t.Fatalf("confirmation did not receive resolved locked plan: %#v", plan)
			}
			return ErrApplyAborted
		},
	})
	if !errors.Is(err, ErrApplyAborted) || strings.Join(order, ",") != "collision,confirm" {
		t.Fatalf("callback order=%v err=%v", order, err)
	}
	if got, readErr := os.ReadFile(destination); readErr != nil || !bytes.Equal(got, original) {
		t.Fatalf("canceled confirmation changed destination: %q %v", got, readErr)
	}
	for _, path := range []string{paths.Receipt, instructionBackup(paths, "claude-global")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("canceled confirmation created %s: %v", path, statErr)
		}
	}
}

func TestLockedPlanChangeAfterConfirmationFailsBeforeMutation(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	confirmed := false
	_, err := ApplyWithOptions("claude", "test", ApplyOptions{ConfirmPlan: func(plan PlanResult) error {
		confirmed = true
		if actionFor(plan, "instruction", "claude-global").Action != "create" {
			t.Fatalf("unexpected locked plan: %#v", plan)
		}
		return os.WriteFile(filepath.Join(repo, "instructions", "claude-global.md"), []byte("changed after display"), 0o644)
	}})
	if err == nil || !confirmed || !strings.Contains(err.Error(), "changed during apply") {
		t.Fatalf("changed exact plan was accepted: confirmed=%v err=%v", confirmed, err)
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("changed exact plan mutated destination: %v", statErr)
	}
	if _, statErr := os.Lstat(paths.Receipt); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("changed exact plan wrote receipt: %v", statErr)
	}
}

func TestLockedPlanConfirmationNotCalledForUnresolvedBlock(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := ApplyWithOptions("claude", "test", ApplyOptions{ConfirmPlan: func(PlanResult) error {
		called = true
		return nil
	}})
	if err != nil || called || !blocked(result) {
		t.Fatalf("blocked plan confirmation: called=%v result=%#v err=%v", called, result, err)
	}
}

func TestInteractiveDecisionRaceFailsBeforeMutation(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	first, _ := instructionDestination(paths, "claude-global")
	second, _ := instructionDestination(paths, "opencode-global")
	_ = os.WriteFile(first, []byte("first"), 0o640)
	_ = os.WriteFile(second, []byte("second"), 0o640)
	_, _, _ = Enroll(repo, "test", "", false)
	calls := 0
	_, err := ApplyWithOptions("all", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) {
		calls++
		if calls == 2 {
			_ = os.WriteFile(first, []byte("raced"), 0o640)
		}
		return CollisionReplace, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "changed during apply") {
		t.Fatalf("race accepted: %v", err)
	}
	if got, _ := os.ReadFile(second); string(got) != "second" {
		t.Fatal("preflight race allowed mutation")
	}
	for _, target := range []string{"claude-global", "opencode-global"} {
		if _, err := os.Lstat(instructionBackup(paths, target)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("preflight race created backup")
		}
	}
}

func TestInteractiveReplacementFailureRollsBackActiveAndBackup(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	original := []byte("original")
	_ = os.WriteFile(destination, original, 0o640)
	_, _, _ = Enroll(repo, "test", "", false)
	beforeReceiptWrite = func() error { return errors.New("forced receipt failure") }
	t.Cleanup(func() { beforeReceiptWrite = nil })
	_, err := ApplyWithOptions("claude", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { return CollisionReplace, nil }})
	if err == nil || !strings.Contains(err.Error(), "forced receipt failure") {
		t.Fatalf("forced failure missing: %v", err)
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) || fileMode(t, destination) != 0o640 {
		t.Fatal("failed replacement did not restore active original")
	}
	if _, err := os.Lstat(instructionBackup(paths, "claude-global")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed replacement did not reconcile new backup")
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed replacement wrote receipt")
	}
}

func TestInteractiveReplacementNeverOverwritesRacingDestination(t *testing.T) {
	tests := []struct {
		name string
		set  func(func(Action) error)
	}{
		{name: "replacement after backup", set: func(h func(Action) error) { afterReplacementBackup = h }},
		{name: "modification after backup", set: func(h func(Action) error) { afterReplacementBackup = h }},
		{name: "recreation before install", set: func(h func(Action) error) { beforeReplacementInstall = h }},
		{name: "replacement after install", set: func(h func(Action) error) { afterReplacementInstall = h }},
		{name: "modification after install", set: func(h func(Action) error) { afterReplacementInstall = h }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := instructionEnvironment(t, "claude-global")
			prepareInstructionParents(t)
			paths, _ := ResolvePaths()
			destination, _ := instructionDestination(paths, "claude-global")
			original := []byte("original before decision")
			newer := []byte("newer racing user bytes")
			if err := os.WriteFile(destination, original, 0o640); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Enroll(repo, "test", "", false); err != nil {
				t.Fatal(err)
			}
			hook := func(Action) error {
				if strings.Contains(tc.name, "replacement") {
					tmp := destination + ".racing"
					if err := os.WriteFile(tmp, newer, 0o640); err != nil {
						return err
					}
					return os.Rename(tmp, destination)
				}
				return os.WriteFile(destination, newer, 0o640)
			}
			tc.set(hook)
			t.Cleanup(func() {
				afterReplacementBackup = nil
				beforeReplacementInstall = nil
				afterReplacementInstall = nil
			})
			_, err := ApplyWithOptions("claude", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { return CollisionReplace, nil }})
			if err == nil {
				t.Fatal("racing replacement unexpectedly succeeded")
			}
			if got, readErr := os.ReadFile(destination); readErr != nil || !bytes.Equal(got, newer) {
				t.Fatalf("newer destination bytes were lost: got=%q err=%v apply=%v", got, readErr, err)
			}
			if _, loadErr := LoadReceipt(paths, Enrollment{}); !errors.Is(loadErr, os.ErrNotExist) {
				t.Fatalf("race falsely recorded ownership: %v", loadErr)
			}
			backup, readErr := os.ReadFile(instructionBackup(paths, "claude-global"))
			if readErr != nil || !bytes.Equal(backup, original) {
				t.Fatalf("original recovery backup was lost: %q %v", backup, readErr)
			}
		})
	}
}

func TestInteractiveReplacementPreservesOpenFDWritesInRecovery(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	original := []byte("original before open fd")
	newer := []byte("newer bytes through retained fd")
	if err := os.WriteFile(destination, original, 0o640); err != nil {
		t.Fatal(err)
	}
	openFile, err := os.OpenFile(destination, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer openFile.Close()
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	beforeReplacementInstall = func(Action) error {
		if err := openFile.Truncate(0); err != nil {
			return err
		}
		if _, err := openFile.Seek(0, 0); err != nil {
			return err
		}
		_, err := openFile.Write(newer)
		return err
	}
	t.Cleanup(func() { beforeReplacementInstall = nil })
	_, err = ApplyWithOptions("claude", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { return CollisionReplace, nil }})
	if err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("open-fd race was not reported with recovery: %v", err)
	}
	recoveries, globErr := filepath.Glob(filepath.Join(filepath.Dir(destination), ".terran-quarantine-*", "displaced"))
	if globErr != nil || len(recoveries) == 0 {
		t.Fatalf("open-fd recovery copy missing: %v %#v", globErr, recoveries)
	}
	found := false
	for _, recovery := range recoveries {
		if data, readErr := os.ReadFile(recovery); readErr == nil && bytes.Equal(data, newer) {
			found = true
		}
	}
	if !found {
		t.Fatalf("newer open-fd bytes were not preserved: %#v", recoveries)
	}
	if _, loadErr := LoadReceipt(paths, Enrollment{}); !errors.Is(loadErr, os.ErrNotExist) {
		t.Fatalf("open-fd race falsely recorded ownership: %v", loadErr)
	}
}

func TestInterruptedReplacementBackupBlocksRestartAdoption(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	source, err := os.ReadFile(filepath.Join(repo, "instructions", "claude-global.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, source, 0o640); err != nil {
		t.Fatal(err)
	}
	backup := instructionBackup(paths, "claude-global")
	if err := ensurePrivateDir(filepath.Dir(backup)); err != nil {
		t.Fatal(err)
	}
	original := []byte("displaced original from interrupted replacement")
	if err := os.WriteFile(backup, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan("claude")
	if err != nil || actionCount(plan, "blocked_collision") != 1 || !strings.Contains(plan.Actions[0].Reason, "interrupted replacement") {
		t.Fatalf("interrupted replacement was not blocked clearly: %#v %v", plan, err)
	}
	called := false
	applied, err := ApplyWithOptions("claude", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) {
		called = true
		return CollisionReplace, nil
	}})
	if err != nil || !blocked(applied) || called {
		t.Fatalf("restart apply did not remain fail-closed: %#v called=%v err=%v", applied, called, err)
	}
	if got, err := os.ReadFile(backup); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("interrupted original backup changed: %q %v", got, err)
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restart apply wrote receipt: %v", err)
	}
}

func TestReplacementTempCleanupFailureRollsBackInstalledFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(string) error
	}{
		{name: "cleanup error", hook: func(string) error { return errors.New("forced temp cleanup failure") }},
		{name: "temp removed by racer", hook: func(path string) error { return os.Remove(path) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := instructionEnvironment(t, "claude-global")
			prepareInstructionParents(t)
			paths, _ := ResolvePaths()
			destination, _ := instructionDestination(paths, "claude-global")
			original := []byte("original before cleanup failure")
			if err := os.WriteFile(destination, original, 0o640); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Enroll(repo, "test", "", false); err != nil {
				t.Fatal(err)
			}
			beforeReplacementCleanup = tc.hook
			t.Cleanup(func() { beforeReplacementCleanup = nil })
			_, err := ApplyWithOptions("claude", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { return CollisionReplace, nil }})
			if err == nil {
				t.Fatal("temporary-link cleanup race unexpectedly succeeded")
			}
			if got, readErr := os.ReadFile(destination); readErr != nil || !bytes.Equal(got, original) {
				t.Fatalf("cleanup failure left falsely unowned managed bytes: %q %v apply=%v", got, readErr, err)
			}
			if _, loadErr := LoadReceipt(paths, Enrollment{}); !errors.Is(loadErr, os.ErrNotExist) {
				t.Fatalf("cleanup failure wrote receipt: %v", loadErr)
			}
		})
	}
}

func TestBackupPublicationRacesPreserveUnexpectedBytes(t *testing.T) {
	t.Run("new backup appears before no-clobber publish", func(t *testing.T) {
		_, repo := instructionEnvironment(t, "claude-global")
		prepareInstructionParents(t)
		paths, _ := ResolvePaths()
		destination, _ := instructionDestination(paths, "claude-global")
		if err := os.WriteFile(destination, []byte("original"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		backup := instructionBackup(paths, "claude-global")
		racing := []byte("newly appeared recovery bytes")
		beforeBackupPublish = func(path string) error { return os.WriteFile(path, racing, 0o600) }
		t.Cleanup(func() { beforeBackupPublish = nil })
		_, err := ApplyWithOptions("claude", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { return CollisionReplace, nil }})
		if err == nil {
			t.Fatal("newly appearing backup was overwritten")
		}
		if got, readErr := os.ReadFile(backup); readErr != nil || !bytes.Equal(got, racing) {
			t.Fatalf("newly appearing backup was changed or removed: %q %v", got, readErr)
		}
	})

	t.Run("published backup changes before rollback", func(t *testing.T) {
		_, repo := instructionEnvironment(t, "claude-global")
		prepareInstructionParents(t)
		paths, _ := ResolvePaths()
		destination, _ := instructionDestination(paths, "claude-global")
		if err := os.WriteFile(destination, []byte("original"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		backup := instructionBackup(paths, "claude-global")
		racing := []byte("changed recovery bytes")
		afterBackupPublication = func(path string) error {
			if err := os.WriteFile(path, racing, 0o600); err != nil {
				return err
			}
			return errors.New("forced failure after backup race")
		}
		t.Cleanup(func() { afterBackupPublication = nil })
		_, err := ApplyWithOptions("claude", "test", ApplyOptions{ResolveCollision: func(Action) (CollisionDecision, error) { return CollisionReplace, nil }})
		if err == nil || !strings.Contains(err.Error(), "preserve changed instruction backup") {
			t.Fatalf("changed backup race was not reported: %v", err)
		}
		if got, readErr := os.ReadFile(backup); readErr != nil || !bytes.Equal(got, racing) {
			t.Fatalf("changed backup was overwritten or removed: %q %v", got, readErr)
		}
	})
}

func TestDecideReplaceBacksUpAndInstallsCatalogVersion(t *testing.T) {
	t.Run("instruction", func(t *testing.T) {
		_, repo := instructionEnvironment(t, "claude-global")
		prepareInstructionParents(t)
		paths, _ := ResolvePaths()
		destination, _ := instructionDestination(paths, "claude-global")
		original := []byte("mine\n")
		if err := os.WriteFile(destination, original, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		result, err := ApplyWithOptions("all", "test", ApplyOptions{Decisions: map[string]CollisionDecision{"instruction/claude-global": CollisionReplace}})
		if err != nil || actionByIDOrFail(t, result, "instruction/claude-global").Action != "replace" {
			t.Fatalf("apply: %#v %v", result, err)
		}
		if got, _ := os.ReadFile(destination); string(got) != "# claude-global\n" {
			t.Fatalf("destination: %q", got)
		}
		if got, _ := os.ReadFile(instructionBackup(paths, "claude-global")); !bytes.Equal(got, original) {
			t.Fatalf("backup: %q", got)
		}
	})
	t.Run("file", func(t *testing.T) {
		_, repo := fileEnvironment(t)
		writeCatalogWithFiles(t, repo, "test-catalog", nil, []FileItem{{Target: "claude-agent", Name: "a.md", Source: "files/a.md"}})
		paths, _ := ResolvePaths()
		destination, _ := managedFileDestination(paths, "file", "claude-agent", "a.md")
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		original := []byte("my agent\n")
		if err := os.WriteFile(destination, original, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplyWithOptions("all", "test", ApplyOptions{Decisions: map[string]CollisionDecision{"file/claude-agent/a.md": CollisionReplace}}); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(destination); string(got) != "# a.md\n" {
			t.Fatalf("destination: %q", got)
		}
		backup := managedBackup(paths, "file", "claude-agent", "a.md")
		if got, _ := os.ReadFile(backup); !bytes.Equal(got, original) {
			t.Fatalf("backup: %q", got)
		}
		if managed := loadTestReceipt(t).Managed; len(managed) != 1 || managed[0].Origin != "adopted" || managed[0].Backup != backup {
			t.Fatalf("receipt: %#v", managed)
		}
	})
	t.Run("json key restores the original on removal", func(t *testing.T) {
		home, repo := fileEnvironment(t)
		settings := claudeSettings(t, home, `{"model":"sonnet","theme":"dark"}`, 0o644)
		writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{"claude-settings": `{"model":"opus"}`})
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		if result, err := Apply("all", "test"); err != nil || !blocked(result) {
			t.Fatalf("undecided collision must block: %#v %v", result, err)
		}
		id := "json-keys/claude-settings/model"
		if _, err := ApplyWithOptions("all", "test", ApplyOptions{Decisions: map[string]CollisionDecision{id: CollisionReplace}}); err != nil {
			t.Fatal(err)
		}
		if got := decodedJSON(t, settings); got["model"] != "opus" || got["theme"] != "dark" {
			t.Fatalf("settings: %#v", got)
		}
		if keys := loadTestReceipt(t).JSONKeys; len(keys) != 1 || keys[0].Origin != "adopted" || string(keys[0].OriginalValue) != `"sonnet"` {
			t.Fatalf("receipt: %#v", keys)
		}
		writeJSONKeysCatalog(t, repo, "test-catalog", nil, map[string]string{})
		if plan, _ := Plan("all"); actionByIDOrFail(t, plan, id).Action != "restore" {
			t.Fatalf("removal plan: %#v", plan)
		}
		if _, err := Apply("all", "test"); err != nil {
			t.Fatal(err)
		}
		if got := decodedJSON(t, settings); got["model"] != "sonnet" || got["theme"] != "dark" {
			t.Fatalf("original value not restored: %#v", got)
		}
		if keys := loadTestReceipt(t).JSONKeys; len(keys) != 0 {
			t.Fatalf("receipt kept restored key: %#v", keys)
		}
	})
	t.Run("skill directory", func(t *testing.T) {
		_, repo := testEnvironment(t)
		paths, _ := ResolvePaths()
		destination, _ := skillDestination(paths, "agents", "example")
		if err := os.MkdirAll(destination, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, "mine.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Enroll(repo, "test", "", false); err != nil {
			t.Fatal(err)
		}
		if result, err := Apply("agents", "test"); err != nil || !blocked(result) {
			t.Fatalf("undecided collision must block: %#v %v", result, err)
		}
		if _, err := ApplyWithOptions("agents", "test", ApplyOptions{Decisions: map[string]CollisionDecision{"skill/agents/example": CollisionReplace}}); err != nil {
			t.Fatal(err)
		}
		source := actionByIDOrFail(t, mustPlan(t, "agents"), "skill/agents/example").Source
		if !skillCopied(destination, source) {
			t.Fatal("catalog skill was not copied")
		}
		if projections := loadTestReceipt(t).Projections; len(projections) != 1 || projections[0].Strategy != "copy" || projections[0].Origin != "created" {
			t.Fatalf("receipt: %#v", projections)
		}
		if got, _ := os.ReadFile(filepath.Join(paths.BackupDir, "skill", "agents", "example", "original", "mine.txt")); string(got) != "mine" {
			t.Fatalf("skill backup: %q", got)
		}
		if plan, _ := Plan("agents"); !plan.Clean {
			t.Fatalf("plan after replace: %#v", plan)
		}
	})
}

func TestDecideReplaceSkillRollsBackWhenLaterStepFails(t *testing.T) {
	_, repo := testEnvironment(t)
	paths, _ := ResolvePaths()
	destination, _ := skillDestination(paths, "agents", "example")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "mine.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	beforeReceiptWrite = func() error { return errors.New("injected receipt failure") }
	t.Cleanup(func() { beforeReceiptWrite = nil })
	if _, err := ApplyWithOptions("agents", "test", ApplyOptions{Decisions: map[string]CollisionDecision{"skill/agents/example": CollisionReplace}}); err == nil {
		t.Fatal("injected failure ignored")
	}
	if info, err := os.Lstat(destination); err != nil || !info.IsDir() {
		t.Fatalf("original skill directory not renamed back: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(destination, "mine.txt")); string(got) != "mine" {
		t.Fatalf("original skill content: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(paths.BackupDir, "skill", "agents", "example", "original")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup left behind: %v", err)
	}
}

func TestDecideReplaceIneligibleSkillExplainsWhy(t *testing.T) {
	_, repo := testEnvironment(t)
	paths, _ := ResolvePaths()
	destination, _ := skillDestination(paths, "agents", "example")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyWithOptions("agents", "test", ApplyOptions{Decisions: map[string]CollisionDecision{"skill/agents/example": CollisionReplace}})
	if action := actionByIDOrFail(t, result, "skill/agents/example"); err != nil || action.Action != "blocked_collision" || action.Reason != "replace not possible: destination is not a directory or symlink" {
		t.Fatalf("apply: %#v %v", action, err)
	}
}

func TestPlanDigestBindsCollidingContent(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	if err := os.WriteFile(destination, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	reviewed := mustPlan(t, "all")
	edited := []byte("mine, edited after review\n")
	if err := os.WriteFile(destination, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyWithOptions("all", "test", ApplyOptions{ExpectDigest: reviewed.Digest, Decisions: map[string]CollisionDecision{"instruction/claude-global": CollisionReplace}})
	if !hasCode(err, CodePlanChanged) {
		t.Fatalf("changed collision content applied against the reviewed digest: %v", err)
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, edited) {
		t.Fatalf("destination replaced: %q", got)
	}
}

func TestDecideKeepHoldsWithoutTouchingDestination(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	destination, _ := instructionDestination(paths, "claude-global")
	original := []byte("mine\n")
	if err := os.WriteFile(destination, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyWithOptions("all", "test", ApplyOptions{Decisions: map[string]CollisionDecision{"instruction/claude-global": CollisionKeep}})
	if err != nil || actionByIDOrFail(t, result, "instruction/claude-global").Action != "held" {
		t.Fatalf("apply: %#v %v", result, err)
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
		t.Fatalf("kept destination changed: %q", got)
	}
	other, _ := instructionDestination(paths, "opencode-global")
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other action not applied: %v", err)
	}
	enrollment, _ := LoadEnrollment(paths)
	if len(enrollment.Holds) != 1 || enrollment.Holds[0] != "instruction/claude-global" {
		t.Fatalf("holds: %v", enrollment.Holds)
	}
	if plan, _ := Plan("all"); !plan.Clean || actionByIDOrFail(t, plan, "instruction/claude-global").Action != "held" {
		t.Fatalf("plan after keep: %#v", plan)
	}
}

func TestDecideForItemOutsideCollisionsIsUsageErrorWithoutMutation(t *testing.T) {
	_, repo := testEnvironment(t)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	paths, _ := ResolvePaths()
	destination, _ := skillDestination(paths, "agents", "example")
	for _, id := range []string{"skill/agents/example", "skill/agents/missing"} {
		_, err := ApplyWithOptions("all", "test", ApplyOptions{Decisions: map[string]CollisionDecision{id: CollisionKeep}})
		if !hasCode(err, CodeUsage) || !strings.Contains(err.Error(), id) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected decision mutated: %v", err)
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected decision wrote a receipt: %v", err)
	}
}

func TestDecisionsArePreflightedTogether(t *testing.T) {
	_, repo := instructionEnvironment(t, "claude-global", "opencode-global")
	prepareInstructionParents(t)
	paths, _ := ResolvePaths()
	first, _ := instructionDestination(paths, "claude-global")
	second, _ := instructionDestination(paths, "opencode-global")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("mine\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyWithOptions("all", "test", ApplyOptions{
		Decisions: map[string]CollisionDecision{"instruction/claude-global": CollisionReplace, "instruction/opencode-global": CollisionReplace},
		ConfirmPlan: func(PlanResult) error {
			return os.WriteFile(second, []byte("changed after decision\n"), 0o640)
		},
	})
	if err == nil {
		t.Fatal("second decision must fail preflight")
	}
	if got, _ := os.ReadFile(first); string(got) != "mine\n" {
		t.Fatalf("first decision applied despite second failing: %q", got)
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt written: %v", err)
	}
}

func TestPlanDigestAndExpect(t *testing.T) {
	_, repo := testEnvironment(t)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	first, err := Plan("all")
	if err != nil || len(first.Digest) != 64 {
		t.Fatalf("digest: %q %v", first.Digest, err)
	}
	if second, _ := Plan("all"); second.Digest != first.Digest {
		t.Fatal("digest is not stable across plans")
	}
	paths, _ := ResolvePaths()
	destination, _ := skillDestination(paths, "agents", "example")
	if _, err := ApplyWithOptions("all", "test", ApplyOptions{ExpectDigest: "0000"}); !hasCode(err, CodePlanChanged) {
		t.Fatalf("stale digest: %v", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale digest mutated: %v", err)
	}
	if _, err := os.Lstat(paths.Receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale digest wrote a receipt: %v", err)
	}
	result, err := ApplyWithOptions("all", "test", ApplyOptions{ExpectDigest: first.Digest})
	if err != nil || result.Digest != first.Digest || !skillCopied(destination, actionByIDOrFail(t, first, "skill/agents/example").Source) {
		t.Fatalf("matching digest: %#v %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skills", "example", "SKILL.md"), []byte("---\nname: example\ndescription: changed\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, _ := Plan("all"); changed.Digest == first.Digest {
		t.Fatal("digest did not change with the catalog source")
	}
}

func TestPlanDigestBindsSourceContentWhenActionsMatch(t *testing.T) {
	_, repo := testEnvironment(t)
	if _, _, err := Enroll(repo, "test", "", false); err != nil {
		t.Fatal(err)
	}
	reviewed := mustPlan(t, "all")
	if err := os.WriteFile(filepath.Join(repo, "skills", "example", "SKILL.md"), []byte("---\nname: example\ndescription: swapped after review\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := mustPlan(t, "all")
	if actionByIDOrFail(t, changed, "skill/agents/example").Action != actionByIDOrFail(t, reviewed, "skill/agents/example").Action {
		t.Fatal("test setup: the action itself changed")
	}
	if changed.Digest == reviewed.Digest {
		t.Fatal("digest did not change when source content changed under an identical plan")
	}
	paths, _ := ResolvePaths()
	destination, _ := skillDestination(paths, "agents", "example")
	if _, err := ApplyWithOptions("all", "test", ApplyOptions{ExpectDigest: reviewed.Digest}); !hasCode(err, CodePlanChanged) {
		t.Fatalf("stale source applied: %v", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale source mutated destination: %v", err)
	}
}

func mustPlan(t *testing.T, target string) PlanResult {
	t.Helper()
	plan, err := Plan(target)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
