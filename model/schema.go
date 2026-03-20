package model

import "strings"

type Cube struct {
	Name                  string                 `yaml:"name"`
	SQL                   string                 `yaml:"sql"`
	SQLTable              string                 `yaml:"sql_table"`
	Dimensions            map[string]Dimension   `yaml:"dimensions"`
	Measures              map[string]Measure     `yaml:"measures"`
	Segments              map[string]Segment     `yaml:"segments,omitempty"`
	PreAggregationFilters []PreAggregationFilter `yaml:"pre_aggregation_filters,omitempty"`
}

// PreAggregationFilter 将外层 timeDimension 的过滤条件下推到子查询内部。
//
// 工作原理：
//
//	BuildQuery 遍历 req.TimeDimensions，如果某个维度的 fieldName
//	匹配 Dimension，就会用 TargetColumn 在子查询中生成 WHERE 条件，
//	注入到 SQL 模板的 {Placeholder} 位置。
//
// 示例 YAML:
//
//	pre_aggregation_filters:
//	  - dimension: ts
//	    target_column: ts
//	    placeholder: time_filter
//
// 对应 SQL 模板中写:
//
//	SELECT ... FROM default.access_sample_raw WHERE 1=1 {time_filter}
type PreAggregationFilter struct {
	Dimension    string `yaml:"dimension"`     // 匹配的维度字段名，如 "ts"
	TargetColumn string `yaml:"target_column"` // 子查询内实际过滤的列名，如 "ts"
	Placeholder  string `yaml:"placeholder"`   // SQL 模板中的占位符名，如 "time_filter"
}

type Dimension struct {
	SQL        string `yaml:"sql"`
	Type       string `yaml:"type"`
	Title      string `yaml:"title,omitempty"`
	PrimaryKey bool   `yaml:"primary_key,omitempty"`
}

type Measure struct {
	SQL   string `yaml:"sql"`
	Type  string `yaml:"type"`
	Title string `yaml:"title,omitempty"`
}

type Segment struct {
	SQL string `yaml:"sql"`
}

// GetField 查找维度或度量字段，subKey 非空时将 SQL 模板中的 {key} 替换为 subKey。
func (c *Cube) GetField(name string, subKey string) (Field, bool) {
	if dim, ok := c.Dimensions[name]; ok {
		sql := dim.SQL
		if subKey != "" {
			sql = strings.ReplaceAll(sql, "{key}", subKey)
		}
		return Field{
			Name: name,
			SQL:  sql,
			Type: dim.Type,
		}, true
	}

	if measure, ok := c.Measures[name]; ok {
		return Field{
			Name: name,
			SQL:  measure.SQL,
			Type: measure.Type,
		}, true
	}

	return Field{}, false
}

// GetSQLTable 返回 cube 的 FROM 子句。
// 注意：返回的 SQL 可能包含 {placeholder} 占位符，
// 需要由 BuildQuery 中的 applyPreAggFilters 进行替换。
func (c *Cube) GetSQLTable() string {
	if c.SQLTable != "" {
		return c.SQLTable
	}
	// 对于复杂子查询，需要添加别名
	if c.SQL != "" {
		return "(" + c.SQL + ") AS " + c.Name
	}
	return ""
}

type Field struct {
	Name string
	SQL  string
	Type string
}
