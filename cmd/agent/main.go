// Command agent collects host metrics and reports them to the Kerge panel.
//
// Usage:
//
//	kerge-agent [--config path] [--state-dir path]
//	kerge-agent --version
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kergeio/kerge-agent/internal/collect"
	"github.com/kergeio/kerge-agent/internal/config"
	"github.com/kergeio/kerge-agent/internal/transport"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// defaultStateDir is where the long-lived credential lives when systemd
// does not provide a state directory.
const defaultStateDir = "/var/lib/kerge-agent"

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "kerge-agent:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("kerge-agent", flag.ContinueOnError)
	configPath := fs.String("config", config.DefaultPath, "path to agent.conf")
	stateDir := fs.String("state-dir", "", "state directory (default $STATE_DIRECTORY, else "+defaultStateDir+")")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	credentialPath := filepath.Join(stateDirectory(*stateDir, getenv), "credential")

	collector := collect.New(collect.Options{
		Interval:     cfg.Interval,
		IfaceExclude: cfg.IfaceExclude,
		Version:      version,
		Logger:       logger,
	})
	client, err := transport.New(transport.Options{
		Server:         cfg.Server,
		Token:          cfg.Token,
		CredentialPath: credentialPath,
		Logger:         logger,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting", "version", version, "server", cfg.Server, "interval", cfg.Interval)
	go collector.Run(ctx, client.Send)
	client.Run(ctx, collector.HostInfo)
	logger.Info("stopped")
	return nil
}

// stateDirectory resolves the state directory. systemd sets
// $STATE_DIRECTORY from StateDirectory= in the unit, and may list several
// paths separated by colons, of which the first is ours.
func stateDirectory(flagValue string, getenv func(string) string) string {
	if flagValue != "" {
		return flagValue
	}
	if dirs := getenv("STATE_DIRECTORY"); dirs != "" {
		if first, _, found := strings.Cut(dirs, ":"); found {
			return first
		}
		return dirs
	}
	return defaultStateDir
}
