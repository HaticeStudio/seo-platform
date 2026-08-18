// Package searchconsole implements the Google Search Console provider.
//
// Credential types:
//   - "service_account_json": a Google service-account key with the
//     webmasters.readonly scope, granted access to the property. This is the
//     unattended fallback; the interactive OAuth flow ships with the Console.
//
// Search performance, sitemap state, and batched URL inspection are declared
// independently so hosts can render the real upstream capability surface.
package searchconsole

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
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
	datasetSitemaps      = "searchconsole/sitemaps"
	datasetURLInspection = "searchconsole/url_inspection"
	maxSitemapBytes      = 10 << 20
)

type Provider struct {
	// newService is swappable for tests; production builds a real client from
	// the credential handle.
	newService      func(ctx context.Context, credential core.CredentialHandle) (service, error)
	client          *http.Client
	inspectionBatch int
	revokeURL       string
}

// service is the thin slice of the Search Console API the provider uses.
type service interface {
	Query(ctx context.Context, property string, request *searchconsole.SearchAnalyticsQueryRequest) (*searchconsole.SearchAnalyticsQueryResponse, error)
	ListSites(ctx context.Context) ([]*searchconsole.WmxSite, error)
	ListSitemaps(ctx context.Context, property string) ([]*searchconsole.WmxSitemap, error)
	InspectURL(ctx context.Context, property, inspectionURL string) (*searchconsole.InspectUrlIndexResponse, error)
}

func New() *Provider {
	return &Provider{newService: newGoogleService, client: &http.Client{Timeout: 30 * time.Second}, inspectionBatch: 20, revokeURL: "https://oauth2.googleapis.com/revoke"}
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
			{Capability: core.CapSitemaps, Dimensions: []string{"url", "type", "pending"}, Metrics: []string{"warnings", "errors", "submitted"}},
			{Capability: core.CapURLInspection, Dimensions: []string{"url", "verdict", "coverage_state", "indexing_state"}, MaxRangeDays: 1, SupportsCursor: true, QuotaHint: "URL Inspection has a limited daily property quota; the default batch is 20 URLs"},
		},
		SetupURL: "https://search.google.com/search-console",
		DocsURL:  "https://developers.google.com/webmaster-tools",
		SetupLinks: []core.SetupLink{
			{Kind: "console", Label: "Search Console", URL: "https://search.google.com/search-console", Description: "Verify the site and review indexing data."},
			{Kind: "enable_api", Label: "Enable Search Console API", URL: "https://console.cloud.google.com/apis/library/searchconsole.googleapis.com", Description: "Enable the API in the Google Cloud project that owns the OAuth client."},
			{Kind: "credentials", Label: "Google Cloud credentials", URL: "https://console.cloud.google.com/apis/credentials", Description: "Create an OAuth web client or a service-account key."},
			{Kind: "permissions", Label: "Users and permissions", URL: "https://search.google.com/search-console/users", Description: "Grant the OAuth user or service account access to the property."},
			{Kind: "sitemaps", Label: "Sitemaps", URL: "https://search.google.com/search-console/sitemaps", Description: "Submit and inspect the public sitemap."},
		},
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
	svc, err := p.newService(ctx, credential)
	if err != nil {
		return core.SyncResult{}, err
	}
	property := request.Property.Reference
	if property == "" {
		return core.SyncResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: "no Search Console property selected"}
	}

	switch request.Capability {
	case core.CapSearchPerformance:
		return p.syncPerformance(ctx, svc, property, request, sink)
	case core.CapSitemaps:
		return p.syncSitemaps(ctx, svc, property, sink)
	case core.CapURLInspection:
		return p.syncURLInspection(ctx, svc, property, request, sink)
	default:
		return core.SyncResult{}, &core.SyncError{Code: core.ErrUnsupported, Message: fmt.Sprintf("google-search-console does not support %q", request.Capability)}
	}
}

