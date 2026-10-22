package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/logic/scheduledtransfer"
	"tui-ssh-workbench/internal/model"
)

type scheduledTransferManagerState struct {
	transfers        []model.ScheduledTransfer
	hosts            []model.Host
	index            int
	loading          bool
	busy             bool
	editing          bool
	confirmingDelete bool
	edit             model.ScheduledTransfer
	field            int
	cursor           int
	errorText        string
}

const (
	scheduledFieldName = iota
	scheduledFieldHost
	scheduledFieldDirection
	scheduledFieldLocalPath
	scheduledFieldRemotePath
	scheduledFieldCron
	scheduledFieldTimeZone
	scheduledFieldEnabled
	scheduledFieldOverwrite
)

type scheduledTransfersLoadedMsg struct {
	transfers []model.ScheduledTransfer
	err       error
}

type scheduledTransferSavedMsg struct {
	transfer model.ScheduledTransfer
	err      error
}

type scheduledTransferDeletedMsg struct {
	id  string
	err error
}

type scheduledTransferRequestedMsg struct {
	id  string
	err error
}

type scheduledTransfersPolledMsg struct {
	transfers []model.ScheduledTransfer
	err       error
}

type scheduledTransfersPollTickMsg struct{}

type scheduledTransferRunnerFailedMsg struct {
	err error
}

// ScheduledTransferRunnerFailed creates a UI message for an unexpected error
// from the workbench-managed scheduled-transfer runner.
func ScheduledTransferRunnerFailed(err error) tea.Msg {
	return scheduledTransferRunnerFailedMsg{err: err}
}

type scheduledTransferNoticeExpiredMsg struct{ id uint64 }

const (
	scheduledTransfersPollInterval  = 5 * time.Second
	scheduledTransferNoticeDuration = 8 * time.Second
)

func (m *modelState) openScheduledTransferManager() tea.Cmd {
	if m.loading {
		m.statusText, m.statusErr = "请等待本地主机目录加载完成", false
		return nil
	}
	if _, ok := m.catalog.(scheduledTransferCatalog); !ok {
		m.statusText, m.statusErr = "当前存储不支持计划传输", true
		return nil
	}
	hosts := make([]model.Host, 0, len(m.hosts))
	for _, host := range m.hosts {
		if host.Kind == model.HostKindSSH || host.Kind == model.HostKindFTP {
			hosts = append(hosts, host)
		}
	}
	sort.Slice(hosts, func(i, j int) bool {
		return strings.ToLower(hosts[i].Name) < strings.ToLower(hosts[j].Name)
	})
	m.scheduledTransfers = &scheduledTransferManagerState{loading: true, hosts: hosts}
	return m.loadScheduledTransfersCmd()
}

func (m modelState) loadScheduledTransfersCmd() tea.Cmd {
	store, ok := m.catalog.(scheduledTransferCatalog)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		transfers, err := store.ListScheduledTransfers(context.Background())
		return scheduledTransfersLoadedMsg{transfers: transfers, err: err}
	}
}

func (m modelState) pollScheduledTransfersCmd() tea.Cmd {
	store, ok := m.catalog.(scheduledTransferCatalog)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		transfers, err := store.ListScheduledTransfers(context.Background())
		return scheduledTransfersPolledMsg{transfers: transfers, err: err}
	}
}

func (m modelState) scheduledTransfersPollTickCmd() tea.Cmd {
	return tea.Tick(scheduledTransfersPollInterval, func(time.Time) tea.Msg {
		return scheduledTransfersPollTickMsg{}
	})
}

