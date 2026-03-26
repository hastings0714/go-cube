# go-cube REST API 手册

go-cube 是 CubeJS 的 Go 轻量替代实现，后端为 ClickHouse。

## GET /load (或 /cubejs-api/v1/load)

### 请求参数

| 参数 | 必填 | 说明 |
|------|------|------|
| `query` | 是 | JSON（URL编码），见下方结构 |
| `queryType` | 否 | 传 `multi` 时响应格式为 `{"results": [...]}` |

### Query 结构

```jsonc
{
  "measures":       ["Cube.field"],    // 聚合字段
  "dimensions":     ["Cube.field"],    // 分组/明细字段
  "segments":       ["Cube.name"],     // 预定义过滤片段（见 Cube 模型定义）
  "filters":        [...],             // 过滤条件
  "timeDimensions": [...],             // 时间范围与粒度
  "order":          {"Cube.field": "desc"},
  "limit":          20,
  "offset":         0,
  "timezone":       "Asia/Shanghai",
  "ungrouped":      true               // 明细模式，跳过 GROUP BY
}
```

---

### measures / dimensions / ungrouped

| 组合 | 行为 |
|------|------|
| 有 measures，无 dimensions | 全表聚合，返回单行 |
| 有 measures，有 dimensions | GROUP BY dimensions |
| `ungrouped: true` | 跳过 GROUP BY，返回原始行 |

---

### timeDimensions

```jsonc
"timeDimensions": [
  {
    "dimension": "AccessView.ts",
    "dateRange": "from 15 minutes ago to 15 minutes from now",
    "granularity": "minute"   // 可选
  }
]
```

**dateRange** 支持：
- 预设：`"today"`、`"this year"`
- 相对：`"from N unit ago to [N unit from now | now]"`
- 绝对：`["2026-01-01T00:00:00.000", "2026-01-01T23:59:59.999"]`

**granularity** 时间字段按粒度分桶后加入 SELECT 和 GROUP BY：

| 值 | ClickHouse 函数 |
|----|----------------|
| `minute` | `toStartOfMinute` |
| `hour` | `toStartOfHour` |
| `day` | `toStartOfDay` |
| `month` | `toStartOfMonth` |

---

### filters

```jsonc
"filters": [
  {"member": "AccessView.status", "operator": "equals", "values": ["200"]}
]
```

`"member"` 与 `"dimension"` 等价。支持的 operator：

| operator | 说明 |
|----------|------|
| `equals` | IN |
| `notEquals` | NOT IN |
| `contains` | LIKE %x% |
| `notContains` | NOT LIKE %x% |
| `startsWith` | LIKE x% |
| `gt` / `gte` / `lt` / `lte` | 数值比较 |
| `set` / `notSet` | 非空 / 为空（无需 values） |

---

### order

对象或数组格式均支持：

```jsonc
"order": {"AccessView.count": "desc", "AccessView.ts": "asc"}
// 或
"order": [["AccessView.count", "desc"]]
```

有 granularity 时，对时间字段排序自动转为聚合函数形式。

---

## 响应格式

```jsonc
{
  "results": [
    {
      "data": [
        {"AccessView.ts": "2026-02-06T12:56:05.000", "AccessView.count": "42"}
      ]
    }
  ]
}
```

字段名格式为 `CubeName.fieldName`。错误时返回 `{"error": "..."}`.

---

## 支持的 Cube 模型

| Cube | 说明 |
|------|------|
| `AccessView` | 访问日志主视图 |
| `AccessRawView` | 访问日志原始请求/响应 |
| `ApiView` | API 资产视图 |
| `ApiWeakView` | API 弱点概览 |
| `WeakView` | 弱点详情（含 AI 分析） |
| `WeakDetailView` | 弱点原始证据 |
| `RiskView` | 风险事件视图 |
| `AuditView` | 审计维度分析 |
| `SystemNodesView` | 节点状态/磁盘监控 |
| `PromptView` | AI Prompt 日志 |
| `RiskPromptView` | 高风险 Prompt 日志 |

---

## 典型示例

### 明细查询

```json
{
  "ungrouped": true,
  "measures": [],
  "timeDimensions": [{"dimension": "AccessView.ts", "dateRange": "from 15 minutes ago to 15 minutes from now"}],
  "order": {"AccessView.ts": "desc"},
  "dimensions": ["AccessView.id", "AccessView.ts", "AccessView.ip", "AccessView.host", "AccessView.url"],
  "limit": 20,
  "offset": 0,
  "segments": ["AccessView.org", "AccessView.black"],
  "timezone": "Asia/Shanghai"
}
```

### 聚合 + 时序

```json
{
  "measures": ["AccessView.count", "AccessView.blockCount"],
  "timeDimensions": [{"dimension": "AccessView.ts", "dateRange": "from 60 minutes ago to 60 minutes from now", "granularity": "minute"}],
  "dimensions": ["AccessView.channel"],
  "segments": ["AccessView.org", "AccessView.black"],
  "timezone": "Asia/Shanghai"
}
```

### 全局汇总

```json
{
  "measures": ["AccessView.count", "AccessView.blockCount", "AccessView.uniqIpCount"],
  "timeDimensions": [{"dimension": "AccessView.ts", "dateRange": "from 60 minutes ago to 60 minutes from now"}],
  "filters": [{"member": "AccessView.resultScore", "operator": "gt", "values": ["0"]}],
  "segments": ["AccessView.org", "AccessView.black"],
  "timezone": "Asia/Shanghai"
}
```

### 时序趋势（月粒度）

```json
{
  "measures": ["AccessView.searchCount", "AccessView.blockSearchCount"],
  "timeDimensions": [{"dimension": "AccessView.ts", "dateRange": "this year", "granularity": "month"}],
  "segments": ["AccessView.org", "AccessView.black"],
  "timezone": "Asia/Shanghai"
}
```
