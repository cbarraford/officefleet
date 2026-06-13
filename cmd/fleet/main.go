package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/cbarraford/office-fleet/internal/api"
	"github.com/cbarraford/office-fleet/internal/auth"
	"github.com/cbarraford/office-fleet/internal/avatar"
	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/db"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/events"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/repo"
	"github.com/cbarraford/office-fleet/internal/run"
	"github.com/cbarraford/office-fleet/internal/secrets"
	"github.com/cbarraford/office-fleet/internal/seed"
	"github.com/cbarraford/office-fleet/internal/server"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/cbarraford/office-fleet/internal/trigger"
	"github.com/cbarraford/office-fleet/internal/web"

	// Register all plugins via init().
	_ "github.com/cbarraford/office-fleet/internal/plugins/discord"
	_ "github.com/cbarraford/office-fleet/internal/plugins/email"
	_ "github.com/cbarraford/office-fleet/internal/plugins/github"
	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
	_ "github.com/cbarraford/office-fleet/internal/plugins/slack"
)

var (
	flagConfig string
	flagDB     string
)

func main() {
	root := &cobra.Command{
		Use:   "fleet",
		Short: "OfficeFleet agent runner",
	}

	root.PersistentFlags().StringVar(&flagConfig, "config", "fleet.yaml", "path to fleet.yaml config file")
	root.PersistentFlags().StringVar(&flagDB, "db", "", "override database DSN")

	root.AddCommand(migrateCmd())
	root.AddCommand(configCmd())
	root.AddCommand(backendsCmd())
	root.AddCommand(agentsCmd())
	root.AddCommand(dutiesCmd())
	root.AddCommand(assignmentsCmd())
	root.AddCommand(runCmd())
	root.AddCommand(scheduleCmd())
	root.AddCommand(serveCmd())
	root.AddCommand(eventsCmd())
	root.AddCommand(seedCmd())
	root.AddCommand(secretsCmd())
	root.AddCommand(usersCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// loadConfig loads fleet.yaml from the --config flag path.
func loadConfig() (*config.Config, error) {
	return config.Load(flagConfig)
}

// loadValidatedConfig loads fleet.yaml and rejects it if validation fails.
// Execution paths (run, schedule, backends test) must not act on an invalid
// config; read-only listing commands stay lenient.
func loadValidatedConfig() (*config.Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if errs := config.Validate(cfg); len(errs) > 0 {
		return nil, fmt.Errorf("invalid config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// resolveDSN returns the effective DSN: --db flag > config DSN > FLEET_DATABASE_DSN env.
func resolveDSN(cfg *config.Config) string {
	if flagDB != "" {
		return flagDB
	}
	if cfg != nil && cfg.Database.DSN != "" {
		return cfg.Database.DSN
	}
	return os.Getenv("FLEET_DATABASE_DSN")
}

// mustDSN returns the DSN or an error when none is configured.
func mustDSN(cfg *config.Config) (string, error) {
	dsn := resolveDSN(cfg)
	if dsn == "" {
		return "", fmt.Errorf("no database DSN configured (set --db, database.dsn in fleet.yaml, or FLEET_DATABASE_DSN env)")
	}
	return dsn, nil
}

// loadCipher returns the secrets cipher, or nil when FLEET_MASTER_KEY is unset
// (legacy-plaintext-only mode). An invalid key is a hard error.
func loadCipher() (*secrets.Cipher, error) {
	keyB64 := os.Getenv(secrets.MasterKeyEnv)
	if keyB64 == "" {
		return nil, nil
	}
	return secrets.NewCipher(keyB64)
}

// decryptSecret resolves a stored value: FSEC1 → decrypt (cipher required);
// legacy plaintext → returned as-is.
func decryptSecret(c *secrets.Cipher, name string, stored []byte) (string, error) {
	if !secrets.IsEncrypted(stored) {
		return string(stored), nil
	}
	if c == nil {
		return "", fmt.Errorf("secret %q is encrypted but %s is not set", name, secrets.MasterKeyEnv)
	}
	plain, err := c.Decrypt(stored)
	if err != nil {
		return "", fmt.Errorf("secret %q: %w", name, err)
	}
	return string(plain), nil
}

// buildSecretLookup returns a SecretLookup that queries the secrets table and
// decrypts FSEC1 values transparently. Missing secrets return ("", nil) so
// --fake runs remain usable without seeded secrets.
func buildSecretLookup(ctx context.Context, pool *pgxpool.Pool, cipher *secrets.Cipher) plugin.SecretLookup {
	return func(name string) (string, error) {
		var val []byte
		err := pool.QueryRow(ctx, "SELECT encrypted_value FROM secrets WHERE name=$1", name).Scan(&val)
		if err != nil {
			return "", nil
		}
		return decryptSecret(cipher, name, val)
	}
}

// dbSecretsProvider implements run.SecretsProvider by loading all secrets from
// the DB and decrypting FSEC1 values transparently. A single undecryptable
// secret fails the entire Load (blast radius: all runs), which is deliberate
// fail-loud behaviour per the SP4a spec — a corrupt or missing key must not
// silently render templates with empty values.
type dbSecretsProvider struct {
	pool   *pgxpool.Pool
	cipher *secrets.Cipher
}

func (d *dbSecretsProvider) Load(ctx context.Context) (map[string]string, error) {
	rows, err := d.pool.Query(ctx, "SELECT name, encrypted_value FROM secrets")
	if err != nil {
		return nil, fmt.Errorf("query secrets: %w", err)
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var name string
		var val []byte
		if err := rows.Scan(&name, &val); err != nil {
			return nil, fmt.Errorf("scan secret row: %w", err)
		}
		plain, err := decryptSecret(d.cipher, name, val)
		if err != nil {
			return nil, fmt.Errorf("decrypt secret %q: %w", name, err)
		}
		m[name] = plain
	}
	return m, rows.Err()
}

// initPlugins initialises all registered plugins using config and DB secrets.
func initPlugins(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, cipher *secrets.Cipher) {
	secretLookup := buildSecretLookup(ctx, pool, cipher)
	for _, p := range plugin.All() {
		var pluginCfg map[string]any
		for _, pc := range cfg.Plugins {
			if pc.Name == p.Name() {
				pluginCfg = pc.Config
				break
			}
		}
		if pluginCfg == nil {
			pluginCfg = map[string]any{}
		}
		if err := p.Init(ctx, pluginCfg, secretLookup); err != nil {
			fmt.Fprintf(os.Stderr, "warning: plugin %q init failed: %v\n", p.Name(), err)
		}
	}
}

// migrateCmd runs DB migrations.
func migrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig() // config is optional; DSN may come from env
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured (set --db, database.dsn in fleet.yaml, or FLEET_DATABASE_DSN env)")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			if err := db.Migrate(ctx, pool); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			if cfg != nil {
				agentRepo := repo.NewAgentRepo(pool)
				dutyRepo := repo.NewDutyRepo(pool)
				assignmentRepo := repo.NewAssignmentRepo(pool)
				if err := seed.FromConfig(ctx, cfg, agentRepo, dutyRepo, assignmentRepo, false); err != nil {
					return fmt.Errorf("seed: %w", err)
				}
			}
			fmt.Println("schema migrated and config seeded")
			return nil
		},
	}
}

// configCmd returns the "config" group of subcommands.
func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Config management commands",
	}
	cmd.AddCommand(configValidateCmd())
	return cmd
}

func configValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate fleet.yaml and print errors or OK",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			errs := config.Validate(cfg)
			if len(errs) == 0 {
				fmt.Println("OK")
				return nil
			}
			for _, e := range errs {
				fmt.Fprintln(os.Stderr, "error:", e)
			}
			return fmt.Errorf("%d validation error(s)", len(errs))
		},
	}
}

// backendsCmd returns the "backends" group of subcommands.
func backendsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backends",
		Short: "Backend management commands",
	}
	cmd.AddCommand(backendsListCmd())
	cmd.AddCommand(backendsLoginCmd())
	cmd.AddCommand(backendsTestCmd())
	return cmd
}

func backendsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured backends from fleet.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if len(cfg.Backends) == 0 {
				fmt.Println("(no backends configured)")
				return nil
			}
			fmt.Printf("%-20s %-20s %-12s %-10s\n", "NAME", "KIND", "AUTH", "EFFORT")
			fmt.Println(strings.Repeat("-", 64))
			for _, b := range cfg.Backends {
				effort := b.DefaultEffort
				if effort == "" {
					effort = "-"
				}
				fmt.Printf("%-20s %-20s %-12s %-10s\n", b.Name, b.Kind, b.Auth.Mode, effort)
			}
			return nil
		},
	}
}

func backendsLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login <backend-name>",
		Short: "Log in to a CLI backend (claude/codex/gemini)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backendName := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			var backend *config.Backend
			for i := range cfg.Backends {
				if cfg.Backends[i].Name == backendName {
					b := cfg.Backends[i]
					backend = &b
					break
				}
			}
			if backend == nil {
				return fmt.Errorf("backend %q not found in config", backendName)
			}
			if backend.Auth.Mode != "subscription" {
				return fmt.Errorf("backend %q does not use subscription auth; login is only supported for subscription backends", backendName)
			}
			c := exec.Command(backend.Kind, "login")
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
}

func backendsTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <backend-name>",
		Short: "One-shot connectivity/auth smoke test against a backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backendName := args[0]
			cfg, err := loadValidatedConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			var backend *config.Backend
			for i := range cfg.Backends {
				if cfg.Backends[i].Name == backendName {
					backend = &cfg.Backends[i]
					break
				}
			}
			if backend == nil {
				return fmt.Errorf("backend %q not found in config", backendName)
			}
			ex, err := executor.FromBackend(cfg, backend)
			if err != nil {
				return fmt.Errorf("build executor: %w", err)
			}
			ws, err := os.MkdirTemp("", "fleet-backend-test-*")
			if err != nil {
				return fmt.Errorf("create workspace: %w", err)
			}
			defer os.RemoveAll(ws)

			fmt.Printf("testing backend %q (kind %s)...\n", backend.Name, backend.Kind)
			result, err := ex.Run(cmd.Context(), executor.LLMRequest{
				Prompt:    "Reply with only the word: OK",
				Workspace: ws,
				Model:     backend.Model,
				Effort:    backend.DefaultEffort,
			})
			if err != nil {
				return fmt.Errorf("backend test failed: %w", err)
			}
			if result.Status != 0 {
				return fmt.Errorf("backend %q returned non-zero status %d: %s", backendName, result.Status, result.Summary)
			}
			fmt.Printf("Status:  %d\n", result.Status)
			fmt.Printf("Summary: %s\n", result.Summary)
			fmt.Printf("Tokens:  %d\n", result.Tokens)
			return nil
		},
	}
}

// agentsCmd returns the "agents" group of subcommands.
func agentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Agent management commands",
	}
	cmd.AddCommand(agentsListCmd())
	return cmd
}

func agentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List agents from DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			agents, err := repo.NewAgentRepo(pool).List(ctx)
			if err != nil {
				return fmt.Errorf("list agents: %w", err)
			}
			if len(agents) == 0 {
				fmt.Println("(no agents)")
				return nil
			}
			fmt.Printf("%-36s %-20s %-15s %-10s\n", "ID", "NAME", "ROLE", "ENABLED")
			fmt.Println(strings.Repeat("-", 85))
			for _, a := range agents {
				fmt.Printf("%-36s %-20s %-15s %-10v\n", a.ID, a.Name, a.Role, a.Enabled)
			}
			return nil
		},
	}
}

// dutiesCmd returns the "duties" group of subcommands.
func dutiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "duties",
		Short: "Duty management commands",
	}
	cmd.AddCommand(dutiesListCmd())
	return cmd
}

func dutiesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List duties from DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			duties, err := repo.NewDutyRepo(pool).List(ctx)
			if err != nil {
				return fmt.Errorf("list duties: %w", err)
			}
			if len(duties) == 0 {
				fmt.Println("(no duties)")
				return nil
			}
			fmt.Printf("%-36s %-20s %-15s\n", "ID", "NAME", "ROLE")
			fmt.Println(strings.Repeat("-", 73))
			for _, d := range duties {
				fmt.Printf("%-36s %-20s %-15s\n", d.ID, d.Name, d.Role)
			}
			return nil
		},
	}
}

// assignmentsCmd returns the "assignments" group of subcommands.
func assignmentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assignments",
		Short: "Assignment management commands",
	}
	cmd.AddCommand(assignmentsListCmd())
	return cmd
}

func assignmentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List assignments from DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			assignments, err := repo.NewAssignmentRepo(pool).List(ctx)
			if err != nil {
				return fmt.Errorf("list assignments: %w", err)
			}
			if len(assignments) == 0 {
				fmt.Println("(no assignments)")
				return nil
			}
			fmt.Printf("%-36s %-36s %-36s %-10s %-10s\n", "ID", "AGENT_ID", "DUTY_ID", "ENABLED", "TRIGGER")
			fmt.Println(strings.Repeat("-", 132))
			for _, a := range assignments {
				fmt.Printf("%-36s %-36s %-36s %-10v %-10s\n", a.ID, a.AgentID, a.DutyID, a.Enabled, a.Trigger.Kind)
			}
			return nil
		},
	}
}

