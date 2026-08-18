// Package searchconsole implements the Google Search Console provider.
//
// Credential types:
//   - "service_account_json": a Google service-account key with the
//     webmasters.readonly scope, granted access to the property. This is the
//     unattended fallback; the interactive OAuth flow ships with the Console.
//
// Only implemented capabilities are declared — search performance today;
// URL inspection and sitemap state follow as they are ported.
package searchconsole

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	searchconsole "google.golang.org/api/searchconsole/v1"
)

const (
	pageSize = 25000
	// detailCap is the documented maximum rows per day and search type.
	detailCap = 50000

	datasetSearchDaily   = "searchconsole/search_daily"
	datasetSearchDetails = "searchconsole/search_details"
)

type Provider struct {
	// newService is swappable for tests; production builds a real client from
	// the credential handle.
	newService func(ctx context.Context, credential core.CredentialHandle) (service, error)
}

// service is the thin slice of the Search Console API the provider uses.
type service interface {
	Query(ctx context.Context, property string, request *searchconsole.SearchAnalyticsQueryRequest) (*searchconsole.SearchAnalyticsQueryResponse, error)
	ListSites(ctx context.Context) ([]*searchconsole.WmxSite, error)
}

func New() *Provider {
	return &Provider{newService: newGoogleService}
}

func (p *Provider) Descriptor() core.Descriptor {
	return core.Descriptor{
		Name:            "google-search-console",
		DisplayName:     "Google Search Console",
		CredentialTypes: []string{"oauth2", "service_account_json"},
		Capabilities: []core.CapabilitySpec{
			{
				Capability:       core.CapSearchPerformance,
				Dimensions:       []string{"date", "query", "page", "country", "device"},
				Metrics:          []string{"clicks", "impressions", "position"},
				MaxRangeDays:     366,
				FreshnessLagDays: 3,
				QuotaHint:        "Search analytics queries are rate limited per property; details paginate at 25k rows",
			},
		},
		SetupURL: "https://search.google.com/search-console",
		DocsURL:  "https://developers.google.com/webmaster-tools",
	}
}

func (p *Provider) DiscoverProperties(ctx context.Context, credential core.CredentialHandle) ([]core.Property, error) {
	svc, err := p.newService(ctx, credential)
	if err != nil {
		return nil, err
	}
	sites, err := svc.ListSites(ctx)
	if err != nil {
		return nil, classify(err)
	}
	out := make([]core.Property, 0, len(sites))
	for _, site := range sites {
		out = append(out, core.Property{Reference: site.SiteUrl, DisplayName: site.SiteUrl})
	}
	return out, nil
}

func (p *Provider) Test(ctx context.Context, _ core.Site, property core.Property, credential core.CredentialHandle) error {
	svc, err := p.newService(ctx, credential)
	if err != nil {
		return err
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -4)
	_, err = svc.Query(ctx, property.Reference, &searchconsole.SearchAnalyticsQueryRequest{
		StartDate: yesterday.Format("2006-01-02"),
		EndDate:   yesterday.Format("2006-01-02"),
		RowLimit:  1,
	})
	if err != nil {
		return classify(err)
	}
	return nil
}

func (p *Provider) Revoke(context.Context, core.CredentialHandle) error {
	// Service-account keys are revoked in Google Cloud IAM; there is no
	// token-revocation endpoint to call for them.
	return nil
}

