package remoteexec

import "time"

type Shell string

const (
	ShellCMD        Shell = "cmd"
	ShellPowerShell Shell = "powershell"
	ShellSH         Shell = "sh"
	ShellBash       Shell = "bash"
)

type Application struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Executable string `json:"executable"`
	Arguments  string `json:"arguments,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	Source     string `json:"source"`
}

type AppLaunchRequest struct {
	RequestID  string `json:"request_id"`
	AppID      string `json:"app_id,omitempty"`
	Executable string `json:"executable,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	Wait       bool   `json:"wait"`
}

type CommandRequest struct {
	RequestID  string `json:"request_id"`
	Shell      Shell  `json:"shell"`
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir,omitempty"`
	TimeoutSec int    `json:"timeout_sec"`
}

type CommandOutput struct {
	RequestID string `json:"request_id"`
	Stream    string `json:"stream"`
	Data      string `json:"data"`
}

type CommandResult struct {
	RequestID  string    `json:"request_id"`
	ExitCode   int       `json:"exit_code"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}

type CommandTask struct {
	ID     string
	Result <-chan CommandResult
	Cancel func()
}
