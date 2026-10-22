package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

type portForwardManagerState struct {
	paneID              string
	kind                sshclient.ForwardKind
	selected            int
	focusSaved          bool
	focusChannels       bool
	editing             bool
	field               int
	localAddress        string
	remote              string
	starting            bool
	loadingRules        bool
	savingRule          bool
	deletingRule        bool
	confirmDeleteRuleID string
	savedRules          []model.PortForwardRule
	noticeText          string
	errorText           string
}

type activeForwardChannel struct {
	forward *sshclient.LocalForward
	info    sshclient.ForwardChannelInfo
}

type portForwardStatsTickMsg struct{ paneID string }

type portForwardChannelClosedMsg struct {
	paneID    string
	forward   *sshclient.LocalForward
	channelID uint64
	err       error
}

type portForwardStartedMsg struct {
	paneID  string
	local   string
	remote  string
	forward *sshclient.LocalForward
	rule    *model.PortForwardRule
	err     error
}

type portForwardRulesListedMsg struct {
	paneID string
	rules  []model.PortForwardRule
	err    error
}

type portForwardRuleSavedMsg struct {
	paneID string
	rule   model.PortForwardRule
	err    error
}

type portForwardRuleDeletedMsg struct {
	paneID string
	ruleID string
	err    error
}

type restoredPortForward struct {
	rule    model.PortForwardRule
	forward *sshclient.LocalForward
	err     error
}

type portForwardRulesRestoredMsg struct {
	paneID  string
	session *sshclient.Session
	rules   []model.PortForwardRule
	results []restoredPortForward
	err     error
}

func (m *modelState) openPortForwardManager(paneID string) tea.Cmd {
	tab := m.activeTab()
	if tab == nil {
		return nil
	}
	pane := findPane(tab.root, paneID)
	if pane == nil || pane.host.Kind != model.HostKindSSH || pane.session == nil {
		return nil
	}
	if _, ok := sshSessionFromTerminalSession(pane.session); !ok {
		return nil
	}
	m.portForwardManager = &portForwardManagerState{paneID: paneID, loadingRules: true}
	m.workspaceLeaderPending = false
	store, ok := m.catalog.(portForwardRuleCatalog)
	if !ok {
		m.portForwardManager.loadingRules = false
		m.portForwardManager.errorText = "当前存储不支持保存端口转发规则"
		return portForwardStatsTick(paneID)
	}
	hostID := pane.host.Id
	rulesCommand := func() tea.Msg {
		rules, err := store.ListPortForwardRules(context.Background(), hostID)
		return portForwardRulesListedMsg{paneID: paneID, rules: rules, err: err}
	}
	return tea.Batch(rulesCommand, portForwardStatsTick(paneID))
}

func portForwardStatsTick(paneID string) tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return portForwardStatsTickMsg{paneID: paneID}
	})
}

func (m *modelState) activePortForwards() []*sshclient.LocalForward {
	manager := m.portForwardManager
	if manager == nil {
		return nil
	}
	pane := m.findPaneAcrossTabs(manager.paneID)
	if pane == nil {
		return nil
	}
	active := make([]*sshclient.LocalForward, 0, len(pane.forwards))
	for _, forward := range pane.forwards {
		if forward != nil && forward.Active() {
			active = append(active, forward)
		}
	}
	return active
}

func (m *modelState) activeForwardChannels() []activeForwardChannel {
	forwards := m.activePortForwards()
	channels := make([]activeForwardChannel, 0)
	for _, forward := range forwards {
		for _, info := range forward.Channels() {
			channels = append(channels, activeForwardChannel{forward: forward, info: info})
		}
	}
	return channels
}

