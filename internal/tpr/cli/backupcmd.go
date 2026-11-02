package cli

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/backup"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/service"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/setup"
)

// EnvBackupPassphrase supplies the passphrase for scripts.
const EnvBackupPassphrase = "TPR_BACKUP_PASSPHRASE"

const managementBase = "/v8/management/tpr"

func activeConfig() string { return setup.ActiveConfigPath() }

// insecureLoopback lets CLI calls reach a local server with a self-signed
// certificate. It is only ever used for the machine's own loopback address.
func insecureLoopback() http.RoundTripper {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // loopback only
}

func (c *context_) readPassphrase(confirm bool) (string, error) {
	if value := os.Getenv(EnvBackupPassphrase); value != "" {
		return value, nil
	}
	file, isFile := c.in.(*os.File)
	if !isFile || !term.IsTerminal(file.Fd()) {
		line, err := bufio.NewReader(c.in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value := strings.TrimRight(line, "\r\n")
		if value == "" {
			return "", fmt.Errorf("no passphrase given. Type it, pipe it in, or set %s", EnvBackupPassphrase)
		}
		return value, nil
	}
	read := func(prompt string) (string, error) {
		fmt.Fprint(c.errOut, prompt)
		raw, err := term.ReadPassword(file.Fd())
		fmt.Fprintln(c.errOut)
		return string(raw), err
	}
	value, err := read("Passphrase: ")
	if err != nil {
		return "", err
	}
	if confirm {
		again, err := read("Again: ")
		if err != nil {
			return "", err
		}
		if again != value {
			return "", errors.New("the two passphrases differ")
		}
	}
	return value, nil
}

func managementKeyOrError() (string, error) {
	key := setup.ManagementKey()
	if key == "" {
		return "", fmt.Errorf("the server is running, so this goes through its API, which needs the management key. Set %s to your key", setup.EnvManagementKey)
	}
	return key, nil
}

func cmdBackup(c *context_, args []string) error {
	flags := newFlags(c, "backup")
	configPath := configFlag(flags)
	positional, err := parseAnywhere(flags, args)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New("usage: tpr backup [FILE]")
	}
	if err := home.Ensure(); err != nil {
		return err
	}
	target := filepath.Join(home.BackupsDir(), "tpr-"+time.Now().UTC().Format("20060102-150405")+".tprbak")
	if len(positional) == 1 {
		target = positional[0]
	}
	passphrase, err := c.readPassphrase(true)
	if err != nil {
		return err
	}
	if err := backup.CheckPassphrase(passphrase); err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("%s already exists. Choose another name", target)
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	ctx, cancel := signalContext()
	defer cancel()
	errWrite := writeBackup(ctx, file, passphrase, *configPath, c.build.Version)
	errClose := file.Close()
	if errWrite != nil {
		_ = os.Remove(target)
		return errWrite
	}
	if errClose != nil {
		return errClose
	}
	fmt.Fprintf(c.out, "Backup written to %s\nKeep the passphrase safe: without it this file cannot be opened.\n", target)
	return nil
}

func writeBackup(ctx context.Context, w io.Writer, passphrase, configPath, version string) error {
	if info, err := setup.LoadServerInfo(configPath); err == nil && serverUp(info.LocalURL()) {
		// The server holds the database open, so only it can copy it consistently.
		key, err := managementKeyOrError()
		if err != nil {
			return err
		}
		body, err := json.Marshal(map[string]string{"passphrase": passphrase})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, info.LocalURL()+managementBase+"/backup", strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Transport: insecureLoopback()}).Do(req)
		if err != nil {
			return fmt.Errorf("ask the server for a backup: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the server refused the backup: %s", readAPIError(resp))
		}
		_, err = io.Copy(w, resp.Body)
		return err
	}
	return backup.Write(ctx, w, passphrase, backup.Options{ConfigPath: configPath, Version: version})
}

func readAPIError(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var parsed struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
		return parsed.Error
	}
	return resp.Status
}

func cmdRestore(c *context_, args []string) error {
	flags := newFlags(c, "restore")
	configPath := configFlag(flags)
	positional, err := parseAnywhere(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: tpr restore FILE")
	}
	file, err := os.Open(positional[0])
	if err != nil {
		return fmt.Errorf("open the backup: %w", err)
	}
	defer func() { _ = file.Close() }()
	passphrase, err := c.readPassphrase(false)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()

	if info, errInfo := setup.LoadServerInfo(*configPath); errInfo == nil && serverUp(info.LocalURL()) {
		return c.restoreThroughServer(ctx, file, passphrase, info)
	}
	opts := backup.Options{ConfigPath: *configPath, Version: c.build.Version}
	inspect, snapshot, err := backup.Stage(ctx, file, passphrase, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Backup from %s is valid (%d files).\nThe previous state is saved in %s\n", inspect.Manifest.CreatedAt, inspect.Files, snapshot)
	if service.SupervisorRunning() {
		if err := service.RequestRestart(); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "The service is restarting to apply it.")
		return nil
	}
	if _, err := backup.ApplyPending(*configPath); err != nil {
		return fmt.Errorf("apply the backup: %w", err)
	}
	fmt.Fprintln(c.out, "Restored.")
	return nil
}

func (c *context_) restoreThroughServer(ctx context.Context, file *os.File, passphrase string, info setup.ServerInfo) error {
	key, err := managementKeyOrError()
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		err := form.WriteField("passphrase", passphrase)
		if err == nil {
			var part io.Writer
			if part, err = form.CreateFormFile("file", filepath.Base(file.Name())); err == nil {
				_, err = io.Copy(part, file)
			}
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, info.LocalURL()+managementBase+"/restore", reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := (&http.Client{Transport: insecureLoopback()}).Do(req)
	if err != nil {
		return fmt.Errorf("send the backup to the server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("the server refused the backup: %s", readAPIError(resp))
	}
	var result struct {
		Restarting bool   `json:"restarting"`
		Snapshot   string `json:"snapshot"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Backup accepted. The previous state is saved in %s\n", result.Snapshot)
	if result.Restarting {
		fmt.Fprintln(c.out, "The service is restarting to apply it.")
	} else {
		fmt.Fprintln(c.out, "Restart the server to apply it.")
	}
	return nil
}
