package terminal

// ShellInfo describes the actual executable selected by the native host. Paths
// are display-only; the renderer can select an ID but cannot supply a command.
type ShellInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
}

type processShellInfo interface{ ShellInfo() ShellInfo }
