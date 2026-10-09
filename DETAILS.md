# 目录结构与模块职责

```
xbox-speedup/
├── .github/workflows/
│   └── container.yml      GitHub Actions 多架构镜像构建与 GHCR 发布
├── cmd/xboxspeedup/
│   ├── main.go            装配各模块、调度器(cron)、优雅退出
│   └── seed.go            首启把镜像内置数据播种到数据卷
├── internal/
│   ├── config/            配置校验、快照、串行应用与失败回滚
│   ├── rules/             静态域名表：加载 platforms.yaml 并建索引
│   ├── ipstore/           各 IP 池的候选 IP、测速记录、当前最快 IP与新鲜度
│   ├── ipsync/            从上游 GitHub 在线同步 IP 列表 + 本地缓存
│   ├── speedtest/         测速引擎：ping 选 top-N → 下载比带宽 → 选最快
│   ├── dnssrv/            DNS 服务(核心)：命中加速/黑名单/劫持，未命中转发上游
│   ├── proxy/             HTTP 换源、资源探测、回源反代与 HTTPS 透传
│   │   ├── proxy.go
│   │   ├── cache.go       有界探测缓存(完整 URI、节点、规则版本)
│   │   └── tls.go         ClientHello SNI 路由，透传原始 TLS 字节
│   ├── persist/           临时文件、fsync 与原子替换
│   ├── logstore/          环形日志缓冲 + 订阅分发(SSE)
│   ├── netutil/           独立上游 DNS、TCP 重试、出口 IP 与 HTTP 工具
│   └── web/               Web 管理：状态/日志/测速/同步/配置 + 内嵌前端
│       ├── auth.go        持久化管理密码、Basic Auth 与同源检查
│       └── ui/index.html  单页面前端
├── data/
│   ├── platforms.yaml     域名表(可编辑)：平台→域名/IP池/黑名单/内置重写
│   └── ip/*.txt           IP 列表快照(首启播种源，运行期由 ipsync 刷新)
├── configs/config.example.yaml  配置示例(带注释)
├── .env.example          本地部署变量示例；实际 .env 不提交
├── Dockerfile             GitHub Actions 使用的多阶段构建定义，Alpine 运行
├── docker-compose.yml     从 GHCR 拉取镜像的群晖 macvlan 部署配置
├── docs/
│   ├── container-image.md GitHub 镜像构建、标签与部署更新说明
│   ├── deploy-synology.md 群晖部署与 HTTPS 配置
│   └── redirect.md        HTTP 换源与 HTTPS 透传原理
└── LICENSE                MIT 许可证
```

## 数据流

```
游戏机 DNS 查询 ─► dnssrv ─┬─ 加速域名 ─► 手动 IP/有效测速节点 ─► 游戏机直连 CDN
                          ├─ 黑名单   ─► 0.0.0.0
                          ├─ 换源启用且代理正常 ─► 容器 IP ─┬─ :80  ─► 302/回源反代
                          │                                 └─ :443 ─► 原 CDN TLS 透传
                          └─ 无有效节点/其余域名 ─► 上游 DNS，截断时改用 TCP

cron/手动 ─► speedtest ─► ping+下载 ─► 写 ipstore 记录 ─► ComputeBest ─► dnssrv 下次查询即用新最快 IP
cron/手动 ─► ipsync   ─► 拉上游 IP 列表 ─► ipstore.SetList(diff) ─► speedtest 增量测新 IP
```

## 关键设计

- **镜像统一由 GitHub 构建**：`main` 分支和版本标签触发 GitHub Actions，镜像发布到 GHCR；
  Compose 只拉取远程镜像，不包含本地构建配置。
- **流量路径清晰**：纯 DNS 优选及 HTTP 302 后的下载直接连接 CDN；HTTP 回源与 HTTPS 透传经容器转发。
- **配置可靠应用**：`config.Manager` 校验后原子写入，串行更新快照并触发回调；应用失败时恢复旧配置。
  下载代理开关立即启停；端口、证书等启动参数变更在 Web 状态中提示重启。
- **按当前有效带宽选优**：全量测速只保留本轮延迟 top-N 的下载成绩，最终直接比较最近一次下载速度；
  下载测量串行执行并限制字节数；无有效带宽成绩时回退上游。仅测延迟的池使用独立策略。
  自动测速关闭后不再启动或在同步后自动测速。
- **统一规则与就绪状态**：DNS 和代理使用相同的有效规则，平台开关优先；DNS 通过注入的就绪回调检查 HTTP/TLS 监听。
- **管理面认证**：管理页面与 API 要求 Basic Auth；密码独立于配置文件。修改操作要求 POST 和同源访问，`/healthz` 只公开就绪状态。
