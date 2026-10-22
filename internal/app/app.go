package app

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
	"tui-ssh-workbench/internal/plugin"
	"tui-ssh-workbench/internal/ui"
)

// Application owns startup dependencies and coordinates the TUI lifecycle.
type Application struct {
	plugins *plugin.Registry
	storage *sqlite.Store
	config  model.Config
}

// New builds the application and registers its compiled-in plugins.
func New(plugins ...plugin.Plugin) (*Application, error) {
	return NewWithDatabase(context.Background(), "", plugins...)
}

// NewWithDatabase builds the application using the specified SQLite database.
// An empty path selects the per-user default.
func NewWithDatabase(ctx context.Context, databasePath string, plugins ...plugin.Plugin) (*Application, error) {
	return NewWithDatabaseAndConfig(ctx, databasePath, model.DefaultConfig(), plugins...)
}

// NewWithDatabaseAndConfig builds the application with explicit runtime settings.
func NewWithDatabaseAndConfig(ctx context.Context, databasePath string, config model.Config, plugins ...plugin.Plugin) (*Application, error) {
	config = config.WithDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	allPlugins := make([]plugin.Plugin, 0, len(plugins)+1)
	allPlugins = append(allPlugins, corePlugin{})
	allPlugins = append(allPlugins, plugins...)
	registry, err := plugin.NewRegistry(allPlugins...)
	if err != nil {
		return nil, err
	}
	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	return &Application{plugins: registry, storage: storage, config: config}, nil
}

// PluginRegistry returns the registry used by the command palette.
func (a *Application) PluginRegistry() *plugin.Registry {
	return a.plugins
}

// Storage returns the local host and group repository for application actions.
func (a *Application) Storage() *sqlite.Store {
	return a.storage
}

// Close releases application resources.
func (a *Application) Close() error {
	if a == nil || a.storage == nil {
		return nil
	}
	return a.storage.Close()
}

// Run starts the Bubble Tea user interface.
func (a *Application) Run() (runErr error) {
	defer func() {
		if closeErr := a.Close(); runErr == nil && closeErr != nil {
			runErr = fmt.Errorf("close local database: %w", closeErr)
		}
	}()
	program := tea.NewProgram(ui.NewModelWithConfig(a.storage, a.config, a.plugins))
	runnerContext, cancelRunner := context.WithCancel(context.Background())
	runnerDone := startScheduledTransferRunner(runnerContext, a.storage, a.config, func(err error) {
		program.Send(ui.ScheduledTransferRunnerFailed(err))
	})

	finalModel, err := program.Run()
	cancelRunner()
	runnerErr := <-runnerDone
	if err == nil && runnerErr != nil {
		err = fmt.Errorf("run scheduled transfer service: %w", runnerErr)
	}
	runErr = err
	if closer, ok := finalModel.(interface{ Close() error }); ok {
		if closeErr := closer.Close(); runErr == nil && closeErr != nil {
			runErr = fmt.Errorf("close terminal session: %w", closeErr)
		}
	}
	return runErr
}

func startScheduledTransferRunner(ctx context.Context, storage *sqlite.Store, config model.Config, onFailure func(error)) <-chan error {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan error, 1)
	go func() {
		err := runScheduledTransfersWithStore(ctx, storage, config, nil, nil)
		if err != nil && ctx.Err() == nil && onFailure != nil {
			onFailure(err)
		}
		done <- err
	}()
	return done
}
