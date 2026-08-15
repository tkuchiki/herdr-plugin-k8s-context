package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/securefile"
	"golang.org/x/sys/unix"
)

const pendingGracePeriod = 5 * time.Minute

type record struct {
	Path    string         `json:"path"`
	Owners  []string       `json:"owners,omitempty"`
	Pending *pendingRecord `json:"pending,omitempty"`
}

type pendingRecord struct {
	SinceUnix      int64    `json:"since_unix"`
	BaselineTabIDs []string `json:"baseline_tab_ids"`
}

// legacyRecord supports aggregate, per-tab, and earlier path-keyed development
// formats.
type legacyRecord struct {
	TabID          string   `json:"tab_id,omitempty"`
	TabIDs         []string `json:"tab_ids,omitempty"`
	Path           string   `json:"path"`
	PendingSince   int64    `json:"pending_since_unix,omitempty"`
	BaselineTabIDs []string `json:"baseline_tab_ids,omitempty"`
}

type legacyMetadata struct {
	Entries []legacyRecord `json:"entries"`
}

type Store struct {
	stateDir   string
	socketPath string
	now        func() time.Time
}

func New(stateDir, socketPath string) Store {
	return Store{stateDir: stateDir, socketPath: socketPath, now: time.Now}
}

// BeginCreation records a generated path before the kubeconfig and its Herdr
// tab are created. The baseline permits recovery after an interrupted create.
func (s Store) BeginCreation(path string, baseline map[string]struct{}) error {
	if path == "" {
		return errors.New("begin kubeconfig lifecycle: path is required")
	}
	if baseline == nil {
		return errors.New("begin kubeconfig lifecycle: baseline tab set is nil")
	}
	if !s.owns(path) {
		return errors.New("begin kubeconfig lifecycle: path is outside the managed kubeconfig directory")
	}
	return s.withLock(func() error {
		records, err := s.loadRecords()
		if err != nil {
			return err
		}
		if _, ok := records[path]; ok {
			return errors.New("begin kubeconfig lifecycle: path is already recorded")
		}
		return s.writeRecord(record{
			Path: path,
			Pending: &pendingRecord{
				SinceUnix:      s.now().Unix(),
				BaselineTabIDs: sortedKeys(baseline),
			},
		})
	})
}

// CommitCreation replaces a pending record with ownership by the created tab.
func (s Store) CommitCreation(path, tabID string) error {
	if path == "" || tabID == "" {
		return errors.New("commit kubeconfig lifecycle: path and tab ID are required")
	}
	return s.withLock(func() error {
		records, err := s.loadRecords()
		if err != nil {
			return err
		}
		record, ok := records[path]
		if !ok || record.Pending == nil {
			return errors.New("commit kubeconfig lifecycle: pending record does not exist")
		}
		record.Pending = nil
		record.Owners = []string{tabID}
		return s.writeRecord(record)
	})
}

// AbortCreation removes an incomplete managed kubeconfig and its record.
func (s Store) AbortCreation(path string) error {
	if path == "" {
		return errors.New("abort kubeconfig lifecycle: path is required")
	}
	return s.withLock(func() error {
		records, err := s.loadRecords()
		if err != nil {
			return err
		}
		if record, ok := records[path]; ok {
			return s.removeRecord(record)
		}
		if !s.owns(path) {
			return errors.New("abort kubeconfig lifecycle: path is outside the managed directory")
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("abort kubeconfig lifecycle: %w", err)
		}
		return nil
	})
}

