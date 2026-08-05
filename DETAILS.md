# 目录结构与模块职责

```
xbox-speedup/
├── .github/workflows/
│   └── container.yml      GitHub Actions 多架构镜像构建与 GHCR 发布
├── cmd/xboxspeedup/
│   ├── main.go            装配各模块、调度器(cron)、优雅退出
│   └── seed.go            首启把镜像内置数据播种到数据卷
├── internal/
│   ├── config/            运行期配置：加载/落盘/无锁热替换 + 变更回调
│   ├── rules/             静态域名表：加载 platforms.yaml 并建索引
│   ├── ipstore/           各 IP 池的候选 IP、测速记录、当前最快 IP(含 EWMA/新鲜度)
│   ├── ipsync/            从上游 GitHub 在线同步 IP 列表 + 本地缓存
│   ├── speedtest/         测速引擎：ping 选 top-N → 下载比带宽 → 选最快
│   ├── dnssrv/            DNS 服务(核心)：命中加速/黑名单/劫持，未命中转发上游
│   ├── proxy/             80 端口 302 重写层 + 智能 404 探测 + 回源反代
│   │   ├── proxy.go
│   │   └── cache.go       探测结论缓存(按 ContentID 目录)
│   ├── logstore/          环形日志缓冲 + 订阅分发(SSE)
│   ├── netutil/           出口 IP 探测、强制连指定 IP 的 HTTP 工具
│   └── web/               Web 管理：状态/日志/测速/同步/配置 + 内嵌前端
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
│   └── redirect.md        302 重写层原理
└── LICENSE                MIT 许可证
```

## 数据流

```
游戏机 DNS 查询 ─► dnssrv ─┬─ 加速域名 ─► ipstore.Best(池) ─► 返回最快 IP ─► 游戏机直连 CDN
                          ├─ 黑名单   ─► 0.0.0.0
                          ├─ 重写源域名(redirect 开) ─► 容器 IP ─► proxy:80 ─► 302/回源反代
                          └─ 其余     ─► 转发上游 DNS

cron/手动 ─► speedtest ─► ping+下载 ─► 写 ipstore 记录 ─► ComputeBest ─► dnssrv 下次查询即用新最快 IP
cron/手动 ─► ipsync   ─► 拉上游 IP 列表 ─► ipstore.SetList(diff) ─► speedtest 增量测新 IP
```

## 关键设计

- **镜像统一由 GitHub 构建**：`main` 分支和版本标签触发 GitHub Actions，镜像发布到 GHCR；
  Compose 只拉取远程镜像，不包含本地构建配置。
- **容器不碰下载流量**：DNS 把 CDN 真实 IP 直接给游戏机，几十 GB 直连。唯一例外是 302 智能兜底里
  「目标缺资源」时的回源反代，那是罕见分支。
- **配置无锁热替换**：`config.Manager` 用读写锁持有快照，`Replace` 先落盘再换指针并触发回调，
  dnssrv/proxy 据此原子重建索引，开关即时生效，无需重启（80 端口的启停除外）。
- **测速结果会过期**：记录带时间戳，选最快只信新鲜记录；周期全量重排对抗过期，IP 同步只增量测新 IP。
- **依赖注入、低耦合**：proxy 不依赖 rules，靠注入的 `poolOf` 函数查池；web 靠 `Deps` 注入全部依赖。
