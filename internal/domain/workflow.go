package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/workspaceidentity"
)

const WorkflowVersion = "workflow.v1"
const MaxWorkflowJSONBytes = 128 * 1024
const MaxWorkflowJobs = 64

// WorkflowSpec is an immutable, bounded program. References read committed
// outputs; model prose never supplies executable expressions.
type WorkflowSpec struct {
	Version string         `json:"version"`
	Name    string         `json:"name"`
	Limits  WorkflowLimits `json:"limits"`
	Steps   []WorkflowStep `json:"steps"`
}
type WorkflowLimits struct {
	MaxSteps      int   `json:"max_steps"`
	MaxJobs       int   `json:"max_jobs"`
	MaxTokens     int64 `json:"max_tokens"`
	MaxTurns      int64 `json:"max_turns"`
	TimeoutMillis int64 `json:"timeout_millis"`
}
type WorkflowValueRef struct {
	// "$input" addresses execution input; otherwise Step is a step ID.
	Step string `json:"step"`
	Path string `json:"path,omitempty"`
}
type WorkflowCondition struct {
	Ref    WorkflowValueRef `json:"ref"`
	Equals json.RawMessage  `json:"equals"`
}
type WorkflowStep struct {
	ID            string                      `json:"id"`
	Kind          string                      `json:"kind"`
	Task          *ChildTask                  `json:"task,omitempty"`
	Completion    *WorkflowCompletion         `json:"completion,omitempty"`
	BatchPlan     *WorkflowValueRef           `json:"batch_plan,omitempty"`
	Inputs        map[string]WorkflowValueRef `json:"inputs,omitempty"`
	Condition     *WorkflowCondition          `json:"condition,omitempty"`
	Then          []WorkflowStep              `json:"then,omitempty"`
	Else          []WorkflowStep              `json:"else,omitempty"`
	Body          []WorkflowStep              `json:"body,omitempty"`
	MaxIterations int                         `json:"max_iterations,omitempty"`
	Items         *WorkflowValueRef           `json:"items,omitempty"`
	MaxItems      int                         `json:"max_items,omitempty"`
	Parallelism   int                         `json:"parallelism,omitempty"`
	MaxAttempts   int                         `json:"max_attempts,omitempty"`
}

func (s WorkflowStep) AttemptLimit() int {
	if s.MaxAttempts == 0 {
		return 3
	}
	return s.MaxAttempts
}
func (s WorkflowStep) WorkerParallelism() int {
	if s.Parallelism == 0 {
		return 1
	}
	return s.Parallelism
}

type WorkflowMapItem struct {
	Index           int             `json:"index"`
	Value           json.RawMessage `json:"value,omitempty"`
	Generation      int             `json:"generation"`
	StepExecutionID string          `json:"step_execution_id"`
	Status          string          `json:"status"`
	Reason          string          `json:"reason,omitempty"`
}

type WorkflowMapState struct {
	StepID string            `json:"step_id"`
	Kind   string            `json:"kind"`
	Inputs json.RawMessage   `json:"inputs"`
	Items  []WorkflowMapItem `json:"items"`
}

type WorkflowBlocked struct {
	Key       string `json:"key"`
	StepID    string `json:"step_id"`
	Retryable bool   `json:"retryable"`
}

type WorkflowRetryOperation struct {
	Revision int64  `json:"revision"`
	Key      string `json:"key"`
}

// The first ordinary worker identity remains compatible with existing steps.
func WorkflowItemStepID(key, kind string, index, generation int) string {
	if kind == "worker" && index == 0 && generation == 1 {
		return "workflowstep-" + key
	}
	return "workflowstep-" + runmutation.Fingerprint("workflow_item.v1", key, strconv.Itoa(index), strconv.Itoa(generation))
}

func (s WorkflowSpec) Step(id string) (WorkflowStep, bool) {
	var find func([]WorkflowStep) (WorkflowStep, bool)
	find = func(steps []WorkflowStep) (WorkflowStep, bool) {
		for _, step := range steps {
			if step.ID == id {
				return step, true
			}
			for _, block := range [][]WorkflowStep{step.Then, step.Else, step.Body} {
				if found, ok := find(block); ok {
					return found, true
				}
			}
		}
		return WorkflowStep{}, false
	}
	return find(s.Steps)
}

// Omitted completion retains the existing report-driven workflow semantics.
type WorkflowCompletion struct {
	Mode   string `json:"mode"`
	Target string `json:"target,omitempty"`
}

func (s WorkflowStep) RequiresCodeVerification() bool {
	return s.Completion != nil && s.Completion.Mode == "verified_code"
}

func (s WorkflowStep) VerificationTarget() string {
	if s.Completion == nil || s.Completion.Target == "" {
		return "."
	}
	return s.Completion.Target
}