// RetainForMovedPane makes destinationTabID an additional owner of every
// kubeconfig owned by sourceTabID. Keeping the source owner is intentional: a
// source tab can still contain other panes using the same kubeconfig.
func (s Store) RetainForMovedPane(sourceTabID, destinationTabID string) error {
	if sourceTabID == "" || destinationTabID == "" {
		return errors.New("move kubeconfig lifecycle: source and destination tab IDs are required")
	}
	if sourceTabID == destinationTabID {
		return nil
	}
	return s.withLock(func() error {
		records, err := s.loadRecords()
		if err != nil {
			return err
		}
		for _, record := range records {
			if !contains(record.Owners, sourceTabID) {
				continue
			}
			record.Owners = appendUnique(record.Owners, destinationTabID)
			if err := s.writeRecord(record); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveTab drops tabID from every record and removes kubeconfigs that no
// longer have an owning tab. It is idempotent for duplicate tab.closed events.
func (s Store) RemoveTab(tabID string) error {
	if tabID == "" {
		return errors.New("remove kubeconfig: tab ID is required")
	}
	return s.withLock(func() error {
		records, err := s.loadRecords()
		if err != nil {
			return err
		}
		var removeErrors []error
		for _, record := range records {
			owners := removeValue(record.Owners, tabID)
			if len(owners) == len(record.Owners) {
				continue
			}
			if len(owners) == 0 {
				if err := s.removeRecord(record); err != nil {
					removeErrors = append(removeErrors, err)
				}
				continue
			}
			record.Owners = owners
			if err := s.writeRecord(record); err != nil {
				removeErrors = append(removeErrors, err)
			}
		}
		return errors.Join(removeErrors...)
	})
}

// Cleanup reconciles active records with the live tab set. Pending records are
// left alone briefly so normal tab creation and event hooks cannot race. Once
// old enough, tabs absent from the recorded baseline are adopted as owners; if
// there are none, the interrupted kubeconfig is removed.
func (s Store) Cleanup(live map[string]struct{}) error {
	if live == nil {
		return errors.New("cleanup kubeconfigs: live tab set is nil")
	}
	return s.withLock(func() error {
		records, loadErr := s.loadRecords()
		var cleanupErrors []error
		if loadErr != nil {
			cleanupErrors = append(cleanupErrors, loadErr)
		}
		for _, record := range records {
			if record.Pending != nil {
				if s.now().Sub(time.Unix(record.Pending.SinceUnix, 0)) < pendingGracePeriod {
					continue
				}
				owners := difference(live, record.Pending.BaselineTabIDs)
				if len(owners) == 0 {
					if err := s.removeRecord(record); err != nil {
						cleanupErrors = append(cleanupErrors, err)
					}
					continue
				}
				record.Pending = nil
				record.Owners = owners
				if err := s.writeRecord(record); err != nil {
					cleanupErrors = append(cleanupErrors, err)
				}
				continue
			}

			owners := intersect(record.Owners, live)
			if len(owners) == 0 {
				if err := s.removeRecord(record); err != nil {
					cleanupErrors = append(cleanupErrors, err)
				}
				continue
			}
			if len(owners) != len(record.Owners) {
				record.Owners = owners
				if err := s.writeRecord(record); err != nil {
					cleanupErrors = append(cleanupErrors, err)
				}
			}
		}
		return errors.Join(cleanupErrors...)
	})
}

func (s Store) withLock(operation func() error) (err error) {
	if err := s.ensureDirs(); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.sessionDir(), ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lifecycle lock: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close lifecycle lock: %w", closeErr)
		}
	}()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock lifecycle state: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	if err := s.migrateAggregateMetadata(); err != nil {
		return err
	}
	return operation()
}

func (s Store) loadRecords() (map[string]record, error) {
	items, err := os.ReadDir(s.sessionDir())
	if err != nil {
		return nil, fmt.Errorf("read lifecycle directory: %w", err)
	}

	records := make(map[string]record)
	legacyPaths := make(map[string]string)
	var recordErrors []error
	for _, item := range items {
		if item.IsDir() || filepath.Ext(item.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.sessionDir(), item.Name())
		record, legacyTabID, legacy, err := readStoredRecord(path)
		if err != nil {
			recordErrors = append(recordErrors, err)
			continue
		}
		if !s.owns(record.Path) {
			recordErrors = append(recordErrors, fmt.Errorf("read lifecycle record %q: path is outside the managed directory", path))
			continue
		}
		expected := s.recordPath(record.Path)
		legacyFilename := legacy && legacyTabID != "" && path == s.legacyRecordPath(legacyTabID)
		if path != expected && !legacyFilename {
			recordErrors = append(recordErrors, fmt.Errorf("read lifecycle record %q: path hash does not match filename", path))
			continue
		}

		merged, err := mergeRecords(records[record.Path], record)
		if err != nil {
			recordErrors = append(recordErrors, fmt.Errorf("merge lifecycle record %q: %w", path, err))
			continue
		}
		records[record.Path] = merged
		if legacy {
			legacyPaths[path] = record.Path
		}
	}

	for path, configPath := range legacyPaths {
		record := records[configPath]
		if err := s.writeRecord(record); err != nil {
			recordErrors = append(recordErrors, err)
			continue
		}
		if path != s.recordPath(record.Path) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				recordErrors = append(recordErrors, fmt.Errorf("remove legacy lifecycle record %q: %w", path, err))
			}
		}
	}
	return records, errors.Join(recordErrors...)
}

func (s Store) writeRecord(record record) error {
	if err := validateRecord(record); err != nil {
		return fmt.Errorf("record kubeconfig: %w", err)
	}
	if !s.owns(record.Path) {
		return errors.New("record kubeconfig: path is outside the managed kubeconfig directory")
	}
	record.Owners = uniqueSorted(record.Owners)
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode lifecycle record: %w", err)
	}
	if err := securefile.WriteAtomic(s.recordPath(record.Path), ".record-*.tmp", contents); err != nil {
		return fmt.Errorf("write lifecycle record: %w", err)
	}
	return nil
}

