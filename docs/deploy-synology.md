# 群晖完整部署指南

目标：让一个容器拥有独立局域网 IP，Xbox 使用该 IP 作为 DNS；NAS 只拉取现成镜像，不编译代码或构建 Docker 镜像。

本文命令在 **NAS 的 SSH 终端** 执行。网段、网关、网卡及 `/volume1` 路径均为群晖示例，必须替换为实际值。威联通请使用 [Container Station 部署指南](deploy-qnap.md)，其中说明了共享目录、虚拟交换机及 macvlan/qnet 的区别。

## 1. 部署前确认环境

需要 NAS 已有 Docker/Container Manager、能运行 Linux 容器，且安装支持当前 Compose 文件的 Compose。镜像只发布 `linux/amd64`、`linux/arm64`，不发布 32 位 ARM 镜像。

```bash
uname -m
sudo docker version
sudo docker compose version
ip -o link
ip -o -4 addr
ip route
```

`x86_64` 对应 amd64，`aarch64` 对应 arm64。若没有 `docker compose`，先使用 NAS 提供的容器项目工具或补齐 Compose；本页后续命令按 Compose 插件编写。

确定以下参数：

| 参数 | 本文示例 | 怎样确定 |
| --- | --- | --- |
| NAS 实际上网接口 | `eth0` | 看 `ip addr`、路由，可能是 `ovs_eth0`、`bond0` 或 VLAN 接口 |
| 局域网网段 | `192.168.1.0/24` | 需与所选接口及 Xbox 所在网络相符 |
| 网关 | `192.168.1.1` | 看 `ip route` 的默认路由 |
| 容器独立 IP | `192.168.1.250` | 未被使用，在 DHCP 分配范围外或已排除冲突 |
| Docker 网络名 | `xbox_macvlan` | 创建与 `.env` 使用同一个名称 |
| 部署文件目录 | `/volume1/docker/xbox-speedup` | 放 Compose 与 `.env` |
| 数据目录 | `/volume1/docker/xbox-speedup-data` | 放持久化配置、IP 文件和管理密码 |

