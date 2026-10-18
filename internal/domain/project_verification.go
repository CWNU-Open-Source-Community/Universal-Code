package domain

// ProjectVerificationCall preserves each native ledger's identity. It is an
// internal read projection, never authority to dispatch a command.
type ProjectVerificationCall struct {
	Supervisor *SupervisorToolCall
	Specialist *SpecialistToolCall
}
