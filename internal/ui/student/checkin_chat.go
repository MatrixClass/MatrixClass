package student

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/theme"
)

func (a *App) buildCheckInView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Padding(18).Gap(12).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, "课堂电子点名签到").FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if a.checkInSubmitted {
				if ui.Button(c, "完成").Padding(4, 10).Clicked() {
					a.mu.Lock()
					a.mode = ModeBar
					a.mu.Unlock()
					a.syncWindow()
				}
			}
		})
		ui.Text(c, fmt.Sprintf("当前机位：%s，请填写班级、姓名和学号后提交签到：", a.machineName)).FontSize(12).TextColor(pal.TextMuted)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "班级").Width(48).FontSize(12)
			ui.TextInput(c, &a.checkInClass).Width(200).Placeholder("请手动输入").AutoFocus()
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "姓名").Width(48).FontSize(12)
			ui.TextInput(c, &a.checkInName).Width(200).Placeholder("请手动输入")
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "学号").Width(48).FontSize(12)
			ui.TextInput(c, &a.checkInNo).Width(200)
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "确认签到").Padding(6, 16).Clicked() {
				a.SubmitCheckIn()
			}
			if a.checkInSubmitted {
				ui.Badge(c, "已完成签到").Background(pal.Selection).TextColor(pal.Text)
			}
		})
	})
}

func (a *App) buildChatView(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Fill().Padding(16).Gap(10).Background(pal.Canvas).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, "课堂消息").FontSize(16).Bold().TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "返回").Padding(4, 10).Clicked() {
				a.OpenMode(ModeBar)
			}
		})

		var chats []core.ChatMessage
		if a.opts.Classroom != nil {
			chats = a.opts.Classroom.ListChats()
		}
		chatGrew := len(chats) != a.lastChatCount
		a.lastChatCount = len(chats)
		ui.Scroll(c.Key("student-chat-scroll")).Grow(1).FillWidth().ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				if len(chats) == 0 {
					ui.Text(c, "暂无课堂消息").FontSize(12).TextColor(pal.TextMuted)
				}
				for i, msg := range chats {
					bubble := ui.Column(c).Key(fmt.Sprintf("st-chat-%d", i)).FillWidth().Padding(6, 8).Radius(3).Background(pal.Card).Border(1, pal.CardBorder).Gap(2)
					if chatGrew && i == len(chats)-1 {
						bubble.ScrollIntoView()
					}
					bubble.Children(func() {
						ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
							ui.Text(c, msg.Sender).FontSize(11).Bold().TextColor(pal.Accent)
							ui.Spacer(c)
							if !msg.CreatedAt.IsZero() {
								ui.Text(c, msg.CreatedAt.Format("15:04")).FontSize(10).TextColor(pal.TextMuted)
							}
						})
						ui.Text(c, msg.Text).FontSize(12).TextColor(pal.Text).FillWidth()
					})
				}
			})
		})

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			inp := ui.TextInput(c, &a.chatInput).Grow(1).Placeholder("向教师端发送消息...")
			if inp.Submitted() || ui.PrimaryButton(c, "发送消息").Padding(4, 14).Clicked() {
				a.SendChat()
			}
		})
	})
}

func (a *App) ToggleHandRaise() {
	raised := !a.handRaised
	if err := a.sendControl(protocol.MsgHandRaise, map[string]any{
		"student_id": a.studentID,
		"raised":     raised,
	}); err != nil {
		a.statusNote = "举手失败: " + err.Error()
		return
	}
	a.handRaised = raised
	if a.opts.Classroom != nil {
		a.opts.Classroom.RaiseHand(a.studentID, a.handRaised)
	}
	if a.handRaised {
		a.statusNote = "已举手，等待教师批准演示"
	} else {
		a.statusNote = "已取消举手"
	}
}

func (a *App) SubmitCheckIn() {
	if strings.TrimSpace(a.checkInClass) == "" || strings.TrimSpace(a.checkInName) == "" {
		a.statusNote = "签到失败：请填写班级和姓名"
		return
	}
	if err := a.sendControl(protocol.MsgCheckInSubmit, map[string]string{
		"student_id": a.studentID,
		"class_name": strings.TrimSpace(a.checkInClass),
		"name":       strings.TrimSpace(a.checkInName),
		"student_no": a.checkInNo,
	}); err != nil {
		a.statusNote = "签到失败: " + err.Error()
		return
	}
	if a.opts.Classroom != nil {
		a.opts.Classroom.SubmitCheckIn(a.studentID, strings.TrimSpace(a.checkInName), a.checkInNo)
	}
	a.checkInSubmitted = true
	a.statusNote = fmt.Sprintf("签到成功: %s (%s)", a.machineName, a.checkInNo)
}

func (a *App) SendChat() {
	txt := strings.TrimSpace(a.chatInput)
	if txt == "" {
		return
	}
	msg := core.ChatMessage{
		SenderID:  a.studentID,
		Sender:    a.machineName,
		Text:      txt,
		CreatedAt: time.Now(),
	}
	if err := a.sendControl(protocol.MsgChat, msg); err != nil {
		a.statusNote = "消息发送失败: " + err.Error()
		return
	}
	if a.opts.Classroom != nil {
		a.opts.Classroom.AddChat(msg)
	}
	a.chatInput = ""
	a.statusNote = "消息已发送"
}

func (a *App) CheckInSubmitted() bool {
	return a.checkInSubmitted
}
