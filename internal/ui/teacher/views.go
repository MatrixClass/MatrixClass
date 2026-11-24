package teacher

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/ui/theme"
)

func (a *App) buildToolbar(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Background(pal.ChromeBar).Padding(8, 12).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(10).DragWindow().Children(func() {
			ui.Text(c, "矩联电子教室 MatrixClass").FontSize(14).Bold().TextColor(pal.Accent)
			ui.Badge(c, a.channel).Background(pal.Selection).TextColor(pal.Accent)
			ui.Spacer(c)
			ui.SearchField(c, &a.searchQuery).Placeholder("筛选机位 / 学号 / IP...").Width(220)
			if ui.Button(c, "").Label("设置").Tooltip("课堂通道与组播设置").Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Image(c, theme.IconSettings).Size(16, 16)
					ui.Text(c, "设置").FontSize(12)
				})
			}).Clicked() {
				a.openSettings()
			}
			if ui.Button(c, "").Label("管控策略").Tooltip("编排并下发课堂管控策略").Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Image(c, theme.IconPolicy).Size(16, 16)
					ui.Text(c, "管控策略").FontSize(12)
				})
			}).Clicked() {
				a.policyModal = true
			}
		})

		ui.Toolbar(c, func() {
			a.toolbarToggle(c, pal, "屏幕广播", theme.IconBroadcast, &a.broadcasting, func() {
				a.applyBroadcastState()
			})
			a.toolbarToggle(c, pal, "语音广播", theme.IconVoice, &a.voiceActive, func() {
				a.applyVoiceState()
			})
			a.toolbarButton(c, pal, "学生演示", theme.IconStudentDemo, a.studentDemoID != "", func() {
				a.ToggleStudentDemo(a.selectedID)
			})
			a.toolbarButton(c, pal, "网络影院", theme.IconMyTheatre, a.mainTab == TabMyTheatre, func() {
				a.mainTab = TabMyTheatre
				a.statusNote = "已打开网络影院，选择视频后点击播放"
			})
			a.toolbarToggle(c, pal, "屏幕监控", theme.IconMonitor, &a.monitoring, func() {
				a.applyMonitorState()
			})
			a.toolbarButton(c, pal, "远程控制", theme.IconRemoteControl, a.remoteControlID != "", func() {
				a.StartRemoteControl(a.selectedID)
			})
			a.toolbarButton(c, pal, "远程操作", theme.IconRemoteExec, a.remoteExecOpen, func() {
				a.remoteExecOpen = true
				a.remoteResult = nil
				a.remoteOutput = ""
			})
			a.toolbarToggle(c, pal, "黑屏锁屏", theme.IconLockScreen, &a.screenLocked, func() {
				a.applyScreenLock()
			})
			a.toolbarButton(c, pal, "文件分发", theme.IconFileDistribute, a.fileDistModal, func() {
				a.OpenFileDistributeWindow()
			})
			a.toolbarButton(c, pal, "收取作业", theme.IconCollectHomework, a.collectingHomework, func() {
				a.CollectHomework()
			})
			a.toolbarButton(c, pal, "随堂测验", theme.IconExam, a.mainTab == TabExamStats, func() {
				a.mainTab = TabExamStats
				a.statusNote = "随堂测验与统计面板已打开"
			})
			a.toolbarButton(c, pal, "点名签到", theme.IconCheckIn, false, func() {
				a.StartCheckIn()
			})
			ui.Spacer(c)
			a.toolbarButton(c, pal, "远程开机", theme.IconPower, false, func() {
				a.ExecutePower(core.PowerWake)
			})
		}).Label("教师主工具栏")
	})
}

func (a *App) toolbarButton(c *ui.Context, pal theme.Palette, label string, icon *ui.SVG, active bool, onClick func()) {
	btn := ui.Button(c, "").Label(label).Tooltip(label).Padding(6, 10).Radius(4)
	if active {
		btn.Background(pal.Selection).Border(1, pal.CardBorder)
	}
	btn.Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(3).Children(func() {
			ui.Image(c, icon).Size(22, 22)
			ui.Text(c, label).FontSize(11).SingleLine()
		})
	})
	if btn.Clicked() {
		onClick()
	}
}

