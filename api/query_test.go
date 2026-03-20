package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Servicewall/go-cube/model"
)

// testCube builds a minimal Cube fixture for unit tests.
func testCube() *model.Cube {
	return &model.Cube{
		Name:     "AccessView",
		SQLTable: "default.access",
		Dimensions: map[string]model.Dimension{
			"id": {SQL: "id", Type: "string"},
			"ts": {SQL: "ts", Type: "time"},
			"ip": {SQL: "ip", Type: "string"},
		},
		Measures: map[string]model.Measure{
			"count": {SQL: "count()", Type: "number"},
		},
		Segments: map[string]model.Segment{
			"org":   {SQL: "org = '1'"},
			"black": {SQL: "black = 1"},
		},
	}
}

func TestBuildQuery_DimensionsOnly(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id", "AccessView.ts"},
		Limit:      10,
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got %v", params)
	}

	for _, substr := range []string{`id AS "AccessView.id"`, `ts AS "AccessView.ts"`, "default.access", "LIMIT 10"} {
		if !contains(sql, substr) {
			t.Errorf("expected SQL to contain %q, got: %s", substr, sql)
		}
	}
}

func TestBuildQuery_MeasuresWithGroupBy(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ip"},
		Measures:   []string{"AccessView.count"},
		Limit:      5,
	}

	sql, _, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, substr := range []string{"GROUP BY", "count()", "ip"} {
		if !contains(sql, substr) {
			t.Errorf("expected SQL to contain %q, got: %s", substr, sql)
		}
	}
}

func TestBuildQuery_FilterEquals(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.ip", Operator: "equals", Values: []interface{}{"1.2.3.4"}},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "ip IN (?)") {
		t.Errorf("expected IN clause, got: %s", sql)
	}
	if len(params) != 1 || params[0] != "1.2.3.4" {
		t.Errorf("unexpected params: %v", params)
	}
}

func TestBuildQuery_FilterContains(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.ip", Operator: "contains", Values: []interface{}{"192"}},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "LIKE") {
		t.Errorf("expected LIKE clause, got: %s", sql)
	}
	if len(params) != 1 || params[0] != "%192%" {
		t.Errorf("expected wildcard param, got: %v", params)
	}
}

func TestBuildQuery_FilterSet(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.ip", Operator: "set"},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "notEmpty(ip)") {
		t.Errorf("expected notEmpty(), got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params for 'set' operator, got: %v", params)
	}
}

func TestBuildQuery_OrderBy(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		Order:      OrderMap{"AccessView.ts": "desc"},
	}

	sql, _, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "ORDER BY") || !contains(sql, "DESC") {
		t.Errorf("expected ORDER BY ts DESC, got: %s", sql)
	}
}

func TestBuildQuery_TimeDimensionRange(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: []string{"2024-01-01", "2024-01-31"}},
			},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "ts >= ?") || !contains(sql, "ts <= ?") {
		t.Errorf("expected date range WHERE clause, got: %s", sql)
	}
	if len(params) != 2 {
		t.Errorf("expected 2 date params, got: %v", params)
	}
}

func TestBuildQuery_TimeDimensionRelative(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: "from 15 minutes ago to now"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "now()") {
		t.Errorf("expected ClickHouse now() expr, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no bind params for relative time, got: %v", params)
	}
}

func TestBuildQuery_TimeDimensionThisMonth(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: "this month"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "toStartOfMonth(now())") {
		t.Errorf("expected toStartOfMonth(now()) in SQL, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no bind params, got: %v", params)
	}
}

func TestBuildQuery_TimeDimensionLastMonth(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: "last month"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "toStartOfMonth(now() - INTERVAL 1 MONTH)") {
		t.Errorf("expected toStartOfMonth(now() - INTERVAL 1 MONTH) in SQL, got: %s", sql)
	}
	if !contains(sql, ">=") || !contains(sql, "<=") {
		t.Errorf("expected >= and <= for range, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no bind params, got: %v", params)
	}
}

