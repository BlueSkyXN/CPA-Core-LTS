> 本文描述历史 version<=2。V3 采用单一 Auth 账号、模型复选和首次联合许可；新配置请读 [V3 指南](flow-control-v3.md)。实时观察在 V3 二进制中默认关闭，旧客户端应适配 schema3 摘要与手动详情接口。

> 本次产品基于 Core `66464451` / Panel `1508583` 发布快照，仅新增累计 Flow；不依赖独立 Codex Identity 批次。集合与联合申请规则以 [V3 指南](flow-control-v3.md) 为准，V1/V2 配置兼容仅为读取旧策略，不要求先应用旧补丁。

# CPA 本地多维流控：算法与调度设计

> 当前补充设计见 [flow-control-v2.md](flow-control-v2.md)：新增单凭据、自定义组合、逐请求状态和 SSE。本页继续说明 V1 保留的基础计数。

## 结论与适用范围

实现为 **单进程、默认关闭、两层计数、同层所有规则取交集**。在现有 Manager 和 ProviderExecutor 之间增加名额管理，不替换账号选择、模型转换、OAuth、UA、额度冷却或 Home 协议。需要用当前项目工具链完成行为测试后再部署。

```text
客户端模型调用
  │
  ├─ request 层：全局／Key／请求模型／Key×请求模型
  │      └─ 原子检查该层全部并发和频率窗口
  │             ├─ 放行 → 持有一次 Manager 操作的名额
  │             └─ 等待／本地429
  │
  ├─ 原有账号选择、亲和、模型别名解析
  │      └─ 可迁移的请求可以跳过当前明显满额账号（最多试64个候选）
  │
  ├─ attempt 层：全局／Key／实际模型／Key×实际模型／Provider／账号／账号×模型
  │      └─ 原子申请全部命中资源 → 复查账号状态 → 原有执行前处理
  │
  └─ ProviderExecutor
         ├─ 非流式：函数返回时释放执行名额
         └─ 流式：生产 channel 结束后释放，不在首块输出时释放
                  逻辑结果流结束后释放 request 层名额
```

## 1. 两种计数，不声称覆盖每一个网络包

- `request`：一次 `Manager.Execute`、`ExecuteCount` 或 `ExecuteStream` 操作，包含它内部的重试和等待账号时间。HTTP Handler 重新调用 Manager 是一次新操作，不是一张跨整个 HTTP 重试过程永久有效的票据。
- `attempt`：Manager 发起的一次实际 ProviderExecutor 调用。Manager 顺序重试、401 刷新后的再次调用、并行尝试分别计数。Executor 内部顺序 HTTP 重试、WS 重连仍属同一次调用；这里不是每个 wire request 的精确 RPM 统计。
- 空闲 WS 不占推理名额，但本功能不另限空闲连接数。异步任务若只提交即返回，计数到该次调用结束，不代表任务后台全部生命周期。
- 第一次启用无法回溯已在禁用状态开始的调用。启用前应停止新流量并等待旧请求自然结束，或者在受控重启后启用。

## 2. 规则选择与分组

规则的 `key/model/provider/account` 是流量筛选条件；`scope` 才决定哪些请求共享同一个计数器。所有匹配规则都生效，不存在“更具体规则覆盖全局规则”。

| scope | 独立计数对象 | stage |
|---|---|---|
| global | 此规则匹配的流量共同计数 | request、attempt |
| key | 每个下游 Key | 两层 |
| model | 每个归一化模型名 | 两层 |
| key-model | 每个 Key×模型二元组 | 两层 |
| provider | 每个上游 Provider | attempt |
| account | 每个上游账号引用 | attempt |
| account-model | 每个账号×模型二元组 | attempt |

例如 `scope: key-model, model: gpt-*` 是每个 Key、每个 GPT 模型分别计数；要限制一个 Key 的所有 GPT 模型合计，则用 `scope: key, model: gpt-*`。要所有 GPT 模型合计，则用 `scope: global, model: gpt-*`。