func (p *Provider) syncPerformance(ctx context.Context, svc service, property string, request core.SyncRequest, sink core.SnapshotSink) (core.SyncResult, error) {
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

func (p *Provider) syncSitemaps(ctx context.Context, svc service, property string, sink core.SnapshotSink) (core.SyncResult, error) {
	sitemaps, err := svc.ListSitemaps(ctx, property)
	if err != nil {
		return core.SyncResult{}, classify(err)
	}
	rows := make([]map[string]any, 0, len(sitemaps))
	for _, sitemap := range sitemaps {
		contents := make([]map[string]any, 0, len(sitemap.Contents))
		for _, content := range sitemap.Contents {
			contents = append(contents, map[string]any{"type": content.Type, "submitted": content.Submitted, "indexed": content.Indexed})
		}
		rows = append(rows, map[string]any{
			"_key": sitemap.Path, "url": sitemap.Path, "type": sitemap.Type,
			"pending": sitemap.IsPending, "warnings": sitemap.Warnings, "errors": sitemap.Errors,
			"last_submitted": sitemap.LastSubmitted, "last_downloaded": sitemap.LastDownloaded,
			"contents": contents,
		})
	}
	if len(rows) > 0 {
		if err := sink.Write(ctx, datasetSitemaps, rows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store Search Console sitemaps"}
		}
	}
	return core.SyncResult{Rows: int64(len(rows)), DataThroughDate: time.Now().UTC().Truncate(24 * time.Hour)}, nil
}

type sitemapDocument struct {
	URLs []struct {
		Location string `xml:"loc"`
	} `xml:"url"`
}

func (p *Provider) sitemapURLs(ctx context.Context, site core.Site) ([]string, error) {
	sitemapURL, err := url.Parse(site.SitemapURL)
	if err != nil || sitemapURL.Scheme == "" || sitemapURL.Host == "" {
		return nil, &core.SyncError{Code: core.ErrNotConfigured, Message: "sitemap URL must be absolute"}
	}
	publicURL, err := url.Parse(site.PublicURL)
	if err != nil || !strings.EqualFold(sitemapURL.Hostname(), publicURL.Hostname()) || (sitemapURL.Scheme != "https" && sitemapURL.Scheme != "http") {
		return nil, &core.SyncError{Code: core.ErrNotConfigured, Message: "sitemap URL must use the public site origin"}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, sitemapURL.String(), nil)
	response, err := p.client.Do(req)
	if err != nil {
		return nil, &core.SyncError{Code: core.ErrTransient, Message: "fetch sitemap failed"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &core.SyncError{Code: core.ErrTransient, Message: fmt.Sprintf("sitemap returned HTTP %d", response.StatusCode)}
	}
	var document sitemapDocument
	if err := xml.NewDecoder(io.LimitReader(response.Body, maxSitemapBytes)).Decode(&document); err != nil {
		return nil, &core.SyncError{Code: core.ErrInternal, Message: "sitemap is not valid XML"}
	}
	seen := make(map[string]bool)
	var out []string
	for _, entry := range document.URLs {
		parsed, parseErr := url.Parse(strings.TrimSpace(entry.Location))
		if parseErr != nil || parsed.Scheme == "" || !strings.EqualFold(parsed.Hostname(), publicURL.Hostname()) {
			continue
		}
		parsed.RawQuery, parsed.Fragment = "", ""
		normalized := parsed.String()
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return nil, &core.SyncError{Code: core.ErrNoData, Message: "sitemap contains no same-origin URLs"}
	}
	return out, nil
}

func (p *Provider) syncURLInspection(ctx context.Context, svc service, property string, request core.SyncRequest, sink core.SnapshotSink) (core.SyncResult, error) {
	urls, err := p.sitemapURLs(ctx, request.Site)
	if err != nil {
		return core.SyncResult{}, err
	}
	offset := 0
	if request.Cursor != "" {
		offset, err = strconv.Atoi(request.Cursor)
		if err != nil || offset < 0 || offset > len(urls) {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "URL inspection cursor is invalid"}
		}
	}
	end := min(offset+p.inspectionBatch, len(urls))
	rows := make([]map[string]any, 0, end-offset)
	for _, inspectionURL := range urls[offset:end] {
		response, inspectErr := svc.InspectURL(ctx, property, inspectionURL)
		if inspectErr != nil {
			if len(rows) > 0 {
				_ = sink.Write(ctx, datasetURLInspection, rows)
				_ = sink.Checkpoint(ctx, strconv.Itoa(offset+len(rows)))
			}
			return core.SyncResult{Rows: int64(len(rows)), Cursor: strconv.Itoa(offset + len(rows))}, classify(inspectErr)
		}
		if response.InspectionResult == nil || response.InspectionResult.IndexStatusResult == nil {
			continue
		}
		status := response.InspectionResult.IndexStatusResult
		knownSitemaps, _ := json.Marshal(status.Sitemap)
		rows = append(rows, map[string]any{
			"_key": inspectionURL, "url": inspectionURL, "verdict": status.Verdict,
			"coverage_state": status.CoverageState, "indexing_state": status.IndexingState,
			"page_fetch_state": status.PageFetchState, "robots_txt_state": status.RobotsTxtState,
			"last_crawl_time": status.LastCrawlTime, "google_canonical": status.GoogleCanonical,
			"user_canonical": status.UserCanonical, "crawled_as": status.CrawledAs,
			"known_sitemaps": string(knownSitemaps), "inspection_result_link": response.InspectionResult.InspectionResultLink,
		})
	}
	if len(rows) > 0 {
		if err := sink.Write(ctx, datasetURLInspection, rows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store URL inspection snapshot"}
		}
	}
	cursor := ""
	if end < len(urls) {
		cursor = strconv.Itoa(end)
		if err := sink.Checkpoint(ctx, cursor); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store URL inspection cursor"}
		}
		return core.SyncResult{Rows: int64(len(rows)), Cursor: cursor}, &core.SyncError{Code: core.ErrPartial, Message: "URL inspection batch completed; more URLs remain"}
	}
	return core.SyncResult{Rows: int64(len(rows)), DataThroughDate: time.Now().UTC().Truncate(24 * time.Hour)}, nil
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

func (g googleService) ListSitemaps(ctx context.Context, property string) ([]*searchconsole.WmxSitemap, error) {
	response, err := g.svc.Sitemaps.List(property).Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	return response.Sitemap, nil
}

func (g googleService) InspectURL(ctx context.Context, property, inspectionURL string) (*searchconsole.InspectUrlIndexResponse, error) {
	return g.svc.UrlInspection.Index.Inspect(&searchconsole.InspectUrlIndexRequest{InspectionUrl: inspectionURL, SiteUrl: property}).Context(ctx).Do()
}