func (m *modelState) startPortForward() tea.Cmd {
	manager := m.portForwardManager
	if manager == nil || manager.starting {
		return nil
	}
	tab := m.activeTab()
	if tab == nil {
		manager.errorText = "当前标签已关闭"
		return nil
	}
	pane := findPane(tab.root, manager.paneID)
	if pane == nil {
		manager.errorText = "当前终端 pane 已关闭"
		return nil
	}
	session, ok := sshSessionFromTerminalSession(pane.session)
	if !ok || session == nil {
		manager.errorText = "本地终端不支持 SSH 端口转发"
		return nil
	}
	local, remote, paneID, kind := manager.localAddress, manager.remote, manager.paneID, manager.kind
	manager.starting = true
	manager.errorText = ""
	return func() tea.Msg {
		var forward *sshclient.LocalForward
		var err error
		switch kind {
		case sshclient.ForwardSOCKS5:
			forward, err = session.ForwardSOCKS5(local)
		case sshclient.ForwardRemoteTCP:
			forward, err = session.ForwardRemote(local, remote)
		default:
			forward, err = session.ForwardLocal(local, remote)
		}
		return portForwardStartedMsg{paneID: paneID, local: local, remote: remote, forward: forward, err: err}
	}
}

func (m *modelState) restorePortForwardRules(paneID, hostID string, session *sshclient.Session) tea.Cmd {
	store, ok := m.catalog.(portForwardRuleCatalog)
	if !ok || session == nil {
		return nil
	}
	return func() tea.Msg {
		rules, err := store.ListPortForwardRules(context.Background(), hostID)
		if err != nil {
			return portForwardRulesRestoredMsg{paneID: paneID, session: session, err: err}
		}
		results := make([]restoredPortForward, 0, len(rules))
		for _, rule := range rules {
			forward, startErr := startPortForwardRule(session, rule)
			results = append(results, restoredPortForward{rule: rule, forward: forward, err: startErr})
		}
		return portForwardRulesRestoredMsg{paneID: paneID, session: session, rules: rules, results: results}
	}
}

func startPortForwardRule(session *sshclient.Session, rule model.PortForwardRule) (*sshclient.LocalForward, error) {
	switch rule.Kind {
	case model.PortForwardSOCKS5:
		return session.ForwardSOCKS5(rule.ListenAddress)
	case model.PortForwardRemoteTCP:
		return session.ForwardRemote(rule.ListenAddress, rule.RemoteAddress)
	case model.PortForwardLocalTCP:
		return session.ForwardLocal(rule.ListenAddress, rule.RemoteAddress)
	default:
		return nil, fmt.Errorf("不支持的端口转发类型：%s", rule.Kind)
	}
}

func savedRuleForForward(hostID string, forward *sshclient.LocalForward) (model.PortForwardRule, error) {
	if forward == nil {
		return model.PortForwardRule{}, errors.New("当前转发不可用")
	}
	rule := model.PortForwardRule{
		HostId:        hostID,
		ListenAddress: forward.ListenAddress(),
	}
	switch forward.Kind() {
	case sshclient.ForwardSOCKS5:
		rule.Kind = model.PortForwardSOCKS5
	case sshclient.ForwardRemoteTCP:
		rule.Kind = model.PortForwardRemoteTCP
		rule.RemoteAddress = forward.RemoteAddress()
	default:
		rule.Kind = model.PortForwardLocalTCP
		rule.RemoteAddress = forward.RemoteAddress()
	}
	return rule, nil
}

func forwardMatchesRule(forward *sshclient.LocalForward, rule model.PortForwardRule) bool {
	if forward == nil || !forward.Active() || forward.ListenAddress() != rule.ListenAddress {
		return false
	}
	switch rule.Kind {
	case model.PortForwardSOCKS5:
		return forward.Kind() == sshclient.ForwardSOCKS5
	case model.PortForwardRemoteTCP:
		return forward.Kind() == sshclient.ForwardRemoteTCP && forward.RemoteAddress() == rule.RemoteAddress
	case model.PortForwardLocalTCP:
		return forward.Kind() == sshclient.ForwardLocalTCP && forward.RemoteAddress() == rule.RemoteAddress
	default:
		return false
	}
}

