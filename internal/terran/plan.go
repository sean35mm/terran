package terran

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

func validateTarget(target string) error {
	return ValidateTarget(target)
}

func selectedSkill(filter, target string) bool {
	spec, ok := lookupTarget("skill", target)
	return filter == "all" || (ok && spec.Group == filter)
}

func selectedInstruction(filter, target string) bool {
	spec, ok := lookupTarget("instruction", target)
	return filter == "all" || (ok && spec.Group == filter)
}

func selectedConfig(filter, target string) bool {
	spec, ok := lookupTarget("config", target)
	return filter == "all" || (ok && spec.Group == filter)
}

func LoadReceipt(paths Paths) (receipt Receipt, err error) {
	defer func() {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			err = Coded(CodeReceiptInvalid, nextState, err)
		}
	}()
	data, _, err := readTrustedFile(paths.Receipt, "receipt", 4<<20, 0o600)
	if err != nil {
		return Receipt{}, err
	}
	receipt, err = decodeReceipt(data)
	if err != nil {
		return Receipt{}, err
	}
	if receipt.SchemaVersion != SchemaVersion || receipt.RepositoryID == "" || !filepath.IsAbs(receipt.RepositoryPath) || filepath.Clean(receipt.RepositoryPath) != receipt.RepositoryPath {
		return Receipt{}, fmt.Errorf("invalid receipt repository")
	}
	seen := map[string]bool{}
	for _, p := range receipt.Projections {
		if p.Catalog != receipt.RepositoryID || !skillNamePattern.MatchString(p.Skill) || p.Strategy != "symlink" || !filepath.IsAbs(p.Source) {
			return Receipt{}, fmt.Errorf("invalid receipt projection")
		}
		if _, ok := lookupTarget("skill", p.Target); !ok {
			return Receipt{}, fmt.Errorf("invalid receipt projection")
		}
		destination, destinationErr := skillDestination(paths, p.Target, p.Skill)
		if destinationErr != nil || p.Destination != destination {
			return Receipt{}, fmt.Errorf("unsafe receipt destination for %s/%s", p.Skill, p.Target)
		}
		if filepath.Clean(p.Source) != p.Source || !contained(receipt.RepositoryPath, p.Source) {
			return Receipt{}, fmt.Errorf("unsafe receipt source for %s/%s", p.Skill, p.Target)
		}
		if canonical, err := filepath.EvalSymlinks(p.Source); err == nil && canonical != p.Source {
			return Receipt{}, fmt.Errorf("unsafe receipt source for %s/%s", p.Skill, p.Target)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Receipt{}, fmt.Errorf("unsafe receipt source for %s/%s", p.Skill, p.Target)
		}
		key := pairKey(p.Skill, p.Target)
		if seen[key] {
			return Receipt{}, fmt.Errorf("duplicate receipt projection")
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for _, managed := range receipt.Managed {
		if (managed.Kind != "instruction" && managed.Kind != "config") || managed.Catalog != receipt.RepositoryID || managed.Name != "" {
			return Receipt{}, fmt.Errorf("invalid receipt managed entry")
		}
		destination, err := managedFileDestination(paths, managed.Kind, managed.Target)
		if err != nil || managed.Strategy != "copy" || !validHash(managed.SourceHash) || !validHash(managed.AppliedHash) {
			return Receipt{}, fmt.Errorf("invalid receipt %s", managed.Kind)
		}
		key := pairKey(managed.Kind, managed.Target)
		if seen[key] {
			return Receipt{}, fmt.Errorf("duplicate receipt %s", managed.Kind)
		}
		seen[key] = true
		if managed.Destination != destination || !filepath.IsAbs(managed.Source) || filepath.Clean(managed.Source) != managed.Source || !contained(receipt.RepositoryPath, managed.Source) {
			return Receipt{}, fmt.Errorf("unsafe receipt %s paths for %s", managed.Kind, managed.Target)
		}
		switch managed.Origin {
		case "created":
			if managed.OriginalHash != "" || managed.OriginalMode != 0 || managed.Backup != "" {
				return Receipt{}, fmt.Errorf("invalid created %s receipt for %s", managed.Kind, managed.Target)
			}
		case "adopted":
			if !validHash(managed.OriginalHash) || managed.OriginalMode&0o022 != 0 || managed.OriginalMode&^0o777 != 0 || managed.Backup != instructionBackup(paths, managed.Target) {
				return Receipt{}, fmt.Errorf("invalid adopted %s receipt for %s", managed.Kind, managed.Target)
			}
		default:
			return Receipt{}, fmt.Errorf("invalid %s origin for %s", managed.Kind, managed.Target)
		}
	}
	return receipt, nil
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func Plan(target string) (PlanResult, error) {
	if err := validateTarget(target); err != nil {
		return PlanResult{}, err
	}
	paths, err := ResolvePaths()
	if err != nil {
		return PlanResult{}, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return PlanResult{}, fmt.Errorf("load enrollment: %w", err)
	}
	return planEnrolled(paths, enrollment, target)
}

func planEnrolled(paths Paths, enrollment Enrollment, target string) (PlanResult, error) {
	loaded, err := LoadManifest(enrollment.RepositoryPath)
	if err != nil {
		return PlanResult{}, err
	}
	if loaded.Manifest.ID != enrollment.RepositoryID {
		return PlanResult{}, Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("enrolled repository id changed"))
	}
	receipt, err := LoadReceipt(paths)
	if errors.Is(err, os.ErrNotExist) {
		receipt = Receipt{}
	} else if err != nil {
		return PlanResult{}, fmt.Errorf("load receipt: %w", err)
	} else if receipt.RepositoryID != enrollment.RepositoryID || receipt.RepositoryPath != loaded.Repository {
		return PlanResult{}, Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("receipt repository differs from enrollment"))
	}
	return makePlan(paths, loaded, receipt, enrollment.Holds, target)
}

// currentPlatform is a variable so tests can simulate another platform.
var currentPlatform = runtime.GOOS

// platformIncluded reports whether an item applies on this platform; no
// platforms means every platform.
func platformIncluded(platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, platform := range platforms {
		if platform == currentPlatform {
			return true
		}
	}
	return false
}

// inert actions never touch the machine: the item is held or not for this platform.
func inert(action Action) bool { return action.Action == "held" || action.Action == "excluded" }

