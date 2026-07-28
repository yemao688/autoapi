package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitMultiFileHappyPathAndPruning(t *testing.T) {
	dir := t.TempDir()
	backupRoot := filepath.Join(dir, "backups")
	firstPath := filepath.Join(dir, "first.json")
	secondPath := filepath.Join(dir, "second.json")
	writeTestFile(t, firstPath, []byte("first-before"), 0o644)
	writeTestFile(t, secondPath, []byte("second-before"), 0o644)

	first := testFileChange(t, ResOpencodeConfig, firstPath, []byte("first-after"), false)
	second := testFileChange(t, ResOpencodeOmoSlim, secondPath, []byte("second-after"), false)
	result, err := Commit(&ChangeSet{Tool: ToolOpencode, Changes: []FileChange{first, second}}, CommitOpts{
		BackupRoot:  backupRoot,
		KeepBackups: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || result.Files[0].Resource != first.Resource || result.Files[1].Resource != second.Resource {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, file := range result.Files {
		got, err := HashFile(file.Path)
		if err != nil {
			t.Fatal(err)
		}
		if got != file.Hash || file.BackupPath == "" {
			t.Fatalf("result does not record hash/backup: %+v", file)
		}
	}

	first = testFileChange(t, ResOpencodeConfig, firstPath, []byte("first-final"), false)
	second = testFileChange(t, ResOpencodeOmoSlim, secondPath, []byte("second-final"), false)
	if _, err := Commit(&ChangeSet{Tool: ToolOpencode, Changes: []FileChange{first, second}}, CommitOpts{
		BackupRoot:  backupRoot,
		KeepBackups: 1,
	}); err != nil {
		t.Fatal(err)
	}
	assertBackupCount(t, backupRoot, ResOpencodeConfig, 1)
	assertBackupCount(t, backupRoot, ResOpencodeOmoSlim, 1)
}

func TestCommitExpectedDriftAbortsWithoutSideEffects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	before := []byte("before")
	writeTestFile(t, path, before, 0o644)
	change := testFileChange(t, ResOpencodeConfig, path, []byte("after"), false)
	_, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{
		BackupRoot:     filepath.Join(dir, "backups"),
		ExpectedHashes: map[Resource]string{ResOpencodeConfig: "wrong-hash"},
	})
	if !errors.Is(err, ErrDrifted) {
		t.Fatalf("expected ErrDrifted, got %v", err)
	}
	assertFileBytes(t, path, before)
	if _, statErr := os.Stat(filepath.Join(dir, "backups")); !os.IsNotExist(statErr) {
		t.Fatalf("drift created backup directory: %v", statErr)
	}
}

func TestCommitLostUpdateAbortsBeforeAnyWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeTestFile(t, path, []byte("snapshot"), 0o644)
	change := testFileChange(t, ResOpencodeConfig, path, []byte("after"), false)
	writeTestFile(t, path, []byte("external-change"), 0o644)

	_, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{BackupRoot: filepath.Join(dir, "backups")})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected lost-update conflict, got %v", err)
	}
	assertFileBytes(t, path, []byte("external-change"))
}

