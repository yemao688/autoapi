package toolconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type commitSnapshot struct {
	change     FileChange
	beforeMode fs.FileMode
	exists     bool
	backupPath string
}

// commitTestHook is intentionally unexported test instrumentation. It lets
// transaction tests change the filesystem between phases without adding a
// production option to Commit.
var commitTestHook func(phase string, index int)

// Commit atomically applies a ChangeSet. Validation and all snapshots happen
// before the first backup or write, which keeps drift and lost-update failures
// side-effect free.
func Commit(cs *ChangeSet, opts CommitOpts) (*CommitResult, error) {
	if cs == nil {
		return nil, fmt.Errorf("toolconfig: nil change set: %w", ErrInvalidPreset)
	}
	if len(cs.Changes) == 0 {
		return nil, fmt.Errorf("toolconfig: empty changeset: %w", ErrInvalidPreset)
	}
	if opts.BackupRoot == "" {
		return nil, fmt.Errorf("toolconfig: backup root is empty")
	}

	snapshots, err := validateAndSnapshot(cs, opts)
	if err != nil {
		return nil, err
	}
	if err := ensureChangeParents(snapshots); err != nil {
		return nil, err
	}
	runCommitTestHook("after-validation", -1)

	backups := make([]string, 0, len(snapshots))
	for i := range snapshots {
		if !snapshots[i].exists {
			continue
		}
		backupPath, err := backupBytesNextToSource(
			snapshots[i].change.Before,
			snapshots[i].change.Path,
			snapshots[i].change.Secret,
			snapshots[i].beforeMode,
		)
		if err != nil {
			cleanupErr := removeBackups(backups)
			return nil, commitFailure(err, cleanupErr)
		}
		snapshots[i].backupPath = backupPath
		if backupPath != "" {
			backups = append(backups, backupPath)
		}
	}
	runCommitTestHook("after-backups", -1)

	attempted := make([]int, 0, len(snapshots))
	for i := range snapshots {
		change := snapshots[i].change
		mode := fs.FileMode(change.Mode)
		if change.Secret {
			mode = 0o600
		} else if mode == 0 {
			mode = 0o644
		}
		renamed := false
		if err := writeFileAtomicWithState(change.Path, change.After, mode, change.Secret, func() error {
			return revalidateBeforeRename(change)
		}, &renamed); err != nil {
			if renamed {
				attempted = append(attempted, i)
			}
			rollbackErr := rollback(snapshots, attempted)
			return nil, commitFailure(err, rollbackErr)
		}
		attempted = append(attempted, i)
		runCommitTestHook("after-write", i)
		afterHash, err := HashFile(change.Path)
		if err != nil {
			rollbackErr := rollback(snapshots, attempted)
			return nil, commitFailure(fmt.Errorf("hash post-write resource %s: %w", change.Resource, err), rollbackErr)
		}
		if afterHash != hashContent(change.After) {
			rollbackErr := rollback(snapshots, attempted)
			integrityErr := fmt.Errorf("post-write hash mismatch for resource %s: expected %q, found %q: %w", change.Resource, hashContent(change.After), afterHash, ErrConflict)
			return nil, commitFailure(integrityErr, rollbackErr)
		}
	}

	result := &CommitResult{Files: make([]FileResult, len(snapshots))}
	for i := range snapshots {
		hash, err := HashFile(snapshots[i].change.Path)
		if err != nil {
			rollbackErr := rollback(snapshots, attempted)
			return nil, commitFailure(fmt.Errorf("hash committed resource %s: %w", snapshots[i].change.Resource, err), rollbackErr)
		}
		if hash != hashContent(snapshots[i].change.After) {
			rollbackErr := rollback(snapshots, attempted)
			integrityErr := fmt.Errorf("committed hash mismatch for resource %s: expected %q, found %q: %w", snapshots[i].change.Resource, hashContent(snapshots[i].change.After), hash, ErrConflict)
			return nil, commitFailure(integrityErr, rollbackErr)
		}
		result.Files[i] = FileResult{
			Resource:   snapshots[i].change.Resource,
			Path:       snapshots[i].change.Path,
			BackupPath: snapshots[i].backupPath,
			Hash:       hash,
		}
	}

	runCommitTestHook("before-prune", -1)
	pruned := make(map[Resource]bool)
	for _, snapshot := range snapshots {
		if pruned[snapshot.change.Resource] {
			continue
		}
		if err := PruneSourceBackups(snapshot.change.Path, opts.KeepBackups); err != nil {
			// Pruning is housekeeping; a successful transaction remains successful
			// when cleanup cannot remove an old backup.
			continue
		}
		pruned[snapshot.change.Resource] = true
	}
	return result, nil
}

