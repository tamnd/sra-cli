// Package sra is the library behind the sra command line:
// the HTTP client, request shaping, and the typed data models for NCBI SRA.
//
// The Client here is the spine every command shares. It sets a real
// User-Agent, paces requests so a busy session stays polite, and retries the
// transient failures (429 and 5xx) that any public API throws under load.
// Search and summary calls follow the standard eUtils two-step:
// esearch returns a list of numeric UIDs, esummary turns them into records.
package sra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultUserAgent identifies the client to NCBI.
const DefaultUserAgent = "sra-cli/dev (+https://github.com/tamnd/sra-cli)"

// Host is the SRA site, used for Locate / URI resolution.
const Host = "eutils.ncbi.nlm.nih.gov"

// baseURL is the root every eUtils request is built from.
const baseURL = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"

// Config carries the tunable parameters for the SRA client.
type Config struct {
	BaseURL   string
	APIKey    string
	UserAgent string
	Rate      time.Duration
	Timeout   time.Duration
	Retries   int
}

// DefaultConfig returns a Config with sensible defaults for the free-tier
// NCBI eUtils API (3 req/s without a key).
func DefaultConfig() Config {
	return Config{
		BaseURL:   baseURL,
		APIKey:    os.Getenv("NCBI_API_KEY"),
		UserAgent: DefaultUserAgent,
		Rate:      400 * time.Millisecond,
		Timeout:   15 * time.Second,
		Retries:   3,
	}
}

// Client talks to the NCBI eUtils SRA API over HTTP.
type Client struct {
	HTTP *http.Client
	cfg  Config
	last time.Time
	// exported shims so domain.go newClient can set them like the scaffold does
	UserAgent string
	Rate      time.Duration
	Retries   int
}

// NewClient returns a Client with the default config.
func NewClient() *Client {
	cfg := DefaultConfig()
	return &Client{
		HTTP:      &http.Client{Timeout: cfg.Timeout},
		cfg:       cfg,
		UserAgent: cfg.UserAgent,
		Rate:      cfg.Rate,
		Retries:   cfg.Retries,
	}
}

// NewClientWithConfig returns a Client using the given config.
func NewClientWithConfig(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = baseURL
	}
	return &Client{
		HTTP:      &http.Client{Timeout: cfg.Timeout},
		cfg:       cfg,
		UserAgent: cfg.UserAgent,
		Rate:      cfg.Rate,
		Retries:   cfg.Retries,
	}
}

// Get fetches rawURL and returns the response body, pacing and retrying.
func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	retries := c.cfg.Retries
	if retries <= 0 {
		retries = c.Retries
	}
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		body, retry, err := c.do(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", rawURL, lastErr)
}

