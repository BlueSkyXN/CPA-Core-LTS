# 本地流控：当前使用与维护指南

本文是当前产品的唯一完整指南。`flow-control-v2.md`、`flow-control-v3.md` 仅保留旧链接入口，不作为另一套设置说明。

功能是默认关闭的 LTS 定制扩展，运行在单个 CPA 进程中。不增加数据库、Redis、依赖或监控历史存储，不修改 Codex UA、`codexmetadata`、usage 统计、模型目录文件或已有 Provider 协议。

## 1. 先分清三个对象

| 对象 | 含义 |
|---|---|
| 调用方 Key | 访问你的 CPA 的 Key；使用已有鉴权产生的调用方引用 |
| 模型 | request 层是请求名称／公开别名；attempt 层是路由解析后的执行目标 |
| 上游账号 | 一条 Auth 记录，一份 OAuth 认证文件或一条配置的上游 API Key；账号＝凭据 |

V3 不按邮箱、远程 account_id、Token 或自定义账号组归并。Token 刷新时保留 Auth.ID，就保留原对象引用。旧 credential 字段仅为迁移入口，V3 不恢复双层账号设计。

Key、账号复选保存管理接口返回的引用，不填写原始 CPA Key、OAuth Token 或上游密钥。模型／Provider 比对忽略大小写，计数去掉已有 reasoning 后缀；真正的请求模型不会因此被重写。

## 2. 开关、保存与实际生效

### 三个主开关

| 配置 | 默认 | 开启 | 关闭 |
|---|---|---|---|
| `enabled` | false | 对已接入 Manager 的新模型调用实施规则 | 新调用不再受本模块限制；已有执行正常结束、释放；不删除规则 |
| `observation.realtime` | false | 允许管理端订阅共享汇总 SSE | 新订阅返回409，已有订阅收到 disabled 后结束；不改变模型计数 |
| `observation.resources` | false | 查询时按需采样进程资源 | 停止采样，不展示旧资源快照；不改变模型计数 |

启用但规则列表为空时，仅记录已接入调用，不施加并发或频率上限；这不是默认隐藏了一条限制。

Panel 的“开始／停止实时”只控制当前页面。关闭页面、关闭实时和关闭资源采样，都不会取消模型执行或释放模型名额。服务端关闭实时后，Panel 不转成自动轮询。

重要：本模块不是常开全站监控。关闭流控时，新进入的普通调用绕过流控适配器，不会被补记为“受控活动请求”；已有受控执行和已排队工作仍完成其生命周期。此时汇总不能被解释为全站并发。首次从关闭切到开启也不能回溯此前未受控的执行，需要严格总量时先排空或在受控重启后启用。

已有队列在关闭规则后解除本地限制，但仍遵从请求取消、截止时间和执行前的账号可用性检查。关闭并不是强行取消排队者。

### 三份状态不能混淆

- 页面草稿：尚未保存，不能当成实际策略。
- 已保存／请求应用的配置：管理端读取到的文件或配置快照。
- 已成功应用的策略：Engine 正在执行的限制，是实际放行的依据。

通过现有 `PUT /v0/management/config.yaml` 保存时，先作格式与 Flow 运行条件检查；明显无法应用的变更返回409，不写文件。检查和 watcher 的实际提交之间仍可能有并发变化，Engine 在提交时会再次判断。

当外部文件修改或提交间竞争导致新策略不能应用时，保留上一份有效 Flow 策略及相应 Manager 运行字段。管理状态返回 `configuration-error` 和 `configuration-failure`。新调用继续按旧策略执行，不因配置错误被全局拒绝，也不是绕开限流。其他独立合法设置仍可按原配置流程应用。

修好并重新提交成功后，失败提示清除；仅等待历史到期不会自动重新提交失败的文件配置。保存成功本身不代替 watcher 运行状态确认。初次启动没有有效策略时，配置加载器仍拒绝非法启用配置，不能以运行期回退掩盖启动错误。

### 推荐操作

先编辑草稿、用预览检查匹配关系；保存后手动刷新，比较“草稿开关”和“实际开关”、策略版本及失败提示。要临时停用，仅关闭 `enabled`，不要同时更换规则语义版本。恢复时再显式启用，原规则保留。

没有 Flow 配置的首次使用者直接得到默认关闭的 V3 草稿，不需要迁移空配置；仅查看或修改其他配置不写入 Flow。首次实际编辑 Flow 时补 `version: 3`。真实 V1/V2 规则和未知内容不会因此被静默升级。

## 3. 最小配置及参数含义

以下是默认关闭的示例，不代表任何服务商公布的额度。合并到现有唯一 `flow-control` 节点，不重复创建根键。

```yaml
flow-control:
  version: 3
  enabled: false
  observation:
    realtime: false
    resources: false
    interval-ms: 2000
    max-observers: 4
  queue:
    max-waiting: 16
    max-waiting-per-key: 4
    max-bytes: 33554432
    max-wait-ms: 15000
  rules:
    - id: caller-total
      label: 每个调用方的进行中调用
      stage: request
      scope: key
      max-concurrent: 4
    - id: each-codex-account
      label: 每个 Codex 认证记录
      stage: attempt
      scope: account
      provider: codex
      max-concurrent: 5
```