func (m *modelState) persistSelectedPortForward() tea.Cmd {
	manager := m.portForwardManager
	if manager == nil || manager.savingRule {
		return nil
	}
	if manager.loadingRules {
		manager.noticeText = "请等待已保存规则加载完成后再保存"
		return nil
	}
	if manager.focusSaved {
		manager.errorText = "请切换到运行中列表后再保存规则"
		return nil
	}
	if manager.focusChannels {
		manager.errorText = "活动通道不能保存为转发规则"
		return nil
	}
	forwards := m.activePortForwards()
	if len(forwards) == 0 {
		manager.errorText = "没有运行中的转发可保存"
		return nil
	}
	index := clamp(manager.selected, 0, len(forwards)-1)
	var hostID string
	if pane := m.findPaneAcrossTabs(manager.paneID); pane != nil {
		hostID = pane.host.Id
	}
	store, ok := m.catalog.(portForwardRuleCatalog)
	if !ok || hostID == "" {
		manager.errorText = "无法保存：主机目录存储不可用"
		return nil
	}
	rule, err := savedRuleForForward(hostID, forwards[index])
	if err != nil {
		manager.errorText = err.Error()
		return nil
	}
	for _, existing := range manager.savedRules {
		if samePortForwardRule(existing, rule) {
			manager.errorText = "此转发规则已经保存"
			return nil
		}
	}
	manager.savingRule = true
	manager.errorText = ""
	paneID := manager.paneID
	return func() tea.Msg {
		saved, err := store.SavePortForwardRule(context.Background(), rule)
		return portForwardRuleSavedMsg{paneID: paneID, rule: saved, err: err}
	}
}

func samePortForwardRule(a, b model.PortForwardRule) bool {
	return a.HostId == b.HostId && a.Kind == b.Kind && a.ListenAddress == b.ListenAddress && a.RemoteAddress == b.RemoteAddress
}

func (m *modelState) startSelectedPortForwardRule() tea.Cmd {
	manager := m.portForwardManager
	if manager == nil || !manager.focusSaved || manager.starting || len(manager.savedRules) == 0 {
		return nil
	}
	index := clamp(manager.selected, 0, len(manager.savedRules)-1)
	rule := manager.savedRules[index]
	forwards := m.activePortForwards()
	for activeIndex, forward := range forwards {
		if forwardMatchesRule(forward, rule) {
			manager.focusSaved = false
			manager.selected = activeIndex
			return m.stopSelectedPortForward()
		}
	}
	pane := m.findPaneAcrossTabs(manager.paneID)
	if pane == nil || pane.host.Id != rule.HostId {
		manager.errorText = "保存规则所属主机与当前 SSH 会话不一致"
		return nil
	}
	session, ok := sshSessionFromTerminalSession(pane.session)
	if !ok || session == nil {
		manager.errorText = "当前 SSH 会话不可用"
		return nil
	}
	manager.starting = true
	manager.errorText = ""
	return func() tea.Msg {
		forward, err := startPortForwardRule(session, rule)
		return portForwardStartedMsg{paneID: manager.paneID, forward: forward, rule: &rule, err: err}
	}
}

func (m *modelState) deleteSelectedPortForwardRule() tea.Cmd {
	manager := m.portForwardManager
	if manager == nil || !manager.focusSaved || manager.deletingRule || len(manager.savedRules) == 0 {
		return nil
	}
	index := clamp(manager.selected, 0, len(manager.savedRules)-1)
	rule := manager.savedRules[index]
	if manager.confirmDeleteRuleID != rule.Id {
		manager.confirmDeleteRuleID = rule.Id
		manager.errorText = "再次按 X 或 D 确认删除这条自动恢复规则；当前转发不会被关闭"
		return nil
	}
	manager.confirmDeleteRuleID = ""
	store, ok := m.catalog.(portForwardRuleCatalog)
	if !ok {
		manager.errorText = "当前存储不支持删除端口转发规则"
		return nil
	}
	manager.deletingRule = true
	manager.errorText = ""
	paneID := manager.paneID
	return func() tea.Msg {
		return portForwardRuleDeletedMsg{paneID: paneID, ruleID: rule.Id, err: store.DeletePortForwardRule(context.Background(), rule.Id)}
	}
}

