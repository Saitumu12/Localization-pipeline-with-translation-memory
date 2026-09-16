package format

import (
	"sort"

	"github.com/Saitumu12/localization-pipeline/internal/xmlsplice"
)

func changesOf(edits []xmlsplice.Edit) []Change {
	out := make([]Change, len(edits))
	for i, e := range edits {
		out[i] = Change{In: e.Span, OutLen: len(e.New)}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].In.Start < out[j].In.Start })
	return out
}