func TestCommitRollbackRestoresEarlierFile(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	firstPath := filepath.Join(dir, "first.json")
	secondPath := filepath.Join(blocked, "second.json")
	before := []byte("first-before")
	writeTestFile(t, firstPath, before, 0o644)
	first := testFileChange(t, ResOpencodeConfig, firstPath, []byte("first-after"), false)
	second := testFileChange(t, ResOpencodeOmoSlim, secondPath, []byte("second-after"), false)
	setCommitTestHook(t, func(phase string, _ int) {
		if phase != "after-backups" {
			return
		}
		if err := os.RemoveAll(blocked); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(blocked, []byte("blocking file"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	_, err := Commit(&ChangeSet{Changes: []FileChange{first, second}}, CommitOpts{BackupRoot: filepath.Join(dir, "backups")})
	if err == nil {
		t.Fatal("expected second write to fail")
	}
	assertFileBytes(t, firstPath, before)
	if _, statErr := os.Stat(secondPath); statErr == nil {
		t.Fatal("failed created target exists after rollback")
	}
}

func TestCommitRollbackRemovesEarlierCreatedFile(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	firstPath := filepath.Join(dir, "created.json")
	secondPath := filepath.Join(blocked, "second.json")
	first := testFileChange(t, ResOpencodeConfig, firstPath, []byte("created"), false)
	second := testFileChange(t, ResOpencodeOmoSlim, secondPath, []byte("second"), false)
	setCommitTestHook(t, func(phase string, _ int) {
		if phase != "after-backups" {
			return
		}
		if err := os.RemoveAll(blocked); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(blocked, []byte("blocking file"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	_, err := Commit(&ChangeSet{Changes: []FileChange{first, second}}, CommitOpts{BackupRoot: filepath.Join(dir, "backups")})
	if err == nil {
		t.Fatal("expected second write to fail")
	}
	if _, statErr := os.Stat(firstPath); !os.IsNotExist(statErr) {
		t.Fatalf("created target survived rollback: %v", statErr)
	}
}

func TestCommitSecretModeAndBackupMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	before := []byte("old-secret")
	writeTestFile(t, path, before, 0o644)
	change := testFileChange(t, ResCodexAuth, path, []byte("new-secret"), true)
	result, err := Commit(&ChangeSet{Tool: ToolCodex, Changes: []FileChange{change}}, CommitOpts{
		BackupRoot: filepath.Join(dir, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode := engineFileMode(t, path); mode != 0o600 {
		t.Fatalf("secret live file mode = %o, want 600", mode)
	}
	if mode := engineFileMode(t, result.Files[0].BackupPath); mode != 0o600 {
		t.Fatalf("secret backup mode = %o, want 600", mode)
	}
	assertFileBytes(t, result.Files[0].BackupPath, before)
}

func TestCommitAllowDriftStillEnforcesBeforeHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeTestFile(t, path, []byte("before"), 0o644)
	change := testFileChange(t, ResOpencodeConfig, path, []byte("after"), false)
	result, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{
		BackupRoot:     filepath.Join(dir, "backups"),
		ExpectedHashes: map[Resource]string{ResOpencodeConfig: "wrong-hash"},
		AllowDrift:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files[0].Hash == change.BeforeHash {
		t.Fatalf("allow-drift commit did not write new content: %+v", result.Files[0])
	}

	stale := FileChange{
		Resource:   ResOpencodeConfig,
		Path:       path,
		Before:     []byte("before"),
		BeforeHash: hashBytes([]byte("before")),
		After:      []byte("should-not-write"),
		Mode:       0o644,
	}
	_, err = Commit(&ChangeSet{Changes: []FileChange{stale}}, CommitOpts{
		BackupRoot:     filepath.Join(dir, "backups-2"),
		ExpectedHashes: map[Resource]string{ResOpencodeConfig: "another-wrong-hash"},
		AllowDrift:     true,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected BeforeHash conflict with AllowDrift, got %v", err)
	}
	assertFileBytes(t, path, []byte("after"))
}

func TestCommitRevalidationUsesSnapshotBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	before := []byte("before")
	writeTestFile(t, path, before, 0o644)
	change := testFileChange(t, ResOpencodeConfig, path, []byte("after"), false)
	backupRoot := filepath.Join(dir, "backups")

	setCommitTestHook(t, func(phase string, _ int) {
		if phase == "after-validation" {
			writeTestFile(t, path, []byte("changed-after-validation"), 0o644)
		}
	})
	_, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{BackupRoot: backupRoot})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected revalidation conflict, got %v", err)
	}
	assertFileBytes(t, path, []byte("changed-after-validation"))
	entries, err := os.ReadDir(filepath.Join(backupRoot, filepath.FromSlash(string(ResOpencodeConfig))))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one snapshot backup, got %d", len(entries))
	}
	assertFileBytes(t, filepath.Join(backupRoot, filepath.FromSlash(string(ResOpencodeConfig)), entries[0].Name()), before)
}

func TestCommitExternalChangeBeforeCommitLeavesNoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeTestFile(t, path, []byte("before"), 0o644)
	change := testFileChange(t, ResOpencodeConfig, path, []byte("after"), false)
	writeTestFile(t, path, []byte("changed-after-plan"), 0o644)
	backupRoot := filepath.Join(dir, "backups")

	_, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{BackupRoot: backupRoot})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected lost-update conflict, got %v", err)
	}
	assertFileBytes(t, path, []byte("changed-after-plan"))
	if _, statErr := os.Stat(backupRoot); !os.IsNotExist(statErr) {
		t.Fatalf("pre-commit drift left backup artifacts: %v", statErr)
	}
}

func TestCommitPostWriteIntegrityFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	before := []byte("before")
	writeTestFile(t, path, before, 0o644)
	change := testFileChange(t, ResOpencodeConfig, path, []byte("after"), false)
	setCommitTestHook(t, func(phase string, index int) {
		if phase == "after-write" && index == 0 {
			writeTestFile(t, path, []byte("tampered-after-rename"), 0o644)
		}
	})

	_, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{BackupRoot: filepath.Join(dir, "backups")})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected post-write integrity failure, got %v", err)
	}
	assertFileBytes(t, path, before)
}

func TestCommitChecksPassAndFailBeforeWrites(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.json")
	checkPath := filepath.Join(dir, "dependency.json")
	writeTestFile(t, targetPath, []byte("target-before"), 0o644)
	writeTestFile(t, checkPath, []byte("dependency"), 0o644)
	checkHash, err := HashFile(checkPath)
	if err != nil {
		t.Fatal(err)
	}
	change := testFileChange(t, ResOpencodeConfig, targetPath, []byte("target-after"), false)
	if _, err := Commit(&ChangeSet{
		Changes: []FileChange{change},
		Checks:  []FileCheck{{Resource: ResOpencodeOmoSlim, Path: checkPath, ExpectedHash: checkHash}},
	}, CommitOpts{BackupRoot: filepath.Join(dir, "backups")}); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, targetPath, []byte("target-after"))

	writeTestFile(t, targetPath, []byte("target-before-failed-check"), 0o644)
	failedChange := testFileChange(t, ResOpencodeConfig, targetPath, []byte("must-not-write"), false)
	_, err = Commit(&ChangeSet{
		Changes: []FileChange{failedChange},
		Checks:  []FileCheck{{Resource: ResOpencodeOmoSlim, Path: checkPath, ExpectedHash: "wrong-hash"}},
	}, CommitOpts{BackupRoot: filepath.Join(dir, "failed-backups")})
	if !errors.Is(err, ErrDrifted) {
		t.Fatalf("expected check drift, got %v", err)
	}
	assertFileBytes(t, targetPath, []byte("target-before-failed-check"))
}

