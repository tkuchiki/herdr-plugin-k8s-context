package kubeconfig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/securefile"
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

// Prepare creates a flattened copy for one Herdr tab. When contextName is empty
// all contexts are retained. Otherwise only the selected context and its
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

// ReservePath returns a random absolute path below stateDir/kubeconfigs. It
// does not create the final file, allowing callers to persist lifecycle state
// before installing credentials at that path.
func ReservePath(stateDir string) (string, error) {
	if stateDir == "" {
		return "", errors.New("reserve kubeconfig: state directory is empty")
	}

	dir, err := filepath.Abs(filepath.Join(stateDir, "kubeconfigs"))
	if err != nil {
		return "", fmt.Errorf("reserve kubeconfig: resolve directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("reserve kubeconfig: create directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("reserve kubeconfig: secure directory: %w", err)
	}
	name, err := randomName()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".yaml"), nil
}

// WriteToPath atomically installs config at a path returned by ReservePath.
func WriteToPath(path string, config *clientcmdapi.Config) error {
	if path == "" {
		return errors.New("write kubeconfig: path is empty")
	}
	if config == nil {
		return errors.New("write kubeconfig: configuration is nil")
	}
	contents, err := clientcmd.Write(*config)
	if err != nil {
		return fmt.Errorf("serialize kubeconfig: %w", err)
	}

	if err := securefile.WriteAtomic(path, ".kubeconfig-*.tmp", contents); err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}
	return nil
}

func randomName() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate kubeconfig name: %w", err)
	}
	return hex.EncodeToString(b), nil
}