func (a *App) toolbarToggle(c *ui.Context, pal theme.Palette, label string, icon *ui.SVG, state *bool, onChanged func()) {
	tg := ui.Toggle(c, state, "").Label(label).Tooltip(label).Padding(6, 10).Radius(4)
	if *state {
		tg.Background(pal.Selection).Border(1, pal.CardBorder)
	}
	tg.Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(3).Children(func() {
			ui.Image(c, icon).Size(22, 22)
			ui.Text(c, label).FontSize(11).SingleLine()
		})
	})
	if tg.Changed() {
		onChanged()
	}
}

func (a *App) buildBody(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.SplitPercent(c, &a.mainSplitPct, func() {
		a.buildMainWorkspace(c, pal, students)
	}, func() {
		a.buildRightSidebar(c, pal, students)
	}).SplitReset(75).Grow(1)
}

func (a *App) buildMainWorkspace(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Column(c).Fill().Background(pal.Canvas).Children(func() {
		ui.Row(c).Background(pal.ChromeBar).Padding(4, 10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.CardBorder).Children(func() {
			ui.Tabs(c, &a.mainTab, "设备视图", "屏幕监控", "网络影院", "随堂测验", "文件与作业")
			ui.Spacer(c)
			ui.Checkbox(c, &a.broadcastWindowed, "窗口模式广播").OnChange(func() {
				a.syncRuntimeFlags()
				if a.broadcasting {
					a.applyBroadcastState()
				}
			})
		})

		switch a.mainTab {
		case TabSeatMap:
			a.buildSeatMapTab(c, pal, students)
		case TabMonitorWall:
			a.buildMonitorWallTab(c, pal, students)
		case TabMyTheatre:
			a.buildMyTheatreTab(c, pal)
		case TabExamStats:
			a.buildExamStatsTab(c, pal)
		case TabFiles:
			a.buildFilesTab(c, pal)
		}

		if (a.mainTab == TabSeatMap || a.mainTab == TabMonitorWall) && !a.settingsOpen && !a.remoteControlOpen {
			if c.Shortcut(ui.Cmd, ui.KeyA) {
				a.selectAllStudents(students)
			}
		}
	})
}

func (a *App) buildSeatMapTab(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Column(c).Grow(1).FillWidth().Children(func() {
		scroll := ui.Scroll(c).Grow(1).Padding(12).Gap(10).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true)
		a.attachBlankAreaContextMenu(scroll, students)
		a.attachMarqueeElement(scroll, "", students)
		scroll.Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, "设备视图").FontSize(15).Bold()
				ui.Badge(c, fmt.Sprintf("显示 %d 台", len(students)))
				if selCount := a.selectedCount(); selCount > 0 {
					ui.Badge(c, fmt.Sprintf("已选 %d 台", selCount)).Background(pal.Selection).TextColor(pal.Accent)
				}
				if len(students) > 0 {
					if ui.Button(c, "全选 (Ctrl+A)").Padding(3, 10).HandleInput(func(ui.InputEvent) bool { return false }).Clicked() {
						a.selectAllStudents(students)
					}
				}
				if ui.Button(c, "自动排座").Padding(3, 10).HandleInput(func(ui.InputEvent) bool { return false }).Clicked() {
					a.autoLayoutSeats()
				}
				ui.Spacer(c)
				ui.Text(c, "提示：Ctrl+左键多选、Ctrl+A全选、点空白处取消、右键批量操作").FontSize(11).TextColor(pal.TextMuted)
			})

			if len(students) == 0 {
				ui.Box(c).Padding(24).Center().Children(func() {
					ui.Text(c, "当前分组暂无学生机在线").TextColor(pal.TextMuted)
				})
				return
			}

			// Fill the available width: column count follows the viewport
			// (measured last frame) instead of fixed-width wrapped cards.
			ui.Grid(c).Columns(seatGridColumns(scroll.Bounds().W)).Gap(seatGridGap).FillWidth().Children(func() {
				for _, st := range students {
					a.buildSeatCard(c, pal, st, students)
				}
			})
			a.drawMarqueeOverlay(c, pal, scroll.Bounds())
		})
		a.suppressClear = false
		a.buildGroupBottomBar(c, pal)
	})
}

