package domain

const ThreadCommandServicePolicyVersion = "thread_command_service_policy.v1"

type CommandServicePolicy string

const (
	CommandServicePolicyAuto      CommandServicePolicy = "auto"
	CommandServicePolicyExclusive CommandServicePolicy = "exclusive"
)

func (p CommandServicePolicy) Valid() bool {
	return p == CommandServicePolicyAuto || p == CommandServicePolicyExclusive
}

// This preference only narrows command behavior. Auto never grants execution,
// changes the selected sandbox, or converts an existing exclusive Job.
type ThreadCommandServicePolicy struct {
	Version  string               `json:"version"`
	ThreadID string               `json:"thread_id"`
	Policy   CommandServicePolicy `json:"policy"`
	Revision int64                `json:"revision"`
}
