package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/herdr"
	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/kubeconfig"
	"github.com/tkuchiki/herdr-plugin-k8s-context/internal/state"
)

func Run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: herdr-plugin-k8s-context <open|picker|cleanup|cleanup-tab>")
	}
	env := herdr.EnvironmentFromOS()
	client := herdr.Client{Env: env, Runner: herdr.ExecRunner{}}
	switch args[0] {
	case "open":
		return client.OpenPicker(ctx)
	case "picker":
		return runPicker(ctx, env, client)
	case "cleanup":
		return cleanup(ctx, env, client)
	case "cleanup-tab":
		return cleanupTab(env)
	default:
		return fmt.Errorf("unknown command %q (expected open, picker, cleanup, or cleanup-tab)", args[0])
	}
}

func runPicker(ctx context.Context, env herdr.Environment, client herdr.Client) error {
	if err := env.ValidatePicker(); err != nil {
		if displayErr := showError(err); displayErr != nil {
			return err
		}
		return nil
	}

	store := state.New(env.StateDir, env.SocketPath)
	if err := cleanup(ctx, env, client); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cleanup skipped: %v\n", err)
	}

	config, err := kubeconfig.Load()
	if err != nil {
		if displayErr := showError(err); displayErr != nil {
			return err
		}
		return nil
	}

	defaultName := ""
	if count, countErr := client.TabCount(ctx, env.WorkspaceID); countErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not determine default tab name: %v\n", countErr)
	} else {
		defaultName = defaultTabName(count)
	}

	for {
		values, err := runForm(defaultName, config.CurrentContext, kubeconfig.ContextNames(config))
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("run form: %w", err)
		}

		prepared, err := kubeconfig.Prepare(config, values.Context, values.Namespace)
		if err == nil {
			var path string
			path, err = kubeconfig.Write(env.StateDir, prepared)
			if err == nil {
				var tabID string
				tabID, err = client.CreateTab(ctx, herdr.CreateTabOptions{Name: values.Name, Kubeconfig: path})
				if err != nil {
					_ = os.Remove(path)
				} else {
					if tabID != "" {
						if lifecycleErr := recordTabLifecycle(ctx, client, store, tabID, path); lifecycleErr != nil {
							fmt.Fprintf(os.Stderr, "Warning: %v\n", lifecycleErr)
						}
					} else {
						fmt.Fprintln(os.Stderr, "Warning: tab was created, but its ID could not be read; kubeconfig will be retained")
					}
					return nil
				}
			}
		}

		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		retry, retryErr := confirmRetry()
		if errors.Is(retryErr, huh.ErrUserAborted) || !retry {
			return nil
		}
		if retryErr != nil {
			return fmt.Errorf("run retry prompt: %w", retryErr)
		}
	}
}

func cleanup(ctx context.Context, env herdr.Environment, client herdr.Client) error {
	if err := env.ValidateLifecycle(); err != nil {
		return err
	}
	live, err := client.ListTabIDs(ctx)
	if err != nil {
		return err
	}
	return state.New(env.StateDir, env.SocketPath).Cleanup(live)
}

func cleanupTab(env herdr.Environment) error {
	if err := env.ValidateLifecycle(); err != nil {
		return err
	}
	tabID, err := herdr.ClosedTabID(env.EventName, env.EventJSON)
	if err != nil {
		return err
	}
	return state.New(env.StateDir, env.SocketPath).RemoveTab(tabID)
}

func recordTabLifecycle(ctx context.Context, client herdr.Client, store state.Store, tabID, path string) error {
	if err := store.Add(state.Entry{TabID: tabID, Path: path}); err != nil {
		return fmt.Errorf("could not record kubeconfig lifecycle: %w", err)
	}
	live, err := client.ListTabIDs(ctx)
	if err != nil {
		return fmt.Errorf("could not verify created tab lifecycle: %w", err)
	}
	if _, ok := live[tabID]; ok {
		return nil
	}
	if err := store.RemoveTab(tabID); err != nil {
		return fmt.Errorf("could not remove kubeconfig for an already closed tab: %w", err)
	}
	return nil
}

type formValues struct {
	Name      string
	Context   string
	Namespace string
}

func runForm(defaultName, current string, names []string) (formValues, error) {
	values := formValues{Name: defaultName}
	options := []huh.Option[string]{huh.NewOption("All contexts (keep current)", "").Selected(true)}
	for _, name := range names {
		label := name
		if name == current {
			label += " (current)"
		}
		options = append(options, huh.NewOption(label, name))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Description("Herdr tab name").
				Value(&values.Name).
				Validate(func(value string) error {
					if strings.TrimSpace(value) == "" {
						return errors.New("name is required")
					}
					return nil
				}),
			huh.NewSelect[string]().
				Title("Context").
				Description("Leave blank to copy every context").
				Options(options...).
				Value(&values.Context).
				Height(8),
			huh.NewInput().
				Title("Namespace").
				Description("Optional default namespace").
				Value(&values.Namespace),
		),
	).WithShowHelp(true)
	if err := form.Run(); err != nil {
		return formValues{}, err
	}
	values.Name = strings.TrimSpace(values.Name)
	values.Context = strings.TrimSpace(values.Context)
	values.Namespace = strings.TrimSpace(values.Namespace)
	return values, nil
}

func defaultTabName(tabCount int) string {
	return strconv.Itoa(tabCount + 1)
}

func confirmRetry() (bool, error) {
	retry := true
	err := huh.NewConfirm().
		Title("Try again?").
		Affirmative("Retry").
		Negative("Cancel").
		Value(&retry).
		Run()
	return retry, err
}

func showError(err error) error {
	return huh.NewNote().
		Title("Unable to open Kubernetes context tab").
		Description(err.Error()).
		Next(true).
		NextLabel("Close").
		Run()
}
