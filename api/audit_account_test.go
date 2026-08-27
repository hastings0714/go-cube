package api

import (
	"strings"
	"testing"

	"github.com/Servicewall/go-cube/model"
)

func TestAuditAccountCounts(t *testing.T) {
	loader, err := model.NewLoaderFromFS(model.InternalFS)
	if err != nil {
		t.Fatal(err)
	}
	cube, err := loader.Load("AuditView")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*QueryRequest{
		{Measures: []string{"AuditView.accountAssetCount", "AuditView.sensitiveAccountAssetCount"}},
		{Dimensions: []string{"AuditView.channel"}, Measures: []string{"AuditView.accountAssetCount"}, Limit: 1000, Offset: 1000},
		{Measures: []string{"AuditView.sensitiveAccountAssetCount"}, Filters: []Filter{{Member: "AuditView.employeeName", Operator: "contains", Values: []interface{}{"张"}}}},
	} {
		query, err := buildQuery(req, cube)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{"default.audit", "uniqExactIf(tuple(channel, content)"} {
			if !strings.Contains(query, fragment) {
				t.Fatalf("missing %q: %s", fragment, query)
			}
		}
		if strings.Contains(query, "approx_top_sum") || strings.Contains(query, "HAVING") {
			t.Fatalf("counts must not use Top300 or aggregate-level sensitive filtering: %s", query)
		}
		if req.Offset > 0 && !strings.Contains(query, "OFFSET 1000") {
			t.Fatal(query)
		}
	}
	if cube.Measures["accountAssetUniq"].SQL != "uniqIf(content, channel, type = 'User')" {
		t.Fatal("existing approximate measure must remain unchanged")
	}
}
