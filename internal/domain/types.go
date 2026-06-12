package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// BackendRef selects a named backend with optional model/effort overrides.
type BackendRef struct {
	Name   string `yaml:"name" json:"name"`
	Model  string `yaml:"model,omitempty" json:"model,omitempty"`
	Effort string `yaml:"effort,omitempty" json:"effort,omitempty"`
}

// Agent is a configured employee: a persona with a name, role, system prompt, and default backend.
type Agent struct {
	ID             uuid.UUID  `db:"id" json:"id"`
	Name           string     `db:"name" json:"name"`
	Role           string     `db:"role" json:"role"`
	SystemPrompt   string     `db:"system_prompt" json:"system_prompt"`
	DefaultBackend BackendRef `db:"default_backend" json:"default_backend"`
	Enabled        bool       `db:"enabled" json:"enabled"`
	AvatarURL      *string    `db:"avatar_url" json:"avatar_url"` // generated/uploaded avatar (SP4c fills it)
	HiredAt        *time.Time `db:"hired_at" json:"hired_at"`     // "hire date" flavour shown in the UI
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at" json:"updated_at"`
}

// OutputActionType declares an output action a Duty can emit.
type OutputActionType struct {
	Plugin string `json:"plugin"`
	Action string `json:"action"`
}

// Duty is a reusable definition of work.
type Duty struct {
	ID            uuid.UUID          `db:"id" json:"id"`
	Name          string             `db:"name" json:"name"`
	Role          string             `db:"role" json:"role"` // category tag, not operative persona
	Description   string             `db:"description" json:"description"`
	TriggerKinds  []string           `db:"trigger_kinds" json:"trigger_kinds"`
	Prompt        string             `db:"prompt" json:"prompt"`
	RequiredTools []string           `db:"required_tools" json:"required_tools"`
	OutputActions []OutputActionType `db:"output_actions" json:"output_actions"`
	ConfigSchema  map[string]any     `db:"config_schema" json:"config_schema"`
	Backend       *BackendRef        `db:"backend" json:"backend"`
	CreatedAt     time.Time          `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time          `db:"updated_at" json:"updated_at"`
}

// TriggerConfig holds the chosen trigger kind and its configuration.
type TriggerConfig struct {
	Kind     string         `json:"kind"`
	Schedule string         `json:"schedule,omitempty"` // cron expression
	Filter   map[string]any `json:"filter,omitempty"`
}

// OutputBinding routes an output action to a specific target. When ForEach
// names a key of LLMResult.Output holding a JSON array, the action is
// delivered once per element (the element renders as {{.Item.*}}).
type OutputBinding struct {
	Plugin  string         `json:"plugin" yaml:"plugin"`
	Action  string         `json:"action" yaml:"action"`
	Params  map[string]any `json:"params" yaml:"params"`
	ForEach string         `json:"for_each,omitempty" yaml:"for_each,omitempty"`
}

// Assignment binds an Agent to a Duty with per-agent config.
type Assignment struct {
	ID                 uuid.UUID       `db:"id" json:"id"`
	Name               string          `db:"name" json:"name"`
	AgentID            uuid.UUID       `db:"agent_id" json:"agent_id"`
	DutyID             uuid.UUID       `db:"duty_id" json:"duty_id"`
	Enabled            bool            `db:"enabled" json:"enabled"`
	Trigger            TriggerConfig   `db:"trigger" json:"trigger"`
	Outputs            []OutputBinding `db:"outputs" json:"outputs"`
	Config             map[string]any  `db:"config" json:"config"`
	Backend            *BackendRef     `db:"backend" json:"backend"`
	TaskPromptOverride *string         `db:"task_prompt_override" json:"task_prompt_override"`
	ExtraInstructions  *string         `db:"extra_instructions" json:"extra_instructions"`
	CreatedAt          time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time       `db:"updated_at" json:"updated_at"`
}

// RunStatus represents the lifecycle state of a Run.
type RunStatus string

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusSkipped   RunStatus = "skipped"
)

// LLMResult is the structured result returned by an Executor.
type LLMResult struct {
	Status     int            `json:"status"`
	Summary    string         `json:"summary"`
	Output     map[string]any `json:"output"`
	Transcript string         `json:"transcript"`
	Tokens     int            `json:"tokens"`
	Cost       float64        `json:"cost"`
}

// OutputDelivery records the result of one output action delivery.
type OutputDelivery struct {
	Plugin string         `json:"plugin"`
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
	Status string         `json:"status"`
	Error  string         `json:"error,omitempty"`
}

// EventStatus is the dispatch lifecycle state of an Event.
type EventStatus string

const (
	EventStatusPending    EventStatus = "pending"
	EventStatusDispatched EventStatus = "dispatched"
)

// Event is the normalized envelope for one inbound occurrence from a plugin
// event source. Persisted for durability and replay; dispatch is
// at-least-once (per-assignment dedup makes redelivery a recorded skip).
type Event struct {
	ID           uuid.UUID       `db:"id" json:"id"`
	SourcePlugin string          `db:"source_plugin" json:"source_plugin"` // e.g. "gitlab"
	EventType    string          `db:"event_type" json:"event_type"`       // e.g. "mr_opened"
	PayloadRaw   json.RawMessage `db:"payload_raw" json:"payload_raw"`     // verbatim from the source
	PayloadNorm  map[string]any  `db:"payload_norm" json:"payload_norm"`   // plugin-normalized, template-friendly
	Identity     string          `db:"identity" json:"identity"`           // who triggered it (author/sender)
	DedupKey     string          `db:"dedup_key" json:"dedup_key"`         // stable "already processed" key
	Status       EventStatus     `db:"status" json:"status"`
	ReceivedAt   time.Time       `db:"received_at" json:"received_at"`
	DispatchedAt *time.Time      `db:"dispatched_at" json:"dispatched_at"`
}

// Run is one execution of an Assignment; the audit and metrics record.
type Run struct {
	ID                   uuid.UUID        `db:"id" json:"id"`
	AssignmentID         uuid.UUID        `db:"assignment_id" json:"assignment_id"`
	AgentID              uuid.UUID        `db:"agent_id" json:"agent_id"`
	DutyID               uuid.UUID        `db:"duty_id" json:"duty_id"`
	TriggerKind          string           `db:"trigger_kind" json:"trigger_kind"`
	EventID              *string          `db:"event_id" json:"event_id"`
	RenderedSystemPrompt string           `db:"rendered_system_prompt" json:"rendered_system_prompt"`
	RenderedPrompt       string           `db:"rendered_prompt" json:"rendered_prompt"`
	LLMResult            *LLMResult       `db:"llm_result" json:"llm_result"`
	OutputsDelivered     []OutputDelivery `db:"outputs_delivered" json:"outputs_delivered"`
	Status               RunStatus        `db:"status" json:"status"`
	Tokens               int              `db:"tokens" json:"tokens"`
	Cost                 float64          `db:"cost" json:"cost"`
	StartedAt            time.Time        `db:"started_at" json:"started_at"`
	FinishedAt           *time.Time       `db:"finished_at" json:"finished_at"`
	Error                *string          `db:"error" json:"error"`
}

// User is an operator account. Roles: admin (full control) | viewer (read-only).
type User struct {
	ID           uuid.UUID `db:"id" json:"id"`
	Username     string    `db:"username" json:"username"`
	PasswordHash string    `db:"password_hash" json:"-"`
	Role         string    `db:"role" json:"role"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`
}

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// AgentStats is the derived per-agent metrics view (spec.md §6) — computed
// from runs on demand, never stored.
type AgentStats struct {
	AgentID          uuid.UUID  `json:"agent_id"`
	TotalRuns        int        `json:"total_runs"`
	RunsLast30d      int        `json:"runs_last_30d"`
	SuccessRate      float64    `json:"success_rate"` // succeeded/(succeeded+failed), last 30d; 0 when no terminal runs
	SkipRate         float64    `json:"skip_rate"`    // skipped/total, last 30d; 0 when no runs
	TotalTokens      int        `json:"total_tokens"`
	TotalCostUSD     float64    `json:"total_cost_usd"`
	TokensLast30d    int        `json:"tokens_last_30d"`
	CostLast30dUSD   float64    `json:"cost_last_30d_usd"`
	OutputsDelivered int        `json:"outputs_delivered"`
	OutputsLast30d   int        `json:"outputs_last_30d"`
	AvgRunDurationS  float64    `json:"avg_run_duration_s"`
	LastRunAt        *time.Time `json:"last_run_at"`
}
