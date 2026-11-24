package teacher

import (
	"fmt"
	"strings"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
)

func (a *App) ToggleBroadcast() {
	a.broadcasting = !a.broadcasting
	a.applyBroadcastState()
}

func (a *App) applyBroadcastState() {
	a.syncRuntimeFlags()
	if a.broadcasting {
		cfg := a.CurrentBroadcastConfig()
		a.activeModeLabel = "屏幕广播"
		a.statusNote = fmt.Sprintf("正在向全班广播教师屏幕 (%s)", cfg.Mode)
		a.sendControlEnvelope(protocol.MsgBroadcastStart, cfg, nil)
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "已停止屏幕广播"
		a.sendControlEnvelope(protocol.MsgBroadcastStop, nil, nil)
	}
}

func (a *App) CurrentBroadcastConfig() core.BroadcastConfig {
	mode := core.BroadcastFullScreen
	if a.broadcastWindowedFlag.Load() {
		mode = core.BroadcastWindowed
	}
	qual := int(a.qualValue.Load())
	if qual <= 0 {
		qual = 75
	}
	return core.BroadcastConfig{
		Mode:         mode,
		WithAudio:    true,
		FPS:          24,
		Quality:      qual,
		UseMulticast: a.mcastFlag.Load(),
		SourceID:     a.teacherID,
	}
}

func (a *App) ToggleVoice() {
	a.voiceActive = !a.voiceActive
	a.applyVoiceState()
}

func (a *App) applyVoiceState() {
	a.syncRuntimeFlags()
	if a.voiceActive {
		a.activeModeLabel = "语音广播"
		a.statusNote = "全班语音广播开启中"
		a.sendControlEnvelope(protocol.MsgVoiceControl, map[string]any{"enabled": true, "volume": a.volume / 100.0}, nil)
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "语音广播已关闭"
		a.sendControlEnvelope(protocol.MsgVoiceControl, map[string]any{"enabled": false}, nil)
	}
}

func (a *App) ToggleStudentDemo(studentID string) {
	if a.studentDemoID != "" {
		prev := a.studentDemoID
		a.studentDemoID = ""
		a.activeModeLabel = "待机监听"
		a.statusNote = "已结束学生演示"
		a.sendControlEnvelope(protocol.MsgStudentDemoReq, map[string]any{"active": false, "student_id": prev}, []string{prev})
		a.sendControlEnvelope(protocol.MsgBroadcastStop, nil, nil)
		return
	}
	if studentID == "" {
		a.statusNote = "请先选择一台学生机作为演示者"
		return
	}
	a.studentDemoID = studentID
	a.activeModeLabel = "学生演示"
	a.statusNote = "正在全班转播学生演示: " + studentID
	a.sendControlEnvelope(protocol.MsgStudentDemoReq, map[string]any{"active": true, "student_id": studentID}, []string{studentID})
	var viewers []string
	for _, st := range a.listStudents() {
		if st.ID != studentID && st.Status != core.StatusOffline {
			viewers = append(viewers, st.ID)
		}
	}
	if len(viewers) > 0 {
		a.sendControlEnvelope(protocol.MsgBroadcastStart, core.BroadcastConfig{
			Mode:         core.BroadcastWindowed,
			WithAudio:    false,
			FPS:          15,
			Quality:      int(a.monitorQual),
			UseMulticast: a.useMulticast,
			SourceID:     studentID,
		}, viewers)
	}
}

func (a *App) ToggleMonitor() {
	a.monitoring = !a.monitoring
	a.applyMonitorState()
}

func (a *App) applyMonitorState() {
	if a.monitoring {
		a.mainTab = TabMonitorWall
		a.activeModeLabel = "屏幕监控"
		a.statusNote = fmt.Sprintf("屏幕监控墙运行中 (%d FPS, 画质 %d%%)", int(a.monitorFPS), int(a.monitorQual))
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "已停止屏幕监控"
	}
	a.sendMonitorConfig()
}

func (a *App) sendMonitorConfig() {
	// Publish the snapshot read by runtime goroutines before notifying students.
	a.monitoringFlag.Store(a.monitoring)
	a.monitorFPSValue.Store(int64(a.monitorFPS))
	fps := int(a.monitorFPS)
	if !a.monitoring {
		fps = 0
	}
	a.sendControlEnvelope(protocol.MsgMonitorControl, struct {
		Active bool `json:"active"`
		core.MonitorConfig
	}{
		Active: a.monitoring,
		MonitorConfig: core.MonitorConfig{
			FPS:     fps,
			Quality: int(a.monitorQual),
			Width:   320,
			Height:  180,
		},
	}, nil)
}

func (a *App) ToggleScreenLock() {
	targets := a.targetStudentIDs()
	a.setScreenLockTargets(!a.targetsLocked(targets), targets)
}

func (a *App) applyScreenLock() {
	a.ToggleScreenLock()
}

