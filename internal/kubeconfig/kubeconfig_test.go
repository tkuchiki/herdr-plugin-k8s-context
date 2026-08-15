package kubeconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestPrepareSelectedContext(t *testing.T) {
	source := testConfig()
	original := source.DeepCopy()

	got, err := Prepare(source, "prod", "payments")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if got.CurrentContext != "prod" {
		t.Fatalf("CurrentContext = %q, want prod", got.CurrentContext)
	}
	if len(got.Contexts) != 1 || got.Contexts["prod"] == nil {
		t.Fatalf("Contexts = %#v, want only prod", got.Contexts)
	}
	if got.Contexts["prod"].Namespace != "payments" {
		t.Fatalf("Namespace = %q, want payments", got.Contexts["prod"].Namespace)
	}
	if len(got.Clusters) != 1 || got.Clusters["prod-cluster"] == nil {
		t.Fatalf("Clusters = %#v, want only prod-cluster", got.Clusters)
	}
	if len(got.AuthInfos) != 1 || got.AuthInfos["prod-user"] == nil {
		t.Fatalf("AuthInfos = %#v, want only prod-user", got.AuthInfos)
	}
	if !reflect.DeepEqual(source, original) {
		t.Fatal("Prepare() mutated source config")
	}
}

func TestPrepareAllContexts(t *testing.T) {
	source := testConfig()

	got, err := Prepare(source, "", "")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if got.CurrentContext != "dev" {
		t.Fatalf("CurrentContext = %q, want dev", got.CurrentContext)
	}
	if len(got.Contexts) != 2 {
		t.Fatalf("len(Contexts) = %d, want 2", len(got.Contexts))
	}
	if got.Contexts["dev"].Namespace != "development" || got.Contexts["prod"].Namespace != "production" {
		t.Fatalf("namespaces changed: %#v", got.Contexts)
	}
}

func TestPrepareNamespaceForCurrentContext(t *testing.T) {
	source := testConfig()

	got, err := Prepare(source, "", "testing")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if len(got.Contexts) != 2 {
		t.Fatalf("len(Contexts) = %d, want 2", len(got.Contexts))
	}
	if got.Contexts["dev"].Namespace != "testing" {
		t.Fatalf("dev namespace = %q, want testing", got.Contexts["dev"].Namespace)
	}
	if got.Contexts["prod"].Namespace != "production" {
		t.Fatalf("prod namespace = %q, want production", got.Contexts["prod"].Namespace)
	}
}

func TestPrepareErrors(t *testing.T) {
	tests := []struct {
		name      string
		config    *clientcmdapi.Config
		context   string
		namespace string
	}{
		{name: "missing context", config: testConfig(), context: "missing"},
		{name: "namespace without current context", config: configWithoutCurrent(), namespace: "testing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Prepare(test.config, test.context, test.namespace); err == nil {
				t.Fatal("Prepare() error = nil, want error")
			}
		})
	}
}

func TestPrepareSelectedContextIgnoresUnrelatedBrokenContext(t *testing.T) {
	source := testConfig()
	source.Contexts["broken"] = &clientcmdapi.Context{Cluster: "missing-cluster"}

	got, err := Prepare(source, "dev", "")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if len(got.Contexts) != 1 || got.Contexts["dev"] == nil {
		t.Fatalf("Contexts = %#v, want only dev", got.Contexts)
	}
}

func TestLoadMergesKubeconfigFiles(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	second := filepath.Join(dir, "second.yaml")
	writeConfigFile(t, first, singleConfig("first", "https://first.example"))
	writeConfigFile(t, second, singleConfig("second", "https://second.example"))
	t.Setenv("KUBECONFIG", first+string(os.PathListSeparator)+second)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Contexts["first"] == nil || got.Contexts["second"] == nil {
		t.Fatalf("Contexts = %#v, want first and second", got.Contexts)
	}
}

func TestPrepareFlattensRelativeCertificateAuthority(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("test-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	contents := `apiVersion: v1
kind: Config
current-context: dev
clusters:
- name: dev-cluster
  cluster:
    server: https://dev.example
    certificate-authority: ca.crt
users:
- name: dev-user
  user:
    token: secret
contexts:
- name: dev
  context:
    cluster: dev-cluster
    user: dev-user
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Prepare(loaded, "dev", "")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	cluster := got.Clusters["dev-cluster"]
	if cluster.CertificateAuthority != "" {
		t.Fatalf("CertificateAuthority = %q, want empty", cluster.CertificateAuthority)
	}
	if string(cluster.CertificateAuthorityData) != "test-ca" {
		t.Fatalf("CertificateAuthorityData = %q, want test-ca", cluster.CertificateAuthorityData)
	}
}

func TestWriteUsesPrivatePermissions(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	path, err := ReservePath(stateDir)
	if err != nil {
		t.Fatalf("ReservePath() error = %v", err)
	}
	err = WriteToPath(path, testConfig())
	if err != nil {
		t.Fatalf("WriteToPath() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}
}

func testConfig() *clientcmdapi.Config {
	return &clientcmdapi.Config{
		CurrentContext: "dev",
		Clusters: map[string]*clientcmdapi.Cluster{
			"dev-cluster":  {Server: "https://dev.example"},
			"prod-cluster": {Server: "https://prod.example"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"dev-user":  {Token: "dev-token"},
			"prod-user": {Token: "prod-token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"dev":  {Cluster: "dev-cluster", AuthInfo: "dev-user", Namespace: "development"},
			"prod": {Cluster: "prod-cluster", AuthInfo: "prod-user", Namespace: "production"},
		},
	}
}

func configWithoutCurrent() *clientcmdapi.Config {
	config := testConfig()
	config.CurrentContext = ""
	return config
}

func singleConfig(name, server string) clientcmdapi.Config {
	return clientcmdapi.Config{
		CurrentContext: name,
		Clusters:       map[string]*clientcmdapi.Cluster{name + "-cluster": {Server: server}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{name + "-user": {Token: name + "-token"}},
		Contexts:       map[string]*clientcmdapi.Context{name: {Cluster: name + "-cluster", AuthInfo: name + "-user"}},
	}
}

func writeConfigFile(t *testing.T, path string, config clientcmdapi.Config) {
	t.Helper()
	if err := clientcmd.WriteToFile(config, path); err != nil {
		t.Fatal(err)
	}
}