func (m *modelState) handleScheduledTransfersPolled(transfers []model.ScheduledTransfer) tea.Cmd {
	if m.scheduledTransferLastFinish == nil {
		m.scheduledTransferLastFinish = make(map[string]time.Time)
	}
	updated := make(map[string]time.Time, len(transfers))
	var completed []model.ScheduledTransfer
	for _, transfer := range transfers {
		previous, seen := m.scheduledTransferLastFinish[transfer.Id]
		finished := !transfer.LastFinishAt.IsZero() &&
			((seen && transfer.LastFinishAt.After(previous)) ||
				(!seen && !m.scheduledTransfersStartedAt.IsZero() && transfer.LastFinishAt.After(m.scheduledTransfersStartedAt)))
		if finished && (transfer.LastStatus == model.ScheduledTransferSucceeded ||
			transfer.LastStatus == model.ScheduledTransferFailed || transfer.LastStatus == model.ScheduledTransferCanceled) {
			completed = append(completed, transfer)
		}
		updated[transfer.Id] = transfer.LastFinishAt
	}
	m.scheduledTransferLastFinish = updated
	if len(completed) == 0 {
		return nil
	}
	return m.showScheduledTransferNotice(completed)
}

func (m *modelState) showScheduledTransferNotice(transfers []model.ScheduledTransfer) tea.Cmd {
	text := ""
	isError := false
	if len(transfers) == 1 {
		transfer := transfers[0]
		label := "成功"
		switch transfer.LastStatus {
		case model.ScheduledTransferFailed:
			label, isError = "失败", true
		case model.ScheduledTransferCanceled:
			label, isError = "已取消", true
		}
		text = fmt.Sprintf("计划传输%s：%s", label, transfer.Name)
	} else {
		succeeded, failed, canceled := 0, 0, 0
		for _, transfer := range transfers {
			switch transfer.LastStatus {
			case model.ScheduledTransferSucceeded:
				succeeded++
			case model.ScheduledTransferFailed:
				failed++
			case model.ScheduledTransferCanceled:
				canceled++
			}
		}
		isError = failed > 0 || canceled > 0
		text = fmt.Sprintf("计划传输结果：%d 项完成，成功 %d，失败 %d，取消 %d", len(transfers), succeeded, failed, canceled)
	}
	return m.showInAppTransferNotification(text, isError)
}

func (m *modelState) showTransferNotification(text string, isError bool) tea.Cmd {
	commands := []tea.Cmd{m.showInAppTransferNotification(text, isError)}
	if m.config.Notifications.Enabled && m.desktopNotifier != nil {
		title := "文件传输完成"
		if isError {
			title = "文件传输未完成"
		}
		commands = append(commands, m.desktopTransferNotificationCmd(title, text))
	}
	return tea.Batch(commands...)
}

func (m *modelState) showInAppTransferNotification(text string, isError bool) tea.Cmd {
	m.scheduledTransferNoticeID++
	message := scheduledTransferNoticeExpiredMsg{id: m.scheduledTransferNoticeID}
	m.scheduledTransferNotice = text
	m.scheduledTransferNoticeErr = isError
	return tea.Tick(scheduledTransferNoticeDuration, func(time.Time) tea.Msg { return message })
}

func (m modelState) desktopTransferNotificationCmd(title, body string) tea.Cmd {
	notifier := m.desktopNotifier
	if notifier == nil {
		return nil
	}
	return func() tea.Msg {
		_ = notifier.Notify(title, body)
		return nil
	}
}

func (m modelState) saveScheduledTransferCmd(transfer model.ScheduledTransfer) tea.Cmd {
	store, ok := m.catalog.(scheduledTransferCatalog)
	if !ok {
		return func() tea.Msg { return scheduledTransferSavedMsg{err: fmt.Errorf("计划任务存储不可用")} }
	}
	return func() tea.Msg {
		saved, err := store.SaveScheduledTransfer(context.Background(), transfer)
		return scheduledTransferSavedMsg{transfer: saved, err: err}
	}
}

func (m modelState) deleteScheduledTransferCmd(id string) tea.Cmd {
	store, ok := m.catalog.(scheduledTransferCatalog)
	if !ok {
		return func() tea.Msg {
			return scheduledTransferDeletedMsg{id: id, err: fmt.Errorf("计划任务存储不可用")}
		}
	}
	return func() tea.Msg {
		return scheduledTransferDeletedMsg{id: id, err: store.DeleteScheduledTransfer(context.Background(), id)}
	}
}

