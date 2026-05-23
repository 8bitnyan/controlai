// Package main is the controlai CLI and daemon entrypoint.
// All CLI subcommands communicate exclusively with the daemon via its REST API;
// no command directly touches SQLite, the file tree, or docker.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"controlai/internal/audit"
	"controlai/internal/daemon"
	"controlai/internal/log"
	migrateyaml "controlai/internal/migrate/yaml"
	"controlai/internal/pki"
	"controlai/internal/recon"
	"controlai/internal/render"
	"controlai/internal/runner"
	"controlai/internal/store/sqlite"
	"controlai/internal/version"
)

// Global flags.
var (
	flagSocket  string
	flagToken   string
	flagDataDir string
	flagLogLevel string
)

func main() {
	root := &cobra.Command{
		Use:   "controlai",
		Short: "controlai — multi-tenant IoT data-pipeline control plane",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			log.Init(flagLogLevel)
		},
	}
	root.PersistentFlags().StringVar(&flagSocket, "socket", "/var/run/controlai.sock", "unix socket path")
	root.PersistentFlags().StringVar(&flagToken, "token", "", "bearer token for remote daemon")
	root.PersistentFlags().StringVar(&flagDataDir, "data-dir", "/var/lib/controlai", "controlai data directory")
	root.PersistentFlags().StringVar(&flagLogLevel, "log-level", "info", "log level (debug|info|warn|error)")

	root.AddCommand(
		versionCmd(),
		daemonCmd(),
		sharedCmd(),
		tenantCmd(),
		siteCmd(),
		capacityCmd(),
		migrateCmd(),
		pkiCmd(),
		tokenCmd(),
		applyCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ─── version ─────────────────────────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("controlai", version.Version)
		},
	}
}

// ─── daemon ──────────────────────────────────────────────────────────────────

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Daemon management",
	}
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the controlai daemon",
		RunE:  runDaemon,
	}
	var tcpAddr, tlsCert, tlsKey string
	var devMode bool
	startCmd.Flags().StringVar(&tcpAddr, "tcp-addr", "", "optional TCP+TLS listen addr (e.g. :8443)")
	startCmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS cert file for TCP listener")
	startCmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS key file for TCP listener")
	startCmd.Flags().BoolVar(&devMode, "dev", false, "dev mode (insecure: no master key required)")
	cmd.AddCommand(startCmd)
	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	devMode, _ := cmd.Flags().GetBool("dev")
	// Validate master key.
	if _, err := pki.MasterKey(devMode); err != nil {
		return fmt.Errorf("startup check: %w", err)
	}

	dbPath := flagDataDir + "/controlai.db"
	store, err := sqlite.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	docker, err := runner.NewDockerClient()
	if err != nil {
		log.Warn("docker client unavailable", "err", err)
	}

	masterKey, _ := pki.MasterKey(devMode) // already validated above; ignore error here

	var lastTick time.Time
	rec := recon.New(recon.Config{
		DataDir:    flagDataDir,
		BasePeriod: 30 * time.Second,
		LastTick:   &lastTick,
		MasterKey:  masterKey,
	}, store, docker, store, log.L())

	tcpAddr, _ := cmd.Flags().GetString("tcp-addr")
	tlsCert, _ := cmd.Flags().GetString("tls-cert")
	tlsKey, _ := cmd.Flags().GetString("tls-key")

	srv := daemon.New(daemon.Config{
		SocketPath:  flagSocket,
		TCPAddr:     tcpAddr,
		TLSCertFile: tlsCert,
		TLSKeyFile:  tlsKey,
		DataDir:     flagDataDir,
		DevMode:     devMode,
		ReconcilerLastTick: &lastTick,
		DockerReachable: func(ctx context.Context) bool {
			if docker == nil {
				return false
			}
			return docker.Ping(ctx) == nil
		},
		DockerListByProject: func(ctx context.Context, projectID string) ([]daemon.ContainerState, error) {
			if docker == nil {
				return nil, fmt.Errorf("docker unavailable")
			}
			cs, err := docker.ListByProject(ctx, projectID)
			if err != nil {
				return nil, err
			}
			out := make([]daemon.ContainerState, len(cs))
			for i, c := range cs {
				out[i] = daemon.ContainerState{Name: c.Name, State: c.State}
			}
			return out, nil
		},
		StartedAt: time.Now(),
	}, store, store, rec, log.L())

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Start reconciler.
	go rec.Run(ctx)

	// Start unix socket server.
	go func() {
		if err := srv.ServeUnix(ctx); err != nil {
			log.Error("unix socket server error", "err", err)
			cancel()
		}
	}()

	// Start optional TCP+TLS server.
	if tcpAddr != "" {
		go func() {
			if err := srv.ServeTCP(ctx); err != nil {
				log.Error("TCP server error", "err", err)
			}
		}()
	}

	log.Info("daemon ready", "version", version.Version, "socket", flagSocket)
	<-ctx.Done()
	log.Info("daemon shutting down")
	return nil
}

