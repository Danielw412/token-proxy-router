package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update"
)

const restartWait = 3 * time.Minute

func cmdUpdate(c *context_, args []string) error {
	flags := newFlags(c, "update")
	check := flags.Bool("check", false, "only look for a newer version")
	rollback := flags.Bool("rollback", false, "go back to the previous version")
	version := flags.String("version", "", "install this exact version, allowing a downgrade")
	configPath := configFlag(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	if *check {
		cache := update.Check(ctx, update.NewReleaseSource())
		if cache.LastError != "" {
			return fmt.Errorf("could not check for updates: %s", cache.LastError)
		}
		switch {
		case cache.Latest == "":
			fmt.Fprintf(c.out, "You have %s. No releases have been published yet.\n", c.build.Version)
		case update.Newer(cache.Latest, c.build.Version):
			fmt.Fprintf(c.out, "%s is available (you have %s). Run `tpr update`.\n%s\n", cache.Latest, c.build.Version, cache.NotesURL)
		default:
			fmt.Fprintf(c.out, "You have %s, and the latest release is %s.\n", c.build.Version, cache.Latest)
		}
		return nil
	}
	updater, err := update.New(c.build.Version)
	if err != nil {
		return err
	}
	if *rollback {
		result, err := updater.Rollback()
		if err != nil {
			return err
		}
		return c.finishUpdate(ctx, result, "Rolled back to")
	}
	result, err := updater.Apply(ctx, update.ApplyOptions{ConfigPath: *configPath, Version: *version})
	if errors.Is(err, update.ErrUpToDate) {
		fmt.Fprintln(c.out, err)
		return nil
	}
	if err != nil {
		return err
	}
	return c.finishUpdate(ctx, result, "Updated to")
}

func (c *context_) finishUpdate(ctx context.Context, result update.Result, verb string) error {
	if !result.Restarting {
		fmt.Fprintf(c.out, "%s %s.\n", verb, result.To)
		if info, err := loadInfo(activeConfig()); err == nil && serverUp(info.LocalURL()) {
			fmt.Fprintln(c.out, "A server is running here that is not under the service. Restart it to use the new version.")
		}
		return nil
	}
	fmt.Fprintln(c.out, "Restarting the service and checking the new version...")
	deadline := time.Now().Add(restartWait)
	for time.Now().Before(deadline) {
		if update.LoadPending() == nil {
			progress := update.LoadProgress()
			if progress.State == update.StateFailed {
				return errors.New(progress.Error)
			}
			fmt.Fprintf(c.out, "%s %s. The service is healthy.\n", verb, result.To)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return errors.New("the service has not finished restarting. Check `tpr status` and `tpr logs`")
}

func serverUp(baseURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second, Transport: insecureLoopback()}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
