# herdr-plugin-k8s-context

Open Herdr tabs with isolated Kubernetes contexts and namespaces.

The plugin opens a popup form for a tab name, kubeconfig context, and default namespace. It writes a private, self-contained kubeconfig for the new tab and launches the tab with `KUBECONFIG` pointing at that copy. The source kubeconfig is never modified.

## Requirements

- Herdr 0.7.4 or later
- Go 1.26 or later when installing from source
- Linux or macOS

`kubectl` is not required by the plugin itself. It is normally installed for use inside the resulting tab.

## Install

Install from GitHub:

```sh
herdr plugin install tkuchiki/herdr-plugin-k8s-context
```

For local development, build and link the checkout:

```sh
make link
```

## Use

Invoke the action from a terminal:

```sh
make open
```

To assign a key, add an entry like this to the Herdr configuration:

```toml
[[keys.command]]
key = "prefix+k"
type = "plugin_action"
command = "herdr.k8s-context.open"
description = "open Kubernetes context tab"
```

The popup contains three fields:

- **Name**: required Herdr tab label, initially set to the number of tabs in the target workspace plus one, matching Herdr's default naming behavior.
- **Context**: a context name, or `All contexts (keep current)`.
- **Namespace**: an optional default namespace.

Context and Namespace behave as follows:

| Context | Namespace | Generated kubeconfig |
| --- | --- | --- |
| omitted | omitted | Keeps every context, namespace, and the original current context. |
| omitted | specified | Keeps every context and changes only the current context's default namespace. |
| specified | omitted | Keeps only the selected context and its referenced cluster/user. |
| specified | specified | Keeps only the selected context and sets its default namespace. |

When Context is omitted, the tab starts with the source kubeconfig's current context and can switch to any other copied context. When Context is specified, unrelated contexts are not present in that tab's kubeconfig.

Namespace changes only the context's default namespace. Commands may still explicitly select another namespace.

## Shell prompt integration

Every tab created by the plugin exports `HERDR_K8S_CONTEXT=1` in addition to `KUBECONFIG`. Prompt tools can use this marker to show Kubernetes information only in plugin-created tabs. The marker contains no context or namespace value, so it cannot become stale; the prompt tool reads the tab's current `KUBECONFIG` whenever it renders.

For [Starship](https://starship.rs/config/#kubernetes), add the following to `~/.config/starship.toml`:

```toml
[kubernetes]
disabled = false
detect_env_vars = ["HERDR_K8S_CONTEXT"]
format = '[$symbol$context( \($namespace\))]($style) '
```

The symbol, style, context aliases, and displayed format remain configurable through Starship.

For [kube-ps1](https://github.com/jonmosco/kube-ps1), install and source kube-ps1 as described in its documentation, then enable it conditionally in the shell startup file. For zsh:

```zsh
if [[ -n "${HERDR_K8S_CONTEXT:-}" ]]; then
  PROMPT='$(kube_ps1)'$PROMPT
fi
```

For bash, use the same condition with `PS1`:

```bash
if [[ -n "${HERDR_K8S_CONTEXT:-}" ]]; then
  PS1='$(kube_ps1)'$PS1
fi
```

These examples display changes made with commands such as `kubectl config use-context` or `kubectl config set-context --current --namespace=...` on the next prompt. The plugin does not modify `PS1`, `PROMPT`, or other shell configuration itself.

## Kubeconfig loading and storage

The plugin follows the standard Kubernetes loading rules:

- If `KUBECONFIG` is set, all files in its path list are merged using client-go precedence rules.
- Otherwise, the plugin loads `$HOME/.kube/config`.
- Relative certificate and key references are resolved and inlined where client-go supports it.

Generated files are stored below `HERDR_PLUGIN_STATE_DIR/kubeconfigs` with mode `0600`; the directory uses mode `0700`. File names do not contain the tab name, context, or namespace.

The plugin records generated files by Herdr session and tab. Closing a tab triggers a `tab.closed` plugin event, which removes that tab's generated kubeconfig immediately. A startup hook and each later popup invocation also reconcile the records with Herdr's live tab list, covering missed events and interrupted cleanup. If tab state cannot be confirmed, the file is retained rather than risking deletion of a kubeconfig that is still in use.

Lifecycle records are stored separately for each tab so concurrent event hooks cannot overwrite records for other tabs. Existing aggregate metadata from earlier development builds is migrated automatically.

### Server restart limitation

After a laptop or normal Herdr server restart, Herdr restores the workspace, tab, pane layout, and working directory, but the original shell process no longer exists. Herdr 0.7.5 does not persist the custom `KUBECONFIG` or `HERDR_K8S_CONTEXT` environment values passed to `tab create`, so an ordinary restored shell does not automatically regain this plugin's Kubernetes isolation or prompt marker.

The generated kubeconfig is retained while the restored tab still exists and is deleted when that tab is closed. This conservative behavior also avoids breaking a live handoff whose original shell still uses the file. Reopen the plugin popup and create a new isolated tab when Kubernetes isolation is needed after a full server restart.

## Development

```sh
make check
make build
```

Run `make help` to list the available development and Herdr integration targets.

Before publishing a release, follow the [release checklist](docs/release-checklist.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
