package remoteexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultTimeout    = 60 * time.Second
	MaxTimeout        = 10 * time.Minute
	DefaultOutputSize = 1 << 20
)

var (
	ErrInvalidCommand    = errors.New("remoteexec: command is empty")
	ErrInvalidShell      = errors.New("remoteexec: unsupported shell")
	ErrInvalidWorkDir    = errors.New("remoteexec: working directory does not exist")
	ErrInvalidExecutable = errors.New("remoteexec: executable does not exist")
	ErrTaskNotFound      = errors.New("remoteexec: task not found")
)

type Service struct {
	mu            sync.Mutex
	tasks         map[string]context.CancelFunc
	nextID        atomic.Uint64
	maxOutput     int
	maxConcurrent int
	active        int
}

func NewService() *Service {
	return &Service{tasks: make(map[string]context.CancelFunc), maxOutput: DefaultOutputSize, maxConcurrent: 2}
}

func (s *Service) SetMaxOutputBytes(size int) {
	if size <= 0 {
		return
	}
	s.mu.Lock()
	s.maxOutput = size
	s.mu.Unlock()
}

func (s *Service) StartCommand(parent context.Context, req CommandRequest, onOutput func(CommandOutput)) (CommandTask, error) {
	if strings.TrimSpace(req.Command) == "" {
		return CommandTask{}, ErrInvalidCommand
	}
	if !supportedShell(req.Shell) {
		return CommandTask{}, ErrInvalidShell
	}
	if req.WorkingDir != "" {
		info, err := os.Stat(req.WorkingDir)
		if err != nil || !info.IsDir() {
			return CommandTask{}, ErrInvalidWorkDir
		}
	}
	timeout := DefaultTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	s.mu.Lock()
	if s.active >= s.maxConcurrent {
		s.mu.Unlock()
		return CommandTask{}, errors.New("remoteexec: too many active commands")
	}
	s.active++
	maxOutput := s.maxOutput
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(parent, timeout)
	id := fmt.Sprintf("remote-%d", s.nextID.Add(1))
	s.mu.Lock()
	s.tasks[id] = cancel
	s.mu.Unlock()
	resultCh := make(chan CommandResult, 1)
	go func() {
		defer close(resultCh)
		defer func() {
			s.mu.Lock()
			delete(s.tasks, id)
			s.active--
			s.mu.Unlock()
		}()
		result := executeCommand(ctx, req, maxOutput, onOutput)
		result.RequestID = req.RequestID
		resultCh <- result
	}()
	return CommandTask{ID: id, Result: resultCh, Cancel: cancel}, nil
}

func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	cancel, ok := s.tasks[id]
	s.mu.Unlock()
	if !ok {
		return ErrTaskNotFound
	}
	cancel()
	return nil
}

func (s *Service) LaunchApp(ctx context.Context, req AppLaunchRequest, apps []Application) CommandResult {
	started := time.Now()
	result := CommandResult{RequestID: req.RequestID, StartedAt: started, Status: "failed", ExitCode: -1}
	executable := strings.TrimSpace(req.Executable)
	if executable == "" && req.AppID != "" {
		for _, app := range apps {
			if app.ID == req.AppID {
				executable = app.Executable
				if req.Arguments == "" {
					req.Arguments = app.Arguments
				}
				if req.WorkingDir == "" {
					req.WorkingDir = app.WorkingDir
				}
				break
			}
		}
	}
	if executable == "" {
		result.Error = ErrInvalidExecutable.Error()
		result.FinishedAt = time.Now()
		return result
	}
	if !isCommandName(executable) {
		// A directory is only acceptable when it is a macOS .app bundle, which
		// the darwin launcher opens through LaunchServices.
		if info, err := os.Stat(executable); err != nil || (info.IsDir() && (runtime.GOOS != "darwin" || !isAppBundlePath(executable))) {
			result.Error = ErrInvalidExecutable.Error()
			result.FinishedAt = time.Now()
			return result
		}
	}
	if req.WorkingDir != "" {
		if info, err := os.Stat(req.WorkingDir); err != nil || !info.IsDir() {
			result.Error = ErrInvalidWorkDir.Error()
			result.FinishedAt = time.Now()
			return result
		}
	}
	result = launchApplication(ctx, req, executable)
	result.RequestID = req.RequestID
	result.StartedAt = started
	result.FinishedAt = time.Now()
	return result
}

func isCommandName(path string) bool {
	return !strings.ContainsAny(path, `/\\`) && filepath.Ext(path) != ""
}

func applicationID(executable string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(executable))))
	return hex.EncodeToString(hash[:8])
}
