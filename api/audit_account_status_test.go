package api

import (
	"strings"
	"testing"

	"github.com/Servicewall/go-cube/model"
)

func TestAuditAccountStatusQueries(t *testing.T) {
	loader, err := model.NewLoaderFromFS(model.InternalFS)
	if err != nil {
		t.Fatal(err)
	}
	cube, err := loader.Load("AuditView")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"new", "active"} {
		query, err := buildQuery(&QueryRequest{
			Dimensions: []string{"AuditView.channel", "AuditView.content"},
			Measures:   []string{"AuditView.accountStatus", "AuditView.count"},
			Filters: []Filter{
				{Member: "AuditView.type", Operator: "equals", Values: []interface{}{"User"}},
				{Member: "AuditView.accountStatus", Operator: "equals", Values: []interface{}{status}},
			},
			Limit: 20, Offset: 40,
		}, cube)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{"HAVING", "min(if(first_ts", "INTERVAL 7 DAY", "OFFSET 40", "'" + status + "'"} {
			if !strings.Contains(query, fragment) {
				t.Fatalf("missing %q: %s", fragment, query)
			}
		}
	}
	query, err := buildQuery(&QueryRequest{Measures: []string{
		"AuditView.accountAssetCount", "AuditView.newAccountAssetCount", "AuditView.activeAccountAssetCount",
	}}, cube)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "HAVING") || strings.Contains(query, "approx_top_sum") {
		t.Fatalf("status counts must not filter the entire aggregate or use Top300: %s", query)
	}
	for _, name := range []string{"newAccountAssetCount", "activeAccountAssetCount", "accountStatus"} {
		if !strings.Contains(cube.Measures[name].SQL, "if(first_ts = toDateTime(0), toDateTime(dt), first_ts)") {
			t.Fatalf("%s must use the same legacy timestamp fallback as firstTs", name)
		}
	}
}
