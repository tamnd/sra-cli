package sra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockSearchBody returns a minimal esearch JSON response with the given UIDs.
func mockSearchBody(count int, ids []string) []byte {
	b, _ := json.Marshal(map[string]any{
		"esearchresult": map[string]any{
			"count":  fmt.Sprintf("%d", count),
			"idlist": ids,
		},
	})
	return b
}

// mockSummaryBody returns a minimal esummary JSON response for the given UID,
// embedding expxml and runs XML strings.
func mockSummaryBody(uid string) []byte {
	expxml := `<Summary><Title>RNA-seq of human brain</Title>` +
		`<Platform instrument_model="Illumina NovaSeq 6000">ILLUMINA</Platform>` +
		`<Statistics total_spots="47000000" total_bases="7050000000"/></Summary>`
	runs := `<Run acc="SRR21234567" total_spots="47000000" total_bases="7050000000" load_done="true" is_public="true"/>`
	rec := map[string]any{
		"uid":         uid,
		"expxml":      expxml,
		"runs":        runs,
		"taxid":       "9606",
		"title":       "RNA-seq of human brain",
		"status":      "live",
		"update_date": "2024-01-15T00:00:00.000Z",
		"create_date": "2023-12-01T00:00:00.000Z",
	}
	result := map[string]any{
		"uids": []string{uid},
		uid:    rec,
	}
	b, _ := json.Marshal(map[string]any{"result": result})
	return b
}

func newTestClient(srv *httptest.Server) *Client {
	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Rate = 0
	cfg.Retries = 0
	cfg.Timeout = 5 * time.Second
	return NewClientWithConfig(cfg)
}

func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewClient()
	c.Rate = 0

	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestGetRetriesOn503(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	c := NewClient()
	c.Rate = 0
	c.Retries = 5
	c.cfg.Retries = 5
	c.cfg.Rate = 0

	start := time.Now()
	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "recovered" {
		t.Errorf("body = %q after retries", body)
	}
	if hits != 3 {
		t.Errorf("server saw %d hits, want 3", hits)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("retries did not back off")
	}
}

func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/esearch.fcgi" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if db := r.URL.Query().Get("db"); db != "sra" {
			t.Errorf("db = %q, want sra", db)
		}
		_, _ = w.Write(mockSearchBody(8154708, []string{"38055985", "38055984", "38055983"}))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	ids, count, err := c.Search(context.Background(), "human RNA", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if count != 8154708 {
		t.Errorf("count = %d, want 8154708", count)
	}
	if len(ids) != 3 {
		t.Errorf("len(ids) = %d, want 3", len(ids))
	}
	if ids[0] != "38055985" {
		t.Errorf("ids[0] = %q, want 38055985", ids[0])
	}
}

func TestFetchRuns(t *testing.T) {
	const uid = "38055985"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/esummary.fcgi" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if db := r.URL.Query().Get("db"); db != "sra" {
			t.Errorf("db = %q, want sra", db)
		}
		_, _ = w.Write(mockSummaryBody(uid))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	runs, err := c.FetchRuns(context.Background(), []string{uid})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	r := runs[0]
	if r.ID != uid {
		t.Errorf("ID = %q, want %q", r.ID, uid)
	}
	if r.Title != "RNA-seq of human brain" {
		t.Errorf("Title = %q", r.Title)
	}
	if r.Platform != "ILLUMINA" {
		t.Errorf("Platform = %q, want ILLUMINA", r.Platform)
	}
	if r.Instrument != "Illumina NovaSeq 6000" {
		t.Errorf("Instrument = %q, want Illumina NovaSeq 6000", r.Instrument)
	}
	if r.TaxID != "9606" {
		t.Errorf("TaxID = %q, want 9606", r.TaxID)
	}
	if r.TotalSpots != "47000000" {
		t.Errorf("TotalSpots = %q, want 47000000", r.TotalSpots)
	}
	if r.TotalBases != "7050000000" {
		t.Errorf("TotalBases = %q, want 7050000000", r.TotalBases)
	}
	if r.Accession != "SRR21234567" {
		t.Errorf("Accession = %q, want SRR21234567", r.Accession)
	}
	if r.Status != "live" {
		t.Errorf("Status = %q, want live", r.Status)
	}
}

func TestGetRun(t *testing.T) {
	const uid = "38055985"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(mockSummaryBody(uid))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	r, err := c.GetRun(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != uid {
		t.Errorf("ID = %q, want %q", r.ID, uid)
	}
}

func TestSearchAndFetch(t *testing.T) {
	const uid = "38055985"
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		switch r.URL.Path {
		case "/esearch.fcgi":
			_, _ = w.Write(mockSearchBody(1, []string{uid}))
		case "/esummary.fcgi":
			_, _ = w.Write(mockSummaryBody(uid))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	runs, count, err := c.SearchAndFetch(context.Background(), "human RNA", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
	if runs[0].ID != uid {
		t.Errorf("ID = %q, want %q", runs[0].ID, uid)
	}
	if reqCount != 2 {
		t.Errorf("server requests = %d, want 2 (search + summary)", reqCount)
	}
}

func TestExtractTagText(t *testing.T) {
	xml := `<Summary><Title>RNA-seq of human brain</Title><Platform instrument_model="Illumina NovaSeq 6000">ILLUMINA</Platform></Summary>`
	cases := []struct{ tag, want string }{
		{"Title", "RNA-seq of human brain"},
		{"Platform", "ILLUMINA"},
	}
	for _, tc := range cases {
		got := extractTagText(xml, tc.tag)
		if got != tc.want {
			t.Errorf("extractTagText(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestExtractAttr(t *testing.T) {
	xml := `<Platform instrument_model="Illumina NovaSeq 6000">ILLUMINA</Platform>`
	got := extractAttr(xml, "Platform", "instrument_model")
	if got != "Illumina NovaSeq 6000" {
		t.Errorf("extractAttr = %q, want Illumina NovaSeq 6000", got)
	}

	xml2 := `<Statistics total_spots="47000000" total_bases="7050000000"/>`
	if v := extractAttr(xml2, "Statistics", "total_spots"); v != "47000000" {
		t.Errorf("total_spots = %q, want 47000000", v)
	}
	if v := extractAttr(xml2, "Statistics", "total_bases"); v != "7050000000" {
		t.Errorf("total_bases = %q, want 7050000000", v)
	}
}
