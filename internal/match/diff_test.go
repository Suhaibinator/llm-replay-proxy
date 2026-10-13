package match

import (
	"slices"
	"testing"
)

func diffAligned(t *testing.T, request, recorded string) []Difference {
	t.Helper()
	var a, b any
	if err := DecodeJSON([]byte(request), &a); err != nil {
		t.Fatal(err)
	}
	if err := DecodeJSON([]byte(recorded), &b); err != nil {
		t.Fatal(err)
	}
	d := []Difference{}
	DiffAligned("", a, b, &d)
	return d
}

func paths(d []Difference) []string {
	out := []string{}
	for _, v := range d {
		p := v.Path
		if v.RecordedPath != "" {
			p += "~" + v.RecordedPath
		}
		if v.Kind != "" {
			p += " " + v.Kind
		}
		out = append(out, p)
	}
	return out
}

func TestDiffAlignedReportsInsertionsOnce(t *testing.T) {
	d := diffAligned(t, `{"input":["a","new","b","c"]}`, `{"input":["a","b","c"]}`)
	if got := paths(d); !slices.Equal(got, []string{"/input/1"}) || d[0].RecordedExists {
		t.Fatalf("insertion: %v", got)
	}
	// By index, every element after the insertion differs.
	var a, b any
	_ = DecodeJSON([]byte(`["a","new","b","c"]`), &a)
	_ = DecodeJSON([]byte(`["a","b","c"]`), &b)
	byIndex := []Difference{}
	DiffJSON("", a, b, &byIndex)
	if len(byIndex) != 3 {
		t.Fatalf("index diff: %v", paths(byIndex))
	}
}

func TestDiffAlignedReportsReordersAndMoves(t *testing.T) {
	d := diffAligned(t, `{"tools":["c","a","b"]}`, `{"tools":["a","b","c"]}`)
	if got := paths(d); !slices.Equal(got, []string{"/tools reordered"}) || d[0].RequestDisplay != "the same 3 elements in another order: request element i is recorded element [2 0 1][i]" {
		t.Fatalf("reorder: %v %q", got, d[0].RequestDisplay)
	}
	if d := diffAligned(t, `["a","b","c"]`, `["a","b","c"]`); len(d) != 0 {
		t.Fatalf("equal arrays: %v", paths(d))
	}
	// d moved from the front to the end; e only exists in the recording.
	d = diffAligned(t, `["a","b","c","d"]`, `["d","a","b","c","e"]`)
	if got := paths(d); !slices.Equal(got, []string{"/3~/0 moved", "/4"}) || d[1].RequestExists {
		t.Fatalf("move: %v", got)
	}
}

func TestDiffAlignedPairsChangedElementsAcrossShifts(t *testing.T) {
	d := diffAligned(t,
		`[{"role":"system"},{"role":"user","content":"hi"},{"role":"tool","output":"new"}]`,
		`[{"role":"user","content":"hi"},{"role":"tool","output":"old"}]`)
	if got := paths(d); !slices.Equal(got, []string{"/0", "/2/output~/1/output"}) {
		t.Fatalf("shifted change: %v", got)
	}
}