func TestBuildQuery_Segments(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Segments:   []string{"AccessView.org", "AccessView.black"},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got %v", params)
	}
	if !contains(sql, "org = '1'") {
		t.Errorf("expected org segment SQL, got: %s", sql)
	}
	if !contains(sql, "black = 1") {
		t.Errorf("expected black segment SQL, got: %s", sql)
	}
}

func TestBuildQuery_UnknownFilterSkipped(t *testing.T) {
	// 字段不在模型中，应直接跳过，不拼入 SQL
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.notExist", Operator: "equals", Values: []interface{}{"x"}},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(sql, "notExist") || contains(sql, "AccessView.notExist") {
		t.Errorf("unknown filter field should not appear in SQL, got: %s", sql)
	}
	if contains(sql, "WHERE") {
		t.Errorf("no WHERE clause expected when all filters skipped, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got %v", params)
	}
}

func TestBuildQuery_FilterTagUsesSchemaSQL(t *testing.T) {
	// riskFilterTag 是 array 类型，单值用 has()
	cube := testCube()
	cube.Dimensions["riskFilterTag"] = model.Dimension{
		SQL:  "arrayConcat(req_risk, res_risk)",
		Type: "array",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.riskFilterTag", Operator: "equals", Values: []interface{}{"SQL注入"}},
		},
	}

	sql, params, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "has(arrayConcat(req_risk, res_risk), ?)") {
		t.Errorf("expected has() for array equals, got: %s", sql)
	}
	if len(params) != 1 || params[0] != "SQL注入" {
		t.Errorf("unexpected params: %v", params)
	}
}

func TestBuildQuery_FilterTagMultiValue(t *testing.T) {
	// equals 多值 -> hasAll(arr, [?,?])
	cube := testCube()
	cube.Dimensions["riskFilterTag"] = model.Dimension{
		SQL:  "arrayConcat(req_risk, res_risk)",
		Type: "array",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.riskFilterTag", Operator: "equals", Values: []interface{}{"SQL注入", "XSS"}},
		},
	}

	sql, params, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "hasAll(arrayConcat(req_risk, res_risk), [?,?])") {
		t.Errorf("expected hasAll() for array equals multi-value, got: %s", sql)
	}
	if len(params) != 2 {
		t.Errorf("expected 2 params, got: %v", params)
	}
}

func TestBuildQuery_FilterTagContains(t *testing.T) {
	// contains 多值 -> hasAny(arr, [?,?])
	cube := testCube()
	cube.Dimensions["riskFilterTag"] = model.Dimension{
		SQL:  "arrayConcat(req_risk, res_risk)",
		Type: "array",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.riskFilterTag", Operator: "contains", Values: []interface{}{"SQL注入", "XSS"}},
		},
	}

	sql, params, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "hasAny(arrayConcat(req_risk, res_risk), [?,?])") {
		t.Errorf("expected hasAny() for array contains, got: %s", sql)
	}
	if len(params) != 2 {
		t.Errorf("expected 2 params, got: %v", params)
	}
}

func TestBuildQuery_FilterTagNotEquals(t *testing.T) {
	// notEquals 用 NOT has()
	cube := testCube()
	cube.Dimensions["riskFilterTag"] = model.Dimension{
		SQL:  "arrayConcat(req_risk, res_risk)",
		Type: "array",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.riskFilterTag", Operator: "notEquals", Values: []interface{}{"SQL注入"}},
		},
	}

	sql, params, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "NOT has(arrayConcat(req_risk, res_risk), ?)") {
		t.Errorf("expected NOT has() for array notEquals, got: %s", sql)
	}
	if len(params) != 1 {
		t.Errorf("expected 1 param, got: %v", params)
	}
}

