package idmap

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type fixture struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
	Cases     []struct {
		Canonical string `json:"canonical"`
		Transport string `json:"transport"`
	} `json:"cases"`
}

func TestCrossLanguageFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if f.Version != 1 || f.Algorithm != "utf8-base64url-tilde-v1" || len(f.Cases) == 0 {
		t.Fatalf("fixture header = %+v", f)
	}
	for _, tc := range f.Cases {
		got, err := Encode(tc.Canonical)
		if err != nil {
			t.Fatalf("Encode(%q): %v", tc.Canonical, err)
		}
		if got != tc.Transport {
			t.Fatalf("Encode(%q) = %q, want %q", tc.Canonical, got, tc.Transport)
		}
		back, err := Decode(tc.Transport)
		if err != nil {
			t.Fatalf("Decode(%q): %v", tc.Transport, err)
		}
		if back != tc.Canonical {
			t.Fatalf("Decode(%q) = %q, want %q", tc.Transport, back, tc.Canonical)
		}
	}
}

func TestUnreservedASCIIStaysReadable(t *testing.T) {
	for _, id := range []string{"mio", "shiro", "1f-living", "a~b", "a.b_c-1"} {
		got, err := Encode(id)
		if err != nil || got != id {
			t.Fatalf("Encode(%q) = %q, %v", id, got, err)
		}
	}
}

func TestReservedASCIIIsEncoded(t *testing.T) {
	for _, id := range []string{"a:b", "a/b", "a=b", "a+b", "a?b", "a#b", "a%b", "a b"} {
		got, err := Encode(id)
		if err != nil || got == id || got[0] != '~' {
			t.Fatalf("Encode(%q) = %q, %v", id, got, err)
		}
		back, err := Decode(got)
		if err != nil || back != id {
			t.Fatalf("Decode(%q) = %q, %v", got, back, err)
		}
	}
}

func TestEncodeRejectsInvalidCanonicalIDs(t *testing.T) {
	cases := []string{
		"",
		" mio",
		"mio ",
		"mio\n",
		"mio\t",
		"mio\x00",
		"mio\x7f",
		string([]byte{0xff}),
		"\u00a0mio",
		"mio\u00a0",
	}
	for _, id := range cases {
		if _, err := Encode(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Encode(%q) err = %v", id, err)
		}
	}
}

func TestDecodeRejectsNonCanonicalTransport(t *testing.T) {
	cases := []string{
		"",
		"~",
		"~44Of44Kq=",
		"~44Of44Kq\n",
		"~!!!!",
		"~bWlv=",
		"mio ",
		"a b",
		"ミオ",
		"mio\n",
		"~44Of44Kq~",
	}
	for _, id := range cases {
		if _, err := Decode(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("Decode(%q) err = %v", id, err)
		}
	}
}

func TestDecodeDoesNotNormalize(t *testing.T) {
	// A composed and a decomposed form remain distinct; the mapping never
	// applies Unicode normalization.
	a := "ミオ"
	b := "ミ" + "\u3099" + "オ"
	ea, _ := Encode(a)
	eb, _ := Encode(b)
	if ea == eb {
		t.Fatalf("normalized distinct IDs: %q", ea)
	}
	da, _ := Decode(ea)
	db, _ := Decode(eb)
	if da != a || db != b || da == db {
		t.Fatalf("round-trip changed IDs: %q %q", da, db)
	}
}
