# 分流规则订阅（Routing Subscriptions）

## 配置与界面

Agent 的 `routing.subscriptions` 是独立、有序的订阅规则组，使用同一个 Go 分流引擎；Windows / macOS React GUI、Agent Web、Android 的原生分流设置可以配置订阅。示例：

```yaml
routing:
  mode: rule
  default_action: DIRECT
  rules:
    - name: 本地内网
      enabled: true
      action: DIRECT
      targets: ["10.0.0.0/8", "192.168.0.0/16"]
  subscriptions:
    - name: GFWList
      enabled: true
      url: https://raw.githubusercontent.com/gfwlist/gfwlist/master/gfwlist.txt
      action: PROXY
      exit_id: ""
```

**优先级：** 在 `mode: rule` 下，首先按原有顺序执行全部手动规则，然后按订阅列表的顺序匹配每个订阅，最后执行 `default_action`。不同订阅的第一条命中生效。订阅组内所有被支持的域名/IP 共享该组的 `action` 和 `exit_id`。留空 `exit_id` 使用默认出口。全局代理 / 全局直连模式仍忽略分类规则。

**支持的列表：** 普通文本（一行一个域名、IP 或 CIDR）、常见 Adblock 的整站域名过滤器（例如 `||google.com^`）以及 Base64 编码的 GFWList。 `@@||example.com^` 作为**该订阅内的例外**，不代表强制 DIRECT；若其不命中此订阅仍会继续后续订阅和默认动作。域名匹配自身与子域名。域名与 CIDR 条目的重复项自动去重。

**有意不支持的语法：** URL 路径、浏览器请求属性（`$third-party` 等）、正则表达式、任意子串过滤器。Agent 只有主机名/IP/端口，不具备完整 HTTP(S) URL 上下文；不能将仅针对某路径的规则扩大为整个站点的代理/拦截。因此遇到这些条目会跳过，并在桌面端显示跳过条数。需要完整 URL 语义的规则仍应交给具备对应请求上下文的组件处理。

## 刷新与安全

Agent 启动或配置热加载后，会异步下载启用的订阅，此后每 6 小时刷新一次。刷新通过 HTTPS/443，限制最多 5 次跳转，单文件不超过 8 MiB，解析结果最多 100,000 条；限制连接只到经过 DNS 解析校验的公网地址，禁止本地和私网访问，防止订阅地址访问设备内服务。不同订阅 URL 不允许重复。

成功解析会原子发布规则快照，并写入用户缓存目录 `RelayProxy/routing-subscriptions`；失败时继续使用上一次成功加载的规则，不会发布空列表或让失败内容接管流量。断网重启时会尝试先从磁盘读取缓存。桌面 GUI 会显示已加载条数、最近更新时间和最近一次拉取错误。首次下载失败且无缓存时，该订阅不参与匹配，走后续订阅或默认动作。

订阅下载是由 Agent 发起的系统 HTTPS 请求，不会自动经过当前分流出口；用于获取列表的 DNS/网络路径也不受订阅规则本身保护。Android 上磁盘缓存位置取决于运行时可用的用户缓存目录，需实机验证。

## 验证点

- GFWList Base64 与普通文本列表都可解析；`@@` 例外、IP/CIDR、域名后缀命中正确。
- 手动规则优先、不同订阅按顺序命中；规则模式以外不生效。
- 本地/内网 URL 和非 HTTPS URL 不可保存，失败更新保留旧缓存。
- 各平台配置序列化与重启读取一致；应用路由时不允许引用停用/丢失的自定义代理出口。
- Agent 退出后取消后台订阅刷新任务。