func (m modelState) requestScheduledTransferNowCmd(id string) tea.Cmd {
	store, ok := m.catalog.(scheduledTransferCatalog)
	if !ok {
		return func() tea.Msg {
			return scheduledTransferRequestedMsg{id: id, err: fmt.Errorf("计划任务存储不可用")}
		}
	}
	return func() tea.Msg {
		return scheduledTransferRequestedMsg{id: id, err: store.RequestScheduledTransferNow(context.Background(), id)}
	}
}

func (m *modelState) handleScheduledTransfersLoaded(msg scheduledTransfersLoadedMsg) {
	state := m.scheduledTransfers
	if state == nil {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.errorText = "读取计划任务失败：" + msg.err.Error()
		return
	}
	state.transfers = msg.transfers
	sort.Slice(state.transfers, func(i, j int) bool {
		left, right := strings.ToLower(state.transfers[i].Name), strings.ToLower(state.transfers[j].Name)
		if left == right {
			return state.transfers[i].Id < state.transfers[j].Id
		}
		return left < right
	})
	state.index = clamp(state.index, 0, max(0, len(state.transfers)-1))
	state.errorText = ""
}

func (m *modelState) handleScheduledTransferSaved(msg scheduledTransferSavedMsg) {
	state := m.scheduledTransfers
	if state == nil {
		return
	}
	state.busy = false
	if msg.err != nil {
		state.errorText = "保存计划任务失败：" + msg.err.Error()
		return
	}
	index := scheduledTransferIndex(state.transfers, msg.transfer.Id)
	if index >= 0 {
		state.transfers[index] = msg.transfer
	} else {
		state.transfers = append(state.transfers, msg.transfer)
	}
	sort.Slice(state.transfers, func(i, j int) bool {
		return strings.ToLower(state.transfers[i].Name) < strings.ToLower(state.transfers[j].Name)
	})
	state.index = scheduledTransferIndex(state.transfers, msg.transfer.Id)
	state.editing = false
	state.errorText = ""
	m.statusText, m.statusErr = "计划传输已保存", false
}

func (m *modelState) handleScheduledTransferDeleted(msg scheduledTransferDeletedMsg) {
	state := m.scheduledTransfers
	if state == nil {
		return
	}
	state.busy = false
	state.confirmingDelete = false
	if msg.err != nil {
		state.errorText = "删除计划任务失败：" + msg.err.Error()
		return
	}
	if index := scheduledTransferIndex(state.transfers, msg.id); index >= 0 {
		state.transfers = append(state.transfers[:index], state.transfers[index+1:]...)
	}
	state.index = clamp(state.index, 0, max(0, len(state.transfers)-1))
	state.errorText = ""
	m.statusText, m.statusErr = "计划传输已删除", false
}

func (m *modelState) handleScheduledTransferRequested(msg scheduledTransferRequestedMsg) {
	state := m.scheduledTransfers
	if state == nil {
		return
	}
	state.busy = false
	if msg.err != nil {
		state.errorText = "立即执行请求失败：" + msg.err.Error()
		return
	}
	if index := scheduledTransferIndex(state.transfers, msg.id); index >= 0 {
		state.transfers[index].RunRequested = true
	}
	state.errorText = ""
	m.statusText, m.statusErr = "已排入立即执行队列；工作台运行期间会自动执行，退出后可由 --run-scheduled runner 接管", false
}

func scheduledTransferIndex(transfers []model.ScheduledTransfer, id string) int {
	for index := range transfers {
		if transfers[index].Id == id {
			return index
		}
	}
	return -1
}

func (m *modelState) beginScheduledTransferEdit(transfer *model.ScheduledTransfer) {
	state := m.scheduledTransfers
	if state == nil || state.busy || state.loading {
		return
	}
	state.editing = true
	state.confirmingDelete = false
	state.errorText = ""
	state.field, state.cursor = scheduledFieldName, 0
	state.edit = model.ScheduledTransfer{
		Direction: model.ScheduledTransferUpload,
		CronSpec:  "0 2 * * *",
		Enabled:   true,
	}
	if len(state.hosts) > 0 {
		state.edit.HostId = state.hosts[0].Id
	}
	if transfer != nil {
		state.edit = *transfer
	}
	state.cursor = len([]rune(state.edit.Name))
}