// ─── shared ───────────────────────────────────────────────────────────────────

// sharedCmd manages the shared Traefik infrastructure project.
func sharedCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "shared", Short: "Manage shared infrastructure (Traefik)"}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Render and start the shared Traefik container",
		Long: `Renders the shared docker-compose.yml and Traefik static.yml from embedded
templates, writes them to <data-dir>/shared/, then runs 'docker compose up -d'
to start the Traefik container.

The dynamic/ directory is created and Traefik's file provider watches it for
per-site MQTT SNI routes written by the reconciler.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			domain, _ := cmd.Flags().GetString("domain")
			acme, _ := cmd.Flags().GetBool("acme")

			rend := render.New()
			rctx := render.RenderContext{
				Shared: &render.SharedCtx{
					ACMEEnabled: acme,
					Domain:      domain,
				},
			}

			// Render shared compose + Traefik static config.
			sharedDir := flagDataDir + "/shared"
			results, err := rend.RenderShared(rctx)
			if err != nil {
				return fmt.Errorf("render shared: %w", err)
			}
			if err := render.WriteResults(sharedDir, results); err != nil {
				return fmt.Errorf("write shared: %w", err)
			}
			fmt.Printf("Rendered %d shared file(s) to %s\n", len(results), sharedDir)

			// Ensure dynamic/ directory exists for Traefik file provider.
			dynamicDir := sharedDir + "/traefik/dynamic"
			if err := os.MkdirAll(dynamicDir, 0o750); err != nil {
				return fmt.Errorf("create dynamic dir: %w", err)
			}

			// Start the shared Traefik container.
			composeFile := sharedDir + "/docker-compose.yml"
			fmt.Println("Starting shared Traefik container...")
			res, err := runner.Up(context.Background(), "controlai-shared", composeFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "docker compose stderr: %s\n", res.Stderr)
				return fmt.Errorf("start shared: %w", err)
			}
			fmt.Println("Shared Traefik container started.")
			fmt.Println("  :80  → HTTP entrypoint")
			fmt.Println("  :443 → HTTPS entrypoint")
			fmt.Println("  :8883 → MQTT/TLS entrypoint (SNI passthrough)")
			return nil
		},
	}
	initCmd.Flags().String("domain", "", "base domain for Traefik ACME and SNI routing")
	initCmd.Flags().Bool("acme", false, "enable Let's Encrypt ACME cert resolver")

	cmd.AddCommand(initCmd)
	return cmd
}

// ─── tenant commands ──────────────────────────────────────────────────────────

func tenantCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tenant", Short: "Manage tenants"}

	createCmd := &cobra.Command{
		Use:   "create <slug>",
		Short: "Create a new tenant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain, _ := cmd.Flags().GetString("domain")
			retention, _ := cmd.Flags().GetString("retention")
			return apiPost("/v1/tenants", map[string]string{
				"slug": args[0], "domain": domain, "retention": retention,
			})
		},
	}
	createCmd.Flags().String("domain", "", "base domain for SNI routing")
	createCmd.Flags().String("retention", "7d", "data retention (1m|1h|1d|7d|30d)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List tenants",
		RunE: func(cmd *cobra.Command, args []string) error { return apiGet("/v1/tenants") },
	}

	rmCmd := &cobra.Command{
		Use:   "rm <tenant-id>",
		Short: "Remove a tenant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			purge, _ := cmd.Flags().GetBool("purge")
			suffix := ""
			if purge {
				suffix = "?purge=true"
			}
			return apiDelete("/v1/tenants/"+args[0]+suffix)
		},
	}
	rmCmd.Flags().Bool("purge", false, "also delete volumes and on-disk data")

	startCmd := &cobra.Command{
		Use:   "start <tenant-id>",
		Short: "Start a tenant's TSDB",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/v1/apply/"+args[0]+"-tsdb", nil)
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop <tenant-id>",
		Short: "Stop a tenant's TSDB",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/v1/apply/"+args[0]+"-tsdb-stop", nil)
		},
	}

	cmd.AddCommand(createCmd, listCmd, rmCmd, startCmd, stopCmd)
	return cmd
}

// ─── site commands ────────────────────────────────────────────────────────────

func siteCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "site", Short: "Manage sites"}

	createCmd := &cobra.Command{
		Use:   "create <tenant-id> <slug>",
		Short: "Create a new site under a tenant",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			broker, _ := cmd.Flags().GetString("broker")
			throughput, _ := cmd.Flags().GetString("throughput")
			direction, _ := cmd.Flags().GetString("direction")
			codec, _ := cmd.Flags().GetString("codec")
			return apiPost("/v1/tenants/"+args[0]+"/sites", map[string]string{
				"slug": args[1], "broker_kind": broker,
				"throughput": throughput, "direction": direction, "payload_codec": codec,
			})
		},
	}
	createCmd.Flags().String("broker", "mosquitto", "broker kind (mosquitto|emqx)")
	createCmd.Flags().String("throughput", "low", "throughput tier (low|mid)")
	createCmd.Flags().String("direction", "uni", "ingest direction (uni|bi)")
	createCmd.Flags().String("codec", "cbor", "payload codec (cbor|json|raw_passthrough)")

	listCmd := &cobra.Command{
		Use:   "list <tenant-id>",
		Short: "List sites for a tenant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/v1/tenants/" + args[0] + "/sites")
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop <tenant-id> <site-id>",
		Short: "Stop a site",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/v1/apply/"+args[0]+"-"+args[1]+"-stop", nil)
		},
	}

	startCmd := &cobra.Command{
		Use:   "start <tenant-id> <site-id>",
		Short: "Start a site",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiPost("/v1/apply/"+args[0]+"-"+args[1], nil)
		},
	}

	rmCmd := &cobra.Command{
		Use:   "rm <tenant-id> <site-id>",
		Short: "Remove a site",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			purge, _ := cmd.Flags().GetBool("purge")
			suffix := ""
			if purge {
				suffix = "?purge=true"
			}
			return apiDelete("/v1/tenants/" + args[0] + "/sites/" + args[1] + suffix)
		},
	}
	rmCmd.Flags().Bool("purge", false, "also delete volumes and on-disk data")

	applyCmd := &cobra.Command{
		Use:   "apply <tenant-id> <site-id>",
		Short: "Apply desired state for a site (blocking, waits up to timeout for convergence)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			timeout, _ := cmd.Flags().GetInt("timeout")
			path := fmt.Sprintf("/v1/apply/%s-%s?wait=%d", args[0], args[1], timeout)
			return apiPost(path, nil)
		},
	}
	applyCmd.Flags().Int("timeout", 120, "convergence wait timeout in seconds")

	cmd.AddCommand(createCmd, listCmd, stopCmd, startCmd, rmCmd, applyCmd)
	return cmd
}

// ─── capacity ────────────────────────────────────────────────────────────────

func capacityCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capacity",
		Short: "Show current and predicted capacity",
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiGet("/v1/capacity")
		},
	}
}

// ─── migrate ─────────────────────────────────────────────────────────────────

func migrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate YAML config files to the latest schema version",
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			dataDir, _ := cmd.Flags().GetString("data-dir")
			root := dataDir + "/tenants"
			migrated, err := migrateyaml.WalkAndMigrate(root, version.MaxSupportedYAMLSchemaVersion, dryRun)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Printf("Would migrate %d file(s):\n", len(migrated))
			} else {
				fmt.Printf("Migrated %d file(s):\n", len(migrated))
			}
			for _, f := range migrated {
				fmt.Println(" ", f)
			}
			return nil
		},
	}
	cmd.Flags().Bool("dry-run", false, "show what would be migrated without modifying files")
	return cmd
}

// ─── pki ─────────────────────────────────────────────────────────────────────

func pkiCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pki", Short: "PKI management"}

	issueCmd := &cobra.Command{
		Use:   "cert issue --site <tenant>/<site> --gateway <name>",
		Short: "Issue a leaf certificate for a gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			// This operation requires daemon access.
			return fmt.Errorf("pki cert issue: daemon-backed issuance not yet implemented in CLI; use the REST API")
		},
	}
	issueCmd.Flags().String("site", "", "site identifier (tenant-id/site-id)")
	issueCmd.Flags().String("gateway", "", "gateway name (slug)")

	revokeCmd := &cobra.Command{
		Use:   "cert revoke <fingerprint>",
		Short: "Revoke a certificate by fingerprint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("pki cert revoke: not yet implemented in CLI; use the REST API")
		},
	}

	caCmd := &cobra.Command{
		Use:   "ca create --site <tenant>/<site>",
		Short: "Manually create a CA for a site (normally done automatically)",
		RunE: func(cmd *cobra.Command, args []string) error {
			masterKey, err := pki.MasterKey(false)
			if err != nil {
				return err
			}
			siteFlag, _ := cmd.Flags().GetString("site")
			if siteFlag == "" {
				return fmt.Errorf("--site is required")
			}
			ca, err := pki.GenerateCA("manual", siteFlag, masterKey)
			if err != nil {
				return err
			}
			fmt.Printf("CA generated for site %s\nFingerprint: %s\n", siteFlag, ca.Fingerprint)
			return nil
		},
	}
	caCmd.Flags().String("site", "", "site identifier")

	cert := &cobra.Command{Use: "cert", Short: "Certificate operations"}
	cert.AddCommand(issueCmd, revokeCmd)
	cmd.AddCommand(caCmd, cert)
	return cmd
}

// ─── token ───────────────────────────────────────────────────────────────────

func tokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage bearer tokens for remote access"}

	createCmd := &cobra.Command{
		Use:   "create <display-name>",
		Short: "Create a new bearer token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := flagDataDir + "/controlai.db"
			store, err := sqlite.Open(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			raw, row, err := store.CreateToken(context.Background(), args[0])
			if err != nil {
				return err
			}
			_ = store.Emit(context.Background(), audit.Event{Kind: audit.KindTokenCreate, Success: true})
			fmt.Printf("Token ID:    %s\nPrefix:      %s...\nRaw token:   %s\n", row.ID, row.Prefix, raw)
			fmt.Println("(Save the raw token — it will not be shown again.)")
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List active tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := flagDataDir + "/controlai.db"
			store, err := sqlite.Open(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			rows, err := store.ListTokens(context.Background())
			if err != nil {
				return err
			}
			for _, r := range rows {
				fmt.Printf("%-36s  %-20s  %s...\n", r.ID, r.DisplayName, r.Prefix)
			}
			return nil
		},
	}

	revokeCmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke a token by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := flagDataDir + "/controlai.db"
			store, err := sqlite.Open(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.RevokeToken(context.Background(), args[0]); err != nil {
				return err
			}
			_ = store.Emit(context.Background(), audit.Event{Kind: audit.KindTokenRevoke, Success: true})
			fmt.Printf("Token %s revoked.\n", args[0])
			return nil
		},
	}

	cmd.AddCommand(createCmd, listCmd, revokeCmd)
	return cmd
}

// ─── apply ───────────────────────────────────────────────────────────────────

func applyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <selector>",
		Short: "Apply desired state for a project (blocking, waits for convergence)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			timeout, _ := cmd.Flags().GetInt("timeout")
			path := fmt.Sprintf("/v1/apply/%s?wait=%d", args[0], timeout)
			return apiPost(path, nil)
		},
	}
	cmd.Flags().Int("timeout", 120, "convergence wait timeout in seconds")
	return cmd
}

// ─── API client helpers ───────────────────────────────────────────────────────

func apiClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", flagSocket)
			},
		},
		Timeout: 60 * time.Second,
	}
}

func apiGet(path string) error {
	resp, err := apiClient().Get("http://controlai" + path)
	if err != nil {
		return fmt.Errorf("daemon unavailable: %w (is `controlai daemon start` running?)", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		fmt.Println(pretty.String())
	} else {
		fmt.Println(string(body))
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func apiPost(path string, body any) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	resp, err := apiClient().Post("http://controlai"+path, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("daemon unavailable: %w (is `controlai daemon start` running?)", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var pretty bytes.Buffer
	if json.Indent(&pretty, b, "", "  ") == nil {
		fmt.Println(pretty.String())
	} else {
		fmt.Println(string(b))
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func apiDelete(path string) error {
	req, err := http.NewRequest(http.MethodDelete, "http://controlai"+path, nil)
	if err != nil {
		return err
	}
	resp, err := apiClient().Do(req)
	if err != nil {
		return fmt.Errorf("daemon unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	fmt.Println("ok")
	return nil
}
