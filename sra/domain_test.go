package sra

import (
	"testing"

	"github.com/tamnd/any-cli/kit"
)

// These tests are offline: they exercise the URI driver's pure string functions
// and the host wiring, which need no network. The client's HTTP behaviour is
// covered in sra_test.go.

func TestDomainInfo(t *testing.T) {
	info := Domain{}.Info()
	if info.Scheme != "sra" {
		t.Errorf("Scheme = %q, want sra", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, Host)
	}
	if info.Identity.Binary != "sra" {
		t.Errorf("Identity.Binary = %q, want sra", info.Identity.Binary)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct{ in, typ, id string }{
		{"38055985", "run", "38055985"},
		{"SRR21234567", "run", "SRR21234567"},
		{"ERR123456", "run", "ERR123456"},
		{"DRR999999", "run", "DRR999999"},
		{"human RNA", "run", "human RNA"},
	}
	for _, tc := range cases {
		typ, id, err := Domain{}.Classify(tc.in)
		if err != nil || typ != tc.typ || id != tc.id {
			t.Errorf("Classify(%q) = (%q, %q, %v), want (%q, %q, nil)",
				tc.in, typ, id, err, tc.typ, tc.id)
		}
	}
}

func TestClassifyEmpty(t *testing.T) {
	_, _, err := Domain{}.Classify("")
	if err == nil {
		t.Error("Classify(\"\") should return an error")
	}
}

func TestLocate(t *testing.T) {
	cases := []struct {
		uriType, id, want string
	}{
		{"run", "38055985", "https://www.ncbi.nlm.nih.gov/sra/38055985"},
		{"run", "SRR21234567", "https://www.ncbi.nlm.nih.gov/sra/SRR21234567"},
	}
	for _, tc := range cases {
		got, err := Domain{}.Locate(tc.uriType, tc.id)
		if err != nil || got != tc.want {
			t.Errorf("Locate(%q, %q) = (%q, %v), want (%q, nil)",
				tc.uriType, tc.id, got, err, tc.want)
		}
	}
}

func TestLocateUnknownType(t *testing.T) {
	_, err := Domain{}.Locate("page", "foo")
	if err == nil {
		t.Error("Locate with unknown type should return an error")
	}
}

func TestHostWiring(t *testing.T) {
	h, err := kit.Open()
	if err != nil {
		t.Fatal(err)
	}

	r := &Run{ID: "38055985", Title: "RNA-seq of human brain"}
	u, err := h.Mint(r)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if want := "sra://run/38055985"; u.String() != want {
		t.Errorf("Mint = %q, want %q", u.String(), want)
	}

	got, err := h.ResolveOn("sra", "SRR21234567")
	if err != nil || got.String() != "sra://run/SRR21234567" {
		t.Errorf("ResolveOn = (%q, %v), want sra://run/SRR21234567", got.String(), err)
	}
}