func TestBuildQuery_OrFilter(t *testing.T) {
	// OR 复合条件：两个维度 LIKE 查询，用 OR 连接
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Or: []Filter{
				{Member: "AccessView.ip", Operator: "contains", Values: []interface{}{"192"}},
				{Member: "AccessView.id", Operator: "contains", Values: []interface{}{"192"}},
			}},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "ip LIKE ?") {
		t.Errorf("expected ip LIKE clause in OR, got: %s", sql)
	}
	if !contains(sql, "id LIKE ?") {
		t.Errorf("expected id LIKE clause in OR, got: %s", sql)
	}
	if !contains(sql, " OR ") {
		t.Errorf("expected OR keyword, got: %s", sql)
	}
	// OR 条件应被括号包裹
	if !contains(sql, "(") || !contains(sql, ")") {
		t.Errorf("expected parentheses around OR clause, got: %s", sql)
	}
	if len(params) != 2 {
		t.Errorf("expected 2 params, got: %v", params)
	}
	for _, p := range params {
		if p != "%192%" {
			t.Errorf("expected wildcard param %%192%%, got: %v", p)
		}
	}
}

func TestBuildQuery_OrFilterSkipsUnknown(t *testing.T) {
	// OR 中若某个维度不存在，应跳过，不影响其他子条件
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Or: []Filter{
				{Member: "AccessView.ip", Operator: "contains", Values: []interface{}{"test"}},
				{Member: "AccessView.notExist", Operator: "contains", Values: []interface{}{"test"}},
			}},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(sql, "notExist") {
		t.Errorf("unknown field should not appear in SQL, got: %s", sql)
	}
	// 只有一个有效子条件，不应出现 OR
	if contains(sql, " OR ") {
		t.Errorf("should not have OR when only one valid condition, got: %s", sql)
	}
	if !contains(sql, "ip LIKE ?") {
		t.Errorf("expected ip LIKE clause, got: %s", sql)
	}
	if len(params) != 1 {
		t.Errorf("expected 1 param, got: %v", params)
	}
}

func TestBuildQuery_OrFilterAllUnknown(t *testing.T) {
	// OR 中所有维度都不存在，不应添加任何 WHERE 条件
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Or: []Filter{
				{Member: "AccessView.notExist1", Operator: "contains", Values: []interface{}{"x"}},
				{Member: "AccessView.notExist2", Operator: "contains", Values: []interface{}{"x"}},
			}},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(sql, "WHERE") {
		t.Errorf("no WHERE clause expected when all or-filters skipped, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got %v", params)
	}
}

func TestBuildQuery_OrFilterMutualExclusion(t *testing.T) {
	// or 与普通条件字段不能同时存在，应返回错误
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{
				Member:   "AccessView.ip",
				Operator: "contains",
				Values:   []interface{}{"192"},
				Or: []Filter{
					{Member: "AccessView.id", Operator: "contains", Values: []interface{}{"192"}},
				},
			},
		},
	}

	_, _, err := BuildQuery(req, testCube())
	if err == nil {
		t.Error("expected error when or and member/operator/values are both set")
	}
}

func TestValidateQuery_Valid(t *testing.T) {
	req := &QueryRequest{Dimensions: []string{"AccessView.id"}}
	if err := validateQuery(req); err != nil {
		t.Errorf("unexpected error for valid query: %v", err)
	}
}

func TestValidateQuery_Empty(t *testing.T) {
	req := &QueryRequest{}
	if err := validateQuery(req); err == nil {
		t.Error("expected error for empty query")
	}
}

func TestValidateQuery_NegativeLimit(t *testing.T) {
	req := &QueryRequest{Dimensions: []string{"AccessView.id"}, Limit: -1}
	if err := validateQuery(req); err == nil {
		t.Error("expected error for negative limit")
	}
}

