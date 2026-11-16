package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jochumdev/incus-templated/templated"
)

func skipE2E(t *testing.T) {
	t.Helper()

	if os.Getenv("TEST_LOCAL") != "" || os.Getenv("INCUS_TEMPLATED_TEST_LOCAL") != "" || os.Getenv("INCUS_COMPOSE_TEST_LOCAL") != "" {
		t.Skip("skipping E2E test in local mode")
	}

	if os.Getenv("TEST_E2E") == "" && os.Getenv("INCUS_TEMPLATED_TEST_E2E") == "" && os.Getenv("INCUS_COMPOSE_TEST_E2E") == "" {
		t.Skip("skipping E2E test: run with just test-e2e or set TEST_E2E=1")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	require.NoError(t, err)

	return strings.TrimSpace(string(out))
}

func incusRemote() string {
	remote := os.Getenv("INCUS_REMOTE")
	if remote != "" {
		return remote
	}

	return "ict-daily-dev01-main"
}

func runCompose(ctx context.Context, t *testing.T, project, fixture string, args ...string) (string, error) {
	t.Helper()

	cmdArgs := []string{
		"--remote", incusRemote(),
		"--project-name", project,
		"--file", fixture,
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "incus-compose", cmdArgs...)
	cmd.Env = append(os.Environ(), "INCUS_REMOTE="+incusRemote())

	out, err := cmd.CombinedOutput()

	return string(out), err
}

func TestE2ECaddyReverseProxy(t *testing.T) {
	skipE2E(t)

	ctx := t.Context()
	project := fmt.Sprintf("test-caddy-e2e-%d", time.Now().UnixNano()%1000000)
	fixture := filepath.Join(repoRoot(t), "test", "fixtures", "e2e", "compose.yaml")

	t.Cleanup(func() {
		downCtx, downCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer downCancel()

		_, _ = runCompose(downCtx, t, project, fixture, "down", "--project")
	})

	t.Logf("deploying fixture stack to project %s on remote %s", project, incusRemote())
	out, err := runCompose(ctx, t, project, fixture, "up", "-d")
	require.NoError(t, err, "incus-compose up failed: %s", out)

	configCtx, configCancel := context.WithCancel(ctx)
	defer configCancel()

	// A per-vhost template caddy validate rejects, for the validation failure below.
	templatesDir := t.TempDir()
	err = os.WriteFile(filepath.Join(templatesDir, "bad.caddyfile"), []byte("{{ .Domain }} {\n\tnot_a_directive\n}\n"), 0o600)
	require.NoError(t, err)

	cfg := newConfig()
	cfg.TemplatesDir = templatesDir
	namesTmpl := `[{{ range $i, $inst := .Instances }}{{ if $i }},{{ end }}{{ json $inst.Name }}{{ end }}]`
	// Two more targets on the same label with no commands, what a Prometheus file_sd consumer needs: one on the
	// host, one in sink's root filesystem, which is written through the instance's SFTP rather than a volume's.
	sdPath := filepath.Join(t.TempDir(), "sd", "targets.json")
	cfg.Targets = []templated.Target{
		{Label: "edge", Preset: "caddy", Project: project, Instance: "caddy"},
		{Label: "edge", Path: sdPath, Text: namesTmpl},
		{Label: "edge", Project: project, Instance: "sink", Path: "/etc/targets.json", Text: namesTmpl},
	}
	cfg.Projects = []string{project}
	cfg.Remote = incusRemote()
	cfg.UseRemote = true
	cfg.Log = "DEBUG"
	cfg.DebounceWindow = 100 * time.Millisecond
	cfg.ReadDelay = 500 * time.Millisecond
	cfg.ProjectDelay = 1 * time.Second

	args, err := cfg.validate()
	require.NoError(t, err)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = mainAction(configCtx, args)
	}()

	defer func() {
		configCancel()
		<-runDone
	}()

	// 1. Verify Caddyfile is deployed with reverse_proxy and both api upstreams.
	t.Log("waiting for incus-templated to deploy Caddyfile with both upstreams")
	require.Eventually(t, func() bool {
		caddyfile, readErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy", "cat", "/config/Caddyfile")
		if readErr != nil {
			t.Logf("reading Caddyfile failed: %v, output: %s", readErr, caddyfile)

			return false
		}

		t.Logf("current Caddyfile:\n%s", caddyfile)

		return strings.Contains(caddyfile, "api.example.test") && strings.Count(caddyfile, ":8080") == 2
	}, 45*time.Second, 2*time.Second, "Caddyfile was not reconciled with two upstreams")

	t.Log("verifying the command-less target landed on the host")
	require.Eventually(t, func() bool {
		sd, readErr := os.ReadFile(sdPath)

		return readErr == nil && string(sd) == `["api1","api2"]`
	}, 15*time.Second, time.Second, "targets.json was not rendered")

	t.Log("verifying the rootfs target landed in sink, readable by other users")
	require.Eventually(t, func() bool {
		sd, readErr := runCompose(ctx, t, project, fixture, "exec", "-T", "sink", "cat", "/etc/targets.json")

		return readErr == nil && strings.TrimSpace(sd) == `["api1","api2"]`
	}, 15*time.Second, time.Second, "sink:/etc/targets.json was not rendered")

	mode, err := runCompose(ctx, t, project, fixture, "exec", "-T", "sink", "stat", "-c", "%a", "/etc/targets.json")
	require.NoError(t, err, "stat failed: %s", mode)
	require.Equal(t, "644", strings.TrimSpace(mode))

	// The next re-render, on the scale-down below, must keep a mode an operator set on the live file.
	out, err = runCompose(ctx, t, project, fixture, "exec", "-T", "sink", "chmod", "600", "/etc/targets.json")
	require.NoError(t, err, "chmod failed: %s", out)

	// 2. Verify HTTP reverse proxying works and reaches a busybox API server.
	t.Log("verifying HTTP reverse proxying through Caddy")
	require.Eventually(t, func() bool {
		resp, httpErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy",
			"wget", "-q", "-O", "-", "--header", "Host: api.example.test", "http://127.0.0.1:80/")
		if httpErr != nil {
			t.Logf("HTTP query failed: %v, output: %s", httpErr, resp)

			return false
		}

		body := strings.TrimSpace(resp)

		return body == "api1-ok" || body == "api2-ok"
	}, 20*time.Second, time.Second, "HTTP request through Caddy failed")

	// 3. Scale-down: Stop api1. Caddyfile should drop api1 and keep only api2.
	t.Log("stopping api1 to test dynamic scale-down")
	out, err = runCompose(ctx, t, project, fixture, "stop", "api1")
	require.NoError(t, err, "stopping api1 failed: %s", out)

	t.Log("waiting for incus-templated to reconcile to single upstream api2")
	require.Eventually(t, func() bool {
		caddyfile, readErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy", "cat", "/config/Caddyfile")
		if readErr != nil {
			return false
		}

		return strings.Contains(caddyfile, "api.example.test") && strings.Count(caddyfile, ":8080") == 1
	}, 45*time.Second, time.Second, "Caddyfile was not updated to 1 upstream after stopping api1")

	// Verify only api2 answers now.
	require.Eventually(t, func() bool {
		resp, httpErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy",
			"wget", "-q", "-O", "-", "--header", "Host: api.example.test", "http://127.0.0.1:80/")
		if httpErr != nil {
			return false
		}

		return strings.TrimSpace(resp) == "api2-ok"
	}, 15*time.Second, time.Second, "traffic did not route exclusively to api2")

	t.Log("verifying the re-rendered rootfs target kept its live mode")
	require.Eventually(t, func() bool {
		sd, readErr := runCompose(ctx, t, project, fixture, "exec", "-T", "sink", "cat", "/etc/targets.json")

		return readErr == nil && strings.TrimSpace(sd) == `["api2"]`
	}, 15*time.Second, time.Second, "sink:/etc/targets.json was not re-rendered")

	mode, err = runCompose(ctx, t, project, fixture, "exec", "-T", "sink", "stat", "-c", "%a", "/etc/targets.json")
	require.NoError(t, err, "stat failed: %s", mode)
	require.Equal(t, "600", strings.TrimSpace(mode))

	// 4. Scale-up: Restart api1. Caddyfile should restore both upstreams.
	t.Log("starting api1 to test dynamic scale-up")
	out, err = runCompose(ctx, t, project, fixture, "start", "api1")
	require.NoError(t, err, "starting api1 failed: %s", out)

	t.Log("waiting for incus-templated to reconcile back to two upstreams")
	require.Eventually(t, func() bool {
		caddyfile, readErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy", "cat", "/config/Caddyfile")
		if readErr != nil {
			return false
		}

		return strings.Contains(caddyfile, "api.example.test") && strings.Count(caddyfile, ":8080") == 2
	}, 45*time.Second, time.Second, "Caddyfile was not restored to 2 upstreams after restarting api1")

	// 5. Volume persistence across reboots: Restart Caddy and verify it still proxies traffic.
	t.Log("restarting Caddy to test volume persistence across reboots")
	out, err = runCompose(ctx, t, project, fixture, "restart", "caddy")
	require.NoError(t, err, "restarting caddy failed: %s", out)

	require.Eventually(t, func() bool {
		resp, httpErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy",
			"wget", "-q", "-O", "-", "--header", "Host: api.example.test", "http://127.0.0.1:80/")
		if httpErr != nil {
			return false
		}

		body := strings.TrimSpace(resp)

		return body == "api1-ok" || body == "api2-ok"
	}, 15*time.Second, time.Second, "traffic failed after Caddy container restart")

	incus := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "incus", append(args, "--project", project)...)
		out, err := cmd.CombinedOutput()

		return string(out), err
	}

	// 6. Validation failure: a vhost caddy validate rejects is staged but never replaces the live file.
	t.Log("giving api2 a vhost that fails caddy validate")
	out, err = incus("config", "set", incusRemote()+":api2", "user.label.edge=http://bad.example.test,upstream=8080,template=bad.caddyfile")
	require.NoError(t, err, "setting api2's label failed: %s", out)

	require.Eventually(t, func() bool {
		staged, readErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy", "cat", "/config/Caddyfile.tmp")

		return readErr == nil && strings.Contains(staged, "bad.example.test")
	}, 30*time.Second, time.Second, "the rejected Caddyfile was never staged")

	caddyfile, err := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy", "cat", "/config/Caddyfile")
	require.NoError(t, err, "reading Caddyfile failed: %s", caddyfile)
	require.NotContains(t, caddyfile, "bad.example.test", "a Caddyfile that failed validation replaced the live one")
	require.Equal(t, 2, strings.Count(caddyfile, ":8080"))

	resp, err := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy",
		"wget", "-q", "-O", "-", "--header", "Host: api.example.test", "http://127.0.0.1:80/")
	require.NoError(t, err, "traffic failed while the bad vhost was pending: %s", resp)

	out, err = incus("config", "set", incusRemote()+":api2", "user.label.edge=http://api.example.test,upstream=8080")
	require.NoError(t, err, "restoring api2's label failed: %s", out)

	// 7. Stopped target: the file is still written to Caddy's volume, and Caddy boots with it.
	t.Log("stopping Caddy, then api1, to test deploying to a stopped instance")
	out, err = runCompose(ctx, t, project, fixture, "stop", "caddy")
	require.NoError(t, err, "stopping caddy failed: %s", out)

	out, err = runCompose(ctx, t, project, fixture, "stop", "api1")
	require.NoError(t, err, "stopping api1 failed: %s", out)

	pool, err := incus("config", "device", "get", incusRemote()+":caddy", "vol-caddy-config", "pool")
	require.NoError(t, err, "reading the volume's pool failed: %s", pool)

	require.Eventually(t, func() bool {
		onVolume, readErr := incus("storage", "volume", "file", "pull", incusRemote()+":"+strings.TrimSpace(pool), "vol-caddy-config/Caddyfile", "-")

		return readErr == nil && strings.Contains(onVolume, "api.example.test") && strings.Count(onVolume, ":8080") == 1
	}, 45*time.Second, 2*time.Second, "the Caddyfile on the stopped instance's volume was not updated")

	out, err = runCompose(ctx, t, project, fixture, "start", "caddy")
	require.NoError(t, err, "starting caddy failed: %s", out)

	// The live config, not a response: a stale config with api1 still in it would answer from api2 as well.
	require.Eventually(t, func() bool {
		routes, readErr := runCompose(ctx, t, project, fixture, "exec", "-T", "caddy",
			"wget", "-q", "-O", "-", "http://127.0.0.1:2019/config/apps/http/servers/srv0/routes")

		return readErr == nil && strings.Count(routes, `"dial"`) == 1
	}, 30*time.Second, time.Second, "Caddy did not boot with the file written while it was stopped")
}
