package teacher

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"nexlab/internal/core"
	"nexlab/internal/ui/theme"
)

func (a *App) buildGroupBottomBar(c *ui.Context, pal theme.Palette) {
	groups := a.listGroups()
	ui.Row(c).FillWidth().Padding(6, 12).Gap(6).Background(pal.ChromeBar).
		BorderWidth(1, 0, 0, 0).BorderColor(pal.CardBorder).AlignItems(ui.Center).Children(func() {
		defBtn := ui.Button(c, "全部设备").Padding(4, 12).Radius(3)
		if a.selectedGroup == "" || a.selectedGroup == "all" {
			defBtn.Background(pal.Selection).Border(1, pal.Accent)
		}
		if defBtn.Clicked() {
			a.selectedGroup = "all"
		}

		for _, g := range groups {
			g := g
			if a.renamingGroupID == g.ID {
				inp := ui.TextInput(c, &a.renameGroupInput).Key("grp-rename-" + g.ID).Width(130).AutoFocus()
				if inp.Submitted() || ui.PrimaryButton(c, "确定").Key("grp-rename-ok-"+g.ID).Padding(4, 10).Clicked() {
					a.renameGroup(g.ID, a.renameGroupInput)
				}
				if ui.Button(c, "取消").Key("grp-rename-cancel-"+g.ID).Padding(4, 8).Clicked() {
					a.renamingGroupID = ""
				}
				continue
			}
			btn := ui.Button(c, g.Name).Key("grp-"+g.ID).Padding(4, 12).Radius(3)
			if a.selectedGroup == g.ID {
				btn.Background(pal.Selection).Border(1, pal.Accent)
			}
			btn.Tooltip("右键可重命名或删除分组")
			btn.ContextMenu(func(m *ui.Menu) {
				if m.Item("重命名分组").Chosen() {
					a.renamingGroupID = g.ID
					a.renameGroupInput = g.Name
				}
				if m.Item("删除分组").Chosen() {
					a.deleteGroup(g.ID)
				}
			})
			if btn.Clicked() {
				a.selectedGroup = g.ID
			}
		}

		if a.creatingGroup {
			inp := ui.TextInput(c, &a.newGroupInput).Placeholder("输入分组名称...").Width(130)
			if inp.Submitted() || ui.PrimaryButton(c, "确定").Padding(4, 10).Clicked() {
				a.createGroup(a.newGroupInput)
			}
			if ui.Button(c, "取消").Padding(4, 8).Clicked() {
				a.creatingGroup = false
				a.newGroupInput = ""
			}
		} else {
			if ui.Button(c, "+").Tooltip("创建新分组").Padding(4, 10).Radius(3).Clicked() {
				a.creatingGroup = true
				a.newGroupInput = ""
			}
		}
		ui.Spacer(c)
	})
}

func (a *App) createGroup(name string) {
	a.createGroupNamed(name)
}

// saveGroupList pushes a group list into the classroom and persists it.
func (a *App) saveGroupList(groups []core.GroupInfo) {
	if a.opts.Classroom != nil {
		a.opts.Classroom.SetGroups(groups)
	}
	if a.store != nil {
		_ = a.store.SaveGroups(groups)
	}
}

func (a *App) renameGroup(id, name string) {
	name = strings.TrimSpace(name)
	a.renamingGroupID = ""
	if name == "" {
		return
	}
	groups := a.listGroups()
	for i := range groups {
		if groups[i].ID == id {
			groups[i].Name = name
		}
	}
	a.saveGroupList(groups)
	a.statusNote = "已重命名分组: " + name
}

func (a *App) deleteGroup(id string) {
	groups := a.listGroups()
	kept := groups[:0]
	name := ""
	for _, g := range groups {
		if g.ID == id {
			name = g.Name
			continue
		}
		kept = append(kept, g)
	}
	a.saveGroupList(kept)
	if a.selectedGroup == id {
		a.selectedGroup = "all"
	}
	a.statusNote = "已删除分组: " + name + "（组内设备回到全部设备）"
}

func (a *App) createGroupNamed(name string) {
	name = strings.TrimSpace(name)
	groups := a.listGroups()
	if name == "" {
		name = fmt.Sprintf("分组 %d", len(groups)+1)
	}
	id := fmt.Sprintf("grp-%d", time.Now().UnixNano())
	groups = append(groups, core.GroupInfo{
		ID:   id,
		Name: name,
	})
	if a.opts.Classroom != nil {
		a.opts.Classroom.SetGroups(groups)
	}
	if a.store != nil {
		_ = a.store.SaveGroups(groups)
	}
	a.selectedGroup = id
	a.creatingGroup = false
	a.newGroupInput = ""
	a.statusNote = "已创建分组: " + name
}

func (a *App) assignTargetsToGroup(groupID string, targets []string) {
	if a.opts.Classroom == nil || len(targets) == 0 {
		return
	}
	type memberAdder interface {
		AddGroupMember(groupID, studentID string) error
		RemoveGroupMember(groupID, studentID string) error
	}
	if adder, ok := a.opts.Classroom.(memberAdder); ok && groupID != "" && groupID != "all" {
		for _, id := range targets {
			_ = adder.AddGroupMember(groupID, id)
		}
		if a.store != nil {
			_ = a.store.SaveGroups(a.opts.Classroom.ListGroups())
		}
		a.statusNote = fmt.Sprintf("已将 %d 台设备移入分组", len(targets))
		return
	}
	groups := a.listGroups()
	targetSet := make(map[string]bool, len(targets))
	for _, id := range targets {
		targetSet[id] = true
	}
	for gi := range groups {
		kept := groups[gi].Members[:0]
		for _, mid := range groups[gi].Members {
			if !targetSet[mid] {
				kept = append(kept, mid)
			}
		}
		groups[gi].Members = kept
		if groups[gi].ID == groupID {
			groups[gi].Members = append(groups[gi].Members, targets...)
		}
	}
	a.opts.Classroom.SetGroups(groups)
	if a.store != nil {
		_ = a.store.SaveGroups(groups)
	}
	if groupID == "" || groupID == "all" {
		a.statusNote = fmt.Sprintf("已将 %d 台设备移回默认分组", len(targets))
	} else {
		a.statusNote = fmt.Sprintf("已将 %d 台设备移入分组", len(targets))
	}
}

func (a *App) listGroups() []core.GroupInfo {
	if a.opts.Classroom == nil {
		return nil
	}
	return a.opts.Classroom.ListGroups()
}
