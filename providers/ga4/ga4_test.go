package ga4

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/providertest"
	"golang.org/x/oauth2"
	analyticsdata "google.golang.org/api/analyticsdata/v1beta"
)

func TestContract(t *testing.T) {
	providertest.RunContract(t, New())
	providertest.RunContract(t, New(WithConversionEvents([]string{"signup"})))
}

func TestConversionCapabilityRequiresConfiguredEvents(t *testing.T) {
	if _, ok := New().Descriptor().Spec(core.CapConversion); ok {
		t.Error("conversion capability declared without configured events")
	}
	if _, ok := New(WithConversionEvents([]string{"signup"})).Descriptor().Spec(core.CapConversion); !ok {
		t.Error("conversion capability missing despite configured events")
	}
}

type memHandle struct{}

func (memHandle) Material() core.SecretMaterial {
	return core.SecretMaterial{Type: "service_account_json", Bytes: []byte("{}")}
}
func (memHandle) Close() {}

type oauthHandle struct{ raw []byte }

func (h oauthHandle) Material() core.SecretMaterial {
	return core.SecretMaterial{Type: "oauth2", Bytes: h.raw}
}
func (oauthHandle) Close() {}

type memSink struct{ writes map[string][]map[string]any }

func newMemSink() *memSink { return &memSink{writes: map[string][]map[string]any{}} }

func (s *memSink) Write(_ context.Context, dataset string, rows []map[string]any) error {
	s.writes[dataset] = append(s.writes[dataset], rows...)
	return nil
}
func (s *memSink) Checkpoint(context.Context, string) error { return nil }

type fakeService struct {
	responses []*analyticsdata.RunReportResponse
	requests  []*analyticsdata.RunReportRequest
}

