package kubeconfig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Load reads kubeconfig using the same precedence and merge rules as kubectl.
func Load() (*clientcmdapi.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	if clientcmdapi.IsConfigEmpty(config) {
		return nil, errors.New("load kubeconfig: configuration is empty")
	}
	return config, nil
}

// ContextNames returns context names in deterministic order.
func ContextNames(config *clientcmdapi.Config) []string {
	names := make([]string, 0, len(config.Contexts))
	for name := range config.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Prepare creates a self-contained copy for one Herdr tab. When contextName is
// empty all contexts are retained. Otherwise only the selected context and its
// referenced cluster and user are retained.
func Prepare(source *clientcmdapi.Config, contextName, namespace string) (*clientcmdapi.Config, error) {
	if source == nil {
		return nil, errors.New("prepare kubeconfig: configuration is nil")
	}

	config := source.DeepCopy()
	targetContext := contextName
	if contextName != "" {
		if _, ok := config.Contexts[contextName]; !ok {
			return nil, fmt.Errorf("prepare kubeconfig: context %q does not exist", contextName)
		}
		config.CurrentContext = contextName
	} else if namespace != "" {
		targetContext = config.CurrentContext
		if targetContext == "" {
			return nil, errors.New("prepare kubeconfig: namespace requires a current context when context is omitted")
		}
		if _, ok := config.Contexts[targetContext]; !ok {
			return nil, fmt.Errorf("prepare kubeconfig: current context %q does not exist", targetContext)
		}
	}

	if namespace != "" {
		config.Contexts[targetContext].Namespace = namespace
	}

	if contextName != "" {
		if err := clientcmdapi.MinifyConfig(config); err != nil {
			return nil, fmt.Errorf("minify kubeconfig: %w", err)
		}
	}
	if err := clientcmdapi.FlattenConfig(config); err != nil {
		return nil, fmt.Errorf("flatten kubeconfig: %w", err)
	}
	if err := clientcmd.Validate(*config); err != nil {
		return nil, fmt.Errorf("prepare kubeconfig: %w", err)
	}

	return config, nil
}

// Write stores config atomically below stateDir/kubeconfigs with private
// permissions and returns its absolute path.
func Write(stateDir string, config *clientcmdapi.Config) (path string, err error) {
	if stateDir == "" {
		return "", errors.New("write kubeconfig: state directory is empty")
	}
	if config == nil {
		return "", errors.New("write kubeconfig: configuration is nil")
	}

	dir, err := filepath.Abs(filepath.Join(stateDir, "kubeconfigs"))
	if err != nil {
		return "", fmt.Errorf("resolve kubeconfig directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create kubeconfig directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure kubeconfig directory: %w", err)
	}

	contents, err := clientcmd.Write(*config)
	if err != nil {
		return "", fmt.Errorf("serialize kubeconfig: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".kubeconfig-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary kubeconfig: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err = tmp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("secure temporary kubeconfig: %w", err)
	}
	if _, err = tmp.Write(contents); err != nil {
		return "", fmt.Errorf("write temporary kubeconfig: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary kubeconfig: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", fmt.Errorf("close temporary kubeconfig: %w", err)
	}

	name, err := randomName()
	if err != nil {
		return "", err
	}
	path = filepath.Join(dir, name+".yaml")
	if err = os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("install kubeconfig: %w", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("secure kubeconfig: %w", err)
	}
	return path, nil
}

func randomName() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate kubeconfig name: %w", err)
	}
	return hex.EncodeToString(b), nil
}
