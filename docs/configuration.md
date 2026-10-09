# 配置、环境变量与 API 参考

日常操作见 [使用说明](user-guide.md)，安装步骤见 [部署指南](deploy-synology.md)。本页按当前源码的字段和默认值说明；完整 YAML 示例见 [config.example.yaml](../configs/config.example.yaml)。

## 1. 配置在哪里，怎样应用

有三类配置：

| 文件或设置 | 用途 | 修改后的应用方式 |
| --- | --- | --- |
| 部署目录 `.env` | 镜像、容器 IP、宿主机挂载目录、管理密码、健康检查 URL | 用 Compose 重新创建容器 |
| 宿主机数据目录 `config.yaml`，容器内 `/data/config.yaml` | 运行设置、平台开关、规则、任务周期、监听参数 | Web/API 保存时应用；直接改文件需重启 |
| 数据目录 `platforms.yaml`，容器内 `/data/platforms.yaml` | 平台域名、黑名单、内置换源、IP 池与测试 URL | 修改后重启加载 |

首次启动自动创建运行配置和种子数据。`configs/config.example.yaml` 是文档示例，不会自动挂载或覆盖运行配置。

Web 中的开关会调用配置 API，不是只修改文件。保存时先校验，再原子写入并串行应用；下载监听启动失败等情况会恢复旧配置并返回错误。启动参数虽然可保存，但必须重启才能切换，状态中的 `restart_required` 会提示。

直接编辑 YAML 时建议先停止容器、备份、修改，然后启动，避免被页面的完整配置保存覆盖。不要把本页的局部示例当作整个配置文件替换；在现有配置中修改对应字段。

## 2. Compose 的 `.env` 变量

| 变量 | 示例/默认值 | 含义 |
| --- | --- | --- |
| `XBOX_IMAGE` | `ghcr.io/rosemengzi/xbox-speedup:latest` | 拉取的镜像；也可固定 SHA 标签或摘要 |
| `XBOX_IP` | `192.168.1.250` | macvlan 容器 IPv4，必须与外部网络的网段匹配且未被其他设备使用 |
| `XBOX_NETWORK` | `xbox_macvlan` | 已创建的外部 Docker 网络名称 |
| `XBOX_DATA_DIR` | `./run-data` | **宿主机**数据目录，Compose 将它挂载为容器 `/data`；NAS 推荐绝对路径 |
| `XBOX_CERT_DIR` | `./certs` | **宿主机**证书目录，挂载为容器 `/certs:ro` |
| `XBOX_WEB_TOKEN` | 空 | 管理密码；空时加载/生成 `/data/web-token`，用户名固定 `admin` |
| `XBOX_HEALTH_URL` | `http://127.0.0.1:8080/healthz` | 容器内部健康检查地址；管理 HTTP 端口变化时要同步调整 |

`.env.example` 应仅在首次部署时复制成 `.env`。升级不要再次覆盖自己的 `.env`。密码含特殊字符时按 Compose 的环境文件规则正确引用；使用自动生成密码可以避免手动处理这些字符。

程序本身还读取 `XBOX_DATA_DIR`、`XBOX_SEED_DIR`。官方镜像中分别固定为 `/data`、`/app/data`，与上述宿主机 `.env` 的 `XBOX_DATA_DIR` 用途不同：默认 Compose 只将宿主机目录映射进去，不把该路径传给程序。通常不需要修改容器内部路径。

## 3. 运行配置字段

### 监听、管理 HTTPS 与 DNS

| 字段 | 默认值 | 说明 | Web/API 保存后需重启 |
| --- | --- | --- | --- |
| `listen.dns` | `:53` | UDP/TCP DNS 地址 | 是 |
| `listen.http` | `:80` | HTTP 下载换源/回源地址，随换源开关启停 | 是 |
| `listen.web` | `:8080` | HTTP 管理地址，健康检查也使用此监听 | 是 |
| `web_tls.enabled` | `false` | 是否增加管理 HTTPS 监听 | 是 |
| `web_tls.addr` | `:8443` | 管理 HTTPS 地址 | 是 |
| `web_tls.cert_file` | `/certs/fullchain.pem` | 管理证书路径，证书需覆盖访问域名 | 是 |
| `web_tls.key_file` | `/certs/privkey.pem` | 管理证书私钥路径 | 是 |
| `advertise_ip` | 空，自动探测 | 告诉主机的容器 IPv4，用于下载代理 DNS 接管 | 是 |
| `upstream.dns` | `223.5.5.5:53`、`119.29.29.29:53` | 按序尝试的独立上游，必须写端口；不要指回本服务形成回环 | 否 |
| `ipv6_filter` | `true` | 对有效节点、黑名单或就绪接管域名过滤 AAAA；普通域名仍转发 | 否 |
| `log_all_queries` | `false` | 记录更多普通查询的转发事件 | 否 |
| `data_dir` | `/data` | 保留的兼容字段；实际文件路径由启动环境和挂载控制，不会自动迁移数据 | 修改会提示重启，但不应以此移动数据 |

