// Package bing implements the Bing Webmaster Tools provider: search
// performance, crawl statistics, and sitemap/feed state via the JSON API.
//
// The credential is a Bing Webmaster API key (credential type "api_key").
// Keys are created and revoked in the Bing Webmaster console; Revoke here is
// therefore a no-op on the provider side.
package bing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
)

const defaultEndpoint = "https://ssl.bing.com/webmaster/api.svc/json/"
const maxResponseBytes = 20 << 20

const (
	datasetSearchDaily   = "bing/search_daily"
	datasetSearchDetails = "bing/search_details"
	datasetCrawlDaily    = "bing/crawl_daily"
	datasetSitemaps      = "bing/sitemaps"
)

type Provider struct {
	client   *http.Client
	endpoint string
}

type Option func(*Provider)

// WithEndpoint overrides the API endpoint (tests, proxies).
func WithEndpoint(endpoint string) Option {
	return func(p *Provider) { p.endpoint = endpoint }
}

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) { p.client = client }
}

func New(options ...Option) *Provider {
	p := &Provider{client: &http.Client{Timeout: 2 * time.Minute}, endpoint: defaultEndpoint}
	for _, option := range options {
		option(p)
	}
	return p
}

func (p *Provider) Descriptor() core.Descriptor {
	return core.Descriptor{
		Name:            "bing-webmaster",
		DisplayName:     "Bing Webmaster Tools",
		CredentialTypes: []string{"api_key"},
		Capabilities: []core.CapabilitySpec{
			{
				Capability:       core.CapSearchPerformance,
				Dimensions:       []string{"date", "query", "page"},
				Metrics:          []string{"clicks", "impressions", "position"},
				MaxRangeDays:     180,
				FreshnessLagDays: 1,
				QuotaHint:        "Bing serves fixed trailing windows; requested ranges are filtered client-side",
			},
			{
				Capability:       core.CapCrawlStats,
				Metrics:          []string{"crawled_pages", "in_index", "crawl_errors", "blocked_by_robots", "http_2xx", "http_4xx", "http_5xx"},
				MaxRangeDays:     180,
				FreshnessLagDays: 1,
			},
			{
				Capability: core.CapSitemaps,
				Dimensions: []string{"url", "type", "status"},
			},
		},
		SetupURL: "https://www.bing.com/webmasters/settings/apiaccess",
		DocsURL:  "https://learn.microsoft.com/bing-webmaster-tools/",
	}
}

type site struct {
	URL string `json:"Url"`
}

func (p *Provider) DiscoverProperties(ctx context.Context, credential core.CredentialHandle) ([]core.Property, error) {
	var sites []site
	if err := p.get(ctx, credential, "GetUserSites", "", &sites); err != nil {
		return nil, classify(err)
	}
	out := make([]core.Property, 0, len(sites))
	for _, s := range sites {
		out = append(out, core.Property{Reference: s.URL, DisplayName: s.URL})
	}
	return out, nil
}

func (p *Provider) Test(ctx context.Context, _ core.Site, property core.Property, credential core.CredentialHandle) error {
	var stats []traffic
	if err := p.get(ctx, credential, "GetRankAndTrafficStats", property.Reference, &stats); err != nil {
		return classify(err)
	}
	return nil
}

func (p *Provider) Revoke(context.Context, core.CredentialHandle) error {
	// API keys are managed in the Bing Webmaster console; there is no
	// revocation endpoint. Deleting the stored secret is the SecretStore's job.
	return nil
}

type traffic struct {
	Clicks                int64   `json:"Clicks"`
	Date                  string  `json:"Date"`
	Impressions           int64   `json:"Impressions"`
	Query                 string  `json:"Query"`
	AvgImpressionPosition float64 `json:"AvgImpressionPosition"`
}

type crawl struct {
	Date               string `json:"Date"`
	CrawledPages       int64  `json:"CrawledPages"`
	InIndex            int64  `json:"InIndex"`
	CrawlErrors        int64  `json:"CrawlErrors"`
	BlockedByRobotsTxt int64  `json:"BlockedByRobotsTxt"`
	Code2xx            int64  `json:"Code2xx"`
	Code4xx            int64  `json:"Code4xx"`
	Code5xx            int64  `json:"Code5xx"`
	ContainsMalware    int64  `json:"ContainsMalware"`
}