func (a *App) buildSeatCard(c *ui.Context, pal theme.Palette, st core.StudentInfo, students []core.StudentInfo) {
	selected := a.isSelected(st.ID)
	bg := pal.Card
	border := pal.CardBorder
	if selected {
		bg = pal.Selection
		border = pal.Accent
	}

	offline := st.Status == core.StatusOffline
	handUp := st.HandRaised || st.Status == core.StatusHandRaised
	if handUp && !selected {
		// Strongest monochrome emphasis without color: a heavy dark border.
		border = pal.Accent
	}
	borderW := float32(1)
	if selected || handUp {
		borderW = 2
	}

	card := ui.Column(c).Key("seat-"+st.ID).FillWidth().MinWidth(seatCardMinWidth).Padding(10).Gap(6).Radius(4).
		Background(bg).Border(borderW, border).Focusable().
		Label(fmt.Sprintf("%s %s", st.MachineName, st.StudentNo))
	if offline {
		card.Opacity(0.45)
	}
	if b := card.Bounds(); b.W > 0 && b.H > 0 {
		a.itemBounds = ensureBoundsMap(a.itemBounds)
		a.itemBounds[st.ID] = b
	}
	a.attachMarqueeElement(card, st.ID, students)

	a.attachStudentContextMenu(card, st)

	card.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Image(c, theme.IconSeat).Size(24, 24)
			ui.Column(c).Grow(1).Gap(1).Children(func() {
				ui.Text(c, st.MachineName).FontSize(13).Bold().SingleLine()
				if st.StudentNo != "" {
					ui.Text(c, "编号 "+st.StudentNo).FontSize(11).TextColor(pal.TextMuted).SingleLine()
				}
			})
			if handUp {
				ui.Image(c, theme.IconHandRaise).Size(18, 18)
			}
		})

		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			seatStatusPill(c, pal, st)
			ui.Spacer(c)
			if st.CheckedIn {
				ui.Text(c, "✓ 已签到").FontSize(10).Bold().TextColor(pal.Text)
			} else {
				ui.Text(c, "未签到").FontSize(10).TextColor(pal.TextMuted)
			}
		})

		ui.Text(c, fmt.Sprintf("%s · %dms", st.IP, st.RTTMs)).FontSize(10).TextColor(pal.TextMuted).SingleLine()
	})
}

func (a *App) buildMonitorWallTab(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Column(c).Grow(1).Padding(12).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "屏幕监控").FontSize(15).Bold()
			ui.Badge(c, fmt.Sprintf("%d FPS · 画质 %d%%", int(a.monitorFPS), int(a.monitorQual)))
			if selCount := a.selectedCount(); selCount > 0 {
				ui.Badge(c, fmt.Sprintf("已选 %d 台", selCount)).Background(pal.Selection).TextColor(pal.Accent)
			}
			if len(students) > 0 {
				if ui.Button(c, "全选 (Ctrl+A)").Padding(3, 10).Clicked() {
					a.selectAllStudents(students)
				}
			}
			ui.Spacer(c)
			if a.monitoring {
				if ui.Button(c, "停止监控").Clicked() {
					a.monitoring = false
					a.applyMonitorState()
				}
			} else {
				if ui.PrimaryButton(c, "开启监控轮询").Clicked() {
					a.monitoring = true
					a.applyMonitorState()
				}
			}
		})

		scroll := ui.Scroll(c).Grow(1).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true)
		a.attachBlankAreaContextMenu(scroll, students)
		a.attachMarqueeElement(scroll, "", students)
		scroll.Children(func() {
			if len(students) == 0 {
				ui.Box(c).Fill().Center().Padding(24).Children(func() {
					ui.Text(c, "暂无学生机在线").FontSize(13).TextColor(pal.TextMuted)
				})
				return
			}
			ui.Grid(c).Columns(monitorGridColumns(scroll.Bounds().W)).Gap(12).FillWidth().Children(func() {
				for _, st := range students {
					a.mu.Lock()
					bmp := a.ensureTileBitmap(st.ID, 240, 135)
					a.mu.Unlock()
					offline := st.Status == core.StatusOffline
					border := pal.CardBorder
					bg := pal.Card
					if a.isSelected(st.ID) {
						border = pal.Accent
						bg = pal.Selection
					}
					tile := ui.Column(c).Key("mon-"+st.ID).FillWidth().Padding(8).Gap(6).Radius(3).
						Background(bg).Border(1.5, border).Focusable()
					if offline {
						tile.Opacity(0.5)
					}
					if b := tile.Bounds(); b.W > 0 && b.H > 0 {
						a.itemBounds = ensureBoundsMap(a.itemBounds)
						a.itemBounds[st.ID] = b
					}
					a.attachMarqueeElement(tile, st.ID, students)
					a.attachStudentContextMenu(tile, st)
					tile.Children(func() {
						ui.Box(c).FillWidth().AspectRatio(16.0 / 9.0).Background(pal.TheatreBg).Radius(2).Clip().Center().Children(func() {
							if offline {
								ui.Text(c, "离线").FontSize(13).Bold().TextColor(ui.Hex("#D4D4D4"))
							} else {
								ui.Image(c, bmp).Fill().Fit(ui.Contain)
							}
						})
						ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
							ui.Text(c, st.MachineName).FontSize(12).Bold().Grow(1).SingleLine()
							if !offline {
								remoteBtn := ui.Button(c, "远控").Padding(2, 6).HandleInput(func(ui.InputEvent) bool { return false })
								if remoteBtn.Clicked() {
									a.StartRemoteControl(st.ID)
								}
							}
						})
					})
				}
			})
			a.drawMarqueeOverlay(c, pal, scroll.Bounds())
		})
		a.suppressClear = false
		a.buildGroupBottomBar(c, pal)
	})
}