func validateAndSnapshot(cs *ChangeSet, opts CommitOpts) ([]commitSnapshot, error) {
	snapshots := make([]commitSnapshot, len(cs.Changes))
	seenResources := make(map[Resource]bool, len(cs.Changes))
	seenPaths := make(map[string]bool, len(cs.Changes))
	for i, change := range cs.Changes {
		if change.Resource == "" {
			return nil, fmt.Errorf("toolconfig: change %d has an empty resource: %w", i, ErrConflict)
		}
		if change.Path == "" || !filepath.IsAbs(change.Path) {
			return nil, fmt.Errorf("toolconfig: resource %s has a non-absolute path %q: %w", change.Resource, change.Path, ErrConflict)
		}
		if _, err := backupResourceDir(opts.BackupRoot, change.Resource); err != nil {
			return nil, fmt.Errorf("toolconfig: resource %s has an invalid backup path: %w", change.Resource, err)
		}
		if seenResources[change.Resource] {
			return nil, fmt.Errorf("toolconfig: duplicate resource %s: %w", change.Resource, ErrConflict)
		}
		if seenPaths[change.Path] {
			return nil, fmt.Errorf("toolconfig: duplicate path %s: %w", change.Path, ErrConflict)
		}
		seenResources[change.Resource] = true
		seenPaths[change.Path] = true

		if hashBytes(change.Before) != change.BeforeHash {
			return nil, fmt.Errorf("toolconfig: resource %s has an invalid before snapshot: %w", change.Resource, ErrConflict)
		}
		currentHash, err := HashFile(change.Path)
		if err != nil {
			return nil, fmt.Errorf("toolconfig: hash current resource %s: %w", change.Resource, err)
		}
		if expected, ok := opts.ExpectedHashes[change.Resource]; ok && !opts.AllowDrift && currentHash != expected {
			return nil, fmt.Errorf("toolconfig: resource %s expected hash %q, found %q: %w", change.Resource, expected, currentHash, ErrDrifted)
		}
		if currentHash != change.BeforeHash {
			return nil, fmt.Errorf("toolconfig: lost update for resource %s: before hash %q, found %q: %w", change.Resource, change.BeforeHash, currentHash, ErrConflict)
		}

		info, statErr := os.Stat(change.Path)
		exists := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("toolconfig: stat current resource %s: %w", change.Resource, statErr)
		}
		if exists && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("toolconfig: resource %s is not a regular file: %w", change.Resource, ErrUnsafeShape)
		}
		if (change.Before == nil) != !exists {
			return nil, fmt.Errorf("toolconfig: resource %s before snapshot presence disagrees with disk: %w", change.Resource, ErrConflict)
		}

		beforeMode := fs.FileMode(0)
		if exists {
			beforeMode = info.Mode().Perm()
		}
		snapshots[i] = commitSnapshot{
			change:     change,
			beforeMode: beforeMode,
			exists:     exists,
		}
	}
	for i, check := range cs.Checks {
		if check.Resource == "" {
			return nil, fmt.Errorf("toolconfig: check %d has an empty resource: %w", i, ErrConflict)
		}
		if check.Path == "" || !filepath.IsAbs(check.Path) {
			return nil, fmt.Errorf("toolconfig: check resource %s has a non-absolute path %q: %w", check.Resource, check.Path, ErrConflict)
		}
		if _, err := backupResourceDir(opts.BackupRoot, check.Resource); err != nil {
			return nil, fmt.Errorf("toolconfig: check resource %s has an invalid path: %w", check.Resource, err)
		}
		currentHash, err := HashFile(check.Path)
		if err != nil {
			return nil, fmt.Errorf("toolconfig: hash check resource %s: %w", check.Resource, err)
		}
		if currentHash != check.ExpectedHash {
			return nil, fmt.Errorf("toolconfig: check resource %s expected hash %q, found %q: %w", check.Resource, check.ExpectedHash, currentHash, ErrDrifted)
		}
	}
	return snapshots, nil
}

func ensureChangeParents(snapshots []commitSnapshot) error {
	for _, snapshot := range snapshots {
		if err := os.MkdirAll(filepath.Dir(snapshot.change.Path), 0o755); err != nil {
			return fmt.Errorf("toolconfig: create parent directory for resource %s: %w", snapshot.change.Resource, err)
		}
	}
	return nil
}

func revalidateBeforeRename(change FileChange) error {
	currentHash, err := HashFile(change.Path)
	if err != nil {
		return fmt.Errorf("toolconfig: revalidate resource %s: %w", change.Resource, err)
	}
	if currentHash != change.BeforeHash {
		return fmt.Errorf("toolconfig: resource %s changed before rename: before hash %q, found %q: %w", change.Resource, change.BeforeHash, currentHash, ErrConflict)
	}
	return nil
}

func rollback(snapshots []commitSnapshot, attempted []int) error {
	var rollbackErrors []error
	for i := len(attempted) - 1; i >= 0; i-- {
		snapshot := snapshots[attempted[i]]
		if !snapshot.exists {
			if err := removeRollbackTarget(snapshot.change.Path); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove created resource %s: %w", snapshot.change.Resource, err))
			}
			continue
		}

		mode := snapshot.beforeMode
		if snapshot.change.Secret {
			mode = 0o600
		} else if mode == 0 {
			mode = 0o644
		}
		if err := WriteFileAtomic(snapshot.change.Path, snapshot.change.Before, mode, true); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore resource %s: %w", snapshot.change.Resource, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func removeRollbackTarget(path string) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	// If the parent was replaced by a regular file, the target cannot exist at
	// that path. Treat it as already absent so an unrelated rollback can finish.
	parentInfo, statErr := os.Stat(filepath.Dir(path))
	if os.IsNotExist(statErr) || (statErr == nil && !parentInfo.IsDir()) {
		return nil
	}
	return err
}

func removeBackups(paths []string) error {
	var cleanupErrors []error
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove backup %s: %w", path, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func commitFailure(err, rollbackErr error) error {
	if rollbackErr == nil {
		return fmt.Errorf("toolconfig: commit failed: %w (rollback completed)", err)
	}
	return fmt.Errorf("toolconfig: commit failed: %w (rollback failed: %v)", err, rollbackErr)
}

func runCommitTestHook(phase string, index int) {
	if commitTestHook != nil {
		commitTestHook(phase, index)
	}
}

func hashBytes(data []byte) string {
	if data == nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func hashContent(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
