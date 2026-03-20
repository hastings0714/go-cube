package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Servicewall/go-cube/model"
)

type QueryRequest struct {
	Ungrouped      bool            `json:"ungrouped"`
	Measures       []string        `json:"measures"`
	TimeDimensions []TimeDimension `json:"timeDimensions"`
	Order          OrderMap        `json:"order"`
	Filters        []Filter        `json:"filters"`
	Dimensions     []string        `json:"dimensions"`
	Limit          int             `json:"limit"`
	Offset         int             `json:"offset"`
	Segments       []string        `json:"segments"`
	Timezone       string          `json:"timezone"`
}

// DateRange 支持字符串或字符串数组格式
type DateRange struct{ V interface{} }

func (dr *DateRange) UnmarshalJSON(data []byte) error {
	var arr []string
	if json.Unmarshal(data, &arr) == nil {
		dr.V = arr
		return nil
	}
	var str string
	if json.Unmarshal(data, &str) == nil {
		dr.V = str
		return nil
	}
	return fmt.Errorf("dateRange must be a string or array of strings")
}

type TimeDimension struct {
	Dimension   string    `json:"dimension"`
	DateRange   DateRange `json:"dateRange"`
	Granularity string    `json:"granularity,omitempty"`
}

type OrderMap map[string]string

type Filter struct {
	Member   string      `json:"member"`
	Operator string      `json:"operator"`
	Values   interface{} `json:"values"`
	Or       []Filter    `json:"or,omitempty"`
}

type QueryResponse struct {
	QueryType string        `json:"queryType"`
	Results   []QueryResult `json:"results"`
	SlowQuery bool          `json:"slowQuery,omitempty"`
}

type QueryResult struct {
	Query QueryRequest `json:"query"`
	Data  []RowData    `json:"data"`
}

type RowData = map[string]interface{}

// splitMemberName 将 "CubeName.fieldName" 或 "CubeName.fieldName.subKey" 拆分为
// (cubeName, fieldName, subKey)，subKey 为空表示无三级 key。
func splitMemberName(s string) (string, string, string) {
	cube, rest, _ := strings.Cut(s, ".")
	field, subKey, _ := strings.Cut(rest, ".")
	return cube, field, subKey
}

// granularityFunc 将 CubeJS granularity 映射到 ClickHouse 截断函数名
var granularityFunc = map[string]string{
	"second":  "toStartOfSecond",
	"minute":  "toStartOfMinute",
	"hour":    "toStartOfHour",
	"day":     "toStartOfDay",
	"week":    "toStartOfWeek",
	"month":   "toStartOfMonth",
	"quarter": "toStartOfQuarter",
	"year":    "toStartOfYear",
}

// ════════════════════════════════════════════════════════════════════════════════
// Pre-Aggregation Filter: 将时间条件下推到子查询内部
// ════════════════════════════════════════════════════════════════════════════════

// preAggFilterResult 收集需要注入到子查询中的过滤条件。
// 所有时间值都内联到 SQL 中（不使用 ? 绑定），因此不产生额外参数。
type preAggFilterResult struct {
	// placeholder -> SQL 片段（含 AND 前缀）
	clauses map[string]string
	// 已成功下推的维度 fieldName 集合，外层 WHERE 应跳过这些维度避免重复过滤
	pushedDown map[string]bool
}

