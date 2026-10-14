package match

import (
	"fmt"
	"strings"
)

// Difference is one JSON Pointer path at which a request and a recorded
// request differ.
type Difference struct {
	Path            string `json:"path"`
	Request         any    `json:"request"`
	Recorded        any    `json:"recorded"`
	RequestExists   bool   `json:"request_exists"`
	RecordedExists  bool   `json:"recorded_exists"`
	RequestDisplay  string `json:"request_display"`
	RecordedDisplay string `json:"recorded_display"`
	// Excluded marks a path under one of the collection's match exclusions:
	// the difference did not affect matching.
	Excluded bool `json:"excluded"`
	// Kind is set by DiffAligned: "reordered" when an array holds the same
	// elements in another order, "moved" when one equal element sits at another
	// index. Other differences leave it empty.
	Kind string `json:"kind,omitempty"`
	// RecordedPath is set by DiffAligned when the recorded side of the
	// difference is at another path than Path, which is the request's.
	RecordedPath string `json:"recorded_path,omitempty"`
}

// MarkExcluded flags the differences that the collection's exclusions hide
// from matching.
func MarkExcluded(d []Difference, exclusions []string) []Difference {
	for i := range d {
		d[i].Excluded = Excluded(d[i].Path, exclusions)
	}
	return d
}

// DiffJSON appends the paths under path where request a and recorded b
// differ. Object keys are compared in sorted order and arrays by index.
func DiffJSON(path string, a, b any, out *[]Difference) {
	differ{out: out}.diff(path, path, a, b)
}

// DiffAligned is DiffJSON with arrays aligned by content instead of index:
// an inserted or removed element is one difference rather than a shift of
// every element after it, an array with the same elements in another order is
// one "reordered" difference, and an equal element at another index is one
// "moved" difference.
func DiffAligned(path string, a, b any, out *[]Difference) {
	differ{out: out, align: true}.diff(path, path, a, b)
}

// maxAlignCells bounds the table aligning two arrays; larger arrays are
// compared by index.
const maxAlignCells = 1 << 20

type differ struct {
	out   *[]Difference
	align bool
}

// add records a difference at the request path pa and recorded path pb and
// returns it for adjustment before the next one is added.
func (d differ) add(pa, pb string, a, b any, aexists, bexists bool) *Difference {
	v := NewDifference(pa, a, b, aexists, bexists)
	if !aexists {
		v.Path = pb
	} else if pb != pa {
		v.RecordedPath = pb
	}
	*d.out = append(*d.out, v)
	return &(*d.out)[len(*d.out)-1]
}

func (d differ) diff(pa, pb string, a, b any) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]struct{}{}
		for k := range am {
			keys[k] = struct{}{}
		}
		for k := range bm {
			keys[k] = struct{}{}
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sortStrings(names)
		for _, k := range names {
			av, aexists := am[k]
			bv, bexists := bm[k]
			ka, kb := pa+"/"+escapePointer(k), pb+"/"+escapePointer(k)
			if aexists && bexists {
				d.diff(ka, kb, av, bv)
			} else {
				d.add(ka, kb, av, bv, aexists, bexists)
			}
		}
		return
	}
	aa, aok := a.([]any)
	ba, bok := b.([]any)
	if aok && bok {
		if d.align && len(aa)*len(ba) <= maxAlignCells {
			d.aligned(pa, pb, aa, ba)
			return
		}
		for i := 0; i < max(len(aa), len(ba)); i++ {
			ia, ib := fmt.Sprintf("%s/%d", pa, i), fmt.Sprintf("%s/%d", pb, i)
			switch {
			case i >= len(aa):
				d.add(ia, ib, nil, ba[i], false, true)
			case i >= len(ba):
				d.add(ia, ib, aa[i], nil, true, false)
			default:
				d.diff(ia, ib, aa[i], ba[i])
			}
		}
		return
	}
	if !equalJSON(a, b) {
		if pa == "" {
			pa, pb = "/", "/"
		}
		d.add(pa, pb, a, b, true, true)
	}
}