// targetsLocked reports whether every target (or the whole class when
// targets is empty) is currently locked.
func (a *App) targetsLocked(targets []string) bool {
	a.lockMu.RLock()
	defer a.lockMu.RUnlock()
	if len(targets) == 0 {
		return a.lockAll
	}
	for _, id := range targets {
		if !a.lockAll && !a.lockedIDs[id] {
			return false
		}
	}
	return true
}

func (a *App) setScreenLockTargets(locked bool, targets []string) {
	a.lockMu.Lock()
	switch {
	case len(targets) == 0:
		a.lockAll = locked
		a.lockedIDs = make(map[string]bool)
	case locked:
		if a.lockedIDs == nil {
			a.lockedIDs = make(map[string]bool)
		}
		for _, id := range targets {
			a.lockedIDs[id] = true
		}
	default:
		if a.lockAll {
			// Convert "whole class locked" into an explicit set minus targets.
			a.lockAll = false
			a.lockedIDs = make(map[string]bool)
			for _, st := range a.listStudents() {
				a.lockedIDs[st.ID] = true
			}
		}
		for _, id := range targets {
			delete(a.lockedIDs, id)
		}
	}
	a.lockMessageSnap = a.lockMessage
	a.screenLocked = a.lockAll || len(a.lockedIDs) > 0
	a.lockMu.Unlock()
	if locked {
		a.activeModeLabel = "黑屏锁屏"
		if len(targets) > 1 {
			a.statusNote = fmt.Sprintf("已对选中的 %d 台学生执行黑屏锁定", len(targets))
		} else {
			a.statusNote = "已对目标学生执行黑屏肃静锁定"
		}
	} else {
		a.activeModeLabel = "待机监听"
		a.statusNote = "已解除学生端屏幕锁定"
	}
	a.sendControlEnvelope(protocol.MsgScreenLock, map[string]any{
		"locked":  locked,
		"message": a.lockMessage,
	}, targets)
}

func (a *App) StartCheckIn() {
	if a.opts.Classroom != nil {
		a.opts.Classroom.StartCheckIn()
	}
	a.statusNote = "已发起课堂点名签到，等待学生提交"
	a.sendControlEnvelope(protocol.MsgCheckInStart, map[string]any{"channel": a.channel}, nil)
}

func (a *App) ApproveHandRaise(studentID string) {
	if a.opts.Classroom != nil {
		a.opts.Classroom.RaiseHand(studentID, false)
	}
	a.sendControlEnvelope(protocol.MsgHandClear, map[string]any{"student_id": studentID, "approved": true}, []string{studentID})
	if a.studentDemoID != studentID {
		if a.studentDemoID != "" {
			// Stops the current demo and notifies the previous presenter.
			a.ToggleStudentDemo("")
		}
		a.ToggleStudentDemo(studentID)
	}
	a.statusNote = "已批准举手学生演示: " + studentID
}

func (a *App) clearAllHands() {
	for _, st := range a.listStudents() {
		if st.HandRaised && a.opts.Classroom != nil {
			a.opts.Classroom.RaiseHand(st.ID, false)
		}
	}
	a.statusNote = "已清空全部举手状态"
}

func (a *App) SendChatMessage() {
	text := strings.TrimSpace(a.chatInput)
	if text == "" {
		return
	}
	if a.opts.Classroom != nil {
		msg := core.ChatMessage{
			ID:        fmt.Sprintf("chat-%d", time.Now().UnixNano()),
			SenderID:  a.teacherID,
			Sender:    a.teacherName,
			Text:      text,
			CreatedAt: time.Now(),
		}
		a.opts.Classroom.AddChat(msg)
		if a.store != nil {
			_ = a.store.SaveChat(msg)
		}
	}
	a.sendControlEnvelope(protocol.MsgChat, core.ChatMessage{
		SenderID: a.teacherID,
		Sender:   a.teacherName,
		Text:     text,
	}, nil)
	a.chatInput = ""
	a.statusNote = "已发送课堂公告消息"
}

func (a *App) ApplyClassroomPolicy() {
	a.policyActive = true
	rule := core.PolicyRule{
		Mode:           a.policyMode,
		BlockedApps:    splitCSV(a.blockedAppsInput),
		AllowedApps:    splitCSV(a.allowedAppsInput),
		BlockedDomains: splitCSV(a.blockedSitesInput),
		AllowedDomains: splitCSV(a.allowedSitesInput),
		LockScreen:     a.ScreenLockAll(),
		LockMessage:    a.lockMessage,
	}
	// The policy targets student machines only; opts.Control is the teacher's
	// own machine and must not be locked or have processes killed.
	a.savePersistedSettings()
	a.sendControlEnvelope(protocol.MsgPolicySync, rule, nil)
	a.statusNote = "管控策略已同步至学生端 (" + a.policyMode + ")"
}