// runCmd returns the "run" subcommand.
func runCmd() *cobra.Command {
	var (
		flagID     string
		flagAgent  string
		flagDuty   string
		flagParams []string
		flagFake   bool
	)

	cmd := &cobra.Command{
		Use:          "run [assignment-id]",
		Short:        "Execute an assignment",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			if flagID == "" && len(args) > 0 {
				flagID = args[0]
			}

			cfg, err := loadValidatedConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			agentRepo := repo.NewAgentRepo(pool)
			dutyRepo := repo.NewDutyRepo(pool)
			assignmentRepo := repo.NewAssignmentRepo(pool)
			runRepo := repo.NewRunRepo(pool)

			// Resolve assignment, agent, duty.
			var assignment *domain.Assignment
			var agent *domain.Agent
			var duty *domain.Duty

			if flagID != "" {
				id, err := uuid.Parse(flagID)
				if err != nil {
					return fmt.Errorf("invalid assignment id %q: %w", flagID, err)
				}
				assignment, err = assignmentRepo.GetByID(ctx, id)
				if err != nil {
					return fmt.Errorf("get assignment: %w", err)
				}
				allAgents, err := agentRepo.List(ctx)
				if err != nil {
					return fmt.Errorf("list agents: %w", err)
				}
				for _, a := range allAgents {
					if a.ID == assignment.AgentID {
						agent = a
						break
					}
				}
				if agent == nil {
					return fmt.Errorf("agent %s not found", assignment.AgentID)
				}
				allDuties, err := dutyRepo.List(ctx)
				if err != nil {
					return fmt.Errorf("list duties: %w", err)
				}
				for _, d := range allDuties {
					if d.ID == assignment.DutyID {
						duty = d
						break
					}
				}
				if duty == nil {
					return fmt.Errorf("duty %s not found", assignment.DutyID)
				}
			} else if flagAgent != "" && flagDuty != "" {
				agent, err = agentRepo.GetByName(ctx, flagAgent)
				if err != nil {
					return fmt.Errorf("get agent %q: %w", flagAgent, err)
				}
				duty, err = dutyRepo.GetByName(ctx, flagDuty)
				if err != nil {
					return fmt.Errorf("get duty %q: %w", flagDuty, err)
				}
				assignment, err = assignmentRepo.GetByAgentAndDuty(ctx, agent.ID, duty.ID)
				if err != nil {
					return fmt.Errorf("get assignment for agent=%q duty=%q: %w", flagAgent, flagDuty, err)
				}
			} else {
				return fmt.Errorf("must provide --id or both --agent and --duty")
			}

			// Parse --param key=value flags.
			eventParams := make(map[string]any)
			for _, p := range flagParams {
				parts := strings.SplitN(p, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid param %q: must be key=value", p)
				}
				eventParams[parts[0]] = parts[1]
			}

			// Initialize plugins.
			cipher, err := loadCipher()
			if err != nil {
				return err
			}
			initPlugins(ctx, cfg, pool, cipher)

			// Resolve executor.
			var exec executor.Executor
			if flagFake {
				exec = executor.NewFakeExecutor(domain.LLMResult{
					Summary:    "fake execution result",
					Output:     map[string]any{"raw": "fake output"},
					Transcript: "fake transcript",
				})
			} else {
				var resolved *config.Backend
				for _, ac := range cfg.Assignments {
					if ac.Agent == agent.Name && ac.Duty == duty.Name {
						b, _, berr := config.ResolveBackend(cfg, ac)
						if berr == nil {
							resolved = b
						}
						break
					}
				}
				if resolved == nil {
					// No matching config assignment (e.g. DB-seeded): keep
					// the SP1 default of the subscription claude CLI.
					exec = executor.NewClaudeExecutor("")
				} else {
					var eerr error
					exec, eerr = executor.FromBackend(cfg, resolved)
					if eerr != nil {
						return fmt.Errorf("build executor: %w", eerr)
					}
				}
			}

			store := state.NewPostgresStore(pool)
			pipeline := run.NewPipeline(cfg, runRepo, store, &dbSecretsProvider{pool: pool, cipher: cipher})

			result, execErr := pipeline.Execute(ctx, run.ExecuteRequest{
				Assignment:  assignment,
				Agent:       agent,
				Duty:        duty,
				TriggerKind: "manual",
				EventParams: eventParams,
				Executor:    exec,
			})
			if result != nil {
				fmt.Printf("Run ID:   %s\n", result.ID)
				fmt.Printf("Status:   %s\n", result.Status)
				if result.Error != nil {
					fmt.Printf("Error:    %s\n", *result.Error)
				}
				if result.LLMResult != nil {
					fmt.Printf("Summary:  %s\n", result.LLMResult.Summary)
					fmt.Printf("Tokens:   %d\n", result.LLMResult.Tokens)
					fmt.Printf("Cost:     $%.6f\n", result.LLMResult.Cost)
				}
				if len(result.OutputsDelivered) > 0 {
					fmt.Println("Outputs:")
					for _, od := range result.OutputsDelivered {
						fmt.Printf("  %s/%s: %s\n", od.Plugin, od.Action, od.Status)
						if od.Error != "" {
							fmt.Printf("    error: %s\n", od.Error)
						}
					}
				}
			}
			if execErr != nil {
				return fmt.Errorf("execute: %w", execErr)
			}
			if result.Status == domain.RunStatusFailed {
				return fmt.Errorf("run %s failed", result.ID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&flagID, "id", "", "assignment UUID")
	cmd.Flags().StringVar(&flagAgent, "agent", "", "agent name")
	cmd.Flags().StringVar(&flagDuty, "duty", "", "duty name")
	cmd.Flags().StringArrayVar(&flagParams, "param", nil, "event param as key=value (repeatable)")
	cmd.Flags().BoolVar(&flagFake, "fake", false, "use FakeExecutor instead of ClaudeExecutor")

	return cmd
}

// scheduleCmd returns the "schedule" daemon subcommand (cron only).
// Deprecated in favor of fleet serve, which also hosts webhooks, polling,
// and the event dispatcher.
func scheduleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schedule",
		Short: "Run the cron scheduler daemon (deprecated: use fleet serve)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			cfg, err := loadValidatedConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			cipher, err := loadCipher()
			if err != nil {
				return err
			}
			initPlugins(ctx, cfg, pool, cipher)
			inv, _ := buildInvoker(cfg, pool, cipher)
			return runSchedulerLoop(ctx, pool, inv)
		},
	}
}

