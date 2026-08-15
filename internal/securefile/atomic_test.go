package securefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicInstallsPrivateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := WriteAtomic(path, ".credentials-*.tmp", []byte("secret")); err != nil {
		t.Fatalf("WriteAtomic() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "secret" {
		t.Fatalf("contents = %q, want secret", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "credentials" {
		t.Fatalf("directory entries = %#v, want only installed file", entries)
	}
}