func makePlan(paths Paths, loaded LoadedManifest, receipt Receipt, holds []string, filter string) (PlanResult, error) {
	held := make(map[string]bool, len(holds))
	for _, id := range holds {
		held[id] = true
	}
	var actions []Action
	// add classifies an item unless the machine holds it; held items are never inspected.
	add := func(action Action, classify func() (string, string)) {
		if held[action.ID] {
			action.Action, action.Reason = "held", "held on this machine"
		} else {
			action.Action, action.Reason = classify()
		}
		actions = append(actions, action)
	}
	excluded := func(platforms []string) func() (string, string) {
		return func() (string, string) { return "excluded", strings.Join(platforms, ",") + "-only" }
	}
	ownedSkills := map[string]ReceiptProjection{}
	for _, projection := range receipt.Projections {
		ownedSkills[pairKey(projection.Skill, projection.Target)] = projection
	}
	ownedInstructions := map[string]ReceiptManaged{}
	ownedConfigs := map[string]ReceiptManaged{}
	for _, managed := range receipt.Managed {
		switch managed.Kind {
		case "instruction":
			ownedInstructions[managed.Target] = managed
		case "config":
			ownedConfigs[managed.Target] = managed
		}
	}
	desiredSkills := map[string]bool{}
	for _, projection := range loaded.Manifest.Projections {
		for _, target := range projection.Targets {
			if !selectedSkill(filter, target) {
				continue
			}
			destination, _ := skillDestination(paths, target, projection.Skill)
			action := Action{ID: ItemID("skill", target, projection.Skill), Kind: "skill", Skill: projection.Skill, Target: target, Source: loaded.Sources[projection.Skill], Destination: destination}
			key := pairKey(projection.Skill, target)
			prior, owned := ownedSkills[key]
			if !platformIncluded(projection.Platforms) {
				// An owned projection falls through to the removal loop below.
				if !owned {
					add(action, excluded(projection.Platforms))
				}
				continue
			}
			desiredSkills[key] = true
			add(action, func() (string, string) {
				return classifyLeaf(filepath.Dir(action.Destination), action.Destination, action.Source, prior, owned)
			})
		}
	}
	for key, prior := range ownedSkills {
		if !selectedSkill(filter, prior.Target) || desiredSkills[key] {
			continue
		}
		destination, _ := skillDestination(paths, prior.Target, prior.Skill)
		action := Action{ID: ItemID("skill", prior.Target, prior.Skill), Kind: "skill", Skill: prior.Skill, Target: prior.Target, Source: prior.Source, Destination: destination}
		add(action, func() (string, string) {
			if exactSymlink(action.Destination, prior.Source) {
				return "remove", ""
			}
			return "blocked_drift", "receipt-owned projection is missing or changed"
		})
	}
	desiredInstructions := map[string]bool{}
	for _, instruction := range loaded.Manifest.Instructions {
		if !selectedInstruction(filter, instruction.Target) {
			continue
		}
		destination, _ := instructionDestination(paths, instruction.Target)
		if contained(loaded.Repository, destination) {
			return PlanResult{}, fmt.Errorf("instruction destination for %s must not be inside the repository", instruction.Target)
		}
		action := Action{ID: ItemID("instruction", instruction.Target, ""), Kind: "instruction", Target: instruction.Target, Source: loaded.InstructionSources[instruction.Target], Destination: destination}
		prior, owned := ownedInstructions[instruction.Target]
		if !platformIncluded(instruction.Platforms) {
			if !owned {
				add(action, excluded(instruction.Platforms))
			}
			continue
		}
		desiredInstructions[instruction.Target] = true
		add(action, func() (string, string) {
			return classifyInstruction(paths, action, loaded.InstructionHashes[instruction.Target], prior, owned)
		})
	}
	for _, prior := range ownedInstructions {
		if !selectedInstruction(filter, prior.Target) || desiredInstructions[prior.Target] {
			continue
		}
		destination, _ := instructionDestination(paths, prior.Target)
		action := Action{ID: ItemID("instruction", prior.Target, ""), Kind: "instruction", Target: prior.Target, Source: prior.Source, Destination: destination}
		add(action, func() (string, string) { return classifyInstructionRemoval(paths, prior, destination) })
	}
	desiredConfigs := map[string]bool{}
	for _, config := range loaded.Manifest.Configs {
		if !selectedConfig(filter, config.Target) {
			continue
		}
		destination, _ := configDestination(paths, config.Target)
		if contained(loaded.Repository, destination) {
			return PlanResult{}, fmt.Errorf("config destination for %s must not be inside the repository", config.Target)
		}
		action := Action{ID: ItemID("config", config.Target, ""), Kind: "config", Target: config.Target, Source: loaded.ConfigSources[config.Target], Destination: destination}
		prior, owned := ownedConfigs[config.Target]
		if !platformIncluded(config.Platforms) {
			if !owned {
				add(action, excluded(config.Platforms))
			}
			continue
		}
		desiredConfigs[config.Target] = true
		add(action, func() (string, string) {
			return classifyInstruction(paths, action, loaded.ConfigHashes[config.Target], prior, owned)
		})
	}
	for _, prior := range ownedConfigs {
		if !selectedConfig(filter, prior.Target) || desiredConfigs[prior.Target] {
			continue
		}
		destination, _ := configDestination(paths, prior.Target)
		action := Action{ID: ItemID("config", prior.Target, ""), Kind: "config", Target: prior.Target, Source: prior.Source, Destination: destination}
		add(action, func() (string, string) { return classifyInstructionRemoval(paths, prior, destination) })
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Kind != actions[j].Kind {
			return actions[i].Kind < actions[j].Kind
		}
		if actions[i].Target != actions[j].Target {
			return actions[i].Target < actions[j].Target
		}
		return actions[i].Skill < actions[j].Skill
	})
	clean := true
	for _, action := range actions {
		if action.Action != "noop" && !inert(action) {
			clean = false
		}
	}
	return PlanResult{SchemaVersion: SchemaVersion, Clean: clean, Actions: actions}, nil
}

func classifyInstruction(paths Paths, action Action, sourceHash string, prior ReceiptManaged, owned bool) (string, string) {
	parent := filepath.Dir(action.Destination)
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		if owned {
			return "blocked_drift", "receipt-owned instruction parent is missing"
		}
		if err := validateProspectiveInstructionParent(parent); err != nil {
			return "blocked_collision", err.Error()
		}
		return "create", "instruction target parent will be created"
	} else if err != nil {
		return "blocked_collision", err.Error()
	} else if err := validateInstructionParent(action.Destination); err != nil {
		if owned {
			return "blocked_drift", err.Error()
		}
		return "blocked_collision", err.Error()
	}
	if owned {
		if err := validateTrustedFile(action.Destination, "managed instruction"); err != nil {
			return "blocked_drift", "receipt-owned instruction is missing or unsafe: " + err.Error()
		}
		hash, err := fileHash(action.Destination)
		if err != nil || hash != prior.AppliedHash {
			return "blocked_drift", "receipt-owned instruction is missing or changed"
		}
		if sourceHash == prior.SourceHash {
			return "noop", ""
		}
		return "update", "instruction source changed"
	}
	if _, err := os.Lstat(action.Destination); errors.Is(err, os.ErrNotExist) {
		return "create", ""
	} else if err != nil {
		return "blocked_collision", err.Error()
	}
	if err := validateTrustedFile(action.Destination, "instruction destination"); err != nil {
		return "blocked_collision", err.Error()
	}
	hash, err := fileHash(action.Destination)
	if err != nil {
		return "blocked_collision", err.Error()
	}
	if hash != sourceHash {
		return "blocked_collision", "instruction destination differs from source"
	}
	backup := instructionBackup(paths, action.Target)
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return "blocked_collision", err.Error()
		}
		if err := validateUnreferencedBackup(backup); err != nil {
			return "blocked_collision", "instruction adoption backup already exists and is unsafe: " + err.Error()
		}
		backupData, _, err := readSafeFile(backup, "unreferenced instruction backup")
		if err != nil {
			return "blocked_collision", "cannot validate unreferenced instruction backup: " + err.Error()
		}
		if hashBytes(backupData) != sourceHash {
			return "blocked_collision", "unreferenced backup differs from the exact active file; possible interrupted replacement requires manual recovery"
		}
		return "adopt", "existing exact regular file; reuse exact unreferenced backup"
	}
	return "adopt", "existing exact regular file"
}

