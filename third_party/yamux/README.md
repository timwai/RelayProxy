# RelayProxy yamux 补丁

基于 `github.com/hashicorp/yamux v0.1.2`，保留上游 MPL-2.0 许可证、协议说明和回归测试。根 `go.mod` 通过本地 replace 使用本目录，升级依赖时必须复核这些补丁。

- `Config.MaxSessionBufferSize` 限制一个会话所有接收缓冲的底层数组容量；包含 Accept/Read 前的数据及扩容时同时存活的新旧数组。默认库配置为 0（兼容上游），RelayProxy 配置为 64 MiB。
- 超出预算前拒绝分配，复位触发超限的流，以固定大小缓冲排空该帧，保留其他流和会话控制消息。该预算不是 yamux 协议层连接窗口，突发超限会丢失触发流。
- 接收缓冲使用显式容量分配，读空即释放；半关闭仍允许读完响应。新增 `CloseRead` 用于完整本地关闭时立即丢弃输入并归还预算；`Reset`、关闭超时及会话关闭也回收相应缓冲。
- 远端传输 EOF 保留已收到的数据供应用读完，显式本地 `Session.Close` 才丢弃仍挂在会话中的缓冲；应用完整关闭流时须调用 `CloseRead`（RelayProxy adapter 已处理），`Stream.Close` 仍只发送 FIN。
- 复位流移除未完成的 SYN 记录，防止连续超限复位积累记录或重复归还建流名额；会话关闭停止各流的关闭计时器。
- `Session.ReceiveBufferBytes()` 提供已保留数组容量统计。该限额不包含 TLS/TCP 缓冲、流元数据、发送队列、应用缓冲及等待 GC 的旧数组，不能当作进程 RSS 的严格上限。

验证：仓库根运行 `go test -race ./internal/tunnel ./agent/client ./server/gateway ./server/session`，本目录独立运行 `go test ./...` 和 `go test -race -short ./...`。上游的 250 MiB/更大传输测试已提供 `testing.Short` 分支，注明 race 检测下可能超时；普通全量保留这些测试，race 按上游 short 模式执行，其余测试和新增预算测试全部运行。新增测试覆盖接受前后聚合超限、其他流继续传输、容量与扩容计费、读取/关闭/RST/会话退出时释放。
