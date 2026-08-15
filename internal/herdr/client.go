package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	PluginID            = "herdr.k8s-context"
	PickerID            = "picker"
	PromptMarkerEnv     = "HERDR_K8S_CONTEXT"
	workspaceIDFallback = "HERDR_K8S_CONTEXT_WORKSPACE_ID"
	defaultBin          = "herdr"
)

type Runner interface {
	Run(ctx context.Context, executable string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return output, nil
}

type Environment struct {
	Binary      string
	WorkspaceID string
	StateDir    string
	SocketPath  string
	CWD         string
	EventName   string
	EventJSON   string
}

func EnvironmentFromOS() Environment {
	binary := os.Getenv("HERDR_BIN_PATH")
	if binary == "" {
		binary = defaultBin
	}
	pluginContext := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")
	workspaceID := os.Getenv("HERDR_WORKSPACE_ID")
	if workspaceID == "" {
		workspaceID = os.Getenv(workspaceIDFallback)
	}
	if workspaceID == "" {
		workspaceID = contextWorkspaceID(pluginContext)
	}
	return Environment{
		Binary:      binary,
		WorkspaceID: workspaceID,
		StateDir:    os.Getenv("HERDR_PLUGIN_STATE_DIR"),
		SocketPath:  os.Getenv("HERDR_SOCKET_PATH"),
		CWD:         contextCWD(pluginContext),
		EventName:   os.Getenv("HERDR_PLUGIN_EVENT"),
		EventJSON:   os.Getenv("HERDR_PLUGIN_EVENT_JSON"),
	}
}

func (e Environment) ValidatePicker() error {
	if e.WorkspaceID == "" {
		return errors.New("HERDR_WORKSPACE_ID is not set")
	}
	if e.StateDir == "" {
		return errors.New("HERDR_PLUGIN_STATE_DIR is not set")
	}
	if e.SocketPath == "" {
		return errors.New("HERDR_SOCKET_PATH is not set")
	}
	return nil
}

func (e Environment) ValidateLifecycle() error {
	if e.StateDir == "" {
		return errors.New("HERDR_PLUGIN_STATE_DIR is not set")
	}
	if e.SocketPath == "" {
		return errors.New("HERDR_SOCKET_PATH is not set")
	}
	return nil
}

func ParseClosedTabID(eventName, raw string) (string, error) {
	event, err := decodePluginEvent[tabClosedEvent](eventName, raw, "tab.closed", "cleanup tab")
	if err != nil {
		return "", err
	}
	if event.Type != "" && event.Type != "tab_closed" && event.Type != "tab.closed" {
		return "", fmt.Errorf("cleanup tab: event data contains unexpected type %q", event.Type)
	}
	if event.TabID == "" {
		return "", errors.New("cleanup tab: event JSON contains no tab ID")
	}
	return event.TabID, nil
}

func ParseMovedPaneTabs(eventName, raw string) (string, string, error) {
	event, err := decodePluginEvent[paneMovedEvent](eventName, raw, "pane.moved", "move pane lifecycle")
	if err != nil {
		return "", "", err
	}
	if event.Type != "" && event.Type != "pane_moved" && event.Type != "pane.moved" {
		return "", "", fmt.Errorf("move pane lifecycle: event data contains unexpected type %q", event.Type)
	}
	if event.PreviousTabID == "" || event.Pane.TabID == "" {
		return "", "", errors.New("move pane lifecycle: event JSON contains incomplete tab IDs")
	}
	return event.PreviousTabID, event.Pane.TabID, nil
}

type pluginEventEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type tabClosedEvent struct {
	Type  string `json:"type"`
	TabID string `json:"tab_id"`
}

type paneMovedEvent struct {
	Type          string `json:"type"`
	PreviousTabID string `json:"previous_tab_id"`
	Pane          struct {
		TabID string `json:"tab_id"`
	} `json:"pane"`
}

func decodePluginEvent[T any](eventName, raw, expectedEvent, operation string) (T, error) {
	var result T
	if eventName != expectedEvent {
		return result, fmt.Errorf("%s: unexpected plugin event %q", operation, eventName)
	}
	if raw == "" {
		return result, fmt.Errorf("%s: HERDR_PLUGIN_EVENT_JSON is empty", operation)
	}
	var envelope pluginEventEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return result, fmt.Errorf("%s: parse event JSON: %w", operation, err)
	}
	legacyEvent := strings.ReplaceAll(expectedEvent, ".", "_")
	if envelope.Event != "" && envelope.Event != expectedEvent && envelope.Event != legacyEvent {
		return result, fmt.Errorf("%s: event JSON contains unexpected event %q", operation, envelope.Event)
	}
	payload := envelope.Data
	if len(payload) == 0 {
		payload = json.RawMessage(raw)
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return result, fmt.Errorf("%s: parse event data: %w", operation, err)
	}
	return result, nil
}

type Client struct {
	Env    Environment
	Runner Runner
}