func validateProspectiveInstructionParent(parent string) error {
	current := parent
	for {
		_, err := os.Lstat(current)
		if err == nil {
			return validateTrustedDirectory(current, "instruction target ancestor")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(current)
		if next == current {
			return fmt.Errorf("instruction target has no existing trusted ancestor")
		}
		current = next
	}
}

func classifyInstructionRemoval(paths Paths, prior ReceiptManaged, destination string) (string, string) {
	if err := validateInstructionParent(destination); err != nil {
		return "blocked_drift", err.Error()
	}
	if err := validateTrustedFile(destination, "managed instruction"); err != nil {
		return "blocked_drift", "receipt-owned instruction is missing or unsafe: " + err.Error()
	}
	hash, err := fileHash(destination)
	if err != nil || hash != prior.AppliedHash {
		return "blocked_drift", "receipt-owned instruction is missing or changed"
	}
	if prior.Origin == "created" {
		return "remove", "created instruction is no longer in manifest"
	}
	if err := validateBackup(paths, prior); err != nil {
		return "blocked_drift", err.Error()
	}
	return "restore", "adopted instruction is no longer in manifest"
}

func validateBackup(paths Paths, prior ReceiptManaged) error {
	backup := instructionBackup(paths, prior.Target)
	if prior.Backup != backup {
		return fmt.Errorf("instruction backup path does not match fixed target")
	}
	if err := validateTrustedFile(backup, "instruction backup"); err != nil {
		return fmt.Errorf("instruction backup is missing or unsafe: %w", err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("instruction backup must be mode 0600")
	}
	hash, err := fileHash(backup)
	if err != nil || hash != prior.OriginalHash {
		return fmt.Errorf("instruction backup hash mismatch")
	}
	return nil
}

func classifyLeaf(root, destination, desired string, prior ReceiptProjection, owned bool) (string, string) {
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if owned {
			return "blocked_drift", "receipt-owned target root is missing"
		}
		return "create", "target root will be created"
	}
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "blocked_collision", "target root is not a real directory"
	}
	if err := validateTargetRoot(root); err != nil {
		return "blocked_collision", err.Error()
	}
	if owned {
		if !exactSymlink(destination, prior.Source) {
			return "blocked_drift", "receipt-owned projection is missing or changed"
		}
		if prior.Source == desired {
			return "noop", ""
		}
		return "replace", "manifest source changed"
	}
	_, err = os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return "create", ""
	}
	if err != nil {
		return "blocked_collision", err.Error()
	}
	if exactSymlink(destination, desired) {
		return "adopt", "existing exact symlink"
	}
	return "blocked_collision", "destination exists and is not safely owned"
}

func exactSymlink(path, expected string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	link, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	link = filepath.Clean(link)
	resolved, err := filepath.EvalSymlinks(link)
	if err == nil {
		return resolved == expected
	}
	return errors.Is(err, os.ErrNotExist) && link == expected
}

func pairKey(skill, target string) string { return skill + "\x00" + target }

func blocked(plan PlanResult) bool {
	for _, action := range plan.Actions {
		if action.Action == "blocked_collision" || action.Action == "blocked_drift" {
			return true
		}
	}
	return false
}

var (
	beforeInstructionMutation func(Action) error
	beforeReceiptWrite        func() error
	afterInstructionRename    func(string) error
	afterInstructionHash      func(string) error
	afterReceiptRename        func() error
	afterReplacementBackup    func(Action) error
	beforeReplacementInstall  func(Action) error
	afterReplacementInstall   func(Action) error
	beforeReplacementCleanup  func(string) error
	beforeBackupPublish       func(string) error
	afterBackupPublication    func(string) error
	restoreReceiptFile        = restoreReceiptSnapshot
	removeInstructionBackup   = safelyRemoveInstructionBackup
)

var ErrApplyAborted = errors.New("apply aborted by user")

type resolvedCollision struct {
	destinationInfo os.FileInfo
	originalHash    string
	originalMode    os.FileMode
	backup          string
	backupInfo      os.FileInfo
	backupHash      string
}

func Apply(target, buildVersion string) (PlanResult, error) {
	return ApplyWithOptions(target, buildVersion, ApplyOptions{})
}