func (m *modelState) updateScheduledTransferKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.scheduledTransfers
	if state == nil {
		return m, nil
	}
	key := msg.Key()
	if state.editing {
		return m.updateScheduledTransferEditorKey(key)
	}
	if state.confirmingDelete {
		if state.busy {
			return m, nil
		}
		switch {
		case key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "n"):
			state.confirmingDelete = false
		case key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y"):
			if state.index < 0 || state.index >= len(state.transfers) {
				state.confirmingDelete = false
				return m, nil
			}
			state.busy = true
			return m, m.deleteScheduledTransferCmd(state.transfers[state.index].Id)
		}
		return m, nil
	}
	if key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "q") {
		m.scheduledTransfers = nil
		return m, nil
	}
	if state.loading || state.busy {
		return m, nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		state.index = clamp(state.index-1, 0, max(0, len(state.transfers)-1))
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		state.index = clamp(state.index+1, 0, max(0, len(state.transfers)-1))
	case key.Code == tea.KeyPgUp:
		state.index = clamp(state.index-8, 0, max(0, len(state.transfers)-1))
	case key.Code == tea.KeyPgDown:
		state.index = clamp(state.index+8, 0, max(0, len(state.transfers)-1))
	case strings.EqualFold(key.Text, "r"):
		state.loading = true
		return m, m.loadScheduledTransfersCmd()
	case strings.EqualFold(key.Text, "n"):
		if len(state.hosts) == 0 {
			state.errorText = "请先添加 SSH/SFTP、SSH/SCP 或 FTP/FTPS 主机"
			return m, nil
		}
		m.beginScheduledTransferEdit(nil)
	case strings.EqualFold(key.Text, "e"):
		if state.index >= 0 && state.index < len(state.transfers) {
			transfer := state.transfers[state.index]
			if transfer.LastStatus == model.ScheduledTransferRunning || transfer.RunRequested {
				state.errorText = "运行中或等待立即执行的计划任务不能编辑"
				return m, nil
			}
			m.beginScheduledTransferEdit(&transfer)
		}
	case strings.EqualFold(key.Text, "i"):
		if state.index >= 0 && state.index < len(state.transfers) {
			transfer := state.transfers[state.index]
			if transfer.LastStatus == model.ScheduledTransferRunning {
				state.errorText = "运行中的计划任务不能重复执行"
				return m, nil
			}
			if transfer.RunRequested {
				state.errorText = "此任务已在立即执行队列中"
				return m, nil
			}
			state.busy = true
			state.errorText = ""
			return m, m.requestScheduledTransferNowCmd(transfer.Id)
		}
	case strings.EqualFold(key.Text, "d"):
		if state.index >= 0 && state.index < len(state.transfers) {
			state.confirmingDelete = true
			state.errorText = ""
		}
	case strings.EqualFold(key.Text, "t"):
		if state.index >= 0 && state.index < len(state.transfers) {
			transfer := state.transfers[state.index]
			if transfer.LastStatus == model.ScheduledTransferRunning || transfer.RunRequested {
				state.errorText = "运行中或等待立即执行的计划任务不能切换状态"
				return m, nil
			}
			transfer.Enabled = !transfer.Enabled
			if transfer.Enabled {
				next, err := scheduledtransfer.NextRunInLocation(transfer.CronSpec, transfer.TimeZone, time.Now())
				if err != nil {
					state.errorText = "计划表达式无效：" + err.Error()
					return m, nil
				}
				transfer.NextRunAt = next
			} else {
				transfer.NextRunAt = time.Time{}
			}
			state.busy = true
			return m, m.saveScheduledTransferCmd(transfer)
		}
	}
	return m, nil
}