func (p *Provider) Sync(ctx context.Context, request core.SyncRequest, credential core.CredentialHandle, sink core.SnapshotSink) (core.SyncResult, error) {
	if request.Capability != core.CapSearchPerformance {
		return core.SyncResult{}, &core.SyncError{Code: core.ErrUnsupported, Message: fmt.Sprintf("google-search-console does not support %q", request.Capability)}
	}
	svc, err := p.newService(ctx, credential)
	if err != nil {
		return core.SyncResult{}, err
	}
	property := request.Property.Reference
	if property == "" {
		return core.SyncResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: "no Search Console property selected"}
	}

	daily, err := fetch(ctx, svc, property, request.StartDate, request.EndDate, []string{"date"}, 0)
	if err != nil {
		return core.SyncResult{}, classify(err)
	}
	details, partial, err := fetchDetails(ctx, svc, property, request.StartDate, request.EndDate)
	if err != nil {
		return core.SyncResult{}, classify(err)
	}

	var dailyRows []map[string]any
	for _, row := range daily {
		if len(row.Keys) < 1 {
			continue
		}
		if _, parseErr := time.Parse("2006-01-02", row.Keys[0]); parseErr != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "Search Console returned an invalid date"}
		}
		impressions := int64(math.Round(row.Impressions))
		dailyRows = append(dailyRows, map[string]any{
			"_key": row.Keys[0], "date": row.Keys[0],
			"clicks": int64(math.Round(row.Clicks)), "impressions": impressions,
			"position_sum": row.Position * float64(impressions),
		})
	}
	var detailRows []map[string]any
	for _, row := range details {
		if len(row.Keys) < 5 {
			continue
		}
		if _, parseErr := time.Parse("2006-01-02", row.Keys[0]); parseErr != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "Search Console returned an invalid detail date"}
		}
		impressions := int64(math.Round(row.Impressions))
		country, device := strings.ToLower(row.Keys[3]), strings.ToLower(row.Keys[4])
		detailRows = append(detailRows, map[string]any{
			"_key": strings.Join([]string{row.Keys[0], row.Keys[1], row.Keys[2], country, device}, "|"),
			"date": row.Keys[0], "query": row.Keys[1], "page": row.Keys[2],
			"country": country, "device": device,
			"clicks": int64(math.Round(row.Clicks)), "impressions": impressions,
			"position_sum": row.Position * float64(impressions),
		})
	}

	if len(dailyRows) > 0 {
		if err := sink.Write(ctx, datasetSearchDaily, dailyRows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store Search Console snapshot"}
		}
	}
	if len(detailRows) > 0 {
		if err := sink.Write(ctx, datasetSearchDetails, detailRows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store Search Console details"}
		}
	}

	result := core.SyncResult{Rows: int64(len(dailyRows) + len(detailRows)), DataThroughDate: request.EndDate}
	if partial {
		return result, &core.SyncError{Code: core.ErrPartial, Message: "some days exceeded the Search Console detail row cap"}
	}
	return result, nil
}

func fetch(ctx context.Context, svc service, property string, start, end time.Time, dimensions []string, startRow int64) ([]*searchconsole.ApiDataRow, error) {
	request := &searchconsole.SearchAnalyticsQueryRequest{
		StartDate:  start.Format("2006-01-02"),
		EndDate:    end.Format("2006-01-02"),
		Dimensions: dimensions,
		DataState:  "final",
		Type:       "web",
		RowLimit:   pageSize,
		StartRow:   startRow,
	}
	var response *searchconsole.SearchAnalyticsQueryResponse
	err := retry(ctx, func() error {
		var callErr error
		response, callErr = svc.Query(ctx, property, request)
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return response.Rows, nil
}

func fetchDetails(ctx context.Context, svc service, property string, start, end time.Time) ([]*searchconsole.ApiDataRow, bool, error) {
	var rows []*searchconsole.ApiDataRow
	partial := false
	for date := start; !date.After(end); date = date.AddDate(0, 0, 1) {
		dayComplete := false
		for offset := int64(0); offset < detailCap; offset += pageSize {
			page, err := fetch(ctx, svc, property, date, date, []string{"date", "query", "page", "country", "device"}, offset)
			if err != nil {
				return nil, false, err
			}
			rows = append(rows, page...)
			if len(page) < pageSize {
				dayComplete = true
				break
			}
		}
		if !dayComplete {
			partial = true
		}
	}
	return rows, partial, nil
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

// classify maps Google API failures to public codes; response bodies stay here.
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
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &core.SyncError{Code: core.ErrTransient, Message: "Google API request timed out"}
	}
	return &core.SyncError{Code: core.ErrInternal, Message: "Google API request failed"}
}

type googleService struct {
	svc *searchconsole.Service
}

func newGoogleService(ctx context.Context, credential core.CredentialHandle) (service, error) {
	material := credential.Material()
	var clientOption option.ClientOption
	switch material.Type {
	case "service_account_json":
		jwtConfig, err := google.JWTConfigFromJSON(material.Bytes, searchconsole.WebmastersReadonlyScope)
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
	svc, err := searchconsole.NewService(ctx, clientOption, option.WithScopes(searchconsole.WebmastersReadonlyScope))
	if err != nil {
		return nil, &core.SyncError{Code: core.ErrInternal, Message: "initialize Search Console client"}
	}
	return googleService{svc: svc}, nil
}

func (g googleService) Query(ctx context.Context, property string, request *searchconsole.SearchAnalyticsQueryRequest) (*searchconsole.SearchAnalyticsQueryResponse, error) {
	return g.svc.Searchanalytics.Query(property, request).Context(ctx).Do()
}

func (g googleService) ListSites(ctx context.Context) ([]*searchconsole.WmxSite, error) {
	response, err := g.svc.Sites.List().Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return response.SiteEntry, nil
}
