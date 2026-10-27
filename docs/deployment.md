# 部署与运维（deployment.md）

编译产物是**单个静态链接二进制**（无 CGO、无运行时依赖），除服务端二进制外只需一个 JSON 书源文件。

## 1. 构建

```bash
cd projects/legado-go
make build                      # → ./bin/legado-server
# 或
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/legado-server ./cmd/server
```

交叉编译（静态、可直接丢进 scratch/alpine 镜像）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/legado-server-linux-amd64 ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/legado-server-linux-arm64 ./cmd/server
```

## 2. 启动

```bash
# 最简：监听 :8080，30 分钟空闲 TTL，最多 64 实例，无需鉴权
./bin/legado-server

# 生产建议：带令牌 + 限流
API_TOKEN=$(openssl rand -hex 24) \
DEFAULT_TTL=10m MAX_INSTANCES=512 SWEEP_INTERVAL=10s RATE_LIMIT=600 \
HTTP_TIMEOUT=15s QUIET=1 \
./bin/legado-server -addr 0.0.0.0:8080
```

命令行参数优先于环境变量（两者都能用）。全部参数见 [configuration.md#服务端参数](configuration.md#服务端参数命令行--环境变量)。

## 3. systemd

`/etc/systemd/system/legado-booksource.service`：

```ini
[Unit]
Description=legado book source microservice
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=legado
Group=legado
WorkingDirectory=/opt/legado-go
ExecStart=/opt/legado-go/bin/legado-server -addr 127.0.0.1:8080
EnvironmentFile=/etc/legado-go/env
Restart=on-failure
RestartSec=3
# 优雅停止：SIGTERM 后等待在途请求（服务端 10s 宽限）
KillSignal=SIGTERM
TimeoutStopSec=20
# 加固
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/legado-go
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

`/etc/legado-go/env`：

```ini
ADDR=127.0.0.1:8080
DEFAULT_TTL=10m
MAX_TTL=6h
MAX_INSTANCES=512
SWEEP_INTERVAL=10s
HTTP_TIMEOUT=15s
CACHE_TTL=30m
GRACE_PERIOD=5m
RATE_LIMIT=600
API_TOKEN=<随机24字节hex>
QUIET=1
```

```bash
sudo useradd -r -s /usr/sbin/nologin legado
sudo systemctl daemon-reload && sudo systemctl enable --now legado-booksource
journalctl -u legado-booksource -f
```

## 4. 容器

仓库已提供可直接使用的 [`Dockerfile`](../Dockerfile)、[`docker-compose.yml`](../docker-compose.yml)、[`.env.example`](../.env.example) 与 [`.github/workflows/docker-publish.yml`](../.github/workflows/docker-publish.yml)，下面的说明都是对这几个文件的解释。

### 4.1 Dockerfile 要点

| 项 | 做法 | 原因 |
| --- | --- | --- |
| 分阶段 | `golang:1.26-alpine` → `gcr.io/distroless/static-debian12:nonroot` | 运行镜像约 15 MB，无 shell/包管理器，攻击面最小 |
| 多架构 | `FROM --platform=$BUILDPLATFORM` + `TARGETOS/TARGETARCH` | 编译器跑在本机架构上**交叉编译**，避免把整个构建阶段扔进 qemu 模拟 |
| 静态链接 | `CGO_ENABLED=0` | 可跑在 distroless/scratch，无 glibc 依赖 |
| 层缓存 | 先 `COPY go.mod go.sum` 再 `go mod download`，并挂 `--mount=type=cache` | 改业务代码不重复拉依赖 |
| 非 root | `USER nonroot:nonroot`（uid 65532） | 容器逃逸后无特权 |
| 健康检查 | `HEALTHCHECK CMD ["/legado-server", "-healthcheck", "http://127.0.0.1:8080/readyz"]` | distroless 没有 shell，只能用 exec 形式；二进制内置该模式，非 2xx 退出码 1 |
| 版本信息 | `-ldflags "-X main.buildVersion/buildCommit/buildDate"` | `docker run --rm <img> -version` 可查构建来源 |

```bash
docker build -t legado-go:local .
docker run --rm legado-go:local -version      # legado-go dev (commit -, built -)
docker buildx build --platform linux/amd64,linux/arm64 -t legado-go:multi .
```

### 4.2 docker compose

