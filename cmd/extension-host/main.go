// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Command extension-host is the extension host binary (eshu-hq/eshu#8877):
// dark by default, serving operator health only, with the enabled claim
// flow behind the cutover gate (--claims-enabled or
// ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1). Dark mode never opens Postgres,
// never dials a claim plane, and never claims work; enabled mode polls the
// plane and runs one collector call per held claim until SIGTERM/SIGINT.
// --dry-run shadows the enabled flow: claims, leases, and heartbeats run
// for real while the commit step only logs the result shape.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is stamped by the release workflow
// (-ldflags "-X main.version=..."); local builds report dev.
var version = "dev"

// defaultAdminListen is the only listen surface: the operator admin
// listener for health, readiness, and metrics.
const defaultAdminListen = ":8082"

type serverConfig struct {
	adminListen   string
	showVersion   bool
	claimsEnabled bool
	dryRun        bool
}

// flagValues is the single constructor output for the binary's flag
// surface. The surface is enforced by enumeration: exactly these flags
// exist, so any other claim-flow knob fails closed at parse time. The
// plane URL, lease timings, producer identity, and collector path stay
// environment-only by design and never appear here.
type flagValues struct {
	set           *flag.FlagSet
	adminListen   *string
	showVersion   *bool
	claimsEnabled *bool
	dryRun        *bool
}

func newFlagSet() *flagValues {
	fs := flag.NewFlagSet("extension-host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fv := &flagValues{
		set:           fs,
		adminListen:   fs.String("admin-listen", defaultAdminListen, "admin listen address (health, readiness, metrics)"),
		showVersion:   fs.Bool("version", false, "print the stamped build version and exit"),
		claimsEnabled: fs.Bool("claims-enabled", false, "run the cutover worker loop (or ESHU_EXTENSIONHOST_CUTOVER_ENABLED=1)"),
		dryRun:        fs.Bool("dry-run", false, "with claims enabled, shadow commits: log result shape, never Complete"),
	}
	return fv
}

// flagNames reports the sorted flag surface for the boundary test.
func flagNames() []string {
	var names []string
	newFlagSet().set.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	return names
}

func parseFlags(args []string) (serverConfig, error) {
	fv := newFlagSet()
	if err := fv.set.Parse(args); err != nil {
		return serverConfig{}, fmt.Errorf("flag parse: %w", err)
	}
	cfg := serverConfig{adminListen: *fv.adminListen, showVersion: *fv.showVersion, dryRun: *fv.dryRun}
	if cfg.showVersion {
		return cfg, nil
	}
	envOn, err := cutoverEnabledByEnv(os.Getenv)
	if err != nil {
		return serverConfig{}, err
	}
	cfg.claimsEnabled = *fv.claimsEnabled || envOn
	if cfg.dryRun && !cfg.claimsEnabled {
		return serverConfig{}, fmt.Errorf("dry-run requires claims-enabled (--claims-enabled or %s=1)", envCutoverEnabled)
	}
	return cfg, nil
}

// modeName and claimsBit render the cutover gate on the operator surface:
// dark by default, enabled behind the gate.
func modeName(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "dark"
}

func claimsBit(enabled bool) int {
	if enabled {
		return 1
	}
	return 0
}

// newMux wires the only three routes the binary serves. There is no
// claim, admission, or mutation surface: unknown paths are 404. The
// health and metrics shapes expose the cutover gate so an operator can
// tell dark from enabled without reading flags.
func newMux(buildVersion string, startedAt time.Time, enabled bool) *http.ServeMux {
	mux := http.NewServeMux()
	status := fmt.Sprintf(`{"status":"ok","mode":%q,"claims_enabled":%d,"version":%q}`+"\n",
		modeName(enabled), claimsBit(enabled), buildVersion)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, status)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, status)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		uptime := int64(time.Since(startedAt).Seconds())
		if uptime < 0 {
			uptime = 0
		}
		_, _ = fmt.Fprintf(w, "# HELP extension_host_build_info Stamped build version.\n")
		_, _ = fmt.Fprintf(w, "# TYPE extension_host_build_info gauge\n")
		_, _ = fmt.Fprintf(w, "extension_host_build_info{version=%q} 1\n", buildVersion)
		_, _ = fmt.Fprintf(w, "# HELP extension_host_claims_enabled Whether the claim flow is enabled.\n")
		_, _ = fmt.Fprintf(w, "# TYPE extension_host_claims_enabled gauge\n")
		_, _ = fmt.Fprintf(w, "extension_host_claims_enabled %d\n", claimsBit(enabled))
		_, _ = fmt.Fprintf(w, "# HELP extension_host_uptime_seconds Seconds since process start.\n")
		_, _ = fmt.Fprintf(w, "# TYPE extension_host_uptime_seconds counter\n")
		_, _ = fmt.Fprintf(w, "extension_host_uptime_seconds %d\n", uptime)
	})
	return mux
}

// serve runs the admin listener until ctx is done, then shuts down
// gracefully. The listener is passed in so a bad address fails before any
// goroutine starts.
func serve(ctx context.Context, ln net.Listener, buildVersion string, startedAt time.Time, enabled bool) error {
	srv := &http.Server{Handler: newMux(buildVersion, startedAt, enabled), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			done <- err
			return
		}
		done <- nil
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("admin shutdown: %w", err)
		}
		return <-done
	case err := <-done:
		return err
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWithContext(ctx, args, stdout, stderr)
}

func runWithContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "extension-host: %v\n", err)
		return 2
	}
	if cfg.showVersion {
		_, _ = fmt.Fprintf(stdout, "extension-host %s\n", version)
		return 0
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	if cfg.claimsEnabled {
		return runEnabled(ctx, cfg, logger, stderr)
	}
	ln, err := net.Listen("tcp", cfg.adminListen)
	if err != nil {
		logger.Error("admin listen failed, failing closed",
			"event_name", "runtime.startup.failed", "error", err)
		return 1
	}
	logger.Info("extension-host dark posture: health only, zero claim flow",
		"event_name", "runtime.startup.ready",
		"mode", "dark", "claims_enabled", false,
		"addr", ln.Addr().String(), "version", version)
	if err := serve(ctx, ln, version, time.Now(), false); err != nil {
		logger.Error("admin server failed", "event_name", "runtime.serve.failed", "error", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
