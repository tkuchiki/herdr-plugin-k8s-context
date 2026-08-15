# Release checklist

Use this checklist before publishing a new version of `herdr-plugin-k8s-context`.

## 1. Choose the release version and ref

Set the version and Git ref being tested:

```sh
RELEASE_VERSION=0.1.0
RELEASE_REF=codex/initial-release
```

Confirm that `herdr-plugin.toml` uses the intended version and that the documented requirements are current.

```sh
grep '^version = ' herdr-plugin.toml
grep '^go ' go.mod
```

The release version must not be tagged until every release gate below passes.

## 2. Run local checks

Start from the release branch with no uncommitted release files:

```sh
git fetch origin
git switch "$RELEASE_REF"
git pull --ff-only
git status --short
```

Run the complete local check set:

```sh
make check
make test-race
make build
git diff --check
git diff --exit-code
```

Confirm that the built binary starts and reports usage rather than crashing:

```sh
./herdr-plugin-k8s-context 2>&1 | grep 'usage:'
```

## 3. Verify pull request CI

The release pull request must pass both GitHub Actions matrix jobs:

- Ubuntu with Go 1.26
- macOS with Go 1.26

With GitHub CLI, check the pull request from the release branch:

```sh
gh pr checks "$RELEASE_REF"
```

Do not merge while either job is pending or failing.

## 4. Test a clean GitHub installation

Use a disposable Docker container so the test cannot use a local checkout, linked plugin, existing Herdr state, or personal kubeconfig.

Start the container:

```sh
docker run --rm -it \
  --name herdr-plugin-release-test \
  --hostname release-test \
  -e TERM=xterm-256color \
  golang:1.26.5-bookworm \
  bash
```

Run the remaining commands in the container.

Install the required tools:

```sh
apt-get update
apt-get install -y ca-certificates curl git

curl -fsSL https://herdr.dev/install.sh | sh
export PATH="/root/.local/bin:$PATH"

KUBECTL_VERSION=v1.36.3
case "$(uname -m)" in
  x86_64) KUBECTL_ARCH=amd64 ;;
  aarch64 | arm64) KUBECTL_ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

curl -fL \
  -o /tmp/kubectl \
  "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${KUBECTL_ARCH}/kubectl"
curl -fL \
  -o /tmp/kubectl.sha256 \
  "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${KUBECTL_ARCH}/kubectl.sha256"
echo "$(cat /tmp/kubectl.sha256)  /tmp/kubectl" | sha256sum --check
install -m 0755 /tmp/kubectl /usr/local/bin/kubectl
rm /tmp/kubectl /tmp/kubectl.sha256

herdr --version
go version
kubectl version --client
```

This follows the official Kubernetes binary installation method and verifies the downloaded checksum. `kubectl` is not installed with `go install` because the `k8s.io/kubectl` module does not expose `cmd/kubectl` as an installable package.

Install the plugin directly from the GitHub release candidate ref:

```sh
herdr plugin install \
  tkuchiki/herdr-plugin-k8s-context \
  --ref codex/initial-release \
  --yes

herdr plugin list --plugin herdr.k8s-context
```

For later releases, replace `codex/initial-release` with the release candidate branch or commit being tested.

## 5. Create an offline kubeconfig fixture

The fixture uses unreachable endpoints because kubeconfig selection and inspection do not require cluster access.

```sh
mkdir -p /root/.kube

cat >/root/.kube/config <<'EOF'
apiVersion: v1
kind: Config
clusters:
  - name: dev
    cluster:
      server: https://dev.invalid
      insecure-skip-tls-verify: true
  - name: prod
    cluster:
      server: https://prod.invalid
      insecure-skip-tls-verify: true
users:
  - name: dummy
    user:
      token: dummy
contexts:
  - name: dev
    context:
      cluster: dev
      user: dummy
      namespace: development
  - name: prod
    context:
      cluster: prod
      user: dummy
      namespace: production
current-context: dev
EOF

chmod 600 /root/.kube/config
sha256sum /root/.kube/config >/tmp/source-kubeconfig.sha256
```

## 6. Run the popup acceptance tests

Start Herdr in the container:

```sh
herdr
```

From a Herdr terminal pane, invoke the installed plugin:

```sh
herdr plugin action invoke open --plugin herdr.k8s-context
```

Test each form combination:

| Context | Namespace | Expected result |
| --- | --- | --- |
| omitted | omitted | Both contexts remain; current context is `dev`; namespace is `development`. |
| omitted | `qa` | Both contexts remain; current context is `dev`; its namespace is `qa`. |
| `prod` | omitted | Only `prod` remains; namespace is `production`. |
| `prod` | `staging` | Only `prod` remains; namespace is `staging`. |

For every created tab, verify the environment and generated kubeconfig:

```sh
test "$HERDR_K8S_CONTEXT" = 1
test -n "$KUBECONFIG"
test -f "$KUBECONFIG"

kubectl config get-contexts -o name
kubectl config current-context
kubectl config view --minify -o jsonpath='{..namespace}{"\n"}'

stat -c '%a' "$KUBECONFIG"
```

The generated kubeconfig mode must be `600`. Confirm that the source kubeconfig was not modified:

```sh
sha256sum -c /tmp/source-kubeconfig.sha256
```

## 7. Verify tab isolation and cleanup

Create two plugin tabs with different context or namespace selections. In one tab, change its current namespace:

```sh
kubectl config set-context --current --namespace=changed
```

Confirm that the other tab and `/root/.kube/config` are unchanged.

Before closing one generated tab, record its kubeconfig path:

```sh
printf '%s\n' "$KUBECONFIG" >/tmp/generated-kubeconfig-path
```

Run `exit` in that tab. From another tab, verify immediate cleanup:

```sh
test ! -e "$(cat /tmp/generated-kubeconfig-path)"
herdr plugin log list --plugin herdr.k8s-context --limit 20
```

There must be no failed `cleanup` or `cleanup-tab` command log.

Repeat the test by creating another plugin tab and closing it through Herdr rather than exiting its shell. Both `exit` and an explicit tab close must remove the generated kubeconfig. The plugin log should show a successful `cleanup` event hook for `pane.exited` or a successful `cleanup-tab` hook for `tab.closed`, respectively.

Finally, split a plugin tab into two panes and run `exit` in only one pane. Confirm that the tab still exists and its generated kubeconfig is not removed.

## 8. Verify known restart behavior

Create a plugin tab and record its generated kubeconfig path:

```sh
printf '%s\n' "$KUBECONFIG" >/tmp/restart-kubeconfig-path
```

Stop the server from a Herdr pane. After the client returns to the outer container shell, start Herdr again:

```sh
herdr server stop
herdr
```

Confirm the documented limitation:

- the restored shell does not regain `KUBECONFIG` or `HERDR_K8S_CONTEXT`;
- `test -e "$(cat /tmp/restart-kubeconfig-path)"` succeeds while the restored tab exists;
- closing the restored tab removes the generated file.

This limitation must remain documented in the README until Herdr persists custom tab environment values.

## 9. Release gate

The release candidate is ready to merge only when all of the following are true:

- [ ] Local formatting, test, race, vet, and build checks pass.
- [ ] Linux and macOS CI jobs pass.
- [ ] Installation from the GitHub release candidate ref succeeds in the disposable container.
- [ ] All four context/namespace combinations behave as documented.
- [ ] Generated kubeconfigs use mode `600` and do not modify the source kubeconfig.
- [ ] Tabs use independent kubeconfig files.
- [ ] Shell exit and explicit tab close remove generated kubeconfigs without cleanup errors.
- [ ] Exiting one pane in a multi-pane tab does not remove the tab's kubeconfig.
- [ ] Restart behavior matches the documented limitation.
- [ ] `herdr-plugin.toml`, README requirements, and the intended tag use the same release version.
- [ ] The repository description, Apache-2.0 license, and `herdr-plugin` GitHub topic are present.

## 10. Publish after merge

After the release pull request is merged, test installation from `main` once more. Then create the tag and GitHub release from the merged commit:

```sh
git switch main
git pull --ff-only

git tag -a "v$RELEASE_VERSION" -m "Release v$RELEASE_VERSION"
git push origin "v$RELEASE_VERSION"

gh release create "v$RELEASE_VERSION" \
  --title "v$RELEASE_VERSION" \
  --generate-notes
```

Finally, test the immutable release ref in a new disposable container:

```sh
herdr plugin install \
  tkuchiki/herdr-plugin-k8s-context \
  --ref "v$RELEASE_VERSION" \
  --yes
```
