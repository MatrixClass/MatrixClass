package filetransfer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultChunkSize = 256 * 1024
	TeacherPeerID    = "teacher"
)

var (
	ErrSessionNotFound  = errors.New("filetransfer: session not found")
	ErrNoTargets        = errors.New("filetransfer: no target student IDs provided")
	ErrInvalidPath      = errors.New("filetransfer: invalid file path")
	ErrChecksumMismatch = errors.New("filetransfer: sha256 checksum mismatch")
	ErrTransferRejected = errors.New("filetransfer: transfer rejected by peer")
)

type StudentMeta struct {
	ID        string
	StudentNo string
	Name      string
}

type Config struct {
	ArchiveDir      string
	ReceiveDir      string
	ChunkSize       int
	SessionLookup   func(peerID string) (core.Session, bool)
	StudentResolver func(studentID string) (StudentMeta, bool)
	OnProgress      func(task core.TransferTask)
	OnAutoOpen      func(path string)
}

type FileOffer struct {
	TaskID      string                 `json:"task_id"`
	Direction   core.TransferDirection `json:"direction"`
	FileName    string                 `json:"file_name"`
	RemoteDir   string                 `json:"remote_dir,omitempty"`
	RemotePath  string                 `json:"remote_path,omitempty"`
	StudentID   string                 `json:"student_id,omitempty"`
	StudentNo   string                 `json:"student_no,omitempty"`
	StudentName string                 `json:"student_name,omitempty"`
	TotalBytes  int64                  `json:"total_bytes"`
	SHA256      string                 `json:"sha256"`
	AutoOpen    bool                   `json:"auto_open,omitempty"`
}

type FileAck struct {
	TaskID       string `json:"task_id"`
	Accepted     bool   `json:"accepted"`
	ResumeOffset int64  `json:"resume_offset"`
	Error        string `json:"error,omitempty"`
}

