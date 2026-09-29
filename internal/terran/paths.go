package terran

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct {
	Home       string
	ConfigBase string
	StateBase  string
	ConfigDir  string
	StateDir   string
	BackupDir  string
	ConfigFile string
	Receipt    string
	Lock       string
	CodexHome  string
}

func ResolvePaths() (Paths, error) {
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return Paths{}, errors.New("HOME must be an absolute path")
	}
	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	stateBase := os.Getenv("XDG_STATE_HOME")
	if stateBase == "" {
		stateBase = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(configBase) || !filepath.IsAbs(stateBase) {
		return Paths{}, fmt.Errorf("HOME and XDG paths must be absolute")
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexHome) {
		return Paths{}, fmt.Errorf("CODEX_HOME must be an absolute path")
	}
	configDir := filepath.Join(filepath.Clean(configBase), "terran")
	stateDir := filepath.Join(filepath.Clean(stateBase), "terran")
	return Paths{home, filepath.Clean(configBase), filepath.Clean(stateBase), configDir, stateDir, filepath.Join(stateDir, "backups"), filepath.Join(configDir, "config.json"), filepath.Join(stateDir, "receipt.json"), filepath.Join(stateDir, "lock"), filepath.Clean(codexHome)}, nil
}

func instructionDestination(paths Paths, target string) (string, error) {
	spec, ok := lookupTarget("instruction", target)
	if !ok {
		return "", fmt.Errorf("unsupported instruction target %q", target)
	}
	return spec.Dest(paths, "")
}

func configDestination(paths Paths, target string) (string, error) {
	spec, ok := lookupTarget("config", target)
	if !ok {
		return "", fmt.Errorf("unsupported config target %q", target)
	}
	return spec.Dest(paths, "")
}

func managedFileDestination(paths Paths, kind, target, name string) (string, error) {
	spec, ok := lookupTarget(kind, target)
	if !ok {
		if kind == "config" || kind == "file" {
			return "", fmt.Errorf("unsupported %s target %q", kind, target)
		}
		return "", fmt.Errorf("unsupported instruction target %q", target)
	}
	if kind == "file" {
		if err := validateFileName(spec, name); err != nil {
			return "", err
		}
	} else if name != "" {
		return "", fmt.Errorf("%s target %q does not take a name", kind, target)
	}
	return spec.Dest(paths, name)
}

var errDestinationSymlink = errors.New("destination path contains a symlink")

// resolveDestination returns a managed-file or settings destination with its
// deepest existing ancestor resolved through symlinks. The fixed root it lives
// under (the deepest of HOME, XDG_CONFIG_HOME, and CODEX_HOME) may itself be a
// symlink, but no existing directory below that root may be: a symlinked
// ancestor would redirect writes away from the fixed destination.
func resolveDestination(paths Paths, destination string) (string, error) {
	root := ""
	for _, candidate := range []string{paths.Home, paths.ConfigBase, paths.CodexHome} {
		if contained(candidate, destination) && len(candidate) > len(root) {
			root = candidate
		}
	}
	if root == "" {
		return "", fmt.Errorf("destination %s is outside HOME, XDG_CONFIG_HOME, and CODEX_HOME", destination)
	}
	existing := filepath.Dir(destination)
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", errDestinationSymlink
	}
	if contained(root, existing) {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		below, _ := filepath.Rel(root, existing)
		if resolved != filepath.Join(resolvedRoot, below) {
			return "", errDestinationSymlink
		}
	}
	rest, _ := filepath.Rel(existing, destination)
	return filepath.Join(resolved, rest), nil
}

func instructionBackup(paths Paths, target string) string {
	return filepath.Join(paths.BackupDir, target, "original")
}

// managedBackup keeps instruction and config backups at their v0.3 paths;
// named files get one backup directory per item.
func managedBackup(paths Paths, kind, target, name string) string {
	if kind == "file" {
		return filepath.Join(paths.BackupDir, kind, target, name, "original")
	}
	return instructionBackup(paths, target)
}

func targetRoot(home, target string) (string, error) {
	return skillDestination(Paths{Home: home}, target, "")
}

func skillDestination(paths Paths, target, name string) (string, error) {
	spec, ok := lookupTarget("skill", target)
	if !ok {
		return "", fmt.Errorf("unsupported target %q", target)
	}
	return spec.Dest(paths, name)
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	if err := validateTrustedDirectory(path, "private state directory"); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func ensureTargetRoot(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		return validateTargetRoot(path)
	}
	if err != nil {
		return err
	}
	_ = info
	return validateTargetRoot(path)
}