模型筛选只支持精确名称和末尾一个 `*`，不采用任意子串。模型、Provider 比对忽略大小写；模型用于计数时去掉既有 reasoning 后缀。请求层保留原请求模型/别名的含义，执行层使用实际解析后的 `req.Model`。原请求、实际发送模型名本身不因计数而被改写。

Key 取既有认证流程产生的 `CallerScope`（固定 SHA-256 引用）；没有时归入统一 anonymous，不按 IP 猜测、不自动放行。SDK 调用方应传内部 CallerScopeMetadataKey；HTTP 客户端不能靠自报 Header 选择这个内部引用。

账号引用来源顺序：显式 `flow_control_group` → Provider+account_id → Provider+上游 API Key → Provider+Auth.ID，之后转为固定 SHA-256 引用。识别到同一上游账号的多个认证文件共享限制，Token 刷新不改变分组；缺少账号信息且重新生成 Auth.ID 时无法自动证明是同一账号，可显式分组。下游 Key 更换会产生新引用。

## 3. 同时检查，再一次性扣数

设请求 x 命中规则集合 R。对于每条规则 r：

```text
并发允许：max_concurrent(r)=0 或 active(bucket(r,x)) < max_concurrent(r)
速率允许：对每个窗口 w，admissions(bucket(r,x), (now-period(w), now]) < requests(w)
最终允许：所有命中规则的并发条件和所有时间窗条件均成立
```

检查与提交在 Engine 的同一个互斥锁内完成；不会“先占全局，再占 Key，最后等待账号”造成同层部分占用。各窗口共用规则桶中的时间序列，按最长所需窗口保留记录；记录提交前不计已用次数。已放行后失败不退还速率记录，尚未真正调用 Executor 的取消/账号变化可按该票据撤回；不会撤回其他并发请求的记录。

并发0表示不设该规则的并发上限；每条有效规则至少有并发上限或一个时间窗。多窗口是真正的滑动计数，不是固定分钟清零或平均令牌补充。多窗口上限可同时表达每秒、每分钟、每小时等次数。

两层之间不是一个横跨全流程的大事务：request 名额表示逻辑操作，等待 attempt 时仍占 request 名额。排队者不占正在等待那一层的执行名额。这个区别必须出现在界面与解释中。

## 4. 有界等待与公平性

- 总等待数、每个 Key 等待数、注册等待 payload 字节总量、最大等待时长均有限制；等待时长取自身配置与父 context 截止时间的较早者。
- 字节数是排队登记的 `len(req.Payload)` 之和，不等于进程 RSS，不覆盖此前已读取的 HTTP Body、翻译副本和连接内存。现有请求体大小上限保持原样。
- 每次唤醒扫描可执行等待者：优先最近最少获得放行的 Key，再选该 Key 最早可执行的请求。可以跳过被特定模型/账号阻塞的队头，避免其他空闲资源也停住。
- 不为每个等待者循环轮询。释放名额、配置更新即时触发调度；一个可停止的定时器处理最早窗口到期或等待截止。
- 客户端取消立即退出队列；超时、队满返回本地429和 Retry-After。并发结束时刻未知，Retry-After 是重试建议，不保证那个时刻一定有空位。
- 不提前发送HTTP200或SSE成功头来伪装排队进度。

账号选择只做有限改良：在没有固定账号、必须保留的 WS、execution session、previous_response_id 或插件调度要求时，最多跳过64个明显无账号名额的候选。模型池解析会推进偏移量，不能为“查看是否可用”重复执行它；因此候选阶段不试探模型相关限制，最终实际模型在 attempt 阶段完整执行限制。

一旦选定账号并入队，本版不在等待中迁移到另一个后来空闲的账号；队列也不是全局最短作业优先或加权最优调度。按 Key 轮转保证的是有资格请求的相对机会，不保证长短任务完成时间公平。

## 5. 流、重试和旧功能交互