func ApplyWithOptions(target, buildVersion string, options ApplyOptions) (PlanResult, error) {
	if err := validateTarget(target); err != nil {
		return PlanResult{}, err
	}
	paths, err := ResolvePaths()
	if err != nil {
		return PlanResult{}, err
	}
	var result PlanResult
	err = withLock(paths.Lock, func() error {
		enrollment, err := LoadEnrollment(paths)
		if err != nil {
			return err
		}
		loaded, err := LoadManifest(enrollment.RepositoryPath)
		if err != nil {
			return err
		}
		if loaded.Manifest.ID != enrollment.RepositoryID {
			return Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("enrolled repository id changed"))
		}
		receipt, err := LoadReceipt(paths)
		if errors.Is(err, os.ErrNotExist) {
			receipt = Receipt{}
		} else if err != nil {
			return err
		} else if receipt.RepositoryID != enrollment.RepositoryID || receipt.RepositoryPath != loaded.Repository {
			return Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("receipt repository differs from enrollment"))
		}
		result, err = makePlan(paths, loaded, receipt, enrollment.Holds, target)
		if err != nil {
			return err
		}
		resolved := map[string]resolvedCollision{}
		for i := range result.Actions {
			action := result.Actions[i]
			if action.Action != "blocked_collision" || options.ResolveCollision == nil {
				continue
			}
			state, eligible := resolvableCollision(paths, loaded, action)
			if !eligible {
				continue
			}
			decision, err := options.ResolveCollision(action)
			if err != nil {
				return fmt.Errorf("resolve collision for %s: %w", action.Target, err)
			}
			switch decision {
			case CollisionReplace:
				result.Actions[i].Action = "replace"
				result.Actions[i].Reason = "replace differing existing file; preserve original in private backup"
			case CollisionSkip:
				result.Actions[i].Action = "skip"
				result.Actions[i].Reason = "keep differing existing file"
			case CollisionAbort:
				return ErrApplyAborted
			default:
				return fmt.Errorf("resolver returned invalid decision %q for %s", decision, action.Target)
			}
			resolved[managedActionKey(action)] = state
		}
		if blocked(result) {
			return nil
		}
		if options.ConfirmPlan != nil {
			if err := options.ConfirmPlan(result); err != nil {
				return fmt.Errorf("confirm plan: %w", err)
			}
		}
		if err := revalidateLoadedManifest(loaded); err != nil {
			return fmt.Errorf("revalidate catalog: %w", err)
		}
		ownedSkills := map[string]ReceiptProjection{}
		for _, projection := range receipt.Projections {
			ownedSkills[pairKey(projection.Skill, projection.Target)] = projection
		}
		ownedInstructions := map[string]ReceiptManaged{}
		for _, managed := range receipt.Managed {
			ownedInstructions[managed.Target] = managed
		}
		for _, action := range result.Actions {
			if err := preflightAction(paths, loaded, action, ownedSkills, ownedInstructions, resolved); err != nil {
				return err
			}
		}
		var skillRollbacks []skillRollback
		for _, action := range result.Actions {
			if action.Kind != "skill" || inert(action) {
				continue
			}
			if err := preflightAction(paths, loaded, action, ownedSkills, ownedInstructions, resolved); err != nil {
				return errors.Join(err, rollbackSkills(skillRollbacks))
			}
			rollback := prepareSkillRollback(action, ownedSkills)
			skillRollbacks = append(skillRollbacks, rollback)
			if err := mutateSkill(paths, loaded, action, ownedSkills); err != nil {
				return errors.Join(err, rollbackSkills(skillRollbacks))
			}
		}
		var rollbacks []instructionRollback
		verifiedInstructionHashes := map[string]string{}
		for _, action := range result.Actions {
			if action.Kind == "skill" || action.Action == "noop" || action.Action == "skip" || inert(action) {
				continue
			}
			if err := preflightAction(paths, loaded, action, ownedSkills, ownedInstructions, resolved); err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			if beforeInstructionMutation != nil {
				if err := beforeInstructionMutation(action); err != nil {
					return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
				}
			}
			var sourceBytes []byte
			if expectedHash, desired := managedSourceHash(loaded, action.Kind, action.Target); desired {
				sourceBytes, err = readVerifiedManagedSource(loaded.Repository, action.Kind, action.Target, action.Source, expectedHash)
				if err != nil {
					rollbackErr := rollbackAll(rollbacks, skillRollbacks)
					return errors.Join(err, rollbackErr)
				}
				verifiedInstructionHashes[action.Target] = hashBytes(sourceBytes)
			}
			state, hasResolvedState := resolved[managedActionKey(action)]
			rollback, err := mutateInstruction(paths, loaded, action, ownedInstructions[action.Target], sourceBytes, state, hasResolvedState)
			rollbacks = append(rollbacks, rollback)
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		if err := revalidateLoadedManifest(loaded); err != nil {
			return errors.Join(fmt.Errorf("revalidate catalog before receipt: %w", err), rollbackAll(rollbacks, skillRollbacks))
		}
		// Held items keep their receipt entries untouched; platform-excluded
		// items get none (an owned one was just removed).
		heldIDs := map[string]bool{}
		for _, action := range result.Actions {
			if action.Action == "held" {
				heldIDs[action.ID] = true
			}
		}
		untouched := func(kind, target, name string, platforms []string) bool {
			return !platformIncluded(platforms) || heldIDs[ItemID(kind, target, name)]
		}
		for _, instruction := range loaded.Manifest.Instructions {
			if !selectedInstruction(target, instruction.Target) || untouched("instruction", instruction.Target, "", instruction.Platforms) {
				continue
			}
			if actionFor(result, "instruction", instruction.Target).Action == "skip" {
				continue
			}
			data, err := readVerifiedInstructionSource(loaded.Repository, instruction.Target, loaded.InstructionSources[instruction.Target], loaded.InstructionHashes[instruction.Target])
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			verifiedInstructionHashes[instruction.Target] = hashBytes(data)
		}
		for _, config := range loaded.Manifest.Configs {
			if !selectedConfig(target, config.Target) || untouched("config", config.Target, "", config.Platforms) {
				continue
			}
			data, err := readVerifiedManagedSource(loaded.Repository, "config", config.Target, loaded.ConfigSources[config.Target], loaded.ConfigHashes[config.Target])
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			verifiedInstructionHashes[config.Target] = hashBytes(data)
		}
		now := time.Now().UTC()
		newReceipt := Receipt{SchemaVersion: SchemaVersion, RepositoryID: enrollment.RepositoryID, RepositoryPath: enrollment.RepositoryPath, RepositoryVersion: loaded.Manifest.Version, ManifestFingerprint: loaded.Fingerprint}
		for _, old := range receipt.Projections {
			if !selectedSkill(target, old.Target) || heldIDs[ItemID("skill", old.Target, old.Skill)] {
				newReceipt.Projections = append(newReceipt.Projections, old)
			}
		}
		for _, projection := range loaded.Manifest.Projections {
			for _, destinationTarget := range projection.Targets {
				if !selectedSkill(target, destinationTarget) || untouched("skill", destinationTarget, projection.Skill, projection.Platforms) {
					continue
				}
				destination, _ := skillDestination(paths, destinationTarget, projection.Skill)
				newReceipt.Projections = append(newReceipt.Projections, ReceiptProjection{Catalog: loaded.Manifest.ID, Skill: projection.Skill, Target: destinationTarget, Source: loaded.Sources[projection.Skill], Destination: destination, Strategy: "symlink", AppliedAt: now, TerranBuildVersion: buildVersion})
			}
		}
		for _, old := range receipt.Managed {
			if (old.Kind == "instruction" && !selectedInstruction(target, old.Target)) || (old.Kind == "config" && !selectedConfig(target, old.Target)) || heldIDs[ItemID(old.Kind, old.Target, "")] {
				newReceipt.Managed = append(newReceipt.Managed, old)
			}
		}
		for _, instruction := range loaded.Manifest.Instructions {
			if !selectedInstruction(target, instruction.Target) || untouched("instruction", instruction.Target, "", instruction.Platforms) {
				continue
			}
			if actionFor(result, "instruction", instruction.Target).Action == "skip" {
				continue
			}
			prior, owned := ownedInstructions[instruction.Target]
			origin := "created"
			var originalHash, backup string
			var originalMode uint32
			if owned {
				origin, originalHash, originalMode, backup = prior.Origin, prior.OriginalHash, prior.OriginalMode, prior.Backup
			} else if action := actionFor(result, "instruction", instruction.Target); action.Action == "adopt" {
				origin = "adopted"
				destination, _ := instructionDestination(paths, instruction.Target)
				info, _ := os.Stat(destination)
				originalHash = loaded.InstructionHashes[instruction.Target]
				originalMode = uint32(info.Mode().Perm())
				backup = instructionBackup(paths, instruction.Target)
			} else if action.Action == "replace" {
				state := resolved[managedActionKey(action)]
				origin = "adopted"
				originalHash = state.originalHash
				originalMode = uint32(state.originalMode.Perm())
				backup = state.backup
			}
			destination, _ := instructionDestination(paths, instruction.Target)
			sourceHash := verifiedInstructionHashes[instruction.Target]
			if sourceHash == "" {
				sourceHash = loaded.InstructionHashes[instruction.Target]
			}
			newReceipt.Managed = append(newReceipt.Managed, ReceiptManaged{Kind: "instruction", Catalog: loaded.Manifest.ID, Target: instruction.Target, Source: loaded.InstructionSources[instruction.Target], Destination: destination, Strategy: "copy", SourceHash: sourceHash, AppliedHash: sourceHash, Origin: origin, OriginalHash: originalHash, OriginalMode: originalMode, Backup: backup, AppliedAt: now, TerranBuildVersion: buildVersion})
		}
		for _, config := range loaded.Manifest.Configs {
			if !selectedConfig(target, config.Target) || untouched("config", config.Target, "", config.Platforms) {
				continue
			}
			if actionFor(result, "config", config.Target).Action == "skip" {
				continue
			}
			prior, owned := ownedInstructions[config.Target]
			origin := "created"
			var originalHash, backup string
			var originalMode uint32
			if owned {
				origin, originalHash, originalMode, backup = prior.Origin, prior.OriginalHash, prior.OriginalMode, prior.Backup
			} else if action := actionFor(result, "config", config.Target); action.Action == "adopt" {
				origin = "adopted"
				destination, _ := configDestination(paths, config.Target)
				info, _ := os.Stat(destination)
				originalHash = loaded.ConfigHashes[config.Target]
				originalMode = uint32(info.Mode().Perm())
				backup = instructionBackup(paths, config.Target)
			} else if action.Action == "replace" {
				state := resolved[managedActionKey(action)]
				origin = "adopted"
				originalHash = state.originalHash
				originalMode = uint32(state.originalMode.Perm())
				backup = state.backup
			}
			destination, _ := configDestination(paths, config.Target)
			sourceHash := verifiedInstructionHashes[config.Target]
			if sourceHash == "" {
				sourceHash = loaded.ConfigHashes[config.Target]
			}
			newReceipt.Managed = append(newReceipt.Managed, ReceiptManaged{Kind: "config", Catalog: loaded.Manifest.ID, Target: config.Target, Source: loaded.ConfigSources[config.Target], Destination: destination, Strategy: "copy", SourceHash: sourceHash, AppliedHash: sourceHash, Origin: origin, OriginalHash: originalHash, OriginalMode: originalMode, Backup: backup, AppliedAt: now, TerranBuildVersion: buildVersion})
		}
		sort.Slice(newReceipt.Projections, func(i, j int) bool {
			return pairKey(newReceipt.Projections[i].Skill, newReceipt.Projections[i].Target) < pairKey(newReceipt.Projections[j].Skill, newReceipt.Projections[j].Target)
		})
		sort.Slice(newReceipt.Managed, func(i, j int) bool {
			return pairKey(newReceipt.Managed[i].Kind, newReceipt.Managed[i].Target) < pairKey(newReceipt.Managed[j].Kind, newReceipt.Managed[j].Target)
		})
		priorReceipt, _, priorReceiptErr := readTrustedFile(paths.Receipt, "receipt", 4<<20, 0o600)
		priorReceiptExisted := priorReceiptErr == nil
		if priorReceiptErr != nil && !errors.Is(priorReceiptErr, os.ErrNotExist) {
			return errors.Join(priorReceiptErr, rollbackAll(rollbacks, skillRollbacks))
		}
		if beforeReceiptWrite != nil {
			if err := beforeReceiptWrite(); err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		intendedReceipt, err := marshalJSON(newReceipt)
		if err != nil {
			return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
		}
		writeResult, writeErr := atomicPrivateJSONBytes(paths.Receipt, intendedReceipt, afterReceiptRename)
		if writeErr != nil {
			if writeResult.renamed {
				installed, _, verifyErr := readTrustedFile(paths.Receipt, "installed receipt", 4<<20, 0o600)
				if verifyErr == nil && bytes.Equal(installed, intendedReceipt) {
					appendReceiptWarning(&result, "receipt committed but directory durability sync failed: "+writeErr.Error())
				} else {
					if verifyErr == nil {
						verifyErr = fmt.Errorf("installed receipt bytes differ from intended receipt")
					}
					restoreErr := restoreReceiptFile(paths.Receipt, priorReceipt, priorReceiptExisted)
					if restoreErr != nil {
						return errors.Join(writeErr, fmt.Errorf("verify installed receipt: %w", verifyErr), fmt.Errorf("restore prior receipt: %w", restoreErr))
					}
					return errors.Join(writeErr, fmt.Errorf("verify installed receipt: %w", verifyErr), rollbackAll(rollbacks, skillRollbacks))
				}
			} else {
				return errors.Join(writeErr, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		for _, action := range result.Actions {
			if action.Kind == "skill" || action.Action != "restore" {
				continue
			}
			backup := instructionBackup(paths, action.Target)
			if receiptReferencesBackup(newReceipt, backup) {
				continue
			}
			if err := validateUnreferencedBackup(backup); err != nil {
				appendActionWarning(&result, action, "backup cleanup warning: "+err.Error())
				continue
			}
			if err := removeInstructionBackup(backup); err != nil {
				appendActionWarning(&result, action, "backup cleanup warning: "+err.Error())
			}
		}
		return nil
	})
	return result, err
}

// CanResolveCollision reports whether an action from Plan is currently eligible
// for ApplyWithOptions' narrowly scoped managed-file collision prompt.
func CanResolveCollision(action Action) (bool, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return false, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return false, err
	}
	loaded, err := LoadManifest(enrollment.RepositoryPath)
	if err != nil {
		return false, err
	}
	if loaded.Manifest.ID != enrollment.RepositoryID {
		return false, Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("enrolled repository id changed"))
	}
	_, eligible := resolvableCollision(paths, loaded, action)
	return eligible, nil
}

func appendReceiptWarning(plan *PlanResult, warning string) {
	if len(plan.Actions) == 0 {
		return
	}
	if plan.Actions[0].Reason != "" {
		plan.Actions[0].Reason += "; "
	}
	plan.Actions[0].Reason += warning
}

func actionFor(plan PlanResult, kind, target string) Action {
	for _, action := range plan.Actions {
		if action.Kind == kind && action.Target == target {
			return action
		}
	}
	return Action{}
}

func receiptReferencesBackup(receipt Receipt, backup string) bool {
	for _, managed := range receipt.Managed {
		if managed.Backup == backup {
			return true
		}
	}
	return false
}

func appendActionWarning(plan *PlanResult, action Action, warning string) {
	for i := range plan.Actions {
		if plan.Actions[i].Kind == action.Kind && plan.Actions[i].Target == action.Target && plan.Actions[i].Skill == action.Skill {
			if plan.Actions[i].Reason != "" {
				plan.Actions[i].Reason += "; "
			}
			plan.Actions[i].Reason += warning
			return
		}
	}
}

func restoreReceiptSnapshot(path string, data []byte, existed bool) error {
	if existed {
		result, err := atomicPrivateJSONBytes(path, data, nil)
		if err == nil {
			return nil
		}
		if result.renamed {
			installed, _, verifyErr := readTrustedFile(path, "restored receipt", 4<<20, 0o600)
			if verifyErr == nil && bytes.Equal(installed, data) {
				return nil
			}
			if verifyErr == nil {
				verifyErr = fmt.Errorf("restored receipt bytes differ from prior receipt")
			}
			return errors.Join(err, verifyErr)
		}
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		if _, lstatErr := os.Lstat(path); errors.Is(lstatErr, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func validateUnreferencedBackup(path string) error {
	if err := validateTrustedFile(path, "unreferenced instruction backup"); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("instruction backup must be mode 0600")
	}
	return nil
}

func safelyRemoveInstructionBackup(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func managedActionKey(action Action) string { return action.Kind + "\x00" + action.Target }

func resolvableCollision(paths Paths, loaded LoadedManifest, action Action) (resolvedCollision, bool) {
	var state resolvedCollision
	if (action.Kind != "instruction" && action.Kind != "config") || action.Action != "blocked_collision" {
		return state, false
	}
	if err := validateInstructionParent(action.Destination); err != nil {
		return state, false
	}
	source, desired := managedSource(loaded, action.Kind, action.Target)
	if !desired || source != action.Source {
		return state, false
	}
	sourceBytes, err := readVerifiedManagedSource(loaded.Repository, action.Kind, action.Target, source, managedHash(loaded, action.Kind, action.Target))
	if err != nil {
		return state, false
	}
	original, mode, err := readSafeFile(action.Destination, "managed-file collision")
	if err != nil || bytes.Equal(original, sourceBytes) {
		return state, false
	}
	info, err := os.Lstat(action.Destination)
	if err != nil {
		return state, false
	}
	state = resolvedCollision{destinationInfo: info, originalHash: hashBytes(original), originalMode: mode, backup: instructionBackup(paths, action.Target)}
	if !safeBackupParent(paths, filepath.Dir(state.backup)) {
		return resolvedCollision{}, false
	}
	if info, err := os.Lstat(state.backup); err == nil {
		if err := validateUnreferencedBackup(state.backup); err != nil {
			return resolvedCollision{}, false
		}
		backupData, _, err := readSafeFile(state.backup, "unreferenced instruction backup")
		if err != nil {
			return resolvedCollision{}, false
		}
		hash := hashBytes(backupData)
		if hash != state.originalHash {
			return resolvedCollision{}, false
		}
		state.backupInfo, state.backupHash = info, hash
	} else if !errors.Is(err, os.ErrNotExist) {
		return resolvedCollision{}, false
	}
	return state, true
}

func safeBackupParent(paths Paths, parent string) bool {
	if !contained(paths.StateDir, parent) {
		return false
	}
	for current := parent; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			if err := validateTrustedDirectory(current, "backup directory"); err != nil {
				return false
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if current == paths.StateDir {
			return true
		}
	}
}

func preflightResolvedCollision(paths Paths, loaded LoadedManifest, action Action, expected resolvedCollision) error {
	fresh, eligible := resolvableCollision(paths, loaded, Action{Kind: action.Kind, Action: "blocked_collision", Target: action.Target, Source: action.Source, Destination: action.Destination})
	if !eligible || fresh.originalHash != expected.originalHash || fresh.originalMode != expected.originalMode || !os.SameFile(fresh.destinationInfo, expected.destinationInfo) {
		return fmt.Errorf("managed-file collision changed during apply")
	}
	if (fresh.backupInfo == nil) != (expected.backupInfo == nil) || fresh.backupHash != expected.backupHash {
		return fmt.Errorf("managed-file collision backup changed during apply")
	}
	if fresh.backupInfo != nil && !os.SameFile(fresh.backupInfo, expected.backupInfo) {
		return fmt.Errorf("managed-file collision backup changed during apply")
	}
	return nil
}

func preflightAction(paths Paths, loaded LoadedManifest, action Action, ownedSkills map[string]ReceiptProjection, ownedInstructions map[string]ReceiptManaged, resolved map[string]resolvedCollision) error {
	if inert(action) {
		return nil
	}
	if err := revalidateLoadedManifest(loaded); err != nil {
		return fmt.Errorf("revalidate catalog before %s: %w", action.Target, err)
	}
	if action.Kind == "skill" {
		if source, desired := loaded.Sources[action.Skill]; desired && source == action.Source {
			if err := validateTrustedSource(loaded.Repository, action.Source); err != nil {
				return err
			}
		}
		if action.Action == "remove" {
			if !exactSymlink(action.Destination, action.Source) {
				return fmt.Errorf("projection changed during apply")
			}
			return nil
		}
		fresh, _ := classifyLeaf(filepath.Dir(action.Destination), action.Destination, action.Source, ownedSkills[pairKey(action.Skill, action.Target)], ownedSkills[pairKey(action.Skill, action.Target)].Skill != "")
		if fresh != action.Action && !(action.Action == "record" && fresh == "noop") {
			return fmt.Errorf("projection changed during apply")
		}
		return nil
	}
	if state, ok := resolved[managedActionKey(action)]; ok {
		return preflightResolvedCollision(paths, loaded, action, state)
	}
	source, desired := managedSource(loaded, action.Kind, action.Target)
	if desired {
		if err := validateTrustedInstructionSource(loaded.Repository, source); err != nil {
			return err
		}
		data, err := readVerifiedManagedSource(loaded.Repository, action.Kind, action.Target, source, managedHash(loaded, action.Kind, action.Target))
		if err != nil {
			return fmt.Errorf("managed source changed during apply: %w", err)
		}
		hash := hashBytes(data)
		prior, owned := ownedInstructions[action.Target]
		fresh, _ := classifyInstruction(paths, action, hash, prior, owned)
		if fresh != action.Action {
			return fmt.Errorf("instruction changed during apply")
		}
	} else {
		fresh, _ := classifyInstructionRemoval(paths, ownedInstructions[action.Target], action.Destination)
		if fresh != action.Action {
			return fmt.Errorf("instruction changed during apply")
		}
	}
	return nil
}

type skillRollback struct {
	action       string
	destination  string
	beforeSource string
	afterSource  string
	createdDirs  []string
}

func prepareSkillRollback(action Action, owned map[string]ReceiptProjection) skillRollback {
	rollback := skillRollback{action: action.Action, destination: action.Destination, afterSource: action.Source}
	if prior := owned[pairKey(action.Skill, action.Target)]; prior.Skill != "" {
		rollback.beforeSource = prior.Source
	}
	if action.Action == "create" {
		for current := filepath.Dir(action.Destination); ; current = filepath.Dir(current) {
			if _, err := os.Lstat(current); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				break
			}
			rollback.createdDirs = append(rollback.createdDirs, current)
		}
	}
	return rollback
}

func rollbackSkills(rollbacks []skillRollback) error {
	var rollbackErrs []error
	for i := len(rollbacks) - 1; i >= 0; i-- {
		rollback := rollbacks[i]
		switch rollback.action {
		case "create":
			if exactSymlink(rollback.destination, rollback.afterSource) {
				if err := os.Remove(rollback.destination); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback created projection %s: %w", rollback.destination, err))
				}
			}
			for _, dir := range rollback.createdDirs {
				if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback projection directory %s: %w", dir, err))
				}
			}
		case "replace":
			if exactSymlink(rollback.destination, rollback.afterSource) {
				if err := replaceSymlink(rollback.beforeSource, rollback.destination); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback replaced projection %s: %w", rollback.destination, err))
				}
			}
		case "remove":
			if _, err := os.Lstat(rollback.destination); errors.Is(err, os.ErrNotExist) {
				if err := os.Symlink(rollback.beforeSource, rollback.destination); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback removed projection %s: %w", rollback.destination, err))
				}
			}
		}
	}
	return errors.Join(rollbackErrs...)
}