type feed struct {
	LastCrawled string `json:"LastCrawled"`
	Status      string `json:"Status"`
	Submitted   string `json:"Submitted"`
	Type        string `json:"Type"`
	URL         string `json:"Url"`
	URLCount    int64  `json:"UrlCount"`
}

func (p *Provider) Sync(ctx context.Context, request core.SyncRequest, credential core.CredentialHandle, sink core.SnapshotSink) (core.SyncResult, error) {
	property := request.Property.Reference
	if property == "" {
		property = request.Site.PublicURL
	}
	switch request.Capability {
	case core.CapSearchPerformance:
		return p.syncSearch(ctx, credential, property, request, sink)
	case core.CapCrawlStats:
		return p.syncCrawl(ctx, credential, property, request, sink)
	case core.CapSitemaps:
		return p.syncSitemaps(ctx, credential, property, request, sink)
	default:
		return core.SyncResult{}, &core.SyncError{Code: core.ErrUnsupported, Message: fmt.Sprintf("bing-webmaster does not support %q", request.Capability)}
	}
}

func (p *Provider) syncSearch(ctx context.Context, credential core.CredentialHandle, property string, request core.SyncRequest, sink core.SnapshotSink) (core.SyncResult, error) {
	var daily, queries, pages []traffic
	if err := p.get(ctx, credential, "GetRankAndTrafficStats", property, &daily); err != nil {
		return core.SyncResult{}, classify(err)
	}
	if err := p.get(ctx, credential, "GetQueryStats", property, &queries); err != nil {
		return core.SyncResult{}, classify(err)
	}
	if err := p.get(ctx, credential, "GetPageStats", property, &pages); err != nil {
		return core.SyncResult{}, classify(err)
	}

	var dailyRows, detailRows []map[string]any
	dataThrough := time.Time{}
	for _, item := range daily {
		date, err := parseDate(item.Date)
		if err != nil || date.Before(request.StartDate) || date.After(request.EndDate) {
			continue
		}
		day := date.Format("2006-01-02")
		dailyRows = append(dailyRows, map[string]any{
			"_key": day, "date": day,
			"clicks": item.Clicks, "impressions": item.Impressions,
		})
		if date.After(dataThrough) {
			dataThrough = date
		}
	}
	appendDetails := func(items []traffic, dimension string) {
		for _, item := range items {
			date, err := parseDate(item.Date)
			if err != nil || date.Before(request.StartDate) || date.After(request.EndDate) || item.Query == "" {
				continue
			}
			day := date.Format("2006-01-02")
			detailRows = append(detailRows, map[string]any{
				"_key": day + "|" + dimension + "|" + item.Query,
				"date": day, dimension: item.Query,
				"clicks": item.Clicks, "impressions": item.Impressions,
				"position_sum": item.AvgImpressionPosition * float64(item.Impressions),
			})
		}
	}
	appendDetails(queries, "query")
	appendDetails(pages, "page")

	if len(dailyRows) > 0 {
		if err := sink.Write(ctx, datasetSearchDaily, dailyRows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store Bing search snapshot"}
		}
	}
	if len(detailRows) > 0 {
		if err := sink.Write(ctx, datasetSearchDetails, detailRows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store Bing search details"}
		}
	}
	return core.SyncResult{Rows: int64(len(dailyRows) + len(detailRows)), DataThroughDate: dataThrough}, nil
}

func (p *Provider) syncCrawl(ctx context.Context, credential core.CredentialHandle, property string, request core.SyncRequest, sink core.SnapshotSink) (core.SyncResult, error) {
	var crawls []crawl
	if err := p.get(ctx, credential, "GetCrawlStats", property, &crawls); err != nil {
		return core.SyncResult{}, classify(err)
	}
	var rows []map[string]any
	dataThrough := time.Time{}
	for _, item := range crawls {
		date, err := parseDate(item.Date)
		if err != nil || date.Before(request.StartDate) || date.After(request.EndDate) {
			continue
		}
		day := date.Format("2006-01-02")
		rows = append(rows, map[string]any{
			"_key": day, "date": day,
			"crawled_pages": item.CrawledPages, "in_index": item.InIndex,
			"crawl_errors": item.CrawlErrors, "blocked_by_robots": item.BlockedByRobotsTxt,
			"http_2xx": item.Code2xx, "http_4xx": item.Code4xx, "http_5xx": item.Code5xx,
			"contains_malware": item.ContainsMalware,
		})
		if date.After(dataThrough) {
			dataThrough = date
		}
	}
	if len(rows) > 0 {
		if err := sink.Write(ctx, datasetCrawlDaily, rows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store Bing crawl snapshot"}
		}
	}
	return core.SyncResult{Rows: int64(len(rows)), DataThroughDate: dataThrough}, nil
}

