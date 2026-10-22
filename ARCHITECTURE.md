# 项目架构

## 运行路径

```text
main.go
  → internal/cmd       GoFrame 命令行入口
  → internal/app       应用装配、依赖注册和生命周期
  → internal/ui        Bubble Tea 状态与界面
  → internal/usecase   连接、会话、文件等应用动作
  → internal/domain    会话、文件传输等核心规则
  → internal/infra      SSH、SFTP、存储、凭据等外部实现
```

目前仓库已落地 `main.go`、`internal/cmd`、`internal/app`、`internal/ui` 和 `internal/plugin`。其他层按里程碑逐步创建，先不放没有职责的空包。

## 目录职责

| 目录 | 负责什么 | 不负责什么 |
| --- | --- | --- |
| `internal/cmd` | GoFrame `gcmd` 命令、参数解析、启动错误返回 | 界面状态、SSH 连接细节 |
| `internal/app` | 把配置、用例、插件和 UI 组装起来；协调启动与退出 | 领域规则和终端绘制 |
| `internal/ui` | Bubble Tea Model、键位、焦点、视图、用户可见状态 | 阻塞网络 I/O、直接读写配置文件 |
| `internal/domain` | 会话、认证引用、文件项、传输任务等核心类型和规则 | SSH/SFTP 库、TUI 库、配置格式 |
| `internal/usecase` | “连接会话”“列出远端文件”“下载文件”等用户动作 | 具体网络协议实现 |
| `internal/infra` | SSH、SFTP、配置、钥匙串、日志等适配器 | Bubble Tea 的 Model |
| `internal/plugin` | 插件元数据、扩展注册、命令分发和 API 版本 | 越权访问应用内部状态 |

推荐的实际目录随功能增长再展开：

```text
internal/
  app/
  cmd/
  domain/
    session/
    transfer/
  infra/
    config/
    credentials/
    ssh/
    sftp/
  plugin/
  ui/
    screens/
    components/
  usecase/
    session/
    transfer/
```

## 依赖规则

1. `domain` 不依赖 UI、GoFrame、SSH/SFTP 库或文件格式。
2. `usecase` 依赖领域类型和自己定义的接口；接口由使用它的一层定义。
3. `infra` 实现用例需要的接口，依赖可以指向 domain/usecase，反方向不成立。
4. `ui` 通过应用动作和消息使用功能，不直接创建 SSH 客户端或打开配置文件。
5. `app` 是装配点；依赖具体实现只应集中在这里或 `infra`。
6. `plugin` 命令作为异步应用动作进入 UI；插件不能从后台 goroutine 直接改 Bubble Tea Model。

## Bubble Tea 状态流

```text
用户按键 / 网络结果 / 文件进度
              ↓ Msg
          Update 更新状态
          ↙           ↘
       View 绘制     Cmd 执行 I/O
                         ↓
                 新 Msg 回到 Update
```

`View` 只读状态并生成画面。网络连接、目录读取、文件复制和插件命令都通过 Bubble Tea 命令异步运行。远程终端是特殊场景：SSH 返回的是 VT 控制序列，必须由终端模拟层维护屏幕状态，再把屏幕单元格渲染到 TUI；不要把远端原始字节直接拼到 `View`。

## 插件系统边界

仓库当前实现 `internal/plugin` 的第一版注册器：

- 插件通过 `Manifest` 声明唯一 ID、名称、版本和 API 版本。
- 插件注册带命名空间的命令，例如 `sftp.refresh`。
- 注册前会校验清单和命令；命令先暂存，插件注册失败时不会留下部分状态。
- 注册器提供稳定排序的命令快照，并通过 `Run` 执行命令。
- `Application.New(pluginA, pluginB)` 是当前编译期插件的装配入口。

第一版仅支持**编译进主程序的可信 Go 插件**，注册器不会扫描目录或运行任意代码。现阶段插件命令也是应用扩展接口，尚未接入命令面板。后续先把命令面板和异步完成消息接通，再稳定插件 API。

第三方动态插件不采用 Go 标准库 `.so` 加载：它只支持有限操作系统，并要求宿主和插件使用一致的 Go 工具链与依赖构建。等内置 API 稳定后，再评估通过独立进程和 JSON-RPC/JSON Lines 通信的插件协议。进程边界便于崩溃隔离、跨语言和跨版本；协议需要明确能力授权、超时、取消、输出大小限制和版本协商。插件不应默认获得 SSH 凭据或任意本地文件访问权。

## 关键数据流

### 本地配置存储

- SSH Host 和多级分组保存在用户配置目录下的 SQLite 数据库 `workbench.db`。
- 主界面的新建表单只维护非敏感连接资料；UI 通过异步 Bubble Tea 命令调用存储接口，成功后更新当前列表，失败时保留表单并显示错误。
- 分组通过 `parent_id` 表示父子关系，Host 通过 `group_id` 关联分组；空 `group_id` 表示未分组。
- 数据库使用 `PRAGMA user_version` 管理结构迁移；删除分组前必须先移走其子分组和 Host。
- Host 通过 `kind` 区分 `ssh` 和 `local`。SQLite v2 迁移会重建 hosts 表，以兼容本地终端没有远端地址和端口的情况；旧 SSH Host 会保留并默认为 `ssh`。SQLite v3 新增按 SSH Host 关联的端口转发规则表；SQLite v4 新增全局快速命令表。
- SQLite 只保存连接配置、机器本机的 SSH 私钥路径、随机系统钥匙串引用、端口转发规则和用户显式保存的快速命令，不保存明文密码或私钥内容；快速命令可能包含用户提供的敏感文本，因此数据库文件限制为仅当前用户可读写。密码和连接状态属于运行时数据。私钥路径通过 SSH 主机表单配置，支持 `~` 和相对路径；路径不进入可移植会话目录或通用 JSON。系统钥匙串适配器位于 `internal/infra/credentialstore`；更新凭据使用新引用后再改 SQLite，删除主机时清理其凭据条目。
- `-db` 可覆盖数据库路径，适合开发和手动备份；不传时使用 `os.UserConfigDir()` 对应的应用目录。
- `config.toml` 留给主题、默认编辑器等应用级偏好，不承载 Host 列表。