func mutateSkill(paths Paths, loaded LoadedManifest, action Action, owned map[string]ReceiptProjection) error {
	if action.Action == "noop" || action.Action == "record" || action.Action == "adopt" {
		return nil
	}
	if err := revalidateLoadedManifest(loaded); err != nil {
		return err
	}
	switch action.Action {
	case "create", "replace":
		root := filepath.Dir(action.Destination)
		if err := ensureTargetRoot(root); err != nil {
			return err
		}
		if action.Action == "create" {
			if _, err := os.Lstat(action.Destination); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("projection changed during apply")
			}
			if err := os.Symlink(action.Source, action.Destination); err != nil {
				return err
			}
		} else if prior := owned[pairKey(action.Skill, action.Target)]; !exactSymlink(action.Destination, prior.Source) {
			return fmt.Errorf("projection changed during apply")
		} else if err := replaceSymlink(action.Source, action.Destination); err != nil {
			return err
		}
	case "remove":
		if !exactSymlink(action.Destination, action.Source) {
			return fmt.Errorf("projection changed during apply")
		}
		return os.Remove(action.Destination)
	}
	return nil
}

type instructionRollback struct {
	destination     string
	before          []byte
	mode            os.FileMode
	existed         bool
	afterHash       string
	backup          string
	backupMade      bool
	backupExisted   bool
	backupAfterInfo os.FileInfo
	backupAfterHash string
	afterInfo       os.FileInfo
	recovery        string
}

