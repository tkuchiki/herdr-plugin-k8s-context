package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/herdr"
	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/state"
)

type lifecycleRunner struct {
	output []byte
	err    error
}

func (r lifecycleRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return r.output, r.err
}

func TestCleanupTabRemovesClosedTabKubeconfig(t *testing.T) {
	stateDir := t.TempDir()
	path := createManagedKubeconfig(t, stateDir, "closed.yaml")
	env := herdr.Environment{
		StateDir:   stateDir,
		SocketPath: "/tmp/herdr.sock",
		EventName:  "tab.closed",
		EventJSON:  `{"event":"tab_closed","data":{"type":"tab_closed","tab_id":"w1:t2","workspace_id":"w1"}}`,
	}
	if err := state.New(env.StateDir, env.SocketPath).Add(state.Entry{TabID: "w1:t2", Path: path}); err != nil {
		t.Fatal(err)
	}
	if err := cleanupTab(env); err != nil {
		t.Fatalf("cleanupTab() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestCleanupReconcilesLiveTabs(t *testing.T) {
	stateDir := t.TempDir()
	env := herdr.Environment{Binary: "herdr", StateDir: stateDir, SocketPath: "/tmp/herdr.sock"}
	store := state.New(env.StateDir, env.SocketPath)
	livePath := createManagedKubeconfig(t, stateDir, "live.yaml")
	stalePath := createManagedKubeconfig(t, stateDir, "stale.yaml")
	if err := store.Add(state.Entry{TabID: "w1:t1", Path: livePath}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(state.Entry{TabID: "w1:t2", Path: stalePath}); err != nil {
		t.Fatal(err)
	}
	client := herdr.Client{
		Env:    env,
		Runner: lifecycleRunner{output: []byte(`{"result":{"tabs":[{"tab_id":"w1:t1"}]}}`)},
	}
	if err := cleanup(context.Background(), env, client); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live kubeconfig removed: %v", err)
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestRecordTabLifecycleRemovesTabClosedBeforeRecordWasWritten(t *testing.T) {
	stateDir := t.TempDir()
	path := createManagedKubeconfig(t, stateDir, "closed-before-record.yaml")
	env := herdr.Environment{Binary: "herdr", StateDir: stateDir, SocketPath: "/tmp/herdr.sock"}
	client := herdr.Client{
		Env:    env,
		Runner: lifecycleRunner{output: []byte(`{"result":{"type":"tab_list","tabs":[]}}`)},
	}
	if err := recordTabLifecycle(
		context.Background(),
		client,
		state.New(env.StateDir, env.SocketPath),
		"w1:t2",
		path,
	); err != nil {
		t.Fatalf("recordTabLifecycle() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestDefaultTabName(t *testing.T) {
	for _, test := range []struct {
		count int
		want  string
	}{
		{count: 0, want: "1"},
		{count: 3, want: "4"},
	} {
		if got := defaultTabName(test.count); got != test.want {
			t.Errorf("defaultTabName(%d) = %q, want %q", test.count, got, test.want)
		}
	}
}

func createManagedKubeconfig(t *testing.T, stateDir, name string) string {
	t.Helper()
	dir := filepath.Join(stateDir, "kubeconfigs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