- HTTP200、第一块 SSE、第一枚 Token 都不是完成。attempt 名额保持到 Executor 返回的生产 channel 关闭；request 名额保持到外层逻辑结果流关闭。
- 消费方取消后先取消并排空生产端，再释放。生产端错误地忽略取消会继续被计数，而不是通过提前减计数制造额外并发；不会用 TTL 强行假装上游已经结束。
- 每个 Manager 并行副本都需要名额。没有给额外 hedge 实现特殊低优先级/no-wait，本版它与其他实际尝试同受限。
- 本地流控错误为 `local_flow_control`，使用 request-stop 路径，不写账号失败/额度冷却，不触发模型回退或 Antigravity credits 回退。
- 在等待后复查账号是否仍存在、已禁用、生命周期 generation 或分组改变、模型是否进入既有冷却。普通观测用的公开 Generation 改变不代表账号已被替换。既有 paid credits 路径保留它允许回退的条件，不重新按免费额度阻止。
- **Codex 429 连续性特殊处理**：V2 将正常调用的 incumbent/canary 激活延后到实际取得名额之后，因此普通排队者不成为在途证据。已被旧/特殊路径直接预留的请求仍立即申请。具体差异见 V2 文档。

## 6. 热更新、容量与兼容

- 没有 `flow-control` 或 `enabled:false` 时走旧行为，不重命名 Home 并发、重试、UA 等旧配置。
- 配置节点类型仍须符合 YAML/Go schema。关闭状态跳过业务语义检查，不意味着 `rules: some-string` 这类错误类型也能被后端接受。Panel 保留未知草稿不等于后端保证接收错误类型。
- 减小限额不取消进行中的响应，已有调用重新投影到新并发规则，待自然结束；已有排队请求保留原截止时间但使用新规则判定。
- 保持规则ID、阶段、scope时保留现存频率记录。增加/延长窗口不能恢复此前未记录或已过期的数据；更换ID、scope或重启不是严格无缝的额度迁移，不能用作计费配额。
- 计数桶默认上限10000、频率历史默认200000；最多128规则、每规则8窗口；活跃跟踪另有100000条硬边界。容量不足返回503，不删除活跃或未过期记录来放行。
- 热更新后若现存状态无法放进新的容量边界，Engine保留旧策略；管理接口显示配置应用失败，新受控请求503，已有响应仍可结束。修正配置后可恢复。
- Home与本地flow-control同时启用会明确拒绝，避免不明含义的双重扣数。本版不能跨多个CPA实例聚合计数。

## 7. API与Panel

`GET /v0/management/flow-control` 当前返回 schema-version 2，另有 `/events` 每秒快照和 `/explain` 只读效果解释。V1 字段保留，增加单凭据、活动列表、实际策略和未完整身份的解释标志。具体 API、展示上限及断线行为见 V2 文档。

规则编辑增加单凭据与自定义组合、五规则场景向导及交集解释；保存继续复用现有 YAML 流程。

## 8. 明确没有实现的部分

不提供多实例分布式配额、TPM/token预测收费、自适应升降限额、持久化任务队列、每客户端空闲WS连接数限制、所有低层HttpRequest入口覆盖、所有Executor内部wire重试独立速率计数。低层Manager.HttpRequest、独立Alpha Search/live/realtime relay及插件绕开Manager的调用不受本批控制；这些边界不可隐去。

## 配置示例（数值仅示例，不是上游允许值或推荐生产限额）

```yaml
flow-control:
  enabled: false
  queue:
    max-waiting: 32
    max-waiting-per-key: 8
    max-bytes: 67108864
    max-wait-ms: 15000
  rules:
    - id: ingress-total
      stage: request
      scope: global
      max-concurrent: 16
    - id: each-key
      stage: request
      scope: key
      max-concurrent: 4
    - id: each-key-model
      stage: request
      scope: key-model
      max-concurrent: 2
      windows:
        - requests: 30
          period-ms: 60000
        - requests: 600
          period-ms: 3600000
    - id: upstream-total
      stage: attempt
      scope: global
      max-concurrent: 8
    - id: each-model
      stage: attempt
      scope: model
      max-concurrent: 6
    - id: each-account
      stage: attempt
      scope: account
      max-concurrent: 2
    - id: each-account-model
      stage: attempt
      scope: account-model
      max-concurrent: 1
```

Key、account留空时匹配全部；要指定对象，从受保护的管理状态接口或Panel选择引用，不在新配置里复制明文密钥。规则ID不要随意改名，否则对应频率历史无法按原桶延续。
