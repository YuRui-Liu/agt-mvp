//go:build !windows

package mocksvc

import (
	"errors"
	"fmt"
	"os"
)

func secureStoreDirectory(parent string) error {
	info, err := os.Lstat(parent)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("identity store directory must not be a symlink")
		}
		if !info.IsDir() {
			return errors.New("identity store parent is not a directory")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return errors.New("identity store directory permissions are not private")
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("inspect identity store directory: %w", err)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create identity store directory: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("secure new identity store directory: %w", err)
	}
	return nil
}
