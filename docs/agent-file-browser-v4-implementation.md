# RelayProxy Agent 文件浏览器 V4 实现记录

> 基线：`main`（原功能导向导航不变）。该分支只替换文件共享 → 其他设备的浏览器；不合并设备中心式 UI 的 PR #209。

## V4 对照

| V4 功能 | 实现入口 | 边界 |
| --- | --- | --- |
| 可用区域放大 | `FileBrowserV4.jsx`、`fileBrowserV4.css` | 普通模式扩大文件区，高度随视口；专注模式隐藏外层导航 |
| Windows 独立窗口 | `file_browser_windows.go`、`OpenFileBrowserWindow` | 1540×960 独立可调整大小 WebView，共用 Wails Agent 身份，不开放 LAN 文件端口 |
| macOS/Web 独立窗口 | `window.open(...?page=files&filesWindow=1)` | 由系统浏览器管理窗口；弹窗拦截时提示 |
| 可调分栏 | React pointer capture、可访问性键盘方向键 | 双击恢复默认宽度 |
| 目录树与面包屑 | 通过 `goGetRemoteFileEntries` 懒加载子目录 | 仅当前经过实时 ACL 核验的共享；未授权目录不展示 |
| 文件列表/网格、搜索排序 | `FileBrowserV4` | 仍使用 Agent 目录缓存、限流与超时机制 |
| 多选、右键和移动端长按 | 文件/目录/空白右键 + 批量工具栏 | 操作前再次检查共享权限 |
| 操作系统拖入文件 | `goBeginFileUpload` / `goAppendFileUploadChunk` / `goCommitFileUpload` | 使用原有分块上传链路，不由前端访问共享端绝对路径 |
| 同一共享内拖放移动 | `goMutateRemoteFile` / FileShareMove | 跨设备、跨共享拒绝；子目录路径隔离 |
| 新建文件夹/重命名/删除 | FileShareMkdir / Move / Remove | 目标设备 ACL 必须为 read_write；删除不递归、拒绝非空目录 |
| 传输任务 | 复用 `goGetFileTransfers` | 远程浏览时也轮询真实任务和速率；上传仍复用原有校验后原子发布 |

## 服务端 / 客户端协议

`FileShareRequest` 增加 `Destination` 和动作 `mkdir` / `move` / `remove`。客户端发送经当前 Server 认证的设备流，真实共享端根据 authenticatedDeviceID + shareID 强制 `read_write`。所有路径为共享根下相对路径；使用 Go `os.Root` 进行路径根约束，阻止 `..`、绝对路径、上传临时文件和链接逃逸。默认不允许覆盖已有目标：移动普通文件使用 hardlink + unlink 的无覆盖发布，目录移动先检查目标冲突并加锁。删除只删除单个文件或空目录。

## 向后兼容

- 原有“我的共享 / 其他设备 / 传输任务”标签不变，本机共享授权仍通过系统原生文件夹选择器及既有配置完成。
- 老版本共享端不支持新增文件操作，会返回“unsupported file sharing operation”；浏览/下载/上传均不受影响。
- 仍保留文件原名、SHA-256 下载校验、断点续传、P2P/公网直连/Relay 传输回退。
- 不把未授权设备放入列表，不允许跨身份浏览或写入。

## 验收

1. React 单测与 Vite build；Windows/macOS Agent 构建与 Go 全量测试。
2. 已授权 read-write：创建/重命名/移动/删除文件可见；只读 ACL 不允许文件写入。
3. 无权限、revoked、离线、非法相对路径、指向根外的 symlink、移动覆盖、删除非空目录均拒绝。
4. Windows 专用窗口与普通窗口能共享远程数据，文件列表及拖拽区域可交互。
5. macOS/Web 新窗口在浏览器权限允许情况下正常打开，返回普通模式不丢失 Agent 导航。
6. 390px 手机浏览器设备树以抽屉展示，文件行无页面级水平溢出。

## 已知限制

- 目录删除刻意采用非递归方式；非空文件夹需先清理内容。
- 跨共享/跨设备移动属于两端授权和复制事务，未实现。
- 独立原生 Wails 窗口目前只在 Windows；macOS 使用现有的外部浏览器 GUI。
- 文件预览详情当前只展示元信息，尚未实现远端图片/视频流式预览。
- 尚需真实双设备传输验收，不能仅以单机 UI 测试代替。
