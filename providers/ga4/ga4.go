// Package ga4 implements the Google Analytics 4 provider. GA4 is an
// acquisition/conversion analytics source — it is not a search index data
// source and never pretends to be one (ADR 0005).
//
// Conversion events are deployment configuration: the host names its own
// events via WithConversionEvents. No event name is built in, and the
// analytics.conversion capability is declared only when events are
// configured, so an unconfigured deployment honestly reports it as absent.
package ga4

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	analyticsadmin "google.golang.org/api/analyticsadmin/v1beta"
	analyticsdata "google.golang.org/api/analyticsdata/v1beta"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const pageSize = 100000

const (
	datasetAcquisitionDaily = "ga4/acquisition_daily"
	datasetConversionDaily  = "ga4/conversion_daily"
	datasetQuality          = "ga4/quality"
)

var acquisitionDimensions = []string{"date", "sessionDefaultChannelGroup", "sessionSource", "sessionMedium", "landingPage", "deviceCategory"}
var trafficMetrics = []string{"sessions", "engagedSessions", "activeUsers", "newUsers", "userEngagementDuration"}

type Provider struct {
	conversionEvents []string
	newService       func(ctx context.Context, credential core.CredentialHandle) (service, error)
	client           *http.Client
	revokeURL        string
}

type service interface {
	ListProperties(ctx context.Context) ([]core.Property, error)
	RunReport(ctx context.Context, property string, request *analyticsdata.RunReportRequest) (*analyticsdata.RunReportResponse, error)
}

type Option func(*Provider)

// WithConversionEvents names the GA4 events this deployment counts as
// conversions. The order defines nothing; each event is reported by name.
func WithConversionEvents(events []string) Option {
	return func(p *Provider) {
		for _, event := range events {
			if trimmed := strings.TrimSpace(event); trimmed != "" {
				p.conversionEvents = append(p.conversionEvents, trimmed)
			}
		}
	}
}

func New(options ...Option) *Provider {
	p := &Provider{newService: newGoogleService, client: &http.Client{Timeout: 30 * time.Second}, revokeURL: "https://oauth2.googleapis.com/revoke"}
	for _, option := range options {
		option(p)
	}
	return p
}

func (p *Provider) Descriptor() core.Descriptor {
	capabilities := []core.CapabilitySpec{{
		Capability:       core.CapAcquisition,
		Dimensions:       []string{"date", "channel_group", "source", "medium", "landing_page", "device"},
		Metrics:          []string{"sessions", "engaged_sessions", "active_users", "new_users", "engagement_seconds"},
		MaxRangeDays:     366,
		FreshnessLagDays: 1,
		QuotaHint:        "GA4 Data API property quota; thresholding and sampling are surfaced in the quality dataset",
	}}
	if len(p.conversionEvents) > 0 {
		capabilities = append(capabilities, core.CapabilitySpec{
			Capability:       core.CapConversion,
			Dimensions:       []string{"date", "channel_group", "source", "medium", "landing_page", "device", "event"},
			Metrics:          []string{"event_count"},
			MaxRangeDays:     366,
			FreshnessLagDays: 1,
		})
	}
	return core.Descriptor{
		Name:            "google-analytics",
		DisplayName:     "Google Analytics 4",
		CredentialTypes: []string{"oauth2", "service_account_json"},
		Capabilities:    capabilities,
		SetupURL:        "https://analytics.google.com/analytics/web/",
		DocsURL:         "https://developers.google.com/analytics/devguides/reporting/data/v1",
		SetupLinks: []core.SetupLink{
			{Kind: "console", Label: "Google Analytics", URL: "https://analytics.google.com/analytics/web/", Description: "Open reports for the selected property."},
			{Kind: "permissions", Label: "Google Analytics Admin", URL: "https://analytics.google.com/analytics/web/#/a/admin", Description: "Grant the OAuth user or service account read access."},
			{Kind: "credentials", Label: "Google Cloud credentials", URL: "https://console.cloud.google.com/apis/credentials", Description: "Create the OAuth web client used by this deployment."},
		},
	}
}

