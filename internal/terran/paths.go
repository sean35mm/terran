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
	configDir := filepath.Join(filepath.Clean(configBase), "terran")
	stateDir := filepath.Join(filepath.Clean(stateBase), "terran")
	return Paths{home, filepath.Clean(configBase), filepath.Clean(stateBase), configDir, stateDir, filepath.Join(stateDir, "backups"), filepath.Join(configDir, "config.json"), filepath.Join(stateDir, "receipt.json"), filepath.Join(stateDir, "lock")}, nil
}

func instructionDestination(paths Paths, target string) (string, error) {
	switch target {
	case "claude-global":
		return filepath.Join(paths.Home, ".claude", "CLAUDE.md"), nil
	case "opencode-global":
		return filepath.Join(paths.ConfigBase, "opencode", "AGENTS.md"), nil
	default:
		return "", fmt.Errorf("unsupported instruction target %q", target)
	}
}

func configDestination(paths Paths, target string) (string, error) {
	switch target {
	case "opencode-config":
		return filepath.Join(paths.ConfigBase, "opencode", "opencode.json"), nil
	default:
		return "", fmt.Errorf("unsupported config target %q", target)
	}
}

func managedFileDestination(paths Paths, kind, target string) (string, error) {
	if kind == "config" {
		return configDestination(paths, target)
	}
	return instructionDestination(paths, target)
}

func instructionBackup(paths Paths, target string) string {
	return filepath.Join(paths.BackupDir, target, "original")
}

func targetRoot(home, target string) (string, error) {
	switch target {
	case "agents":
		return filepath.Join(home, ".agents", "skills"), nil
	case "claude":
		return filepath.Join(home, ".claude", "skills"), nil
	default:
		return "", fmt.Errorf("unsupported target %q", target)
	}
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