type FileComplete struct {
	TaskID    string `json:"task_id"`
	DoneBytes int64  `json:"done_bytes"`
	SHA256    string `json:"sha256"`
	SavedPath string `json:"saved_path,omitempty"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

type CollectRequest struct {
	TaskID    string `json:"task_id"`
	RemoteDir string `json:"remote_dir"`
	StudentID string `json:"student_id,omitempty"`
}

type Service struct {
	mu              sync.RWMutex
	archiveDir      string
	receiveDir      string
	chunkSize       int
	sessions        map[string]core.Session
	students        map[string]StudentMeta
	sessionLookup   func(peerID string) (core.Session, bool)
	studentResolver func(studentID string) (StudentMeta, bool)
	onProgress      func(task core.TransferTask)
	onAutoOpen      func(path string)
	tasks           map[string]*core.TransferTask
	taskOrder       []string
	sentRecords     map[string]sentRecord
	nextTaskID      atomic.Uint64
}

type sentRecord struct {
	localPath string
	remoteDir string
	fileName  string
	sha256    string
	autoOpen  bool
}

var _ core.FileTransferService = (*Service)(nil)

func NewService(cfg Config) *Service {
	chunkSize := cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	archiveDir := cfg.ArchiveDir
	if archiveDir == "" {
		archiveDir = filepath.Join(os.TempDir(), "nexlab-submissions")
	}
	receiveDir := cfg.ReceiveDir
	if receiveDir == "" {
		receiveDir = filepath.Join(os.TempDir(), "nexlab-received")
	}
	return &Service{
		archiveDir:      archiveDir,
		receiveDir:      receiveDir,
		chunkSize:       chunkSize,
		sessions:        make(map[string]core.Session),
		students:        make(map[string]StudentMeta),
		sessionLookup:   cfg.SessionLookup,
		studentResolver: cfg.StudentResolver,
		onProgress:      cfg.OnProgress,
		onAutoOpen:      cfg.OnAutoOpen,
		tasks:           make(map[string]*core.TransferTask),
		sentRecords:     make(map[string]sentRecord),
	}
}

func (s *Service) RegisterSession(peerID string, sess core.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess == nil {
		delete(s.sessions, peerID)
		return
	}
	s.sessions[peerID] = sess
}

func (s *Service) SetTeacherSession(sess core.Session) {
	s.RegisterSession(TeacherPeerID, sess)
}

func (s *Service) RegisterStudentMeta(meta StudentMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.students[meta.ID] = meta
}

func (s *Service) SetProgressCallback(fn func(task core.TransferTask)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onProgress = fn
}

func (s *Service) SetArchiveDir(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	s.mu.Lock()
	s.archiveDir = dir
	s.mu.Unlock()
}

func (s *Service) ArchiveDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.archiveDir
}

func (s *Service) SetAutoOpenCallback(fn func(path string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onAutoOpen = fn
}

func (s *Service) getSession(peerID string) (core.Session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[peerID]
	lookup := s.sessionLookup
	s.mu.RUnlock()
	if ok && sess != nil {
		return sess, true
	}
	if lookup != nil {
		return lookup(peerID)
	}
	return nil, false
}

func (s *Service) resolveStudent(studentID string) StudentMeta {
	s.mu.RLock()
	meta, ok := s.students[studentID]
	resolver := s.studentResolver
	s.mu.RUnlock()
	if ok {
		return meta
	}
	if resolver != nil {
		if resolved, found := resolver(studentID); found {
			return resolved
		}
	}
	return StudentMeta{ID: studentID}
}

func (s *Service) newTask(direction core.TransferDirection, fileName, localPath, remotePath string, totalBytes int64, hash, studentID string, autoOpen bool) *core.TransferTask {
	id := fmt.Sprintf("ft-%d-%d", time.Now().UnixNano(), s.nextTaskID.Add(1))
	task := &core.TransferTask{
		ID:         id,
		Direction:  direction,
		FileName:   fileName,
		LocalPath:  localPath,
		RemotePath: remotePath,
		TotalBytes: totalBytes,
		SHA256:     hash,
		AutoOpen:   autoOpen,
		StudentID:  studentID,
		Status:     "running",
	}
	s.mu.Lock()
	s.tasks[id] = cloneTask(task)
	s.taskOrder = append(s.taskOrder, id)
	cb := s.onProgress
	s.mu.Unlock()
	if cb != nil {
		cb(*cloneTask(task))
	}
	return task
}

func (s *Service) updateTask(task *core.TransferTask, doneBytes int64, status, errMsg string) {
	s.mu.Lock()
	task.DoneBytes = doneBytes
	if status != "" {
		task.Status = status
	}
	task.Error = errMsg
	s.tasks[task.ID] = cloneTask(task)
	cb := s.onProgress
	s.mu.Unlock()
	if cb != nil {
		cb(*cloneTask(task))
	}
}

func (s *Service) taskDoneBytes(task *core.TransferTask) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return task.DoneBytes
}

func (s *Service) ListTasks() []*core.TransferTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*core.TransferTask, 0, len(s.taskOrder))
	for _, id := range s.taskOrder {
		if task, ok := s.tasks[id]; ok {
			out = append(out, cloneTask(task))
		}
	}
	return out
}

func cloneTask(t *core.TransferTask) *core.TransferTask {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func (s *Service) DistributeFile(ctx context.Context, localPath, remoteDir string, targetIDs []string, autoOpen bool) (*core.TransferTask, error) {
	if len(targetIDs) == 0 {
		return nil, ErrNoTargets
	}
	size, hash, err := inspectFileSHA256(localPath)
	if err != nil {
		return nil, err
	}

	fileName := filepath.Base(localPath)
	s.recordSentFile(sentRecord{
		localPath: localPath,
		remoteDir: remoteDir,
		fileName:  fileName,
		sha256:    hash,
		autoOpen:  autoOpen,
	})
	task := s.newTask(core.TransferSend, fileName, localPath, remoteDir, size, hash, strings.Join(targetIDs, ","), autoOpen)

	var (
		wg      sync.WaitGroup
		errMu   sync.Mutex
		firstEr error
		progMu  sync.Mutex
		perDone = make(map[string]int64, len(targetIDs))
	)
	for _, targetID := range targetIDs {
		perDone[targetID] = 0
	}

	for _, targetID := range targetIDs {
		wg.Add(1)
		go func(tid string) {
			defer wg.Done()
			sendErr := s.distributeToPeerWithRetry(ctx, task.ID, localPath, remoteDir, fileName, tid, size, hash, autoOpen, func(done int64) {
				progMu.Lock()
				perDone[tid] = done
				minDone := size
				for _, v := range perDone {
					if v < minDone {
						minDone = v
					}
				}
				progMu.Unlock()
				s.updateTask(task, minDone, "running", "")
			})
			if sendErr != nil {
				errMu.Lock()
				if firstEr == nil {
					firstEr = sendErr
				}
				errMu.Unlock()
			}
		}(targetID)
	}
	wg.Wait()

	if firstEr != nil {
		s.updateTask(task, s.taskDoneBytes(task), "failed", firstEr.Error())
		return cloneTask(task), firstEr
	}
	s.updateTask(task, size, "completed", "")
	return cloneTask(task), nil
}

func (s *Service) recordSentFile(rec sentRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sentRecords == nil {
		s.sentRecords = make(map[string]sentRecord)
	}
	if rec.sha256 != "" {
		s.sentRecords["sha:"+rec.sha256] = rec
	}
	s.sentRecords["name:"+rec.fileName+"|"+rec.remoteDir] = rec
	s.sentRecords["file:"+rec.fileName] = rec
}

func (s *Service) RetryDistribute(ctx context.Context, studentID, fileName, remotePath, sha256 string, autoOpen bool) (*core.TransferTask, error) {
	if studentID == "" {
		return nil, ErrNoTargets
	}
	s.mu.RLock()
	var rec sentRecord
	var ok bool
	if sha256 != "" {
		rec, ok = s.sentRecords["sha:"+sha256]
	}
	if !ok && fileName != "" {
		rec, ok = s.sentRecords["name:"+fileName+"|"+remotePath]
	}
	if !ok && fileName != "" {
		rec, ok = s.sentRecords["file:"+fileName]
	}
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("filetransfer: no source record for retry (%s)", fileName)
	}
	return s.DistributeFile(ctx, rec.localPath, rec.remoteDir, []string{studentID}, autoOpen || rec.autoOpen)
}

func (s *Service) distributeToPeerWithRetry(
	ctx context.Context,
	taskID, localPath, remoteDir, fileName, tid string,
	size int64,
	hash string,
	autoOpen bool,
	onBytes func(int64),
) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		sess, ok := s.getSession(tid)
		if !ok {
			lastErr = fmt.Errorf("%w: %s", ErrSessionNotFound, tid)
			continue
		}
		stream, err := sess.OpenStream(protocol.StreamFile)
		if err != nil {
			lastErr = err
			continue
		}
		offer := FileOffer{
			TaskID:     taskID,
			Direction:  core.TransferSend,
			FileName:   fileName,
			RemoteDir:  remoteDir,
			RemotePath: filepath.Join(remoteDir, fileName),
			StudentID:  tid,
			TotalBytes: size,
			SHA256:     hash,
			AutoOpen:   autoOpen,
		}
		_, sendErr := s.sendOverStream(ctx, stream, localPath, offer, onBytes)
		_ = stream.Close()
		if sendErr == nil {
			return nil
		}
		lastErr = sendErr
		if errors.Is(sendErr, ErrTransferRejected) || ctx.Err() != nil {
			return sendErr
		}
	}
	return lastErr
}

func (s *Service) SubmitHomework(ctx context.Context, studentID, localPath string) (*core.TransferTask, error) {
	sess, ok := s.getSession(TeacherPeerID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, TeacherPeerID)
	}
	size, hash, err := inspectFileSHA256(localPath)
	if err != nil {
		return nil, err
	}

	meta := s.resolveStudent(studentID)
	fileName := filepath.Base(localPath)
	task := s.newTask(core.TransferSubmit, fileName, localPath, "", size, hash, studentID, false)

	stream, err := sess.OpenStream(protocol.StreamFile)
	if err != nil {
		s.updateTask(task, 0, "failed", err.Error())
		return cloneTask(task), err
	}
	defer stream.Close()

	offer := FileOffer{
		TaskID:      task.ID,
		Direction:   core.TransferSubmit,
		FileName:    fileName,
		StudentID:   studentID,
		StudentNo:   meta.StudentNo,
		StudentName: meta.Name,
		TotalBytes:  size,
		SHA256:      hash,
	}
	complete, err := s.sendOverStream(ctx, stream, localPath, offer, func(done int64) {
		s.updateTask(task, done, "running", "")
	})
	if err != nil {
		s.updateTask(task, s.taskDoneBytes(task), "failed", err.Error())
		return cloneTask(task), err
	}
	s.mu.Lock()
	task.RemotePath = complete.SavedPath
	s.mu.Unlock()
	s.updateTask(task, size, "completed", "")
	return cloneTask(task), nil
}

func (s *Service) CollectFiles(ctx context.Context, remoteDir, saveDir string, targetIDs []string) ([]*core.TransferTask, error) {
	if len(targetIDs) == 0 {
		return nil, ErrNoTargets
	}
	if saveDir == "" {
		saveDir = s.archiveDir
	}
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		return nil, err
	}

	var (
		wg      sync.WaitGroup
		resMu   sync.Mutex
		tasks   []*core.TransferTask
		firstEr error
	)

	for _, studentID := range targetIDs {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			sess, ok := s.getSession(sid)
			if !ok {
				resMu.Lock()
				if firstEr == nil {
					firstEr = fmt.Errorf("%w: %s", ErrSessionNotFound, sid)
				}
				resMu.Unlock()
				return
			}
			stream, err := sess.OpenStream(protocol.StreamFile)
			if err != nil {
				resMu.Lock()
				if firstEr == nil {
					firstEr = err
				}
				resMu.Unlock()
				return
			}
			defer stream.Close()

			reqID := fmt.Sprintf("collect-%d-%d", time.Now().UnixNano(), s.nextTaskID.Add(1))
			reqPayload, err := json.Marshal(CollectRequest{
				TaskID:    reqID,
				RemoteDir: remoteDir,
				StudentID: sid,
			})
			if err != nil {
				resMu.Lock()
				if firstEr == nil {
					firstEr = err
				}
				resMu.Unlock()
				return
			}
			if err := protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgCollectReq, reqPayload); err != nil {
				resMu.Lock()
				if firstEr == nil {
					firstEr = err
				}
				resMu.Unlock()
				return
			}

			meta := s.resolveStudent(sid)
			for {
				if err := ctx.Err(); err != nil {
					resMu.Lock()
					if firstEr == nil {
						firstEr = err
					}
					resMu.Unlock()
					return
				}
				_, msgType, payload, err := protocol.ReadFrame(stream)
				if err != nil {
					resMu.Lock()
					if firstEr == nil {
						firstEr = err
					}
					resMu.Unlock()
					return
				}
				if msgType == protocol.MsgFileComplete {
					var doneMsg FileComplete
					if err := json.Unmarshal(payload, &doneMsg); err != nil {
						resMu.Lock()
						if firstEr == nil {
							firstEr = err
						}
						resMu.Unlock()
						return
					}
					if !doneMsg.Success {
						resMu.Lock()
						if firstEr == nil {
							firstEr = errors.New(doneMsg.Error)
						}
						resMu.Unlock()
					}
					return
				}
				if msgType != protocol.MsgFileOffer {
					resMu.Lock()
					if firstEr == nil {
						firstEr = fmt.Errorf("filetransfer: unexpected frame type %d", msgType)
					}
					resMu.Unlock()
					return
				}
				var offer FileOffer
				if err := json.Unmarshal(payload, &offer); err != nil {
					resMu.Lock()
					if firstEr == nil {
						firstEr = err
					}
					resMu.Unlock()
					return
				}
				if offer.StudentID == "" {
					offer.StudentID = sid
				}
				if offer.StudentNo == "" {
					offer.StudentNo = meta.StudentNo
				}
				if offer.StudentName == "" {
					offer.StudentName = meta.Name
				}
				folder := formatStudentFolder(offer.StudentNo, offer.StudentName, offer.StudentID)
				destPath := filepath.Join(saveDir, folder, sanitizeFileName(offer.FileName))
				task, recvErr := s.receiveOffer(ctx, stream, offer, destPath, core.TransferCollect)
				if recvErr != nil {
					resMu.Lock()
					if firstEr == nil {
						firstEr = recvErr
					}
					resMu.Unlock()
					return
				}
				resMu.Lock()
				tasks = append(tasks, task)
				resMu.Unlock()
			}
		}(studentID)
	}
	wg.Wait()

	sort.Slice(tasks, func(left, right int) bool {
		if tasks[left].StudentID != tasks[right].StudentID {
			return tasks[left].StudentID < tasks[right].StudentID
		}
		return tasks[left].FileName < tasks[right].FileName
	})
	return tasks, firstEr
}

func (s *Service) HandleStream(ctx context.Context, stream io.ReadWriteCloser) error {
	defer stream.Close()

	_, msgType, payload, err := protocol.ReadFrame(stream)
	if err != nil {
		return err
	}

	switch msgType {
	case protocol.MsgFileOffer:
		var offer FileOffer
		if err := json.Unmarshal(payload, &offer); err != nil {
			return err
		}
		destPath := s.resolveReceivePath(offer)
		dir := offer.Direction
		if dir == "" {
			dir = core.TransferSend
		}
		_, err := s.receiveOffer(ctx, stream, offer, destPath, dir)
		return err

	case protocol.MsgCollectReq:
		var req CollectRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return err
		}
		return s.handleCollectRequest(ctx, stream, req)

	default:
		return fmt.Errorf("filetransfer: unexpected initial frame type %d", msgType)
	}
}

func (s *Service) handleCollectRequest(ctx context.Context, stream io.ReadWriteCloser, req CollectRequest) error {
	targetDir := strings.TrimSpace(req.RemoteDir)
	if targetDir == "" {
		targetDir = s.receiveDir
	}
	files, err := listCollectableFiles(targetDir)
	if errors.Is(err, os.ErrNotExist) && s.receiveDir != "" && targetDir != s.receiveDir {
		_ = os.MkdirAll(s.receiveDir, 0o755)
		targetDir = s.receiveDir
		files, err = listCollectableFiles(targetDir)
	}
	if err != nil {
		errPayload, _ := json.Marshal(FileComplete{
			TaskID:  req.TaskID,
			Success: false,
			Error:   err.Error(),
		})
		_ = protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileComplete, errPayload)
		return err
	}

	meta := s.resolveStudent(req.StudentID)
	for _, filePath := range files {
		size, hash, err := inspectFileSHA256(filePath)
		if err != nil {
			errPayload, _ := json.Marshal(FileComplete{
				TaskID:  req.TaskID,
				Success: false,
				Error:   err.Error(),
			})
			_ = protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileComplete, errPayload)
			return err
		}
		fileName := filepath.Base(filePath)
		task := s.newTask(core.TransferCollect, fileName, filePath, req.RemoteDir, size, hash, req.StudentID, false)
		offer := FileOffer{
			TaskID:      task.ID,
			Direction:   core.TransferCollect,
			FileName:    fileName,
			RemoteDir:   req.RemoteDir,
			RemotePath:  filePath,
			StudentID:   req.StudentID,
			StudentNo:   meta.StudentNo,
			StudentName: meta.Name,
			TotalBytes:  size,
			SHA256:      hash,
		}
		if _, err := s.sendOverStream(ctx, stream, filePath, offer, func(done int64) {
			s.updateTask(task, done, "running", "")
		}); err != nil {
			s.updateTask(task, s.taskDoneBytes(task), "failed", err.Error())
			return err
		}
		s.updateTask(task, size, "completed", "")
	}

	donePayload, err := json.Marshal(FileComplete{
		TaskID:  req.TaskID,
		Success: true,
	})
	if err != nil {
		return err
	}
	return protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileComplete, donePayload)
}

func (s *Service) sendOverStream(ctx context.Context, stream io.ReadWriter, localPath string, offer FileOffer, onBytes func(int64)) (FileComplete, error) {
	offerBytes, err := json.Marshal(offer)
	if err != nil {
		return FileComplete{}, err
	}
	if err := protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileOffer, offerBytes); err != nil {
		return FileComplete{}, err
	}

	_, msgType, ackPayload, err := protocol.ReadFrame(stream)
	if err != nil {
		return FileComplete{}, err
	}
	if msgType != protocol.MsgFileAck {
		return FileComplete{}, fmt.Errorf("filetransfer: expected MsgFileAck, got %d", msgType)
	}
	var ack FileAck
	if err := json.Unmarshal(ackPayload, &ack); err != nil {
		return FileComplete{}, err
	}
	if !ack.Accepted {
		if ack.Error != "" {
			return FileComplete{}, fmt.Errorf("%w: %s", ErrTransferRejected, ack.Error)
		}
		return FileComplete{}, ErrTransferRejected
	}
	offset := ack.ResumeOffset
	if offset < 0 || offset > offer.TotalBytes {
		offset = 0
	}
	if onBytes != nil {
		onBytes(offset)
	}

	if offset < offer.TotalBytes {
		f, err := os.Open(localPath)
		if err != nil {
			return FileComplete{}, err
		}
		defer f.Close()

		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				return FileComplete{}, err
			}
		}

		buf := make([]byte, 8+s.chunkSize)
		for offset < offer.TotalBytes {
			if err := ctx.Err(); err != nil {
				return FileComplete{}, err
			}
			n, readErr := f.Read(buf[8:])
			if n > 0 {
				binary.BigEndian.PutUint64(buf[:8], uint64(offset))
				if err := protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileChunk, buf[:8+n]); err != nil {
					return FileComplete{}, err
				}
				offset += int64(n)
				if onBytes != nil {
					onBytes(offset)
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return FileComplete{}, readErr
			}
		}
	}

	sendDone, err := json.Marshal(FileComplete{
		TaskID:    offer.TaskID,
		DoneBytes: offset,
		SHA256:    offer.SHA256,
		Success:   true,
	})
	if err != nil {
		return FileComplete{}, err
	}
	if err := protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileComplete, sendDone); err != nil {
		return FileComplete{}, err
	}

	_, respType, respPayload, err := protocol.ReadFrame(stream)
	if err != nil {
		return FileComplete{}, err
	}
	if respType != protocol.MsgFileComplete {
		return FileComplete{}, fmt.Errorf("filetransfer: expected MsgFileComplete, got %d", respType)
	}
	var final FileComplete
	if err := json.Unmarshal(respPayload, &final); err != nil {
		return FileComplete{}, err
	}
	if !final.Success {
		if final.Error != "" {
			return final, errors.New(final.Error)
		}
		return final, ErrChecksumMismatch
	}
	return final, nil
}

func (s *Service) receiveOffer(ctx context.Context, stream io.ReadWriter, offer FileOffer, destPath string, dir core.TransferDirection) (*core.TransferTask, error) {
	task := s.newTask(dir, offer.FileName, destPath, offer.RemotePath, offer.TotalBytes, offer.SHA256, offer.StudentID, offer.AutoOpen)

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		s.sendAckError(stream, offer.TaskID, err)
		s.updateTask(task, 0, "failed", err.Error())
		return cloneTask(task), err
	}

	// If complete file already exists and matches SHA-256, skip transfer via full resume offset.
	if offer.SHA256 != "" {
		if existingSize, existingHash, err := inspectFileSHA256(destPath); err == nil && existingSize == offer.TotalBytes && strings.EqualFold(existingHash, offer.SHA256) {
			if err := s.sendAckResume(stream, offer.TaskID, offer.TotalBytes); err != nil {
				s.updateTask(task, 0, "failed", err.Error())
				return cloneTask(task), err
			}
			_, _, _, err := protocol.ReadFrame(stream)
			if err != nil {
				s.updateTask(task, 0, "failed", err.Error())
				return cloneTask(task), err
			}
			s.sendFinalComplete(stream, offer.TaskID, offer.TotalBytes, existingHash, destPath, true, "")
			s.updateTask(task, offer.TotalBytes, "completed", "")
			s.triggerAutoOpen(offer.AutoOpen, destPath)
			return cloneTask(task), nil
		}
	}

	partPath := destPath + ".part"
	partMetaPath := partPath + ".sha256"
	var resumeOffset int64
	if info, err := os.Stat(partPath); err == nil && info.Mode().IsRegular() {
		if info.Size() > 0 && info.Size() <= offer.TotalBytes {
			canResume := true
			if offer.SHA256 != "" {
				if metaBytes, mErr := os.ReadFile(partMetaPath); mErr == nil {
					canResume = strings.EqualFold(strings.TrimSpace(string(metaBytes)), offer.SHA256)
				}
			}
			if canResume {
				resumeOffset = info.Size()
			}
		}
	}
	if offer.SHA256 != "" {
		_ = os.WriteFile(partMetaPath, []byte(offer.SHA256), 0o644)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resumeOffset == 0 {
		flags |= os.O_TRUNC
	}
	outFile, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		s.sendAckError(stream, offer.TaskID, err)
		s.updateTask(task, 0, "failed", err.Error())
		return cloneTask(task), err
	}
	if resumeOffset > 0 {
		if _, err := outFile.Seek(resumeOffset, io.SeekStart); err != nil {
			outFile.Close()
			s.sendAckError(stream, offer.TaskID, err)
			s.updateTask(task, 0, "failed", err.Error())
			return cloneTask(task), err
		}
	}
	bw := bufio.NewWriterSize(outFile, 512*1024)

	if err := s.sendAckResume(stream, offer.TaskID, resumeOffset); err != nil {
		outFile.Close()
		s.updateTask(task, 0, "failed", err.Error())
		return cloneTask(task), err
	}
	doneBytes := resumeOffset
	s.updateTask(task, doneBytes, "running", "")

	for {
		if err := ctx.Err(); err != nil {
			outFile.Close()
			s.updateTask(task, doneBytes, "failed", err.Error())
			return cloneTask(task), err
		}
		_, msgType, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			outFile.Close()
			s.updateTask(task, doneBytes, "failed", err.Error())
			return cloneTask(task), err
		}
		switch msgType {
		case protocol.MsgFileChunk:
			if len(payload) < 8 {
				outFile.Close()
				err := errors.New("filetransfer: malformed chunk frame")
				s.updateTask(task, doneBytes, "failed", err.Error())
				return cloneTask(task), err
			}
			chunkOffset := int64(binary.BigEndian.Uint64(payload[:8]))
			data := payload[8:]
			if chunkOffset != doneBytes {
				if err := bw.Flush(); err != nil {
					outFile.Close()
					s.updateTask(task, doneBytes, "failed", err.Error())
					return cloneTask(task), err
				}
				if _, err := outFile.Seek(chunkOffset, io.SeekStart); err != nil {
					outFile.Close()
					s.updateTask(task, doneBytes, "failed", err.Error())
					return cloneTask(task), err
				}
				doneBytes = chunkOffset
			}
			if _, err := bw.Write(data); err != nil {
				outFile.Close()
				s.updateTask(task, doneBytes, "failed", err.Error())
				return cloneTask(task), err
			}
			doneBytes += int64(len(data))
			s.updateTask(task, doneBytes, "running", "")

		case protocol.MsgFileComplete:
			if err := bw.Flush(); err != nil {
				outFile.Close()
				s.sendFinalComplete(stream, offer.TaskID, doneBytes, "", "", false, err.Error())
				s.updateTask(task, doneBytes, "failed", err.Error())
				return cloneTask(task), err
			}
			if err := outFile.Close(); err != nil {
				s.sendFinalComplete(stream, offer.TaskID, doneBytes, "", "", false, err.Error())
				s.updateTask(task, doneBytes, "failed", err.Error())
				return cloneTask(task), err
			}
			actualSize, actualHash, err := inspectFileSHA256(partPath)
			if err != nil {
				s.sendFinalComplete(stream, offer.TaskID, doneBytes, "", "", false, err.Error())
				s.updateTask(task, doneBytes, "failed", err.Error())
				return cloneTask(task), err
			}
			if offer.SHA256 != "" && !strings.EqualFold(actualHash, offer.SHA256) {
				_ = os.Remove(partPath)
				_ = os.Remove(partMetaPath)
				s.sendFinalComplete(stream, offer.TaskID, actualSize, actualHash, "", false, ErrChecksumMismatch.Error())
				s.updateTask(task, actualSize, "failed", ErrChecksumMismatch.Error())
				return cloneTask(task), ErrChecksumMismatch
			}
			if err := replaceFile(partPath, destPath); err != nil {
				s.sendFinalComplete(stream, offer.TaskID, actualSize, actualHash, "", false, err.Error())
				s.updateTask(task, actualSize, "failed", err.Error())
				return cloneTask(task), err
			}
			_ = os.Remove(partMetaPath)
			s.sendFinalComplete(stream, offer.TaskID, actualSize, actualHash, destPath, true, "")
			s.updateTask(task, actualSize, "completed", "")
			s.triggerAutoOpen(offer.AutoOpen, destPath)
			return cloneTask(task), nil

		default:
			outFile.Close()
			err := fmt.Errorf("filetransfer: unexpected frame type %d", msgType)
			s.updateTask(task, doneBytes, "failed", err.Error())
			return cloneTask(task), err
		}
	}
}

func (s *Service) sendAckResume(stream io.Writer, taskID string, resumeOffset int64) error {
	payload, err := json.Marshal(FileAck{
		TaskID:       taskID,
		Accepted:     true,
		ResumeOffset: resumeOffset,
	})
	if err != nil {
		return err
	}
	return protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileAck, payload)
}

func (s *Service) sendAckError(stream io.Writer, taskID string, err error) {
	payload, _ := json.Marshal(FileAck{
		TaskID:   taskID,
		Accepted: false,
		Error:    err.Error(),
	})
	_ = protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileAck, payload)
}

func (s *Service) sendFinalComplete(stream io.Writer, taskID string, doneBytes int64, hash, savedPath string, ok bool, errMsg string) {
	payload, _ := json.Marshal(FileComplete{
		TaskID:    taskID,
		DoneBytes: doneBytes,
		SHA256:    hash,
		SavedPath: savedPath,
		Success:   ok,
		Error:     errMsg,
	})
	_ = protocol.WriteFrame(stream, protocol.StreamFile, protocol.MsgFileComplete, payload)
}

func (s *Service) triggerAutoOpen(autoOpen bool, path string) {
	if !autoOpen {
		return
	}
	s.mu.RLock()
	cb := s.onAutoOpen
	s.mu.RUnlock()
	if cb != nil {
		cb(path)
	}
}

func (s *Service) resolveReceivePath(offer FileOffer) string {
	fileName := sanitizeFileName(offer.FileName)
	if offer.Direction == core.TransferSubmit {
		folder := formatStudentFolder(offer.StudentNo, offer.StudentName, offer.StudentID)
		return filepath.Join(s.ArchiveDir(), folder, fileName)
	}
	if strings.TrimSpace(offer.RemoteDir) != "" {
		if err := os.MkdirAll(offer.RemoteDir, 0o755); err == nil {
			return filepath.Join(offer.RemoteDir, fileName)
		}
	}
	return filepath.Join(s.receiveDir, fileName)
}

func replaceFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}

func inspectFileSHA256(path string) (int64, string, error) {
	if path == "" {
		return 0, "", ErrInvalidPath
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if info.IsDir() {
		return 0, "", ErrInvalidPath
	}
	hasher := sha256.New()
	n, err := io.Copy(hasher, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(hasher.Sum(nil)), nil
}

func listCollectableFiles(remoteDir string) ([]string, error) {
	info, err := os.Stat(remoteDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{remoteDir}, nil
	}
	entries, err := os.ReadDir(remoteDir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		files = append(files, filepath.Join(remoteDir, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func sanitizeFileName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	base = strings.ReplaceAll(base, "..", "_")
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "unnamed.bin"
	}
	return base
}

func sanitizeSegment(seg string) string {
	cleaned := strings.TrimSpace(seg)
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return replacer.Replace(cleaned)
}

func formatStudentFolder(studentNo, name, studentID string) string {
	no := sanitizeSegment(studentNo)
	nm := sanitizeSegment(name)
	id := sanitizeSegment(studentID)
	switch {
	case no != "" && nm != "":
		return no + "_" + nm
	case no != "":
		return no
	case id != "" && nm != "":
		return id + "_" + nm
	case nm != "":
		return nm
	case id != "":
		return id
	default:
		return "unknown_student"
	}
}