func (m *modelState) portForwardRuleIndex(id string) int {
	if m.portForwardManager == nil {
		return 0
	}
	for index, rule := range m.portForwardManager.savedRules {
		if rule.Id == id {
			return index
		}
	}
	return 0
}

func (m *modelState) upsertPortForwardRule(rule model.PortForwardRule) {
	manager := m.portForwardManager
	if manager == nil {
		return
	}
	for index, existing := range manager.savedRules {
		if existing.Id == rule.Id || samePortForwardRule(existing, rule) {
			manager.savedRules[index] = rule
			return
		}
	}
	manager.savedRules = append(manager.savedRules, rule)
	sortPortForwardRules(manager.savedRules)
}

func (m *modelState) removePortForwardRule(id string) {
	manager := m.portForwardManager
	if manager == nil {
		return
	}
	for index, rule := range manager.savedRules {
		if rule.Id == id {
			manager.savedRules = append(manager.savedRules[:index], manager.savedRules[index+1:]...)
			if manager.focusSaved && manager.selected >= len(manager.savedRules) {
				manager.selected = max(0, len(manager.savedRules)-1)
			}
			return
		}
	}
}

func sortPortForwardRules(rules []model.PortForwardRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Kind != rules[j].Kind {
			return rules[i].Kind < rules[j].Kind
		}
		if rules[i].ListenAddress != rules[j].ListenAddress {
			return rules[i].ListenAddress < rules[j].ListenAddress
		}
		return rules[i].RemoteAddress < rules[j].RemoteAddress
	})
}

func (m *modelState) findPaneAcrossTabs(paneID string) *terminalPane {
	for _, tab := range m.tabs {
		if pane := findPane(tab.root, paneID); pane != nil {
			return pane
		}
	}
	return nil
}

func (m *modelState) updatePortForwardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	manager := m.portForwardManager
	if manager == nil {
		return m, nil
	}
	key := msg.Key()
	if manager.editing {
		if manager.starting {
			return m, nil
		}
		if key.Code == tea.KeyTab && key.Mod&tea.ModShift != 0 {
			manager.field = 0
			return m, nil
		}
		switch key.Code {
		case tea.KeyEscape:
			manager.editing = false
			manager.errorText = ""
			return m, nil
		case tea.KeyEnter, tea.KeyTab, tea.KeyDown:
			if manager.kind == sshclient.ForwardSOCKS5 {
				return m, m.startPortForward()
			}
			if manager.field == 0 {
				manager.field = 1
				return m, nil
			}
			if strings.TrimSpace(manager.localAddress) == "" || strings.TrimSpace(manager.remote) == "" {
				manager.errorText = "请填写本地监听地址和远程目标地址。"
				return m, nil
			}
			return m, m.startPortForward()
		case tea.KeyUp:
			manager.field = 0
			return m, nil
		case tea.KeyBackspace:
			value := []rune(manager.fieldValue())
			if len(value) > 0 {
				manager.setFieldValue(string(value[:len(value)-1]))
			}
			manager.errorText = ""
			return m, nil
		default:
			if key.Text != "" && !strings.ContainsAny(key.Text, "\r\n") {
				value := []rune(manager.fieldValue())
				if len(value)+len([]rune(key.Text)) <= 255 {
					manager.setFieldValue(manager.fieldValue() + key.Text)
				}
			}
			manager.errorText = ""
			return m, nil
		}
	}

	forwards := m.activePortForwards()
	channels := m.activeForwardChannels()
	switch key.Code {
	case tea.KeyEscape:
		if manager.confirmDeleteRuleID != "" {
			manager.confirmDeleteRuleID = ""
			manager.errorText = ""
			return m, nil
		}
		m.portForwardManager = nil
		return m, nil
	case tea.KeyTab:
		switch {
		case manager.focusSaved:
			manager.focusSaved = false
			manager.focusChannels = false
		case manager.focusChannels:
			manager.focusChannels = false
			manager.focusSaved = true
		default:
			manager.focusChannels = true
			manager.focusSaved = false
		}
		manager.selected = 0
		manager.confirmDeleteRuleID = ""
		manager.errorText = ""
		return m, nil
	case tea.KeyUp:
		count := len(forwards)
		if manager.focusChannels {
			count = len(channels)
		}
		if manager.focusSaved {
			count = len(manager.savedRules)
		}
		if count > 0 {
			manager.selected = (manager.selected - 1 + count) % count
		}
		manager.confirmDeleteRuleID = ""
		return m, nil
	case tea.KeyDown:
		count := len(forwards)
		if manager.focusChannels {
			count = len(channels)
		}
		if manager.focusSaved {
			count = len(manager.savedRules)
		}
		if count > 0 {
			manager.selected = (manager.selected + 1) % count
		}
		manager.confirmDeleteRuleID = ""
		return m, nil
	case tea.KeyEnter:
		if manager.focusSaved {
			return m, m.startSelectedPortForwardRule()
		}
		if manager.focusChannels {
			return m, m.closeSelectedForwardChannel()
		}
		if len(forwards) > 0 {
			return m, m.stopSelectedPortForward()
		}
	}
	switch strings.ToLower(key.Text) {
	case "n", "a":
		manager.editing = true
		manager.kind = sshclient.ForwardLocalTCP
		manager.field = 0
		manager.localAddress = "127.0.0.1:8080"
		manager.remote = "127.0.0.1:80"
		manager.errorText = ""
	case "s":
		manager.editing = true
		manager.kind = sshclient.ForwardSOCKS5
		manager.field = 0
		manager.localAddress = "127.0.0.1:1080"
		manager.remote = ""
		manager.errorText = ""
	case "r":
		manager.editing = true
		manager.kind = sshclient.ForwardRemoteTCP
		manager.field = 0
		manager.localAddress = "127.0.0.1:8080"
		manager.remote = "127.0.0.1:22"
		manager.errorText = ""
	case "x", "d":
		if manager.focusSaved {
			return m, m.deleteSelectedPortForwardRule()
		}
		if manager.focusChannels {
			return m, m.closeSelectedForwardChannel()
		}
		return m, m.stopSelectedPortForward()
	case "p":
		return m, m.persistSelectedPortForward()
	case "q":
		m.portForwardManager = nil
	}
	return m, nil
}