// buildPreAggFilters 扫描 req.TimeDimensions，匹配 cube.PreAggregationFilters，
// 生成需要注入到子查询内部的 WHERE 条件片段。
//
// 所有时间值都通过 convertToClickHouseTimeExpr 统一转换为 ClickHouse 内联表达式，
// 不使用 ? 绑定参数。
//
// 支持的 dateRange 格式：
//   - nil / 不传               → 不加任何过滤（全部数据）
//   - ["2026-03-20T00:00:00.000", "2026-03-20T23:59:59.000"] → 绝对时间
//   - "from 7 days ago to now" → 相对范围
//   - "today"                  → toDate(col) = today()
//   - "this month" / "last month" → 月级范围
func buildPreAggFilters(req *QueryRequest, cube *model.Cube) *preAggFilterResult {
	result := &preAggFilterResult{
		clauses:    make(map[string]string),
		pushedDown: make(map[string]bool),
	}

	if len(cube.PreAggregationFilters) == 0 {
		return result
	}

	// 建立 dimension fieldName -> PreAggregationFilter 的快速查找
	filterMap := make(map[string]model.PreAggregationFilter, len(cube.PreAggregationFilters))
	for _, pf := range cube.PreAggregationFilters {
		filterMap[pf.Dimension] = pf
	}

	for _, td := range req.TimeDimensions {
		// dateRange 为 nil → 不传为全部，不下推任何条件
		if td.DateRange.V == nil {
			continue
		}
		_, fieldName, _ := splitMemberName(td.Dimension)
		pf, ok := filterMap[fieldName]
		if !ok {
			continue
		}

		col := pf.TargetColumn
		pushed := false

		switch v := td.DateRange.V.(type) {
		case []string:
			// 绝对时间范围：["2026-03-20T00:00:00.000", "2026-03-20T23:59:59.000"]
			if len(v) == 2 {
				startExpr := convertToClickHouseTimeExpr(v[0])
				endExpr := convertToClickHouseTimeExpr(v[1])
				clause := fmt.Sprintf("AND %s >= %s AND %s <= %s", col, startExpr, col, endExpr)
				result.clauses[pf.Placeholder] = clause
				pushed = true
			}
		case string:
			// 相对时间范围 / 单值
			if v != "" {
				if start, end, ok := parseRelativeTimeRange(v); ok {
					clause := fmt.Sprintf("AND %s >= %s AND %s <= %s", col, start, col, end)
					result.clauses[pf.Placeholder] = clause
					pushed = true
				} else {
					// 单值如 "today" → toDate(col) = today()
					clause := fmt.Sprintf("AND toDate(%s) = %s", col, convertToClickHouseTimeExpr(v))
					result.clauses[pf.Placeholder] = clause
					pushed = true
				}
			}
		}

		if pushed {
			result.pushedDown[fieldName] = true
		}
	}

	return result
}

// applyPreAggFilters 将下推条件注入到 cube SQL 模板中，返回替换后的 SQL。
//
// 规则：
//   - 匹配到的占位符 → 替换为生成的 AND 子句
//   - 未匹配的占位符 → 替换为空字符串（因为模板中有 WHERE 1=1，语法安全）
func applyPreAggFilters(sqlTable string, cube *model.Cube, result *preAggFilterResult) string {
	replaced := sqlTable

	for _, pf := range cube.PreAggregationFilters {
		placeholder := "{" + pf.Placeholder + "}"
		if clause, ok := result.clauses[pf.Placeholder]; ok {
			replaced = strings.Replace(replaced, placeholder, clause, 1)
		} else {
			// 没有传入对应时间范围 → 移除占位符
			replaced = strings.Replace(replaced, placeholder, "", 1)
		}
	}

	return replaced
}

// ════════════════════════════════════════════════════════════════════════════════
// BuildQuery 主函数
// ════════════════════════════════════════════════════════════════════════════════

