package bing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/providertest"
)

func TestContract(t *testing.T) {
	providertest.RunContract(t, New())
}

type memHandle struct{ bytes []byte }

func (h memHandle) Material() core.SecretMaterial {
	return core.SecretMaterial{Type: "api_key", Bytes: h.bytes}
}
func (memHandle) Close() {}

type memSink struct {
	writes map[string][]map[string]any
}

func newMemSink() *memSink { return &memSink{writes: map[string][]map[string]any{}} }

func (s *memSink) Write(_ context.Context, dataset string, rows []map[string]any) error {
	s.writes[dataset] = append(s.writes[dataset], rows...)
	return nil
}
func (s *memSink) Checkpoint(context.Context, string) error { return nil }

func wcfDate(t time.Time) string {
	return fmt.Sprintf("/Date(%d)/", t.UnixMilli())
}

func fakeAPI(t *testing.T, handler func(method string, query map[string][]string) (any, int)) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/")
		body, status := handler(method, r.URL.Query())
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"d": body})
	}))
	t.Cleanup(server.Close)
	return New(WithEndpoint(server.URL+"/"), WithHTTPClient(server.Client()))
}

func syncRequest(capability core.Capability) core.SyncRequest {
	return core.SyncRequest{
		Site:       core.Site{ID: "default", PublicURL: "https://example.test"},
		Property:   core.Property{Reference: "https://example.test"},
		Capability: capability,
		StartDate:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
	}
}

func TestSyncSearchPerformance(t *testing.T) {
	inRange := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	outOfRange := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	provider := fakeAPI(t, func(method string, query map[string][]string) (any, int) {
		if got := query["apikey"]; len(got) != 1 || got[0] != "test-key" {
			return nil, http.StatusUnauthorized
		}
		switch method {
		case "GetRankAndTrafficStats":
			return []map[string]any{
				{"Date": wcfDate(inRange), "Clicks": 5, "Impressions": 100},
				{"Date": wcfDate(outOfRange), "Clicks": 9, "Impressions": 900},
			}, http.StatusOK
		case "GetQueryStats":
			return []map[string]any{{"Date": wcfDate(inRange), "Query": "renovation", "Clicks": 2, "Impressions": 40, "AvgImpressionPosition": 3.5}}, http.StatusOK
		case "GetPageStats":
			return []map[string]any{{"Date": wcfDate(inRange), "Query": "/pricing", "Clicks": 1, "Impressions": 10, "AvgImpressionPosition": 2}}, http.StatusOK
		}
		return nil, http.StatusNotFound
	})

	sink := newMemSink()
	result, err := provider.Sync(context.Background(), syncRequest(core.CapSearchPerformance), memHandle{[]byte("test-key")}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 3 {
		t.Errorf("rows = %d, want 3 (out-of-range row must be filtered)", result.Rows)
	}
	if got := result.DataThroughDate; !got.Equal(inRange) {
		t.Errorf("data through = %v, want %v", got, inRange)
	}
	if len(sink.writes[datasetSearchDaily]) != 1 || len(sink.writes[datasetSearchDetails]) != 2 {
		t.Errorf("unexpected sink writes: %+v", sink.writes)
	}
	for _, row := range sink.writes[datasetSearchDetails] {
		if row["_key"] == "" {
			t.Error("detail row missing _key")
		}
	}
}

func TestSyncCrawlStatsAndSitemaps(t *testing.T) {
	day := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	provider := fakeAPI(t, func(method string, _ map[string][]string) (any, int) {
		switch method {
		case "GetCrawlStats":
			return []map[string]any{{"Date": wcfDate(day), "CrawledPages": 120, "InIndex": 100, "Code2xx": 118}}, http.StatusOK
		case "GetFeeds":
			return []map[string]any{{"Url": "https://example.test/sitemap.xml", "Type": "Sitemap", "Status": "Success", "UrlCount": 42, "Submitted": wcfDate(day), "LastCrawled": wcfDate(day)}}, http.StatusOK
		}
		return nil, http.StatusNotFound
	})

	sink := newMemSink()
	if _, err := provider.Sync(context.Background(), syncRequest(core.CapCrawlStats), memHandle{[]byte("k")}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.writes[datasetCrawlDaily]) != 1 {
		t.Errorf("crawl rows: %+v", sink.writes)
	}
	if _, err := provider.Sync(context.Background(), syncRequest(core.CapSitemaps), memHandle{[]byte("k")}, sink); err != nil {
		t.Fatal(err)
	}
	rows := sink.writes[datasetSitemaps]
	if len(rows) != 1 || rows[0]["pending"] != false {
		t.Errorf("sitemap rows: %+v", rows)
	}
}

func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   core.ErrorCode
	}{
		{http.StatusUnauthorized, core.ErrUnauthorized},
		{http.StatusForbidden, core.ErrUnauthorized},
	} {
		provider := fakeAPI(t, func(string, map[string][]string) (any, int) { return nil, tc.status })
		_, err := provider.Sync(context.Background(), syncRequest(core.CapSearchPerformance), memHandle{[]byte("k")}, newMemSink())
		var classified *core.SyncError
		if !asSyncError(err, &classified) || classified.Code != tc.want {
			t.Errorf("HTTP %d classified as %v, want %s", tc.status, err, tc.want)
		}
		if classified != nil && strings.Contains(classified.Message, "http") {
			t.Errorf("classification leaks transport detail: %q", classified.Message)
		}
	}
}

func TestRetryOnServerError(t *testing.T) {
	var calls int
	day := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	provider := fakeAPI(t, func(method string, _ map[string][]string) (any, int) {
		if method == "GetCrawlStats" {
			calls++
			if calls == 1 {
				return nil, http.StatusInternalServerError
			}
			return []map[string]any{{"Date": wcfDate(day), "CrawledPages": 1}}, http.StatusOK
		}
		return nil, http.StatusNotFound
	})
	if _, err := provider.Sync(context.Background(), syncRequest(core.CapCrawlStats), memHandle{[]byte("k")}, newMemSink()); err != nil {
		t.Fatalf("expected retry to recover: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestUnsupportedCapability(t *testing.T) {
	provider := New()
	_, err := provider.Sync(context.Background(), syncRequest(core.CapConversion), memHandle{[]byte("k")}, newMemSink())
	var classified *core.SyncError
	if !asSyncError(err, &classified) || classified.Code != core.ErrUnsupported {
		t.Errorf("want UNSUPPORTED, got %v", err)
	}
}

func TestDiscoverProperties(t *testing.T) {
	provider := fakeAPI(t, func(method string, _ map[string][]string) (any, int) {
		if method == "GetUserSites" {
			return []map[string]any{{"Url": "https://example.test"}}, http.StatusOK
		}
		return nil, http.StatusNotFound
	})
	properties, err := provider.DiscoverProperties(context.Background(), memHandle{[]byte("k")})
	if err != nil {
		t.Fatal(err)
	}
	if len(properties) != 1 || properties[0].Reference != "https://example.test" {
		t.Errorf("properties = %+v", properties)
	}
}

func asSyncError(err error, target **core.SyncError) bool {
	if err == nil {
		return false
	}
	se, ok := err.(*core.SyncError)
	if ok {
		*target = se
	}
	return ok
}
