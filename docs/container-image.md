# 镜像、升级、备份与回退

镜像由本仓库的 GitHub Actions 构建并发布到 GHCR。NAS 只拉取和运行，不安装 Go、不执行 `docker build` 或 `docker compose up --build`。

初次部署见 [威联通指南](deploy-qnap.md) / [群晖指南](deploy-synology.md)，管理操作见 [使用说明](user-guide.md)。以下 NAS 命令按群晖目录举例；威联通需将部署目录 `/volume1/docker/xbox-speedup`、数据目录 `/volume1/docker/xbox-speedup-data` 替换为自己的共享文件夹路径，例如 `/share/Container/xbox-speedup`、`/share/Container/xbox-speedup-data`。容器内的 `/data`、`/certs` 保持相同。

## 1. 镜像地址和架构

```text
ghcr.io/rosemengzi/xbox-speedup:latest
```

发布 `linux/amd64`、`linux/arm64`，Compose 根据宿主机架构选择；不含 32 位 ARM。

2026-10-09 已验证修复版 `sha-83e1456` 的双架构清单可匿名读取，对应代码 [83e1456](https://github.com/rosemengzi/xbox-speedup/commit/83e145628459d0af8448a732d3ef133efe7fff60)。完整索引摘要为：

```text
sha256:cf9ee4690b0be56f45cc14720fdb943216bd1e745fe1f699c48e149f54731534
```

此记录标识一个已验证版本，不代表以后 `latest` 始终指向这个摘要。

## 2. GitHub 构建与首次 Fork 发布

工作流 [.github/workflows/container.yml](../.github/workflows/container.yml) 在以下事件运行：

| 事件 | 行为 |
| --- | --- |
| `main` 更新 | 测试并构建，发布 `latest` 与 SHA 标签 |
| 推送 `v*` 标签 | 发布版本标签与 SHA 标签 |
| 手动 workflow_dispatch | 按选择的分支构建；默认分支可更新 `latest` |
| Pull Request | 测试及镜像构建验证，不发布 |

CI 先执行 `go test -race ./...`、`go vet ./...`，再构建并发布双架构镜像、来源证明和 SBOM。包含 `[skip ci]` 的纯文档提交可以跳过构建，因此源码仓库的最新文档提交与镜像中的代码 revision 不一定相同。

刚创建的 Fork 若没有触发构建，在 Actions 中启用工作流，然后打开 Build container image，选择 Run workflow 和 `main`。等发布步骤成功，再在 NAS 拉取。

代码仓库公开不自动代表镜像包公开。个人容器包首次发布默认私有；参见 [GitHub 包可见性说明](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)。如果另一个 Fork 需要匿名拉取，在该账号 Packages 中打开容器包，进入 Package settings → Change visibility → Public；该公开变更不可改回 Private。保留私有时先在 NAS 登录：

```bash
sudo docker login ghcr.io -u rosemengzi
```

密码使用具备 `read:packages` 权限的个人访问令牌，不使用 GitHub 账号密码。使用其他 Fork 时替换账号和 `XBOX_IMAGE`。本 Fork 的上述已验证镜像可匿名访问，不需要为了使用它先创建令牌。

## 3. `latest`、版本标签和固定摘要

| 引用 | 用途 |
| --- | --- |
| `:latest` | 跟随默认分支最新成功发布，适合试用 |
| `:v...` | 固定已发布版本标签；只有真正发布的标签才可用 |
| `:sha-83e1456` | 固定对应已构建源码的短 SHA 标签 |
| `@sha256:...` | 按不可变内容摘要固定镜像 |

在 `.env` 设置，例如：

```dotenv
XBOX_IMAGE=ghcr.io/rosemengzi/xbox-speedup:sha-83e1456
```

也可按本文已验证摘要固定：

```dotenv
XBOX_IMAGE=ghcr.io/rosemengzi/xbox-speedup@sha256:cf9ee4690b0be56f45cc14720fdb943216bd1e745fe1f699c48e149f54731534
```

`latest` 会变化；稳定运行后建议记下自己验证过的 SHA 标签/摘要，以便回退。仓库某个提交如果没有构建，不能假设存在同名镜像 SHA 标签。

## 4. 查看当前运行版本

```bash
sudo docker inspect --format '{{.Config.Image}}' xbox-speedup
sudo docker inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' xbox-speedup
sudo docker inspect --format '{{.State.Health.Status}}' xbox-speedup
```

第一条是运行容器使用的镜像引用，第二条是镜像的源码 revision，第三条是就绪状态。`latest` 名称本身不能说明容器是否已经更新；必须拉取并重建才能使用新内容。

默认 Compose 使用 `pull_policy: always`，启动项目时会检查远程镜像；普通 `docker restart` 不会拉取新镜像。Compose 的拉取策略见 [Docker 服务配置](https://docs.docker.com/reference/compose-file/services/#pull_policy)。

## 5. 日常更新

升级前先完成备份并记录当前镜像版本，方法见下节。使用 Git 克隆的部署目录：

```bash
cd /volume1/docker/xbox-speedup
sudo git pull --ff-only
sudo docker compose config --quiet
sudo docker compose pull
sudo docker compose up -d
sudo docker compose ps
sudo docker logs --tail 100 xbox-speedup
```

没有 Git 时，重新获取最新 `docker-compose.yml` 和 `.env.example` 供对照，保留自己的 `.env` 和数据目录；不要直接覆盖运行配置或密码。随后执行同样的 pull/up 命令。

镜像内容变化时 Compose 重建容器，现有挂载数据保留。检查 health、重新登录管理页面，并确认平台和任务设置。测速成绩重启后清空，自动测速开启时会重新测；关闭时手动测速或继续使用锁定 IP。

### 从原版迁移到修复版

- 首次出现管理认证：用 `admin` 和 `.env` 密码或数据目录的 `web-token` 登录。
- 管理 HTTPS 默认改为 `8443`；旧配置已写 `443` 时不会被升级自动改动，需自行改端口并重启再启用下载代理。
- HTTP 换源开关现在同时启动下载 TLS `443`，需要两个端口均可用。
- 未测或失败的节点不再被当作有效冠军，可能显示「上游解析」；完成测速后再看实际效果。
- 域名表保持用户版本，不自动合并新增域名。

## 6. 备份与数据恢复

需要保留：数据目录、部署 `.env`、自定义 Compose；如使用自己的管理证书，备份证书所在目录。备份包含管理凭据，应按管理密码保存，不上传到公开仓库。

下面创建停止服务时的一致文件快照。备份之前先暂停下载并安排 DNS 恢复或维护窗口，避免 Xbox 仍依赖已停止的服务：

```bash
sudo mkdir -p /volume1/docker/xbox-speedup-backups
cd /volume1/docker/xbox-speedup
XBOX_BACKUP_STAMP=$(date +%Y%m%d-%H%M%S)
sudo cp .env "/volume1/docker/xbox-speedup-backups/env-${XBOX_BACKUP_STAMP}"
sudo cp docker-compose.yml "/volume1/docker/xbox-speedup-backups/compose-${XBOX_BACKUP_STAMP}.yml"
sudo docker stop xbox-speedup
sudo tar -C /volume1/docker -czf "/volume1/docker/xbox-speedup-backups/data-${XBOX_BACKUP_STAMP}.tar.gz" xbox-speedup-data
sudo docker start xbox-speedup
```

根据自己的 `XBOX_DATA_DIR` 替换 tar 的目录。只有文件配置、列表和密码可恢复；内存测速成绩、探测结论和 Web 日志不在备份中。

恢复建议先把归档解压到一个新目录进行核对，保留当前数据，再让 `.env` 的 `XBOX_DATA_DIR` 指向核对后的恢复目录并重建容器。不要直接用旧备份覆盖仍在运行并保存配置的数据目录。

## 7. 回退镜像

在 `.env` 把 `XBOX_IMAGE` 改成自己先前验证过的 SHA 标签或摘要，然后：

```bash
cd /volume1/docker/xbox-speedup
sudo docker compose pull
sudo docker compose up -d
sudo docker compose ps
```

拉取旧镜像失败但该镜像已存在本机时，可使用：

```bash
sudo docker compose up -d --pull never
```

该选项不下载，只使用本机已有镜像；不存在则无法启动。参见 [Docker compose up](https://docs.docker.com/reference/cli/docker/compose/up/)。

镜像回退不会自动回退持久化配置。需要恢复配置时按备份流程处理，先核对新旧程序可读的字段、管理密码和端口；必要时使用对应数据快照。

## 8. 重启、重建与停止的区别

| 需要完成的事 | 操作 |
| --- | --- |
| 重新加载直接编辑的运行 YAML 或已挂载证书 | `sudo docker restart xbox-speedup` |
| 应用 `.env`、密码环境变量、容器 IP、挂载目录或 Compose 变更 | Compose 重建；`.env` 不是程序动态读取文件 |
| 使用新版本程序 | pull 后 up，让镜像更新触发重建 |
| 暂时停服务 | 先恢复主机 DNS，再停止容器 |

重建示例：

```bash
cd /volume1/docker/xbox-speedup
sudo docker compose up -d --force-recreate
```

如只想使用已有镜像重建，可加 `--pull never`。变更容器 IP 时也要更新 Xbox DNS；设置了固定 `advertise_ip` 时须同步改运行配置，否则代理会返回旧地址。

换源开关、平台开关、锁定 IP 和任务周期通过 Web/API 保存后即可应用；直接编辑 YAML 需要重启。修改启动参数后以页面重启提示为准。

## 9. 维护域名表

用户 `/data/platforms.yaml` 始终优先。需要查看新镜像内置默认表时，可复制到一个对照文件：

```bash
sudo docker cp xbox-speedup:/app/data/platforms.yaml /volume1/docker/xbox-speedup/platforms.new-default.yaml
```

与数据目录中的 `platforms.yaml` 对照，手动合并需要的域名/池，保留自定义内容，重启加载。新增平台还需在运行配置中启用；不要直接覆盖整张用户表以免丢失修改。

## 10. 健康检查与日志

镜像 HEALTHCHECK 默认每 30 秒访问一次内部 `/healthz`，超时 5 秒，启动宽限 15 秒，连续失败 3 次标记 unhealthy。接口检查 DNS 及已启用下载监听就绪，不检查 CDN 或互联网。

Compose 的普通 `restart: unless-stopped` 按进程/容器退出处理，不会仅因为 unhealthy 自动重启。修改管理 HTTP 地址/端口要同步调整 `XBOX_HEALTH_URL` 并重建容器。

Docker `json-file` 日志配置为每个文件最多 10 MiB、最多 3 个文件。Web 的实时日志是独立的内存缓冲，并非同一个日志文件。详细排查见 [使用说明](user-guide.md)。
