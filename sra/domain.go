package sra

import (
	"context"
	"strings"
	"unicode"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes SRA as a kit Domain: a driver that a multi-domain
// host (ant) enables with a single blank import,
//
//	import _ "github.com/tamnd/sra-cli/sra"
//
// exactly as a database/sql program enables a driver with `import _
// "github.com/lib/pq"`. The init below registers it; the host then dereferences
// sra:// URIs by routing to the operations Register installs. The same
// Domain also builds the standalone sra binary (see cmd/sra/main.go), so the
// binary and a host share one source of truth.
func init() { kit.Register(Domain{}) }

// Domain is the SRA driver. It carries no state; the per-run client is
// built by the factory Register hands kit.
type Domain struct{}

// Info describes the scheme, the hostnames a pasted link is matched against, and
// the identity reused for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "sra",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "sra",
			Short:  "Read public NCBI SRA sequencing run data.",
			Long: `Read public NCBI SRA sequencing run data.

sra reads from the NCBI Sequence Read Archive (8M+ runs) over plain HTTPS,
shapes it into clean records, and prints output that pipes into the rest of
your tools. No API key required for up to 3 req/s; set NCBI_API_KEY for
higher limits.`,
			Site: "www.ncbi.nlm.nih.gov/sra",
			Repo: "https://github.com/tamnd/sra-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	// search: full-text search across SRA, returns Run records.
	kit.Handle(app, kit.OpMeta{Name: "search", Group: "read", List: true,
		Summary: "Search NCBI SRA and return run records",
		Args:    []kit.Arg{{Name: "query", Help: "search terms", Variadic: true}}}, searchRuns)

	// run: fetch a single SRA run record by numeric UID.
	kit.Handle(app, kit.OpMeta{Name: "run", Group: "read", Single: true,
		Summary: "Fetch an SRA run record by numeric UID", URIType: "run", Resolver: true,
		Args: []kit.Arg{{Name: "uid", Help: "SRA numeric UID or accession (SRR/ERR/DRR)"}}}, getRun)

	// study: search SRA restricted to study-level records.
	kit.Handle(app, kit.OpMeta{Name: "study", Group: "read", List: true,
		Summary: "Search SRA studies by title or keyword",
		Args:    []kit.Arg{{Name: "query", Help: "search terms", Variadic: true}}}, searchStudy)

	// organism: search SRA by organism / taxonomy.
	kit.Handle(app, kit.OpMeta{Name: "organism", Group: "read", List: true,
		Summary: "Search SRA runs by organism name or taxon",
		Args:    []kit.Arg{{Name: "taxon", Help: "organism name or taxon", Variadic: true}}}, searchOrganism)
}

// newClient builds the SRA client from the host-resolved config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	scfg := DefaultConfig()
	if cfg.UserAgent != "" {
		scfg.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		scfg.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		scfg.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		scfg.Timeout = cfg.Timeout
	}
	return NewClientWithConfig(scfg), nil
}

// --- inputs ---

type searchInput struct {
	Query  []string `kit:"arg,variadic" help:"search terms"`
	Limit  int      `kit:"flag,inherit" help:"max results"`
	Start  int      `kit:"flag" help:"result offset"`
	Client *Client  `kit:"inject"`
}

type runRef struct {
	UID    string  `kit:"arg" help:"SRA numeric UID or accession (SRR/ERR/DRR)"`
	Client *Client `kit:"inject"`
}

type studyInput struct {
	Query  []string `kit:"arg,variadic" help:"study title or keyword"`
	Limit  int      `kit:"flag,inherit" help:"max results"`
	Start  int      `kit:"flag" help:"result offset"`
	Client *Client  `kit:"inject"`
}

type organismInput struct {
	Taxon  []string `kit:"arg,variadic" help:"organism name or taxon"`
	Limit  int      `kit:"flag,inherit" help:"max results"`
	Start  int      `kit:"flag" help:"result offset"`
	Client *Client  `kit:"inject"`
}

// --- handlers ---

func searchRuns(ctx context.Context, in searchInput, emit func(*Run) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	runs, _, err := in.Client.SearchAndFetch(ctx, strings.Join(in.Query, " "), limit, in.Start)
	if err != nil {
		return mapErr(err)
	}
	for _, r := range runs {
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

func getRun(ctx context.Context, in runRef, emit func(*Run) error) error {
	uid := in.UID
	// If the caller passes an accession like SRR21234567, search for it.
	if !isDigits(uid) {
		ids, _, err := in.Client.Search(ctx, uid+"[accession]", 1, 0)
		if err != nil {
			return mapErr(err)
		}
		if len(ids) == 0 {
			return errs.NotFound("sra accession %s: not found", uid)
		}
		uid = ids[0]
	}
	r, err := in.Client.GetRun(ctx, uid)
	if err != nil {
		return mapErr(err)
	}
	return emit(r)
}

func searchStudy(ctx context.Context, in studyInput, emit func(*Run) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	q := strings.Join(in.Query, " ")
	// Append [study] qualifier to restrict to study-level hits.
	if q != "" {
		q = q + "[study]"
	}
	runs, _, err := in.Client.SearchAndFetch(ctx, q, limit, in.Start)
	if err != nil {
		return mapErr(err)
	}
	for _, r := range runs {
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

func searchOrganism(ctx context.Context, in organismInput, emit func(*Run) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	taxon := strings.Join(in.Taxon, " ")
	q := taxon + "[organism]"
	runs, _, err := in.Client.SearchAndFetch(ctx, q, limit, in.Start)
	if err != nil {
		return mapErr(err)
	}
	for _, r := range runs {
		if err := emit(r); err != nil {
			return err
		}
	}
	return nil
}

// --- Resolver: pure string functions, no network ---

// Classify turns any accepted input into the canonical (type, id).
// All-digit strings are SRA UIDs. SRR/ERR/DRR accessions resolve via search.
// Any other non-empty string is treated as a run reference.
func (Domain) Classify(input string) (uriType, id string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", errs.Usage("empty SRA reference")
	}
	return "run", input, nil
}

// Locate is the inverse: the live https URL for a (type, id).
func (Domain) Locate(uriType, id string) (string, error) {
	if uriType != "run" {
		return "", errs.Usage("sra has no resource type %q", uriType)
	}
	return "https://www.ncbi.nlm.nih.gov/sra/" + id, nil
}

// --- helpers ---

// isDigits reports whether s is a non-empty string of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// mapErr converts a library error into the kit error kind that carries the right
// exit code.
func mapErr(err error) error {
	return err
}