// WorkflowVerification records the last host check, not a permanent grant.
// Completion rechecks the original native evidence against current source.
type WorkflowVerification struct {
	StepExecutionID string    `json:"step_execution_id"`
	StepID          string    `json:"step_id"`
	AgentID         string    `json:"agent_id"`
	JobID           string    `json:"job_id,omitempty"`
	Target          string    `json:"target"`
	State           string    `json:"state"`
	Reusable        bool      `json:"reusable"`
	SourceVersion   string    `json:"source_version,omitempty"`
	TestFingerprint string    `json:"test_fingerprint,omitempty"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	TreeReaped      bool      `json:"tree_reaped"`
	CheckedAt       time.Time `json:"checked_at"`
	Reason          string    `json:"reason,omitempty"`
}
type WorkflowDefinition struct {
	ID        string       `json:"id"`
	Digest    string       `json:"digest"`
	Spec      WorkflowSpec `json:"spec"`
	CreatedAt time.Time    `json:"created_at"`
}
type WorkflowFrame struct {
	Block     string `json:"block"`
	Index     int    `json:"index"`
	Iteration int    `json:"iteration,omitempty"`
}
type WorkflowState struct {
	Stack           []WorkflowFrame                   `json:"stack"`
	Outputs         map[string]json.RawMessage        `json:"outputs"`
	Iterations      map[string]int                    `json:"iterations"`
	Steps           int                               `json:"steps"`
	Jobs            int                               `json:"jobs"`
	Verifications   map[string]WorkflowVerification   `json:"verifications,omitempty"`
	Maps            map[string]WorkflowMapState       `json:"maps,omitempty"`
	AllocatedJobs   int                               `json:"allocated_jobs,omitempty"`
	Blocked         *WorkflowBlocked                  `json:"blocked,omitempty"`
	RetryOperations map[string]WorkflowRetryOperation `json:"retry_operations,omitempty"`
	// WaitFingerprint records the dependencies observed before yielding. A
	// waiting execution wakes when those durable dependencies change.
	WaitFingerprint string `json:"wait_fingerprint,omitempty"`
}
type WorkflowExecution struct {
	ID           string          `json:"id"`
	DefinitionID string          `json:"definition_id"`
	RunID        string          `json:"run_id"`
	Inputs       json.RawMessage `json:"inputs"`
	Status       string          `json:"status"`
	Reason       string          `json:"reason,omitempty"`
	State        WorkflowState   `json:"state"`
	UsageBefore  RunAgentUsage   `json:"usage_before"`
	TurnsBefore  int64           `json:"turns_before"`
	Version      int64           `json:"revision"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}
type WorkflowStepExecution struct {
	ID          string          `json:"id"`
	ExecutionID string          `json:"execution_id"`
	StepID      string          `json:"step_id"`
	AgentID     string          `json:"agent_id"`
	SessionID   string          `json:"session_id"`
	Inputs      json.RawMessage `json:"inputs"`
	Status      string          `json:"status"`
	Output      json.RawMessage `json:"output,omitempty"`
	Reason      string          `json:"reason,omitempty"`
}

