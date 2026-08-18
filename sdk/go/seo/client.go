// Package seo provides a small, dependency-free client for seo-platform's
// versioned HTTP API. Provider credentials are accepted only by explicit
// connection methods and are never retained by Client.
package seo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type TokenSource func(context.Context) (string, error)

type Client struct {
	baseURL    string
	token      TokenSource
	httpClient *http.Client
}

func New(baseURL string, token TokenSource, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("seo: base URL must be absolute")
	}
	if token == nil {
		return nil, errors.New("seo: token source is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: parsed.String(), token: token, httpClient: httpClient}, nil
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("seo: API returned %d: %s", e.Status, e.Message)
}

type Capability struct {
	Capability       string   `json:"capability"`
	Dimensions       []string `json:"dimensions,omitempty"`
	Metrics          []string `json:"metrics,omitempty"`
	MaxRangeDays     int      `json:"max_range_days,omitempty"`
	FreshnessLagDays int      `json:"freshness_lag_days,omitempty"`
	SupportsCursor   bool     `json:"supports_cursor"`
	QuotaHint        string   `json:"quota_hint,omitempty"`
}

type Provider struct {
	Name            string       `json:"name"`
	DisplayName     string       `json:"display_name"`
	CredentialTypes []string     `json:"credential_types"`
	Capabilities    []Capability `json:"capabilities"`
	SetupURL        string       `json:"setup_url,omitempty"`
	DocsURL         string       `json:"docs_url,omitempty"`
	OAuthAvailable  bool         `json:"oauth_available"`
	SetupLinks      []SetupLink  `json:"setup_links,omitempty"`
}

type SetupLink struct {
	Kind        string `json:"kind,omitempty"`
	Label       string `json:"label"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

type Site struct {
	PublicURL     string `json:"public_url"`
	SitemapURL    string `json:"sitemap_url"`
	OAuthCallback string `json:"oauth_callback"`
}

type Connection struct {
	Provider          string `json:"provider"`
	Configured        bool   `json:"configured"`
	Enabled           bool   `json:"enabled"`
	CredentialType    string `json:"credential_type,omitempty"`
	PropertyReference string `json:"property_reference,omitempty"`
	State             string `json:"state"`
	LastSuccessAt     string `json:"last_success_at,omitempty"`
	DataThroughDate   string `json:"data_through_date,omitempty"`
	LastErrorCode     string `json:"last_error_code,omitempty"`
	LastErrorMessage  string `json:"last_error_message,omitempty"`
}

type Property struct {
	Reference   string `json:"reference"`
	DisplayName string `json:"display_name"`
}

type SyncRun struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	Capability     string `json:"capability"`
	StartDate      string `json:"start_date"`
	EndDate        string `json:"end_date"`
	Status         string `json:"status"`
	RowsSynced     int64  `json:"rows_synced"`
	Cursor         string `json:"cursor,omitempty"`
	TriggeredBy    string `json:"triggered_by,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
	ErrorCode      string `json:"error_code,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at,omitempty"`
}

type ReportRow struct {
	Dataset   string         `json:"dataset"`
	Key       string         `json:"key"`
	Data      map[string]any `json:"data"`
	UpdatedAt string         `json:"updated_at"`
}

func (c *Client) Providers(ctx context.Context) ([]Provider, error) {
	var out struct {
		Providers []Provider `json:"providers"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v0/providers", nil, &out)
	return out.Providers, err
}

func (c *Client) Site(ctx context.Context) (Site, error) {
	var out Site
	err := c.do(ctx, http.MethodGet, "/api/v0/site", nil, &out)
	return out, err
}

func (c *Client) Connections(ctx context.Context) ([]Connection, error) {
	var out struct {
		Connections []Connection `json:"connections"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v0/connections", nil, &out)
	return out.Connections, err
}

func (c *Client) Properties(ctx context.Context, provider string) ([]Property, error) {
	var out struct {
		Properties []Property `json:"properties"`
	}
	err := c.do(ctx, http.MethodGet, connectionPath(provider)+"/properties", nil, &out)
	return out.Properties, err
}

func (c *Client) SetCredential(ctx context.Context, provider, credentialType, material string) ([]Property, error) {
	var out struct {
		Properties []Property `json:"properties"`
	}
	err := c.do(ctx, http.MethodPut, connectionPath(provider)+"/credential", map[string]string{
		"credential_type": credentialType, "material": material,
	}, &out)
	return out.Properties, err
}

func (c *Client) SetProperty(ctx context.Context, provider, reference string) error {
	return c.do(ctx, http.MethodPut, connectionPath(provider)+"/property", map[string]string{"property_reference": reference}, nil)
}

func (c *Client) TestConnection(ctx context.Context, provider string) (bool, string, error) {
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	err := c.do(ctx, http.MethodPost, connectionPath(provider)+"/test", struct{}{}, &out)
	return out.OK, out.Error, err
}

func (c *Client) RevokeConnection(ctx context.Context, provider string) error {
	return c.do(ctx, http.MethodDelete, connectionPath(provider), nil, nil)
}

func (c *Client) StartOAuth(ctx context.Context, provider, redirectURI string) (authorizeURL, state string, err error) {
	return c.StartOAuthWithReturn(ctx, provider, redirectURI, "/")
}

func (c *Client) StartOAuthWithReturn(ctx context.Context, provider, redirectURI, returnTo string) (authorizeURL, state string, err error) {
	var out struct {
		AuthorizeURL string `json:"authorize_url"`
		State        string `json:"state"`
	}
	err = c.do(ctx, http.MethodPost, connectionPath(provider)+"/oauth/start", map[string]string{"redirect_uri": redirectURI, "return_to": returnTo}, &out)
	return out.AuthorizeURL, out.State, err
}

func (c *Client) CompleteOAuth(ctx context.Context, provider, state, code string) ([]Property, error) {
	properties, _, err := c.CompleteOAuthWithReturn(ctx, provider, state, code)
	return properties, err
}

func (c *Client) CompleteOAuthWithReturn(ctx context.Context, provider, state, code string) ([]Property, string, error) {
	var out struct {
		Properties []Property `json:"properties"`
		ReturnTo   string     `json:"return_to"`
	}
	err := c.do(ctx, http.MethodPost, connectionPath(provider)+"/oauth/complete", map[string]string{"state": state, "code": code}, &out)
	return out.Properties, out.ReturnTo, err
}

func (c *Client) SyncRuns(ctx context.Context, provider string, limit int) ([]SyncRun, error) {
	query := url.Values{}
	if provider != "" {
		query.Set("provider", provider)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v0/sync-runs"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		SyncRuns []SyncRun `json:"sync_runs"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.SyncRuns, err
}

type CreateSyncRunRequest struct {
	Provider       string `json:"provider"`
	Capability     string `json:"capability"`
	StartDate      string `json:"start_date,omitempty"`
	EndDate        string `json:"end_date,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func (c *Client) CreateSyncRun(ctx context.Context, input CreateSyncRunRequest) (SyncRun, bool, error) {
	var out struct {
		SyncRun        SyncRun `json:"sync_run"`
		AlreadyRunning bool    `json:"already_running"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v0/sync-runs", input, &out)
	return out.SyncRun, out.AlreadyRunning, err
}

func (c *Client) ReportDatasets(ctx context.Context) ([]string, error) {
	var out struct {
		Datasets []string `json:"datasets"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v0/report-datasets", nil, &out)
	return out.Datasets, err
}

func (c *Client) ReportRows(ctx context.Context, dataset string, limit int) ([]ReportRow, error) {
	rows, _, err := c.ReportRowsPage(ctx, dataset, limit, "")
	return rows, err
}

// ReportRowsPage reads one page of a normalized dataset. Pass nextCursor back
// unchanged; an empty nextCursor means the dataset is exhausted.
func (c *Client) ReportRowsPage(ctx context.Context, dataset string, limit int, cursor string) ([]ReportRow, string, error) {
	query := url.Values{"dataset": []string{dataset}}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var out struct {
		Rows       []ReportRow `json:"rows"`
		NextCursor string      `json:"next_cursor"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v0/report-rows?"+query.Encode(), nil, &out)
	return out.Rows, out.NextCursor, err
}

func connectionPath(provider string) string {
	return "/api/v0/connections/" + url.PathEscape(provider)
}

func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("seo: encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("seo: create request: %w", err)
	}
	token, err := c.token(ctx)
	if err != nil {
		return fmt.Errorf("seo: get access token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("seo: token source returned an empty token")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("seo: request: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 4<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&problem)
		if problem.Error == "" {
			problem.Error = http.StatusText(response.StatusCode)
		}
		return &APIError{Status: response.StatusCode, Message: problem.Error}
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return fmt.Errorf("seo: decode response: %w", err)
	}
	return nil
}