func mutateInstruction(paths Paths, loaded LoadedManifest, action Action, prior ReceiptManaged, verifiedSource []byte, expected resolvedCollision, hasExpected bool) (instructionRollback, error) {
	rollback := instructionRollback{destination: action.Destination}
	if action.Action == "adopt" || action.Action == "replace" {
		data, mode, err := readSafeFile(action.Destination, "instruction destination")
		if err != nil {
			return rollback, err
		}
		if hasExpected {
			info, err := os.Lstat(action.Destination)
			if err != nil || hashBytes(data) != expected.originalHash || mode != expected.originalMode || !os.SameFile(info, expected.destinationInfo) {
				return rollback, fmt.Errorf("managed-file collision changed during apply")
			}
		}
		backup := instructionBackup(paths, action.Target)
		if _, err := os.Lstat(backup); err == nil {
			existing, _, err := readSafeFile(backup, "unreferenced instruction backup")
			if err != nil {
				return rollback, err
			}
			info, err := os.Lstat(backup)
			if hasExpected && (err != nil || expected.backupInfo == nil || hashBytes(existing) != expected.backupHash || !os.SameFile(info, expected.backupInfo)) {
				return rollback, fmt.Errorf("managed-file collision backup changed during apply")
			}
			if !bytes.Equal(existing, data) {
				return rollback, fmt.Errorf("unreferenced backup differs from active file; possible interrupted replacement requires manual recovery")
			}
			rollback.backupExisted = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback, err
		} else if hasExpected && expected.backupInfo != nil {
			return rollback, fmt.Errorf("managed-file collision backup changed during apply")
		}
		rollback.backup = backup
		if !rollback.backupExisted {
			info, created, err := publishBackup(backup, data)
			rollback.backupMade = created
			rollback.backupAfterInfo = info
			rollback.backupAfterHash = hashBytes(data)
			if err != nil {
				return rollback, err
			}
		}
		if afterBackupPublication != nil {
			if err := afterBackupPublication(backup); err != nil {
				return rollback, err
			}
		}
		if rollback.backupMade {
			info, err := os.Lstat(backup)
			if err != nil || rollback.backupAfterInfo == nil || !os.SameFile(info, rollback.backupAfterInfo) {
				return rollback, fmt.Errorf("instruction backup changed during apply")
			}
		}
		if err := validatePublishedBackup(backup, data); err != nil {
			return rollback, err
		}
		if action.Action == "adopt" {
			return rollback, nil
		}
		rollback.before, rollback.mode, rollback.existed = data, mode, true
		if afterReplacementBackup != nil {
			if err := afterReplacementBackup(action); err != nil {
				return rollback, err
			}
		}
	}
	if action.Action != "create" && action.Action != "replace" {
		data, mode, err := readSafeFile(action.Destination, "managed instruction")
		if err != nil {
			return rollback, err
		}
		rollback.before, rollback.mode, rollback.existed = data, mode, true
	}
	switch action.Action {
	case "create", "update":
		spec, _ := lookupTarget(action.Kind, action.Target)
		mode := spec.Mode
		if action.Kind == "instruction" && rollback.existed {
			mode = rollback.mode
		}
		mutation, err := atomicInstructionFile(action.Destination, verifiedSource, mode, action.Action == "create")
		if mutation.mutated {
			rollback.afterHash = managedHash(loaded, action.Kind, action.Target)
			rollback.afterInfo = mutation.info
		}
		if err != nil {
			return rollback, err
		}
	case "replace":
		spec, _ := lookupTarget(action.Kind, action.Target)
		mode := spec.Mode
		if action.Kind == "instruction" {
			mode = rollback.mode
		}
		mutation, err := conditionalInstructionReplace(action, verifiedSource, mode, expected.destinationInfo, expected.originalHash, expected.originalMode, true)
		if mutation.mutated {
			rollback.afterHash = managedHash(loaded, action.Kind, action.Target)
			rollback.afterInfo = mutation.info
		}
		rollback.recovery = mutation.recovery
		if err != nil {
			return rollback, err
		}
	case "remove":
		if err := os.Remove(action.Destination); err != nil {
			return rollback, err
		}
	case "restore":
		if err := validateBackup(paths, prior); err != nil {
			return rollback, err
		}
		data, _, err := readSafeFile(instructionBackup(paths, action.Target), "instruction backup")
		if err != nil {
			return rollback, err
		}
		mutation, err := atomicInstructionFile(action.Destination, data, os.FileMode(prior.OriginalMode), false)
		if mutation.mutated {
			rollback.afterHash = prior.OriginalHash
			rollback.afterInfo = mutation.info
		}
		if err != nil {
			return rollback, err
		}
	}
	return rollback, nil
}

