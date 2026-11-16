package templated

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/avast/retry-go/v5"
)

var execCommand = exec.CommandContext

// deployOS stages, checks, atomically renames, and reloads the rendered file on the local filesystem.
func deployOS(ctx context.Context, logger *slog.Logger, target Target, content []byte) error {
	return retry.New(
		retry.Context(ctx),
		retry.Attempts(10),
		retry.Delay(250*time.Millisecond),
		retry.MaxDelay(3*time.Second),
		retry.DelayType(retry.BackOffDelay),
		retry.LastErrorOnly(true),
		retry.OnRetry(func(n uint, err error) {
			// A canceled deployment is not retried; the attempt only failed because of it.
			if ctx.Err() != nil {
				return
			}

			logger.Warn("retrying deployment", "attempt", n+1, "target", target.String(), "err", err)
		}),
	).Do(func() error {
		dir := filepath.Dir(target.Path)

		err := os.MkdirAll(dir, 0o750)
		if err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}

		// The file is read by another service (Caddy, Prometheus, ...) that may run as its own user,
		// so keep the mode an operator set on the live file and default to world-readable.
		mode := os.FileMode(0o644)

		fi, err := os.Stat(target.Path)
		if err == nil {
			mode = fi.Mode().Perm()
		}

		staging := filepath.Join(dir, filepath.Base(target.Path)+".tmp")

		err = os.WriteFile(staging, content, mode)
		if err != nil {
			return fmt.Errorf("writing staging file %s: %w", staging, err)
		}

		// WriteFile leaves the mode of a staging file left over from an earlier attempt untouched.
		err = os.Chmod(staging, mode)
		if err != nil {
			return fmt.Errorf("setting mode on staging file %s: %w", staging, err)
		}

		run := func(argv []string) error {
			if len(argv) == 0 {
				return nil
			}

			argv = expand(argv, staging, target.Path)

			var out bytes.Buffer

			cmd := execCommand(ctx, argv[0], argv[1:]...)
			cmd.Stdout = &out
			cmd.Stderr = &out

			err := cmd.Run()
			if err != nil {
				return fmt.Errorf("%q failed: %w (output: %q)", argv, err, out.String())
			}

			return nil
		}

		err = run(target.Format)
		if err != nil {
			return err
		}

		err = run(target.Validate)
		if err != nil {
			return err
		}

		err = os.Rename(staging, target.Path)
		if err != nil {
			return fmt.Errorf("atomic rename %s -> %s: %w", staging, target.Path, err)
		}

		err = run(target.Reload)
		if err != nil {
			return err
		}

		logger.Info("Deployed", "target", target.String())

		return nil
	})
}
