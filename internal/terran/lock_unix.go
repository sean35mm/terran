//go:build darwin || linux

package terran

import (
	"fmt"
	"os"
	"syscall"
)

func withLock(path string, fn func() error) error {
	if err := ensurePrivateDir(pathDir(path)); err != nil {
		return err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock without following links: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Nlink != 1 {
		return fmt.Errorf("state lock must be a regular single-link file")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("state lock must be owned by the effective user")
	}
	if os.FileMode(stat.Mode).Perm() != 0o600 {
		return fmt.Errorf("state lock must be mode 0600")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
