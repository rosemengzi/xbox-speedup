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

## Fork 首次发布

代码仓库公开不会自动将容器镜像设为公开。GitHub 的个人账号容器包首次发布默认是 Private；详见 [GitHub 包可见性说明](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)。

构建成功后，在账号的 Packages 中打开 `xbox-speedup`，进入 Package settings。如果希望 NAS 无需登录即可拉取，选择 Change visibility → Public；GitHub 的此项变更不可撤回为 Private。保留私有时，NAS 需先通过 `docker login ghcr.io -u rosemengzi` 登录，密码使用具有 `read:packages` 权限的个人访问令牌，不能使用 GitHub 账号密码。

刚创建的 Fork 若未自动运行构建，可在 Actions → Build container image → Run workflow 选择 `main` 手动触发。

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
