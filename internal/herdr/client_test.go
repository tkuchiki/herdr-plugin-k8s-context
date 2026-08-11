package herdr

import (
	"context"
	"reflect"
	"testing"
)

type fakeRunner struct {
	executable string
	args       []string
	output     []byte
	err        error
}

func (f *fakeRunner) Run(_ context.Context, executable string, args ...string) ([]byte, error) {
	f.executable = executable
	f.args = append([]string(nil), args...)
	return f.output, f.err
}

func TestOpenPicker(t *testing.T) {
	runner := &fakeRunner{}
	client := Client{
		Env:    Environment{Binary: "/opt/herdr", WorkspaceID: "w1"},
		Runner: runner,
	}
	if err := client.OpenPicker(context.Background()); err != nil {
		t.Fatalf("OpenPicker() error = %v", err)
	}
	want := []string{
		"plugin", "pane", "open",
		"--plugin", PluginID,
		"--entrypoint", PickerID,
		"--env", workspaceIDFallback + "=w1",
		"--focus",
	}
	if runner.executable != "/opt/herdr" || !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("command = %q %#v, want /opt/herdr %#v", runner.executable, runner.args, want)
	}
}

func TestCreateTabBuildsArgvAndParsesID(t *testing.T) {
	runner := &fakeRunner{output: []byte(`{"result":{"type":"tab_create","tab":{"id":"w1:t2"}}}`)}
	client := Client{
		Env: Environment{
			Binary:      "/opt/herdr",
			WorkspaceID: "w1",
			CWD:         "/work/project",
		},
		Runner: runner,
	}
	id, err := client.CreateTab(context.Background(), CreateTabOptions{
		Name:       "prod; echo unsafe",
		Kubeconfig: "/state/config file.yaml",
	})
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}
	if id != "w1:t2" {
		t.Fatalf("ID = %q, want w1:t2", id)
	}
	want := []string{
		"tab", "create", "--workspace", "w1",
		"--cwd", "/work/project",
		"--label", "prod; echo unsafe",
		"--env", "KUBECONFIG=/state/config file.yaml",
		"--env", "HERDR_K8S_CONTEXT=1",
		"--focus",
	}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestListTabIDs(t *testing.T) {
	runner := &fakeRunner{output: []byte(`{"result":{"tabs":[{"id":"w1:t1"},{"tab_id":"w1:t2"}]}}`)}
	client := Client{Env: Environment{Binary: "herdr"}, Runner: runner}
	ids, err := client.ListTabIDs(context.Background())
	if err != nil {
		t.Fatalf("ListTabIDs() error = %v", err)
	}
	for _, id := range []string{"w1:t1", "w1:t2"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("IDs = %#v, missing %s", ids, id)
		}
	}
}

func TestListTabIDsAcceptsEmptyList(t *testing.T) {
	runner := &fakeRunner{output: []byte(`{"result":{"type":"tab_list","tabs":[]}}`)}
	client := Client{Env: Environment{Binary: "herdr"}, Runner: runner}
	ids, err := client.ListTabIDs(context.Background())
	if err != nil {
		t.Fatalf("ListTabIDs() error = %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("len(IDs) = %d, want 0", len(ids))
	}
}

func TestTabCountUsesWorkspaceFilter(t *testing.T) {
	runner := &fakeRunner{output: []byte(`{"result":{"type":"tab_list","tabs":[{"tab_id":"w1:t1"},{"tab_id":"w1:t2"}]}}`)}
	client := Client{Env: Environment{Binary: "/opt/herdr"}, Runner: runner}
	count, err := client.TabCount(context.Background(), "w1")
	if err != nil {
		t.Fatalf("TabCount() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("TabCount() = %d, want 2", count)
	}
	want := []string{"tab", "list", "--workspace", "w1"}
	if runner.executable != "/opt/herdr" || !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("command = %q %#v, want /opt/herdr %#v", runner.executable, runner.args, want)
	}
}

func TestTabCountRejectsEmptyWorkspace(t *testing.T) {
	client := Client{Runner: &fakeRunner{}}
	if _, err := client.TabCount(context.Background(), ""); err == nil {
		t.Fatal("TabCount() error = nil, want empty workspace error")
	}
}

func TestContextCWD(t *testing.T) {
	raw := `{"workspace":{"cwd":"/workspace"},"focused_pane":{"cwd":"/focused"}}`
	if got := contextCWD(raw); got != "/focused" {
		t.Fatalf("contextCWD() = %q, want /focused", got)
	}
}

func TestEnvironmentFromOSGetsWorkspaceFromPluginContext(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "")
	t.Setenv(workspaceIDFallback, "")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"focused_pane":{"workspace_id":"w2"}}`)

	if got := EnvironmentFromOS().WorkspaceID; got != "w2" {
		t.Fatalf("WorkspaceID = %q, want w2", got)
	}
}

func TestEnvironmentFromOSPrefersExplicitWorkspaceFallback(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "")
	t.Setenv(workspaceIDFallback, "w3")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"workspace":{"workspace_id":"w2"}}`)

	if got := EnvironmentFromOS().WorkspaceID; got != "w3" {
		t.Fatalf("WorkspaceID = %q, want w3", got)
	}
}

func TestValidatePickerRequiresSessionIdentity(t *testing.T) {
	env := Environment{WorkspaceID: "w1", StateDir: "/state"}
	if err := env.ValidatePicker(); err == nil {
		t.Fatal("ValidatePicker() error = nil, want missing socket path error")
	}
}

func TestValidateLifecycleDoesNotRequireWorkspace(t *testing.T) {
	env := Environment{StateDir: "/state", SocketPath: "/tmp/herdr.sock"}
	if err := env.ValidateLifecycle(); err != nil {
		t.Fatalf("ValidateLifecycle() error = %v", err)
	}
}

func TestClosedTabID(t *testing.T) {
	raw := `{"event":"tab_closed","data":{"type":"tab_closed","tab_id":"w1:t2","workspace_id":"w1"}}`
	id, err := ClosedTabID("tab.closed", raw)
	if err != nil {
		t.Fatalf("ClosedTabID() error = %v", err)
	}
	if id != "w1:t2" {
		t.Fatalf("ClosedTabID() = %q, want w1:t2", id)
	}
}

func TestClosedTabIDRejectsUnexpectedEvent(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "tab.created", raw: `{"data":{"tab_id":"w1:t2"}}`},
		{name: "tab.closed", raw: `{"event":"tab_created","data":{"tab_id":"w1:t2"}}`},
		{name: "tab.closed", raw: `{"event":"tab_closed","data":{}}`},
		{name: "tab.closed", raw: `{`},
	} {
		if _, err := ClosedTabID(test.name, test.raw); err == nil {
			t.Errorf("ClosedTabID(%q, %q) error = nil", test.name, test.raw)
		}
	}
}
