package toolconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data through a temporary file in the destination
// directory and then renames it into place. Existing file permissions win over
// mode unless enforceMode is true.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode, enforceMode bool) error {
	return writeFileAtomic(path, data, mode, enforceMode, nil)
}

// writeFileAtomic is the engine variant that can revalidate a destination
// after the temporary file is synced and immediately before rename.
func writeFileAtomic(path string, data []byte, mode fs.FileMode, enforceMode bool, beforeRename func() error) error {
	return writeFileAtomicWithState(path, data, mode, enforceMode, beforeRename, nil)
}

func writeFileAtomicWithState(path string, data []byte, mode fs.FileMode, enforceMode bool, beforeRename func() error, renamed *bool) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat destination: %w", err)
	}
	targetMode := mode.Perm()
	if exists && !enforceMode {
		targetMode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(targetMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if beforeRename != nil {
		if err := beforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temporary file: %w", err)
	}
	removeTemp = false
	if renamed != nil {
		*renamed = true
	}
	if enforceMode {
		if err := os.Chmod(path, mode.Perm()); err != nil {
			return fmt.Errorf("enforce destination permissions: %w", err)
		}
	}

	// Syncing the directory makes the rename durable on filesystems that expose
	// directory fsync. The file sync above remains the important data barrier.
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open destination directory: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil && !errors.Is(err, fs.ErrInvalid) {
		return fmt.Errorf("sync destination directory: %w", err)
	}
	return nil
}
