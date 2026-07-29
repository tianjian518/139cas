# 139CAS

> OpenList 二开版，主打**光鸭云盘（GuangYaPan）挂载 + CAS 轻量占位**，面向 Emby / STRM 播放场景。

139CAS 在 OpenList 的基础上：

- 新增 **GuangYaPan（光鸭云盘）** 存储驱动，可挂载光鸭并向 Emby 等播放器提供可播放 / 下载链接。
- 合入 **CAS 轻量占位文件**能力，支持 **115 Cloud / 139Yun / 189CloudPC**，用 `.cas` 占位节省空间，播放时临时恢复真实文件，兼容 STRM / Emby 流程。
- 提供**成熟的 Docker 镜像**，支持多架构（amd64 / arm64 / arm / 386 / ppc64le / riscv64 / loong64），一键部署。

---

## 功能

- 新增 `GuangYaPan` 驱动并注册到 OpenList。
- 支持目录列表（修复光鸭目录被识别成文件的问题）。
- 支持获取文件播放 / 下载链接，可用于 Emby 拉取播放地址。
- 支持新建 / 删除 / 移动 / 复制 / 重命名。
- 支持上传文件：MD5 计算 → 光鸭秒传检查 → OSS 临时凭证 → Aliyun OSS SDK 上传。
- **CAS 轻量占位**：
  - `115 Cloud`：SHA1 / PreID 型 `.cas`。
  - `139Yun`：`personal_new` 类型，SHA256 型 `.cas`。
  - `189CloudPC`：MD5 / SliceMD5 型 `.cas`。
  - 上传真实文件后可生成同名 `.cas` 并按配置删除源文件；访问 `.cas` 时临时恢复真实文件，获取播放链接后清理临时目录。
  - 支持通过 `/d/*.cas` 或 `/p/*.cas` 进入真实文件预览 / Range 播放流程。

---

## Docker 部署（推荐）

镜像发布在 Docker Hub：

```bash
docker pull tianjian518/139cas:latest
```

### docker run

```bash
docker run -d \
  --name 139cas \
  -p 5244:5244 \
  -p 5245:5245 \
  -e OPENLIST_HTTP_PORT=5244 \
  -e TZ=Asia/Shanghai \
  -v /etc/139cas:/opt/openlist/data \
  --restart unless-stopped \
  tianjian518/139cas:latest
```

### docker-compose

仓库自带 `docker-compose.yml`，直接：

```bash
docker compose up -d
```

### 端口说明

- `5244`：HTTP 管理端口（Web 界面 / API）。
- `5245`：HTTPS 端口（如需）。
- 单端口环境（如某些面板只给一个 HTTP 端口）：设置 `OPENLIST_HTTP_PORT` 即可，容器会自动适配；也可设 `OPENLIST_HTTPS_PORT=-1` 关闭 HTTPS。
- 容器 entrypoint 兼容 `SERVER_PORT` / `PORT` 环境变量，会自动映射为 `OPENLIST_HTTP_PORT`。

### 获取管理员密码

```bash
docker exec -it 139cas ./openlist admin
```

---

## 从源码构建镜像

```bash
git clone https://github.com/tianjian518/139cas.git
cd 139cas
docker build -t 139cas:local .
docker run -d --name 139cas -p 5244:5244 -v /etc/139cas:/opt/openlist/data 139cas:local
```

> 构建时 `build.sh` 会自动拉取 OpenList 前端 `public/dist` 内嵌进二进制，因此不会出现 "index.html not exist" 的问题。

---

## 驱动配置（GuangYaPan）

在 OpenList 管理后台添加存储，驱动选择：

```text
GuangYaPan
```

需填写字段：

```text
access_token
refresh_token
client_id
device_id
page_size
order_by
sort_type
```

`client_id` 默认值：

```text
aMe-8VSlkrbQXpUR
```

`root_folder_id` 可留空，表示光鸭根目录。

---

## CAS 配置

### 115 Cloud

115 CAS 使用 SHA1 / PreID 秒传恢复，可用字段：`generate_cas`、`delete_source`、`restore_source_from_cas`、`cas_ext_allowlist`、`cas_download_restore`。

- 开启 `generate_cas`：上传真实文件会在同目录生成同名 `.cas`。
- 同时开启 `delete_source`：生成 `.cas` 成功后删除源文件，只保留占位 `.cas`。
- 开启 `restore_source_from_cas`：上传 `.cas` 时尝试通过 115 秒传恢复真实文件。
- 开启 `cas_download_restore`：通过 `/d/*.cas` 或 `/p/*.cas` 访问时临时恢复真实文件获取播放链接，外部 STRM 指向 `.cas` URL 时 Emby 走此流程。

> 注意：115 的 `.cas` 当前只保存 SHA1 和 PreID。若秒传接口要求额外区间 SHA1 校验，恢复会失败并返回明确错误。

### 139Yun

适用于驱动类型 `personal_new`。可用字段同上。139 依赖 SHA256 秒传恢复——云端只有 MD5、没有 SHA256 的文件不能生成可恢复的 139 `.cas`。

### 189CloudPC

189CloudPC CAS 使用 MD5 / SliceMD5 秒传恢复。额外字段：`delete_cas_after_restore`、`auto_restore_existing_cas`、`auto_restore_existing_cas_paths`。

`cas_ext_allowlist` 留空表示允许所有扩展名；Emby 目录建议只放媒体扩展名，例如：

```text
mp4,mkv,avi,mov,ts,m2ts
```

---

## 注意事项

- 本项目是 OpenList 的二开测试源码，**非官方 OpenList 发布版**。
- CAS 会调用网盘秒传 / 恢复 / 删除接口；开启 `delete_source` 前请先用测试目录验证。
- 操作真实网盘文件前，建议先用测试目录和小文件验证移动、复制、重命名、删除、上传。
- GuangYaPan 接口字段可能会变化，如遇到写操作失败，优先检查 `drivers/guangyapan/api.go` 与 `drivers/guangyapan/upload.go`。

---

## 上游与致谢

- 基于 [OpenList](https://github.com/OpenListTeam/OpenList) 二开。
- GuangYaPan 驱动与 CAS 能力参考 [xmm2022/openlist-guangyapan-src](https://github.com/xmm2022/openlist-guangyapan-src)。
- 单端口 / 面板部署适配参考 [tianjian518/openlist-guangyapan-wispbyte](https://github.com/tianjian518/openlist-guangyapan-wispbyte)。
