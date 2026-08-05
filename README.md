# Xbox 下载加速器

把 [skydevil88/XboxDownload](https://github.com/skydevil88/XboxDownload) 的「主机加速」功能抽出来，
做成一个适合运行在群晖 Docker 中、拥有独立局域网 IP 的下载加速服务。Xbox 或其他游戏机
把 DNS 指向本服务后，即可使用测速选出的 CDN 节点下载；Web 管理界面提供运行状态、实时日志、
手动测速、周期任务和 302 重写配置。

> 只做主机加速，不含原项目的防 DNS 污染、TLS 中间人、商店查询、硬盘回传等功能。

## 原理

游戏机把 DNS 指向本服务后，所有查询都发过来：

1. **命中加速域名**（Xbox/PS/NS/EA/战网等下载域名）→ 返回测速选出的**最快 IP**，游戏机直连 CDN 下载（几十 GB 的数据**不经过容器**）。
2. **命中黑名单域名** → 返回 `0.0.0.0` 屏蔽（屏蔽会干扰加速的域名）。
3. **加速域名的 AAAA(IPv6)** → 返回空，逼游戏机走 IPv4 快 IP。
4. **其余域名** → 转发上游 DNS（阿里/腾讯），保证游戏机正常联网。

另有一个**可选的 80 端口 302 重写层**（默认关）：把 `assets1.xboxlive.com` 一类国际域名
302 跳到 `.cn` 国内 CDN；反向也支持。开启**智能兜底**后，会先探测目标 CDN 是否有该资源，
若 cn 缺该游戏（404/403）则自动绕回原域名，避免下载失败。详见 [docs/redirect.md](docs/redirect.md)。

## 技术栈

- **Go 1.25**，单静态二进制，Alpine 镜像
- **GitHub Actions + GHCR**，在 GitHub 构建并发布 amd64/arm64 镜像
- [miekg/dns](https://github.com/miekg/dns) DNS 服务
- [pro-bing](https://github.com/prometheus-community/pro-bing) ICMP 测速（不可用时退化 TCP 延迟）
- [robfig/cron](https://github.com/robfig/cron) 周期调度
- 内嵌 Web 界面（原生 JS + SSE 实时日志）

## 快速开始（群晖 macvlan）

完整步骤见 [docs/deploy-synology.md](docs/deploy-synology.md)。概要：

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
4. 用**另一台设备**浏览器打开 `http://<容器IP>:8080`（macvlan 下 NAS 本机通常无法直接访问容器），执行全量测速。
5. Xbox 主 DNS 设为容器 IP，辅 DNS 留空；按实际网络情况决定是否关闭 IPv6。停止使用时把 DNS 改回自动获取。

## 数据来源

- **IP 列表**：从上游 GitHub 在线同步（跟随作者更新），本地缓存兜底，详见 [internal/ipsync](internal/ipsync/ipsync.go)。
- **域名表**：内置为可编辑的 [data/platforms.yaml](data/platforms.yaml)，极少变动，改它不用改代码。

## 配置

容器首启在 `/data/config.yaml` 生成默认配置，绝大多数项可在 Web 界面改。
完整字段说明见 [configs/config.example.yaml](configs/config.example.yaml)。

### HTTPS 管理界面

将证书目录以只读方式挂载到 `/certs`，并在运行时 `config.yaml` 中启用：

```yaml
web_tls:
  enabled: true
  addr: ":443"
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