func (m *modelState) closeSelectedForwardChannel() tea.Cmd {
	manager := m.portForwardManager
	if manager == nil || !manager.focusChannels {
		return nil
	}
	channels := m.activeForwardChannels()
	if len(channels) == 0 {
		return nil
	}
	channel := channels[clamp(manager.selected, 0, len(channels)-1)]
	paneID := manager.paneID
	return func() tea.Msg {
		return portForwardChannelClosedMsg{
			paneID: paneID, forward: channel.forward, channelID: channel.info.ID,
			err: channel.forward.CloseChannel(channel.info.ID),
		}
	}
}

func (manager *portForwardManagerState) fieldValue() string {
	if manager.field == 0 {
		return manager.localAddress
	}
	return manager.remote
}

func (manager *portForwardManagerState) setFieldValue(value string) {
	if manager.field == 0 {
		manager.localAddress = value
	} else {
		manager.remote = value
	}
}

func (m *modelState) stopSelectedPortForward() tea.Cmd {
	manager := m.portForwardManager
	if manager == nil {
		return nil
	}
	forwards := m.activePortForwards()
	if len(forwards) == 0 {
		return nil
	}
	index := clamp(manager.selected, 0, len(forwards)-1)
	forward := forwards[index]
	return func() tea.Msg {
		return portForwardStoppedMsg{paneID: manager.paneID, forward: forward, err: forward.Close()}
	}
}

type portForwardStoppedMsg struct {
	paneID  string
	forward *sshclient.LocalForward
	err     error
}

