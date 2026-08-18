package searchconsole

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/providertest"
	"google.golang.org/api/googleapi"
	searchconsole "google.golang.org/api/searchconsole/v1"
)

func TestContract(t *testing.T) {
	providertest.RunContract(t, New())
}

type memHandle struct{}

func (memHandle) Material() core.SecretMaterial {
	return core.SecretMaterial{Type: "service_account_json", Bytes: []byte("{}")}
}
func (memHandle) Close() {}

type memSink struct{ writes map[string][]map[string]any }

func newMemSink() *memSink { return &memSink{writes: map[string][]map[string]any{}} }

func (s *memSink) Write(_ context.Context, dataset string, rows []map[string]any) error {
	s.writes[dataset] = append(s.writes[dataset], rows...)
	return nil
}
func (s *memSink) Checkpoint(context.Context, string) error { return nil }

// fakeService scripts Query responses keyed by dimension shape.
type fakeService struct {
	daily      []*searchconsole.ApiDataRow
	details    map[string][]*searchconsole.ApiDataRow // keyed by start date
	err        error
	queryCalls int
}

func (f *fakeService) Query(_ context.Context, _ string, request *searchconsole.SearchAnalyticsQueryRequest) (*searchconsole.SearchAnalyticsQueryResponse, error) {
	f.queryCalls++
	if f.err != nil {
		return nil, f.err
	}
	if len(request.Dimensions) == 1 {
		return &searchconsole.SearchAnalyticsQueryResponse{Rows: f.daily}, nil
	}
	return &searchconsole.SearchAnalyticsQueryResponse{Rows: f.details[request.StartDate]}, nil
}

func (f *fakeService) ListSites(context.Context) ([]*searchconsole.WmxSite, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []*searchconsole.WmxSite{{SiteUrl: "sc-domain:example.test"}}, nil
}

func withFake(fake *fakeService) *Provider {
	return &Provider{newService: func(context.Context, core.CredentialHandle) (service, error) {
		return fake, nil
	}}
}

func request() core.SyncRequest {
	return core.SyncRequest{
		Site:       core.Site{ID: "default", PublicURL: "https://example.test"},
		Property:   core.Property{Reference: "sc-domain:example.test"},
		Capability: core.CapSearchPerformance,
		StartDate:  time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
	}
}

func TestSyncWritesDailyAndDetails(t *testing.T) {
	fake := &fakeService{
		daily: []*searchconsole.ApiDataRow{
			{Keys: []string{"2026-08-10"}, Clicks: 12, Impressions: 300, Position: 4.2},
		},
		details: map[string][]*searchconsole.ApiDataRow{
			"2026-08-10": {{Keys: []string{"2026-08-10", "renovation", "https://example.test/", "TWN", "MOBILE"}, Clicks: 2, Impressions: 50, Position: 3}},
			"2026-08-11": {},
		},
	}
	sink := newMemSink()
	result, err := withFake(fake).Sync(context.Background(), request(), memHandle{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 2 {
		t.Errorf("rows = %d, want 2", result.Rows)
	}
	daily := sink.writes[datasetSearchDaily]
	if len(daily) != 1 || daily[0]["_key"] != "2026-08-10" {
		t.Errorf("daily rows: %+v", daily)
	}
	details := sink.writes[datasetSearchDetails]
	if len(details) != 1 {
		t.Fatalf("detail rows: %+v", details)
	}
	if details[0]["country"] != "twn" || details[0]["device"] != "mobile" {
		t.Errorf("dimensions not normalized: %+v", details[0])
	}
}

func TestSyncClassifiesGoogleErrors(t *testing.T) {
	for _, tc := range []struct {
		code int
		want core.ErrorCode
	}{
		{http.StatusForbidden, core.ErrUnauthorized},
		{http.StatusUnauthorized, core.ErrUnauthorized},
	} {
		fake := &fakeService{err: &googleapi.Error{Code: tc.code, Body: "secret-internal-detail"}}
		_, err := withFake(fake).Sync(context.Background(), request(), memHandle{}, newMemSink())
		classified, ok := err.(*core.SyncError)
		if !ok || classified.Code != tc.want {
			t.Fatalf("HTTP %d: got %v, want %s", tc.code, err, tc.want)
		}
		if fmt.Sprint(classified) == "" || classified.Message == "secret-internal-detail" {
			t.Error("raw Google error body leaked")
		}
	}
}

func TestRetryOnServerErrorThenRateLimitClassification(t *testing.T) {
	fake := &fakeService{err: &googleapi.Error{Code: http.StatusTooManyRequests}}
	_, err := withFake(fake).Sync(context.Background(), request(), memHandle{}, newMemSink())
	classified, ok := err.(*core.SyncError)
	if !ok || classified.Code != core.ErrRateLimited {
		t.Fatalf("got %v, want RATE_LIMITED", err)
	}
	if fake.queryCalls != 3 {
		t.Errorf("query attempts = %d, want 3 (retryable errors retry before classification)", fake.queryCalls)
	}
}

func TestUnsupportedCapability(t *testing.T) {
	req := request()
	req.Capability = core.CapConversion
	_, err := withFake(&fakeService{}).Sync(context.Background(), req, memHandle{}, newMemSink())
	classified, ok := err.(*core.SyncError)
	if !ok || classified.Code != core.ErrUnsupported {
		t.Errorf("got %v, want UNSUPPORTED", err)
	}
}

func TestDiscoverProperties(t *testing.T) {
	properties, err := withFake(&fakeService{}).DiscoverProperties(context.Background(), memHandle{})
	if err != nil {
		t.Fatal(err)
	}
	if len(properties) != 1 || properties[0].Reference != "sc-domain:example.test" {
		t.Errorf("properties = %+v", properties)
	}
}

func TestMissingPropertyIsNotConfigured(t *testing.T) {
	req := request()
	req.Property.Reference = ""
	_, err := withFake(&fakeService{}).Sync(context.Background(), req, memHandle{}, newMemSink())
	classified, ok := err.(*core.SyncError)
	if !ok || classified.Code != core.ErrNotConfigured {
		t.Errorf("got %v, want NOT_CONFIGURED", err)
	}
}