func (a *App) RevokeClassroomPolicy() {
	a.policyActive = false
	rule := core.PolicyRule{
		Mode:        "off",
		LockScreen:  a.ScreenLockAll(),
		LockMessage: a.lockMessage,
	}
	a.savePersistedSettings()
	a.sendControlEnvelope(protocol.MsgPolicySync, rule, nil)
	a.statusNote = "已撤销管控策略，学生端限制已解除"
}

func (a *App) ExecutePower(action core.PowerAction) {
	a.ExecutePowerTargets(action, a.targetStudentIDs())
}

func (a *App) ExecutePowerTargets(action core.PowerAction, targets []string) {
	if len(targets) == 0 && action == core.PowerWake {
		woken := 0
		for _, st := range a.listStudents() {
			if st.MAC == "" || a.opts.Control == nil {
				continue
			}
			_ = a.opts.Control.ExecutePower(core.PowerWake, st.MAC)
			woken++
		}
		if woken == 0 {
			a.statusNote = "没有记录到学生机 MAC 地址，无法远程开机"
		} else {
			a.statusNote = fmt.Sprintf("已向 %d 台学生机发送网络唤醒包", woken)
		}
		return
	}
	if len(targets) == 0 {
		a.sendControlEnvelope(protocol.MsgPowerCommand, map[string]any{"action": action, "mac": ""}, nil)
		a.statusNote = fmt.Sprintf("已发送全班电源指令: %s", action)
		return
	}
	macMap := make(map[string]string)
	for _, st := range a.listStudents() {
		macMap[st.ID] = st.MAC
	}
	for _, id := range targets {
		mac := macMap[id]
		// Only Wake-on-LAN is executed locally: it sends a magic packet to
		// the (powered-off) student. Shutdown/reboot/logoff must never run
		// on the teacher's own machine; students execute them on receipt.
		if action == core.PowerWake && mac != "" && a.opts.Control != nil {
			_ = a.opts.Control.ExecutePower(action, mac)
		}
		a.sendControlEnvelope(protocol.MsgPowerCommand, map[string]any{"action": action, "mac": mac}, []string{id})
	}
	if len(targets) > 1 {
		a.statusNote = fmt.Sprintf("已对 %d 台学生发送电源指令: %s", len(targets), action)
	} else {
		a.statusNote = fmt.Sprintf("已发送电源指令: %s", action)
	}
}

func (a *App) resetStudentExitPassword() {
	targets := a.targetStudentIDs()
	if len(targets) == 0 {
		a.statusNote = "请选择要重置退出密码的学生"
		a.Invalidate()
		return
	}
	a.sendControlEnvelope(protocol.MsgExitPasswordReset, nil, targets)
	a.statusNote = fmt.Sprintf("已向 %d 台学生机发送退出密码重置命令", len(targets))
	a.Invalidate()
}

func (a *App) ClearOfflineStudents() {
	if a.opts.Classroom == nil {
		return
	}
	var removedIDs []string
	for _, st := range a.opts.Classroom.ListStudents() {
		if st.Status == core.StatusOffline {
			removedIDs = append(removedIDs, st.ID)
		}
	}
	if len(removedIDs) == 0 {
		a.statusNote = "当前没有离线电脑需要清除"
		return
	}
	a.mu.Lock()
	for _, id := range removedIDs {
		delete(a.monitorTiles, id)
		delete(a.pendingMonitor, id)
		if a.selectedIDs != nil {
			delete(a.selectedIDs, id)
		}
		if a.selectedID == id {
			a.selectedID = ""
		}
	}
	a.mu.Unlock()

	for _, id := range removedIDs {
		a.opts.Classroom.RemoveStudent(id)
	}
	if a.store != nil {
		_ = a.store.DeleteStudents(removedIDs)
	}
	a.statusNote = fmt.Sprintf("已清除 %d 台离线电脑", len(removedIDs))
}

func (a *App) autoLayoutSeats() {
	type autoLayouter interface {
		AutoLayoutSeats(rows, cols int)
	}
	if al, ok := a.opts.Classroom.(autoLayouter); ok {
		al.AutoLayoutSeats(0, 4)
		a.statusNote = "已按学号自动排列机房座位"
	}
}

func (a *App) swapStudentSeats(idA, idB string) {
	if a.opts.Classroom == nil {
		return
	}
	var stuA, stuB *core.StudentInfo
	for _, s := range a.opts.Classroom.ListStudents() {
		sCopy := s
		if s.ID == idA {
			stuA = &sCopy
		} else if s.ID == idB {
			stuB = &sCopy
		}
	}
	if stuA == nil || stuB == nil {
		return
	}
	a.opts.Classroom.UpdateSeat(stuA.ID, stuB.Seat)
	a.opts.Classroom.UpdateSeat(stuB.ID, stuA.Seat)
	a.statusNote = fmt.Sprintf("已调换座位: %s ↔ %s", stuA.MachineName, stuB.MachineName)
}