| 参数 | 语义与范围 |
|---|---|
| `version` | 3 使用单账号、模型复选和首次联合申请；未指定或<=2保留旧语义 |
| `max-concurrent` | 1..100000限制并发；0表示本条不限制并发，启用的规则仍须有频率窗口 |
| `queue.max-waiting` | 0..10000；0不排队，默认0 |
| `queue.max-waiting-per-key` | 不超过总等待人数；未填或0时继承总人数 |
| `queue.max-bytes` | 等待正文登记量，最多4GiB；允许排队且未填或0时用64MiB |
| `queue.max-wait-ms` | 累计等待0..300000ms；V3的0表示不等，默认0；V1/V2允许队列时须>0 |
| `max-buckets` | 默认10000，上限100000；按实际命中创建，不预分配完整维度组合 |
| `max-history` | 默认200000，上限2000000；纯并发不记录频率历史 |
| `observation.interval-ms` | 未填或0用2000；明确值500..30000 |
| `observation.max-observers` | 未填或0用4；明确值1..16；关闭推送请用 realtime=false |

关闭流控允许保留无效规则草稿，但配置版本、YAML字段类型和观察参数仍须合法。无效草稿的解释返回“不完整／无法解释”和具体错误，不会访问非法窗口或伪报零用量。

## 4. 通用组合、模型复选与交叉限制

筛选决定哪些请求受限，分组决定这些请求怎样共享计数。复选同字段内是 OR，不同字段间是 AND。省略集合表示全部，显式空集合不是全部；启用时拒绝空集合。同一规则重复命中只计一次，不同规则分别计数。

常用分组：global、key、model、key-model、account、account-model、key-account、key-account-model；`scope: custom` 可选择 `key/model/account/provider/auth-kind`。request 阶段只能按调用方与请求模型，不知道上游账号。Provider和认证类型通常用作筛选。

```yaml
# 添加到 rules；替换为当前 Core 目录中确认的真实目标。
- id: shared-model-set
  stage: attempt
  scope: custom
  group-by: [key, account]
  models: [codex::model-a, codex::model-b]
  max-concurrent: 3
- id: each-model
  stage: attempt
  scope: custom
  group-by: [key, account, model]
  models: [codex::model-a, codex::model-b]
  max-concurrent: 2
```

这表示每个调用方×账号的两个模型合计<=3，同时每个模型<=2。再添加集合 `[model-b, model-c]` 时，model-b同时受两条共享约束。所有命中规则取交集，具体规则不覆盖公共规则，规则顺序不改变含义。结束一个执行只触发重新判断，不保证某个等待者的其他约束也已经有余量。

规则ID是计数身份，不自动合并看似相同的规则。共享计数不代表平均分配或每个模型都有保留名额。规则计数不能相加作为真实执行总量。

### 公开别名与执行目标

request 阶段选择 `models` 中的公开名称。attempt 阶段选择带 `provider::model` 的实际目标。能力接口只有带 `resolved-model-options` 特性时，Panel 才将 `model-options` 作为已解析目录；旧 Core 不会被自动用公开别名替代。

一个公开别名可能对应多个上游模型池目标。目录逐个列出实际目标，并提供 `aliases`、`accounts` 说明。查询使用纯解析路径，不推进轮转游标、不选账号、不申请名额。想限制这个模型池合计，复选全部目标并不按 model 分组。`model-options-truncated=true` 表示目录或关联列表截断，保存的未列出选项仍保留。

插件在目录解析后动态重写的目标仍需根据实际运行记录确认；目录是“已知目标”，不是对任意插件行为的完整枚举。未解析的信息不能猜成公开别名。不存在匹配规则不代表存在一个隐藏限制。

## 5. 生命周期、队列和顺序重试

首次创建逻辑调用还不占运行名额；原选择器确定合法目标后，在同一个 Engine 临界区内联合检查 request+attempt 所有适用约束，全部满足才一起占用。首次等待不占首次运行名额。

request 计一次 Manager.Execute/ExecuteCount/ExecuteStream 操作，内部重试共用它。attempt 计 Manager 发起的一次 Executor 调用；Executor 内部HTTP重试或WS重连不是本模块逐网络请求的精确RPM。外层 Handler 再进入Manager是另一次操作。

收到200、首个Token、返回StreamResult均不释放。HoldChannel在生产源结束后释放；客户端取消但生产者尚未关闭时保持draining。没有执行TTL。生产者忽略取消的问题要真实修复，不能靠删计数掩盖。

顺序流式401刷新重试、模型池内下一目标的继续执行，先排空并等待前一个生产端关闭，再申请下一次名额。收尾等待计入同一次逻辑调用的累计等待预算，受原ctx取消约束；没有把它标成“额外并行尝试”。额外并行副本仍无空闲就不排队，不抢占正常等待者。等待超时后也不能提前释放尚未结束的源。