func readVerifiedInstructionSource(repository, target, source, expectedHash string) ([]byte, error) {
	return readVerifiedManagedSource(repository, "instruction", target, source, expectedHash)
}

func readVerifiedManagedSource(repository, kind, target, source, expectedHash string) ([]byte, error) {
	if err := validateTrustedInstructionSource(repository, source); err != nil {
		return nil, err
	}
	data, _, err := readTrustedFile(source, "instruction source", instructionLimit, 0)
	if err != nil {
		return nil, err
	}
	if err := validateTrustedInstructionSource(repository, source); err != nil {
		return nil, err
	}
	if hashBytes(data) != expectedHash {
		return nil, fmt.Errorf("managed source changed during apply")
	}
	spec, ok := lookupTarget(kind, target)
	if !ok {
		return nil, fmt.Errorf("unsupported %s target %q", kind, target)
	}
	if spec.Validate != nil {
		if err := spec.Validate(data); err != nil {
			return nil, fmt.Errorf("config source is unsafe: %w", err)
		}
	}
	return data, nil
}

func managedSource(loaded LoadedManifest, kind, target string) (string, bool) {
	if kind == "config" {
		source, ok := loaded.ConfigSources[target]
		return source, ok
	}
	source, ok := loaded.InstructionSources[target]
	return source, ok
}

func managedHash(loaded LoadedManifest, kind, target string) string {
	if kind == "config" {
		return loaded.ConfigHashes[target]
	}
	return loaded.InstructionHashes[target]
}

func managedSourceHash(loaded LoadedManifest, kind, target string) (string, bool) {
	_, ok := managedSource(loaded, kind, target)
	return managedHash(loaded, kind, target), ok
}

func rollbackInstructions(rollbacks []instructionRollback) error {
	var rollbackErrs []error
	for i := len(rollbacks) - 1; i >= 0; i-- {
		rollback := rollbacks[i]
		activeRestored := true
		if !rollback.existed {
			if rollback.afterHash != "" {
				if current, err := os.Stat(rollback.destination); err == nil && rollback.afterInfo != nil && os.SameFile(current, rollback.afterInfo) {
					if err := os.Remove(rollback.destination); err != nil {
						rollbackErrs = append(rollbackErrs, fmt.Errorf("remove created instruction %s: %w", rollback.destination, err))
					}
				}
			}
		} else {
			if rollback.afterHash == "" {
				if _, err := os.Lstat(rollback.destination); !errors.Is(err, os.ErrNotExist) {
					activeRestored = false
				}
			} else {
				current, err := os.Stat(rollback.destination)
				if err != nil || rollback.afterInfo == nil || !os.SameFile(current, rollback.afterInfo) {
					activeRestored = false
				}
			}
			if activeRestored {
				var err error
				if rollback.afterHash != "" {
					_, err = conditionalInstructionReplace(Action{Destination: rollback.destination}, rollback.before, rollback.mode, rollback.afterInfo, rollback.afterHash, rollback.afterInfo.Mode().Perm(), false)
				} else {
					_, err = atomicInstructionFile(rollback.destination, rollback.before, rollback.mode, false)
				}
				if err != nil {
					activeRestored = false
					rollbackErrs = append(rollbackErrs, fmt.Errorf("restore instruction %s: %w", rollback.destination, err))
				}
			}
		}
		if rollback.recovery != "" {
			recovery, mode, err := readSafeFile(rollback.recovery, "replacement recovery file")
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil:
				activeRestored = false
				rollbackErrs = append(rollbackErrs, fmt.Errorf("inspect replacement recovery %s: %w", rollback.recovery, err))
			case hashBytes(recovery) != hashBytes(rollback.before) || mode != rollback.mode:
				activeRestored = false
				rollbackErrs = append(rollbackErrs, fmt.Errorf("newer displaced bytes preserved at %s", rollback.recovery))
			case !activeRestored:
				rollbackErrs = append(rollbackErrs, fmt.Errorf("displaced original preserved at %s", rollback.recovery))
			default:
				if err := removeRecoveryFile(rollback.recovery); err != nil {
					activeRestored = false
					rollbackErrs = append(rollbackErrs, fmt.Errorf("remove replacement recovery %s: %w", rollback.recovery, err))
				}
			}
		}
		if rollback.backupMade && activeRestored {
			if err := removeBackupConditional(rollback.backup, rollback.backupAfterInfo, rollback.backupAfterHash); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
		}
	}
	return errors.Join(rollbackErrs...)
}

func rollbackAll(instructions []instructionRollback, skills []skillRollback) error {
	return errors.Join(rollbackInstructions(instructions), rollbackSkills(skills))
}

func ensureInstructionParent(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	return validateInstructionParent(destination)
}

type instructionFileMutation struct {
	mutated  bool
	info     os.FileInfo
	recovery string
}

func atomicInstructionFile(destination string, data []byte, mode os.FileMode, requireMissing bool) (instructionFileMutation, error) {
	var mutation instructionFileMutation
	if err := ensureInstructionParent(destination); err != nil {
		return mutation, err
	}
	if requireMissing {
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return mutation, fmt.Errorf("instruction changed during apply")
		}
	}
	dir := filepath.Dir(destination)
	f, err := os.CreateTemp(dir, ".terran-instruction-*")
	if err != nil {
		return mutation, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode.Perm()); err != nil {
		return mutation, err
	}
	if _, err := f.Write(data); err != nil {
		return mutation, err
	}
	if err := f.Sync(); err != nil {
		return mutation, err
	}
	if err := f.Close(); err != nil {
		return mutation, err
	}
	if requireMissing {
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return mutation, fmt.Errorf("instruction changed during apply")
		}
	}
	if err := os.Rename(tmp, destination); err != nil {
		return mutation, err
	}
	mutation.mutated = true
	mutation.info, _ = os.Stat(destination)
	if afterInstructionRename != nil {
		if err := afterInstructionRename(destination); err != nil {
			return mutation, err
		}
	}
	if err := syncDirectory(dir); err != nil {
		return mutation, err
	}
	written, err := fileHash(destination)
	if err != nil || written != hashBytes(data) {
		return mutation, fmt.Errorf("instruction hash verification failed")
	}
	if afterInstructionHash != nil {
		if err := afterInstructionHash(destination); err != nil {
			return mutation, err
		}
	}
	ok = true
	return mutation, nil
}

