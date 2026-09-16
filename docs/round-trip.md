# How the round trip works

The claim this file backs up: a localization file that goes through the pipeline
comes back out with every entry nobody touched byte-for-byte identical.

## Why re-serializing does not work

The obvious implementation is to unmarshal the XML into structs, change a field
and marshal it again. That loses on real files, because XML has many spellings
for the same document and Go's `encoding/xml` picks its own:

| In the file | What a marshal round trip produces |
| --- | --- |
| `<target state="new" xml:space="preserve"/>` | attributes reordered, namespace prefixes rewritten |
| `<target/>` | `<target></target>` |
| `&#246;` | `ö` |
| `&apos;` | `'` |
| two-space indentation | whatever the encoder does |
| `<!DOCTYPE TS>` | dropped |
| `<!-- translator note -->` | dropped unless explicitly modelled |

Every one of those appears in `testdata/oss`. A tool that rewrote them would
turn a one-string change into a diff touching the whole file, and would silently
drop information the file was carrying.

## What this does instead

The file is never regenerated. It is copied, and the few byte ranges that
changed are overwritten.

### 1. Scan with offsets

`internal/xmlsplice` wraps `xml.Decoder` and records where each token sits,
using `Decoder.InputOffset()`, which is the input position at the end of the
token just returned:

```go
func (s *Scanner) Next() (xml.Token, Span, error) {
    tok, err := s.dec.Token()
    ...
    span := Span{Start: s.prev, End: int(s.dec.InputOffset())}
    s.prev = span.End
    return xml.CopyToken(tok), span, nil
}
```

Because `s.prev` is carried forward, the spans are contiguous: the whitespace
between two elements is its own `CharData` token with its own span, so slicing
the source by every token's span reconstructs the document exactly. That is
asserted directly in `TestScannerSpansCoverTheDocument`.

A self-closing `<target/>` needs no special case. The decoder returns a start
tag and then a synthetic end tag at the same input position, so the "content"
span comes out empty and the element span still covers `<target/>`.

### 2. Record where each entry's translation lives

Parsing an entry records a `slot`:

```go
type slot struct {
    exists   bool
    elem     xmlsplice.Span // the whole <target>…</target>
    attrs    xmlsplice.Span // raw attribute text inside the start tag
    inner    xmlsplice.Span // content between the tags
    insertAt int            // where to put one when the element is missing
    indent   string         // whitespace to line an inserted element up with
}
```

Keeping `attrs` as a raw byte range is what preserves attributes nobody asked
about. To mark an entry reviewed, the pipeline edits that text in place:

```go
func setAttr(raw, name, value string) string {
    // replace this attribute's value where it already is, leaving every other
    // attribute's order, spacing and quoting exactly as it was …
    return raw + fmt.Sprintf(` %s="%s"`, name, escapeAttr(value))  // … or append
}
```

So `<target state="new" xml:space="preserve">` becomes
`<target state="translated" xml:space="preserve">` and nothing else moves.

### 3. Splice

`Bytes()` collects one edit per changed entry and hands them to `Apply`, which
checks the spans do not overlap and are in range, then copies:

```go
prev := 0
for _, e := range sorted {
    out = append(out, src[prev:e.Start]...)   // untouched bytes, copied
    out = append(out, e.New...)               // the replacement
    prev = e.End
}
return append(out, src[prev:]...)
```

With no edits the loop does not run and the output is the input.

### 4. Only rewrite what actually changed

The export step decides per entry whether to emit an edit at all:

```go
worked := group[0].Origin != "import" || group[0].ReviewedBy != nil
if !worked && sameStrings(targets, u.Targets) {
    continue
}
```

An entry that arrived with the file and was never opened produces no edit, so
its bytes are copied. This is why signing off 2,445 qBittorrent translations and
exporting changes 9 lines of a 13,530-line file: only the nine entries that were
marked `type="unfinished"` had anything to say.

## How it is tested

In `internal/format/roundtrip_test.go`, over all five fixtures:

- `TestWriteWithoutEditsIsIdentical` — parse, write, `bytes.Equal` with the input.
- `TestUntouchedEntriesAreByteIdentical` — edit every seventh entry, reparse the
  output, and compare the raw bytes of every *other* entry between input and
  output. 2,791 entries are checked in the PeerTube file alone.
- `TestOnlyDeclaredSpansChange` — walk the declared edit ranges and assert that
  everything between them, including the prolog, comments and indentation, is
  unchanged.

In `internal/pipeline/roundtrip_test.go` the same two guarantees are asserted
end to end through Postgres, so they hold for the real import and export path
and not only for the parser.

## Where translations are not byte-preserved

Only inside entries somebody edited, which is the point. Two details worth
knowing:

- Translations are stored as raw inner XML, so inline tags such as
  `<x id="INTERPOLATION"/>` are carried through untouched rather than being
  decoded and re-encoded. A submitted translation that is not well-formed is
  rejected before it is stored.
- A Qt plural entry that is edited has its `<numerusform>` children regenerated,
  since the number of forms can change. Unedited plural entries are untouched
  like anything else.
