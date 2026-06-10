package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/db"
	"github.com/cbarraford/office-fleet/internal/domain"
	"github.com/cbarraford/office-fleet/internal/executor"
	"github.com/cbarraford/office-fleet/internal/plugin"
	"github.com/cbarraford/office-fleet/internal/repo"
	"github.com/cbarraford/office-fleet/internal/run"
	"github.com/cbarraford/office-fleet/internal/seed"
	"github.com/cbarraford/office-fleet/internal/state"
	"github.com/cbarraford/office-fleet/internal/trigger"

	// Register all plugins via init().
	_ "github.com/cbarraford/office-fleet/internal/plugins/gitlab"
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

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// loadConfig loads fleet.yaml from the --config flag path.
func loadConfig() (*config.Config, error) {
	return config.Load(flagConfig)
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

// buildSecretLookup returns a SecretLookup that queries the secrets table.
// Missing secrets return ("", nil) so --fake runs remain usable without seeded secrets.
func buildSecretLookup(ctx context.Context, pool *pgxpool.Pool) plugin.SecretLookup {
	return func(name string) (string, error) {
		var val []byte
		err := pool.QueryRow(ctx, "SELECT encrypted_value FROM secrets WHERE name=$1", name).Scan(&val)
		if err != nil {
			return "", nil
		}
		return string(val), nil
	}
}

// dbSecretsProvider implements run.SecretsProvider by loading all secrets from the DB.
type dbSecretsProvider struct {
	pool *pgxpool.Pool
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
		m[name] = string(val)
	}
	return m, rows.Err()
}

// initPlugins initialises all registered plugins using config and DB secrets.
func initPlugins(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool) {
	secretLookup := buildSecretLookup(ctx, pool)
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
				if err := seed.FromConfig(ctx, cfg, agentRepo, dutyRepo, assignmentRepo); err != nil {
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
			cfg, err := loadConfig()
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

			cfg, err := loadConfig()
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
			initPlugins(ctx, cfg, pool)

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
			pipeline := run.NewPipeline(cfg, runRepo, store, &dbSecretsProvider{pool: pool})

			result, err := pipeline.Execute(ctx, run.ExecuteRequest{
				Assignment:  assignment,
				Agent:       agent,
				Duty:        duty,
				TriggerKind: "manual",
				EventParams: eventParams,
				Executor:    exec,
			})
			if err != nil {
				return fmt.Errorf("execute: %w", err)
			}

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

// scheduleCmd returns the "schedule" daemon subcommand.
func scheduleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schedule",
		Short: "Run the cron scheduler daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			cfg, err := loadConfig()
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

			initPlugins(ctx, cfg, pool)

			assignments, err := repo.NewAssignmentRepo(pool).List(ctx)
			if err != nil {
				return fmt.Errorf("list assignments: %w", err)
			}

			sched := trigger.NewScheduler()
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

			agentRepo := repo.NewAgentRepo(pool)
			dutyRepo := repo.NewDutyRepo(pool)
			assignmentRepo := repo.NewAssignmentRepo(pool)
			runRepo := repo.NewRunRepo(pool)
			store := state.NewPostgresStore(pool)
			pipeline := run.NewPipeline(cfg, runRepo, store, &dbSecretsProvider{pool: pool})

			fmt.Println("scheduler running...")

			sched.Run(ctx, func(runCtx context.Context, assignmentID string) {
				id, err := uuid.Parse(assignmentID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "scheduler: invalid assignment id %s: %v\n", assignmentID, err)
					return
				}
				assignment, err := assignmentRepo.GetByID(runCtx, id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "scheduler: get assignment %s: %v\n", assignmentID, err)
					return
				}

				allAgents, err := agentRepo.List(runCtx)
				if err != nil {
					fmt.Fprintf(os.Stderr, "scheduler: list agents: %v\n", err)
					return
				}
				var agent *domain.Agent
				for _, a := range allAgents {
					if a.ID == assignment.AgentID {
						agent = a
						break
					}
				}
				if agent == nil {
					fmt.Fprintf(os.Stderr, "scheduler: agent %s not found\n", assignment.AgentID)
					return
				}

				allDuties, err := dutyRepo.List(runCtx)
				if err != nil {
					fmt.Fprintf(os.Stderr, "scheduler: list duties: %v\n", err)
					return
				}
				var duty *domain.Duty
				for _, d := range allDuties {
					if d.ID == assignment.DutyID {
						duty = d
						break
					}
				}
				if duty == nil {
					fmt.Fprintf(os.Stderr, "scheduler: duty %s not found\n", assignment.DutyID)
					return
				}

				var resolvedBackend *config.Backend
				for _, ac := range cfg.Assignments {
					if ac.Agent == agent.Name && ac.Duty == duty.Name {
						b, _, berr := config.ResolveBackend(cfg, ac)
						if berr == nil {
							resolvedBackend = b
						}
						break
					}
				}
				var exec executor.Executor
				if resolvedBackend == nil {
					exec = executor.NewClaudeExecutor("")
				} else {
					var eerr error
					exec, eerr = executor.FromBackend(cfg, resolvedBackend)
					if eerr != nil {
						fmt.Fprintf(os.Stderr, "scheduler: build executor for assignment %s: %v\n", assignmentID, eerr)
						return
					}
				}
				result, err := pipeline.Execute(runCtx, run.ExecuteRequest{
					Assignment:  assignment,
					Agent:       agent,
					Duty:        duty,
					TriggerKind: "cron",
					EventParams: map[string]any{},
					Executor:    exec,
				})
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

			return nil
		},
	}
}