监听地址为 `host:port` 或 `:port`。部署仍建议使用表中默认端口；修改 DNS 或 TLS 下载端口时，要确保客户端能访问实际协议端口。管理 HTTPS 与下载 TLS 不能同时共用同一个非零端口。

管理 HTTPS 与下载 TLS 是两套路径：前者需要证书、默认 `8443`；后者透传 CDN 的原始 TLS、默认 `443`，不使用管理证书。

### 平台设置

键名必须对应 `platforms.yaml` 中的平台。例如：

```yaml
platforms:
  XboxGlobal:
    enabled: true
    speedtest: true
    hidden: false
    pinned_ip: ""
```

| 字段 | 含义 |
| --- | --- |
| `enabled` | 该平台是否应用 DNS 优选、黑名单与可选换源 |
| `speedtest` | 是否参与全量/自动测速；行上的单池手动测速按钮另行触发 |
| `hidden` | 隐藏平台且不应用加速；Web 的隐藏操作同时关闭启用和测速 |
| `pinned_ip` | 有效 IPv4 或空；非空优先使用它，不纳入该平台的全量/自动测速条件 |

默认平台表见 [README](../README.md)。隐藏后的恢复按钮只恢复显示，需要再手动启用。锁定 IP 的格式会校验，资源和线路可用性需自行验证。

### 换源与 HTTPS 透传

```yaml
redirect:
  enabled: false
  tls_addr: ":443"
  smart_fallback: true
  rules:
    - from: assets1.xboxlive.com
      to: assets1.xboxlive.cn
      enabled: true
    - from: assets1.xboxlive.cn
      to: assets1.xboxlive.com
      enabled: false
```

| 字段 | 默认值/含义 |
| --- | --- |
| `redirect.enabled` | 默认 `false`；立即启停 HTTP 和下载 TLS 监听 |
| `redirect.tls_addr` | 默认 `:443`；下载 TLS 监听地址，修改需重启 |
| `redirect.smart_fallback` | 默认 `true`；HTTP 探测失败/资源缺失时回源 |
| `redirect.rules[].from` | 精确源域名，不带协议、路径或端口 |
| `redirect.rules[].to` | HTTP 目标域名，可带端口；不带协议或路径 |
| `redirect.rules[].enabled` | 是否启用该条规则 |

HTTP 监听地址在 `listen.http`，不是 `redirect.addr`。TLS 始终透传到原域名的 CDN `443`，不会把 TLS 请求改成 `to` 域名。

同一源域名只能配置一次，启用规则不能形成循环。用户规则可覆盖平台内置的同源规则，显式关闭同源规则可删除内置规则。源域名属于已停用/隐藏平台时，该规则不会接管流量。

默认包含指定 Xbox `.com → .cn` 规则，并保留一条关闭的反向示例。更多说明见 [redirect.md](redirect.md)。

### 测速参数

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `speedtest.enabled` | `true` | 自动测速总开关，影响启动、周期及同步后的增量测速；不禁止手动测速 |
| `speedtest.schedule` | `0 */6 * * *` | 自动全量测速周期 |
| `speedtest.ping_top_n` | `10` | 对下载型池选择多少个低延迟候选；上限 100 |
| `speedtest.download_mb` | `30` | 每个下载样本最多读取的 MiB；上限 1024 |
| `speedtest.timeout_seconds` | `10` | 单个下载样本超时秒数；上限 120 |
| `speedtest.freshness_minutes` | `30` | 完整测速时复用有效下载成绩的窗口；上限 1440，0 不复用 |

示例：一个有 10 个需重新下载的候选池，默认样本读取量上限合计约 300 MiB；实际量受超时、响应长度和缓存复用影响。测试不同池会依次进行。

新鲜度控制下一次全量测速是否复用成绩，不是后台健康探测或到期自动撤销 DNS 的定时器。全量测速会清理本轮未进入 top-N 的历史下载成绩；下载失败的节点不会以延迟成绩冒充下载冠军。测试 URL 为空的池只按有效延迟选择。

### IP 同步参数

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `ip_sync.enabled` | `true` | 自动同步开关；手动同步仍可执行 |
| `ip_sync.schedule` | `0 4 * * *` | 每天 04:00 同步所有定义的池 |
| `ip_sync.proxies` | 见 YAML 示例 | 按顺序尝试的 HTTP(S) URL 前缀；空字符串表示直连 GitHub 原始文件 |