func TestSplitMemberName(t *testing.T) {
	cases := []struct {
		in         string
		wantCube   string
		wantField  string
		wantSubKey string
	}{
		{"AccessView.id", "AccessView", "id", ""},
		{"AccessView.ts", "AccessView", "ts", ""},
		{"id", "id", "", ""},
		{"AccessView.customData.UserToken", "AccessView", "customData", "UserToken"},
	}
	for _, c := range cases {
		cube, field, subKey := splitMemberName(c.in)
		if cube != c.wantCube || field != c.wantField || subKey != c.wantSubKey {
			t.Errorf("splitMemberName(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.in, cube, field, subKey, c.wantCube, c.wantField, c.wantSubKey)
		}
	}
}

func TestParseRelativeTimeRange(t *testing.T) {
	cases := []struct {
		input       string
		wantStart   string
		wantEnd     string
		wantIsRange bool
	}{
		{"from 15 minutes ago to now", "now() - INTERVAL 15 MINUTE", "now()", true},
		{"from 1 hour ago to now", "now() - INTERVAL 1 HOUR", "now()", true},
		{"from 7 days ago to now", "now() - INTERVAL 7 DAY", "now()", true},
		{"today", "", "", false},
	}
	for _, c := range cases {
		start, end, isRange := parseRelativeTimeRange(c.input)
		if isRange != c.wantIsRange {
			t.Errorf("parseRelativeTimeRange(%q) isRange=%v, want %v", c.input, isRange, c.wantIsRange)
			continue
		}
		if isRange {
			if start != c.wantStart {
				t.Errorf("parseRelativeTimeRange(%q) start=%q, want %q", c.input, start, c.wantStart)
			}
			if end != c.wantEnd {
				t.Errorf("parseRelativeTimeRange(%q) end=%q, want %q", c.input, end, c.wantEnd)
			}
		}
	}
}

func TestConvertToClickHouseTimeExpr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"now", "now()"},
		{"today", "today()"},
		{"yesterday", "yesterday()"},
		{"15 minutes ago", "now() - INTERVAL 15 MINUTE"},
		{"1 hour ago", "now() - INTERVAL 1 HOUR"},
		{"15 minutes from now", "now() + INTERVAL 15 MINUTE"},
	}
	for _, c := range cases {
		if got := convertToClickHouseTimeExpr(c.in); got != c.want {
			t.Errorf("convertToClickHouseTimeExpr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildQuery_CustomDataSubKey(t *testing.T) {
	cube := testCube()
	cube.Dimensions["customData"] = model.Dimension{
		SQL:  "data[indexOf(key, '{key}')]",
		Type: "string",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.customData.UserToken"},
	}

	sql, params, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, `data[indexOf(key, 'UserToken')]`) {
		t.Errorf("expected subKey substitution in SQL, got: %s", sql)
	}
	if !contains(sql, `"AccessView.customData.UserToken"`) {
		t.Errorf("expected full alias in SQL, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got %v", params)
	}
}

func TestBuildQuery_CustomDataSubKeyFilter(t *testing.T) {
	cube := testCube()
	cube.Dimensions["customData"] = model.Dimension{
		SQL:  "data[indexOf(key, '{key}')]",
		Type: "string",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Filters: []Filter{
			{Member: "AccessView.customData.UserToken", Operator: "equals", Values: []interface{}{"abc"}},
		},
	}

	sql, params, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, `data[indexOf(key, 'UserToken')] IN (?)`) {
		t.Errorf("expected filter with subKey substitution, got: %s", sql)
	}
	if len(params) != 1 || params[0] != "abc" {
		t.Errorf("unexpected params: %v", params)
	}
}

func TestBuildQuery_CustomDataSubKeyOrderBy(t *testing.T) {
	cube := testCube()
	cube.Dimensions["customData"] = model.Dimension{
		SQL:  "data[indexOf(key, '{key}')]",
		Type: "string",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.customData.UserToken"},
		Order:      OrderMap{"AccessView.customData.UserToken": "desc"},
	}

	sql, _, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, `data[indexOf(key, 'UserToken')] DESC`) {
		t.Errorf("expected subKey substitution in ORDER BY, got: %s", sql)
	}
}