func (p *Provider) syncSitemaps(ctx context.Context, credential core.CredentialHandle, property string, request core.SyncRequest, sink core.SnapshotSink) (core.SyncResult, error) {
	var feeds []feed
	if err := p.get(ctx, credential, "GetFeeds", property, &feeds); err != nil {
		return core.SyncResult{}, classify(err)
	}
	rows := make([]map[string]any, 0, len(feeds))
	for _, item := range feeds {
		row := map[string]any{
			"_key": item.URL, "url": item.URL, "type": item.Type,
			"status": item.Status, "url_count": item.URLCount,
			"pending": !strings.EqualFold(item.Status, "Success"),
		}
		if t, err := parseDate(item.Submitted); err == nil {
			row["last_submitted_at"] = t.Format("2006-01-02")
		}
		if t, err := parseDate(item.LastCrawled); err == nil {
			row["last_downloaded_at"] = t.Format("2006-01-02")
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		if err := sink.Write(ctx, datasetSitemaps, rows); err != nil {
			return core.SyncResult{}, &core.SyncError{Code: core.ErrInternal, Message: "store Bing sitemap snapshot"}
		}
	}
	return core.SyncResult{Rows: int64(len(rows)), DataThroughDate: request.EndDate}, nil
}

type httpError struct{ Status int }

func (e *httpError) Error() string { return fmt.Sprintf("Bing API returned HTTP %d", e.Status) }

func (p *Provider) get(ctx context.Context, credential core.CredentialHandle, method, siteURL string, target any) error {
	apiKey := strings.TrimSpace(string(credential.Material().Bytes))
	if apiKey == "" {
		return &core.SyncError{Code: core.ErrUnauthorized, Message: "Bing API key is empty"}
	}
	values := url.Values{"apikey": []string{apiKey}}
	if siteURL != "" {
		values.Set("siteUrl", siteURL)
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+method+"?"+values.Encode(), nil)
		if err != nil {
			return err
		}
		response, err := p.client.Do(req)
		if err != nil {
			last = err
		} else {
			func() {
				defer response.Body.Close()
				if response.StatusCode < 200 || response.StatusCode >= 300 {
					last = &httpError{Status: response.StatusCode}
					return
				}
				var envelope struct {
					Data json.RawMessage `json:"d"`
				}
				if decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&envelope); decodeErr != nil {
					last = decodeErr
					return
				}
				last = json.Unmarshal(envelope.Data, target)
			}()
		}
		if last == nil {
			return nil
		}
		var httpErr *httpError
		if errors.As(last, &httpErr) && httpErr.Status != http.StatusTooManyRequests && httpErr.Status < 500 {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * time.Second):
		}
	}
	return last
}

var datePattern = regexp.MustCompile(`^/Date\((-?\d+)`)

// parseDate handles the WCF "/Date(1690000000000)/" millisecond format the
// Bing JSON API uses.
func parseDate(raw string) (time.Time, error) {
	match := datePattern.FindStringSubmatch(raw)
	if len(match) != 2 {
		return time.Time{}, errors.New("invalid Bing date")
	}
	millis, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(millis).UTC().Truncate(24 * time.Hour), nil
}

// classify maps transport failures to public error codes; raw response bodies
// never leave this package.
func classify(err error) error {
	var alreadyClassified *core.SyncError
	if errors.As(err, &alreadyClassified) {
		return err
	}
	var httpErr *httpError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden:
			return &core.SyncError{Code: core.ErrUnauthorized, Message: "Bing property authorization failed"}
		case httpErr.Status == http.StatusTooManyRequests:
			return &core.SyncError{Code: core.ErrRateLimited, Message: "Bing API quota exceeded"}
		case httpErr.Status >= 500:
			return &core.SyncError{Code: core.ErrTransient, Message: "Bing API is temporarily unavailable"}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &core.SyncError{Code: core.ErrTransient, Message: "Bing API request timed out"}
	}
	return &core.SyncError{Code: core.ErrInternal, Message: "Bing API response could not be read"}
}