func (m modelState) portForwardManagerView() string {
	manager := m.portForwardManager
	if manager == nil {
		return ""
	}
	tab := m.activeTab()
	var paneTitle string
	if tab != nil {
		if pane := findPane(tab.root, manager.paneID); pane != nil {
			paneTitle = pane.host.Name
		}
	}
	lines := []string{mutedStyle.Render("本地监听器通过当前 SSH 连接转发；SOCKS5 目标由 SSH 服务器侧访问。"), mutedStyle.Render("通道流量：↑ 客户端到目标，↓ 目标到客户端。"), ""}
	forwards := m.activePortForwards()
	channels := m.activeForwardChannels()
	activeHeading := sectionStyle.Render("运行中")
	if !manager.focusSaved && !manager.focusChannels && !manager.editing {
		activeHeading = selectedAccent.Render("运行中  ←")
	}
	lines = append(lines, activeHeading)
	if len(forwards) == 0 {
		lines = append(lines, mutedStyle.Render("  当前没有运行中的转发。"))
	} else {
		for index, forward := range forwards {
			line := describeActiveForward(forward)
			if _, ok := m.savedRuleForActiveForward(forward); ok {
				line += "  [已保存]"
			}
			line = fitText(line, 78)
			if !manager.editing && !manager.focusSaved && !manager.focusChannels && index == manager.selected {
				lines = append(lines, selectedStyle.Render("› "+line))
			} else {
				lines = append(lines, "  "+line)
			}
		}
	}
	channelHeading := sectionStyle.Render(fmt.Sprintf("活动通道 · %d", len(channels)))
	if manager.focusChannels && !manager.editing {
		channelHeading = selectedAccent.Render(fmt.Sprintf("活动通道 · %d  ←", len(channels)))
	}
	lines = append(lines, "", channelHeading)
	if len(channels) == 0 {
		lines = append(lines, mutedStyle.Render("  暂无活动转发通道。连接到监听端口后会显示在这里。"))
	} else {
		start, end := visibleForwardChannelRange(manager.selected, len(channels), 5)
		if start > 0 {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("  …前面还有 %d 条", start)))
		}
		for index := start; index < end; index++ {
			channel := channels[index]
			line := fmt.Sprintf("#%d  %s  ↑ %s  ↓ %s  %s", channel.info.ID, describeForwardChannelRoute(channel),
				formatForwardBytes(channel.info.BytesToTarget), formatForwardBytes(channel.info.BytesFromTarget),
				time.Since(channel.info.StartedAt).Truncate(time.Second))
			line = fitText(line, 78)
			if manager.focusChannels && !manager.editing && index == manager.selected {
				lines = append(lines, selectedStyle.Render("› "+line))
			} else {
				lines = append(lines, "  "+line)
			}
		}
		if end < len(channels) {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("  …后面还有 %d 条", len(channels)-end)))
		}
	}
	lines = append(lines, "", sectionStyle.Render("已保存 · SSH 连接后自动恢复"))
	if manager.loadingRules {
		lines = append(lines, mutedStyle.Render("  正在读取保存的规则…"))
	} else if len(manager.savedRules) == 0 {
		lines = append(lines, mutedStyle.Render("  暂无保存的规则。选中运行中的转发后按 P 保存。"))
	} else {
		for index, rule := range manager.savedRules {
			line := describePortForwardRule(rule)
			state := "未运行"
			for _, forward := range forwards {
				if forwardMatchesRule(forward, rule) {
					state = "运行中"
					break
				}
			}
			line = fitText(fmt.Sprintf("%s  [%s]", line, state), 78)
			if !manager.editing && manager.focusSaved && index == manager.selected {
				lines = append(lines, selectedStyle.Render("› "+line))
			} else {
				lines = append(lines, "  "+line)
			}
		}
	}
	if manager.editing {
		localStyle, remoteStyle := secondaryStyle, secondaryStyle
		localMark, remoteMark := "  ", "  "
		if manager.field == 0 {
			localStyle, localMark = selectedAccent, "› "
		} else {
			remoteStyle, remoteMark = selectedAccent, "› "
		}
		switch manager.kind {
		case sshclient.ForwardSOCKS5:
			lines = append(lines, "", sectionStyle.Render("新建 SOCKS5 动态转发"),
				localStyle.Render(localMark+"本地代理监听  "+manager.localAddress),
				mutedStyle.Render("默认监听 127.0.0.1，客户端选择的目标由 SSH 服务器连接。"),
			)
		case sshclient.ForwardRemoteTCP:
			lines = append(lines, "", sectionStyle.Render("新建远程端口转发"),
				localStyle.Render(localMark+"远程监听  "+manager.localAddress),
				remoteStyle.Render(remoteMark+"本地目标  "+manager.remote),
				mutedStyle.Render("远程客户端连接监听端口后，通过 SSH 回到本机目标。"),
			)
		default:
			// The default and only remaining kind is local-to-remote TCP.
			lines = append(lines, "", sectionStyle.Render("新建本地端口转发"),
				localStyle.Render(localMark+"本地监听  "+manager.localAddress),
				remoteStyle.Render(remoteMark+"远程目标  "+manager.remote),
				mutedStyle.Render("示例：127.0.0.1:8080 → 127.0.0.1:80"),
			)
		}
		if manager.starting {
			lines = append(lines, mutedStyle.Render("正在启动监听…"))
		}
	} else {
		lines = append(lines, "",
			mutedStyle.Render("N 本地  R 远程  S SOCKS5  Tab 切换转发/通道/保存规则  ↑/↓ 选择"),
			mutedStyle.Render("Enter 启停或关闭通道  P 保存转发  X/D 停止、关闭通道或删除规则（需确认）  Esc 返回"),
		)
	}
	if manager.errorText != "" {
		lines = append(lines, statusErrorStyle.Render(manager.errorText))
	}
	if manager.noticeText != "" {
		lines = append(lines, secondaryStyle.Render(manager.noticeText))
	}
	return m.modalView("SSH 转发 · "+paneTitle, lines, 86)
}