func (f *fakeService) RunReport(_ context.Context, _ string, request *analyticsdata.RunReportRequest) (*analyticsdata.RunReportResponse, error) {
	copied := *request
	f.requests = append(f.requests, &copied)
	if len(f.responses) == 0 {
		return &analyticsdata.RunReportResponse{}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func withFake(p *Provider, fake *fakeService) *Provider {
	p.newService = func(context.Context, core.CredentialHandle) (service, error) { return fake, nil }
	return p
}

func dims(values ...string) []*analyticsdata.DimensionValue {
	out := make([]*analyticsdata.DimensionValue, 0, len(values))
	for _, v := range values {
		out = append(out, &analyticsdata.DimensionValue{Value: v})
	}
	return out
}

func metrics(values ...string) []*analyticsdata.MetricValue {
	out := make([]*analyticsdata.MetricValue, 0, len(values))
	for _, v := range values {
		out = append(out, &analyticsdata.MetricValue{Value: v})
	}
	return out
}

func request(capability core.Capability) core.SyncRequest {
	return core.SyncRequest{
		Site:       core.Site{ID: "default", PublicURL: "https://example.test"},
		Property:   core.Property{Reference: "123456"},
		Capability: capability,
		StartDate:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
	}
}

func TestSyncAcquisition(t *testing.T) {
	fake := &fakeService{responses: []*analyticsdata.RunReportResponse{{
		Rows: []*analyticsdata.Row{{
			DimensionValues: dims("20260810", "Organic Search", "google", "organic", "https://example.test/pricing?utm=x", "DESKTOP"),
			MetricValues:    metrics("10", "8", "9", "4", "512.5"),
		}},
		RowCount: 1,
		Metadata: &analyticsdata.ResponseMetaData{SubjectToThresholding: true},
	}}}
	sink := newMemSink()
	result, err := withFake(New(), fake).Sync(context.Background(), request(core.CapAcquisition), memHandle{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 1 {
		t.Errorf("rows = %d, want 1", result.Rows)
	}
	rows := sink.writes[datasetAcquisitionDaily]
	if len(rows) != 1 {
		t.Fatalf("acquisition rows: %+v", sink.writes)
	}
	row := rows[0]
	if row["landing_page"] != "/pricing" {
		t.Errorf("landing page not sanitized: %v", row["landing_page"])
	}
	if row["device"] != "desktop" || row["sessions"] != int64(10) {
		t.Errorf("row = %+v", row)
	}
	quality := sink.writes[datasetQuality]
	if len(quality) != 1 || quality[0]["subject_to_thresholding"] != true {
		t.Errorf("quality metadata not surfaced: %+v", quality)
	}
}

func TestSyncConversionUsesConfiguredEvents(t *testing.T) {
	fake := &fakeService{responses: []*analyticsdata.RunReportResponse{{
		Rows: []*analyticsdata.Row{{
			DimensionValues: dims("20260811", "Direct", "(direct)", "(none)", "/", "MOBILE", "signup"),
			MetricValues:    metrics("3"),
		}},
		RowCount: 1,
	}}}
	provider := withFake(New(WithConversionEvents([]string{"signup", " demo_request "})), fake)
	sink := newMemSink()
	result, err := provider.Sync(context.Background(), request(core.CapConversion), memHandle{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 1 {
		t.Errorf("rows = %d, want 1", result.Rows)
	}
	rows := sink.writes[datasetConversionDaily]
	if len(rows) != 1 || rows[0]["event"] != "signup" || rows[0]["event_count"] != int64(3) {
		t.Fatalf("conversion rows: %+v", rows)
	}

	filter := fake.requests[0].DimensionFilter
	if filter == nil || filter.Filter == nil || filter.Filter.InListFilter == nil {
		t.Fatal("conversion query missing event filter")
	}
	values := filter.Filter.InListFilter.Values
	if len(values) != 2 || values[0] != "signup" || values[1] != "demo_request" {
		t.Errorf("filter events = %v (must come from configuration, trimmed)", values)
	}
}

func TestConversionWithoutEventsIsUnsupported(t *testing.T) {
	_, err := withFake(New(), &fakeService{}).Sync(context.Background(), request(core.CapConversion), memHandle{}, newMemSink())
	classified, ok := err.(*core.SyncError)
	if !ok || classified.Code != core.ErrUnsupported {
		t.Errorf("got %v, want UNSUPPORTED", err)
	}
}

func TestMissingPropertyIsNotConfigured(t *testing.T) {
	req := request(core.CapAcquisition)
	req.Property.Reference = ""
	_, err := withFake(New(), &fakeService{}).Sync(context.Background(), req, memHandle{}, newMemSink())
	classified, ok := err.(*core.SyncError)
	if !ok || classified.Code != core.ErrNotConfigured {
		t.Errorf("got %v, want NOT_CONFIGURED", err)
	}
}

func TestClassifyExpiredOAuthRefreshToken(t *testing.T) {
	err := classify(&oauth2.RetrieveError{
		Response: &http.Response{StatusCode: http.StatusUnauthorized},
		Body:     []byte(`{"error":"invalid_grant","secret":"must-not-leak"}`),
	})
	classified, ok := err.(*core.SyncError)
	if !ok || classified.Code != core.ErrUnauthorized {
		t.Fatalf("got %v, want UNAUTHORIZED", err)
	}
	if strings.Contains(classified.Message, "invalid_grant") || strings.Contains(classified.Message, "must-not-leak") {
		t.Fatalf("OAuth response body leaked: %q", classified.Message)
	}
}

func TestNoHardcodedEventNames(t *testing.T) {
	// The public provider must not carry any deployment's event names.
	d := New().Descriptor()
	for _, spec := range d.Capabilities {
		for _, dim := range spec.Dimensions {
			if dim == "estimate_start" || dim == "estimate_submit" || dim == "line_contact_click" {
				t.Fatalf("hardcoded host event leaked into descriptor: %s", dim)
			}
		}
	}
}

func TestRevokeOAuthRefreshToken(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Error("refresh token must not be placed in the URL")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		received = r.Form.Get("token")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	raw, err := (core.OAuthMaterial{ClientID: "client", TokenURL: "https://token.example.test", RefreshToken: "refresh-secret"}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	provider := New()
	provider.client = server.Client()
	provider.revokeURL = server.URL
	if err := provider.Revoke(context.Background(), oauthHandle{raw: raw}); err != nil {
		t.Fatal(err)
	}
	if received != "refresh-secret" {
		t.Fatalf("revoked token = %q", received)
	}
}
