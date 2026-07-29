package toolconfig

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupFile reads path and writes a timestamped snapshot next to the source
// file. backupRoot and resource are retained for source compatibility with
// older callers; Commit uses the source path directly.
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
	return backupBytesNextToSource(data, path, secret, info.Mode().Perm())
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

// backupBytesWithMode is the legacy central-root implementation retained for
// callers of BackupBytes. Commit does not use it.
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

func backupBytesNextToSource(data []byte, sourcePath string, secret bool, mode os.FileMode) (string, error) {
	sourcePath = filepath.Clean(sourcePath)
	dir := filepath.Dir(sourcePath)
	baseName := filepath.Base(sourcePath)
	if baseName == "." || baseName == string(filepath.Separator) || baseName == "" {
		baseName = "backup"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create source backup directory: %w", err)
	}

	ext := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, ext)
	timestamp := time.Now().Local().Format("20060102150405")
	backupMode := mode.Perm()
	if backupMode == 0 {
		backupMode = 0o644
	}
	if secret {
		backupMode = 0o600
	}
	for attempt := 0; ; attempt++ {
		suffix := "." + timestamp
		if attempt > 0 {
			suffix += fmt.Sprintf("-%d", attempt)
		}
		destination := filepath.Join(dir, stem+suffix+ext)
		err := writeBackupExclusive(destination, data, backupMode)
		if err == nil {
			return destination, nil
		}
		if os.IsExist(err) {
			continue
		}
		return "", fmt.Errorf("write source backup: %w", err)
	}
}

func writeBackupExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// IsSourceBackup reports whether backupPath is a generated timestamped backup
// for sourcePath. It requires the same directory and the source stem/ext.
func IsSourceBackup(sourcePath, backupPath string) bool {
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		return false
	}
	backupAbs, err := filepath.Abs(backupPath)
	if err != nil || filepath.Clean(filepath.Dir(sourceAbs)) != filepath.Clean(filepath.Dir(backupAbs)) {
		return false
	}
	sourceBase := filepath.Base(sourceAbs)
	backupBase := filepath.Base(backupAbs)
	ext := filepath.Ext(sourceBase)
	stem := strings.TrimSuffix(sourceBase, ext)
	prefix := stem + "."
	if !strings.HasPrefix(backupBase, prefix) || !strings.HasSuffix(backupBase, ext) {
		return false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(backupBase, prefix), ext)
	if len(middle) < len("20060102150405") {
		return false
	}
	for _, digit := range middle[:len("20060102150405")] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	remainder := middle[len("20060102150405"):]
	if remainder == "" {
		return true
	}
	if !strings.HasPrefix(remainder, "-") || len(remainder) == 1 {
		return false
	}
	for _, digit := range remainder[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// PruneSourceBackups retains the newest keep generated backups for sourcePath
// by filesystem modification time. keep == 0 means keep all backups.
func PruneSourceBackups(sourcePath string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(filepath.Dir(sourcePath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read source backup directory: %w", err)
	}
	type backupEntry struct {
		name string
		mod  time.Time
	}
	backups := make([]backupEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !IsSourceBackup(sourcePath, filepath.Join(filepath.Dir(sourcePath), entry.Name())) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat source backup %q: %w", entry.Name(), err)
		}
		backups = append(backups, backupEntry{name: entry.Name(), mod: info.ModTime()})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].mod.Equal(backups[j].mod) {
			return backups[i].name < backups[j].name
		}
		return backups[i].mod.Before(backups[j].mod)
	})
	if len(backups) <= keep {
		return nil
	}
	for _, entry := range backups[:len(backups)-keep] {
		if err := os.Remove(filepath.Join(filepath.Dir(sourcePath), entry.name)); err != nil {
			return fmt.Errorf("remove source backup %q: %w", entry.name, err)
		}
	}
	return nil
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
