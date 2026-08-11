package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Entry struct {
	TabID string `json:"tab_id"`
	Path  string `json:"path"`
}

// metadata is the legacy aggregate format. It is retained only for migration.
type metadata struct {
	Entries []Entry `json:"entries"`
}

type Store struct {
	stateDir   string
	socketPath string
}

func New(stateDir, socketPath string) Store {
	return Store{stateDir: stateDir, socketPath: socketPath}
}

func (s Store) Add(entry Entry) error {
	if entry.TabID == "" || entry.Path == "" {
		return errors.New("record kubeconfig: tab ID and path are required")
	}
	if !s.owns(entry.Path) {
		return errors.New("record kubeconfig: path is outside the managed kubeconfig directory")
	}
	if err := s.migrateLegacy(); err != nil {
		return err
	}
	return s.writeRecord(entry)
}

// RemoveTab removes the managed kubeconfig and lifecycle record for tabID.
// It is idempotent so duplicate tab.closed events are harmless.
func (s Store) RemoveTab(tabID string) error {
	if tabID == "" {
		return errors.New("remove kubeconfig: tab ID is required")
	}
	if err := s.migrateLegacy(); err != nil {
		return err
	}
	recordPath := s.recordPath(tabID)
	entry, err := readRecord(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if entry.TabID != tabID {
		return fmt.Errorf("remove kubeconfig: lifecycle record does not match tab %q", tabID)
	}
	return s.removeRecord(recordPath, entry)
}

// Cleanup removes only files recorded for this Herdr session whose tab IDs
// are proven absent from the live tab set.
func (s Store) Cleanup(live map[string]struct{}) error {
	if live == nil {
		return errors.New("cleanup kubeconfigs: live tab set is nil")
	}
	if err := s.migrateLegacy(); err != nil {
		return err
	}
	records, recordErrors := s.records()
	var cleanupErrors []error
	if recordErrors != nil {
		cleanupErrors = append(cleanupErrors, recordErrors)
	}
	for recordPath, entry := range records {
		if _, ok := live[entry.TabID]; ok {
			continue
		}
		if err := s.removeRecord(recordPath, entry); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func (s Store) records() (map[string]Entry, error) {
	result := make(map[string]Entry)
	var recordErrors []error
	items, err := os.ReadDir(s.sessionDir())
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lifecycle directory: %w", err)
	}
	for _, item := range items {
		if item.IsDir() || filepath.Ext(item.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.sessionDir(), item.Name())
		entry, err := readRecord(path)
		if err != nil {
			recordErrors = append(recordErrors, err)
			continue
		}
		if path != s.recordPath(entry.TabID) {
			recordErrors = append(recordErrors, fmt.Errorf("read lifecycle record %q: tab ID hash does not match filename", path))
			continue
		}
		result[path] = entry
	}
	return result, errors.Join(recordErrors...)
}

func (s Store) writeRecord(entry Entry) error {
	if err := s.ensureDirs(); err != nil {
		return err
	}
	path := s.recordPath(entry.TabID)
	existing, err := readRecord(path)
	if err == nil {
		if existing == entry {
			return nil
		}
		return fmt.Errorf("record kubeconfig: tab %q already has a different lifecycle record", entry.TabID)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	contents, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode lifecycle record: %w", err)
	}
	return writeAtomic(path, contents)
}

func readRecord(path string) (Entry, error) {
	var entry Entry
	contents, err := os.ReadFile(path)
	if err != nil {
		return entry, err
	}
	if err := json.Unmarshal(contents, &entry); err != nil {
		return entry, fmt.Errorf("parse lifecycle record %q: %w", path, err)
	}
	if entry.TabID == "" || entry.Path == "" {
		return entry, fmt.Errorf("parse lifecycle record %q: tab ID and path are required", path)
	}
	return entry, nil
}

func (s Store) removeRecord(recordPath string, entry Entry) error {
	if !s.owns(entry.Path) {
		return fmt.Errorf("remove kubeconfig for tab %q: path is outside the managed directory", entry.TabID)
	}
	if err := os.Remove(entry.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove kubeconfig for tab %q: %w", entry.TabID, err)
	}
	if err := os.Remove(recordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove lifecycle record for tab %q: %w", entry.TabID, err)
	}
	return nil
}

func (s Store) migrateLegacy() error {
	contents, err := os.ReadFile(s.legacyPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy state metadata: %w", err)
	}
	var data metadata
	if err := json.Unmarshal(contents, &data); err != nil {
		return fmt.Errorf("parse legacy state metadata: %w", err)
	}
	for _, entry := range data.Entries {
		if entry.TabID == "" || entry.Path == "" {
			return errors.New("migrate legacy state metadata: tab ID and path are required")
		}
		if err := s.writeRecord(entry); err != nil {
			return fmt.Errorf("migrate legacy state metadata: %w", err)
		}
	}
	if err := os.Remove(s.legacyPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy state metadata: %w", err)
	}
	return nil
}

func (s Store) ensureDirs() error {
	if s.stateDir == "" {
		return errors.New("write lifecycle record: state directory is empty")
	}
	for _, dir := range []string{s.stateDir, filepath.Join(s.stateDir, "lifecycle"), s.sessionDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create lifecycle directory: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure lifecycle directory: %w", err)
		}
	}
	return nil
}

func writeAtomic(path string, contents []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".record-*.tmp")
	if err != nil {
		return fmt.Errorf("create lifecycle record: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure lifecycle record: %w", err)
	}
	if _, err = tmp.Write(contents); err != nil {
		return fmt.Errorf("write lifecycle record: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync lifecycle record: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close lifecycle record: %w", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install lifecycle record: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func (s Store) owns(path string) bool {
	root, err := filepath.Abs(filepath.Join(s.stateDir, "kubeconfigs"))
	if err != nil {
		return false
	}
	candidate, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return filepath.Dir(candidate) == root && filepath.Base(candidate) != "."
}

func (s Store) sessionDir() string {
	return filepath.Join(s.stateDir, "lifecycle", hashName(s.socketPath))
}

func (s Store) recordPath(tabID string) string {
	return filepath.Join(s.sessionDir(), hashName(tabID)+".json")
}

func (s Store) legacyPath() string {
	return filepath.Join(s.stateDir, "tabs-"+hashName(s.socketPath)+".json")
}

func hashName(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:8])
}
