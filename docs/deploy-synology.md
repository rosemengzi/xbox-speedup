# 群晖 Docker 部署指南

目标：容器拿到一个**独立的局域网 IP**，把 Xbox 的 DNS 指向它即生效。

## 前置：查三样东西

SSH 登录群晖（控制面板 → 终端机和 SNMP → 启用 SSH），执行：

```bash
ip -o link        # 看物理网卡名：常见 eth0；开了 Open vSwitch 是 ovs_eth0；做了链路聚合是 bond0
ip -o -4 addr     # 看 NAS 当前 IP 和网段
```

确定三样：
1. **网卡名**（如 `eth0` / `ovs_eth0`）
2. **网段与网关**（如 `192.168.1.0/24`，网关 `192.168.1.1`）
3. **给容器选一个空闲 IP**（如 `192.168.1.250`，必须在路由器 DHCP 分配范围之外，避免撞号）

## 步骤一：把项目拷到 NAS

用 File Station 或 scp，把整个项目目录放到例如 `/volume1/docker/xbox-speedup`。

```bash
scp -r ./xbox-speedup admin@<NAS_IP>:/volume1/docker/xbox-speedup
```

## 步骤二：建 macvlan 网络（SSH，一次性）

```bash
sudo docker network create -d macvlan \
  --subnet=192.168.1.0/24 \
  --gateway=192.168.1.1 \
  -o parent=eth0 \
  xbox_macvlan
```

按你前置查到的值替换 `subnet` / `gateway` / `parent`。

## 步骤三：创建部署配置

复制环境变量示例：

```bash
cd /volume1/docker/xbox-speedup
cp .env.example .env
```

编辑 `.env`：

```dotenv
XBOX_IP=192.168.1.250
XBOX_DATA_DIR=/volume1/docker/xbox
XBOX_CERT_DIR=/path/to/certificate
XBOX_NETWORK=xbox_macvlan
```

`XBOX_IP` 必须与创建 macvlan 时使用的网段一致。`XBOX_CERT_DIR` 中未提供证书也不影响默认的
HTTP 管理界面，因为 HTTPS 默认关闭。

## 步骤四：构建并启动

```bash
cd /volume1/docker/xbox-speedup
sudo docker compose up -d --build
```

首次构建会拉 Go 镜像编译，几分钟。完成后：

```bash
sudo docker logs -f xbox-speedup    # 应看到 “DNS 监听 :53 / Web 界面 …”
```

> DSM 7.2 也可用 Container Manager 图形界面：项目 → 新增 → 选这个目录的 docker-compose.yml。
> 但 macvlan 网络仍建议用上面的 SSH 命令先建好。

## 步骤五：设置 Xbox

1. 浏览器（**用电脑或手机，不要用 NAS 本机**，原因见下方注意）打开 `http://<容器IP>:8080`，点「立即全量测速」，等各平台选出最快 IP。
2. Xbox：设置 → 网络 → 高级设置 → DNS 设置 → 手动 → **主 DNS = 容器 IP**，辅 DNS 留空。
3. 路由器里**关闭 IPv6**（否则 Xbox 可能绕过加速走 IPv6）。
4. 开始下载游戏。下完记得把 Xbox DNS 改回自动获取。

## 验证

从电脑（非 NAS 本机）：

```bash
nslookup assets1.xboxlive.cn <容器IP>     # 应返回测速选出的国内快 IP，而非真实解析
```

Web 界面的「实时连接日志」里，Xbox 下载时会滚出 `DNS-A` 记录（命中的下载域名 + 返回 IP）。

## 注意事项 / 常见问题

- **NAS 本机访问不了容器 IP**：macvlan 的固有限制——同一网卡上宿主机和容器无法直接通信。所以
  Web 界面要用**另一台设备**（电脑/手机）打开。Xbox 是独立设备，不受影响。
- **53 端口冲突？** 不会。容器有独立 IP，53/80/8080 都绑在容器自己的 IP 上，和群晖 DSM（即便装了
  DNS Server 套件）互不干扰。
- **302 重写层默认关**。需要时在 Web 界面打开「重写层」开关，**然后重启容器**（`docker compose restart`）让它占用 80 端口。日常纯 DNS 就够。
- **ICMP 测速**：compose 已加 `NET_RAW`。若你的环境不允许，测速会自动退化为 TCP 连接延迟，仍能选最快。
- **IP 列表/配置持久化**在 `XBOX_DATA_DIR` 指定的目录中（容器内为 `/data`）。删除容器不会删除该目录。
- **关闭加速**：把 Xbox DNS 改回自动获取即可；容器可继续留着。

## 可选：启用 HTTPS

证书目录需要包含 `fullchain.pem` 和 `privkey.pem`，并通过 `.env` 的 `XBOX_CERT_DIR`
只读挂载到容器。然后编辑 `XBOX_DATA_DIR/config.yaml`：

```yaml
web_tls:
  enabled: true
  addr: ":443"
  cert_file: "/certs/fullchain.pem"
  key_file: "/certs/privkey.pem"
```

重启容器后，可通过 `https://<证书覆盖的域名>` 访问。域名需要在局域网 DNS 或路由器 Host
记录中解析到 `XBOX_IP`。证书续期后需重启容器，使服务重新加载证书。