func (a *App) buildRightSidebar(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	ui.Column(c).Fill().Background(pal.ChromeRail).BorderWidth(0, 0, 0, 1).BorderColor(pal.CardBorder).Children(func() {
		ui.Row(c).Padding(9, 12).Gap(6).Background(pal.ChromeBar).BorderWidth(0, 0, 1, 0).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
			ui.Image(c, theme.IconChat).Size(16, 16)
			ui.Text(c, "消息与举手").FontSize(13).Bold()
		})

		ui.Column(c).Grow(1).Padding(8).Gap(8).Children(func() {
			switch a.sideTab {
			case SideTabChatQueue:
				a.buildSideChatQueue(c, pal, students)
			}
		})

		ui.Divider(c)
		// Fixed bottom volume sliders for the right panel
		ui.Column(c).Background(pal.ChromeBar).Padding(8, 10).Gap(4).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, "扬声器").FontSize(12).Bold().Width(42)
				if ui.Slider(c, &a.volume, 0, 100).Label("扬声器音量滑块").Grow(1).Changed() {
					if a.volume > 0 && a.speakerMuted {
						a.speakerMuted = false
					}
					a.applySpeakerVolume()
				}
				spkTxt := fmt.Sprintf("%d%%", int(a.volume))
				if a.speakerMuted {
					spkTxt = "已静音"
				}
				ui.Text(c, spkTxt).FontSize(11).TextColor(pal.TextMuted).Width(40)
				spkMuteLbl := "静音"
				if a.speakerMuted {
					spkMuteLbl = "取消静音"
				}
				if ui.Button(c, spkMuteLbl).Key("spk-mute").Padding(2, 8).Clicked() {
					a.ToggleSpeakerMute()
				}
			})

			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, "麦克风").FontSize(12).Bold().Width(42)
				if ui.Slider(c, &a.micVolume, 0, 100).Label("麦克风音量滑块").Grow(1).Changed() {
					if a.micVolume > 0 && a.micMuted {
						a.micMuted = false
					}
					a.syncMicGain()
				}
				micTxt := fmt.Sprintf("%d%%", int(a.micVolume))
				if a.micMuted {
					micTxt = "已静音"
				}
				ui.Text(c, micTxt).FontSize(11).TextColor(pal.TextMuted).Width(40)
				micMuteLbl := "静音"
				if a.micMuted {
					micMuteLbl = "取消静音"
				}
				if ui.Button(c, micMuteLbl).Key("mic-mute").Padding(2, 8).Clicked() {
					a.ToggleMicMute()
				}
			})
		})
	})
}