func conditionalInstructionReplace(action Action, data []byte, mode os.FileMode, expectedInfo os.FileInfo, expectedHash string, expectedMode os.FileMode, runReplacementHooks bool) (instructionFileMutation, error) {
	var mutation instructionFileMutation
	destination := action.Destination
	if err := ensureInstructionParent(destination); err != nil {
		return mutation, err
	}
	dir := filepath.Dir(destination)
	tmp, err := prepareInstructionTemp(dir, data, mode)
	if err != nil {
		return mutation, err
	}
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	quarantineDir, err := os.MkdirTemp(dir, ".terran-quarantine-*")
	if err != nil {
		return mutation, err
	}
	quarantine := filepath.Join(quarantineDir, "displaced")
	mutation.recovery = quarantine
	if err := os.Rename(destination, quarantine); err != nil {
		_ = os.Remove(quarantineDir)
		return mutation, fmt.Errorf("quarantine expected destination: %w", err)
	}
	if err := syncDirectory(quarantineDir); err != nil {
		return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
	}
	if err := syncDirectory(dir); err != nil {
		return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
	}
	displaced, displacedMode, err := readSafeFile(quarantine, "quarantined managed file")
	if err != nil {
		return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
	}
	displacedInfo, err := os.Lstat(quarantine)
	if err != nil || !os.SameFile(displacedInfo, expectedInfo) || hashBytes(displaced) != expectedHash || displacedMode != expectedMode {
		return mutation, errors.Join(fmt.Errorf("managed-file collision changed before replacement"), restoreDisplacedNoReplace(destination, quarantine))
	}
	if runReplacementHooks && beforeReplacementInstall != nil {
		if err := beforeReplacementInstall(action); err != nil {
			return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
		}
	}
	if err := os.Link(tmp, destination); err != nil {
		return mutation, errors.Join(fmt.Errorf("install managed file without overwrite: %w", err), restoreDisplacedNoReplace(destination, quarantine))
	}
	mutation.mutated = true
	mutation.info, err = os.Lstat(destination)
	if err != nil {
		return mutation, err
	}
	if runReplacementHooks && beforeReplacementCleanup != nil {
		if err := beforeReplacementCleanup(tmp); err != nil {
			return mutation, err
		}
	}
	if err := os.Remove(tmp); err != nil {
		return mutation, err
	}
	tmp = ""
	if runReplacementHooks && afterReplacementInstall != nil {
		if err := afterReplacementInstall(action); err != nil {
			return mutation, err
		}
	}
	if afterInstructionRename != nil {
		if err := afterInstructionRename(destination); err != nil {
			return mutation, err
		}
	}
	if err := syncDirectory(dir); err != nil {
		return mutation, err
	}
	if err := validateTrustedFile(destination, "installed managed file"); err != nil {
		return mutation, err
	}
	installedInfo, err := os.Lstat(destination)
	if err != nil || !os.SameFile(installedInfo, mutation.info) || installedInfo.Mode().Perm() != mode.Perm() {
		return mutation, fmt.Errorf("installed managed file changed during replacement")
	}
	installedHash, err := fileHash(destination)
	if err != nil || installedHash != hashBytes(data) {
		return mutation, fmt.Errorf("installed managed file changed during replacement")
	}
	if afterInstructionHash != nil {
		if err := afterInstructionHash(destination); err != nil {
			return mutation, err
		}
	}
	displaced, displacedMode, err = readSafeFile(quarantine, "quarantined managed file")
	if err != nil || hashBytes(displaced) != expectedHash || displacedMode != expectedMode {
		return mutation, fmt.Errorf("quarantined original changed during replacement; recovery preserved at %s", quarantine)
	}
	displacedInfo, err = os.Lstat(quarantine)
	if err != nil || !os.SameFile(displacedInfo, expectedInfo) {
		return mutation, fmt.Errorf("quarantined original changed during replacement; recovery preserved at %s", quarantine)
	}
	if err := removeRecoveryFile(quarantine); err != nil {
		return mutation, err
	}
	mutation.recovery = ""
	return mutation, nil
}

func prepareInstructionTemp(dir string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, ".terran-instruction-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode.Perm()); err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return tmp, nil
}

func restoreDisplacedNoReplace(destination, quarantine string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination was recreated; displaced file preserved at %s", quarantine)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination while restoring displaced file: %w; recovery preserved at %s", err, quarantine)
	}
	if err := os.Link(quarantine, destination); err != nil {
		return fmt.Errorf("restore displaced file without overwrite: %w; recovery preserved at %s", err, quarantine)
	}
	if err := removeRecoveryFile(quarantine); err != nil {
		return fmt.Errorf("restored displaced file but cleanup failed: %w", err)
	}
	return nil
}

func removeRecoveryFile(path string) error {
	dir := filepath.Dir(path)
	parent := filepath.Dir(dir)
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func removeBackupConditional(path string, expectedInfo os.FileInfo, expectedHash string) error {
	if expectedInfo == nil {
		return fmt.Errorf("preserve changed instruction backup %s", path)
	}
	dir := filepath.Dir(path)
	quarantineDir, err := os.MkdirTemp(dir, ".terran-backup-quarantine-*")
	if err != nil {
		return err
	}
	quarantine := filepath.Join(quarantineDir, "displaced")
	if err := os.Rename(path, quarantine); err != nil {
		_ = os.Remove(quarantineDir)
		return fmt.Errorf("preserve changed instruction backup %s: %w", path, err)
	}
	if err := syncDirectory(quarantineDir); err != nil {
		return errors.Join(err, restoreDisplacedNoReplace(path, quarantine))
	}
	if err := syncDirectory(dir); err != nil {
		return errors.Join(err, restoreDisplacedNoReplace(path, quarantine))
	}
	data, mode, err := readSafeFile(quarantine, "rollback instruction backup")
	info, statErr := os.Lstat(quarantine)
	if err != nil || statErr != nil || !os.SameFile(info, expectedInfo) || mode != 0o600 || hashBytes(data) != expectedHash {
		return errors.Join(fmt.Errorf("preserve changed instruction backup %s", path), restoreDisplacedNoReplace(path, quarantine))
	}
	if err := removeRecoveryFile(quarantine); err != nil {
		return fmt.Errorf("remove instruction backup %s: %w", path, err)
	}
	return nil
}

func publishBackup(path string, data []byte) (os.FileInfo, bool, error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, false, err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".terran-backup-*")
	if err != nil {
		return nil, false, err
	}
	tmp := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(0o600); err != nil {
		return nil, false, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, false, err
	}
	if err := f.Sync(); err != nil {
		return nil, false, err
	}
	if err := f.Close(); err != nil {
		return nil, false, err
	}
	if beforeBackupPublish != nil {
		if err := beforeBackupPublish(path); err != nil {
			return nil, false, err
		}
	}
	if err := os.Link(tmp, path); err != nil {
		return nil, false, fmt.Errorf("publish instruction backup without overwrite: %w", err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return nil, true, statErr
	}
	if err := os.Remove(tmp); err != nil {
		return info, true, err
	}
	if err := syncDirectory(dir); err != nil {
		return info, true, err
	}
	return info, true, nil
}

func validatePublishedBackup(path string, data []byte) error {
	if err := validateTrustedFile(path, "instruction backup"); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("instruction backup must be mode 0600")
	}
	hash, err := fileHash(path)
	if err != nil || hash != hashBytes(data) {
		return fmt.Errorf("instruction backup hash verification failed")
	}
	return nil
}

func readSafeFile(path, description string) ([]byte, os.FileMode, error) {
	return readTrustedFile(path, description, 4<<20, 0)
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func replaceSymlink(source, destination string) error {
	tmp := filepath.Join(filepath.Dir(destination), ".terran-"+filepath.Base(destination)+fmt.Sprintf("-%d", os.Getpid()))
	if err := os.Symlink(source, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, destination); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