const (
	schedulerOrphanedRunAge   = 15 * time.Minute
	schedulerShutdownGrace    = 30 * time.Second
	schedulerReconcileTimeout = 10 * time.Second
	schedulerRestartError     = "orphaned by restart"
	schedulerInterruptedError = "interrupted by shutdown"
)

type schedulerRunReconciler interface {
	ReconcileStaleRunning(ctx context.Context, cutoff time.Time, reason string) (int64, error)
}

func reconcileSchedulerStartupRuns(ctx context.Context, runs schedulerRunReconciler, now time.Time) error {
	n, err := runs.ReconcileStaleRunning(ctx, now.Add(-schedulerOrphanedRunAge), schedulerRestartError)
	if err != nil {
		return fmt.Errorf("reconcile orphaned runs: %w", err)
	}
	if n > 0 {
		fmt.Printf("scheduler: marked %d orphaned running run(s) failed\n", n)
	}
	return nil
}

func reconcileSchedulerShutdownRuns(ctx context.Context, runs schedulerRunReconciler, shutdownStarted time.Time) error {
	n, err := runs.ReconcileStaleRunning(ctx, shutdownStarted, schedulerInterruptedError)
	if err != nil {
		return fmt.Errorf("reconcile interrupted runs: %w", err)
	}
	if n > 0 {
		fmt.Printf("scheduler: marked %d interrupted running run(s) failed\n", n)
	}
	return nil
}

// buildInvoker wires the shared assignment-execution path.
// It returns both the Invoker and the underlying Pipeline so callers that need
// to attach lifecycle hooks (e.g. serveCmd) can call SetRunUpdateHook.
func buildInvoker(cfg *config.Config, pool *pgxpool.Pool, cipher *secrets.Cipher) (*run.Invoker, *run.Pipeline) {
	pipeline := run.NewPipeline(cfg, repo.NewRunRepo(pool), state.NewPostgresStore(pool), &dbSecretsProvider{pool: pool, cipher: cipher})
	inv := run.NewInvoker(cfg, pipeline,
		repo.NewAssignmentRepo(pool), repo.NewAgentRepo(pool), repo.NewDutyRepo(pool))
	return inv, pipeline
}