func (a *App) buildSideChatQueue(c *ui.Context, pal theme.Palette, students []core.StudentInfo) {
	var raised []core.StudentInfo
	for _, st := range students {
		if st.HandRaised {
			raised = append(raised, st)
		}
	}

	ui.Column(c).Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(8).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "举手队列").FontSize(12).Bold()
			ui.Spacer(c)
			ui.Badge(c, fmt.Sprintf("%d 人举手", len(raised)))
		})
		if len(raised) == 0 {
			ui.Text(c, "暂无学生举手").FontSize(11).TextColor(pal.TextMuted)
		} else {
			for _, st := range raised {
				ui.Row(c).Key("hand-" + st.ID).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Image(c, theme.IconHandRaise).Size(16, 16)
					ui.Text(c, fmt.Sprintf("%s (%s)", st.MachineName, st.StudentNo)).FontSize(12).Grow(1).SingleLine()
					if ui.PrimaryButton(c, "批准演示").Padding(2, 8).Clicked() {
						a.ApproveHandRaise(st.ID)
					}
				})
			}
		}
	})

	var chats []core.ChatMessage
	if a.opts.Classroom != nil {
		chats = a.opts.Classroom.ListChats()
	}

	chatGrew := len(chats) != a.lastChatCount
	a.lastChatCount = len(chats)
	ui.Scroll(c).Grow(1).Gap(6).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
		if len(chats) == 0 {
			ui.Text(c, "暂无课堂消息").FontSize(11).TextColor(pal.TextMuted)
		}
		for i, msg := range chats {
			chatKey := msg.ID
			if chatKey == "" {
				chatKey = fmt.Sprintf("chat-%s-%d", msg.SenderID, msg.CreatedAt.UnixMilli())
			}
			bubble := ui.Column(c).Key(chatKey).FillWidth().Background(pal.Card).Border(1, pal.CardBorder).Radius(3).Padding(8).Gap(2)
			if chatGrew && i == len(chats)-1 {
				bubble.ScrollIntoView()
			}
			bubble.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, msg.Sender).FontSize(11).Bold().TextColor(pal.Accent)
					ui.Spacer(c)
					ui.Text(c, msg.CreatedAt.Format("15:04")).FontSize(10).TextColor(pal.TextMuted)
				})
				ui.Text(c, msg.Text).FontSize(12).FillWidth()
			})
		}
	})

	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		input := ui.TextInput(c, &a.chatInput).Placeholder("发送课堂消息...").Grow(1)
		if input.Submitted() || ui.PrimaryButton(c, "发送").Clicked() {
			a.SendChatMessage()
		}
	})
}

func (a *App) buildSidePolicy(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Grow(1).FillWidth().Gap(8).Children(func() {
		ui.Scroll(c).Grow(1).FillWidth().Gap(8).ScrollbarAxes(false, true).Scrollbars(true).KeepScrollbars(true).Children(func() {
			ui.Text(c, "管控模式").FontSize(11).TextColor(pal.TextMuted)
			policyDisplay := "黑名单模式"
			if a.policyMode == "whitelist" {
				policyDisplay = "白名单模式"
			}
			if ui.Select(c, &policyDisplay, []string{"黑名单模式", "白名单模式"}).Changed() {
				if policyDisplay == "白名单模式" {
					a.policyMode = "whitelist"
				} else {
					a.policyMode = "blacklist"
				}
			}
			if a.policyMode == "whitelist" {
				ui.Text(c, "只允许运行的进程 (逗号分隔)").FontSize(11).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.allowedAppsInput).Placeholder("例如：notepad.exe, editor.exe")
				ui.Text(c, "只允许访问的网址域名").FontSize(11).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.allowedSitesInput).Placeholder("例如：school.edu.cn, docs.example.com")
			} else {
				ui.Text(c, "禁止运行的进程 (逗号分隔)").FontSize(11).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.blockedAppsInput).Placeholder("例如：game.exe, game2.exe")
				ui.Text(c, "禁止访问的网址域名").FontSize(11).TextColor(pal.TextMuted)
				ui.TextInput(c, &a.blockedSitesInput).Placeholder("例如：example.com, video.example.com")
			}
			ui.Text(c, "黑屏锁定提示语").FontSize(11).TextColor(pal.TextMuted)
			ui.TextInput(c, &a.lockMessage)
		})
		ui.Row(c).Gap(6).FillWidth().Children(func() {
			if ui.PrimaryButton(c, "下发管控策略").Grow(1).Clicked() {
				a.ApplyClassroomPolicy()
				a.policyModal = false
			}
			if ui.Button(c, "撤销管控策略").Grow(1).Clicked() {
				a.RevokeClassroomPolicy()
				a.policyModal = false
			}
		})
	})
}