func (p *Provider) DiscoverProperties(ctx context.Context, credential core.CredentialHandle) ([]core.Property, error) {
	svc, err := p.newService(ctx, credential)
	if err != nil {
		return nil, err
	}
	properties, err := svc.ListProperties(ctx)
	if err != nil {
		return nil, classify(err)
	}
	return properties, nil
}

func (p *Provider) Test(ctx context.Context, _ core.Site, property core.Property, credential core.CredentialHandle) error {
	svc, err := p.newService(ctx, credential)
	if err != nil {
		return err
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	_, err = svc.RunReport(ctx, normalizeProperty(property.Reference), &analyticsdata.RunReportRequest{
		DateRanges: []*analyticsdata.DateRange{{StartDate: yesterday, EndDate: yesterday}},
		Metrics:    []*analyticsdata.Metric{{Name: "sessions"}},
		Limit:      1,
	})
	if err != nil {
		return classify(err)
	}
	return nil
}

func (p *Provider) Revoke(ctx context.Context, credential core.CredentialHandle) error {
	material := credential.Material()
	if material.Type != "oauth2" {
		// Service-account keys are revoked in Google Cloud IAM.
		return nil
	}
	oauth, err := core.ParseOAuthMaterial(material.Bytes)
	if err != nil {
		return &core.SyncError{Code: core.ErrUnauthorized, Message: "Google OAuth credential is invalid"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.revokeURL, strings.NewReader(url.Values{"token": []string{oauth.RefreshToken}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := p.client.Do(req)
	if err != nil {
		return &core.SyncError{Code: core.ErrTransient, Message: "Google token revocation failed"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &core.SyncError{Code: core.ErrTransient, Message: "Google token revocation failed"}
	}
	return nil
}

func (p *Provider) Sync(ctx context.Context, request core.SyncRequest, credential core.CredentialHandle, sink core.SnapshotSink) (core.SyncResult, error) {
	property := normalizeProperty(request.Property.Reference)
	if property == "properties/" {
		return core.SyncResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: "no GA4 property configured"}
	}
	svc, err := p.newService(ctx, credential)
	if err != nil {
		return core.SyncResult{}, err
	}
	switch request.Capability {
	case core.CapAcquisition:
		return p.syncAcquisition(ctx, svc, property, request, sink)
	case core.CapConversion:
		if len(p.conversionEvents) == 0 {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrUnsupported, Message: "no conversion events configured"}
		}
		return p.syncConversion(ctx, svc, property, request, sink)
	default:
		return core.SyncResult{}, &core.SyncError{Code: core.ErrUnsupported, Message: "google-analytics does not support this capability"}
	}
}

func (p *Provider) syncAcquisition(ctx context.Context, svc service, property string, request core.SyncRequest, sink core.SnapshotSink) (core.SyncResult, error) {
	reportRows, metadata, err := runPagedReport(ctx, svc, property, request.StartDate, request.EndDate, acquisitionDimensions, trafficMetrics, nil)
	if err != nil {
		return core.SyncResult{}, classify(err)
	}
	rows := make([]map[string]any, 0, len(reportRows))
	for _, reportRow := range rowsWithDimensions(reportRows, 6) {
		base, key, parseErr := dimensionColumns(reportRow)
		if parseErr != nil {
			return core.SyncResult{}, parseErr
		}
		base["_key"] = key
		base["sessions"] = metricInt(reportRow, 0)
		base["engaged_sessions"] = metricInt(reportRow, 1)
		base["active_users"] = metricInt(reportRow, 2)
		base["new_users"] = metricInt(reportRow, 3)
		base["engagement_seconds"] = metricFloat(reportRow, 4)
		rows = append(rows, base)
	}
	if len(rows) > 0 {
		if err := sink.Write(ctx, datasetAcquisitionDaily, rows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store GA4 acquisition snapshot"}
		}
	}
	if err := writeQuality(ctx, sink, request.EndDate, metadata); err != nil {
		return core.SyncResult{}, err
	}
	return core.SyncResult{Rows: int64(len(rows)), DataThroughDate: request.EndDate}, nil
}

func (p *Provider) syncConversion(ctx context.Context, svc service, property string, request core.SyncRequest, sink core.SnapshotSink) (core.SyncResult, error) {
	dimensions := append(append([]string{}, acquisitionDimensions...), "eventName")
	filter := &analyticsdata.FilterExpression{Filter: &analyticsdata.Filter{
		FieldName:    "eventName",
		InListFilter: &analyticsdata.InListFilter{Values: p.conversionEvents, CaseSensitive: true},
	}}
	reportRows, metadata, err := runPagedReport(ctx, svc, property, request.StartDate, request.EndDate, dimensions, []string{"eventCount"}, filter)
	if err != nil {
		return core.SyncResult{}, classify(err)
	}
	rows := make([]map[string]any, 0, len(reportRows))
	for _, reportRow := range rowsWithDimensions(reportRows, 7) {
		base, key, parseErr := dimensionColumns(reportRow)
		if parseErr != nil {
			return core.SyncResult{}, parseErr
		}
		event := reportRow.DimensionValues[6].Value
		base["_key"] = key + "\x00" + event
		base["event"] = event
		base["event_count"] = metricInt(reportRow, 0)
		rows = append(rows, base)
	}
	if len(rows) > 0 {
		if err := sink.Write(ctx, datasetConversionDaily, rows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store GA4 conversion snapshot"}
		}
	}
	if err := writeQuality(ctx, sink, request.EndDate, metadata); err != nil {
		return core.SyncResult{}, err
	}
	return core.SyncResult{Rows: int64(len(rows)), DataThroughDate: request.EndDate}, nil
}

// writeQuality records thresholding/sampling flags so reports can distinguish
// "truly zero" from "GA4 withheld data" (ADR 0005: no fabricated equivalence).
func writeQuality(ctx context.Context, sink core.SnapshotSink, end time.Time, metadata *analyticsdata.ResponseMetaData) error {
	row := map[string]any{
		"_key":              end.Format("2006-01-02"),
		"data_through_date": end.Format("2006-01-02"),
	}
	if metadata != nil {
		row["subject_to_thresholding"] = metadata.SubjectToThresholding
		row["data_loss_other_row"] = metadata.DataLossFromOtherRow
		row["sampled"] = len(metadata.SamplingMetadatas) > 0
		if metadata.EmptyReason != "" {
			row["empty_reason"] = metadata.EmptyReason
		}
	}
	if err := sink.Write(ctx, datasetQuality, []map[string]any{row}); err != nil {
		return &core.SyncError{Code: core.ErrInternal, Message: "store GA4 quality metadata"}
	}
	return nil
}

func rowsWithDimensions(rows []*analyticsdata.Row, minimum int) []*analyticsdata.Row {
	out := rows[:0:0]
	for _, row := range rows {
		if len(row.DimensionValues) >= minimum {
			out = append(out, row)
		}
	}
	return out
}

func dimensionColumns(row *analyticsdata.Row) (map[string]any, string, error) {
	date, err := time.Parse("20060102", row.DimensionValues[0].Value)
	if err != nil {
		return nil, "", &core.SyncError{Code: core.ErrInternal, Message: "Google Analytics returned an invalid date"}
	}
	landing := safeLandingPage(row.DimensionValues[4].Value)
	device := strings.ToLower(row.DimensionValues[5].Value)
	day := date.Format("2006-01-02")
	columns := map[string]any{
		"date":          day,
		"channel_group": row.DimensionValues[1].Value,
		"source":        row.DimensionValues[2].Value,
		"medium":        row.DimensionValues[3].Value,
		"landing_page":  landing,
		"device":        device,
	}
	key := strings.Join([]string{day, row.DimensionValues[1].Value, row.DimensionValues[2].Value, row.DimensionValues[3].Value, landing, device}, "\x00")
	return columns, key, nil
}

func safeLandingPage(raw string) string {
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Path != "" {
		raw = parsed.Path
	}
	raw = strings.SplitN(raw, "?", 2)[0]
	raw = strings.SplitN(raw, "#", 2)[0]
	if !strings.HasPrefix(raw, "/") || len(raw) > 500 {
		return "/"
	}
	return raw
}

func runPagedReport(ctx context.Context, svc service, property string, start, end time.Time, dimensions, metrics []string, filter *analyticsdata.FilterExpression) ([]*analyticsdata.Row, *analyticsdata.ResponseMetaData, error) {
	request := &analyticsdata.RunReportRequest{
		DateRanges:          []*analyticsdata.DateRange{{StartDate: start.Format("2006-01-02"), EndDate: end.Format("2006-01-02")}},
		DimensionFilter:     filter,
		Limit:               pageSize,
		ReturnPropertyQuota: true,
	}
	for _, name := range dimensions {
		request.Dimensions = append(request.Dimensions, &analyticsdata.Dimension{Name: name})
	}
	for _, name := range metrics {
		request.Metrics = append(request.Metrics, &analyticsdata.Metric{Name: name})
	}
	var rows []*analyticsdata.Row
	var metadata *analyticsdata.ResponseMetaData
	for offset := int64(0); ; offset += pageSize {
		request.Offset = offset
		var response *analyticsdata.RunReportResponse
		err := retry(ctx, func() error {
			var callErr error
			response, callErr = svc.RunReport(ctx, property, request)
			return callErr
		})
		if err != nil {
			return nil, metadata, err
		}
		rows = append(rows, response.Rows...)
		metadata = mergeMetadata(metadata, response.Metadata)
		if offset+int64(len(response.Rows)) >= response.RowCount || len(response.Rows) == 0 {
			break
		}
	}
	return rows, metadata, nil
}

func mergeMetadata(a, b *analyticsdata.ResponseMetaData) *analyticsdata.ResponseMetaData {
	if a == nil {
		a = &analyticsdata.ResponseMetaData{}
	}
	if b == nil {
		return a
	}
	a.SubjectToThresholding = a.SubjectToThresholding || b.SubjectToThresholding
	a.DataLossFromOtherRow = a.DataLossFromOtherRow || b.DataLossFromOtherRow
	a.SamplingMetadatas = append(a.SamplingMetadatas, b.SamplingMetadatas...)
	if a.EmptyReason == "" {
		a.EmptyReason = b.EmptyReason
	}
	return a
}

func metricFloat(row *analyticsdata.Row, index int) float64 {
	if index >= len(row.MetricValues) {
		return 0
	}
	value, _ := strconv.ParseFloat(row.MetricValues[index].Value, 64)
	return value
}

func metricInt(row *analyticsdata.Row, index int) int64 {
	return int64(math.Round(metricFloat(row, index)))
}

func normalizeProperty(property string) string {
	property = strings.TrimSpace(property)
	if !strings.HasPrefix(property, "properties/") {
		property = "properties/" + property
	}
	return property
}

func retry(ctx context.Context, call func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = call(); err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func retryable(err error) bool {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == http.StatusTooManyRequests || apiErr.Code >= 500
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func classify(err error) error {
	var alreadyClassified *core.SyncError
	if errors.As(err, &alreadyClassified) {
		return err
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden:
			return &core.SyncError{Code: core.ErrUnauthorized, Message: "Google property authorization failed"}
		case apiErr.Code == http.StatusTooManyRequests:
			return &core.SyncError{Code: core.ErrRateLimited, Message: "Google API quota exceeded"}
		case apiErr.Code >= 500:
			return &core.SyncError{Code: core.ErrTransient, Message: "Google API is temporarily unavailable"}
		}
	}
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		if retrieveErr.Response != nil && (retrieveErr.Response.StatusCode == http.StatusBadRequest || retrieveErr.Response.StatusCode == http.StatusUnauthorized) {
			return &core.SyncError{Code: core.ErrUnauthorized, Message: "Google OAuth authorization expired or was revoked"}
		}
		return &core.SyncError{Code: core.ErrTransient, Message: "Google OAuth token refresh failed"}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &core.SyncError{Code: core.ErrTransient, Message: "Google API request timed out"}
	}
	return &core.SyncError{Code: core.ErrInternal, Message: "Google API request failed"}
}

type googleService struct {
	data  *analyticsdata.Service
	admin *analyticsadmin.Service
}

func newGoogleService(ctx context.Context, credential core.CredentialHandle) (service, error) {
	material := credential.Material()
	var clientOption option.ClientOption
	switch material.Type {
	case "service_account_json":
		jwtConfig, err := google.JWTConfigFromJSON(material.Bytes, analyticsdata.AnalyticsReadonlyScope)
		if err != nil {
			return nil, &core.SyncError{Code: core.ErrUnauthorized, Message: "invalid Google service-account credential"}
		}
		clientOption = option.WithHTTPClient(jwtConfig.Client(ctx))
	case "oauth2":
		parsed, err := core.ParseOAuthMaterial(material.Bytes)
		if err != nil {
			return nil, &core.SyncError{Code: core.ErrUnauthorized, Message: "invalid OAuth credential"}
		}
		config := oauth2.Config{
			ClientID:     parsed.ClientID,
			ClientSecret: parsed.ClientSecret,
			Endpoint:     oauth2.Endpoint{TokenURL: parsed.TokenURL},
		}
		clientOption = option.WithTokenSource(config.TokenSource(ctx, &oauth2.Token{RefreshToken: parsed.RefreshToken}))
	default:
		return nil, &core.SyncError{Code: core.ErrUnauthorized, Message: "an OAuth or service-account credential is required"}
	}
	dataService, err := analyticsdata.NewService(ctx, clientOption, option.WithScopes(analyticsdata.AnalyticsReadonlyScope))
	if err != nil {
		return nil, &core.SyncError{Code: core.ErrInternal, Message: "initialize Google Analytics client"}
	}
	adminService, err := analyticsadmin.NewService(ctx, clientOption, option.WithScopes(analyticsdata.AnalyticsReadonlyScope))
	if err != nil {
		return nil, &core.SyncError{Code: core.ErrInternal, Message: "initialize Google Analytics Admin client"}
	}
	return googleService{data: dataService, admin: adminService}, nil
}

func (g googleService) RunReport(ctx context.Context, property string, request *analyticsdata.RunReportRequest) (*analyticsdata.RunReportResponse, error) {
	return g.data.Properties.RunReport(property, request).Context(ctx).Do()
}

func (g googleService) ListProperties(ctx context.Context) ([]core.Property, error) {
	properties := make([]core.Property, 0)
	err := g.admin.AccountSummaries.List().PageSize(200).Pages(ctx, func(response *analyticsadmin.GoogleAnalyticsAdminV1betaListAccountSummariesResponse) error {
		for _, account := range response.AccountSummaries {
			for _, property := range account.PropertySummaries {
				reference := strings.TrimPrefix(strings.TrimSpace(property.Property), "properties/")
				if reference == "" {
					continue
				}
				displayName := strings.TrimSpace(property.DisplayName)
				if displayName == "" {
					displayName = property.Property
				}
				if accountName := strings.TrimSpace(account.DisplayName); accountName != "" {
					displayName += " — " + accountName
				}
				properties = append(properties, core.Property{Reference: reference, DisplayName: displayName})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(properties, func(i, j int) bool {
		if properties[i].DisplayName == properties[j].DisplayName {
			return properties[i].Reference < properties[j].Reference
		}
		return properties[i].DisplayName < properties[j].DisplayName
	})
	return properties, nil
}