func describeForwardChannelRoute(channel activeForwardChannel) string {
	forward := channel.forward
	switch forward.Kind() {
	case sshclient.ForwardSOCKS5:
		return fmt.Sprintf("SOCKS5 %s → %s", forward.ListenAddress(), channel.info.Target)
	case sshclient.ForwardRemoteTCP:
		return fmt.Sprintf("远程 %s → 本地 %s", forward.ListenAddress(), channel.info.Target)
	default:
		return fmt.Sprintf("本地 %s → 远程 %s", forward.ListenAddress(), channel.info.Target)
	}
}

func visibleForwardChannelRange(selected, total, limit int) (int, int) {
	if total <= limit {
		return 0, total
	}
	start := clamp(selected-limit/2, 0, total-limit)
	return start, start + limit
}

func formatForwardBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	index := -1
	for value >= unit && index < len(units)-1 {
		value /= unit
		index++
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

func describeActiveForward(forward *sshclient.LocalForward) string {
	switch forward.Kind() {
	case sshclient.ForwardSOCKS5:
		return fmt.Sprintf("%s  SOCKS5 动态代理", forward.LocalAddress())
	case sshclient.ForwardRemoteTCP:
		return fmt.Sprintf("远程 %s  →  本地 %s", forward.ListenAddress(), forward.RemoteAddress())
	default:
		return fmt.Sprintf("本地 %s  →  远程 %s", forward.ListenAddress(), forward.RemoteAddress())
	}
}

func describePortForwardRule(rule model.PortForwardRule) string {
	switch rule.Kind {
	case model.PortForwardSOCKS5:
		return fmt.Sprintf("%s  SOCKS5 动态代理", rule.ListenAddress)
	case model.PortForwardRemoteTCP:
		return fmt.Sprintf("远程 %s  →  本地 %s", rule.ListenAddress, rule.RemoteAddress)
	default:
		return fmt.Sprintf("本地 %s  →  远程 %s", rule.ListenAddress, rule.RemoteAddress)
	}
}

func (m modelState) savedRuleForActiveForward(forward *sshclient.LocalForward) (model.PortForwardRule, bool) {
	if m.portForwardManager == nil {
		return model.PortForwardRule{}, false
	}
	for _, rule := range m.portForwardManager.savedRules {
		if forwardMatchesRule(forward, rule) {
			return rule, true
		}
	}
	return model.PortForwardRule{}, false
}
