package application

import "context"

type ProjectHookSelection struct {
	Fingerprint      string
	AfterEditFormat  bool
	BeforeFinishTest bool
}

// ProjectHookAuthorizer checks the operator's trust in the exact pinned project
// hook definition. It does not authorize execution; normal tool policy does.
type ProjectHookAuthorizer interface {
	AuthorizeProjectHooks(context.Context, string) (ProjectHookSelection, error)
}

func (s *AgentRunner) WithProjectHooks(authorizer ProjectHookAuthorizer) *AgentRunner {
	s.projectHooks = authorizer
	return s
}
