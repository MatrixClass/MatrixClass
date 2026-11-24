package main

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/remoteexec"
	"nexlab/internal/transport"
	"nexlab/internal/ui/student"
)

func (rt *Runtime) wireHandlers() {
	rt.Transport.OnSession(func(sess core.Session) {
		rt.Files.SetTeacherSession(sess)
		rt.App.SetStatusNote("已连接频道: " + rt.channel)
		rt.goLifecycle(func(ctx context.Context) { rt.acceptFileStreams(ctx, sess) })
		env, err := protocol.NewEnvelope(protocol.MsgStudentRegister, rt.studentID, core.StudentInfo{
			ID:          rt.studentID,
			StudentNo:   rt.studentNo,
			MachineName: rt.machineName,
			MAC:         rt.macAddr,
			Channel:     rt.channel,
			Status:      core.StatusOnline,
		})
		if err == nil {
			env.Channel = rt.channel
			env.Timestamp = time.Now().UnixMilli()
			_ = sess.SendEnvelope(env)
		}
	})

	rt.Transport.OnMulticastData(func(data []byte) {
		rt.streamMu.Lock()
		isDemoSrc := rt.demoActive
		rt.streamMu.Unlock()
		if isDemoSrc {
			return
		}
		cp := make([]byte, len(data))
		copy(cp, data)
		enqueueLatestFrame(rt.bcastDecodeCh, cp)
	})

	rt.Transport.OnEnvelope(func(sess core.Session, env protocol.Envelope) {
		switch env.Type {
		case protocol.MsgPing:
			rt.Control.NotifyTeacherHeartbeat()
			pong, err := protocol.NewEnvelope(protocol.MsgPong, rt.studentID, json.RawMessage(env.Payload))
			if err == nil {
				pong.Channel = rt.channel
				pong.Timestamp = time.Now().UnixMilli()
				_ = sess.SendEnvelope(pong)
			}
		case protocol.MsgBroadcastStart:
			var cfg core.BroadcastConfig
			_ = json.Unmarshal(env.Payload, &cfg)
			rt.App.StartBroadcast(cfg.Mode == core.BroadcastWindowed)
		case protocol.MsgBroadcastStop:
			rt.App.StopBroadcast()
		case protocol.MsgVoiceControl:
			var req struct {
				Enabled bool `json:"enabled"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if req.Enabled {
					rt.App.SetStatusNote("教师端语音广播进行中")
				} else {
					rt.App.SetStatusNote("已连接频道: " + rt.channel)
				}
			}
		case protocol.MsgTheatreControl:
			var ts core.TheatreState
			if json.Unmarshal(env.Payload, &ts) == nil {
				rt.App.SetTheatreState(ts)
			}
		case protocol.MsgAudioFrame:
			var encoded []byte
			if json.Unmarshal(env.Payload, &encoded) == nil && len(encoded) > 0 {
				if af, err := rt.Media.DecodeAudioFrame(encoded); err == nil {
					_ = rt.Media.PlayAudioFrame(af)
				}
			}
		case protocol.MsgMonitorControl:
			var req struct {
				Active bool `json:"active"`
				core.MonitorConfig
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if len(req.TargetIDs) > 0 && !containsID(req.TargetIDs, rt.studentID) {
					return
				}
				if req.Active && req.FPS > 0 {
					w, h := req.Width, req.Height
					if w <= 0 {
						w = 320
					}
					if h <= 0 {
						h = 180
					}
					rt.startMonitorStreamWithReset(w, h, req.FPS, req.Quality, req.Reset)
					rt.App.SetMonitoring(true)
				} else {
					rt.stopMonitorStream()
					rt.App.SetMonitoring(false)
				}
			}
		case protocol.MsgRemoteControlReq:
			var req struct {
				StudentID string `json:"student_id"`
				Enabled   bool   `json:"enabled"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if req.StudentID != "" && req.StudentID != rt.studentID {
					return
				}
				rt.App.SetRemoteControlled(req.Enabled)
				if req.Enabled {
					rt.startRemoteStream()
				} else {
					rt.stopRemoteStream()
				}
			}
		case protocol.MsgStudentDemoReq:
			var req struct {
				Active    bool   `json:"active"`
				StudentID string `json:"student_id"`
			}
			if json.Unmarshal(env.Payload, &req) == nil {
				if req.StudentID != "" && req.StudentID != rt.studentID {
					return
				}
				if req.Active {
					rt.startDemoStream()
					rt.App.SetStatusNote("正在向全班进行学生演示")
				} else {
					rt.stopDemoStream()
					rt.App.SetStatusNote("学生演示已结束")
				}
			}
		case protocol.MsgHandClear:
			rt.App.SetHandRaised(false)
			rt.App.SetStatusNote("教师已批准您的举手演示")
		case protocol.MsgScreenLock:
			var req struct {
				Locked  bool   `json:"locked"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				rt.App.SetLocked(req.Locked, req.Message)
			}
		case protocol.MsgPolicySync:
			var rule core.PolicyRule
			if err := json.Unmarshal(env.Payload, &rule); err == nil {
				_ = rt.Control.ApplyPolicy(rule)
			}
		case protocol.MsgExitPasswordReset:
			rt.App.ResetExitPassword()
		case protocol.MsgStudentNetworkConfig:
			var cfg transport.NetworkConfig
			if json.Unmarshal(env.Payload, &cfg) == nil {
				if err := rt.App.ApplyNetworkConfig(cfg); err == nil {
					rt.Transport.SetNetworkConfig(cfg)
					go func() {
						if err := rt.Transport.ReconnectStudent(context.Background(), rt.App.Channel()); err != nil {
							rt.App.SetStatusNote("网络参数已保存，重连失败: " + err.Error())
						}
					}()
				} else {
					rt.App.SetStatusNote("网络参数无效: " + err.Error())
				}
			}
		case protocol.MsgPowerCommand:
			var req struct {
				Action core.PowerAction `json:"action"`
				MAC    string           `json:"mac"`
			}
			if err := json.Unmarshal(env.Payload, &req); err == nil {
				_ = rt.Control.ExecutePower(req.Action, req.MAC)
			}
		case protocol.MsgRemoteInputEvent:
			var ev core.RemoteInputEvent
			if err := json.Unmarshal(env.Payload, &ev); err == nil {
				_ = rt.Control.InjectInput(ev)
			}
		case protocol.MsgCheckInStart:
			rt.App.OpenMode(student.ModeCheckIn)
		case protocol.MsgExamPublish:
			var paper core.ExamPaper
			if err := json.Unmarshal(env.Payload, &paper); err == nil {
				rt.Exam.PublishPaper(paper)
				rt.App.OpenMode(student.ModeExam)
			}
		case protocol.MsgHomeworkCollectStart:
			rt.App.OpenHomeworkSubmitWindow()
		case protocol.MsgHomeworkCollectEnd:
			rt.App.CloseHomeworkSubmitWindow()
		case protocol.MsgChat:
			var msg core.ChatMessage
			if err := json.Unmarshal(env.Payload, &msg); err == nil {
				rt.Classroom.AddChat(msg)
				rt.App.ShowTeacherNotice(msg.Sender, msg.Text)
				rt.App.Invalidate()
			}
		case protocol.MsgRemoteAppListReq:
			rt.sendRemoteEnvelope(sess, protocol.MsgRemoteAppListResp, env.RequestID, rt.Remote.ListApplications())
		case protocol.MsgRemoteAppLaunch:
			var req remoteexec.AppLaunchRequest
			if json.Unmarshal(env.Payload, &req) == nil {
				rt.App.SetStatusNote("教师请求启动程序")
				result := rt.Remote.LaunchApp(context.Background(), req, rt.Remote.ListApplications())
				rt.sendRemoteEnvelope(sess, protocol.MsgRemoteAppResult, env.RequestID, result)
			}
		case protocol.MsgRemoteCommandStart:
			var req remoteexec.CommandRequest
			if json.Unmarshal(env.Payload, &req) == nil {
				rt.startRemoteCommand(sess, req)
			}
		case protocol.MsgRemoteCommandCancel:
			requestID := env.RequestID
			if requestID == "" {
				var req struct {
					RequestID string `json:"request_id"`
				}
				if json.Unmarshal(env.Payload, &req) == nil {
					requestID = req.RequestID
				}
			}
			rt.remoteMu.Lock()
			task, ok := rt.remoteTasks[requestID]
			rt.remoteMu.Unlock()
			if ok && task.Cancel != nil {
				task.Cancel()
			}
		}
	})
}

func (rt *Runtime) sendRemoteEnvelope(sess core.Session, msgType protocol.MsgType, requestID string, payload any) {
	if sess == nil {
		return
	}
	env, err := protocol.NewEnvelope(msgType, rt.studentID, payload)
	if err != nil {
		return
	}
	env.RequestID = requestID
	env.Channel = rt.channel
	env.Timestamp = time.Now().UnixMilli()
	_ = sess.SendEnvelope(env)
}

func (rt *Runtime) startRemoteCommand(sess core.Session, req remoteexec.CommandRequest) {
	rt.App.SetStatusNote("教师正在执行远程命令")
	rt.lifecycleMu.Lock()
	if rt.lifecycleCtx == nil || rt.lifecycleClosed {
		rt.lifecycleMu.Unlock()
		return
	}
	lifecycleCtx := rt.lifecycleCtx
	rt.lifecycleMu.Unlock()
	task, err := rt.Remote.StartCommand(lifecycleCtx, req, func(output remoteexec.CommandOutput) {
		rt.sendRemoteEnvelope(sess, protocol.MsgRemoteCommandOutput, req.RequestID, output)
	})
	if err != nil {
		rt.sendRemoteEnvelope(sess, protocol.MsgRemoteCommandResult, req.RequestID, remoteexec.CommandResult{
			RequestID:  req.RequestID,
			Status:     "rejected",
			Error:      err.Error(),
			ExitCode:   -1,
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		})
		return
	}
	rt.remoteMu.Lock()
	rt.remoteTasks[req.RequestID] = task
	rt.remoteMu.Unlock()
	rt.goLifecycle(func(ctx context.Context) {
		result := <-task.Result
		rt.remoteMu.Lock()
		delete(rt.remoteTasks, req.RequestID)
		rt.remoteMu.Unlock()
		rt.sendRemoteEnvelope(sess, protocol.MsgRemoteCommandResult, req.RequestID, result)
	})
}

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
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
				_ = rt.Files.HandleStream(taskCtx, stream)
				rt.App.Invalidate()
			})
		} else {
			_ = stream.Close()
		}
	}
}
