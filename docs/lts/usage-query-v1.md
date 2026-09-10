# Usage 按量查询 v1

## 兼容边界

旧 `/v0/management/usage` 仍返回完整快照；`/usage/export`、`/usage/import` 保持 canonical v3 及原迁移、去重规则。新查询使用同一内存记录源，不消费队列，不引入数据库，不删除历史。新接口均沿用原 Management 鉴权和可用性中间件。

## 接口

路径均相对于 `/v0/management`：

| 方法与路径 | 行为 |
| --- | --- |
| `GET /usage/query/capabilities` | `version: 1`、不透明 `bound`、`now_ms`、模型目录、`max_page_size: 200` |
| `POST /usage/query/summary` | 当前范围的 totals 和按 modules 选择的分组，不包含请求明细 |
| `POST /usage/query/details` | 时间倒序、内部序号倒序的当前页；完整筛选结果的 total 和 metrics |
| `POST /usage/query/pricing` | 与摘要相同的查询，加上按模型、tier 证据和上下文档位分类的计价事实汇总 |

POST JSON 公共字段：`bound`、`now_ms`、`from_ms`、`to_ms`、`timezone`、`filter`。时间使用 JavaScript 毫秒精度，起止均包含；省略时间边界表示不限制。`timezone` 是 IANA 时区。`now_ms` 固定速率、健康网格和相对时间桶的参考时刻。

摘要 `modules`：`models`、`api_models`、`credentials`、`hours`、`days`、`minutes`、`rates`、`status`、`health`。`hours` 默认最近 24 小时，可用 `hour_from_ms` 扩展；`days` 保留实际存在的日期；`minutes` 为最后 60 分钟；`rates` 为最后 30 分钟；`status` 为 20 个十分钟块。`health` 保留页面独立的最近七天全局健康窗口，不受概览时间过滤影响。

每次摘要只遍历一次记录，同时累计选定模块。扫描以 256 条为一块，短读锁复制有限工作缓冲，锁外累计；不会先构造完整 Snapshot。第一版扫描仍与记录数相关，并不承诺常数时间。最多 100,000 个分组，超出时明确报错，不静默截断。

## 分页、筛选与失效

- `limit` 默认 100，最大 200；`cursor` 使用上页返回的 `next_cursor`。
- 首次 `include_options: true` 返回仅受时间范围限制的模型、API、来源/凭据身份、effort 和字段可用性；翻页与其他筛选不必重复读取选项。
- `filter` 支持 `model`、`api`、`auth_index`、来源与凭据配对的 `identities`、`tier`、`effort`、`failed`、`cached`，以及 token `metric` 的 `minimum`/`maximum`。metric 对齐 Panel 现有八种数值筛选。
- bound 固定实例代次和已记录序号上界；cursor 另绑定时间范围、筛选及排序位置。同一会话的摘要、费用和明细传递同一 bound。
- 晚完成请求、历史导入只在下次建立 bound 后出现；重复导入不获得新序号。内部序号不进入旧快照或导出格式。
- 非法查询返回 400；实例或上界失效返回 409；15 秒查询预算或请求取消返回 408。单个请求体上限 2 MiB。
- 新 Panel 只在能力端点 404 且同前缀旧管理接口正常时进入旧版模式。网络、5xx、解析失败不得触发全量 fallback。失效 bound 通过刷新会话恢复，不能拼接两代页面。

## 动态计价

Panel 规范化当前价格配置一次，并解析每个模型。`rules[model].long_threshold` 只表达本次有效长上下文阈值；关闭 GPT long band 或未匹配模型不发送阈值。Core 先逐条归一化 token、判断 tier/证据和上下文类别，再累计 prompt/cache-read/cache-write/output、请求数与总 token。

Panel 对每类使用原价格算法选取 status、单价和警告，再计算金额及加权覆盖率。单价和历史金额不写入 Core。费用是独立加载状态，不阻塞概览和第一页；金额最终显示前须完成该查询范围的分类汇总。汇总改变浮点求和顺序，应以数值容差及实际金额展示核对，不提前舍入。

## HTTP 响应压缩

上述七个 usage JSON 路由在鉴权之后按 `Accept-Encoding` 协商压缩：同权重时优先 Brotli level 4、Zstandard level 1、gzip level 3。显式 `q=0` 不会被通配符覆盖；显式更高权重的 `identity` 优先。没有可接受编码时返回 406，导入 handler 不会执行。

客户端允许 identity 时，小于 1024 bytes 的响应不压缩；禁止 identity 时，小 JSON 也使用允许的编码。压缩层只保留不足 1024 bytes 的前置缓冲，超过阈值直接写入压缩器，不再复制完整快照。响应携带 `Vary: Accept-Encoding`，压缩后删除旧 `Content-Length`，已编码响应不重复压缩。JSON、状态码、POST 请求体和统计 schema 不变。

不覆盖 `usage-queue`、普通日志、SSE 或 WebSocket。Panel 使用浏览器原生解压，无需选择算法。Nginx wrapper 必须透传 `Accept-Encoding` 和 `Content-Encoding`；旧 Core 可以由 wrapper gzip 兜底。

## 发布与回滚门槛

先验证临时实例上的旧导出/新导入往返与新旧版本组合，再发布；本地构建或测试不能代表已经部署。

任何 Core 重启或 wrapper 重建前，都要确认统计保存位置在被重建实例之外。若要求升级零缺失，应在维护窗口暂停新流量、等待在途请求和 usage 分发排空、最终导出并校验，再重启、导入、对账及恢复流量。导出后到停止前的新增请求不能由旧备份恢复；不能仅以定时备份代替最终导出。现有自动保存流程只有经核对覆盖这个尾部窗口才可替代维护步骤。

Core、Panel、HFS wrapper 可分批回滚。旧接口和导出版本未改变，因此旧 Panel 可连接新 Core，新 Panel 也保留明确的旧 Core 兼容路径。回滚或发布不授权删除历史、改 Secrets 或擅自重建生产 Space。