func (a *App) buildStatusBar(c *ui.Context, pal theme.Palette) {
	all := a.listStudents()
	online, checkedIn := 0, 0
	for _, st := range all {
		if st.Status != core.StatusOffline {
			online++
		}
		if st.CheckedIn {
			checkedIn++
		}
	}
	var stats core.TransportStats
	if a.opts.Transport != nil {
		stats = a.opts.Transport.Stats()
	}

	ui.Row(c).Background(pal.StatusBarBg).Padding(4, 12).Gap(14).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("在线: %d/%d", online, len(all))).FontSize(11).Bold().TextColor(pal.StatusBarText)
		ui.Text(c, fmt.Sprintf("已签到: %d", checkedIn)).FontSize(11).TextColor(pal.StatusBarText)
		ui.Text(c, fmt.Sprintf("频道: %s", a.channel)).FontSize(11).TextColor(pal.StatusBarText)
		ui.Text(c, fmt.Sprintf("当前模式: %s", a.activeModeLabel)).FontSize(11).Bold().TextColor(pal.Accent)
		ui.Spacer(c)
		ui.Text(c, a.statusNote).FontSize(11).TextColor(pal.TextMuted).SingleLine()
		ui.Text(c, fmt.Sprintf("会话:%d | Tx:%dKB/s | 丢包:%.2f%%",
			stats.ActiveSessions, stats.TxBytesPerSec/1024, stats.PacketLossRate*100)).FontSize(11).TextColor(pal.StatusBarText)
	})
}

const (
	seatCardMinWidth = 176
	seatGridGap      = 12
	seatGridPadding  = 12
)

// seatGridColumns picks how many seat cards fit in the scroll viewport width.
func seatGridColumns(viewportW float32) int {
	usable := viewportW - 2*seatGridPadding - 14 // minus scrollbar gutter
	if usable <= 0 {
		return 4
	}
	cols := int((usable + seatGridGap) / (seatCardMinWidth + seatGridGap))
	if cols < 1 {
		cols = 1
	}
	return cols
}

// monitorTileMinWidth keeps each monitor thumbnail readable while the grid
// stretches tiles to fill the available width.
const monitorTileMinWidth = 240

func monitorGridColumns(viewportW float32) int {
	usable := viewportW - 14
	if usable <= 0 {
		return 4
	}
	cols := int((usable + seatGridGap) / (monitorTileMinWidth + seatGridGap))
	if cols < 1 {
		cols = 1
	}
	return cols
}

// seatStatusPill renders the student state in strict monochrome, using fill,
// border and weight rather than color:
//   - hand raised: solid dark pill (most urgent)
//   - locked: gray filled pill with a lock icon
//   - remote/broadcast/exam: outlined pill
//   - online/offline: plain text (offline cards are faded as a whole)
func seatStatusPill(c *ui.Context, pal theme.Palette, st core.StudentInfo) {
	text, _ := statusBadge(pal, st)
	switch {
	case st.HandRaised || st.Status == core.StatusHandRaised:
		ui.Badge(c, text).Background(pal.Accent).TextColor(pal.AccentText).Bold()
	case st.Status == core.StatusLocked:
		ui.Row(c).AlignItems(ui.Center).Gap(3).Padding(1, 7).Radius(9).Background(pal.ChromeActive).Children(func() {
			ui.Image(c, theme.IconLockScreen).Size(11, 11)
			ui.Text(c, text).FontSize(10).Bold().TextColor(pal.Text)
		})
	case st.Status == core.StatusControlled, st.Status == core.StatusBroadcast, st.Status == core.StatusExamining:
		ui.Badge(c, text).Background(pal.Card).Border(1, pal.Text).TextColor(pal.Text)
	case st.Status == core.StatusOffline:
		ui.Text(c, text).FontSize(11).TextColor(pal.TextMuted)
	default:
		ui.Text(c, "● "+text).FontSize(11).Bold().TextColor(pal.Text)
	}
}

func statusBadge(pal theme.Palette, st core.StudentInfo) (string, ui.Color) {
	if st.HandRaised || st.Status == core.StatusHandRaised {
		return "举手", pal.Warning
	}
	switch st.Status {
	case core.StatusOffline:
		return "离线", pal.TextMuted
	case core.StatusLocked:
		return "锁屏", pal.Danger
	case core.StatusControlled:
		return "远控中", pal.Info
	case core.StatusBroadcast:
		return "接收广播", pal.Accent
	case core.StatusExamining:
		return "测验中", pal.Info
	default:
		return "在线", pal.Success
	}
}