func TestCommitRejectsEmptyChangeSet(t *testing.T) {
	_, err := Commit(&ChangeSet{}, CommitOpts{BackupRoot: t.TempDir()})
	if !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("expected ErrInvalidPreset, got %v", err)
	}
}

func TestCommitCreatesMissingParentDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new", "nested", "config.json")
	change := testFileChange(t, ResOpencodeConfig, path, []byte("created"), false)
	if _, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{BackupRoot: filepath.Join(dir, "backups")}); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, []byte("created"))
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		t.Fatalf("created parent directory is missing: %v", err)
	}
}

func TestCommitPruneFailureIsBestEffort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeTestFile(t, path, []byte("before"), 0o644)
	change := testFileChange(t, ResOpencodeConfig, path, []byte("after"), false)
	backupRoot := filepath.Join(dir, "backups")
	setCommitTestHook(t, func(phase string, _ int) {
		if phase != "before-prune" {
			return
		}
		resourceDir := filepath.Join(backupRoot, filepath.FromSlash(string(ResOpencodeConfig)))
		if err := os.RemoveAll(resourceDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resourceDir, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	result, err := Commit(&ChangeSet{Changes: []FileChange{change}}, CommitOpts{
		BackupRoot:  backupRoot,
		KeepBackups: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Hash != hashContent(change.After) {
		t.Fatalf("unexpected result after prune failure: %+v", result)
	}
	assertFileBytes(t, path, []byte("after"))
}

func setCommitTestHook(t *testing.T, hook func(phase string, index int)) {
	t.Helper()
	previous := commitTestHook
	commitTestHook = hook
	t.Cleanup(func() { commitTestHook = previous })
}

func testFileChange(t *testing.T, resource Resource, path string, after []byte, secret bool) FileChange {
	t.Helper()
	before, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		before = nil
	} else if err != nil {
		t.Fatal(err)
	}
	hash, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return FileChange{
		Resource:   resource,
		Path:       path,
		Secret:     secret,
		Before:     before,
		BeforeHash: hash,
		After:      after,
		Mode:       0o644,
	}
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertBackupCount(t *testing.T, backupRoot string, resource Resource, want int) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(backupRoot, filepath.FromSlash(string(resource))))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != want {
		t.Fatalf("backup count for %s = %d, want %d", resource, len(entries), want)
	}
}

func engineFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
