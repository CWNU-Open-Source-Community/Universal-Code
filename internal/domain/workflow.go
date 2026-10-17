package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
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
	Inputs        map[string]WorkflowValueRef `json:"inputs,omitempty"`
	Condition     *WorkflowCondition          `json:"condition,omitempty"`
	Then          []WorkflowStep              `json:"then,omitempty"`
	Else          []WorkflowStep              `json:"else,omitempty"`
	Body          []WorkflowStep              `json:"body,omitempty"`
	MaxIterations int                         `json:"max_iterations,omitempty"`
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
	Stack      []WorkflowFrame            `json:"stack"`
	Outputs    map[string]json.RawMessage `json:"outputs"`
	Iterations map[string]int             `json:"iterations"`
	Steps      int                        `json:"steps"`
	Jobs       int                        `json:"jobs"`
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
			switch s.Kind {
			case "worker":
				if s.Task == nil || s.Condition != nil || len(s.Then)+len(s.Else)+len(s.Body) != 0 || s.MaxIterations != 0 || len(s.Task.DependencyOrdinals) != 0 {
					return errors.New("worker step requires one task without control blocks or task dependencies")
				}
				normalized, err := NormalizeChildTaskProposalSpec(ChildTaskProposalSpec{Version: ChildTaskProposalVersion, Tasks: []ChildTask{*s.Task}})
				if err != nil {
					return fmt.Errorf("step %s: %w", s.ID, err)
				}
				task := normalized.Tasks[0]
				s.Task = &task
			case "if":
				if s.Task != nil || s.Condition == nil || len(s.Body) != 0 || s.MaxIterations != 0 || len(s.Inputs) != 0 {
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
				if s.Task != nil || s.Condition == nil || len(s.Then)+len(s.Else) != 0 || s.MaxIterations < 1 || s.MaxIterations > 32 || len(s.Inputs) != 0 {
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
