# 威联通 Container Station 部署指南

威联通可以运行本项目。它与群晖使用同一个 Go 程序和 Docker 镜像，无需重新开发；主要区别在容器管理工具、共享目录及网络接口。本文采用独立局域网 IP，让 Xbox 将该地址设为 DNS。

镜像地址为 `ghcr.io/rosemengzi/xbox-speedup:latest`，已发布 `linux/amd64` 与 `linux/arm64`。本指南按官方 Container Station 资料和仓库配置编写，**尚未在威联通实机部署验收**。

## 1. 先确认 NAS 能运行镜像

在 App Center 安装并启动 Container Station。下文以 **Container Station 3 和 Compose V2** 为准；不同 QTS/QuTS hero 版本的菜单名称可能不同。Container Station 支持 Docker 镜像和 Compose 应用，参见 [官方使用说明](https://www.qnap.com/en-us/how-to/tutorial/article/how-to-use-container-station-3)。

需要命令行操作时，在控制台启用 SSH，以具有 Docker 管理权限的账号登录 NAS。下文命令在 **NAS 的 SSH 终端** 执行；已进入 root shell 时可省略 `sudo`。

```bash
uname -m
sudo docker version
sudo docker compose version
```

| 架构输出 | 对应镜像 | 是否发布 |
| --- | --- | --- |
| `x86_64` | `linux/amd64`，包括 Intel/AMD 的 64 位 NAS | 是 |
| `aarch64` | `linux/arm64` | 是 |
| `armv7l` / `armv6l` | 32 位 ARM | 否 |

CPU 匹配还需 Docker 正常运行。若命令不存在或版本较旧，先确认 NAS 型号可用的 Container Station 版本。当前命令写作 `docker compose`，中间为空格；见 [QNAP 的 Compose V2 说明](https://www.qnap.com/en/how-to/faq/article/why-cant-i-use-docker-compose-commands-in-container-station)。NAS 不需要安装 Go，也不需要构建镜像。

## 2. 选择网络并确认参数

| 方案 | 适用场景 | 本项目的配置方式 |
| --- | --- | --- |
| macvlan | Xbox、电脑、手机通过独立局域网 IP 访问服务 | 复用仓库默认 Compose，见第 4 节 |
| 威联通 qnet | 还需要 NAS 本机或其他网络中的容器访问此服务 | 使用第 5 节的 Container Station 应用示例并验证互通 |

macvlan 是 Docker 通用驱动，适用于满足其要求的 Linux 主机。**普通 macvlan 网络隔离宿主机与容器**：NAS SSH 中访问容器 IP 失败，而电脑访问正常，并不代表服务故障。网络设备须允许同一上行接口承载多个 MAC；见 [Docker macvlan 文档](https://docs.docker.com/engine/network/drivers/macvlan/)。

QNAP 官方人员转述研发团队的说明：只需其他局域网设备访问固定 IP 时可用 macvlan；需要 NAS/其他容器互通时可考虑 qnet。具体路由、防火墙及不同容器网络仍需验证，见 [QNAP 的网络选择说明](https://community.qnap.com/t/settting-a-docker-container-to-a-static-ip-via-docker-compose/6214/4)。

确认实际接口：

```bash
ip -o link
ip -o -4 addr
ip route
```

如 NAS 没有完整的 `ip` 工具，可用 `ifconfig`、`route -n` 辅助查看。结合 **网络与虚拟交换机（Network & Virtual Switch）** 页面，确认 Xbox 所在局域网连接的是哪一个接口。多网口、端口聚合、虚拟交换机或 VLAN 都可能改变接口名称；不能只按“网卡 1”推断为 `eth0`。

| 参数 | 本文示例 | 必须确认的内容 |
| --- | --- | --- |
| 局域网网段 | `192.168.1.0/24` | 与 Xbox 所在网络相符 |
| 网关 | `192.168.1.1` | 本网络的实际网关 |
| 容器独立 IP | `192.168.1.250` | 在正确网段内、与 NAS 地址不同、未被占用并排除 DHCP 冲突 |
| macvlan 的 `parent` | `eth0` | 实际连接该局域网的接口，不能照抄示例 |
| qnet 的 `iface` | `br0` | 按 QNAP 网络配置确认的实际接口，不能照抄示例 |

`docker0`、`lxcbr0` 等内部容器网络不自动代表物理局域网。普通 Docker `bridge` 内部地址通常也不能直接作为 Xbox 的 DNS 地址。目标是让容器拥有 **Xbox 可访问的局域网 IP**。不同 VLAN 之间还需要路由和访问规则。

## 3. 准备威联通共享目录

本文使用 `/share/Container` 举例；先在 File Station / 共享文件夹设置中确认它是已有共享文件夹，或创建用于部署的共享文件夹，再核对路径：

```bash
ls -ld /share/Container
readlink -f /share/Container
```

共享文件夹名称不是 `Container` 时，替换本文所有 `/share/Container`。QTS 与 QuTS hero 的底层存储布局可能不同，使用自己确认的共享路径，不直接照搬群晖 `/volume1/docker` 或另一台 NAS 的底层卷路径。

确认共享文件夹存在且指向预期存储后，创建子目录：

```bash
sudo mkdir -p /share/Container/xbox-speedup
sudo mkdir -p /share/Container/xbox-speedup-data
sudo mkdir -p /share/Container/xbox-speedup/certs
```

| 宿主机位置 | 用途 | 容器内位置 |
| --- | --- | --- |
| `/share/Container/xbox-speedup` | Compose 与 `.env` 部署文件 | 不挂载 |
| `/share/Container/xbox-speedup-data` | 配置、域名表、IP 列表、管理密码 | `/data`，可写 |
| `/share/Container/xbox-speedup/certs` | 可选管理 HTTPS 证书 | `/certs`，只读 |

证书目录可以为空；默认管理 HTTP 和下载 TLS 透传都不要求用户提供证书。共享目录和接口的确认方式也可参考 [QNAP 官方容器部署示例](https://www.qnap.com/en/how-to/faq/article/how-to-deploy-unifi-network-application-via-docker-on-qnap-nas)。

## 4. 方式 A：SSH + 默认 Compose + macvlan

适合只让 Xbox 和局域网电脑/手机访问服务的情况，直接使用本仓库的部署文件。

### 获取文件与填写 `.env`

首次部署时下载：

```bash
cd /share/Container/xbox-speedup
sudo curl -fL https://raw.githubusercontent.com/rosemengzi/xbox-speedup/main/docker-compose.yml -o docker-compose.yml
sudo curl -fL https://raw.githubusercontent.com/rosemengzi/xbox-speedup/main/.env.example -o .env.example
sudo cp .env.example .env
```

已有 `.env` 时保留原文件。NAS 无法访问 GitHub 时，可以在电脑下载这两个文件后通过 File Station 上传。首次启动的程序和种子数据已在镜像中，无需上传全部源码。

编辑 `.env`，以下目录和 IP 按实际环境替换：

```dotenv
XBOX_IP=192.168.1.250
XBOX_DATA_DIR=/share/Container/xbox-speedup-data
XBOX_CERT_DIR=/share/Container/xbox-speedup/certs
XBOX_NETWORK=xbox_macvlan
XBOX_IMAGE=ghcr.io/rosemengzi/xbox-speedup:latest
XBOX_WEB_TOKEN=
XBOX_HEALTH_URL=http://127.0.0.1:8080/healthz
```

密码留空会生成并持久化到数据目录的 `web-token`。也可设置自己的管理密码；`.env` 使用 Compose 语法，包含特殊字符时须正确引用，参见 [配置参考](configuration.md)。

### 创建网络

先执行 `sudo docker network ls`。仅在没有目标网络时创建；**先把下面的 `eth0`、网段与网关改为第 2 节确认的实际值**：

```bash
sudo docker network create -d macvlan \
  --subnet=192.168.1.0/24 \
  --gateway=192.168.1.1 \
  -o parent=eth0 \
  xbox_macvlan
sudo docker network inspect xbox_macvlan
```

核对驱动、网段、网关和 `parent`。已有网络时也要核对，不能仅凭同名就复用。默认 Compose 声明的是外部网络，不会替你创建它。

### 拉取与启动

在包含 `.env` 与 Compose 的目录执行：

```bash
cd /share/Container/xbox-speedup
sudo docker compose config --quiet
sudo docker compose pull
sudo docker compose up -d
sudo docker compose ps
sudo docker logs --tail 100 xbox-speedup
sudo docker inspect --format '{{.State.Health.Status}}' xbox-speedup
```

`config --quiet` 检查配置语法，不输出展开后的密码。等待状态为 `healthy`，再从另一台局域网设备测试。健康检查在容器内部访问 `127.0.0.1`，不受 macvlan 宿主机隔离影响；`healthy` 只表示服务监听就绪。

## 5. 方式 B：Container Station 应用 + qnet

需要 NAS 本机互通，或希望通过应用 YAML 管理时，可使用 qnet。这里的网络/IPAM 写法根据 [QNAP 官方 qnet 静态 IP 示例](https://www.qnap.com/en/how-to/faq/article/how-to-deploy-unifi-network-application-via-docker-on-qnap-nas) 调整；不需要修改项目程序。

完成第 1～3 节后，在 Container Station 的 **应用程序 / Applications → 创建 / Create** 粘贴以下 YAML。将 IP、路径、两个 `iface`、网段和网关一起改成实际值，然后使用界面的 Validate 检查并创建。

**这是独立的替代部署方式，不与第 4 节同时启动。** 若已有同名容器，先停止并通过原部署方式移除容器，保留共享数据目录，再切换方式。

```yaml
services:
  xbox-speedup:
    image: ghcr.io/rosemengzi/xbox-speedup:latest
    pull_policy: always
    container_name: xbox-speedup
    restart: unless-stopped
    environment:
      XBOX_WEB_TOKEN: ""
      XBOX_HEALTH_URL: http://127.0.0.1:8080/healthz
    cap_add:
      - NET_RAW
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
    networks:
      xbox_qnet:
        ipv4_address: 192.168.1.250
    volumes:
      - /share/Container/xbox-speedup-data:/data
      - /share/Container/xbox-speedup/certs:/certs:ro
networks:
  xbox_qnet:
    driver: qnet
    driver_opts:
      iface: br0
    ipam:
      driver: qnet
      options:
        iface: br0
      config:
        - subnet: 192.168.1.0/24
          gateway: 192.168.1.1
```

示例使用实际值形式，**不依赖外部 `.env`**。在图形界面只粘贴仓库原始 `${XBOX_*}` YAML，并不会因为某个共享文件夹里有 `.env` 就保证读取它。使用图形界面时将值填入 YAML；使用 SSH 时则在项目目录让 Compose 读取 `.env`。

如设置应用的默认 Web URL 端口，选择 `xbox-speedup` 和 `8080`。该设置只生成入口链接，服务实际监听端口仍由程序配置决定。创建后查看容器日志和健康状态，按下一节验证局域网及所需 NAS 互通。

qnet 需要威联通提供的网络/IPAM 驱动。出现 `plugin not found` 等错误时先确认 Container Station 状态和该版本支持情况，不要将 `qnet` 随意改成普通 `bridge`；仅需外部设备访问时可回到方式 A。

## 6. 登录、设置 Xbox 与验收

从电脑或手机访问 `http://192.168.1.250:8080`，将示例 IP 替换为自己的容器 IP。用户名为 `admin`；使用自动密码时，在 NAS 上查看：

```bash
sudo cat /share/Container/xbox-speedup-data/web-token
```

容器与 NAS 使用不同 IP，因此容器的 `8080`、`80`、`443` 不需要占用 NAS 本身的同号端口；以上两种独立 IP 示例都无需添加宿主机 `ports` 映射。

1. 等待启动测速结束，检查 Xbox 平台有有效节点；需要时同步 IP 后手动测速。
2. 保持「302 重写层」关闭，先验证默认 DNS 优选。将 Xbox 的主 DNS 设置成容器 IP，避免公网备用 DNS 绕过它。
3. 在另一台电脑运行 `nslookup assets1.xboxlive.com 192.168.1.250`。有有效节点时应返回所选 CDN IP；没有有效节点时回退上游，不能只靠应答有 IP 判断加速成功。
4. 比较同一游戏下载速度，测试暂停/恢复与正常联网。
5. 按需要再开启换源；开启后确认独立 IP 上的 TCP `80`、`443` 都可达。纯 DNS 模式的游戏数据直连 CDN；HTTP 回源/HTTPS 透传的数据会经过 NAS。

局域网访问需要 UDP/TCP `53` 和 TCP `8080`；可选下载代理使用 TCP `80`、`443`。已有防火墙规则按实际网络核对。完整操作、日志和验收方法见 [使用说明](user-guide.md)，协议区别见 [下载代理说明](redirect.md)。

## 7. 更新与威联通常见问题

方式 A：备份自己的 `.env` 与持久化数据，记录当前镜像版本；然后在部署目录执行：

```bash
cd /share/Container/xbox-speedup
sudo docker compose config --quiet
sudo docker compose pull
sudo docker compose up -d
sudo docker compose ps
```

方式 B：在 Container Station 拉取所用镜像的新版本，再通过应用的重新创建 / Recreate 更新容器，保留原挂载路径和网络配置。普通重启只使用现有容器，不会切换到新镜像。升级后再次检查健康状态和 DNS；测速成绩在重启后重建。固定版本、备份及回退见 [维护说明](container-image.md)，其中群晖路径需换成这里的实际共享目录。

| 现象 | 检查方法 |
| --- | --- |
| NAS 访问容器失败，电脑可以访问 | macvlan 宿主机隔离；用外部设备验收，需要 NAS 互通则评估 qnet |
| 所有设备均无法访问容器 | 核对实际 `parent` / `iface`、VLAN、IP 冲突、网关和网络访问规则 |
| 网络创建成功，但没有局域网连通 | 创建成功不证明接口选择正确；结合虚拟交换机的物理上行检查 |
| `no matching manifest` | 核对 CPU 架构；当前只发布 amd64/arm64 |
| `network not found` | 方式 A 先创建与 `XBOX_NETWORK` 同名的外部网络 |
| `plugin not found` | 方式 B 核对 qnet 网络与 IPAM 驱动及 Container Station 状态 |
| 无法写配置或读取密码 | 核对真实共享路径、持久化挂载和目录权限 |
| 图形界面创建后目录/IP 不是预期 | 核对应用中实际保存的 YAML；不要假设它读取另一个目录的 `.env` |
| NAS 自己已有 8080/443 服务 | 使用独立容器 IP，不需要修改 NAS 管理端口 |
| healthy 但下载没变快 | 检查有效测速结果、主机是否使用此 DNS，再按同一游戏比较实际速度 |
