package securefile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteAtomic installs contents at path with mode 0600 using a temporary file
// in the same directory. Callers are responsible for securing the directory.
func WriteAtomic(path, temporaryPattern string, contents []byte) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), temporaryPattern)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	if err = temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary file: %w", err)
	}
	if _, err = temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install file: %w", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("secure installed file: %w", err)
	}
	return nil
}
