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
			if _, _, err := Enroll(repo, "test", false); err != nil {
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
			receipt, err := LoadReceipt(paths)
			if err != nil {
				t.Fatal(err)
			}
			var managed ReceiptInstruction
			if tc.kind == "config" {
				managed = ReceiptInstruction(receipt.Configs[0])
			} else {
				managed = receipt.Instructions[0]
			}
			if managed.Origin != "adopted" || managed.OriginalHash != hashBytes(original) || managed.OriginalMode != 0o640 || managed.Backup != backup {
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
	if _, _, err := Enroll(repo, "test", false); err != nil {
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
	receipt, err := LoadReceipt(paths)
	if err != nil || len(receipt.Instructions) != 1 || receipt.Instructions[0].Target != "opencode-global" {
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
		_, _, _ = Enroll(repo, "test", false)
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
		_, _, _ = Enroll(repo, "test", false)
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
	if _, _, err := Enroll(repo, "test", false); err != nil {
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
	if _, _, err := Enroll(repo, "test", false); err != nil {
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
	if _, _, err := Enroll(repo, "test", false); err != nil {
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
	_, _, _ = Enroll(repo, "test", false)
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
	_, _, _ = Enroll(repo, "test", false)
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
			if _, _, err := Enroll(repo, "test", false); err != nil {
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
			if _, loadErr := LoadReceipt(paths); !errors.Is(loadErr, os.ErrNotExist) {
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
	if _, _, err := Enroll(repo, "test", false); err != nil {
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
	if _, loadErr := LoadReceipt(paths); !errors.Is(loadErr, os.ErrNotExist) {
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
	if _, _, err := Enroll(repo, "test", false); err != nil {
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
			if _, _, err := Enroll(repo, "test", false); err != nil {
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
			if _, loadErr := LoadReceipt(paths); !errors.Is(loadErr, os.ErrNotExist) {
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
		if _, _, err := Enroll(repo, "test", false); err != nil {
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
		if _, _, err := Enroll(repo, "test", false); err != nil {
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
