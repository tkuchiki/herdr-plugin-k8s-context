package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCleanupRemovesOnlyStaleOwnedFiles(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	livePath := writeConfig(t, kubeDir, "live.yaml")
	stalePath := writeConfig(t, kubeDir, "stale.yaml")

	store := New(stateDir, "/tmp/herdr.sock")
	for _, entry := range []Entry{
		{TabID: "w1:t1", Path: livePath},
		{TabID: "w1:t2", Path: stalePath},
	} {
		if err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Cleanup(map[string]struct{}{"w1:t1": {}}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live file removed: %v", err)
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(store.recordPath("w1:t1")); err != nil {
		t.Fatalf("live record removed: %v", err)
	}
	if _, err := os.Stat(store.recordPath("w1:t2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale record still exists or unexpected error: %v", err)
	}
}

func TestCleanupContinuesPastMalformedRecord(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	stalePath := writeConfig(t, kubeDir, "stale.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	if err := store.Add(Entry{TabID: "w1:t1", Path: stalePath}); err != nil {
		t.Fatal(err)
	}
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
	if err := store.Add(Entry{TabID: "w1:t1", Path: path}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.RemoveTab("w1:t1"); err != nil {
			t.Fatalf("RemoveTab() error = %v", err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestRemoveTabRejectsPathOutsideManagedDirectory(t *testing.T) {
	stateDir := t.TempDir()
	makeKubeDir(t, stateDir)
	outsidePath := writeConfig(t, t.TempDir(), "outside.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	entry := Entry{TabID: "w1:t1", Path: outsidePath}
	if err := store.ensureDirs(); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.recordPath(entry.TabID), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveTab(entry.TabID); err == nil {
		t.Fatal("RemoveTab() error = nil, want unmanaged path error")
	}
	if _, err := os.Stat(outsidePath); err != nil {
		t.Fatalf("outside file removed: %v", err)
	}
	if _, err := os.Stat(store.recordPath(entry.TabID)); err != nil {
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
	if err := store.Add(Entry{TabID: "w1:t1", Path: path}); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveTab("w1:t1"); err == nil {
		t.Fatal("RemoveTab() error = nil, want remove error")
	}
	if _, err := os.Stat(store.recordPath("w1:t1")); err != nil {
		t.Fatalf("record removed after kubeconfig removal failure: %v", err)
	}
}

func TestLegacyMetadataMigration(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	path := writeConfig(t, kubeDir, "legacy.yaml")
	store := New(stateDir, "/tmp/herdr.sock")
	contents, err := json.Marshal(metadata{Entries: []Entry{{TabID: "w1:t1", Path: path}}})
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
	if _, err := os.Stat(store.recordPath("w1:t1")); err != nil {
		t.Fatalf("migrated record missing: %v", err)
	}
}

func TestConcurrentAddsUseIndependentRecords(t *testing.T) {
	stateDir := t.TempDir()
	kubeDir := makeKubeDir(t, stateDir)
	store := New(stateDir, "/tmp/herdr.sock")
	const count = 20
	entries := make([]Entry, count)
	for index := range count {
		entries[index] = Entry{
			TabID: fmt.Sprintf("w1:t%d", index),
			Path:  writeConfig(t, kubeDir, fmt.Sprintf("%d.yaml", index)),
		}
	}
	var group sync.WaitGroup
	errorsByTab := make(chan error, count)
	for _, entry := range entries {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := store.Add(entry); err != nil {
				errorsByTab <- err
			}
		}()
	}
	group.Wait()
	close(errorsByTab)
	for err := range errorsByTab {
		t.Errorf("Add() error = %v", err)
	}
	records, err := store.records()
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
	if err := store.Add(Entry{TabID: "w1:t1", Path: writeConfig(t, kubeDir, "tab.yaml")}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		mode os.FileMode
	}{
		{path: filepath.Join(stateDir, "lifecycle"), mode: 0o700},
		{path: store.sessionDir(), mode: 0o700},
		{path: store.recordPath("w1:t1"), mode: 0o600},
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