### 终端会话

用户选择主机 → UI 根据 `kind` 启动 SSH 连接或本地登录 Shell → SSH 适配器按 ProxyJump 列表逐跳建立隧道并分别校验各跳的主机指纹 → 两类适配器都提供读写、尺寸调整和关闭接口 → 成功后加入当前标签或分屏 pane → 每个 pane 独立维护会话、VT 屏幕、鼠标 / 键盘模式和连接状态。分屏会先显示 Host 选择框，再为选中的目标启动新会话。标签切换时只改变活动焦点，不关闭后台连接；关闭 pane / 标签或退出应用时释放对应 SSH / 本地 PTY 资源和中间跳板连接。

SSH 键盘交互式认证由 `sshclient` 在后台触发 challenge 消息，UI 收集每轮问题的答案后通过响应 channel 返回；非回显答案会遮罩。取消连接会取消认证上下文并解除等待，不会把答案写入主机目录或日志。

普通密码或私钥口令认证失败时，`sshclient.AuthenticationError` 携带拒绝认证的具体 Host。连接开始前，UI 按 ProxyJump 链从系统钥匙串异步读取已保存凭据；读取失败时继续尝试公钥/agent 和交互认证。用户按主机列表的 `c` 可写入或删除 SSH / FTP/FTPS 主机凭据。SQLite 仅保存随机引用，导出会话目录时移除引用；运行时凭据按连接目标映射，成功、取消或非认证错误后清理，不进入日志。

SSH pane 的本地 TCP 转发、远程 TCP 转发和 SOCKS5 动态代理由 `sshclient.Session` 所有。本地监听和 SOCKS5 目标通过当前认证的 SSH transport 建立 `direct-tcpip` 通道；远程监听使用 SSH `tcpip-forward` 请求，把服务端连接转回本机目标。SOCKS5 仅支持 TCP CONNECT，域名由 SSH 服务器解析，并强制绑定本机回环地址。管理界面可添加和停止转发；用户可保存运行规则，SQLite 按 Host 持久化，SSH 会话连通后异步自动恢复。恢复失败会保留已成功启动的规则，并在 pane 状态中报告失败数量；关闭连接时会关闭监听器及活动连接。

### 文件传输

SSH pane 内的 SFTP 复用已认证的 `*ssh.Client`，打开单栏远程文件浏览。独立 SFTP 面板通过 `ConnectSFTP` 建立不启动远端 Shell 的 SSH transport，并显示本地和远端两个目录栏；关闭面板时释放独立 transport。`internal/infra/sftpclient` 封装远端目录读取、文件操作、单文件原子提交和递归目录传输。Bubble Tea 命令在后台执行文件 I/O，再通过消息更新路径、列表、状态和传输字节进度。单文件先写临时文件，成功后再替换目标；覆盖前由 UI 二次确认。目录传输按文件提交，失败或取消后已完成项目会保留；取消会关闭并重新打开 SFTP subsystem。

目前 UI 命令仍直接调用 infra adapter，作为可运行的纵向切片。独立双栏 SFTP 默认显示本地与远程目录；左侧可连接第二台 SSH 主机，切换为远程目录后可执行远程到远程传输。两侧支持多选，按名称顺序检查覆盖冲突；确认后通过固定大小为 3 的 worker pool 并发传输。双向同步队列可以混合左右方向的任务，并在完成后分别刷新目标栏。跳过冲突或取消队列可在调度前完成，单项失败不会阻断其他项目。单文件在目标端以隐藏临时文件提交；取消或网络中断后保留临时文件，重试前会校验它与当前源文件前缀的 SHA-256，一致才从已有偏移续传，否则截断后重传。文件传输保留源端 rwx 权限与修改时间；目录属性在子项传输完成后应用，以避免后续写入覆盖目录修改时间。双栏可递归比较目录树并按方向生成合并同步队列；双向模式按修改时间选择较新文件，无法判断的同时间属性冲突和类型冲突会保留并列出；默认不删除目标额外项目，镜像删除需要二次确认并在执行前重扫。符号链接和特殊文件显示为跳过或阻止项，目录扫描上限为 100000 项。递归删除先由界面确认，删除算法通过 Lstat 检查条目并对符号链接只删除链接本身。当前尚无独立 usecase 接口。

### 远程编辑

打开本地或远程文件时先按大小和内容类型判断是否可预览 → 本地编辑缓冲保留原始内容 → 保存前检查文件版本 → 冲突时保留用户缓冲并提示 → 通过同目录临时文件提交本地修改，或通过远程临时文件提交 SFTP 修改。
