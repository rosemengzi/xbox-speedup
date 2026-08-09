# 进度与计划

更新日期：2026-08-09

## 已完成

- [x] 项目骨架、Go module、多阶段 Dockerfile、macvlan compose
- [x] 配置模块：加载/落盘/无锁热替换/变更回调（含 json+yaml 双标签）
- [x] 域名表 `data/platforms.yaml`（移植原项目 HostRules：Xbox/商店/PS/NS/EA/战网）
- [x] IP 池存储：候选 IP、测速记录、新鲜度、当前下载速度最快 IP 选择
- [x] IP 在线同步：上游 GitHub + 代理回退链 + 本地缓存 + 增量 diff
- [x] DNS 服务：加速/黑名单/劫持/AAAA 过滤/上游转发（**已本地验证**）
- [x] 测速引擎：ping 选 top-N → 下载比带宽 → 选最快（沿用原项目逻辑，ICMP/TCP 回退）
- [x] 80 端口 302 重写层：双向规则表 + 智能 404 探测 + 回源反代
- [x] Web 界面：状态、实时日志(SSE)、手动测速/同步、平台&规则开关（**API 已验证**）
- [x] 周期调度：测速与 IP 同步按 cron
- [x] 文档：README / DETAILS / docs/redirect
- [x] 2026-07-08：Web 管理界面支持独立 HTTPS 监听和只读证书挂载
- [x] 2026-07-27：公开发布前敏感信息检查、部署参数环境变量化、开源文档整理
- [x] 2026-08-06：改用 GitHub Actions 构建多架构镜像，并通过 GHCR 部署
- [x] 2026-08-09：修复过期 EWMA 分数覆盖当前下载速度、旧冠军长期占用最快 IP 的问题

## 已用真实网络验证

- [x] IP 同步：6 个列表全部从上游 GitHub（经代理）拉取成功
- [x] 测速全流程：XboxCn1/XboxCn2/Ps/Akamai 真实测出 RTT+带宽并选最快（ICMP 在本机退化为 TCP 延迟）
- [x] DNS 用上测速结果：加速域名解析到测速冠军 IP
- [x] 302 重写：Host=assets1.xboxlive.com + 智能探测 → 302 跳 assets1.xboxlive.cn
- [x] Web 界面：IP 池明细表（/api/pool）+ 测速进行中指示器
- [x] Web 界面：平台隐藏（隐藏即强制不启用，后端兜底）、日志按类型筛选、
      自动测速/自动同步周期下拉（热生效，调度器随配置重建）、记录全部 DNS 查询开关

## 待真实环境验证（需特权/实机，沙箱外）

- [ ] ICMP 测速（需 NET_RAW，本机无特权时走 TCP 回退）
- [ ] 群晖 macvlan 实机：独立 IP、Xbox 实测下载提速

## 决策记录

- 育碧：已移除。原项目当前版本已删除其域名映射，仅剩孤儿 IP 文件；其下载 CDN
  （static*.cdn.ubi.com）兼发 Connect 客户端资源，劫持有破坏客户端风险，故不接入。
- XboxApp：保持 ping-only 排序。带宽测速需依赖外部商店 API 解析包地址，收益不抵复杂度。

## 后续可选
- 配置项可视化编辑（上游 DNS、周期、端口）、日志导出、测速历史曲线
- 单元测试：IP 列表解析、DNS 命中判定、重写规则匹配
