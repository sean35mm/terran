package terran

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func Status(target string) (StatusResult, error) {
	plan, err := Plan(target)
	if err != nil {
		return StatusResult{}, err
	}
	result := StatusResult{SchemaVersion: SchemaVersion, Clean: true}
	for _, action := range plan.Actions {
		item := StatusItem{ID: action.ID, Kind: action.Kind, Skill: action.Skill, Target: action.Target, Name: action.Name, Source: action.Source, Destination: action.Destination, Detail: action.Reason}
		switch action.Action {
		case "noop":
			item.Status = "ok"
		case "create":
			item.Status = "missing"
		case "adopt", "replace", "record", "update":
			item.Status = "pending"
		case "remove", "restore", "release":
			item.Status = "orphaned"
		case "blocked_collision":
			item.Status = "collision"
		case "blocked_drift":
			item.Status = "drift"
		case "held", "excluded":
			item.Status = action.Action
		}
		if item.Status != "ok" && !inert(action) {
			result.Clean = false
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func Doctor(buildVersion string) DoctorResult {
	result := DoctorResult{SchemaVersion: SchemaVersion, Healthy: true}
	add := func(name, status, message string) {
		result.Checks = append(result.Checks, Check{name, status, message})
		if status == "fail" {
			result.Healthy = false
		}
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		add("platform", "ok", runtime.GOOS+" is supported")
	} else {
		add("platform", "fail", runtime.GOOS+" is unsupported")
	}
	paths, err := ResolvePaths()
	if err != nil {
		add("paths", "fail", err.Error())
		return result
	}
	add("paths", "ok", "HOME and XDG paths are absolute")
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		add("enrollment", "fail", err.Error())
		return result
	}
	add("enrollment", "ok", fmt.Sprintf("%s at %s", enrollment.RepositoryID, enrollment.RepositoryPath))
	add("holds", "info", fmt.Sprintf("%d held item(s) on this machine", len(enrollment.Holds)))
	held := make(map[string]bool, len(enrollment.Holds))
	for _, id := range enrollment.Holds {
		held[id] = true
	}
	if err := validateTrustedStateFile(paths.ConfigFile, "config.json"); err != nil {
		add("config_permissions", "fail", err.Error())
	} else {
		add("config_permissions", "ok", "config.json is a trusted mode-0600 state file")
	}
	for _, dir := range []string{paths.ConfigDir, paths.StateDir} {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm()&0o077 != 0 {
			add("directory_permissions", "fail", dir+" must be user-private")
		} else {
			add("directory_permissions", "ok", dir+" is user-private")
		}
	}
	for _, file := range []string{paths.Receipt, paths.Lock} {
		if _, err := os.Lstat(file); errors.Is(err, os.ErrNotExist) {
			add("state_permissions", "warn", file+" does not exist yet")
		} else if err != nil {
			add("state_permissions", "fail", err.Error())
		} else if err := validateTrustedStateFile(file, filepath.Base(file)); err != nil {
			add("state_permissions", "fail", err.Error())
		} else {
			add("state_permissions", "ok", file+" is a trusted mode-0600 state file")
		}
	}
	catalogs, err := LoadCatalogs(enrollment)
	if err != nil {
		add("manifest", "fail", err.Error())
	} else {
		loaded := catalogs.Primary
		add("manifest", "ok", loaded.Manifest.Version+" "+loaded.Fingerprint)
		if catalogs.Overlay != nil {
			add("overlay", "ok", catalogs.Overlay.Manifest.ID+" "+catalogs.Overlay.Manifest.Version+" at "+catalogs.Overlay.Repository)
		}
		if buildVersion != loaded.Manifest.Version && !strings.HasPrefix(buildVersion, loaded.Manifest.Version+"-") {
			add("binary_version", "warn", "binary "+buildVersion+" differs from catalog "+loaded.Manifest.Version)
		} else {
			add("binary_version", "ok", buildVersion+" matches catalog "+loaded.Manifest.Version)
		}
		for _, catalog := range catalogs.list() {
			for _, tool := range catalog.Manifest.Tools {
				if !platformIncluded(tool.Platforms) {
					continue
				}
				if _, err := exec.LookPath(tool.Name); err != nil {
					add("tool:"+tool.Name, "fail", tool.Name+" not found on PATH; follow terran-provision tools step")
				} else {
					add("tool:"+tool.Name, "ok", tool.Name+" found on PATH")
				}
			}
		}
	}
	for _, spec := range targetSpecs {
		if spec.Kind != "skill" {
			continue
		}
		target := spec.ID
		root, _ := spec.Dest(paths, "")
		_, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			add("target_"+target, "warn", root+" does not exist yet")
		} else if err != nil {
			add("target_"+target, "fail", err.Error())
		} else if err := validateTargetRoot(root); err != nil {
			add("target_"+target, "fail", err.Error())
		} else {
			add("target_"+target, "ok", root+" is a trusted real directory")
		}
	}
	if receipt, err := LoadReceipt(paths, enrollment); errors.Is(err, os.ErrNotExist) {
		add("instruction_receipt", "warn", "no instruction receipt exists yet")
		add("config_receipt", "warn", "no config receipt exists yet")
	} else if err != nil {
		add("instruction_receipt", "fail", err.Error())
		add("config_receipt", "fail", err.Error())
	} else {
		fileHealthy, files := true, false
		for _, managed := range receipt.Managed {
			if managed.Kind == "file" {
				files = true
				if !held[ItemID(managed.Kind, managed.Target, managed.Name)] && !doctorManagedFile(paths, enrollment, managed.Kind, managed, add) {
					fileHealthy = false
				}
			}
		}
		if files && fileHealthy {
			add("file_receipt", "ok", "file receipt paths, hashes, modes, and backups are valid")
		}
		instructionHealthy := true
		for _, managed := range receipt.Managed {
			if managed.Kind == "instruction" && !held[ItemID(managed.Kind, managed.Target, "")] && !doctorManagedFile(paths, enrollment, managed.Kind, managed, add) {
				instructionHealthy = false
			}
		}
		if instructionHealthy {
			add("instruction_receipt", "ok", "instruction receipt paths, hashes, modes, and backups are valid")
		}
		configHealthy := true
		for _, managed := range receipt.Managed {
			if managed.Kind == "config" && !held[ItemID(managed.Kind, managed.Target, "")] && !doctorManagedFile(paths, enrollment, managed.Kind, managed, add) {
				configHealthy = false
			}
		}
		if configHealthy {
			add("config_receipt", "ok", "config receipt paths, hashes, modes, and backups are valid")
		}
	}
	status, err := Status("all")
	if err != nil {
		add("projections", "fail", err.Error())
	} else if !status.Clean {
		add("projections", "fail", "one or more projections are not clean")
	} else {
		add("projections", "ok", "all projections are clean")
	}
	executable, executableErr := os.Executable()
	pathExecutable, pathErr := exec.LookPath("terran")
	if executableErr != nil || pathErr != nil {
		add("binary_path", "warn", "terran is not discoverable on PATH")
	} else {
		executable, _ = filepath.EvalSymlinks(executable)
		pathExecutable, _ = filepath.EvalSymlinks(pathExecutable)
		if executable != pathExecutable {
			add("binary_path", "warn", "running binary differs from terran on PATH")
		} else {
			add("binary_path", "ok", executable+" ("+buildVersion+")")
		}
	}
	return result
}

func doctorManagedFile(paths Paths, enrollment Enrollment, kind string, managed ReceiptManaged, add func(string, string, string)) bool {
	name := kind + "_" + managedLabel(managed.Target, managed.Name)
	destination, destinationErr := managedFileDestination(paths, kind, managed.Target, managed.Name)
	if destinationErr != nil || destination != managed.Destination || contained(enrollment.RepositoryPath, destination) || (enrollment.OverlayPath != "" && contained(enrollment.OverlayPath, destination)) {
		add(name, "fail", "fixed "+kind+" destination is invalid")
		return false
	}
	if err := validateInstructionParent(destination); err != nil {
		add(name, "fail", err.Error())
		return false
	}
	if err := validateTrustedFile(destination, "managed "+kind); err != nil {
		add(name, "fail", err.Error())
		return false
	}
	hash, err := fileHash(destination)
	if err != nil || hash != managed.AppliedHash {
		add(name, "fail", "managed "+kind+" hash mismatch")
		return false
	}
	if err := validateExecutableMode(kind, managed.Target, destination); err != nil {
		add(name, "fail", err.Error())
		return false
	}
	if managed.Origin == "adopted" {
		if err := validateBackup(paths, managed); err != nil {
			add(name, "fail", err.Error())
			return false
		}
	}
	add(name, "ok", destination+" is clean")
	return true
}

func validateTrustedStateFile(path, description string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validateTrustedFileInfo(info, path, description, 0o600)
}