func DecodeWorkflowSpec(raw []byte) (WorkflowSpec, error) {
	var spec WorkflowSpec
	if len(raw) == 0 || len(raw) > MaxWorkflowJSONBytes {
		return spec, errors.New("workflow JSON exceeds its byte bound")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&spec); err != nil {
		return spec, err
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return spec, errors.New("workflow contains trailing JSON")
	}
	return NormalizeWorkflowSpec(spec)
}
func NormalizeWorkflowSpec(spec WorkflowSpec) (WorkflowSpec, error) {
	if spec.Version != WorkflowVersion || strings.TrimSpace(spec.Name) != spec.Name || spec.Name == "" || len(spec.Name) > 256 {
		return spec, errors.New("workflow version or name is invalid")
	}
	l := spec.Limits
	if l.MaxSteps < 1 || l.MaxSteps > 1024 || l.MaxJobs < 1 || l.MaxJobs > MaxWorkflowJobs || l.MaxTokens < 1 || l.MaxTokens > MaxAgentTokenReservation || l.MaxTurns < 1 || l.MaxTurns > 4096 || l.TimeoutMillis < 1 || l.TimeoutMillis > 86400000 {
		return spec, errors.New("workflow requires bounded step, job, token, turn and time limits")
	}
	seen := map[string]bool{}
	count := 0
	var visit func([]WorkflowStep, int) error
	visit = func(steps []WorkflowStep, depth int) error {
		if depth > 8 || len(steps) == 0 {
			return errors.New("workflow blocks must be nonempty and at most eight levels deep")
		}
		for i := range steps {
			s := &steps[i]
			count++
			if count > 128 || !validWorkflowStepID(s.ID) || seen[s.ID] {
				return errors.New("workflow requires at most 128 uniquely named steps")
			}
			seen[s.ID] = true
			for name, ref := range s.Inputs {
				if !validWorkflowStepID(name) || !validWorkflowRef(ref) {
					return errors.New("workflow input reference is invalid")
				}
			}
			if s.Condition != nil && (!validWorkflowRef(s.Condition.Ref) || !json.Valid(s.Condition.Equals) || len(s.Condition.Equals) > 4096) {
				return errors.New("workflow condition is invalid")
			}
			if c := s.Completion; c != nil {
				if (s.Kind != "worker" && s.Kind != "map") || (c.Mode != "report" && c.Mode != "verified_code") ||
					(c.Mode == "report" && c.Target != "") || strings.TrimSpace(c.Target) != c.Target || len(c.Target) > 1024 {
					return errors.New("workflow completion requires a worker and mode report or verified_code; only verified_code accepts a target")
				}
				if c.Target != "" {
					if err := (workspaceidentity.PathPolicy{}).Check(c.Target); err != nil {
						return fmt.Errorf("step %s verification target: %w", s.ID, err)
					}
				}
			}
			if s.Kind != "worker" && s.Kind != "map" && (s.Items != nil || s.MaxItems != 0 || s.Parallelism != 0 || s.MaxAttempts != 0) {
				return errors.New("workflow expansion and retry fields require a worker or map")
			}
			switch s.Kind {
			case "worker", "map":
				if s.Kind == "map" {
					if _, ok := s.Inputs["item"]; ok {
						return errors.New("map input name item is reserved")
					}
					if _, ok := s.Inputs["index"]; ok {
						return errors.New("map input name index is reserved")
					}
				}
				if s.MaxAttempts < 0 || s.MaxAttempts > 3 {
					return errors.New("workflow max_attempts must be 1..3 when provided")
				}
				if s.Kind == "worker" && (s.Items != nil || s.MaxItems != 0 || s.Parallelism != 0) {
					return errors.New("worker cannot declare map expansion fields")
				}
				if s.Kind == "map" && (s.Items == nil || !validWorkflowRef(*s.Items) || s.MaxItems < 1 || s.MaxItems > 64 || s.Parallelism < 0 || s.Parallelism > 2) {
					return errors.New("map requires items, max_items 1..64 and parallelism 1..2 when provided")
				}
				if s.Task == nil || s.BatchPlan != nil || s.Condition != nil || len(s.Then)+len(s.Else)+len(s.Body) != 0 || s.MaxIterations != 0 || len(s.Task.DependencyOrdinals) != 0 {
					return errors.New("worker step requires one task without control blocks or task dependencies")
				}
				normalized, err := NormalizeChildTaskProposalSpec(ChildTaskProposalSpec{Version: ChildTaskProposalVersion, Tasks: []ChildTask{*s.Task}})
				if err != nil {
					return fmt.Errorf("step %s: %w", s.ID, err)
				}
				task := normalized.Tasks[0]
				s.Task = &task
				if s.RequiresCodeVerification() && !task.Worker.AllowsTool("run_tests") {
					return errors.New("verified_code completion requires an execution worker with run_tests")
				}
			case "batch":
				if s.BatchPlan == nil || !validWorkflowRef(*s.BatchPlan) || s.Task != nil || s.Condition != nil || len(s.Then)+len(s.Else)+len(s.Body)+len(s.Inputs) != 0 || s.MaxIterations != 0 {
					return errors.New("batch step requires a reference to an approved isolated delivery plan")
				}
			case "if":
				if s.Task != nil || s.BatchPlan != nil || s.Condition == nil || len(s.Body) != 0 || s.MaxIterations != 0 || len(s.Inputs) != 0 {
					return errors.New("if step requires a condition and branches")
				}
				if err := visit(s.Then, depth+1); err != nil {
					return err
				}
				if len(s.Else) > 0 {
					if err := visit(s.Else, depth+1); err != nil {
						return err
					}
				}
			case "repeat":
				if s.Task != nil || s.BatchPlan != nil || s.Condition == nil || len(s.Then)+len(s.Else) != 0 || s.MaxIterations < 1 || s.MaxIterations > 32 || len(s.Inputs) != 0 {
					return errors.New("repeat requires an until condition and 1..32 iterations")
				}
				if err := visit(s.Body, depth+1); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported workflow step kind %q", s.Kind)
			}
		}
		return nil
	}
	if err := visit(spec.Steps, 0); err != nil {
		return spec, err
	}
	raw, err := json.Marshal(spec)
	if err != nil || len(raw) > MaxWorkflowJSONBytes {
		return spec, errors.New("workflow exceeds its JSON byte bound")
	}
	return spec, nil
}
func validWorkflowStepID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func validWorkflowRef(ref WorkflowValueRef) bool {
	return (ref.Step == "$input" || validWorkflowStepID(ref.Step)) && len(ref.Path) <= 1024 && (ref.Path == "" || strings.HasPrefix(ref.Path, "/"))
}
