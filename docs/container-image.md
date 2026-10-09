# GitHub 容器镜像

项目镜像由 GitHub Actions 构建并发布到 GitHub Container Registry（GHCR）：

```text
ghcr.io/rosemengzi/xbox-speedup
```

Compose 文件不包含 `build` 字段，部署主机只负责拉取和运行镜像，不安装 Go 工具链，也不执行本地 Docker 构建。

## 构建触发条件

`.github/workflows/container.yml` 在以下事件运行：

- `main` 分支更新：构建并发布 `latest` 与提交 SHA 标签。
- 推送 `v*` Git 标签：构建并发布版本标签与提交 SHA 标签。
- 手动运行工作流：构建并发布提交 SHA 标签；默认分支运行时同时更新 `latest`。
- Pull Request：运行 Go 测试并验证镜像能够构建，不发布镜像。

每次发布同时生成 `linux/amd64` 和 `linux/arm64` 镜像，并附带构建来源证明与 SBOM。

## 镜像标签

- `latest`：`main` 分支最新成功构建。
- `v*`：对应的 Git 版本标签，例如 `v1.0.0`。
- `sha-<短提交号>`：对应源码提交，可用于固定版本和回退。

## 部署与更新

默认部署使用：

```yaml
image: ghcr.io/rosemengzi/xbox-speedup:latest
pull_policy: always
```

拉取并应用最新镜像：

```bash
docker compose pull
docker compose up -d
```

Docker 容器实际绑定不可变的镜像摘要。远程标签更新后，第二条命令会在镜像内容发生变化时重建容器，持久化数据目录不受影响。