func (m *modelState) updateScheduledTransferEditorKey(key tea.Key) (tea.Model, tea.Cmd) {
	state := m.scheduledTransfers
	if state == nil || state.busy {
		return m, nil
	}
	if key.Code == tea.KeyEscape {
		state.editing = false
		state.errorText = ""
		return m, nil
	}
	if key.Mod&tea.ModCtrl != 0 && (key.Code == 's' || key.Code == 'S' || key.Code == tea.KeyEnter) {
		return m.saveScheduledTransfer()
	}
	if key.Code == tea.KeyTab || key.Code == tea.KeyDown {
		state.field = (state.field + 1) % 9
		state.cursor = len([]rune(scheduledFieldValue(state.edit, state.field)))
		return m, nil
	}
	if key.Code == tea.KeyUp {
		state.field = (state.field + 8) % 9
		state.cursor = len([]rune(scheduledFieldValue(state.edit, state.field)))
		return m, nil
	}
	if key.Code == tea.KeyEnter {
		state.field = (state.field + 1) % 9
		state.cursor = len([]rune(scheduledFieldValue(state.edit, state.field)))
		return m, nil
	}
	if state.field == scheduledFieldHost && (key.Code == tea.KeyLeft || key.Code == tea.KeyRight) {
		if len(state.hosts) > 0 {
			current := scheduledTransferHostIndex(state.hosts, state.edit.HostId)
			delta := 1
			if key.Code == tea.KeyLeft {
				delta = -1
			}
			state.edit.HostId = state.hosts[(current+delta+len(state.hosts))%len(state.hosts)].Id
		}
		return m, nil
	}
	if state.field == scheduledFieldDirection && (key.Code == tea.KeyLeft || key.Code == tea.KeyRight || key.Text == " ") {
		if state.edit.Direction == model.ScheduledTransferUpload {
			state.edit.Direction = model.ScheduledTransferDownload
		} else {
			state.edit.Direction = model.ScheduledTransferUpload
		}
		return m, nil
	}
	if (state.field == scheduledFieldEnabled || state.field == scheduledFieldOverwrite) && (key.Text == " " || key.Code == tea.KeyLeft || key.Code == tea.KeyRight) {
		if state.field == scheduledFieldEnabled {
			state.edit.Enabled = !state.edit.Enabled
		} else {
			state.edit.Overwrite = !state.edit.Overwrite
		}
		return m, nil
	}
	if state.field == scheduledFieldHost || state.field == scheduledFieldDirection || state.field == scheduledFieldEnabled || state.field == scheduledFieldOverwrite {
		return m, nil
	}
	value := []rune(scheduledFieldValue(state.edit, state.field))
	state.cursor = clamp(state.cursor, 0, len(value))
	switch key.Code {
	case tea.KeyLeft:
		state.cursor = max(0, state.cursor-1)
	case tea.KeyRight:
		state.cursor = min(len(value), state.cursor+1)
	case tea.KeyHome:
		state.cursor = 0
	case tea.KeyEnd:
		state.cursor = len(value)
	case tea.KeyBackspace:
		if state.cursor > 0 {
			value = append(value[:state.cursor-1], value[state.cursor:]...)
			state.cursor--
		}
	case tea.KeyDelete:
		if state.cursor < len(value) {
			value = append(value[:state.cursor], value[state.cursor+1:]...)
		}
	default:
		if key.Mod == 0 && key.Text != "" {
			limit := 4096
			if state.field == scheduledFieldName {
				limit = 80
			} else if state.field == scheduledFieldCron {
				limit = scheduledtransfer.MaxCronSpecLength
			} else if state.field == scheduledFieldTimeZone {
				limit = 128
			}
			for _, character := range key.Text {
				if unicode.IsControl(character) || len(value) >= limit {
					continue
				}
				value = insertQuickCommandRune(value, state.cursor, character)
				state.cursor++
			}
		}
	}
	setScheduledFieldValue(&state.edit, state.field, string(value))
	state.errorText = ""
	return m, nil
}