func readStoredRecord(path string) (record, string, bool, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return record{}, "", false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(contents, &fields); err != nil {
		return record{}, "", false, fmt.Errorf("parse lifecycle record %q: %w", path, err)
	}
	if _, hasOwners := fields["owners"]; hasOwners || fields["pending"] != nil {
		var current record
		if err := json.Unmarshal(contents, &current); err != nil {
			return record{}, "", false, fmt.Errorf("parse lifecycle record %q: %w", path, err)
		}
		if err := validateRecord(current); err != nil {
			return record{}, "", false, fmt.Errorf("parse lifecycle record %q: %w", path, err)
		}
		return current, "", false, nil
	}

	var legacy legacyRecord
	if err := json.Unmarshal(contents, &legacy); err != nil {
		return record{}, "", false, fmt.Errorf("parse legacy lifecycle record %q: %w", path, err)
	}
	converted, err := convertLegacyRecord(legacy)
	if err != nil {
		return record{}, "", false, fmt.Errorf("parse legacy lifecycle record %q: %w", path, err)
	}
	return converted, legacy.TabID, true, nil
}

func convertLegacyRecord(legacy legacyRecord) (record, error) {
	owners := appendUnique(legacy.TabIDs, legacy.TabID)
	pending := legacy.PendingSince != 0
	if legacy.Path == "" || (pending == (len(owners) > 0)) {
		return record{}, errors.New("path and exactly one lifecycle state are required")
	}
	record := record{Path: legacy.Path, Owners: owners}
	if pending {
		record.Pending = &pendingRecord{
			SinceUnix:      legacy.PendingSince,
			BaselineTabIDs: uniqueSorted(legacy.BaselineTabIDs),
		}
	}
	return record, nil
}

func validateRecord(record record) error {
	active := len(record.Owners) > 0
	pending := record.Pending != nil
	if record.Path == "" || active == pending {
		return errors.New("path and exactly one lifecycle state are required")
	}
	if pending && record.Pending.SinceUnix == 0 {
		return errors.New("pending creation time is required")
	}
	return nil
}

func mergeRecords(existing, incoming record) (record, error) {
	if existing.Path == "" {
		return incoming, nil
	}
	if existing.Path != incoming.Path {
		return record{}, errors.New("kubeconfig paths do not match")
	}
	if existing.Pending != nil || incoming.Pending != nil {
		return record{}, errors.New("duplicate records include a pending lifecycle")
	}
	existing.Owners = appendUnique(existing.Owners, incoming.Owners...)
	return existing, nil
}

func (s Store) removeRecord(record record) error {
	if !s.owns(record.Path) {
		return fmt.Errorf("remove kubeconfig %q: path is outside the managed directory", record.Path)
	}
	if err := os.Remove(record.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove kubeconfig %q: %w", record.Path, err)
	}
	if err := os.Remove(s.recordPath(record.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove lifecycle record for %q: %w", record.Path, err)
	}
	return nil
}

func (s Store) migrateAggregateMetadata() error {
	contents, err := os.ReadFile(s.legacyPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy state metadata: %w", err)
	}
	var metadata legacyMetadata
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return fmt.Errorf("parse legacy state metadata: %w", err)
	}
	records := make(map[string]record)
	for _, legacy := range metadata.Entries {
		record, err := convertLegacyRecord(legacy)
		if err != nil {
			return fmt.Errorf("migrate legacy state metadata: %w", err)
		}
		merged, err := mergeRecords(records[record.Path], record)
		if err != nil {
			return fmt.Errorf("migrate legacy state metadata: %w", err)
		}
		records[record.Path] = merged
	}
	for path, record := range records {
		if existing, _, _, err := readStoredRecord(s.recordPath(path)); err == nil {
			record, err = mergeRecords(existing, record)
			if err != nil {
				return fmt.Errorf("migrate legacy state metadata: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("migrate legacy state metadata: %w", err)
		}
		if err := s.writeRecord(record); err != nil {
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

func (s Store) recordPath(path string) string {
	return filepath.Join(s.sessionDir(), hashName(path)+".json")
}

func (s Store) legacyRecordPath(tabID string) string {
	return filepath.Join(s.sessionDir(), hashName(tabID)+".json")
}

func (s Store) legacyPath() string {
	return filepath.Join(s.stateDir, "tabs-"+hashName(s.socketPath)+".json")
}

func appendUnique(values []string, additions ...string) []string {
	return uniqueSorted(append(append([]string(nil), values...), additions...))
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeValue(values []string, target string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return uniqueSorted(result)
}

func intersect(values []string, set map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := set[value]; ok {
			result = append(result, value)
		}
	}
	return uniqueSorted(result)
}

func difference(values map[string]struct{}, baseline []string) []string {
	base := make(map[string]struct{}, len(baseline))
	for _, value := range baseline {
		base[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		if _, ok := base[value]; !ok {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func hashName(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:8])
}
