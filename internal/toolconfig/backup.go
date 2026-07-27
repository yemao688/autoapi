package toolconfig

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// BackupFile reads path and atomically writes its snapshot into
// backupRoot/resource. Backup names sort in chronological order, so lexical
// order is sufficient for pruning.
func BackupFile(path, backupRoot string, resource Resource, secret bool) (string, error) {
	src, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return "", fmt.Errorf("stat source file: %w", err)
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return "", fmt.Errorf("read source file: %w", err)
	}
	return backupBytesWithMode(data, backupRoot, resource, filepath.Base(path), secret, info.Mode().Perm())
}

// BackupBytes atomically writes an already-captured file snapshot. Callers
// that have planned a ChangeSet should use this instead of re-reading the
// live target.
func BackupBytes(data []byte, backupRoot string, resource Resource, basename string, secret bool) (string, error) {
	return backupBytesWithMode(data, backupRoot, resource, basename, secret, 0o644)
}

// BackupFileBytes is an explicit alias for callers that prefer the file-based
// name while supplying snapshot bytes.
func BackupFileBytes(data []byte, backupRoot string, resource Resource, basename string, secret bool) (string, error) {
	return BackupBytes(data, backupRoot, resource, basename, secret)
}

func backupBytesWithMode(data []byte, backupRoot string, resource Resource, basename string, secret bool, mode os.FileMode) (string, error) {
	dir, err := backupResourceDir(backupRoot, resource)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}

	baseName := filepath.Base(basename)
	if baseName == "." || baseName == string(filepath.Separator) || baseName == "" {
		baseName = "backup"
	}
	base := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + baseName
	destination := filepath.Join(dir, base)
	backupMode := mode.Perm()
	if backupMode == 0 {
		backupMode = 0o644
	}
	if secret {
		backupMode = 0o600
	}
	for attempt := 1; ; attempt++ {
		if _, err := os.Stat(destination); err == nil {
			destination = filepath.Join(dir, fmt.Sprintf("%s-%d", base, attempt))
			continue
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat backup destination: %w", err)
		}
		if err := writeFileAtomic(destination, data, backupMode, true, nil); err != nil {
			return "", fmt.Errorf("write backup file: %w", err)
		}
		return destination, nil
	}
}

// PruneBackups retains the newest keep backups by filename and removes older
// entries from the resource directory. keep == 0 means keep all backups.
func PruneBackups(backupRoot string, resource Resource, keep int) error {
	if keep <= 0 {
		return nil
	}
	dir, err := backupResourceDir(backupRoot, resource)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read backup directory: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if keep < 0 {
		keep = 0
	}
	if keep >= len(names) {
		return nil
	}
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("remove backup %q: %w", name, err)
		}
	}
	return nil
}

func backupResourceDir(backupRoot string, resource Resource) (string, error) {
	resourcePath := filepath.FromSlash(string(resource))
	clean := filepath.Clean(resourcePath)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) ||
		len(clean) > 3 && clean[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("invalid backup resource %q", resource)
	}
	return filepath.Join(backupRoot, clean), nil
}