func BuildQuery(req *QueryRequest, cube *model.Cube) (string, []interface{}, error) {
	var sql strings.Builder
	var whereParams []interface{}
	var havingParams []interface{}

	// ── 第 0 步：处理 pre-aggregation filter 下推 ──────────────────────────
	preAggResult := buildPreAggFilters(req, cube)

	// 收集有 granularity 的时间维度：alias -> truncated SQL expr
	type granularityCol struct {
		alias string
		expr  string
	}
	var granCols []granularityCol
	for _, td := range req.TimeDimensions {
		if td.Granularity == "" {
			continue
		}
		fn, ok := granularityFunc[td.Granularity]
		if !ok {
			continue
		}
		_, fieldName, subKey := splitMemberName(td.Dimension)
		field, ok := cube.GetField(fieldName, subKey)
		if !ok {
			continue
		}
		alias := td.Dimension + "." + td.Granularity
		expr := fmt.Sprintf("%s(%s)", fn, field.SQL)
		granCols = append(granCols, granularityCol{alias: alias, expr: expr})
	}

	// ── SELECT ─────────────────────────────────────────────────────────────
	sql.WriteString("SELECT ")
	first := true
	memberInSelect := make(map[string]bool)
	writeFields := func(names []string) {
		for _, name := range names {
			_, fieldName, subKey := splitMemberName(name)
			if field, ok := cube.GetField(fieldName, subKey); ok {
				if !first {
					sql.WriteString(", ")
				}
				fmt.Fprintf(&sql, "%s AS \"%s\"", field.SQL, name)
				first = false
				memberInSelect[name] = true
			}
		}
	}
	writeFields(req.Dimensions)
	writeFields(req.Measures)
	for _, gc := range granCols {
		if !first {
			sql.WriteString(", ")
		}
		fmt.Fprintf(&sql, "%s AS \"%s\"", gc.expr, gc.alias)
		first = false
		memberInSelect[gc.alias] = true
	}
	if first {
		sql.WriteString("1")
	}

	// ── FROM — 注入下推条件到子查询模板 ────────────────────────────────────
	sqlTable := cube.GetSQLTable()
	sqlTable = applyPreAggFilters(sqlTable, cube, preAggResult)

	sql.WriteString(" FROM ")
	sql.WriteString(sqlTable)

	// ── WHERE / HAVING ─────────────────────────────────────────────────────
	var where []string
	var having []string

	isMeasure := func(member string) bool {
		_, fieldName, _ := splitMemberName(member)
		_, ok := cube.Measures[fieldName]
		return ok
	}

	// segments
	for _, seg := range req.Segments {
		_, segName, _ := splitMemberName(seg)
		if s, ok := cube.Segments[segName]; ok && s.SQL != "" {
			where = append(where, s.SQL)
		}
	}

	// filters
	for _, filter := range req.Filters {
		if len(filter.Or) > 0 {
			if filter.Member != "" || filter.Operator != "" || filter.Values != nil {
				return "", nil, fmt.Errorf("filter 不能同时包含 or 和 member/operator/values 字段")
			}
			var orClauses []string
			var orParams []interface{}
			for _, sub := range filter.Or {
				clause, p := buildFilterClause(sub, cube)
				if clause != "" {
					orClauses = append(orClauses, clause)
					orParams = append(orParams, p...)
				}
			}
			if len(orClauses) > 0 {
				hasMeasure := false
				for _, sub := range filter.Or {
					if isMeasure(sub.Member) {
						hasMeasure = true
						break
					}
				}
				combined := "(" + strings.Join(orClauses, " OR ") + ")"
				if hasMeasure {
					having = append(having, combined)
					havingParams = append(havingParams, orParams...)
				} else {
					where = append(where, combined)
					whereParams = append(whereParams, orParams...)
				}
			}
			continue
		}

		clause, p := buildFilterClause(filter, cube)
		if clause != "" {
			if isMeasure(filter.Member) {
				having = append(having, clause)
				havingParams = append(havingParams, p...)
			} else {
				where = append(where, clause)
				whereParams = append(whereParams, p...)
			}
		}
	}

	// timeDimensions — 仅对未下推的维度生成外层 WHERE 条件。
	// 已下推到子查询的维度不再重复过滤，避免在聚合后的列（如 max_ts）上做冗余判断。
	for _, td := range req.TimeDimensions {
		_, fieldName, subKey := splitMemberName(td.Dimension)
		// 已下推到子查询 → 跳过外层
		if preAggResult.pushedDown[fieldName] {
			continue
		}
		field, ok := cube.GetField(fieldName, subKey)
		if !ok || td.DateRange.V == nil {
			continue
		}
		switch v := td.DateRange.V.(type) {
		case []string:
			if len(v) == 2 {
				where = append(where, fmt.Sprintf("%s >= ? AND %s <= ?", field.SQL, field.SQL))
				whereParams = append(whereParams, normalizeDateTime(v[0]), normalizeDateTime(v[1]))
			}
		case string:
			if v != "" {
				if start, end, ok := parseRelativeTimeRange(v); ok {
					where = append(where, fmt.Sprintf("%s >= %s AND %s <= %s", field.SQL, start, field.SQL, end))
				} else {
					where = append(where, fmt.Sprintf("toDate(%s) = %s", field.SQL, convertToClickHouseTimeExpr(v)))
				}
			}
		}
	}

	if len(where) > 0 {
		sql.WriteString(" WHERE ")
		sql.WriteString(strings.Join(where, " AND "))
	}

	// ── GROUP BY ───────────────────────────────────────────────────────────
	if len(req.Measures) > 0 && (len(req.Dimensions) > 0 || len(granCols) > 0) {
		sql.WriteString(" GROUP BY ")
		groupFirst := true
		for _, dim := range req.Dimensions {
			if _, ok := memberInSelect[dim]; ok {
				if !groupFirst {
					sql.WriteString(", ")
				}
				fmt.Fprintf(&sql, "\"%s\"", dim)
				groupFirst = false
			}
		}
		for _, gc := range granCols {
			if _, ok := memberInSelect[gc.alias]; ok {
				if !groupFirst {
					sql.WriteString(", ")
				}
				fmt.Fprintf(&sql, "\"%s\"", gc.alias)
				groupFirst = false
			}
		}
	}

	// ── HAVING ─────────────────────────────────────────────────────────────
	if len(having) > 0 {
		sql.WriteString(" HAVING ")
		sql.WriteString(strings.Join(having, " AND "))
	}

	// ── 参数拼接 ───────────────────────────────────────────────────────────
	// pre-agg 条件的时间值已内联到 SQL 中，不产生绑定参数。
	// 顺序：whereParams → havingParams，与 SQL 中 ? 出现顺序一致。
	var params []interface{}
	params = append(params, whereParams...)
	params = append(params, havingParams...)

	// ── ORDER BY ───────────────────────────────────────────────────────────
	if len(req.Order) > 0 {
		sql.WriteString(" ORDER BY ")
		i := 0
		for member, direction := range req.Order {
			if i > 0 {
				sql.WriteString(", ")
			}
			if _, ok := memberInSelect[member]; ok {
				// 对于含 subKey 的成员（如 AccessView.customData.UserToken），
				// 使用原始 SQL 表达式而非带引号别名，避免 ClickHouse 解析问题。
				_, fieldName, subKey := splitMemberName(member)
				if subKey != "" {
					if f, ok := cube.GetField(fieldName, subKey); ok {
						sql.WriteString(f.SQL)
					} else {
						fmt.Fprintf(&sql, "\"%s\"", member)
					}
				} else {
					fmt.Fprintf(&sql, "\"%s\"", member)
				}
			} else {
				found := false
				for _, gc := range granCols {
					if strings.HasPrefix(gc.alias, member+".") {
						if _, ok := memberInSelect[gc.alias]; ok {
							fmt.Fprintf(&sql, "\"%s\"", gc.alias)
							found = true
							break
						}
					}
				}
				if !found {
					_, fieldName, subKey := splitMemberName(member)
					if f, ok := cube.GetField(fieldName, subKey); ok {
						sql.WriteString(f.SQL)
					} else {
						sql.WriteString(member)
					}
				}
			}
			if direction == "desc" {
				sql.WriteString(" DESC")
			}
			i++
		}
	}

	// ── LIMIT / OFFSET ────────────────────────────────────────────────────
	if req.Limit > 0 {
		fmt.Fprintf(&sql, " LIMIT %d", req.Limit)
	}
	if req.Offset > 0 {
		fmt.Fprintf(&sql, " OFFSET %d", req.Offset)
	}

	return sql.String(), params, nil
}