func (c *Client) do(ctx context.Context, rawURL string) (body []byte, retry bool, err error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	ua := c.cfg.UserAgent
	if ua == "" {
		ua = c.UserAgent
	}
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

func (c *Client) pace() {
	rate := c.cfg.Rate
	if rate <= 0 {
		rate = c.Rate
	}
	if rate <= 0 {
		return
	}
	if wait := rate - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

// buildURL constructs an eUtils endpoint URL with the given parameters.
func (c *Client) buildURL(endpoint string, params url.Values) string {
	params.Set("retmode", "json")
	base := c.cfg.BaseURL
	if base == "" {
		base = baseURL
	}
	apiKey := c.cfg.APIKey
	if apiKey != "" {
		params.Set("api_key", apiKey)
	}
	return base + "/" + endpoint + "?" + params.Encode()
}

// --- wire types (unexported) ---

type wireSearch struct {
	ESearchResult struct {
		Count  string   `json:"count"`
		IDList []string `json:"idlist"`
	} `json:"esearchresult"`
}

type wireSummaryResult struct {
	UID        string `json:"uid"`
	ExpXML     string `json:"expxml"`
	Runs       string `json:"runs"`
	TaxID      string `json:"taxid"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	UpdateDate string `json:"update_date"`
	CreateDate string `json:"create_date"`
}

type wireSummary struct {
	Result map[string]json.RawMessage `json:"result"`
}

// --- public output types ---

// Run is a single SRA experiment/run record.
type Run struct {
	ID         string `json:"id"                    kit:"id"` // SRA UID (numeric)
	Title      string `json:"title"`
	Accession  string `json:"accession,omitempty"`  // first run acc (SRR...)
	Platform   string `json:"platform,omitempty"`
	Instrument string `json:"instrument,omitempty"`
	TaxID      string `json:"tax_id,omitempty"`
	TotalSpots string `json:"total_spots,omitempty"`
	TotalBases string `json:"total_bases,omitempty"`
	Status     string `json:"status,omitempty"`
	CreateDate string `json:"create_date,omitempty"`
	UpdateDate string `json:"update_date,omitempty"`
}

// --- client methods ---

// Search runs an esearch query against the SRA database and returns
// a list of numeric UIDs plus the total count.
func (c *Client) Search(ctx context.Context, query string, limit, start int) ([]string, int, error) {
	if limit <= 0 {
		limit = 10
	}
	params := url.Values{
		"db":       {"sra"},
		"term":     {query},
		"retmax":   {strconv.Itoa(limit)},
		"retstart": {strconv.Itoa(start)},
	}
	body, err := c.Get(ctx, c.buildURL("esearch.fcgi", params))
	if err != nil {
		return nil, 0, err
	}
	var ws wireSearch
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, 0, fmt.Errorf("esearch parse: %w", err)
	}
	count, _ := strconv.Atoi(ws.ESearchResult.Count)
	ids := ws.ESearchResult.IDList
	if ids == nil {
		ids = []string{}
	}
	return ids, count, nil
}

// FetchRuns fetches esummary for a batch of SRA UIDs and returns Run records.
func (c *Client) FetchRuns(ctx context.Context, ids []string) ([]*Run, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	params := url.Values{
		"db": {"sra"},
		"id": {strings.Join(ids, ",")},
	}
	body, err := c.Get(ctx, c.buildURL("esummary.fcgi", params))
	if err != nil {
		return nil, err
	}
	var ws wireSummary
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, fmt.Errorf("esummary parse: %w", err)
	}
	uids, err := parseUIDs(ws.Result)
	if err != nil {
		return nil, err
	}
	var out []*Run
	for _, uid := range uids {
		raw, ok := ws.Result[uid]
		if !ok {
			continue
		}
		var wr wireSummaryResult
		if err := json.Unmarshal(raw, &wr); err != nil {
			continue
		}
		out = append(out, runFromWire(uid, wr))
	}
	return out, nil
}

// GetRun fetches a single SRA run record by numeric UID.
func (c *Client) GetRun(ctx context.Context, uid string) (*Run, error) {
	runs, err := c.FetchRuns(ctx, []string{uid})
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("sra uid %s: not found", uid)
	}
	return runs[0], nil
}

// SearchAndFetch runs Search then FetchRuns in one call.
func (c *Client) SearchAndFetch(ctx context.Context, query string, limit, start int) ([]*Run, int, error) {
	ids, count, err := c.Search(ctx, query, limit, start)
	if err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return nil, count, nil
	}
	runs, err := c.FetchRuns(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	return runs, count, nil
}

// --- helpers ---

// parseUIDs extracts the ordered UID list from the esummary result map.
func parseUIDs(result map[string]json.RawMessage) ([]string, error) {
	raw, ok := result["uids"]
	if !ok {
		return nil, nil
	}
	var uids []string
	if err := json.Unmarshal(raw, &uids); err != nil {
		return nil, fmt.Errorf("parse uids: %w", err)
	}
	return uids, nil
}

// runFromWire converts the wire representation to a Run.
func runFromWire(uid string, wr wireSummaryResult) *Run {
	id := wr.UID
	if id == "" {
		id = uid
	}
	title := wr.Title
	if title == "" {
		title = extractTagText(wr.ExpXML, "Title")
	}
	platform := extractTagText(wr.ExpXML, "Platform")
	instrument := extractAttr(wr.ExpXML, "Platform", "instrument_model")
	totalSpots := extractAttr(wr.ExpXML, "Statistics", "total_spots")
	totalBases := extractAttr(wr.ExpXML, "Statistics", "total_bases")
	accession := extractAttr(wr.Runs, "Run", "acc")

	return &Run{
		ID:         id,
		Title:      title,
		Accession:  accession,
		Platform:   platform,
		Instrument: instrument,
		TaxID:      wr.TaxID,
		TotalSpots: totalSpots,
		TotalBases: totalBases,
		Status:     wr.Status,
		CreateDate: wr.CreateDate,
		UpdateDate: wr.UpdateDate,
	}
}

// extractTagText returns the text content of the first occurrence of <tag>...</tag>.
func extractTagText(s, tag string) string {
	open := "<" + tag
	close := "</" + tag + ">"
	start := strings.Index(s, open)
	if start < 0 {
		return ""
	}
	// find the end of the opening tag
	gt := strings.Index(s[start:], ">")
	if gt < 0 {
		return ""
	}
	contentStart := start + gt + 1
	end := strings.Index(s[contentStart:], close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(s[contentStart : contentStart+end])
}

var attrRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// extractAttr returns the value of attr inside the first element matching tag.
func extractAttr(s, tag, attr string) string {
	open := "<" + tag
	start := strings.Index(s, open)
	if start < 0 {
		return ""
	}
	gt := strings.Index(s[start:], ">")
	if gt < 0 {
		return ""
	}
	elem := s[start : start+gt+1]
	// find attr="value" within elem
	target := attr + `="`
	idx := strings.Index(elem, target)
	if idx < 0 {
		return ""
	}
	rest := elem[idx+len(target):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
