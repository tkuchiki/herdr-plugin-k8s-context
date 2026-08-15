package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCleanupRemovesOnlyStaleOwnedFiles(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	livePath := writeConfig(t, kubeDir, "live.yaml")
	stalePath := writeConfig(t, kubeDir, "stale.yaml")

	store := New(stateDir, "/tmp/herdr.sock")
	activateRecord(t, store, livePath, "w1:t1")
	activateRecord(t, store, stalePath, "w1:t2")
	if err := store.Cleanup(map[string]struct{}{"w1:t1": {}}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live file removed: %v", err)
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(store.recordPath(livePath)); err != nil {
		t.Fatalf("live record removed: %v", err)
	}
	if _, err := os.Stat(store.recordPath(stalePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale record still exists or unexpected error: %v", err)
	}
}

func TestCleanupContinuesPastMalformedRecord(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	stalePath := writeConfig(t, kubeDir, "stale.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	activateRecord(t, store, stalePath, "w1:t1")
	if err := os.WriteFile(filepath.Join(store.sessionDir(), "malformed.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(map[string]struct{}{}); err == nil {
		t.Fatal("Cleanup() error = nil, want malformed record error")
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("valid stale kubeconfig was not removed: %v", err)
	}
}

func TestRemoveTabIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "tab.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	activateRecord(t, store, path, "w1:t1")
	for range 2 {
		if err := store.RemoveTab("w1:t1"); err != nil {
			t.Fatalf("RemoveTab() error = %v", err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestShareTabKeepsKubeconfigUntilEveryOwnerCloses(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "moved.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	activateRecord(t, store, path, "w1:t1")
	if err := store.RetainForMovedPane("w1:t1", "w2:t2"); err != nil {
		t.Fatalf("RetainForMovedPane() error = %v", err)
	}
	if err := store.RemoveTab("w1:t1"); err != nil {
		t.Fatalf("RemoveTab(source) error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("kubeconfig removed while destination is live: %v", err)
	}
	if err := store.RemoveTab("w2:t2"); err != nil {
		t.Fatalf("RemoveTab(destination) error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestCleanupReconcilesOldPendingRecordToNewTabs(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "pending.yaml")
	now := time.Unix(1000, 0)
	store := New(stateDir, "/tmp/herdr.sock")
	store.now = func() time.Time { return now }
	if err := store.BeginCreation(path, map[string]struct{}{"w1:t1": {}}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(pendingGracePeriod) }
	if err := store.Cleanup(map[string]struct{}{"w1:t1": {}, "w1:t2": {}}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	record, _, _, err := readStoredRecord(store.recordPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if record.Pending != nil || !contains(record.Owners, "w1:t2") {
		t.Fatalf("record = %#v, want active ownership by w1:t2", record)
	}
}

func TestCleanupRemovesOldPendingRecordWithoutNewTab(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "pending.yaml")
	now := time.Unix(1000, 0)
	store := New(stateDir, "/tmp/herdr.sock")
	store.now = func() time.Time { return now }
	if err := store.BeginCreation(path, map[string]struct{}{"w1:t1": {}}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(pendingGracePeriod) }
	if err := store.Cleanup(map[string]struct{}{"w1:t1": {}}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestCleanupKeepsRecentPendingRecord(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "pending.yaml")
	now := time.Unix(1000, 0)
	store := New(stateDir, "/tmp/herdr.sock")
	store.now = func() time.Time { return now }
	if err := store.BeginCreation(path, map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(pendingGracePeriod - time.Second) }
	if err := store.Cleanup(map[string]struct{}{}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recent pending kubeconfig removed: %v", err)
	}
}

func TestRemoveTabRejectsPathOutsideManagedDirectory(t *testing.T) {
	stateDir := t.TempDir()
	makeKubeDir(t, stateDir)
	outsidePath := writeConfig(t, t.TempDir(), "outside.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	entry := legacyRecord{TabID: "w1:t1", Path: outsidePath}
	if err := store.ensureDirs(); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyRecordPath(entry.TabID), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveTab(entry.TabID); err == nil {
		t.Fatal("RemoveTab() error = nil, want unmanaged path error")
	}
	if _, err := os.Stat(outsidePath); err != nil {
		t.Fatalf("outside file removed: %v", err)
	}
	if _, err := os.Stat(store.legacyRecordPath(entry.TabID)); err != nil {
		t.Fatalf("record removed after rejected cleanup: %v", err)
	}
}

func TestRemoveTabKeepsRecordWhenKubeconfigRemovalFails(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := filepath.Join(kubeDir, "not-empty.yaml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(stateDir, "/tmp/herdr.sock")
	activateRecord(t, store, path, "w1:t1")
	if err := store.RemoveTab("w1:t1"); err == nil {
		t.Fatal("RemoveTab() error = nil, want remove error")
	}
	if _, err := os.Stat(store.recordPath(path)); err != nil {
		t.Fatalf("record removed after kubeconfig removal failure: %v", err)
	}
}

func TestLegacyMetadataMigration(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "legacy.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	contents, err := json.Marshal(legacyMetadata{Entries: []legacyRecord{{TabID: "w1:t1", Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyPath(), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(map[string]struct{}{"w1:t1": {}}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(store.legacyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy metadata still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(store.recordPath(path)); err != nil {
		t.Fatalf("migrated record missing: %v", err)
	}
}

func TestLegacyMetadataMigrationMergesExistingOwners(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "legacy-merged.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	activateRecord(t, store, path, "w1:t1")
	contents, err := json.Marshal(legacyMetadata{Entries: []legacyRecord{{TabID: "w1:t2", Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.legacyPath(), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(map[string]struct{}{"w1:t1": {}, "w1:t2": {}}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	record, _, _, err := readStoredRecord(store.recordPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Owners) != 2 || !contains(record.Owners, "w1:t1") || !contains(record.Owners, "w1:t2") {
		t.Fatalf("owners = %#v, want w1:t1 and w1:t2", record.Owners)
	}
}

func TestPerTabRecordMigration(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "legacy-tab.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	if err := store.ensureDirs(); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(legacyRecord{TabID: "w1:t1", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := store.legacyRecordPath("w1:t1")
	if err := os.WriteFile(legacyPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(map[string]struct{}{"w1:t1": {}}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy record still exists or unexpected error: %v", err)
	}
	record, _, legacy, err := readStoredRecord(store.recordPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if legacy || len(record.Owners) != 1 || record.Owners[0] != "w1:t1" {
		t.Fatalf("migrated record = %#v, legacy = %v", record, legacy)
	}
}

func TestConcurrentAddsUseIndependentRecords(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	store := New(stateDir, "/tmp/herdr.sock")
	const count = 20
	type creation struct {
		tabID string
		path  string
	}
	entries := make([]creation, count)
	for index := range count {
		entries[index] = creation{
			tabID: fmt.Sprintf("w1:t%d", index),
			path:  writeConfig(t, kubeDir, fmt.Sprintf("%d.yaml", index)),
		}
	}
	var group sync.WaitGroup
	errorsByTab := make(chan error, count)
	for _, entry := range entries {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := store.BeginCreation(entry.path, map[string]struct{}{}); err != nil {
				errorsByTab <- err
				return
			}
			if err := store.CommitCreation(entry.path, entry.tabID); err != nil {
				errorsByTab <- err
			}
		}()
	}
	group.Wait()
	close(errorsByTab)
	for err := range errorsByTab {
		t.Errorf("Add() error = %v", err)
	}
	records, err := store.loadRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != count {
		t.Fatalf("len(records) = %d, want %d", len(records), count)
	}
}

func TestLifecyclePermissions(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	store := New(stateDir, "/tmp/herdr.sock")
	activateRecord(t, store, writeConfig(t, kubeDir, "tab.yaml"), "w1:t1")
	for _, test := range []struct {
		path string
		mode os.FileMode
	}{
		{path: filepath.Join(stateDir, "lifecycle"), mode: 0o700},
		{path: store.sessionDir(), mode: 0o700},
		{path: store.recordPath(filepath.Join(kubeDir, "tab.yaml")), mode: 0o600},
	} {
		info, err := os.Stat(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != test.mode {
			t.Errorf("mode(%s) = %o, want %o", test.path, got, test.mode)
		}
	}
}

func makeKubeDir(t *testing.T, stateDir string) string {
	t.Helper()
	dir := filepath.Join(stateDir, "kubeconfigs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeConfig(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func activateRecord(t *testing.T, store Store, path, tabID string) {
	t.Helper()
	if err := store.BeginCreation(path, map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCreation(path, tabID); err != nil {
		t.Fatal(err)
	}
}