func validateQuery(req *QueryRequest) error {
	if len(req.Dimensions) == 0 && len(req.Measures) == 0 {
		return fmt.Errorf("query must have at least one dimension or measure")
	}
	if req.Limit < 0 {
		return fmt.Errorf("limit must be non-negative")
	}
	if req.Offset < 0 {
		return fmt.Errorf("offset must be non-negative")
	}
	return nil
}

func buildInClause(fieldSQL string, operator string, values []interface{}) (string, []interface{}) {
	placeholders := strings.Repeat("?,", len(values))
	placeholders = placeholders[:len(placeholders)-1]
	params := make([]interface{}, len(values))
	for i, v := range values {
		params[i] = v
	}
	if operator == "notEquals" {
		return fmt.Sprintf("%s NOT IN (%s)", fieldSQL, placeholders), params
	}
	return fmt.Sprintf("%s IN (%s)", fieldSQL, placeholders), params
}

func buildArrayClause(fieldSQL string, operator string, values []interface{}) (string, []interface{}) {
	params := make([]interface{}, len(values))
	for i, v := range values {
		params[i] = v
	}
	negate := operator == "notEquals" || operator == "notContains"
	neg := ""
	if negate {
		neg = "NOT "
	}
	if len(values) == 1 {
		return fmt.Sprintf("%shas(%s, ?)", neg, fieldSQL), params
	}
	placeholders := strings.Repeat("?,", len(values))
	placeholders = placeholders[:len(placeholders)-1]
	fn := "hasAny"
	if operator == "equals" || operator == "notEquals" {
		fn = "hasAll"
	}
	return fmt.Sprintf("%s%s(%s, [%s])", neg, fn, fieldSQL, placeholders), params
}

var operatorMap = map[string]string{
	"contains":    "LIKE",
	"notContains": "NOT LIKE",
	"startsWith":  "LIKE",
	"endsWith":    "LIKE",
	"gt":          ">",
	"gte":         ">=",
	"lt":          "<",
	"lte":         "<=",
}

func convertOperator(op string) string {
	if sqlOp, ok := operatorMap[op]; ok {
		return sqlOp
	}
	return op
}

