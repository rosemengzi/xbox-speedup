# Xbox 下载加速器

把 [skydevil88/XboxDownload](https://github.com/skydevil88/XboxDownload) 的「主机加速」功能抽出来，
做成一个适合运行在群晖 Docker 中、拥有独立局域网 IP 的下载加速服务。Xbox 或其他游戏机
把 DNS 指向本服务后，即可使用测速选出的 CDN 节点下载；Web 管理界面提供运行状态、实时日志、
手动测速、周期任务和 302 重写配置。

> 只做主机加速，不含原项目的防 DNS 污染、TLS 中间人、商店查询、硬盘回传等功能。

## 原理

游戏机把 DNS 指向本服务后，所有查询都发过来：

测速会先从全部 IP 中选出延迟最低的前 N 个，再按当前有效下载速度选择最快 IP；超过新鲜度窗口的
候选会重新测速，本轮未进入前 N 的历史成绩不会继续参与选择。

1. **命中加速域名**（Xbox/PS/NS/EA/战网等下载域名）→ 返回测速选出的**最快 IP**，游戏机直连 CDN 下载（几十 GB 的数据**不经过容器**）。
2. **命中黑名单域名** → 返回 `0.0.0.0` 屏蔽（屏蔽会干扰加速的域名）。
3. **加速域名的 AAAA(IPv6)** → 有有效 IPv4 节点时返回空；没有可用节点时保留上游解析。
4. **其余域名** → 转发上游 DNS（阿里/腾讯），保证游戏机正常联网。

另有一个**可选的 HTTP 换源与 HTTPS 透传层**（默认关）：HTTP 可把 `assets1.xboxlive.com` 一类国际域名
302 跳到 `.cn` 国内 CDN；反向也支持，但不能同时启用形成循环。开启**智能兜底**后，会先探测目标 CDN 是否有该资源，
若资源缺失或探测失败则回源原域名。HTTPS 保留原域名与真实 CDN 证书，通过 `:443` 透传。详见 [docs/redirect.md](docs/redirect.md)。

## 技术栈

- **Go 1.25**，单静态二进制，Alpine 镜像
- **GitHub Actions + GHCR**，在 GitHub 构建并发布 amd64/arm64 镜像
- [miekg/dns](https://github.com/miekg/dns) DNS 服务
- [pro-bing](https://github.com/prometheus-community/pro-bing) ICMP 测速（不可用时退化 TCP 延迟）
- [robfig/cron](https://github.com/robfig/cron) 周期调度
- 内嵌 Web 界面（原生 JS + SSE 实时日志）

## 快速开始（群晖 macvlan）

完整步骤见 [docs/deploy-synology.md](docs/deploy-synology.md)。概要：

> **构建约束**：部署主机（包括群晖 NAS）不执行本地 Docker 构建。所有镜像只能由 GitHub Actions
> 构建并发布到 GHCR，部署时通过 `docker compose pull` 拉取；不要使用 `docker build` 或
> `docker compose up --build`。

1. SSH 建 macvlan 网络（一次性）：
   ```bash
   sudo docker network create -d macvlan \
     --subnet=192.168.1.0/24 --gateway=192.168.1.1 -o parent=eth0 xbox_macvlan
   ```
2. 创建部署配置并修改容器 IP、数据目录：
   ```bash
   cp .env.example .env
   ```
   需要启用前端 HTTPS 时，将 `fullchain.pem` 和 `privkey.pem` 放入
   `XBOX_CERT_DIR` 指向的证书目录。
3. 拉取 GitHub Container Registry 镜像并启动：
   ```bash
   docker compose pull
   docker compose up -d
   ```
4. 用**另一台设备**浏览器打开 `http://<容器IP>:8080`（macvlan 下 NAS 本机通常无法直接访问容器），以 `admin` 登录并执行全量测速。密码可在 `.env` 设置 `XBOX_WEB_TOKEN`；留空时在数据目录自动生成 `web-token`，升级和重启保持不变。
5. Xbox 主 DNS 设为容器 IP，辅 DNS 留空；按实际网络情况决定是否关闭 IPv6。停止使用时把 DNS 改回自动获取。

## 数据来源

- **IP 列表**：从上游 GitHub 在线同步（跟随作者更新），本地缓存兜底，详见 [internal/ipsync](internal/ipsync/ipsync.go)。
- **域名表**：内置为可编辑的 [data/platforms.yaml](data/platforms.yaml)。已有 `/data/platforms.yaml` 会保留；升级时新增的内置域名需要手动合并，修改后重启加载。

本 Fork 基于 [ahaduoduoduo/xbox-speedup](https://github.com/ahaduoduoduo/xbox-speedup)，保留原项目的 MIT 许可证，并增加故障回退、HTTPS 透传和管理认证等修复。

## 配置

容器首启在 `/data/config.yaml` 生成默认配置，绝大多数项可在 Web 界面改。
完整字段说明见 [configs/config.example.yaml](configs/config.example.yaml)。

### 本 Fork 的可靠性修复

- 未测速或所有下载测试失败时恢复上游解析；只测延迟的平台使用独立选择策略。
- DNS 截断应答转 TCP 重试，失败上游自动切换；未有有效 IPv4 节点时保留上游 IPv6。
- 平台停用/隐藏同步作用于 DNS 与换源，自动测速关闭后不再进行启动或同步后的自动测速。
- 资源探测保留完整 URI，按具体资源、节点及规则版本缓存；探测失败不污染缓存。
- 下载测速串行进行，并限制实际读取字节数、校验 200/206 应答。
- Web 可锁定平台 IP，显示自动推荐地址；管理操作要求认证和 POST。
- 配置校验、原子写入和串行应用；监听失败回滚配置，端口/证书变更明确提示重启。
- 下载 HTTPS 在 `:443` 透传原域名，保留 CDN 证书；HTTP 可继续 302 换源。HTTPS 透传和回源时下载数据经过 NAS。

默认仍关闭换源。先验证同一游戏下载的实际速度，再按需开启；NAS 与 Xbox 的出口/分流策略应保持一致。TLS 透传支持带明文 SNI 的 TCP/TLS 请求，不提供 QUIC 或加密 SNI 路由。真实 Xbox 与 NAS 效果仍需要实机验证。

管理密码在宿主机可通过 `cat <XBOX_DATA_DIR>/web-token` 查看，或设置自己的 `XBOX_WEB_TOKEN` 后重建容器。健康检查使用 `/healthz`；修改管理 HTTP 端口时同步调整 `.env` 中的 `XBOX_HEALTH_URL`。健康状态用于发现故障，Docker 的普通 restart 策略不会仅因 unhealthy 自动重启。

### HTTPS 管理界面

将证书目录以只读方式挂载到 `/certs`，并在运行时 `config.yaml` 中启用：

```yaml
web_tls:
  enabled: true
  addr: ":8443"
  cert_file: "/certs/fullchain.pem"
  key_file: "/certs/privkey.pem"
```

证书需要覆盖访问域名。局域网 DNS 或路由器 Host 记录应将该域名解析到容器的 macvlan IP。
服务启动时加载证书，证书续期后需要重启容器。

## 文档

- [docs/deploy-synology.md](docs/deploy-synology.md)：群晖 Docker 部署步骤
- [docs/container-image.md](docs/container-image.md)：GitHub 镜像构建、标签与更新方式
- [DETAILS.md](DETAILS.md)：目录结构与各模块职责
- [TODO.md](TODO.md)：进度与后续计划
- [docs/redirect.md](docs/redirect.md)：302 重写层与智能兜底原理

## 许可证

[MIT](LICENSE)