```bash
cp .env.example .env          # 生产环境务必在 .env 里设置 API_TOKEN
# 方式一：本地构建并启动
docker compose up -d --build
# 方式二：直接拉已发布镜像（.env 里设 IMAGE=ghcr.io/philogag/legado-go:latest）
docker compose pull && docker compose up -d

docker compose ps             # STATUS 列应显示 (healthy)
curl -s localhost:8080/healthz; echo
curl -s localhost:8080/readyz;  echo
docker compose logs -f legado
docker compose down           # 加 -v 会一并删网络
```

compose 里的加固与默认值：

- `read_only: true` + `tmpfs: /tmp`：进程内无本地状态（实例、缓存、Cookie 全在内存），可只读运行
- `cap_drop: [ALL]`、`no-new-privileges:true`、非 root
- `stop_grace_period: 20s` 配合服务端 `SHUTDOWN_TIMEOUT=10s` 完成优雅停机
- 资源上限 `cpus: 2 / memory: 512M`（`deploy.resources.limits`），按 §10 容量规划调整
- 日志 `json-file` 轮转 10 MB × 3

### 4.3 容器里做端到端演示

`examples/book-source.json` 指向 `http://127.0.0.1:18081`（本机 fixture 假书站）。容器内的 `127.0.0.1` 是容器自己，**不是宿主机**，要二选一：

```bash
# 方案 A：让容器能解析宿主机（把书源 URL 改成 http://host.docker.internal:18081）
docker run --add-host=host.docker.internal:host-gateway -p 8080:8080 legado-go:local
# 方案 B：fixture 与微服务同网络
docker network create legado-demo
go run ./tools/fixtureserver -addr 0.0.0.0:18081   # 宿主机跑，书源 URL 用 http://<宿主机IP>:18081
```

### 4.4 CI/CD：多架构镜像发布到 ghcr.io

