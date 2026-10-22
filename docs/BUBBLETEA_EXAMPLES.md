# Bubble Tea 交互示例

本项目使用 Bubble Tea v2。Bubbles 和 Lip Gloss 也使用 v2 导入路径：

```go
import (
	"fmt"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)
```

Bubble Tea v2 和 Lip Gloss v2 已用于当前主界面；Bubbles 组件会在具体表单、视口等功能落地时按需加入。Bubbles v2 与 Bubble Tea v2、Lip Gloss v2 配套使用；旧版教程常见的 `github.com/charmbracelet/...` 导入路径和字段 API 不一定适用。

以下代码是面向本项目的可组合片段。`model` 中的会话、用例和状态字段需要按你当前实现补齐。

## 先记住 Bubble Tea 的消息流

```text
按键 / 窗口尺寸 / 网络结果 / 文件进度
                   ↓ tea.Msg
                Update
              ↙         ↘
          更新状态       tea.Cmd 做异步工作
              ↓             ↓
             View ←──── 新的 tea.Msg
```

- `Model` 保存界面状态，例如当前页面、焦点、弹窗是否打开、传输了多少字节。
- `Update` 接收消息并改变状态。这里不执行阻塞网络 I/O。
- `tea.Cmd` 表示一次异步动作；动作完成后返回一个 `tea.Msg`。
- `View` 根据当前状态绘制画面，不在这里修改状态或连接 SSH。

## 1. 确认弹窗

删除会话、覆盖远端文件、断开连接前都适合确认。弹窗打开时先拦截按键，避免底下的页面同时响应。

```go
type model struct {
	width, height int
	confirmOpen   bool
	// 其他页面状态……
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		if m.confirmOpen {
			switch keyMsg.String() {
			case "y", "enter":
				m.confirmOpen = false
				return m, m.deleteSelectedSessionCmd() // I/O 放在 Cmd 里
			case "n", "esc":
				m.confirmOpen = false
				return m, nil
			}
			return m, nil // 弹窗打开时，不把其他按键传给底层页面
		}

		if keyMsg.String() == "d" {
			m.confirmOpen = true
			return m, nil
		}
	}

	// 其他消息和按键交给正常页面逻辑。
	return m, nil
}

func (m model) View() tea.View {
	body := m.mainView() // 你自己的主页面渲染函数

	if m.confirmOpen && m.width > 0 && m.height > 0 {
		// 让底层画面占满终端，再把对话框作为更高一层绘制。
		body = lipgloss.NewStyle().Width(m.width).Height(m.height).Render(body)
		dialog := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#F2CC60")).
			Background(lipgloss.Color("#1D2021")).
			Padding(1, 3).
			Render("删除这个会话？\n\nEnter 确认 · Esc 取消")

		x := max(0, (m.width-lipgloss.Width(dialog))/2)
		y := max(0, (m.height-lipgloss.Height(dialog))/2)
		body = lipgloss.NewCompositor(
			lipgloss.NewLayer(body),
			lipgloss.NewLayer(dialog).X(x).Y(y).Z(1),
		).Render()
	}

	view := tea.NewView(body)
	view.AltScreen = true
	return view
}
```

`WindowSizeMsg` 到来时把 `msg.Width` 和 `msg.Height` 存进模型，弹窗才能正确居中。确认后返回一个 `tea.Cmd`，Cmd 执行删除并返回 `deleteFinishedMsg`；再由 `Update` 根据结果显示成功或错误状态。

## 2. SFTP 文件传输进度

进度条应该显示真实传输字节数。传输用例每次读写后产生 `Sent` 和 `Total`，UI 收到消息后更新 Bubbles 进度模型。

```go
type transferProgressMsg struct {
	Sent  int64
	Total int64
}

type model struct {
	progress progress.Model
	sent     int64
	total    int64
}

func newModel() model {
	return model{
		progress: progress.New(progress.WithDefaultBlend()),
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.progress.SetWidth(max(1, msg.Width-20))
		return m, nil

	case transferProgressMsg:
		m.sent, m.total = msg.Sent, msg.Total
		if msg.Total <= 0 {
			return m, nil // 大小未知时改用 spinner 或显示已传字节数
		}

		ratio := float64(msg.Sent) / float64(msg.Total)
		ratio = min(1, max(0, ratio))
		return m, m.progress.SetPercent(ratio)

	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progress, cmd = m.progress.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) transferView() string {
	if m.total <= 0 {
		return fmt.Sprintf("已传输 %d 字节", m.sent)
	}
	return fmt.Sprintf("%s  %d / %d 字节", m.progress.View(), m.sent, m.total)
}
```

传输服务本身不要依赖 Bubble Tea：让它通过回调或项目自己的事件类型报告进度，再由 UI 层转换成 `transferProgressMsg`。用户按取消时，把 `context.Context` 的取消信号传到 SFTP 复制循环。

## 3. SSH 连接中的 spinner

连接耗时未知，用 spinner 表示“正在进行”比伪造百分比更合适。