func (c Client) OpenPicker(ctx context.Context) error {
	if c.Runner == nil {
		return errors.New("open picker: runner is nil")
	}
	args := []string{"plugin", "pane", "open", "--plugin", PluginID, "--entrypoint", PickerID}
	if c.Env.WorkspaceID != "" {
		args = append(args, "--env", workspaceIDFallback+"="+c.Env.WorkspaceID)
	}
	args = append(args, "--focus")
	if _, err := c.Runner.Run(ctx, c.Env.Binary, args...); err != nil {
		return fmt.Errorf("open picker: %w", err)
	}
	return nil
}

type CreateTabOptions struct {
	Name       string
	Kubeconfig string
}

func (c Client) CreateTab(ctx context.Context, options CreateTabOptions) (string, error) {
	if c.Runner == nil {
		return "", errors.New("create tab: runner is nil")
	}
	if c.Env.WorkspaceID == "" {
		return "", errors.New("create tab: workspace ID is empty")
	}
	if options.Name == "" {
		return "", errors.New("create tab: name is empty")
	}
	if options.Kubeconfig == "" {
		return "", errors.New("create tab: kubeconfig path is empty")
	}

	args := []string{"tab", "create", "--workspace", c.Env.WorkspaceID}
	if c.Env.CWD != "" {
		args = append(args, "--cwd", c.Env.CWD)
	}
	args = append(args,
		"--label", options.Name,
		"--env", "KUBECONFIG="+options.Kubeconfig,
		"--env", PromptMarkerEnv+"=1",
		"--focus",
	)
	output, err := c.Runner.Run(ctx, c.Env.Binary, args...)
	if err != nil {
		return "", fmt.Errorf("create tab: %w", err)
	}
	return tabID(output), nil
}

func (c Client) ListTabIDs(ctx context.Context) (map[string]struct{}, error) {
	return c.listTabIDs(ctx, "list tabs", "tab", "list")
}

func (c Client) TabCount(ctx context.Context, workspaceID string) (int, error) {
	if workspaceID == "" {
		return 0, errors.New("count tabs: workspace ID is empty")
	}
	ids, err := c.listTabIDs(ctx, "count tabs", "tab", "list", "--workspace", workspaceID)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (c Client) listTabIDs(ctx context.Context, operation string, args ...string) (map[string]struct{}, error) {
	if c.Runner == nil {
		return nil, fmt.Errorf("%s: runner is nil", operation)
	}
	output, err := c.Runner.Run(ctx, c.Env.Binary, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	ids := make(map[string]struct{})
	var value any
	if err := json.Unmarshal(output, &value); err != nil {
		return nil, fmt.Errorf("%s: parse response: %w", operation, err)
	}
	if !collectTabIDs(value, false, ids) {
		return nil, fmt.Errorf("%s: response contained no tab list", operation)
	}
	return ids, nil
}

func tabID(output []byte) string {
	var value any
	if json.Unmarshal(output, &value) != nil {
		return ""
	}
	if id := findStringKey(value, "tab_id"); id != "" {
		return id
	}
	ids := make(map[string]struct{})
	collectTabIDs(value, false, ids)
	for id := range ids {
		return id
	}
	return ""
}

func findStringKey(value any, key string) string {
	switch value := value.(type) {
	case map[string]any:
		if found, ok := value[key].(string); ok {
			return found
		}
		for _, child := range value {
			if found := findStringKey(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range value {
			if found := findStringKey(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func collectTabIDs(value any, inTab bool, ids map[string]struct{}) bool {
	foundList := false
	switch value := value.(type) {
	case map[string]any:
		if inTab {
			if id, ok := value["id"].(string); ok && id != "" {
				ids[id] = struct{}{}
			}
			if id, ok := value["tab_id"].(string); ok && id != "" {
				ids[id] = struct{}{}
			}
		}
		for key, child := range value {
			if key == "tabs" {
				if _, ok := child.([]any); ok {
					foundList = true
				}
			}
			if collectTabIDs(child, inTab || key == "tab" || key == "tabs", ids) {
				foundList = true
			}
		}
	case []any:
		for _, child := range value {
			if collectTabIDs(child, inTab, ids) {
				foundList = true
			}
		}
	}
	return foundList
}

func contextCWD(raw string) string {
	if raw == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return ""
	}
	for _, key := range []string{"focused_pane", "pane"} {
		if object, ok := value.(map[string]any); ok {
			if cwd := findCWD(object[key]); cwd != "" {
				return cwd
			}
		}
	}
	return findCWD(value)
}

func contextWorkspaceID(raw string) string {
	if raw == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return ""
	}
	if id := findStringKey(value, "workspace_id"); id != "" {
		return id
	}
	if object, ok := value.(map[string]any); ok {
		if workspace, ok := object["workspace"].(map[string]any); ok {
			if id, ok := workspace["id"].(string); ok {
				return id
			}
		}
	}
	return ""
}

func findCWD(value any) string {
	switch value := value.(type) {
	case map[string]any:
		if cwd, ok := value["cwd"].(string); ok && cwd != "" {
			return cwd
		}
		if cwd, ok := value["foreground_cwd"].(string); ok && cwd != "" {
			return cwd
		}
		for _, child := range value {
			if cwd := findCWD(child); cwd != "" {
				return cwd
			}
		}
	case []any:
		for _, child := range value {
			if cwd := findCWD(child); cwd != "" {
				return cwd
			}
		}
	}
	return ""
}