程序在配置的前缀之后拼接上游完整 URL，末尾还会尝试内置 jsDelivr 地址。这些前缀是 IP 文件下载源，不是对 Xbox 下载流量使用的 HTTP/SOCKS 代理。外部下载源的可用性会变化，应以同步日志为准。

### cron 与时间

采用分、时、日、月、星期的常用五字段 cron，按容器时区运行：

```text
0 */6 * * *    每天 00:00、06:00、12:00、18:00
0 4 * * *      每天 04:00
0 4 * * 0      每周日 04:00
```

官方镜像时区是 `Asia/Shanghai`。非法周期会在保存时被拒绝。页面提供预设周期；自定义表达式需用 YAML/API 修改，页面会显示自定义值。

## 4. 域名表和 IP 列表

`/data/platforms.yaml` 定义 `pools` 和 `platforms`：

- 池：`ip_file` 指定 `/data/ip` 中的文件，`test_url` 指定带 Host 的真实测试资源，空 URL 表示只测延迟。
- 平台：`pool` 指向池，`hosts` 是精确下载域名，`blacklist` 是阻断域名，`redirects` 是平台内置 HTTP 归并规则。

新增平台时也要在 `config.yaml` 的 `platforms` 中添加对应开关，不能只增加域名表就期待自动启用。

IP 文本首行为对应关键字，后续为 IPv4 和可选位置，例如 `IP.Akamai.txt`：

```text
Akamai
203.0.113.10    (示例位置)
203.0.113.11    (示例位置)
```

这些文档地址只展示格式，不能作为实际 CDN 节点。在线同步会覆盖对应池的缓存文件，所以自行添加的节点不会保证在下一次同步后保留；需要保留手工列表时应关闭自动同步并避免对它手动同步，或另行设计自定义池。

已有域名表不会在升级时自动覆盖；新增默认域名需要手动合并。更改域名表或直接改 IP 文件后重启加载。

## 5. 管理 API

这些是当前管理页面使用的接口，并非另一个独立服务。除 `/healthz` 外需要 HTTP Basic Auth；示例 `curl -u admin` 会询问密码，避免将密码写进命令。

| 日常方法与路径 | 内容 |
| --- | --- |
| `GET /healthz` | 200/503 服务就绪，无需认证 |
| `GET /api/status` | 当前节点、自动推荐、运行监听、代理/测速状态、`restart_required` |
| `GET /api/config` | 完整运行配置，不含管理密码 |
| `POST /api/config` | 替换完整配置，校验并保存/应用 |
| `GET /api/pool?name=Akamai` | 池自动冠军及候选成绩，池名区分大小写 |
| `POST /api/speedtest` | 手动测速，JSON `{"pool":"Akamai"}`；空池表示全量 |
| `POST /api/sync` | 手动同步 IP 列表 |
| `GET /api/logs/recent` | 当前环形日志快照 |
| `GET /api/logs` | SSE 实时事件流，含连接刷新和心跳 |

只读调用示例：

```bash
curl -u admin http://192.168.1.250:8080/api/status
curl -u admin 'http://192.168.1.250:8080/api/pool?name=Akamai'
curl -u admin -N http://192.168.1.250:8080/api/logs
```

手动触发示例：

```bash
curl -u admin -X POST -H 'Content-Type: application/json' \
  --data '{"pool":"Akamai"}' http://192.168.1.250:8080/api/speedtest
curl -u admin -X POST http://192.168.1.250:8080/api/sync
```

更新完整配置时，先导出当前对象，编辑后提交：

```bash
curl -u admin http://192.168.1.250:8080/api/config -o config-current.json
# 复制为 config-new.json，编辑需要改变的字段，保留其他字段。
curl -u admin -X POST -H 'Content-Type: application/json' \
  --data-binary @config-new.json http://192.168.1.250:8080/api/config
```

`POST /api/config` 不是 PATCH，不能只发送一个需要改动的局部对象。它拒绝未知 JSON 字段和多个 JSON 对象；请求体最多 1 MiB。保存失败查看响应正文；成功后仍需检查 `restart_required`。

JSON 成绩字段 `speed_mbps` 是历史字段名，实际数值单位为 **MiB/s**，不是 Mbps；`rtt_ms` 是毫秒。返回的任务触发 `ok` 不代表后台任务已完成。

浏览器修改请求会检查 Origin/跨站来源，认证不能代替此检查。管理 API 应按 NAS 的局域网管理策略访问，需要加密传输时启用管理 HTTPS。
