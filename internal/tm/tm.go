// Package tm holds the translation memory's matching rules: how a source
// string is normalised for lookup, and how exact and semantic hits are merged
// into one ranked list of suggestions.
package tm

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/Saitumu12/localization-pipeline/internal/store"
)

// Normalize collapses whitespace so that strings differing only in wrapping or
// indentation count as the same entry. Case is preserved: "Save" and "save" are
// different strings to a translator.
func Normalize(plain string) string {
	return strings.Join(strings.Fields(plain), " ")
}

// Hash keys the exact-match index. Hashing keeps the index small and fixed
// width even when a source string runs to several paragraphs.
func Hash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// Merge combines exact and semantic hits into one list. Exact matches always
// rank first, duplicates of the same translation are dropped, and the rest are
// ordered by similarity.
func Merge(exact, semantic []store.Match, limit int) []store.Match {
	sort.SliceStable(semantic, func(i, j int) bool { return semantic[i].Score > semantic[j].Score })

	out := make([]store.Match, 0, limit)
	seen := map[string]bool{}
	for _, group := range [][]store.Match{exact, semantic} {
		for _, m := range group {
			if seen[m.TargetText] {
				continue
			}
			seen[m.TargetText] = true
			out = append(out, m)
			if len(out) == limit {
				return out
			}
		}
	}
	return out
}
