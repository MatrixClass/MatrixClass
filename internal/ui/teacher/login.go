package teacher

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/ui/theme"
)

func hashPassword(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("nexlab:" + raw))
	return hex.EncodeToString(sum[:])
}

func numericChannelOptions() []string {
	opts := make([]string, 32)
	for i := range opts {
		opts[i] = strconv.Itoa(i + 1)
	}
	return opts
}

func (a *App) SubmitStartupLogin() bool {
	user := strings.TrimSpace(a.loginUserInput)
	if user == "" {
		user = "admin"
		a.loginUserInput = user
	}
	if a.authPasswordHash == "" {
		a.authUsername = user
		if a.loginPassInput != "" {
			a.authPasswordHash = hashPassword(a.loginPassInput)
		}
		a.loginPassInput = ""
		a.loginError = ""
		a.ConfirmChannel(a.channelInput)
		return true
	}
	if user != a.authUsername || hashPassword(a.loginPassInput) != a.authPasswordHash {
		a.loginError = "账号或密码错误"
		return false
	}
	a.loginPassInput = ""
	a.loginError = ""
	a.ConfirmChannel(a.channelInput)
	return true
}

func (a *App) ConfirmChannel(ch string) {
	ch = normalizeChannelID(ch)
	a.channel = ch
	a.channelInput = ch
	a.channelNum = parseChannelNum(ch)
	a.channelModal = false
	if a.store != nil {
		_ = a.store.TouchChannel(ch)
		if recent, err := a.store.ListRecentChannels(8); err == nil {
			a.recentChannels = recent
		}
		a.savePersistedSettings()
	}
	a.statusNote = "当前课堂频道 ID: " + ch + " · 等待学生端加入"
	if a.onChannelSelected != nil {
		a.onChannelSelected(ch)
	}
}

func normalizeChannelID(raw string) string {
	raw = strings.TrimSpace(raw)
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return "1"
}

func parseChannelNum(raw string) float64 {
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		return float64(n)
	}
	return 1
}

func (a *App) buildChannelSelectModal(c *ui.Context, pal theme.Palette) {
	ui.Column(c).Width(420).Background(pal.Canvas).Border(1, pal.CardBorder).Radius(4).Padding(18).Gap(12).Children(func() {
		ui.Text(c, "教师端登录与频道选择").FontSize(16).Bold()
		ui.Text(c, "请选择数字频道 ID，学生端将按此频道 ID 自动连接").FontSize(12).TextColor(pal.TextMuted)
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "频道 ID").FontSize(12).Bold().Width(70)
			if ui.Select(c, &a.channelInput, numericChannelOptions()).Grow(1).Changed() {
				a.channelNum = parseChannelNum(a.channelInput)
			}
		})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "教师账号").FontSize(12).Bold().Width(70)
			ui.TextInput(c, &a.loginUserInput).Placeholder("默认: admin").Grow(1)
		})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "登录密码").FontSize(12).Bold().Width(70)
			ph := "首次可设置密码（留空则无密码直接登录）"
			if a.authPasswordHash != "" {
				ph = "请输入登录密码"
			}
			pw := ui.TextInput(c, &a.loginPassInput).Password().Placeholder(ph).Grow(1)
			if pw.Submitted() {
				a.SubmitStartupLogin()
			}
		})
		if a.loginError != "" {
			ui.Text(c, a.loginError).FontSize(12).TextColor(pal.Danger)
		}
		ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "进入课堂").Padding(5, 16).Clicked() {
				a.SubmitStartupLogin()
			}
		})
	})
}
