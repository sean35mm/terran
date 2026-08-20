//go:build darwin || linux

package terran

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func validateTrustedFileInfo(info os.FileInfo, path, description string, exactMode os.FileMode) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s %s must be a regular non-symlink file", description, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot inspect ownership of %s", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s %s must be owned by the effective user", description, path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("%s %s must have a single hard link", description, path)
	}
	if exactMode != 0 {
		if info.Mode().Perm() != exactMode {
			return fmt.Errorf("%s %s must be mode %04o", description, path, exactMode)
		}
	} else if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s %s must not be group- or world-writable", description, path)
	}
	return nil
}

func validateTrustedDirectory(path, description string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s %s must be a real directory", description, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot inspect ownership of %s", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s %s must be owned by the effective user", description, path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s %s must not be group- or world-writable", description, path)
	}
	return nil
}

func validateTrustedFile(path, description string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validateTrustedFileInfo(info, path, description, 0)
}

func readTrustedFile(path, description string, max int64, exactMode os.FileMode) ([]byte, os.FileMode, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if err := validateTrustedFileInfo(before, path, description, exactMode); err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !os.SameFile(before, opened) {
		return nil, 0, fmt.Errorf("%s %s changed while opening", description, path)
	}
	if err := validateTrustedFileInfo(opened, path, description, exactMode); err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) > max {
		return nil, 0, fmt.Errorf("%s exceeds %d bytes", path, max)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) {
		return nil, 0, fmt.Errorf("%s %s changed while reading", description, path)
	}
	if err := validateTrustedFileInfo(after, path, description, exactMode); err != nil {
		return nil, 0, err
	}
	return data, opened.Mode().Perm(), nil
}

func validateTargetRoot(path string) error {
	if err := validateTrustedDirectory(filepath.Dir(path), "target root parent"); err != nil {
		return err
	}
	return validateTrustedDirectory(path, "target root")
}

func validateTrustedSource(repository, source string) error {
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil || canonicalRepository != repository {
		return fmt.Errorf("repository path is no longer canonical")
	}
	if err := validateTrustedDirectory(repository, "repository"); err != nil {
		return err
	}
	if !contained(repository, source) {
		return fmt.Errorf("source %s escapes repository", source)
	}
	relative, err := filepath.Rel(repository, source)
	if err != nil {
		return err
	}
	current := repository
	for _, component := range splitRelativePath(relative) {
		current = filepath.Join(current, component)
		if err := validateTrustedDirectory(current, "source directory"); err != nil {
			return err
		}
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil || canonicalSource != source || !contained(repository, canonicalSource) {
		return fmt.Errorf("source %s is no longer canonical beneath the repository", source)
	}
	if err := validateTrustedFile(filepath.Join(source, "SKILL.md"), "SKILL.md"); err != nil {
		return err
	}
	return nil
}

func validateTrustedInstructionSource(repository, source string) error {
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil || canonicalRepository != repository {
		return fmt.Errorf("repository path is no longer canonical")
	}
	if !contained(repository, source) {
		return fmt.Errorf("source %s escapes repository", source)
	}
	relative, err := filepath.Rel(repository, filepath.Dir(source))
	if err != nil {
		return err
	}
	current := repository
	for _, component := range splitRelativePath(relative) {
		current = filepath.Join(current, component)
		if err := validateTrustedDirectory(current, "instruction source directory"); err != nil {
			return err
		}
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil || canonicalSource != source || !contained(repository, canonicalSource) {
		return fmt.Errorf("instruction source %s is not canonical beneath the repository", source)
	}
	return validateTrustedFile(source, "instruction source")
}

func validateInstructionParent(path string) error {
	parent := filepath.Dir(path)
	if err := validateTrustedDirectory(filepath.Dir(parent), "instruction target parent ancestor"); err != nil {
		return err
	}
	return validateTrustedDirectory(parent, "instruction target parent")
}

func splitRelativePath(path string) []string {
	if path == "." {
		return nil
	}
	var parts []string
	for path != "." {
		dir, base := filepath.Split(path)
		parts = append([]string{base}, parts...)
		path = filepath.Clean(dir)
	}
	return parts
}
