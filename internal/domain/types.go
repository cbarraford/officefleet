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
	ID             uuid.UUID  `db:"id"`
	Name           string     `db:"name"`
	Role           string     `db:"role"`
	SystemPrompt   string     `db:"system_prompt"`
	DefaultBackend BackendRef `db:"default_backend"`
	Enabled        bool       `db:"enabled"`
	CreatedAt      time.Time  `db:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"`
}

// OutputActionType declares an output action a Duty can emit.
type OutputActionType struct {
	Plugin string `json:"plugin"`
	Action string `json:"action"`
}

// Duty is a reusable definition of work.
type Duty struct {
	ID            uuid.UUID          `db:"id"`
	Name          string             `db:"name"`
	Role          string             `db:"role"` // category tag, not operative persona
	Description   string             `db:"description"`
	TriggerKinds  []string           `db:"trigger_kinds"`
	Prompt        string             `db:"prompt"`
	RequiredTools []string           `db:"required_tools"`
	OutputActions []OutputActionType `db:"output_actions"`
	ConfigSchema  map[string]any     `db:"config_schema"`
	Backend       *BackendRef        `db:"backend"`
	CreatedAt     time.Time          `db:"created_at"`
	UpdatedAt     time.Time          `db:"updated_at"`
}

// TriggerConfig holds the chosen trigger kind and its configuration.
type TriggerConfig struct {
	Kind     string         `json:"kind"`
	Schedule string         `json:"schedule,omitempty"` // cron expression
	Filter   map[string]any `json:"filter,omitempty"`
}

// OutputBinding routes an output action to a specific target.
type OutputBinding struct {
	Plugin string         `json:"plugin"`
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

// Assignment binds an Agent to a Duty with per-agent config.
type Assignment struct {
	ID                 uuid.UUID       `db:"id"`
	AgentID            uuid.UUID       `db:"agent_id"`
	DutyID             uuid.UUID       `db:"duty_id"`
	Enabled            bool            `db:"enabled"`
	Trigger            TriggerConfig   `db:"trigger"`
	Outputs            []OutputBinding `db:"outputs"`
	Config             map[string]any  `db:"config"`
	Backend            *BackendRef     `db:"backend"`
	TaskPromptOverride *string         `db:"task_prompt_override"`
	ExtraInstructions  *string         `db:"extra_instructions"`
	CreatedAt          time.Time       `db:"created_at"`
	UpdatedAt          time.Time       `db:"updated_at"`
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
	ID           uuid.UUID       `db:"id"`
	SourcePlugin string          `db:"source_plugin"` // e.g. "gitlab"
	EventType    string          `db:"event_type"`    // e.g. "mr_opened"
	PayloadRaw   json.RawMessage `db:"payload_raw"`   // verbatim from the source
	PayloadNorm  map[string]any  `db:"payload_norm"`  // plugin-normalized, template-friendly
	Identity     string          `db:"identity"`      // who triggered it (author/sender)
	DedupKey     string          `db:"dedup_key"`     // stable "already processed" key
	Status       EventStatus     `db:"status"`
	ReceivedAt   time.Time       `db:"received_at"`
	DispatchedAt *time.Time      `db:"dispatched_at"`
}

// Run is one execution of an Assignment; the audit and metrics record.
type Run struct {
	ID                   uuid.UUID        `db:"id"`
	AssignmentID         uuid.UUID        `db:"assignment_id"`
	AgentID              uuid.UUID        `db:"agent_id"`
	DutyID               uuid.UUID        `db:"duty_id"`
	TriggerKind          string           `db:"trigger_kind"`
	EventID              *string          `db:"event_id"`
	RenderedSystemPrompt string           `db:"rendered_system_prompt"`
	RenderedPrompt       string           `db:"rendered_prompt"`
	LLMResult            *LLMResult       `db:"llm_result"`
	OutputsDelivered     []OutputDelivery `db:"outputs_delivered"`
	Status               RunStatus        `db:"status"`
	Tokens               int              `db:"tokens"`
	Cost                 float64          `db:"cost"`
	StartedAt            time.Time        `db:"started_at"`
	FinishedAt           *time.Time       `db:"finished_at"`
	Error                *string          `db:"error"`
}