// aligned diffs two arrays by their longest common subsequence of equal
// elements. Unmatched elements equal to one on the other side are reported
// as moved; the rest are paired in order between matched ones and diffed,
// and any left over are added or removed.
func (d differ) aligned(pa, pb string, aa, ba []any) {
	ka, kb := elementKeys(aa), elementKeys(ba)
	if order, ok := permutation(ka, kb); ok {
		if !isIdentity(order) {
			v := d.add(pa, pb, nil, nil, true, true)
			v.Kind = "reordered"
			v.RequestDisplay = fmt.Sprintf("the same %d elements in another order: request element i is recorded element %v[i]", len(aa), order)
			v.RecordedDisplay = fmt.Sprintf("%d elements", len(ba))
		}
		return
	}
	// lcs[i][j] is the common subsequence length of ka[i:] and kb[j:].
	lcs := make([][]int, len(ka)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(kb)+1)
	}
	for i := len(ka) - 1; i >= 0; i-- {
		for j := len(kb) - 1; j >= 0; j-- {
			if ka[i] == kb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	// Collect the unmatched runs between matched elements.
	type gap struct{ a, b []int }
	var gaps []gap
	var g gap
	for i, j := 0, 0; i < len(ka) || j < len(kb); {
		switch {
		case i < len(ka) && j < len(kb) && ka[i] == kb[j]:
			if len(g.a)+len(g.b) > 0 {
				gaps = append(gaps, g)
				g = gap{}
			}
			i++
			j++
		case j >= len(kb) || (i < len(ka) && lcs[i+1][j] >= lcs[i][j+1]):
			g.a = append(g.a, i)
			i++
		default:
			g.b = append(g.b, j)
			j++
		}
	}
	if len(g.a)+len(g.b) > 0 {
		gaps = append(gaps, g)
	}
	// An unmatched element equal to an unmatched one elsewhere has moved.
	free := map[string][]int{}
	for _, g := range gaps {
		for _, j := range g.b {
			free[kb[j]] = append(free[kb[j]], j)
		}
	}
	movedA, movedB := map[int]bool{}, map[int]bool{}
	for _, g := range gaps {
		for _, i := range g.a {
			if js := free[ka[i]]; len(js) > 0 {
				j := js[0]
				free[ka[i]] = js[1:]
				movedA[i], movedB[j] = true, true
				v := d.add(fmt.Sprintf("%s/%d", pa, i), fmt.Sprintf("%s/%d", pb, j), aa[i], ba[j], true, true)
				v.Kind = "moved"
				v.RequestDisplay = fmt.Sprintf("equal element moved from recorded index %d to request index %d", j, i)
				v.RecordedDisplay = v.RequestDisplay
			}
		}
	}
	for _, g := range gaps {
		as, bs := without(g.a, movedA), without(g.b, movedB)
		for k := 0; k < max(len(as), len(bs)); k++ {
			switch {
			case k >= len(as):
				j := bs[k]
				d.add(fmt.Sprintf("%s/%d", pa, j), fmt.Sprintf("%s/%d", pb, j), nil, ba[j], false, true)
			case k >= len(bs):
				i := as[k]
				d.add(fmt.Sprintf("%s/%d", pa, i), fmt.Sprintf("%s/%d", pb, i), aa[i], nil, true, false)
			default:
				i, j := as[k], bs[k]
				d.diff(fmt.Sprintf("%s/%d", pa, i), fmt.Sprintf("%s/%d", pb, j), aa[i], ba[j])
			}
		}
	}
}

func elementKeys(v []any) []string {
	keys := make([]string, len(v))
	for i, e := range v {
		b, _ := encode(e)
		keys[i] = string(b)
	}
	return keys
}

// permutation reports whether b holds exactly a's elements and, if so, the
// index in b of each element of a.
func permutation(a, b []string) ([]int, bool) {
	if len(a) != len(b) || len(a) < 2 {
		return nil, false
	}
	at := map[string][]int{}
	for j, k := range b {
		at[k] = append(at[k], j)
	}
	order := make([]int, len(a))
	for i, k := range a {
		js := at[k]
		if len(js) == 0 {
			return nil, false
		}
		order[i], at[k] = js[0], js[1:]
	}
	return order, true
}

func isIdentity(order []int) bool {
	for i, j := range order {
		if i != j {
			return false
		}
	}
	return true
}

func without(v []int, drop map[int]bool) []int {
	out := v[:0:0]
	for _, x := range v {
		if !drop[x] {
			out = append(out, x)
		}
	}
	return out
}

// NewDifference describes one differing value; a missing side has exists false.
func NewDifference(path string, request, recorded any, requestExists, recordedExists bool) Difference {
	return Difference{Path: path, Request: request, Recorded: recorded, RequestExists: requestExists, RecordedExists: recordedExists, RequestDisplay: displayJSON(request, requestExists), RecordedDisplay: displayJSON(recorded, recordedExists)}
}

func displayJSON(v any, exists bool) string {
	if !exists {
		return "(missing)"
	}
	b, _ := encode(v)
	return string(b)
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
func escapePointer(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, "~", "~0"), "/", "~1")
}
func equalJSON(a, b any) bool {
	x, _ := encode(a)
	y, _ := encode(b)
	return string(x) == string(y)
}

// DecodeJSON decodes raw into the tree matching uses: exact numbers, and
// duplicate names, invalid UTF-8 or trailing values rejected.
func DecodeJSON(raw []byte, dst *any) error {
	v, err := decode(raw)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}