func (m *modelState) appendScheduledTransferPaste(content string) {
	state := m.scheduledTransfers
	if state == nil || !state.editing || state.busy || state.field == scheduledFieldHost || state.field == scheduledFieldDirection || state.field == scheduledFieldEnabled || state.field == scheduledFieldOverwrite {
		return
	}
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", " "), "\n", " ")
	content = strings.ReplaceAll(content, "\r", " ")
	value := []rune(scheduledFieldValue(state.edit, state.field))
	limit := 4096
	if state.field == scheduledFieldName {
		limit = 80
	} else if state.field == scheduledFieldCron {
		limit = scheduledtransfer.MaxCronSpecLength
	} else if state.field == scheduledFieldTimeZone {
		limit = 128
	}
	for _, character := range content {
		if unicode.IsControl(character) || len(value) >= limit {
			continue
		}
		value = insertQuickCommandRune(value, state.cursor, character)
		state.cursor++
	}
	setScheduledFieldValue(&state.edit, state.field, string(value))
	state.errorText = ""
}

func scheduledFieldValue(transfer model.ScheduledTransfer, field int) string {
	switch field {
	case scheduledFieldName:
		return transfer.Name
	case scheduledFieldLocalPath:
		return transfer.LocalPath
	case scheduledFieldRemotePath:
		return transfer.RemotePath
	case scheduledFieldCron:
		return transfer.CronSpec
	case scheduledFieldTimeZone:
		return transfer.TimeZone
	default:
		return ""
	}
}

func setScheduledFieldValue(transfer *model.ScheduledTransfer, field int, value string) {
	switch field {
	case scheduledFieldName:
		transfer.Name = value
	case scheduledFieldLocalPath:
		transfer.LocalPath = value
	case scheduledFieldRemotePath:
		transfer.RemotePath = value
	case scheduledFieldCron:
		transfer.CronSpec = value
	case scheduledFieldTimeZone:
		transfer.TimeZone = value
	}
}

func scheduledTransferHostIndex(hosts []model.Host, id string) int {
	for index := range hosts {
		if hosts[index].Id == id {
			return index
		}
	}
	return 0
}

func (m *modelState) saveScheduledTransfer() (tea.Model, tea.Cmd) {
	state := m.scheduledTransfers
	if state == nil {
		return m, nil
	}
	if strings.TrimSpace(state.edit.Name) == "" {
		state.errorText = "请填写计划任务名称"
		state.field = scheduledFieldName
		return m, nil
	}
	if state.edit.HostId == "" {
		state.errorText = "请选择 SSH/SFTP、SSH/SCP 或 FTP/FTPS 主机"
		state.field = scheduledFieldHost
		return m, nil
	}
	if strings.TrimSpace(state.edit.LocalPath) == "" || strings.TrimSpace(state.edit.RemotePath) == "" {
		state.errorText = "请填写本地路径和远程路径"
		if strings.TrimSpace(state.edit.LocalPath) == "" {
			state.field = scheduledFieldLocalPath
		} else {
			state.field = scheduledFieldRemotePath
		}
		return m, nil
	}
	state.edit.LocalPath = expandLocalPath(state.edit.LocalPath)
	localPath, err := filepath.Abs(state.edit.LocalPath)
	if err != nil {
		state.errorText = "解析本地路径失败：" + err.Error()
		state.field = scheduledFieldLocalPath
		return m, nil
	}
	state.edit.LocalPath = localPath
	if _, statErr := os.Lstat(localPath); statErr != nil && !os.IsNotExist(statErr) {
		state.errorText = "读取本地路径失败：" + statErr.Error()
		state.field = scheduledFieldLocalPath
		return m, nil
	}
	state.edit.RemotePath = strings.TrimSpace(state.edit.RemotePath)
	state.edit.TimeZone = strings.TrimSpace(state.edit.TimeZone)
	if state.edit.TimeZone == "" {
		state.edit.TimeZone = "Local"
	}
	if err := scheduledtransfer.ValidateTimeZone(state.edit.TimeZone); err != nil {
		state.errorText = err.Error()
		state.field = scheduledFieldTimeZone
		return m, nil
	}
	if state.edit.Enabled {
		next, err := scheduledtransfer.NextRunInLocation(state.edit.CronSpec, state.edit.TimeZone, time.Now())
		if err != nil {
			state.errorText = "计划表达式无效：" + err.Error()
			state.field = scheduledFieldCron
			return m, nil
		}
		state.edit.NextRunAt = next
	} else {
		state.edit.NextRunAt = time.Time{}
	}
	state.busy = true
	state.errorText = ""
	return m, m.saveScheduledTransferCmd(state.edit)
}