func processFilterValue(value interface{}, operator string) interface{} {
	s, ok := value.(string)
	if !ok {
		return value
	}
	switch operator {
	case "contains", "notContains":
		return "%" + s + "%"
	case "startsWith":
		return s + "%"
	case "endsWith":
		return "%" + s
	}
	return value
}

func parseRelativeTimeRange(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	switch s {
	case "this month":
		return "toStartOfMonth(now())", "toStartOfMonth(now() + INTERVAL 1 MONTH)", true
	case "last month":
		return "toStartOfMonth(now() - INTERVAL 1 MONTH)", "toStartOfMonth(now())", true
	}
	s = strings.TrimPrefix(s, "from ")
	if idx := strings.LastIndex(s, " to "); idx > 0 {
		start, end := strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+4:])
		if start != "" && end != "" {
			return convertToClickHouseTimeExpr(start), convertToClickHouseTimeExpr(end), true
		}
	}
	return "", "", false
}

// convertToClickHouseTimeExpr 将时间字符串转为 ClickHouse 表达式。
//  1. 关键字：now → now(), today → today(), yesterday → yesterday()
//  2. 相对时间：15 minutes ago → now() - INTERVAL 15 MINUTE
//  3. 绝对时间：2026-03-20T00:00:00.000 → '2026-03-20 00:00:00'
//     自动去掉 T 分隔符和毫秒部分，用单引号包裹。
func convertToClickHouseTimeExpr(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	switch lower {
	case "now":
		return "now()"
	case "today":
		return "today()"
	case "yesterday":
		return "yesterday()"
	}
	if strings.HasSuffix(lower, " ago") {
		if parts := strings.Fields(strings.TrimSuffix(lower, " ago")); len(parts) == 2 {
			return fmt.Sprintf("now() - INTERVAL %s %s", parts[0], convertUnit(parts[1]))
		}
	}
	if strings.HasSuffix(lower, " from now") {
		if parts := strings.Fields(strings.TrimSuffix(lower, " from now")); len(parts) == 2 {
			return fmt.Sprintf("now() + INTERVAL %s %s", parts[0], convertUnit(parts[1]))
		}
	}
	// 绝对时间：标准化后用单引号包裹
	// "2026-03-20T00:00:00.000" → "'2026-03-20 00:00:00'"
	normalized := strings.Replace(s, "T", " ", 1)
	if idx := strings.Index(normalized, "."); idx > 0 {
		normalized = normalized[:idx]
	}
	return fmt.Sprintf("'%s'", normalized)
}

var unitMap = map[string]string{
	"second": "SECOND", "minute": "MINUTE", "hour": "HOUR",
	"day": "DAY", "week": "WEEK", "month": "MONTH", "year": "YEAR",
}

func convertUnit(unit string) string {
	unit = strings.TrimSuffix(unit, "s")
	if u, ok := unitMap[unit]; ok {
		return u
	}
	return strings.ToUpper(unit)
}

// normalizeDateTime 将前端传入的时间字符串标准化为 ClickHouse DateTime 格式："2026-03-20T00:00:00.000" → "2026-03-20 00:00:00"
func normalizeDateTime(s string) string {
	s = strings.Replace(s, "T", " ", 1)
	if idx := strings.Index(s, "."); idx > 0 {
		s = s[:idx]
	}
	return s
}

func buildFilterClause(filter Filter, cube *model.Cube) (string, []interface{}) {
	_, fieldName, subKey := splitMemberName(filter.Member)
	field, ok := cube.GetField(fieldName, subKey)
	if !ok || field.SQL == "" {
		return "", nil
	}

	switch filter.Operator {
	case "set":
		return fmt.Sprintf("notEmpty(%s)", field.SQL), nil
	case "notSet":
		return fmt.Sprintf("empty(%s)", field.SQL), nil
	}

	valuesArr, _ := filter.Values.([]interface{})
	if len(valuesArr) == 0 && filter.Values != nil {
		valuesArr = []interface{}{filter.Values}
	}
	if len(valuesArr) == 0 {
		return "", nil
	}

	if field.Type == "array" {
		return buildArrayClause(field.SQL, filter.Operator, valuesArr)
	}

	if filter.Operator == "equals" || filter.Operator == "notEquals" {
		return buildInClause(field.SQL, filter.Operator, valuesArr)
	}
	sqlOp := convertOperator(filter.Operator)
	value := processFilterValue(valuesArr[0], filter.Operator)
	return fmt.Sprintf("%s %s ?", field.SQL, sqlOp), []interface{}{value}
}
