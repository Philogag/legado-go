# 实例生命周期与 TTL（ttl.md）

书源实例是**有状态**的（Cookie、`source` 变量、JS 上下文、HTTP 缓存），
所以微服务用 TTL 注册表来管理它们：随用随建、闲置自毁、删除留墓碑。

代码：`internal/registry/registry.go`（384 行）+ `internal/registry/registry_test.go`。

## 状态机

```
                Create / CreateWithID
        ┌──────────────────────────────┐
        ▼                              │
   ┌─────────┐   idle > TTL    ┌──────────────┐   grace 到期   ┌────────┐
   │  ALIVE  │ ──────────────► │   TOMBSTONE  │ ─────────────► │  GONE  │
   └─────────┘                 └──────────────┘                └────────┘
        │  Touch / 任意调用            │ CreateWithID(同 ID)
        └──────────◄──────────────────┘  （idle 时也可直接重建）
        │
        │ Delete
        ▼
   ┌─────────┐
   │ REMOVED │（墓碑期内 Get → EXPIRED，墓碑过期后 Get → NOT_FOUND）
   └─────────┘
```

| 状态 | 内存 | `Get` 行为 | 说明 |
| --- | --- | --- | --- |
| ALIVE | 持有 `*booksource.Source` | 返回实例并 **刷新 lastUsed** | 正常服务 |
| TOMBSTONE | 只留 `Info`（name/url/owner/…） | `EXPIRED`（410） | 被 TTL 或 delete 回收，等 `GRACE_PERIOD` |
| GONE | 无 | `NOT_FOUND`（404） | 墓碑清理完毕 |
| CLOSED（实例级） | — | `CLOSED`（410） | 实例被 `Close()`，注册表也视为失效 |

墓碑的意义：调用方拿到的是 `410 EXPIRED` 而不是 `404`，
可以据此判断"**实例曾存在，是我的 ID 过期了**"（404 则说明 ID 根本不对）。

## 两条过期规则

| 规则 | 字段 | 默认 | 触发条件 |
| --- | --- | --- | --- |
| 滑动空闲 TTL | `Entry.TTL` | `DEFAULT_TTL`（30m） | `now - lastUsed > TTL` |
| 绝对寿命上限 | `registry.Options.MaxTTL` | `0`＝不限 | `now - Entry.Created > MaxTTL` |

```go
reg := registry.New(registry.Options{
    DefaultTTL: 10 * time.Minute,
    MaxTTL:     2 * time.Hour,   // ① 任何 TTL 都被压到 ≤2h；② 实例活满 2h 必被回收
})
e, _ := reg.Create(cfg, time.Hour, "svc-a")      // 1h 空闲
e.TTL = 0                                        // 只读视图，改它无效
```

`MaxTTL` 是**注册表级**配置（对应 `-max-ttl` / `MAX_TTL`），有双重作用：

1. **封顶**：`Create`/`Touch` 传入的 TTL 若超过 `MaxTTL` 会被压到 `MaxTTL`；
2. **绝对寿命**：即使一直有调用续期，`now - Created > MaxTTL` 时也会被回收。

用于"这个实例最多活 2 小时，之后必须重建"的场景。没有 per-request 的寿命字段。

## 续期语义

以下**都会**刷新 `lastUsed`（滑动窗口）：

- `Get(id)` / `GetSource(id)`
- 所有书源调用：`Search`、`Explore`、`GetBookInfo`、`GetChapterList`、`GetContent`、`EvalJS`
- `POST /v1/instances/{id}/touch`、MCP `source_touch`

以下**不**续期（只读）：

- `GET /v1/instances`（列表）
- `GET /v1/instances/{id}`（单个）
- MCP `source_list` / `source_get`

`Touch(id, d)` 改 TTL 并续期；`d <= 0` 时回落到 `DefaultTTL`（同样受 `MaxTTL` 封顶）。

## 回收怎么发生

1. **后台扫描**：`SweepInterval`（默认 1m）的 ticker 调 `Sweep()`。
2. **手动扫描**：`reg.Sweep()`，返回本次回收数量；测试与运维脚本常用。
3. **被动发现**：`Get` 时若已过期，就地转墓碑并返回 `EXPIRED`（不依赖 ticker 是否及时）。

`Sweep()` 一次做两件事：

- 把所有 `now-lastUsed > TTL`（或超过 `MaxTTL`）的 ALIVE 实例降级为墓碑，并 `Close()` 释放资源；
- 把所有 `now-tombstoneAt > GracePeriod` 的墓碑删除。

`Close()` 幂等；`reg.Close()` 停止 ticker（可重复调用）。

## 并发与一致性

- 一把 `sync.RWMutex` 保护 entries/tombstones 两张表；`Create` 去重、`Sweep` 与 `Get` 互斥。
- `Entry.LastUsed`/`TTL` 是普通字段，`Get` 在读锁内更新 —— 高频访问下这是唯一可观测的锁竞争点。
- 每个实例内部的 HTTP 缓存/Cookie 有自己的锁（`internal/netx`），与注册表锁无关。
- **不做**分布式协调：注册表是进程内内存结构，**多副本部署时实例不共享**（见 [deployment.md](deployment.md#6-多副本与负载均衡)）。

## 统计

```go
type Stats struct {
	Instances int // 当前存活
	Sweeps    int // Sweep 执行次数
	Evicted   int // 被 TTL 回收总数
	Created   int // 创建总数
}
reg.Stats()
```

`GET /healthz`、`GET /v1/instances` 都会带出 `stats`，可直接接监控。

## 参数调优

| 场景 | 建议 |
| --- | --- |
| 高并发短任务（每次调用新建实例） | `DEFAULT_TTL=60s`、`MAX_INSTANCES=2000`、`SWEEP_INTERVAL=10s` |
| 少量长会话（Agent 多轮对话） | `DEFAULT_TTL=30m`、`GRACE_PERIOD=10m` |
| 内存紧张（每实例含 goja VM，约 1–3 MB） | 降低 `MAX_INSTANCES`，`DEFAULT_TTL=5m` |
| 需要"必须重建"保证（防状态漂移） | 调小 `-max-ttl`（如 `2h`），到点强制重建 |

内存占用参考：单个空闲实例约 1–3 MB（goja VM + 缓存），活跃抓取时峰值取决于页面大小。
`MAX_INSTANCES` 是硬上限，但**不限制内存**——它只限制实例数。

## 代码示例

```go
reg := registry.New(registry.Options{
	DefaultTTL:    10 * time.Minute,
	MaxInstances:  500,
	SweepInterval: 30 * time.Second,
	GracePeriod:   time.Minute,
	SourceOptions: booksource.Options{Timeout: 15 * time.Second},
})
defer reg.Close()

e, err := reg.CreateWithID("session-1", cfg, 0, "agent") // ttl=0 → DefaultTTL
if model.KindOf(err) == model.KindInvalidConfig { /* 配置错 */ }

src, err := reg.GetSource("session-1") // 刷新 lastUsed（等价 Get(id).Source）
if model.KindOf(err) == model.KindExpired {
	e, _ = reg.CreateWithID("session-1", cfg, 0, "agent") // 墓碑期可直接重建
}
_ = src
```

测试里怎么稳定验证过期：用短 TTL + 手动 `Sweep()`，不要 sleep 等 ticker
（`internal/registry/registry_test.go` 就是这么写的）。
