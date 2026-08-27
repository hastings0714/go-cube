package api

import (
	"strings"
	"testing"

	"github.com/Servicewall/go-cube/model"
)

func TestAccountAssetQueries(t *testing.T) {
	loader, err := model.NewLoaderFromFS(model.InternalFS)
	if err != nil {
		t.Fatal(err)
	}
	cube, err := loader.Load("AccountAssetView")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*QueryRequest{
		{Measures: []string{"AccountAssetView.assetCount", "AccountAssetView.sensitiveCount"}},
		{Dimensions: []string{"AccountAssetView.appId", "AccountAssetView.application"}, Measures: []string{"AccountAssetView.assetCount"}},
		{Dimensions: []string{"AccountAssetView.appId", "AccountAssetView.content", "AccountAssetView.employeeName", "AccountAssetView.hasSensitive"}, Measures: []string{"AccountAssetView.count", "AccountAssetView.reqSensScoreTuple", "AccountAssetView.firstTs"}, Limit: 20, Offset: 1000},
	} {
		query, err := buildQuery(request, cube)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{"host_dict", "account_asset", "account_day", "GROUP BY app_id, content"} {
			if !strings.Contains(query, fragment) {
				t.Errorf("missing %s", fragment)
			}
		}
		if strings.Contains(query, "approx_top_sum") || strings.Contains(query, "default.audit") {
			t.Fatal("account queries must not depend on audit's bounded asset set")
		}
		if request.Offset > 0 && !strings.Contains(query, "OFFSET 1000") {
			t.Fatal("missing pagination offset")
		}
	}
	if cube.Measures["assetCount"].SQL != "count()" {
		t.Fatal("asset count must be exact after application/account grouping")
	}
}