工作流 `.github/workflows/docker-publish.yml` 使用 marketplace 上的 [`push-to-ghcr`](https://github.com/marketplace/actions/push-to-ghcr)（`macbre/push-to-ghcr@v18`，内部会准备好 QEMU + buildx 并推送）。

触发与产出的镜像标签：

| 事件 | 标签 |
| --- | --- |
| `push` 到 `main` / `master` | `latest`、`sha-<7位>` |
| `push` tag `v1.2.3` | `1.2.3`、`latest` |
| `release` published `v1.2.3` | `1.2.3`（action 自动取 release tag 并去掉 `v`） |
| `pull_request` | 不推送：只 `docker build` 并起容器 curl `/healthz`、`/readyz`、`/mcp` 冒烟 |
| `workflow_dispatch` | 手动指定 `platforms` 与是否推送 |

关键配置：

- `permissions: packages: write` + `secrets.GITHUB_TOKEN`（无需额外 PAT）
- 默认平台 `linux/amd64,linux/arm64`（`env.PLATFORMS`，手动触发时可改）
- 镜像名 `ghcr.io/${{ github.repository }}`，可用仓库变量 `IMAGE_NAME` 覆盖（自动转小写）
- 版本号通过 `extra_args: --build-arg VERSION=` 注入，`-version` 可见
- `needs: test`：`go vet` + `go test -race ./...` 不过不发布
- 工作流会自动识别仓库布局：`Dockerfile` 在根目录或在 `projects/legado-go/` 下都能工作

首次发布后，把包可见性改为 public（默认继承仓库可见性），或在使用方 `docker login ghcr.io -u <user> -p <PAT>`：

```bash
docker pull ghcr.io/philogag/legado-go:latest
docker run --rm -p 8080:8080 -e API_TOKEN=$(openssl rand -hex 16) ghcr.io/philogag/legado-go:latest
```

## 5. 反向代理（nginx）

```nginx
upstream legado { server 127.0.0.1:8080; keepalive 32; }

server {
    listen 443 ssl http2;
    server_name books.example.com;
    client_max_body_size 8m;          # 书源 JSON 可能较大（MCP 上限 4MiB）

    location / {
        proxy_pass http://legado;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header X-Forwarded-For $remote_addr;   # 限流按此取值
        proxy_set_header Host $host;
        proxy_read_timeout 120s;      # 长章节多页合并可能 >60s
        proxy_buffering off;          # MCP 为纯 JSON，可关
    }
}
```

要点：

- **必须**透传客户端 IP（`X-Forwarded-For`），否则 `RATE_LIMIT` 会把所有请求算成同一个 IP。
- 建议保留 `Connection ""` + keepalive，减少 TLS 之外的握手开销。
- 长正文（`nextContentUrl` 多页）可能超过 60s，`proxy_read_timeout` 放宽到 120s。
- 若启用鉴权，`Authorization` 头默认会被转发，无需额外配置。

## 6. 多副本与负载均衡

注册表是**进程内内存**结构，两个副本之间不共享实例。因此：

| 方案 | 做法 | 代价 |
| --- | --- | --- |
| 单副本（推荐起步） | 一个进程，`MAX_INSTANCES` 按内存调大 | 单点，靠 systemd/k8s 重启 |
| 会话粘性 | LB 按 `X-Instance-Id` 或客户端 IP 做一致性哈希 | 副本故障会丢实例（可接受：重建即可） |
| 无粘性 | 客户端失败重试：`404/410` → 重新 `POST /v1/instances` | 需要客户端支持"重建"语义（推荐，最健壮） |

**不要**把"实例"当成持久数据——它本来就是短生命周期缓存。跨副本真正需要一致的只有书源配置本身（放配置中心或卷）。

## 7. 可观测性

| 指标 | 来源 |
| --- | --- |
| 存活/墓碑/回收计数 | `GET /healthz` → `stats` |
| 实例列表与剩余 TTL | `GET /v1/instances` → `instances[].remainingSeconds` |
| 单请求耗时 | 响应体 `elapsedMs`（search/bookinfo/chapters/content） |
| 回收事件日志 | `OnEvict` 回调 → `evicted instance <id> (<name>) after <ttl> idle` |
| JS 日志 | `VERBOSE_JS=1` → `[js] ...`（书源里的 `console.log`） |

对接 Prometheus 的最小做法：外部探针抓 `/healthz`，用 `json_exporter` 转成指标；
或按 `Stats` 结构自行加 `/metrics`（`internal/httpapi` 加一个 handler 即可）。

日志为单行文本，字段顺序固定：`[server] <时间> <消息>`，便于 `grep 'evicted instance'`。

## 8. 优雅停机

`cmd/server/main.go` 监听 `SIGINT/SIGTERM`，调用 `srv.Shutdown(ctx)` 后 `reg.Close()`：

1. 停止接受新连接（在途请求继续）；
2. 宽限 10s（可改 `-shutdown-timeout`），超时强制关闭；
3. `reg.Close()` 停止 sweep ticker 并 `Close()` 所有实例（释放 goja VM 与连接池）。

k8s 请把 `terminationGracePeriodSeconds` 设得比宽限时间长（例如 30s）。

## 9. 备份与恢复

本服务**无持久状态**，不需要备份数据库。需要备份的是：

- 书源 JSON 配置（放 git 或配置中心）；
- 若书源依赖登录，`header`/`loginHeader` 里的 Cookie（会过期，建议放 secret manager）。

重启 = 所有实例消失，客户端按 `404` 重建即可。

## 10. 容量规划（实测参考）

| 项目 | 数值 |
| --- | --- |
| 空载内存 | ~15 MB |
| 每个存活实例 | ~1–3 MB（goja VM + 缓存） |
| 冷启动到可服务 | < 100 ms |
| 单实例搜索（本地假书站） | 5–30 ms |
| 单实例搜索（真实站点） | 300–3000 ms（取决于站点） |
| 并发瓶颈 | 目标站点限流 > 本服务（Go 侧无锁竞争热点） |

粗略公式：`内存 ≈ 15MB + 活跃实例数 × 2MB`。
500 个活跃实例约 1 GB —— 用 `MAX_INSTANCES=512` 给个硬上限即可。

## 11. 安全清单

- [x] `API_TOKEN` 非空即启用 Bearer 鉴权（`/api/v1` 与 `/mcp`）
- [x] `RATE_LIMIT` 按 IP 限流
- [x] `MAX_INSTANCES` 防实例爆炸
- [x] `HTTP_TIMEOUT` 限制单次抓取，`nextContentUrl`/`nextTocUrl` 最多 20 页防拖死
- [x] 请求体上限 4 MiB（MCP），REST 侧 echo 默认 4 MiB body limit
- [x] JS 沙箱无文件/无直连网络，只能通过 `java.ajax` 出网
- [ ] 建议：把服务放在内网，只暴露 `/v1` 给可信调用方（书源 JS 由第三方编写，等于执行不可信代码）
- [ ] 建议：`API_TOKEN` 走 secret 注入，不要写进 compose 文件