```go
type connectResultMsg struct {
	err error
}

type model struct {
	spinner    spinner.Model
	connecting bool
	connectErr error
}

func newModel() model {
	return model{spinner: spinner.New()}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "enter" && !m.connecting {
			m.connecting = true
			m.connectErr = nil
			return m, tea.Batch(m.spinner.Tick, m.connectCmd())
		}

	case spinner.TickMsg:
		if !m.connecting {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case connectResultMsg:
		m.connecting = false
		m.connectErr = msg.err
		return m, nil
	}

	return m, nil
}

func (m model) connectionStatus() string {
	if m.connecting {
		return m.spinner.View() + " 正在连接 SSH…"
	}
	if m.connectErr != nil {
		return "连接失败：" + m.connectErr.Error()
	}
	return "按 Enter 连接"
}
```

`connectCmd()` 应该是 `tea.Cmd`：在里面调用连接用例，并返回 `connectResultMsg`。连接成功后把连接句柄交给应用层管理，不要把 `ssh.Client` 塞进界面文字或日志。

## 4. 新建会话表单与密码输入

`textinput` 适合主机、端口、用户名和单行路径。密码用 `EchoPassword` 隐藏输入。

```go
func newSessionInputs() (host, user, password textinput.Model) {
	host = textinput.New()
	host.Placeholder = "主机名或 IP"
	host.Focus()

	user = textinput.New()
	user.Placeholder = "用户名"

	password = textinput.New()
	password.Placeholder = "密码"
	password.EchoMode = textinput.EchoPassword
	password.EchoCharacter = '•'

	return host, user, password
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

// 在 Update 中把输入消息交给当前获得焦点的字段。
func (m model) updateFocusedInput(msg tea.Msg) (model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.focusIndex {
	case 0:
		m.hostInput, cmd = m.hostInput.Update(msg)
	case 1:
		m.userInput, cmd = m.userInput.Update(msg)
	case 2:
		m.passwordInput, cmd = m.passwordInput.Update(msg)
	}
	return m, cmd
}
```

模型初始化时调用 `textinput.Blink`，字段切换时对新字段调用 `Focus()`、旧字段调用 `Blur()`。密码只在连接所需的短时间内保留在内存；长期保存时接入系统钥匙串，不要把明文密码写入普通会话配置或日志。

## 5. 远程文件预览和日志滚动

`viewport` 用于滚动长内容，例如远端文件预览、连接日志和帮助页。

```go
func newPreview(content string, width, height int) viewport.Model {
	view := viewport.New(
		viewport.WithWidth(width),
		viewport.WithHeight(height),
	)
	view.SetContent(content)
	view.SoftWrap = true
	return view
}

func (m model) updatePreview(msg tea.Msg) (model, tea.Cmd) {
	var cmd tea.Cmd
	m.preview, cmd = m.preview.Update(msg)
	return m, cmd
}

func (m model) previewView() string {
	return m.preview.View()
}
```

终端尺寸变化时更新 `SetWidth` / `SetHeight`。预览远端文件前先检查文件大小和内容类型；大文件不要一次性全部读进 UI 状态。要实现可编辑文件，考虑用 `textarea` 维护本地编辑缓冲，保存前检查远端文件是否已变化。

## 6. 这个项目可以优先练的组件

| 项目功能 | Bubble Tea / Bubbles 用法 |
| --- | --- |
| 会话管理页、远端目录列表、命令面板 | `bubbles/list`：选择、过滤、分页 |
| 新建连接、重命名会话 | `bubbles/textinput`：单行字段与焦点切换 |
| 删除会话、覆盖文件、断开连接 | 自己维护弹窗状态；Lip Gloss layers 负责叠放 |
| SSH 连接、目录加载 | `bubbles/spinner`：不确定时长的等待状态 |
| 上传 / 下载队列 | `bubbles/progress`：按真实字节数显示进度 |
| 文件预览、运行日志、帮助 | `bubbles/viewport`：滚动长内容 |
| 远端小文件编辑 | `bubbles/textarea`：多行输入缓冲 |
| 页面底部快捷键说明 | `bubbles/help` 或自己渲染一行帮助 |

## 建议你按这个顺序自己写

1. 给当前骨架加 `WindowSizeMsg`，让界面记住终端宽高。
2. 做会话列表的 `d` 删除确认弹窗，只处理 UI 状态，不接真实 SSH。
3. 做一个假的连接 `tea.Cmd`，练习 spinner 与异步结果消息。
4. 用静态文本接通文件预览 `viewport`，练习滚动和尺寸变化。
5. 最后把 SFTP 传输用例接入进度消息和取消上下文。

每一步先让自己写 `Model` 字段和 `Update` 分支，再用这个文档查 API。遇到错误时可以让 Codex 先解释消息流或指出下一步，不必一开始就让它代写整个功能。

## 官方示例

- [Bubble Tea：动画进度条](https://github.com/charmbracelet/bubbletea/blob/main/examples/progress-animated/main.go)
- [Bubble Tea：文件下载进度](https://github.com/charmbracelet/bubbletea/blob/main/examples/progress-download/main.go)
- [Bubble Tea：多个文本输入框](https://github.com/charmbracelet/bubbletea/blob/main/examples/textinputs/main.go)
- [Bubble Tea：Spinner](https://github.com/charmbracelet/bubbletea/blob/main/examples/spinners/main.go)
- [Bubbles v2 升级说明](https://github.com/charmbracelet/bubbles/blob/main/UPGRADE_GUIDE_V2.md)
- [Lip Gloss：Canvas 和 Layer 示例](https://github.com/charmbracelet/lipgloss/blob/main/examples/canvas/main.go)