// runSchedulerLoop blocks running cron-triggered assignments until ctx is done.
func runSchedulerLoop(ctx context.Context, pool *pgxpool.Pool, inv *run.Invoker) error {
	runRepo := repo.NewRunRepo(pool)
	if err := reconcileSchedulerStartupRuns(ctx, runRepo, time.Now()); err != nil {
		return err
	}

	assignments, err := repo.NewAssignmentRepo(pool).List(ctx)
	if err != nil {
		return fmt.Errorf("list assignments: %w", err)
	}
	sched := trigger.NewScheduler()
	sched.SetShutdownGrace(schedulerShutdownGrace)
	for _, a := range assignments {
		if !a.Enabled || a.Trigger.Kind != "cron" {
			continue
		}
		t := trigger.NewCron(a.Trigger.Schedule)
		if err := sched.Add(a.ID.String(), t, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping assignment %s: bad cron schedule: %v\n", a.ID, err)
			continue
		}
		fmt.Printf("scheduled assignment %s (schedule: %s)\n", a.ID, a.Trigger.Schedule)
	}
	fmt.Println("scheduler running...")
	sched.Run(ctx, func(runCtx context.Context, assignmentID string) {
		id, err := uuid.Parse(assignmentID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scheduler: invalid assignment id %s: %v\n", assignmentID, err)
			return
		}
		result, err := inv.Invoke(runCtx, id, "cron", nil, map[string]any{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "scheduler: execute assignment %s: %v\n", assignmentID, err)
			return
		}
		if result.Error != nil {
			fmt.Printf("scheduler: assignment %s completed with status %s (error: %s)\n", assignmentID, result.Status, *result.Error)
		} else {
			fmt.Printf("scheduler: assignment %s completed with status %s\n", assignmentID, result.Status)
		}
	})
	if ctx.Err() != nil {
		reconcileCtx, cancel := context.WithTimeout(context.Background(), schedulerReconcileTimeout)
		defer cancel()
		if err := reconcileSchedulerShutdownRuns(reconcileCtx, runRepo, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// serveCmd returns the "serve" daemon: webhooks, polling, dispatcher, cron.
func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the OfficeFleet daemon (webhooks, polling, event dispatch, cron)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			cfg, err := loadValidatedConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			cipher, err := loadCipher()
			if err != nil {
				return err
			}
			initPlugins(ctx, cfg, pool, cipher)

			// Warn about unencrypted secrets at startup.
			if allSecrets, listErr := repo.NewSecretRepo(pool).List(ctx); listErr == nil {
				var unenc []string
				for name, val := range allSecrets {
					if !secrets.IsEncrypted(val) {
						unenc = append(unenc, name)
					}
				}
				if len(unenc) > 0 {
					if cipher == nil {
						fmt.Fprintf(os.Stderr, "warning: %s is not set; %d secret(s) are stored as plaintext: %s\n",
							secrets.MasterKeyEnv, len(unenc), strings.Join(unenc, ", "))
					} else {
						fmt.Fprintf(os.Stderr, "warning: %d secret(s) are stored as plaintext (run 'fleet secrets encrypt-existing' to migrate): %s\n",
							len(unenc), strings.Join(unenc, ", "))
					}
				}
			} else {
				fmt.Fprintf(os.Stderr, "warning: could not check secret encryption status: %v\n", listErr)
			}

			inv, pipeline := buildInvoker(cfg, pool, cipher)
			eventRepo := repo.NewEventRepo(pool)
			cursorRepo := repo.NewCursorRepo(pool)

			// Avatars (SP4c): store + optional image generator + async service.
			avatarsDir := cfg.Serve.AvatarsDir
			if avatarsDir == "" {
				avatarsDir = "./avatars"
			}
			avatarStore, err := avatar.NewStore(avatarsDir)
			if err != nil {
				return fmt.Errorf("avatars: %w", err)
			}
			var avatarGen avatar.Generator
			if cfg.Serve.AvatarBackend != "" {
				var ib *config.ImageBackend
				for i := range cfg.ImageBackends {
					if cfg.ImageBackends[i].Name == cfg.Serve.AvatarBackend {
						ib = &cfg.ImageBackends[i]
						break
					}
				}
				if ib == nil {
					// unreachable: loadValidatedConfig enforces the reference
					return fmt.Errorf("avatar_backend %q not defined", cfg.Serve.AvatarBackend)
				}
				promptText := cfg.Serve.AvatarPrompt
				if promptText == "" {
					promptText = avatar.DefaultPrompt
				}
				promptTmpl, perr := template.New("avatar").Parse(promptText)
				if perr != nil {
					return fmt.Errorf("avatar_prompt: %w", perr) // unreachable: validated at load
				}
				avatarGen = avatar.NewOpenAIImageGenerator(ib.BaseURI, ib.Model, ib.Auth.APIKey, ib.Size, promptTmpl)
				fmt.Printf("avatar generation via image backend %q\n", ib.Name)
			}
			avatarSvc := avatar.NewService(avatarGen, avatarStore, repo.NewAgentRepo(pool), nil)

			addr := cfg.Serve.Addr
			if addr == "" {
				addr = ":8080"
			}
			rescan := 30 * time.Second
			if cfg.Serve.RescanInterval != "" {
				rescan, _ = time.ParseDuration(cfg.Serve.RescanInterval) // validated at load
			}

			dispatcher := events.NewDispatcher(eventRepo, repo.NewAssignmentRepo(pool), inv, cfg.Serve.Workers, rescan)
			ingestor := events.NewIngestor(eventRepo, dispatcher.Notify)
			go dispatcher.Run(ctx)

			// Poll loops: one per plugin that implements PollSource.
			for _, p := range plugin.All() {
				src, ok := p.(plugin.PollSource)
				if !ok {
					continue
				}
				interval := pollInterval(cfg, p.Name())
				fmt.Printf("polling %s every %s\n", p.Name(), interval)
				go events.RunPoller(ctx, p.Name(), src, interval, cursorRepo, ingestor.Ingest,
					func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
			}

			// Wire the REST API. Use a typed-nil-safe pattern for the Encryptor:
			// assigning cipher (*secrets.Cipher) directly to api.Encryptor would
			// produce a non-nil interface wrapping a nil pointer, defeating nil checks.
			var enc api.Encryptor
			if cipher != nil {
				enc = cipher
			}
			apiSrv := api.New(api.Deps{
				Agents:        repo.NewAgentRepo(pool),
				Assignments:   repo.NewAssignmentRepo(pool),
				Avatars:       avatarSvc,
				Duties:        repo.NewDutyRepo(pool),
				Events:        eventRepo,
				Runs:          repo.NewRunRepo(pool),
				Secrets:       repo.NewSecretRepo(pool),
				Users:         repo.NewUserRepo(pool),
				Sessions:      auth.NewSessions(repo.NewSessionRepo(pool)),
				Invoker:       inv,
				Encryptor:     enc,
				IsEncrypted:   secrets.IsEncrypted,
				Notify:        dispatcher.Notify,
				Config:        cfg,
				SecureCookies: cfg.Serve.SecureCookies,
			})
			pipeline.SetRunUpdateHook(apiSrv.RunUpdateSink())

			httpSrv := &http.Server{Addr: addr, Handler: server.New(ingestor).Handler(
				apiSrv.Mount,
				func(mux *http.ServeMux) { avatar.MountHTTP(mux, avatarsDir) },
				web.Mount,
			)}
			go func() {
				fmt.Printf("webhook listener on %s\n", addr)
				if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					fmt.Fprintf(os.Stderr, "serve: http: %v\n", err)
				}
			}()

			go func() {
				if err := runSchedulerLoop(ctx, pool, inv); err != nil {
					fmt.Fprintf(os.Stderr, "serve: scheduler: %v\n", err)
				}
			}()

			<-ctx.Done()
			fmt.Println("shutting down...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
			return nil
		},
	}
}

// pollInterval reads poll_interval from a plugin's config block (default 60s).
func pollInterval(cfg *config.Config, pluginName string) time.Duration {
	for _, pc := range cfg.Plugins {
		if pc.Name != pluginName {
			continue
		}
		if v, ok := pc.Config["poll_interval"].(string); ok && v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
	}
	return time.Minute
}

// eventsCmd returns the "events" group of subcommands.
func eventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Event management commands",
	}
	cmd.AddCommand(eventsListCmd())
	cmd.AddCommand(eventsReplayCmd())
	return cmd
}