队列按调用方轮转，再取该Key最早当前可执行项；阻塞队头可被跳过。准备阶段编译分组信息；一轮最多放行64个，后续经现有定时器短暂续调度。每次放行都按最新共享计数判断，不沿用已过期的可用结果。既有等待者先被尝试，新请求不能无限插队。

没有固定约束时只在原合法候选中参考账号容量；选定并入队后不动态换账号，不修改模型或会话状态。公平是启动机会，不是执行时间、Token或费用平均，也不保证复杂交叉集合在等待截止前必然成功。

队列数量、每Key数量、payload登记字节和累计等待时间均有边界。并发繁忙的Retry-After只是建议，因为无法预知活动请求何时结束；外部网关超时仍需单独配置。不会提前发送假200。

## 6. 热更新与旧配置

跨V1/V2与V3时，仍有活动或等待项就拒绝语义切换。先排空再切换。没有旧策略则不要求迁移。

纯并发规则可对活动身份重新投影分组。降低上限不砍流，新调用等待自然回落。标签与排序不重置计数。

仍保留频率历史时，同一规则ID改变筛选集合或计数组合会被拒绝；历史仅保存时间和许可ID，无法推断旧请求属于新集合。保持原范围、等历史到期后重新提交，或明确使用新ID开始新历史。关闭规则时允许保留待修订草稿；重新启用仍作历史兼容判断，不能靠一次关闭绕过它。

Home与本地限制不同时启用。先关闭本地、完成运行状态确认，再启用Home；不要一次同时更换两套账号分配方式。本批不改Home协议、额度冷却、usage回调和codexmetadata语义。

## 7. 管理 API 与观察资源

继续使用现有管理认证；无新增写入接口。所有URL以下省略 `/v0/management`。

| 方法与路径 | 用途 |
|---|---|
| GET `/flow-control` | schema3能力、期望配置、有效策略、失败说明、引用目录和摘要 |
| GET `/flow-control/summary` | 按需轻量摘要，不扫描目录 |
| GET `/flow-control/events` | 开启realtime后订阅共享摘要；关闭409，过多观察者503 |
| GET `/flow-control/details` | 手动分页，默认100最多200；offset<=10000，支持过滤 |
| POST `/flow-control/preview` | 草稿或当前策略，1..24个具体目标；只解释，不占位 |
| POST `/flow-control/migration-preview` | 旧配置迁移建议，不自动写盘 |
| GET `/flow-control/explain` | 保留单目标读取；非法停用草稿也安全返回说明 |

schema仍为3，新增字段兼容旧读者：`configuration-failure`含code/rule/message/rejected-at；`configured-policy`为期望配置，`policy`为已应用策略；`model-options`补aliases/accounts，`features`补resolved-model-options、last-good-policy。不能把HTTP保存成功或configured-enabled当作已应用。

预览是“本阶段容量”，首次启动仍要联合满足另一阶段、账号选择与会话要求。未知账号不显示零，草稿无法对应运行计数则显示未知。结果不是预留容量或准确ETA。

多个观察者共享一份限频JSON，不写监控历史。标签页隐藏暂停。详情先选记录再解释，不自动每秒查询；单页查询仍有锁内读取成本，不声称零开销。队列阻塞直方图是最近判断，不是实时全约束扫描。

资源查询只读取Go堆对象、Go-managed内存、goroutines以及当前工作目录文件系统可用空间，分别缓存5秒/30秒。不是RSS、CPU、日志目录大小或“流控精确内存”；未知显示null。关闭资源后下一份汇总移除旧值。无观察者时没有常驻采样任务。

## 8. 范围和运维限制

单进程有效，不是跨实例总额度。不增加Token TPM、费用预测、任务持久化、账号去重或监控数据库。当前低层 Manager.HttpRequest、绕过Manager自行联网的插件、独立实时relay等未全面纳入。本模块不等于所有API入口防过载；现有请求体大小、连接和流缓冲需继续按原设计管理。

等待正文登记量不是进程总内存，规则数与计数组上限也不表示预分配。已有模型日志、错误日志和外部网关记录仍会占磁盘，继续使用原日志轮转；Flow本身不新增快照落盘。构建忽略local目录，交接文档仍保留在交付包中。

## 9. 验证与维护

默认开关、模型目录只读、配置失败保留旧策略、交叉规则、顺序重试收尾、取消与重复释放，都有针对性测试。集成测试位于 auth、management 包，需项目声明的Go版本与真实依赖。Panel的YAML、React测试也必须使用锁定依赖，不得用替身宣告通过。

```sh
# Core：使用 go.mod 指定的工具链
go test -race ./sdk/cliproxy/flowcontrol ./sdk/cliproxy/auth ./internal/api/handlers/management
go test ./internal/config ./sdk/cliproxy
# Panel：在匹配的项目中执行
npm ci
npm run test:flow-control
npm run type-check
npm run lint
npm run build
```

独立包复制测试、语法检查与合成性能对照不等于完整项目构建。交付根目录TEST_RESULTS记录实际已执行项，不沿用输入基线的通过数量。
