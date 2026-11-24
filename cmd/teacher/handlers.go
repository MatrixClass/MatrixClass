package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/filetransfer"
	"nexlab/internal/protocol"
)

func (rt *Runtime) wireHandlers() {
	rt.Classroom.OnChange(func() {
		rt.App.Invalidate()
	})
	rt.Exam.OnChange(func() {
		rt.App.Invalidate()
	})
	rt.Transport.OnSession(func(sess core.Session) {
		rt.Files.RegisterSession(sess.ID(), sess)
		rt.goLifecycle(func(ctx context.Context) { rt.acceptFileStreams(ctx, sess) })
	})
	rt.Transport.OnEnvelope(func(sess core.Session, env protocol.Envelope) {
		senderID := env.SenderID
		if senderID == "" {
			senderID = sess.ID()
		}
		// Once a session has registered, its identity is fixed: ignore a
		// different SenderID claimed in later envelopes.
		if bound, ok := rt.studentForSession(sess.ID()); ok {
			senderID = bound
		}
		switch env.Type {
		case protocol.MsgPong:
			var sentTs int64
			if err := json.Unmarshal(env.Payload, &sentTs); err == nil && sentTs > 0 {
				rtt := int(time.Now().UnixMilli() - sentTs)
				if rtt < 0 {
					rtt = 0
				}
				_ = rt.Classroom.UpdateTelemetry(senderID, rtt, rt.Transport.Stats().PacketLossRate)
			}
		case protocol.MsgStudentRegister:
			var st core.StudentInfo
			if err := json.Unmarshal(env.Payload, &st); err == nil {
				if st.ID == "" {
					st.ID = sess.ID()
				}
				if !rt.bindSessionStudent(sess, st.ID) {
					log.Printf("reject register: student %s already bound to another live session (from %s)", st.ID, sess.RemoteAddr())
					return
				}
				rt.Files.RegisterSession(st.ID, sess)
				rt.Files.RegisterStudentMeta(filetransfer.StudentMeta{
					ID:        st.ID,
					StudentNo: st.StudentNo,
				})
				if existing, ok := rt.Classroom.GetStudent(st.ID); ok {
					st.Seat = existing.Seat
					st.GroupID = existing.GroupID
					st.CheckedIn = existing.CheckedIn
				} else if st.Seat.Row == 0 && st.Seat.Col == 0 {
					st.Seat = rt.nextFreeSeat()
				}
				st.IP = sess.RemoteAddr()
				st.Status = core.StatusOnline
				st.LastSeen = time.Now()
				rt.Classroom.UpsertStudent(st)
				if rt.Store != nil {
					_ = rt.Store.UpsertStudent(st)
				}
				if rt.App.Broadcasting() || rt.App.TheatrePlaying() {
					rt.forceKeyFrame.Store(true)
					if bEnv, err := protocol.NewEnvelope(protocol.MsgBroadcastStart, rt.teacherID, rt.App.CurrentBroadcastConfig()); err == nil {
						bEnv.Channel = rt.channel
						bEnv.Timestamp = time.Now().UnixMilli()
						_ = sess.SendEnvelope(bEnv)
					}
				}
				if locked, lockMsg := rt.App.ScreenLockedFor(st.ID); locked {
					if lEnv, err := protocol.NewEnvelope(protocol.MsgScreenLock, rt.teacherID, map[string]any{
						"locked":  true,
						"message": lockMsg,
					}); err == nil {
						lEnv.Channel = rt.channel
						lEnv.Timestamp = time.Now().UnixMilli()
						_ = sess.SendEnvelope(lEnv)
					}
				}
				if rt.App.Monitoring() {
					rt.resetStudentDecoder(st.ID + ":monitor")
					if mEnv, err := protocol.NewEnvelope(protocol.MsgMonitorControl, rt.teacherID, struct {
						Active bool `json:"active"`
						core.MonitorConfig
					}{
						Active: true,
						MonitorConfig: core.MonitorConfig{
							Width:   320,
							Height:  180,
							FPS:     int(rt.App.MonitorFPS()),
							Quality: int(rt.App.MonitorQual()),
							Reset:   true,
						},
					}); err == nil {
						mEnv.Channel = rt.channel
						mEnv.Timestamp = time.Now().UnixMilli()
						_ = sess.SendEnvelope(mEnv)
					}
				} else {
					rt.resetStudentDecoder(st.ID + ":monitor")
				}
			}
		case protocol.MsgCheckInSubmit:
			var req struct {
				StudentID string `json:"student_id"`
				ClassName string `json:"class_name"`
				Name      string `json:"name"`
				StudentNo string `json:"student_no"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				if req.StudentID == "" {
					req.StudentID = senderID
				}
				rt.Classroom.SubmitCheckIn(req.StudentID, req.Name, req.StudentNo)
				if rt.Store != nil {
					if st, ok := rt.Classroom.GetStudent(req.StudentID); ok {
						st.ClassName = req.ClassName
						rt.Classroom.UpsertStudent(st)
						_ = rt.Store.UpsertStudent(st)
					}
				}
			}
		case protocol.MsgHandRaise:
			var req struct {
				StudentID string `json:"student_id"`
				Raised    bool   `json:"raised"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				if req.StudentID == "" {
					req.StudentID = senderID
				}
				rt.Classroom.RaiseHand(req.StudentID, req.Raised)
			}
		case protocol.MsgChat:
			var msg core.ChatMessage
			if err := json.Unmarshal(env.Payload, &msg); err == nil {
				if msg.CreatedAt.IsZero() {
					msg.CreatedAt = time.Now()
				}
				rt.Classroom.AddChat(msg)
				if rt.Store != nil {
					_ = rt.Store.SaveChat(msg)
				}
			}
		case protocol.MsgExamAnswer:
			var sub core.ExamSubmission
			if err := json.Unmarshal(env.Payload, &sub); err == nil {
				studentID, ok := rt.studentForSession(sess.ID())
				if !ok {
					log.Printf("drop exam answer from unregistered session %s", sess.ID())
					break
				}
				sub.StudentID = studentID
				// Persist the server-graded result, not the student's
				// self-reported score.
				graded := rt.Exam.SubmitAnswer(sub)
				if rt.Store != nil {
					_ = rt.Store.SaveExamSubmission(graded)
				}
			}
		case protocol.MsgFileRetryReq:
			var req struct {
				StudentID  string `json:"student_id"`
				FileName   string `json:"file_name"`
				RemotePath string `json:"remote_path"`
				SHA256     string `json:"sha256"`
				AutoOpen   bool   `json:"auto_open"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				// Only the requesting student may receive the retry; the
				// payload's student_id is not trusted.
				targetID, ok := rt.studentForSession(sess.ID())
				if !ok {
					targetID = senderID
				}
				if targetID != "" && rt.Files != nil {
					rt.goLifecycle(func(ctx context.Context) {
						_, _ = rt.Files.RetryDistribute(ctx, targetID, req.FileName, req.RemotePath, req.SHA256, req.AutoOpen)
					})
				}
			}
		case protocol.MsgRemoteAppListResp, protocol.MsgRemoteAppResult, protocol.MsgRemoteCommandOutput, protocol.MsgRemoteCommandResult:
			rt.App.HandleRemoteEnvelope(env)
		case protocol.MsgVideoFrame:
			var pkt videoStreamPacket
			if err := json.Unmarshal(env.Payload, &pkt); err == nil && len(pkt.Data) > 0 {
				rt.Classroom.TouchStudent(senderID)
				kind := pkt.Kind
				if kind == "" {
					kind = "monitor"
				}
				decKey := senderID + ":" + kind
				frame, err := rt.decodeStudentFrame(decKey, pkt.Data)
				if err != nil {
					rt.resetStudentDecoder(decKey)
					if kind == "monitor" && rt.App.Monitoring() && sess != nil {
						if mEnv, mErr := protocol.NewEnvelope(protocol.MsgMonitorControl, rt.teacherID, struct {
							Active bool `json:"active"`
							core.MonitorConfig
						}{
							Active: true,
							MonitorConfig: core.MonitorConfig{
								Width:   320,
								Height:  180,
								FPS:     int(rt.App.MonitorFPS()),
								Quality: int(rt.App.MonitorQual()),
								Reset:   true,
							},
						}); mErr == nil {
							mEnv.Channel = rt.channel
							mEnv.Timestamp = time.Now().UnixMilli()
							_ = sess.SendEnvelope(mEnv)
						}
					}
				}
				if err == nil && frame != nil {
					switch kind {
					case "remote":
						rt.App.UpdateRemoteFrame(senderID, frame)
					case "demo":
						rt.App.UpdateDemoFrame(senderID, frame)
						if rt.App.StudentDemoID() == senderID {
							_ = rt.Transport.BroadcastMulticast(pkt.Data)
						}
					default:
						rt.App.UpdateMonitorFrame(senderID, frame)
					}
				}
			}
		}
		rt.App.Invalidate()
	})
}

func (rt *Runtime) nextFreeSeat() core.SeatPos {
	used := make(map[core.SeatPos]bool)
	for _, st := range rt.Classroom.ListStudents() {
		used[st.Seat] = true
	}
	for idx := 0; idx < 256; idx++ {
		pos := core.SeatPos{Row: idx / 4, Col: idx % 4}
		if !used[pos] {
			return pos
		}
	}
	return core.SeatPos{}
}

func (rt *Runtime) acceptFileStreams(ctx context.Context, sess core.Session) {
	type streamAccepter interface {
		AcceptStream() (protocol.StreamKind, io.ReadWriteCloser, error)
	}
	ac, ok := sess.(streamAccepter)
	if !ok {
		return
	}
	for {
		kind, stream, err := ac.AcceptStream()
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			_ = stream.Close()
			return
		default:
		}
		if kind == protocol.StreamFile {
			rt.goLifecycle(func(taskCtx context.Context) {
				_ = rt.Files.HandleStreamFrom(taskCtx, sess.ID(), stream)
				rt.App.Invalidate()
			})
		} else {
			_ = stream.Close()
		}
	}
}

// bindSessionStudent records that sess registered as studentID. It refuses
// when studentID is already bound to a different session that is still live
// and comes from another host (an impersonation attempt); a reconnect from
// the same host replaces the stale binding.
func (rt *Runtime) bindSessionStudent(sess core.Session, studentID string) bool {
	rt.sessMu.Lock()
	defer rt.sessMu.Unlock()
	if rt.sessionStudents == nil {
		rt.sessionStudents = make(map[string]string)
	}
	for otherSess, otherStudent := range rt.sessionStudents {
		if otherSess == sess.ID() {
			continue
		}
		other := rt.liveSession(otherSess)
		if other == nil {
			// Prune bindings of sessions that have gone away.
			delete(rt.sessionStudents, otherSess)
			continue
		}
		if otherStudent != studentID {
			continue
		}
		if hostOf(other.RemoteAddr()) != hostOf(sess.RemoteAddr()) {
			return false
		}
		delete(rt.sessionStudents, otherSess)
	}
	rt.sessionStudents[sess.ID()] = studentID
	return true
}

func (rt *Runtime) studentForSession(sessID string) (string, bool) {
	rt.sessMu.RLock()
	defer rt.sessMu.RUnlock()
	id, ok := rt.sessionStudents[sessID]
	return id, ok
}

func (rt *Runtime) liveSession(sessID string) core.Session {
	if rt.Transport == nil {
		return nil
	}
	for _, s := range rt.Transport.Sessions() {
		if s.ID() == sessID {
			return s
		}
	}
	return nil
}

func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func (rt *Runtime) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			env, err := protocol.NewEnvelope(protocol.MsgPing, rt.teacherID, time.Now().UnixMilli())
			if err == nil {
				env.Channel = rt.channel
				env.Timestamp = time.Now().UnixMilli()
				_ = rt.Transport.BroadcastControl(env, nil)
			}
			rt.markOfflineStudents(rt.Classroom.MarkOfflineStale(15 * time.Second))
		}
	}
}

func (rt *Runtime) markOfflineStudents(studentIDs []string) {
	for _, studentID := range studentIDs {
		rt.resetStudentDecoder(studentID + ":monitor")
		rt.resetStudentDecoder(studentID + ":remote")
		rt.resetStudentDecoder(studentID + ":demo")
	}
	if len(studentIDs) > 0 && rt.App != nil {
		rt.App.Invalidate()
	}
}
