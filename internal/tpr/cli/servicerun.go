package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update"
)

const serviceLogMaxSize = 20 << 20

// cmdServiceRun is the supervisor: what the systemd unit and Windows autostart run.
func cmdServiceRun(c *context_, args []string) error {
	flags := newFlags(c, "service-run")
	detach := flags.Bool("detach", false, "start in the background and return")
	dataHome := flags.String("home", "", "data folder, for autostart entries that cannot set TPR_HOME")
	configPath := flags.String("config", "", "config file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dataHome != "" {
		if err := os.Setenv(home.EnvHome, *dataHome); err != nil {
			return fmt.Errorf("set data folder: %w", err)
		}
	}
	exe, err := update.TargetPath()
	if err != nil {
		return err
	}
	if *detach {
		// Windows autostart launches a console program. Handing over to a process
		// with no window is what keeps a console from staying open.
		return service.StartDetached(exe)
	}
	path := *configPath
	if path == "" {
		path = home.ConfigPath()
	}
	first, err := setup.Bootstrap(path)
	if err != nil {
		return err
	}
	if first != nil {
		first.Print(c.out)
	}
	logFile := &service.RotatingFile{Path: service.LogPath(), MaxSize: serviceLogMaxSize}
	defer func() { _ = logFile.Close() }()
	update.CleanupAside(exe)
	supervisor := &service.Supervisor{
		Exe:       exe,
		Args:      []string{"-config", path},
		Dir:       filepath.Clean(home.Dir()),
		HealthURL: func() string { return service.HealthURLFor(path) },
		Pending:   update.PendingCheck,
		Log:       logFile,
	}
	ctx, cancel := signalContext()
	defer cancel()
	return supervisor.Run(ctx)
}