func TestBuildQuery_CustomDataSubKeyGroupBy(t *testing.T) {
	cube := testCube()
	cube.Dimensions["customData"] = model.Dimension{
		SQL:  "data[indexOf(key, '{key}')]",
		Type: "string",
	}
	req := &QueryRequest{
		Dimensions: []string{"AccessView.customData.UserToken"},
		Measures:   []string{"AccessView.count"},
	}

	sql, _, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// GROUP BY 应出现两次（SELECT 和 GROUP BY 各一次）
	if !contains(sql, "GROUP BY") {
		t.Errorf("expected GROUP BY clause, got: %s", sql)
	}
	if !contains(sql, `data[indexOf(key, 'UserToken')]`) {
		t.Errorf("expected subKey substitution in GROUP BY, got: %s", sql)
	}
}

// TestHandleLoad_PostWrapped 验证 POST {"query":{...},"queryType":"multi"} 的 unwrap 逻辑。
func TestHandleLoad_PostWrapped(t *testing.T) {
	defer func() { recover() }() // chClient=nil 会 panic，属预期，忽略
	body := `{"query":{"dimensions":["AccessView.id"],"limit":1},"queryType":"multi"}`
	req := httptest.NewRequest(http.MethodPost, "/load", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h := &Handler{modelLoader: model.NewLoader(model.InternalFS), chClient: nil}
	h.HandleLoad(rr, req)

	if rr.Code == http.StatusBadRequest {
		t.Errorf("wrapped POST body should parse OK, got 400: %s", rr.Body.String())
	}
}

// TestHandleLoad_GetQuery 验证 GET ?query=... 格式正常解析。
func TestHandleLoad_GetQuery(t *testing.T) {
	defer func() { recover() }()
	q := url.QueryEscape(`{"dimensions":["AccessView.id"],"limit":1}`)
	req := httptest.NewRequest(http.MethodGet, "/load?query="+q, nil)
	rr := httptest.NewRecorder()

	h := &Handler{modelLoader: model.NewLoader(model.InternalFS), chClient: nil}
	h.HandleLoad(rr, req)

	if rr.Code == http.StatusBadRequest {
		t.Errorf("GET ?query= should parse OK, got 400: %s", rr.Body.String())
	}
}

// TestBuildQuery_MeasureFilterGoesToHaving verifies that a filter on a measure
// field is placed in HAVING (not WHERE), so ClickHouse won't reject it with
// "aggregate function found in WHERE".
func TestBuildQuery_MeasureFilterGoesToHaving(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ip"},
		Measures:   []string{"AccessView.count"},
		Filters: []Filter{
			// measure filter → HAVING
			{Member: "AccessView.count", Operator: "gte", Values: []interface{}{"5"}},
			// dimension filter → WHERE
			{Member: "AccessView.ip", Operator: "equals", Values: []interface{}{"1.2.3.4"}},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// WHERE must NOT contain the aggregate filter
	whereIdx := strings.Index(sql, "WHERE")
	havingIdx := strings.Index(sql, "HAVING")
	if whereIdx < 0 {
		t.Fatalf("expected WHERE clause, got: %s", sql)
	}
	if havingIdx < 0 {
		t.Fatalf("expected HAVING clause, got: %s", sql)
	}

	// dimension filter in WHERE, before HAVING
	if !contains(sql[:havingIdx], "ip IN (?)") {
		t.Errorf("expected dimension filter in WHERE section, got: %s", sql)
	}
	// measure filter in HAVING, after GROUP BY
	if !contains(sql[havingIdx:], "count() >= ?") {
		t.Errorf("expected measure filter in HAVING section, got: %s", sql)
	}

	if len(params) != 2 {
		t.Errorf("expected 2 params, got %v", params)
	}
}

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func testPreAggCube() *model.Cube {
	return &model.Cube{
		Name: "ApiView",
		SQL: `SELECT org, url, method, app_name,
       max(ts) as max_ts
FROM (
  SELECT org, ts, url, method
  FROM default.access_sample_raw
  WHERE 1=1 {time_filter}
) AS a
GROUP BY org, app_name, method, url`,
		Dimensions: map[string]model.Dimension{
			"ts":      {SQL: "if(max_ts=toDateTime(0), now(), max_ts)", Type: "time"},
			"appName": {SQL: "app_name", Type: "string"},
			"url":     {SQL: "url", Type: "string"},
		},
		Measures: map[string]model.Measure{
			"allCount": {SQL: "uniqExact(method, app_name, url)", Type: "number"},
		},
		Segments: map[string]model.Segment{
			"org": {SQL: "org = ''"},
		},
		PreAggregationFilters: []model.PreAggregationFilter{
			{Dimension: "ts", TargetColumn: "ts", Placeholder: "time_filter"},
		},
	}
}

func TestPreAgg_AbsoluteTimeRange(t *testing.T) {
	// 绝对时间应下推到子查询，外层 WHERE 不应出现时间条件
	req := &QueryRequest{
		Dimensions: []string{"ApiView.appName"},
		Measures:   []string{"ApiView.allCount"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "ApiView.ts",
				DateRange: DateRange{V: []string{"2026-03-20T00:00:00.000", "2026-03-20T23:59:59.000"}},
			},
		},
	}

	sql, params, err := BuildQuery(req, testPreAggCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 子查询内应包含下推的时间条件
	if !contains(sql, "ts >= '2026-03-20 00:00:00'") {
		t.Errorf("expected pushdown start time in subquery, got: %s", sql)
	}
	if !contains(sql, "ts <= '2026-03-20 23:59:59'") {
		t.Errorf("expected pushdown end time in subquery, got: %s", sql)
	}
	// 占位符应被替换掉
	if contains(sql, "{time_filter}") {
		t.Errorf("placeholder should be replaced, got: %s", sql)
	}
	// 外层不应有 >= ? 绑定参数的时间条件（已下推）
	outerIdx := strings.Index(sql, ") AS ApiView")
	if outerIdx > 0 {
		outerSQL := sql[outerIdx:]
		if contains(outerSQL, ">= ?") || contains(outerSQL, "<= ?") {
			t.Errorf("pushed-down dimension should not appear in outer WHERE, got: %s", outerSQL)
		}
	}
	// 不应有绑定参数（下推时间内联，外层时间被跳过）
	if len(params) != 0 {
		t.Errorf("expected no bind params (all inlined), got: %v", params)
	}
}

func TestPreAgg_RelativeTimeRange(t *testing.T) {
	// "from 7 days ago to now" 应下推为 ClickHouse 表达式
	req := &QueryRequest{
		Dimensions: []string{"ApiView.appName"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "ApiView.ts",
				DateRange: DateRange{V: "from 7 days ago to now"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testPreAggCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(sql, "ts >= now() - INTERVAL 7 DAY") {
		t.Errorf("expected relative start in subquery, got: %s", sql)
	}
	if !contains(sql, "ts <= now()") {
		t.Errorf("expected relative end in subquery, got: %s", sql)
	}
	if contains(sql, "{time_filter}") {
		t.Errorf("placeholder should be replaced, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestPreAgg_Today(t *testing.T) {
	// "today" 应下推为 toDate(ts) = today()
	req := &QueryRequest{
		Dimensions: []string{"ApiView.appName"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "ApiView.ts",
				DateRange: DateRange{V: "today"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testPreAggCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(sql, "toDate(ts) = today()") {
		t.Errorf("expected toDate(ts) = today() in subquery, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestPreAgg_ThisMonth(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"ApiView.appName"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "ApiView.ts",
				DateRange: DateRange{V: "this month"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testPreAggCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(sql, "ts >= toStartOfMonth(now())") {
		t.Errorf("expected this month start in subquery, got: %s", sql)
	}
	if !contains(sql, "ts <= toStartOfMonth(now() + INTERVAL 1 MONTH)") {
		t.Errorf("expected this month end in subquery, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestPreAgg_LastMonth(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"ApiView.appName"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "ApiView.ts",
				DateRange: DateRange{V: "last month"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testPreAggCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(sql, "ts >= toStartOfMonth(now() - INTERVAL 1 MONTH)") {
		t.Errorf("expected last month start in subquery, got: %s", sql)
	}
	if !contains(sql, "ts <= toStartOfMonth(now())") {
		t.Errorf("expected last month end in subquery, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestPreAgg_NoDateRange(t *testing.T) {
	// 不传 dateRange → 占位符清除，不加任何时间过滤
	req := &QueryRequest{
		Dimensions: []string{"ApiView.appName"},
		TimeDimensions: []TimeDimension{
			{Dimension: "ApiView.ts"},
		},
	}

	sql, params, err := BuildQuery(req, testPreAggCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if contains(sql, "{time_filter}") {
		t.Errorf("placeholder should be removed, got: %s", sql)
	}
	// WHERE 1=1 后面不应跟任何 AND
	if contains(sql, "WHERE 1=1 AND") {
		t.Errorf("should not have AND after WHERE 1=1 when no dateRange, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestPreAgg_NoCube_FallsBackToOuterWhere(t *testing.T) {
	// sql_table 类型的 cube（无 PreAggregationFilters）
	// 时间条件应出现在外层 WHERE，使用 ? 绑定
	cube := testCube() // 无 PreAggregationFilters
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: []string{"2024-01-01", "2024-01-31"}},
			},
		},
	}

	sql, params, err := BuildQuery(req, cube)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(sql, "ts >= ?") || !contains(sql, "ts <= ?") {
		t.Errorf("expected ? binding in outer WHERE for sql_table cube, got: %s", sql)
	}
	if len(params) != 2 {
		t.Errorf("expected 2 params, got: %v", params)
	}
}

func TestPreAgg_PushedDownSkipsOuterWhere(t *testing.T) {
	// 验证下推后外层 WHERE 确实没有时间条件
	req := &QueryRequest{
		Dimensions: []string{"ApiView.appName"},
		Measures:   []string{"ApiView.allCount"},
		Segments:   []string{"ApiView.org"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "ApiView.ts",
				DateRange: DateRange{V: []string{"2026-01-01T00:00:00.000", "2026-01-31T23:59:59.000"}},
			},
		},
	}

	sql, _, err := BuildQuery(req, testPreAggCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 找到外层 WHERE（在 AS ApiView 之后）
	outerStart := strings.Index(sql, "AS ApiView")
	if outerStart < 0 {
		t.Fatalf("expected 'AS ApiView' in SQL, got: %s", sql)
	}
	outerSQL := sql[outerStart:]

	// 外层 WHERE 应只有 segment 条件，没有时间条件
	if contains(outerSQL, "'2026-01-01") || contains(outerSQL, "'2026-01-31") {
		t.Errorf("outer WHERE should not contain pushed-down time literals, got: %s", outerSQL)
	}
	if !contains(outerSQL, "org = ''") {
		t.Errorf("outer WHERE should still contain segment, got: %s", outerSQL)
	}
}

func TestConvertToClickHouseTimeExpr_AbsoluteTime(t *testing.T) {
	cases := []struct{ in, want string }{
		// 带 T 和毫秒
		{"2026-03-20T00:00:00.000", "'2026-03-20 00:00:00'"},
		{"2026-03-20T23:59:59.999", "'2026-03-20 23:59:59'"},
		// 带 T 无毫秒
		{"2026-03-20T12:30:00", "'2026-03-20 12:30:00'"},
		// 已经是标准格式
		{"2026-03-20 00:00:00", "'2026-03-20 00:00:00'"},
		// 纯日期
		{"2024-01-01", "'2024-01-01'"},
	}
	for _, c := range cases {
		if got := convertToClickHouseTimeExpr(c.in); got != c.want {
			t.Errorf("convertToClickHouseTimeExpr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeDateTime(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-03-20T00:00:00.000", "2026-03-20 00:00:00"},
		{"2026-03-20T23:59:59.999", "2026-03-20 23:59:59"},
		{"2026-03-20T12:30:00", "2026-03-20 12:30:00"},
		{"2026-03-20 00:00:00", "2026-03-20 00:00:00"},
		{"2024-01-01", "2024-01-01"},
	}
	for _, c := range cases {
		if got := normalizeDateTime(c.in); got != c.want {
			t.Errorf("normalizeDateTime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildQuery_Granularity(t *testing.T) {
	req := &QueryRequest{
		Measures: []string{"AccessView.count"},
		TimeDimensions: []TimeDimension{
			{
				Dimension:   "AccessView.ts",
				DateRange:   DateRange{V: "from 7 days ago to now"},
				Granularity: "day",
			},
		},
	}

	sql, _, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SELECT 中应有 toStartOfDay(ts) AS "AccessView.ts.day"
	if !contains(sql, `toStartOfDay(ts) AS "AccessView.ts.day"`) {
		t.Errorf("expected granularity column in SELECT, got: %s", sql)
	}
	// GROUP BY 中应引用该别名
	if !contains(sql, `"AccessView.ts.day"`) {
		t.Errorf("expected granularity alias in GROUP BY, got: %s", sql)
	}
}

func TestBuildQuery_GranularityOrderBy(t *testing.T) {
	// ORDER BY 中 member 为 "AccessView.ts"，应匹配到 granularity alias "AccessView.ts.day"
	req := &QueryRequest{
		Measures: []string{"AccessView.count"},
		TimeDimensions: []TimeDimension{
			{
				Dimension:   "AccessView.ts",
				DateRange:   DateRange{V: "from 7 days ago to now"},
				Granularity: "day",
			},
		},
		Order: OrderMap{"AccessView.ts": "desc"},
	}

	sql, _, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(sql, `"AccessView.ts.day" DESC`) {
		t.Errorf("expected granularity alias in ORDER BY, got: %s", sql)
	}
}

func TestBuildQuery_TimeDimensionToday(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: "today"},
			},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "toDate(ts) = today()") {
		t.Errorf("expected toDate(ts) = today(), got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestBuildQuery_TimeDimensionNilDateRange(t *testing.T) {
	// dateRange 为 nil，不应生成任何时间条件
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{Dimension: "AccessView.ts"},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(sql, "WHERE") {
		t.Errorf("should not have WHERE when dateRange is nil, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestBuildQuery_EmptyStringDateRange(t *testing.T) {
	// dateRange 为空字符串，不应生成时间条件
	req := &QueryRequest{
		Dimensions: []string{"AccessView.ts"},
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: ""},
			},
		},
	}

	sql, params, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(sql, "WHERE") {
		t.Errorf("should not have WHERE for empty dateRange, got: %s", sql)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got: %v", params)
	}
}

func TestBuildQuery_OffsetOnly(t *testing.T) {
	req := &QueryRequest{
		Dimensions: []string{"AccessView.id"},
		Limit:      10,
		Offset:     20,
	}

	sql, _, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(sql, "LIMIT 10") {
		t.Errorf("expected LIMIT 10, got: %s", sql)
	}
	if !contains(sql, "OFFSET 20") {
		t.Errorf("expected OFFSET 20, got: %s", sql)
	}
}

func TestBuildQuery_NoDimensionsNoMeasures_SelectsOne(t *testing.T) {
	// 只有 timeDimension，没有显式 dimension/measure
	req := &QueryRequest{
		TimeDimensions: []TimeDimension{
			{
				Dimension: "AccessView.ts",
				DateRange: DateRange{V: "today"},
			},
		},
	}

	sql, _, err := BuildQuery(req, testCube())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(sql, "SELECT 1 FROM") {
		t.Errorf("expected SELECT 1 when no dimensions/measures, got: %s", sql)
	}
}
