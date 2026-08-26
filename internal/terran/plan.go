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
	"sort"
	"syscall"
	"time"
)

func validateTarget(target string) error {
	if target != "all" && target != "agents" && target != "claude" && target != "opencode" {
		return fmt.Errorf("target must be all, agents, claude, or opencode")
	}
	return nil
}

func selectedSkill(filter, target string) bool {
	return filter == "all" || filter == target
}

func selectedInstruction(filter, target string) bool {
	return filter == "all" || (filter == "claude" && target == "claude-global") || (filter == "opencode" && target == "opencode-global")
}

func selectedConfig(filter, target string) bool {
	return filter == "all" || (filter == "opencode" && target == "opencode-config")
}

func LoadReceipt(paths Paths) (Receipt, error) {
	var receipt Receipt
	if err := readTrustedStateStrict(paths.Receipt, "receipt", &receipt, 4<<20); err != nil {
		return Receipt{}, err
	}
	if receipt.SchemaVersion != SchemaVersion || receipt.RepositoryID == "" || !filepath.IsAbs(receipt.RepositoryPath) || filepath.Clean(receipt.RepositoryPath) != receipt.RepositoryPath {
		return Receipt{}, fmt.Errorf("invalid receipt repository")
	}
	seen := map[string]bool{}
	for _, p := range receipt.Projections {
		if !skillNamePattern.MatchString(p.Skill) || (p.Target != "agents" && p.Target != "claude") || p.Strategy != "symlink" || !filepath.IsAbs(p.Source) {
			return Receipt{}, fmt.Errorf("invalid receipt projection")
		}
		root, _ := targetRoot(paths.Home, p.Target)
		if p.Destination != filepath.Join(root, p.Skill) {
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
	for _, instruction := range receipt.Instructions {
		destination, err := instructionDestination(paths, instruction.Target)
		if err != nil || instruction.Strategy != "copy" || !validHash(instruction.SourceHash) || !validHash(instruction.AppliedHash) {
			return Receipt{}, fmt.Errorf("invalid receipt instruction")
		}
		if seen[instruction.Target] {
			return Receipt{}, fmt.Errorf("duplicate receipt instruction")
		}
		seen[instruction.Target] = true
		if instruction.Destination != destination || !filepath.IsAbs(instruction.Source) || filepath.Clean(instruction.Source) != instruction.Source || !contained(receipt.RepositoryPath, instruction.Source) {
			return Receipt{}, fmt.Errorf("unsafe receipt instruction paths for %s", instruction.Target)
		}
		switch instruction.Origin {
		case "created":
			if instruction.OriginalHash != "" || instruction.OriginalMode != 0 || instruction.Backup != "" {
				return Receipt{}, fmt.Errorf("invalid created instruction receipt for %s", instruction.Target)
			}
		case "adopted":
			if !validHash(instruction.OriginalHash) || instruction.OriginalMode&0o022 != 0 || instruction.OriginalMode&^0o777 != 0 || instruction.Backup != instructionBackup(paths, instruction.Target) {
				return Receipt{}, fmt.Errorf("invalid adopted instruction receipt for %s", instruction.Target)
			}
		default:
			return Receipt{}, fmt.Errorf("invalid instruction origin for %s", instruction.Target)
		}
	}
	seen = map[string]bool{}
	for _, stored := range receipt.Configs {
		config := ReceiptInstruction(stored)
		destination, err := configDestination(paths, config.Target)
		if err != nil || config.Strategy != "copy" || !validHash(config.SourceHash) || !validHash(config.AppliedHash) {
			return Receipt{}, fmt.Errorf("invalid receipt config")
		}
		if seen[config.Target] {
			return Receipt{}, fmt.Errorf("duplicate receipt config")
		}
		seen[config.Target] = true
		if config.Destination != destination || !filepath.IsAbs(config.Source) || filepath.Clean(config.Source) != config.Source || !contained(receipt.RepositoryPath, config.Source) {
			return Receipt{}, fmt.Errorf("unsafe receipt config paths for %s", config.Target)
		}
		switch config.Origin {
		case "created":
			if config.OriginalHash != "" || config.OriginalMode != 0 || config.Backup != "" {
				return Receipt{}, fmt.Errorf("invalid created config receipt for %s", config.Target)
			}
		case "adopted":
			if !validHash(config.OriginalHash) || config.OriginalMode&0o022 != 0 || config.OriginalMode&^0o777 != 0 || config.Backup != instructionBackup(paths, config.Target) {
				return Receipt{}, fmt.Errorf("invalid adopted config receipt for %s", config.Target)
			}
		default:
			return Receipt{}, fmt.Errorf("invalid config origin for %s", config.Target)
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
	loaded, err := LoadManifest(enrollment.RepositoryPath)
	if err != nil {
		return PlanResult{}, err
	}
	if loaded.Manifest.ID != enrollment.RepositoryID {
		return PlanResult{}, fmt.Errorf("enrolled repository id changed")
	}
	receipt, err := LoadReceipt(paths)
	if errors.Is(err, os.ErrNotExist) {
		receipt = Receipt{}
	} else if err != nil {
		return PlanResult{}, fmt.Errorf("load receipt: %w", err)
	} else if receipt.RepositoryID != enrollment.RepositoryID || receipt.RepositoryPath != loaded.Repository {
		return PlanResult{}, fmt.Errorf("receipt repository differs from enrollment")
	}
	return makePlan(paths, loaded, receipt, target)
}

func makePlan(paths Paths, loaded LoadedManifest, receipt Receipt, filter string) (PlanResult, error) {
	ownedSkills := map[string]ReceiptProjection{}
	for _, projection := range receipt.Projections {
		ownedSkills[pairKey(projection.Skill, projection.Target)] = projection
	}
	ownedInstructions := map[string]ReceiptInstruction{}
	for _, instruction := range receipt.Instructions {
		ownedInstructions[instruction.Target] = instruction
	}
	ownedConfigs := map[string]ReceiptInstruction{}
	for _, config := range receipt.Configs {
		ownedConfigs[config.Target] = ReceiptInstruction(config)
	}
	desiredSkills := map[string]Action{}
	for _, projection := range loaded.Manifest.Projections {
		for _, target := range projection.Targets {
			if !selectedSkill(filter, target) {
				continue
			}
			root, _ := targetRoot(paths.Home, target)
			action := Action{Kind: "skill", Skill: projection.Skill, Target: target, Source: loaded.Sources[projection.Skill], Destination: filepath.Join(root, projection.Skill)}
			desiredSkills[pairKey(projection.Skill, target)] = action
		}
	}
	actions := make([]Action, 0, len(desiredSkills)+len(ownedSkills)+len(loaded.Manifest.Instructions)+len(ownedInstructions)+len(loaded.Manifest.Configs)+len(ownedConfigs))
	for key, action := range desiredSkills {
		prior, owned := ownedSkills[key]
		action.Action, action.Reason = classifyLeaf(filepath.Dir(action.Destination), action.Destination, action.Source, prior, owned)
		actions = append(actions, action)
	}
	for key, prior := range ownedSkills {
		if !selectedSkill(filter, prior.Target) || desiredSkills[key].Skill != "" {
			continue
		}
		root, _ := targetRoot(paths.Home, prior.Target)
		action := Action{Kind: "skill", Skill: prior.Skill, Target: prior.Target, Source: prior.Source, Destination: filepath.Join(root, prior.Skill)}
		if exactSymlink(action.Destination, prior.Source) {
			action.Action = "remove"
		} else {
			action.Action, action.Reason = "blocked_drift", "receipt-owned projection is missing or changed"
		}
		actions = append(actions, action)
	}
	desiredInstructions := map[string]bool{}
	for _, instruction := range loaded.Manifest.Instructions {
		if !selectedInstruction(filter, instruction.Target) {
			continue
		}
		desiredInstructions[instruction.Target] = true
		destination, _ := instructionDestination(paths, instruction.Target)
		if contained(loaded.Repository, destination) {
			return PlanResult{}, fmt.Errorf("instruction destination for %s must not be inside the repository", instruction.Target)
		}
		action := Action{Kind: "instruction", Target: instruction.Target, Source: loaded.InstructionSources[instruction.Target], Destination: destination}
		prior, owned := ownedInstructions[instruction.Target]
		action.Action, action.Reason = classifyInstruction(paths, action, loaded.InstructionHashes[instruction.Target], prior, owned)
		actions = append(actions, action)
	}
	for _, prior := range ownedInstructions {
		if !selectedInstruction(filter, prior.Target) || desiredInstructions[prior.Target] {
			continue
		}
		destination, _ := instructionDestination(paths, prior.Target)
		action := Action{Kind: "instruction", Target: prior.Target, Source: prior.Source, Destination: destination}
		action.Action, action.Reason = classifyInstructionRemoval(paths, prior, destination)
		actions = append(actions, action)
	}
	desiredConfigs := map[string]bool{}
	for _, config := range loaded.Manifest.Configs {
		if !selectedConfig(filter, config.Target) {
			continue
		}
		desiredConfigs[config.Target] = true
		destination, _ := configDestination(paths, config.Target)
		if contained(loaded.Repository, destination) {
			return PlanResult{}, fmt.Errorf("config destination for %s must not be inside the repository", config.Target)
		}
		action := Action{Kind: "config", Target: config.Target, Source: loaded.ConfigSources[config.Target], Destination: destination}
		prior, owned := ownedConfigs[config.Target]
		action.Action, action.Reason = classifyInstruction(paths, action, loaded.ConfigHashes[config.Target], prior, owned)
		actions = append(actions, action)
	}
	for _, prior := range ownedConfigs {
		if !selectedConfig(filter, prior.Target) || desiredConfigs[prior.Target] {
			continue
		}
		destination, _ := configDestination(paths, prior.Target)
		action := Action{Kind: "config", Target: prior.Target, Source: prior.Source, Destination: destination}
		action.Action, action.Reason = classifyInstructionRemoval(paths, prior, destination)
		actions = append(actions, action)
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
		if action.Action != "noop" {
			clean = false
		}
	}
	return PlanResult{SchemaVersion: SchemaVersion, Clean: clean, Actions: actions}, nil
}

func classifyInstruction(paths Paths, action Action, sourceHash string, prior ReceiptInstruction, owned bool) (string, string) {
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
		return "adopt", "existing exact regular file; replace unreferenced stale backup"
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

func classifyInstructionRemoval(paths Paths, prior ReceiptInstruction, destination string) (string, string) {
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

func validateBackup(paths Paths, prior ReceiptInstruction) error {
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
	restoreReceiptFile        = restoreReceiptSnapshot
	removeInstructionBackup   = safelyRemoveInstructionBackup
)

func Apply(target, buildVersion string) (PlanResult, error) {
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
			return fmt.Errorf("enrolled repository id changed")
		}
		receipt, err := LoadReceipt(paths)
		if errors.Is(err, os.ErrNotExist) {
			receipt = Receipt{}
		} else if err != nil {
			return err
		} else if receipt.RepositoryID != enrollment.RepositoryID || receipt.RepositoryPath != loaded.Repository {
			return fmt.Errorf("receipt repository differs from enrollment")
		}
		result, err = makePlan(paths, loaded, receipt, target)
		if err != nil || blocked(result) {
			return err
		}
		if err := revalidateLoadedManifest(loaded); err != nil {
			return fmt.Errorf("revalidate catalog: %w", err)
		}
		ownedSkills := map[string]ReceiptProjection{}
		for _, projection := range receipt.Projections {
			ownedSkills[pairKey(projection.Skill, projection.Target)] = projection
		}
		ownedInstructions := map[string]ReceiptInstruction{}
		for _, instruction := range receipt.Instructions {
			ownedInstructions[instruction.Target] = instruction
		}
		for _, config := range receipt.Configs {
			ownedInstructions[config.Target] = ReceiptInstruction(config)
		}
		for _, action := range result.Actions {
			if err := preflightAction(paths, loaded, action, ownedSkills, ownedInstructions); err != nil {
				return err
			}
		}
		var skillRollbacks []skillRollback
		for _, action := range result.Actions {
			if action.Kind != "skill" {
				continue
			}
			if err := preflightAction(paths, loaded, action, ownedSkills, ownedInstructions); err != nil {
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
			if action.Kind == "skill" || action.Action == "noop" {
				continue
			}
			if err := preflightAction(paths, loaded, action, ownedSkills, ownedInstructions); err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			if beforeInstructionMutation != nil {
				if err := beforeInstructionMutation(action); err != nil {
					return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
				}
			}
			var sourceBytes []byte
			if expectedHash, desired := managedSourceHash(loaded, action.Kind, action.Target); desired {
				sourceBytes, err = readVerifiedManagedSource(loaded.Repository, action.Kind, action.Source, expectedHash)
				if err != nil {
					rollbackErr := rollbackAll(rollbacks, skillRollbacks)
					return errors.Join(err, rollbackErr)
				}
				verifiedInstructionHashes[action.Target] = hashBytes(sourceBytes)
			}
			rollback, err := mutateInstruction(paths, loaded, action, ownedInstructions[action.Target], sourceBytes)
			rollbacks = append(rollbacks, rollback)
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		if err := revalidateLoadedManifest(loaded); err != nil {
			return errors.Join(fmt.Errorf("revalidate catalog before receipt: %w", err), rollbackAll(rollbacks, skillRollbacks))
		}
		for _, instruction := range loaded.Manifest.Instructions {
			if !selectedInstruction(target, instruction.Target) {
				continue
			}
			data, err := readVerifiedInstructionSource(loaded.Repository, loaded.InstructionSources[instruction.Target], loaded.InstructionHashes[instruction.Target])
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			verifiedInstructionHashes[instruction.Target] = hashBytes(data)
		}
		for _, config := range loaded.Manifest.Configs {
			if !selectedConfig(target, config.Target) {
				continue
			}
			data, err := readVerifiedManagedSource(loaded.Repository, "config", loaded.ConfigSources[config.Target], loaded.ConfigHashes[config.Target])
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			verifiedInstructionHashes[config.Target] = hashBytes(data)
		}
		now := time.Now().UTC()
		newReceipt := Receipt{SchemaVersion: SchemaVersion, RepositoryID: enrollment.RepositoryID, RepositoryPath: enrollment.RepositoryPath, RepositoryVersion: loaded.Manifest.Version, ManifestFingerprint: loaded.Fingerprint}
		for _, old := range receipt.Projections {
			if !selectedSkill(target, old.Target) {
				newReceipt.Projections = append(newReceipt.Projections, old)
			}
		}
		for _, projection := range loaded.Manifest.Projections {
			for _, destinationTarget := range projection.Targets {
				if !selectedSkill(target, destinationTarget) {
					continue
				}
				root, _ := targetRoot(paths.Home, destinationTarget)
				newReceipt.Projections = append(newReceipt.Projections, ReceiptProjection{Skill: projection.Skill, Target: destinationTarget, Source: loaded.Sources[projection.Skill], Destination: filepath.Join(root, projection.Skill), Strategy: "symlink", AppliedAt: now, TerranBuildVersion: buildVersion})
			}
		}
		for _, old := range receipt.Instructions {
			if !selectedInstruction(target, old.Target) {
				newReceipt.Instructions = append(newReceipt.Instructions, old)
			}
		}
		for _, instruction := range loaded.Manifest.Instructions {
			if !selectedInstruction(target, instruction.Target) {
				continue
			}
			prior, owned := ownedInstructions[instruction.Target]
			origin := "created"
			var originalHash, backup string
			var originalMode uint32
			if owned {
				origin, originalHash, originalMode, backup = prior.Origin, prior.OriginalHash, prior.OriginalMode, prior.Backup
			} else if actionFor(result, "instruction", instruction.Target).Action == "adopt" {
				origin = "adopted"
				destination, _ := instructionDestination(paths, instruction.Target)
				info, _ := os.Stat(destination)
				originalHash = loaded.InstructionHashes[instruction.Target]
				originalMode = uint32(info.Mode().Perm())
				backup = instructionBackup(paths, instruction.Target)
			}
			destination, _ := instructionDestination(paths, instruction.Target)
			sourceHash := verifiedInstructionHashes[instruction.Target]
			if sourceHash == "" {
				sourceHash = loaded.InstructionHashes[instruction.Target]
			}
			newReceipt.Instructions = append(newReceipt.Instructions, ReceiptInstruction{Target: instruction.Target, Source: loaded.InstructionSources[instruction.Target], Destination: destination, Strategy: "copy", SourceHash: sourceHash, AppliedHash: sourceHash, Origin: origin, OriginalHash: originalHash, OriginalMode: originalMode, Backup: backup, AppliedAt: now, TerranBuildVersion: buildVersion})
		}
		for _, old := range receipt.Configs {
			if !selectedConfig(target, old.Target) {
				newReceipt.Configs = append(newReceipt.Configs, old)
			}
		}
		for _, config := range loaded.Manifest.Configs {
			if !selectedConfig(target, config.Target) {
				continue
			}
			prior, owned := ownedInstructions[config.Target]
			origin := "created"
			var originalHash, backup string
			var originalMode uint32
			if owned {
				origin, originalHash, originalMode, backup = prior.Origin, prior.OriginalHash, prior.OriginalMode, prior.Backup
			} else if actionFor(result, "config", config.Target).Action == "adopt" {
				origin = "adopted"
				destination, _ := configDestination(paths, config.Target)
				info, _ := os.Stat(destination)
				originalHash = loaded.ConfigHashes[config.Target]
				originalMode = uint32(info.Mode().Perm())
				backup = instructionBackup(paths, config.Target)
			}
			destination, _ := configDestination(paths, config.Target)
			sourceHash := verifiedInstructionHashes[config.Target]
			if sourceHash == "" {
				sourceHash = loaded.ConfigHashes[config.Target]
			}
			newReceipt.Configs = append(newReceipt.Configs, ReceiptConfig{Target: config.Target, Source: loaded.ConfigSources[config.Target], Destination: destination, Strategy: "copy", SourceHash: sourceHash, AppliedHash: sourceHash, Origin: origin, OriginalHash: originalHash, OriginalMode: originalMode, Backup: backup, AppliedAt: now, TerranBuildVersion: buildVersion})
		}
		sort.Slice(newReceipt.Projections, func(i, j int) bool {
			return pairKey(newReceipt.Projections[i].Skill, newReceipt.Projections[i].Target) < pairKey(newReceipt.Projections[j].Skill, newReceipt.Projections[j].Target)
		})
		sort.Slice(newReceipt.Instructions, func(i, j int) bool { return newReceipt.Instructions[i].Target < newReceipt.Instructions[j].Target })
		sort.Slice(newReceipt.Configs, func(i, j int) bool { return newReceipt.Configs[i].Target < newReceipt.Configs[j].Target })
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
	for _, instruction := range receipt.Instructions {
		if instruction.Backup == backup {
			return true
		}
	}
	for _, config := range receipt.Configs {
		if config.Backup == backup {
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

func preflightAction(paths Paths, loaded LoadedManifest, action Action, ownedSkills map[string]ReceiptProjection, ownedInstructions map[string]ReceiptInstruction) error {
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
	source, desired := managedSource(loaded, action.Kind, action.Target)
	if desired {
		if err := validateTrustedInstructionSource(loaded.Repository, source); err != nil {
			return err
		}
		data, err := readVerifiedManagedSource(loaded.Repository, action.Kind, source, managedHash(loaded, action.Kind, action.Target))
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
	destination   string
	before        []byte
	mode          os.FileMode
	existed       bool
	afterHash     string
	backup        string
	backupMade    bool
	backupBefore  []byte
	backupExisted bool
	afterInfo     os.FileInfo
}

func mutateInstruction(paths Paths, loaded LoadedManifest, action Action, prior ReceiptInstruction, verifiedSource []byte) (instructionRollback, error) {
	rollback := instructionRollback{destination: action.Destination}
	if action.Action == "adopt" {
		data, mode, err := readSafeFile(action.Destination, "instruction destination")
		if err != nil {
			return rollback, err
		}
		backup := instructionBackup(paths, action.Target)
		if _, err := os.Lstat(backup); err == nil {
			existing, _, err := readSafeFile(backup, "unreferenced instruction backup")
			if err != nil {
				return rollback, err
			}
			rollback.backupBefore, rollback.backupExisted = existing, true
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback, err
		}
		rollback.backup, rollback.backupMade = backup, true
		if err := writeBackup(backup, data, rollback.backupExisted); err != nil {
			return rollback, err
		}
		_ = mode
		return rollback, nil
	}
	if action.Action != "create" {
		data, mode, err := readSafeFile(action.Destination, "managed instruction")
		if err != nil {
			return rollback, err
		}
		rollback.before, rollback.mode, rollback.existed = data, mode, true
	}
	switch action.Action {
	case "create", "update":
		mode := os.FileMode(0o644)
		if action.Kind == "config" {
			mode = 0o600
		}
		if rollback.existed {
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

func readVerifiedInstructionSource(repository, source, expectedHash string) ([]byte, error) {
	return readVerifiedManagedSource(repository, "instruction", source, expectedHash)
}

func readVerifiedManagedSource(repository, kind, source, expectedHash string) ([]byte, error) {
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
	if kind == "config" {
		if err := validateOpenCodeConfig(data); err != nil {
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
		if rollback.backupMade {
			if rollback.backupExisted {
				if err := atomicPrivateReplace(rollback.backup, rollback.backupBefore); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("restore instruction backup %s: %w", rollback.backup, err))
				}
			} else {
				if err := os.Remove(rollback.backup); err != nil && !errors.Is(err, os.ErrNotExist) {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("remove instruction backup %s: %w", rollback.backup, err))
				}
			}
		}
		if !rollback.existed {
			if rollback.afterHash != "" {
				if current, err := os.Stat(rollback.destination); err == nil && rollback.afterInfo != nil && os.SameFile(current, rollback.afterInfo) {
					if err := os.Remove(rollback.destination); err != nil {
						rollbackErrs = append(rollbackErrs, fmt.Errorf("remove created instruction %s: %w", rollback.destination, err))
					}
				}
			}
			continue
		}
		if rollback.afterHash == "" {
			if _, err := os.Lstat(rollback.destination); !errors.Is(err, os.ErrNotExist) {
				continue
			}
		} else {
			current, err := os.Stat(rollback.destination)
			if err != nil || rollback.afterInfo == nil || !os.SameFile(current, rollback.afterInfo) {
				continue
			}
		}
		if _, err := atomicInstructionFile(rollback.destination, rollback.before, rollback.mode, false); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore instruction %s: %w", rollback.destination, err))
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
	mutated bool
	info    os.FileInfo
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

func writeBackup(path string, data []byte, replace bool) error {
	if !replace {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("instruction adoption backup already exists")
		}
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	var err error
	if replace {
		err = atomicPrivateReplace(path, data)
	} else {
		err = atomicPrivateFile(path, data)
	}
	if err != nil {
		return err
	}
	if err := validateTrustedFile(path, "instruction backup"); err != nil {
		return err
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("instruction backup must be mode 0600")
	}
	hash, err := fileHash(path)
	if err != nil || hash != hashBytes(data) {
		return fmt.Errorf("instruction backup hash verification failed")
	}
	return nil
}

func atomicPrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".terran-backup-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("instruction backup changed during apply")
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	ok = true
	return nil
}

func atomicPrivateReplace(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".terran-backup-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return err
	}
	ok = true
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