macvlan 适用于 Linux 主机，网络设备要允许同一上行接口出现多个 MAC。普通 macvlan 容器无法直接与 NAS 宿主机通信；电脑/手机和 Xbox 可以按正常局域网路径访问。此限制及网络创建方式见 [Docker macvlan 文档](https://docs.docker.com/engine/network/drivers/macvlan/)。

先只让 Xbox 使用这个 DNS，不必改整个家庭网络的 DHCP DNS。若 Xbox 与 NAS 分属不同 VLAN，需保证主机能访问容器的 DNS 和所选下载监听。

## 2. 获取部署文件

### 方式 A：NAS 已安装 Git

首次部署、目标目录尚未有仓库时：

```bash
sudo git clone https://github.com/rosemengzi/xbox-speedup.git /volume1/docker/xbox-speedup
cd /volume1/docker/xbox-speedup
```

已经有仓库时不要再次克隆；升级看 [维护说明](container-image.md)。

### 方式 B：NAS 没有 Git

只需要 Compose 与环境文件；可以从仓库网页下载，或执行：

```bash
sudo mkdir -p /volume1/docker/xbox-speedup
cd /volume1/docker/xbox-speedup
sudo curl -fL https://raw.githubusercontent.com/rosemengzi/xbox-speedup/main/docker-compose.yml -o docker-compose.yml
sudo curl -fL https://raw.githubusercontent.com/rosemengzi/xbox-speedup/main/.env.example -o .env.example
```

如果 NAS 无法下载 GitHub 文件，在电脑下载后上传到此目录即可。NAS 不需要源码、Go 编译器或 `data/`：运行镜像已内置程序和首启种子文件。

## 3. 创建 macvlan 网络

先检查是否已存在：

```bash
sudo docker network ls
```

仅在不存在时创建，并替换网段、网关和 `parent`：

```bash
sudo docker network create -d macvlan \
  --subnet=192.168.1.0/24 \
  --gateway=192.168.1.1 \
  -o parent=eth0 \
  xbox_macvlan
```

核对：

```bash
sudo docker network inspect xbox_macvlan
```

确认驱动、subnet、gateway 和 parent 正确。已有同名网络时先核对配置，不要因为同名就认为它适用于当前网段。默认 Compose 将该网络声明为 external，不负责自动创建。

## 4. 创建目录和 `.env`

```bash
sudo mkdir -p /volume1/docker/xbox-speedup-data
sudo mkdir -p /volume1/docker/xbox-speedup/certs
cd /volume1/docker/xbox-speedup
sudo cp .env.example .env
sudo vi .env
```

`cp` 只在首次部署时执行。`.env` 示例：

```dotenv
XBOX_IP=192.168.1.250
XBOX_DATA_DIR=/volume1/docker/xbox-speedup-data
XBOX_CERT_DIR=/volume1/docker/xbox-speedup/certs
XBOX_NETWORK=xbox_macvlan
XBOX_WEB_TOKEN=
XBOX_IMAGE=ghcr.io/rosemengzi/xbox-speedup:latest
XBOX_HEALTH_URL=http://127.0.0.1:8080/healthz
```

`XBOX_IP` 必须属于创建的网络；不能填写 NAS 已用 IP。数据目录要可写，证书目录默认只读挂载。

管理用户名固定为 `admin`。`XBOX_WEB_TOKEN` 留空时会生成持久化密码；也可填自己的密码。不要把包含密码的 `.env` 提交到仓库。默认管理 HTTPS 关闭，空证书目录可以正常启动。

初次使用保留下载代理关闭和其他运行参数的默认值；它们在首启后由 `/data/config.yaml` 管理，而不是由 `.env` 控制。

## 5. 拉取镜像和启动

```bash
cd /volume1/docker/xbox-speedup
sudo docker compose config --quiet
sudo docker compose pull
sudo docker compose up -d
sudo docker compose ps
sudo docker logs --tail 100 xbox-speedup
```

Compose 自动拉取匹配 NAS 架构的镜像。预构建镜像地址是 `ghcr.io/rosemengzi/xbox-speedup:latest`。若报 `denied`，先核对镜像名称和镜像包可见性，按 [镜像说明](container-image.md) 处理。

日志应显示容器对外 IP、DNS 监听、Web 地址、管理密码文件路径。密码本身不会打印在启动日志。失败时优先看「加载配置」「DNS 启动」「Web 启动」等错误。

检查容器内部就绪状态：

```bash
sudo docker exec xbox-speedup wget -qO- http://127.0.0.1:8080/healthz
sudo docker inspect --format '{{.State.Health.Status}}' xbox-speedup
```

启动后的健康状态可能先显示 `starting`；正常应变成 `healthy`，接口返回 `ok`。如果你修改了管理 HTTP 地址/端口，内部检查 URL 和 `.env` 的 `XBOX_HEALTH_URL` 要一起调整。

### 使用群晖项目界面

已有 Container Manager 项目功能时，可以在「项目」中新建项目，选择上面的部署目录及 `docker-compose.yml`，按界面提示启动。外部 macvlan 网络仍先在 SSH 中创建，`.env` 放在同一部署目录。

界面名称可能因 DSM 版本不同而变化；配置依据始终是本仓库 Compose 文件，不需要另行手填宿主机端口映射或开启本地镜像构建。

## 6. 首次登录和测速

从另一台电脑/手机打开：

```text
http://192.168.1.250:8080
```

如果 `XBOX_WEB_TOKEN` 留空，SSH 查看密码：

```bash
sudo cat /volume1/docker/xbox-speedup-data/web-token
```

用 `admin` 登录，完成以下操作：

1. 等待默认启动测速结束；查看平台、当前节点和 `SPEEDTEST` 日志。
2. 需要最新列表时点击「同步 IP」，等待 `SYNC` 完成，再点击「全量测速」。
3. 保持所需 Xbox 平台启用。PS、Switch 等默认关闭，用到时再启用并设置测速。
4. 打开「详情」检查成绩；没有有效成绩显示「上游解析」，不会使用未测速的第一个候选。
5. 暂时保持「302 重写层」关闭，先验收 DNS 模式。

主页面各按钮、锁定 IP、隐藏与周期设置的具体区别见 [使用说明](user-guide.md)。

## 7. 设置 Xbox DNS

在 Xbox 的网络高级设置中找到 DNS 设置，选择手动，把主 DNS 填为容器 IP，本例为 `192.168.1.250`。备用 DNS 按设备允许留空；若必须填写，优先使用同一个服务地址，避免填入可能绕过本服务的公网备用 DNS。

修改后检查联网，再进行同一游戏下载对比。如果仍使用旧 IP，等待缓存更新，暂停后重新开始下载或按主机网络操作重新建立连接。

无需先关闭整个家庭网络的 IPv6。服务默认在存在有效 IPv4 节点时过滤匹配域名的 AAAA；若实测主机仍经其他 DNS/代理路径使用 IPv6，再按实际网络策略排查。

停止使用时把主机 DNS 改回自动获取。要停容器，也先恢复主机 DNS。

## 8. 验证 DNS 和真实效果

在另一台电脑上使用实际容器 IP：

```bash
nslookup assets1.xboxlive.cn 192.168.1.250
nslookup assets1.xboxlive.com 192.168.1.250
nslookup example.com 192.168.1.250
```

纯 DNS 模式下，已测速域名应返回对应池的有效节点或锁定 IP。无成绩时是上游结果。普通域名应正常转发。还可在浏览器访问 `http://192.168.1.250:8080/healthz`。

DNS-A 命中或服务 healthy 只能说明程序对应路径工作，不能证明游戏提速。按 [实机验收步骤](user-guide.md) 比较同一游戏下载、持续速度、暂停/恢复与正常联网；尽量让 NAS 与 Xbox 使用相同的出口和分流策略。

## 9. 可选：启用 HTTP 换源及 HTTPS 透传

在管理页面打开「302 重写层」的「启用」，同时保留「智能兜底」。这会自动启动下载 HTTP `:80` 和 TLS `:443`；两者均就绪后 DNS 才将有效源规则指向容器。

下载 HTTPS 透传不需要管理证书。若已经给管理页面使用 `:443`，先改成 `:8443` 并重启再开启代理，避免端口冲突。

源平台和规则必须启用，正反向规则不能同时形成循环。详细行为及数据是否经过 NAS，见 [使用说明](user-guide.md) 与 [换源原理](redirect.md)。

## 10. 可选：启用管理 HTTPS

把覆盖访问域名的有效证书放到 `XBOX_CERT_DIR` 对应目录，文件名为 `fullchain.pem`、`privkey.pem`。它们只用于管理页面，与下载 TLS 分开。

编辑已有数据目录的 `config.yaml` 时，先停止并备份：

```bash
sudo docker stop xbox-speedup
sudo cp /volume1/docker/xbox-speedup-data/config.yaml /volume1/docker/xbox-speedup-data/config.yaml.before-https
sudo vi /volume1/docker/xbox-speedup-data/config.yaml
```

在原配置中修改：

```yaml
web_tls:
  enabled: true
  addr: ":8443"
  cert_file: "/certs/fullchain.pem"
  key_file: "/certs/privkey.pem"
```

然后启动：

```bash
sudo docker start xbox-speedup
sudo docker logs --tail 50 xbox-speedup
```

从电脑访问 `https://<证书覆盖的域名>:8443`。该域名在电脑实际使用的 DNS 或 hosts 中要解析到容器 IP；证书的域名不匹配时浏览器会报错。健康检查仍可使用内部 HTTP `8080`。

证书更新后需重启容器使其重新加载。管理 HTTPS 端口变化也需重启；修改挂载目录本身则需 Compose 重建。

## 11. 数据、更新与排查入口

首次启动后，数据目录大致如下：

```text
xbox-speedup-data/
├── config.yaml
├── platforms.yaml
├── web-token                 仅使用自动文件密码时生成
└── ip/
    ├── IP.Akamai.txt
    ├── IP.XboxCn1.txt
    ├── IP.XboxCn2.txt
    ├── IP.XboxApp.txt
    └── IP.Ps.txt
```

种子目录可能还有其他上游快照文件，只有平台表定义的池会加载/同步。当前测速成绩、自动推荐和 Web 实时日志不保存在这些文件中。

更新、固定版本、回退、备份和密码重建见 [维护说明](container-image.md)。诊断表见 [使用说明](user-guide.md)，高级设置见 [配置参考](configuration.md)。

常见首启问题：

| 现象 | 处理方向 |
| --- | --- |
| network not found | 先创建与 `XBOX_NETWORK` 同名的外部网络 |
| address already in use / IP 冲突 | 核对容器 IP、容器内监听、管理/下载 TLS 端口 |
| no matching manifest | 检查 NAS 架构，镜像仅含 amd64/arm64 |
| permission denied / 无法写配置 | 检查持久化目录存在且可写，证书目录不要当成数据目录 |
| NAS 访问失败但电脑访问正常 | macvlan 宿主机隔离，按电脑/手机结果判断 |
| 所有设备均访问失败 | 检查 parent、网关、VLAN、容器 IP 与 NAS/网络防火墙 |
| healthy 但无有效节点 | healthy 只看监听；检查测速/同步日志及出口网络 |
| 修改 `.env` 没生效 | 需要 Compose 重建容器，普通 restart 不重新读取 `.env` |
