package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/herdr"
	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/state"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type lifecycleRunner struct {
	output []byte
	err    error
}

func (r lifecycleRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return r.output, r.err
}

type runnerResult struct {
	output []byte
	err    error
}

type sequenceRunner struct {
	results []runnerResult
	args    [][]string
}

func (r *sequenceRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.args = append(r.args, append([]string(nil), args...))
	result := r.results[len(r.args)-1]
	return result.output, result.err
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
	activateLifecycle(t, state.New(env.StateDir, env.SocketPath), path, "w1:t2")
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
	activateLifecycle(t, store, livePath, "w1:t1")
	activateLifecycle(t, store, stalePath, "w1:t2")
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

func TestMovePaneSharesKubeconfigWithDestinationTab(t *testing.T) {
	stateDir := t.TempDir()
	path := createManagedKubeconfig(t, stateDir, "moved.yaml")
	env := herdr.Environment{
		StateDir:   stateDir,
		SocketPath: "/tmp/herdr.sock",
		EventName:  "pane.moved",
		EventJSON:  `{"event":"pane_moved","data":{"type":"pane_moved","previous_tab_id":"w1:t1","pane":{"tab_id":"w2:t2"}}}`,
	}
	store := state.New(env.StateDir, env.SocketPath)
	activateLifecycle(t, store, path, "w1:t1")
	if err := movePane(env); err != nil {
		t.Fatalf("movePane() error = %v", err)
	}
	if err := store.RemoveTab("w1:t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("moved pane kubeconfig removed with source tab: %v", err)
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
	store := state.New(env.StateDir, env.SocketPath)
	if err := store.BeginCreation(path, map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := recordTabLifecycle(
		context.Background(),
		client,
		store,
		"w1:t2",
		path,
	); err != nil {
		t.Fatalf("recordTabLifecycle() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubeconfig still exists or unexpected error: %v", err)
	}
}

func TestCreateIsolatedTabCompletesLifecycle(t *testing.T) {
	stateDir := t.TempDir()
	env := herdr.Environment{
		Binary:      "herdr",
		WorkspaceID: "w1",
		StateDir:    stateDir,
		SocketPath:  "/tmp/herdr.sock",
	}
	runner := &sequenceRunner{results: []runnerResult{
		{output: []byte(`{"result":{"tabs":[{"tab_id":"w1:t1"}]}}`)},
		{output: []byte(`{"result":{"tab":{"tab_id":"w1:t2"}}}`)},
		{output: []byte(`{"result":{"tabs":[{"tab_id":"w1:t1"},{"tab_id":"w1:t2"}]}}`)},
	}}
	client := herdr.Client{Env: env, Runner: runner}
	result, err := createIsolatedTab(
		context.Background(),
		stateDir,
		client,
		state.New(stateDir, env.SocketPath),
		"dev",
		&clientcmdapi.Config{},
	)
	if err != nil {
		t.Fatalf("createIsolatedTab() error = %v", err)
	}
	if result.Warning != nil {
		t.Fatalf("createIsolatedTab() warning = %v", result.Warning)
	}
	path := kubeconfigPathFromArgs(t, runner.args[1])
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("created kubeconfig missing: %v", err)
	}
	if err := state.New(stateDir, env.SocketPath).RemoveTab("w1:t2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kubeconfig still exists after lifecycle cleanup: %v", err)
	}
}

func TestCreateIsolatedTabRollsBackCreateFailure(t *testing.T) {
	stateDir := t.TempDir()
	env := herdr.Environment{
		Binary:      "herdr",
		WorkspaceID: "w1",
		StateDir:    stateDir,
		SocketPath:  "/tmp/herdr.sock",
	}
	runner := &sequenceRunner{results: []runnerResult{
		{output: []byte(`{"result":{"tabs":[]}}`)},
		{err: errors.New("create failed")},
	}}
	client := herdr.Client{Env: env, Runner: runner}
	_, err := createIsolatedTab(
		context.Background(),
		stateDir,
		client,
		state.New(stateDir, env.SocketPath),
		"dev",
		&clientcmdapi.Config{},
	)
	if err == nil {
		t.Fatal("createIsolatedTab() error = nil")
	}
	path := kubeconfigPathFromArgs(t, runner.args[1])
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed tab kubeconfig still exists: %v", err)
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

func activateLifecycle(t *testing.T, store state.Store, path, tabID string) {
	t.Helper()
	if err := store.BeginCreation(path, map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCreation(path, tabID); err != nil {
		t.Fatal(err)
	}
}

func kubeconfigPathFromArgs(t *testing.T, args []string) string {
	t.Helper()
	for _, arg := range args {
		if path, ok := strings.CutPrefix(arg, "KUBECONFIG="); ok {
			return path
		}
	}
	t.Fatal("KUBECONFIG argument not found")
	return ""
}