func (m modelState) scheduledTransferManagerView() string {
	state := m.scheduledTransfers
	if state == nil {
		return ""
	}
	if state.editing {
		return m.scheduledTransferEditorView()
	}
	if state.confirmingDelete && state.index >= 0 && state.index < len(state.transfers) {
		transfer := state.transfers[state.index]
		lines := []string{
			warningStyle.Render("此操作无法撤销。"),
			"确定删除计划任务「" + transfer.Name + "」吗？",
			"已完成的传输结果和任务记录也会一并删除。",
			"",
			helpKeyStyle.Render("Enter / y ") + "确认   " + helpKeyStyle.Render("Esc / n ") + "取消",
		}
		if state.busy {
			lines = append(lines, "", accentStyle.Render("◌ 正在删除…"))
		}
		return m.modalView("删除计划传输", lines, 76)
	}
	lines := []string{
		mutedStyle.Render("工作台打开时自动执行；关闭后可用 --run-scheduled 继续运行。"),
		mutedStyle.Render("SSH 需已信任主机指纹；FTP 密码需保存到系统钥匙串。覆盖由每个任务单独控制。"),
	}
	if state.loading {
		lines = append(lines, "", accentStyle.Render("◌ 正在读取计划任务…"))
	} else if len(state.transfers) == 0 {
		lines = append(lines, "", mutedStyle.Render("暂无计划任务，按 n 新增。"))
	} else {
		lines = append(lines, "", mutedStyle.Render("名称              状态       方向       下次运行                主机 / 远程路径"))
		visible := min(len(state.transfers), 8)
		start := clamp(state.index-visible+1, 0, max(0, len(state.transfers)-visible))
		for index := start; index < start+visible; index++ {
			transfer := state.transfers[index]
			hostName := scheduledTransferHostName(state.hosts, transfer.HostId)
			direction := "上传"
			if transfer.Direction == model.ScheduledTransferDownload {
				direction = "下载"
			}
			nextRun := "已停用"
			if transfer.Enabled && !transfer.NextRunAt.IsZero() {
				location, err := scheduledtransfer.LoadLocation(transfer.TimeZone)
				if err != nil {
					location = time.Local
				}
				nextRun = transfer.NextRunAt.In(location).Format("2006-01-02 15:04 -0700")
			}
			status := scheduledTransferStatusLabel(transfer.LastStatus)
			if transfer.RunRequested {
				status = "待立即执行"
			} else if transfer.LastStatus == model.ScheduledTransferRunning && transfer.LeaseUntil.Before(time.Now()) {
				status = "待恢复"
			}
			line := fmt.Sprintf("%-16s  %-8s  %-8s  %-19s  %s  %s",
				fitText(transfer.Name, 16), status, direction, nextRun,
				fitText(hostName, 14), fitText(transfer.RemotePath, 24))
			if index == state.index {
				line = selectedAccent.Render("› ") + line
			} else {
				line = "  " + line
			}
			lines = append(lines, line)
			if transfer.LastError != "" && index == state.index {
				lines = append(lines, warningStyle.Render("  上次错误："+fitText(sanitizeRemoteText(transfer.LastError), 76)))
			}
		}
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	lines = append(lines, "", mutedStyle.Render("↑/↓ 选择 · n 新增 · e 编辑 · i 立即执行 · t 启用/停用 · d 删除 · r 刷新 · Esc 关闭"))
	return m.modalView("计划传输管理", lines, 112)
}

func (m modelState) scheduledTransferEditorView() string {
	state := m.scheduledTransfers
	title := "新建计划传输"
	if state.edit.Id != "" {
		title = "编辑计划传输"
	}
	values := []string{
		state.edit.Name,
		scheduledTransferHostName(state.hosts, state.edit.HostId),
		"上传文件 / 目录",
		state.edit.LocalPath,
		state.edit.RemotePath,
		state.edit.CronSpec,
		state.edit.TimeZone,
		"停用",
		"跳过已有目标",
	}
	selectedHost := scheduledTransferHost(state.hosts, state.edit.HostId)
	usesSCP := selectedHost != nil && selectedHost.FileProtocol == model.SSHFileProtocolSCP
	if state.edit.Direction == model.ScheduledTransferDownload {
		values[scheduledFieldDirection] = "下载文件 / 目录"
	}
	if usesSCP {
		values[scheduledFieldDirection] = "上传文件 / 目录"
		if state.edit.Direction == model.ScheduledTransferDownload {
			values[scheduledFieldDirection] = "下载文件 / 目录"
		}
	}
	if state.edit.Enabled {
		values[scheduledFieldEnabled] = "启用"
	}
	if state.edit.Overwrite {
		values[scheduledFieldOverwrite] = "覆盖目标"
	}
	labels := []string{"名称", "主机", "方向", "本地路径", "远程路径", "Cron 表达式", "时区", "任务状态", "目标冲突"}
	lines := []string{
		mutedStyle.Render("本地路径支持 ~ 展开，保存为绝对路径。目录递归传输会跳过符号链接和特殊文件。"),
		mutedStyle.Render("Cron 使用五字段格式，例如 `0 2 * * *`；也支持 `@hourly` 和 `@every 30m`。"),
		mutedStyle.Render("时区使用 IANA 名称，例如 Asia/Shanghai；留空或填写 Local 表示运行机器时区。@every 按固定时长计算。"),
		"",
	}
	if usesSCP {
		lines = append(lines, warningStyle.Render("SCP 文件操作依赖远端 legacy SCP 与 POSIX shell；文件工作区目录列表还需要 GNU/BSD stat。"))
	}
	for index, label := range labels {
		value := values[index]
		if index == scheduledFieldName || index == scheduledFieldLocalPath || index == scheduledFieldRemotePath || index == scheduledFieldCron || index == scheduledFieldTimeZone {
			value = scheduledTransferEditField(value, state.cursor, state.field == index, max(18, min(84, m.width-32)))
		} else if state.field == index {
			value = selectedAccent.Render("‹ " + value + " ›")
		}
		lines = append(lines, selectedStyle.Render(label+"  ")+value)
	}
	lines = append(lines, "", mutedStyle.Render("Tab / ↑↓ 移动 · ←/→ 切换选项 · 空格切换开关 · Ctrl+S 保存 · Esc 取消"))
	if state.busy {
		lines = append(lines, "", accentStyle.Render("◌ 正在保存到 SQLite…"))
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	return m.modalView(title, lines, 104)
}

func scheduledTransferEditField(value string, cursor int, active bool, width int) string {
	runes := []rune(value)
	if !active {
		return fitText(value, width)
	}
	cursor = clamp(cursor, 0, len(runes))
	withCursor := append([]rune(nil), runes[:cursor]...)
	withCursor = append(withCursor, '▏')
	withCursor = append(withCursor, runes[cursor:]...)
	return fitText(string(withCursor), width)
}

func scheduledTransferHostName(hosts []model.Host, id string) string {
	if host := scheduledTransferHost(hosts, id); host != nil {
		label := fmt.Sprintf("%s (%s)", host.Name, host.IP)
		if host.FileProtocol == model.SSHFileProtocolSCP {
			label += " [SCP]"
		}
		return label
	}
	if id != "" {
		return "主机已删除"
	}
	return "未选择"
}

func scheduledTransferHost(hosts []model.Host, id string) *model.Host {
	for index := range hosts {
		if hosts[index].Id == id {
			return &hosts[index]
		}
	}
	return nil
}

func scheduledTransferStatusLabel(status model.ScheduledTransferStatus) string {
	switch status {
	case model.ScheduledTransferRunning:
		return "运行中"
	case model.ScheduledTransferSucceeded:
		return "成功"
	case model.ScheduledTransferFailed:
		return "失败"
	case model.ScheduledTransferCanceled:
		return "已取消"
	default:
		return "待运行"
	}
}