func eventsListCmd() *cobra.Command {
	var flagStatus string
	var flagLimit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent events",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			evs, err := repo.NewEventRepo(pool).ListRecent(ctx, flagStatus, flagLimit)
			if err != nil {
				return fmt.Errorf("list events: %w", err)
			}
			if len(evs) == 0 {
				fmt.Println("(no events)")
				return nil
			}
			fmt.Printf("%-36s %-10s %-14s %-11s %-25s %s\n", "ID", "SOURCE", "TYPE", "STATUS", "RECEIVED", "DEDUP_KEY")
			fmt.Println(strings.Repeat("-", 130))
			for _, ev := range evs {
				fmt.Printf("%-36s %-10s %-14s %-11s %-25s %s\n",
					ev.ID, ev.SourcePlugin, ev.EventType, ev.Status,
					ev.ReceivedAt.Format(time.RFC3339), ev.DedupKey)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&flagStatus, "status", "", "filter by status (pending|dispatched)")
	cmd.Flags().IntVar(&flagLimit, "limit", 50, "max events to show")
	return cmd
}

func eventsReplayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <event-id>",
		Short: "Re-queue a dispatched event (picked up by fleet serve's rescan)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			id, err := uuid.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid event id: %w", err)
			}
			cfg, _ := loadConfig()
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()

			eventRepo := repo.NewEventRepo(pool)
			ev, err := eventRepo.GetByID(ctx, id)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}
			if ev.Status == domain.EventStatusPending {
				fmt.Printf("event %s is already pending\n", id)
				return nil
			}
			if err := eventRepo.MarkPending(ctx, id); err != nil {
				return fmt.Errorf("mark pending: %w", err)
			}
			fmt.Printf("event %s re-queued; fleet serve's rescan will dispatch it within one interval\n", id)
			fmt.Println("note: assignments that already processed this event's dedup_key will record a skipped run")
			return nil
		},
	}
}

// seedCmd returns the "seed" command (DB is the source of truth; this is the
// explicit override).
func seedCmd() *cobra.Command {
	var flagForce bool
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed entities from fleet.yaml (no-op unless DB is empty; --force overwrites)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, err := loadValidatedConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			dsn := resolveDSN(cfg)
			if dsn == "" {
				return fmt.Errorf("no database DSN configured")
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			if flagForce {
				fmt.Println("WARNING: --force overwrites same-named entities, including UI edits")
			}
			if err := seed.FromConfig(ctx, cfg,
				repo.NewAgentRepo(pool), repo.NewDutyRepo(pool), repo.NewAssignmentRepo(pool), flagForce); err != nil {
				return fmt.Errorf("seed: %w", err)
			}
			fmt.Println("seed complete")
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagForce, "force", false, "overwrite existing entities from fleet.yaml")
	return cmd
}

// secretsCmd returns the "secrets" group of subcommands.
func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Secret management commands",
	}
	cmd.AddCommand(secretsSetCmd(), secretsListCmd(), secretsDeleteCmd(), secretsEncryptExistingCmd())
	return cmd
}

func secretsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name>",
		Short: "Set a secret (value read from stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cipher, err := loadCipher()
			if err != nil {
				return err
			}
			if cipher == nil {
				return fmt.Errorf("%s is not set; refusing to store a plaintext secret", secrets.MasterKeyEnv)
			}
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read value from stdin: %w", err)
			}
			value := strings.TrimRight(string(data), "\r\n")
			if value == "" {
				return fmt.Errorf("empty secret value")
			}
			cfg, _ := loadConfig()
			dsn, err := mustDSN(cfg)
			if err != nil {
				return err
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			enc, err := cipher.Encrypt([]byte(value))
			if err != nil {
				return err
			}
			if err := repo.NewSecretRepo(pool).Upsert(ctx, args[0], enc); err != nil {
				return fmt.Errorf("store secret: %w", err)
			}
			fmt.Printf("secret %q stored (encrypted)\n", args[0])
			return nil
		},
	}
}

func secretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List secrets (names and encryption status; values never shown)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn, err := mustDSN(cfg)
			if err != nil {
				return err
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			all, err := repo.NewSecretRepo(pool).List(ctx)
			if err != nil {
				return fmt.Errorf("list secrets: %w", err)
			}
			if len(all) == 0 {
				fmt.Println("(no secrets)")
				return nil
			}
			fmt.Printf("%-40s %-10s\n", "NAME", "ENCRYPTED")
			fmt.Println(strings.Repeat("-", 52))
			for name, val := range all {
				enc := secrets.IsEncrypted(val)
				fmt.Printf("%-40s %-10v\n", name, enc)
			}
			return nil
		},
	}
}

func secretsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn, err := mustDSN(cfg)
			if err != nil {
				return err
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			if err := repo.NewSecretRepo(pool).Delete(ctx, args[0]); err != nil {
				return fmt.Errorf("delete secret: %w", err)
			}
			fmt.Printf("secret %q deleted\n", args[0])
			return nil
		},
	}
}

func secretsEncryptExistingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "encrypt-existing",
		Short: "Encrypt all plaintext secrets in the DB (idempotent; requires FLEET_MASTER_KEY)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cipher, err := loadCipher()
			if err != nil {
				return err
			}
			if cipher == nil {
				return fmt.Errorf("%s is not set; cannot encrypt existing secrets", secrets.MasterKeyEnv)
			}
			cfg, _ := loadConfig()
			dsn, err := mustDSN(cfg)
			if err != nil {
				return err
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			secretRepo := repo.NewSecretRepo(pool)
			all, err := secretRepo.List(ctx)
			if err != nil {
				return fmt.Errorf("list secrets: %w", err)
			}
			count := 0
			for name, val := range all {
				if secrets.IsEncrypted(val) {
					continue
				}
				enc, err := cipher.Encrypt(val)
				if err != nil {
					return fmt.Errorf("encrypt secret %q: %w", name, err)
				}
				if err := secretRepo.Upsert(ctx, name, enc); err != nil {
					return fmt.Errorf("store secret %q: %w", name, err)
				}
				fmt.Printf("encrypted secret %q\n", name)
				count++
			}
			fmt.Printf("done: %d secret(s) encrypted\n", count)
			return nil
		},
	}
}

// usersCmd returns the "users" group of subcommands.
func usersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "Operator account commands",
	}
	cmd.AddCommand(usersCreateCmd(), usersListCmd(), usersDeleteCmd())
	return cmd
}

func usersCreateCmd() *cobra.Command {
	var flagRole string
	var flagPasswordStdin bool
	cmd := &cobra.Command{
		Use:   "create <username>",
		Short: "Create an operator account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			if flagRole != domain.RoleAdmin && flagRole != domain.RoleViewer {
				return fmt.Errorf("--role must be admin or viewer")
			}
			var password string
			if flagPasswordStdin {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read password from stdin: %w", err)
				}
				password = strings.TrimRight(string(data), "\r\n")
			} else {
				fmt.Print("Password: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("read password: %w", err)
				}
				password = strings.TrimRight(line, "\r\n")
			}
			hash, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			cfg, _ := loadConfig()
			dsn, err := mustDSN(cfg)
			if err != nil {
				return err
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			u := &domain.User{Username: args[0], PasswordHash: hash, Role: flagRole}
			if err := repo.NewUserRepo(pool).Create(ctx, u); err != nil {
				return fmt.Errorf("create user: %w", err)
			}
			fmt.Printf("user %q created with role %s\n", u.Username, u.Role)
			return nil
		},
	}
	cmd.Flags().StringVar(&flagRole, "role", "viewer", "admin or viewer")
	cmd.Flags().BoolVar(&flagPasswordStdin, "password-stdin", false, "read password from stdin")
	return cmd
}

func usersListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List operator accounts (username, role, created; never password hashes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn, err := mustDSN(cfg)
			if err != nil {
				return err
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			users, err := repo.NewUserRepo(pool).List(ctx)
			if err != nil {
				return fmt.Errorf("list users: %w", err)
			}
			if len(users) == 0 {
				fmt.Println("(no users)")
				return nil
			}
			fmt.Printf("%-30s %-10s %-25s\n", "USERNAME", "ROLE", "CREATED")
			fmt.Println(strings.Repeat("-", 67))
			for _, u := range users {
				fmt.Printf("%-30s %-10s %-25s\n", u.Username, u.Role, u.CreatedAt.Format(time.RFC3339))
			}
			return nil
		},
	}
}

func usersDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <username>",
		Short: "Delete an operator account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			cfg, _ := loadConfig()
			dsn, err := mustDSN(cfg)
			if err != nil {
				return err
			}
			pool, err := db.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer pool.Close()
			if err := repo.NewUserRepo(pool).Delete(ctx, args[0]); err != nil {
				return fmt.Errorf("delete user: %w", err)
			}
			fmt.Printf("user %q deleted\n", args[0])
			return nil
		},
	}
}
