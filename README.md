# Localization pipeline

A translation review tool for XLIFF 1.2 and Qt Linguist `.ts` files. It imports
a localization file, keeps the uploaded bytes, runs quality checks on every
translation, suggests earlier translations from a translation memory, and writes
the file back out with only the entries somebody actually changed rewritten.

Go for the server and the file handling, PostgreSQL with pgvector for storage and
the memory's nearest-neighbour search, React for the review UI.

**Reading this repo:** the idea worth looking at first is
[internal/xmlsplice](internal/xmlsplice/xmlsplice.go) and how
[internal/format](internal/format/xliff.go) uses it, explained in
[docs/round-trip.md](docs/round-trip.md). The two defaults that had to be
measured rather than guessed are in [docs/tuning.md](docs/tuning.md).

## Why the file handling works the way it does

Localization files live in version control next to the code. A tool that
re-serializes them produces a diff on every line even when one string changed:
attribute order moves, self-closing tags expand, entities get rewritten,
whitespace shifts. That makes review impossible and loses information the file
was carrying.

This pipeline never re-serializes. Parsing records the byte offset of every
token, so each entry's `<target>` or `<translation>` element is known as a byte
range in the original file. Writing copies the uploaded bytes and splices new
text into just those ranges:

```
original:  … <trans-unit id="7"><source>Save</source><target>Speichern</target></trans-unit> …
                                                     └──────── byte range 412..446 ────────┘
export:    everything outside the replaced ranges is memcpy'd from the input
```

Two consequences, both tested against real files:

- A file that is imported and exported with no edits is **byte-for-byte identical**.
- After editing some entries, every other entry is **byte-for-byte what it was**,
  including comments, indentation, the XML prolog and `<!DOCTYPE TS>`.

An entry is only rewritten when its translation changed or somebody worked on it
here. Signing off 2,445 qBittorrent translations and exporting **changed 9 lines of a
13,530-line file** — exactly the nine entries whose `type="unfinished"` marker
was removed.

## Running it

Postgres with pgvector and the embedding service both run in Docker:

```sh
docker compose up -d          # postgres on :5433, embeddings on :8081
cd web && npm install && npm run build && cd ..
go run ./cmd/server           # http://localhost:8080
```

The first `docker compose up` builds the embedding service, which installs
onnxruntime and bakes the model into the image. That takes a few minutes and
needs network access; afterwards the container starts offline in a second or
two. `docker compose ps` shows both services as healthy when they are ready.

The server applies its own migrations at startup. For UI work, `npm run dev` in
`web/` serves the front end on :5173 and proxies `/api` to the Go server.

Machine translation is off unless `ANTHROPIC_API_KEY` is set. Everything else
works without it.

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | `postgres://loc:locpass@localhost:5433/loc?sslmode=disable` | Postgres connection string |
| `EMBEDDINGS_URL` | `http://localhost:8081` | the embedding service |
| `ANTHROPIC_API_KEY` | unset | enables machine translation |
| `LLM_MODEL` | `claude-sonnet-5` | model used for first-pass translations |
| `ADDR` | `:8080` | listen address |
| `WEB_DIR` | `web/dist` | built UI to serve |

### A run from the terminal

```sh
go build -o locctl ./cmd/locctl

./locctl new-project -name qbittorrent-de -source en -target de
./locctl import -project 1 testdata/oss/qbittorrent.de.ts
./locctl files -project 1
./locctl issues -file 1 -kind placeholder

# Sign off the translations the file arrived with, under a named reviewer,
# which is also what loads them into the memory.
./locctl adopt -file 1 -reviewer "sai"

./locctl suggest -project 1 -text "Do you really want to exit qBittorrent?"
./locctl inconsistencies -project 1
./locctl export -file 1 -o /tmp/out.ts
```

## The review workflow

Each translation has a status and an origin. Status is where it is in review;
origin is where the current text came from.

| Status | Meaning |
| --- | --- |
| `untranslated` | no translation yet |
| `draft` | has text, nobody has signed it off |
| `needs_review` | queued for a reviewer — this is where machine output lands |
| `approved` | a named person signed it off |
| `rejected` | sent back |

| Origin | Meaning |
| --- | --- |
| `import` | arrived inside the uploaded file |
| `human` | typed by a person here |
| `machine` | produced by the model |
| `memory` | a memory suggestion someone accepted |

Approval is the only route into the translation memory, and it is refused while
the entry has a blocking finding.

### Re-importing

Uploading the same file again is the normal case, not an edge case: a project
re-runs `lupdate` or its equivalent whenever the source strings change, and
uploads the result. The uploaded file is authoritative, so the segment set is
rebuilt from it, but review state is not thrown away:

- An entry whose source **and** translation both come back unchanged keeps its
  status, origin and reviewer. It is the same work.
- An entry whose source was reworded, or whose translation differs from the one
  on record, starts again from what the file says, with no reviewer. Approval
  applies to a specific pair of texts, not to a slot in the file.
- An entry the new file no longer contains disappears from the queue. Its
  translation memory entries survive, because those belong to the project.

The import reports how many entries kept their review state.

### Typing a translation

Translations are stored as the raw XML fragment that sits inside the element, so
inline placeholders such as `<x id="INTERPOLATION"/>` are carried through
untouched instead of being decoded and re-encoded.

A bare `&` is escaped on the way in rather than refused, because Qt menu labels
are full of accelerators and a translator typing `&Datei` means a literal
ampersand. A bare `<` is still refused: it is either markup or a mistake, and
guessing which would risk changing what the file says.

## Quality checks

Checks run on import, on every save, and on demand after the glossary changes.
An **error** blocks approval; a **warning** is shown to the reviewer.

**Placeholder mismatch** (error). Source and translation are compared as a
multiset of placeholders, so a translation may reorder them but may not drop,
duplicate or invent one. The syntaxes recognised are the ones the fixtures
actually contain: printf (`%s`, `%.2f`, `%1$@`, `%@`), Qt (`%1`, `%L2`, `%n`),
Twig (`{{ name }}`), ICU and brace arguments (`{count}`, `{0}`,
`{count, plural, …}`), Python named (`%(name)s`), and XLIFF inline elements
(`<x id="INTERPOLATION"/>`, `<g>`, `<ph>`).

Which percent syntax wins is decided by the file format, because `%1d` is Qt's
first argument followed by a literal `d` in a `.ts` file but a printf integer in
an XLIFF file exported from C or Objective-C. Both readings appear in the
fixtures.

**Character budget** (error when the file declares one, warning otherwise).
XLIFF's own `maxwidth`/`size-unit="char"` attributes are a hard limit. Where a
file declares nothing, the translation is compared against a sliding allowance:
short labels are allowed to expand much further than sentences, because they
genuinely do. Length is counted in visible characters — markup stripped,
entities resolved, runes not bytes.

**Terminology** (warning), in two forms. A per-project glossary says that when
the source contains a term, the translation is expected to use an agreed
wording. A term has to start a word but may carry an inflected ending, so
"Torrent" is satisfied by "Torrents" and "Torrentdatei" but not by the "tab"
inside "establish". Insisting on the exact word flagged 30 correct German
strings in qBittorrent, against 4 that genuinely drop the term. The boundary is
tested with `unicode` rather than `\b`, which is ASCII-only in Go and would
split a word like `Größe` down the middle.

Separately, a translation that disagrees with wording already approved for the
identical source string elsewhere in the project is flagged, and the project
page lists every source string with more than one approved wording.

## Translation memory

Only translations a human approved are stored. A lookup returns two things:

1. **Exact matches** on the normalised source (whitespace collapsed, case kept).
2. **Semantic matches**: the source is embedded with
   `sentence-transformers/all-MiniLM-L6-v2` (384 dimensions, served by the
   Python sidecar in `embedsvc/`) and pgvector ranks the memory by cosine
   distance with `ORDER BY embedding <=> $1`, served by an HNSW index.

This is what finds a prior translation for a string that was reworded rather
than repeated. Against the qBittorrent memory (2,203 entries from 2,445 approved
translations):

```
$ locctl suggest -project 1 -limit 1 -text "Do you really want to exit qBittorrent?"
semantic 0.924  Are you sure you want to quit qBittorrent?
         -> Sind Sie sicher, dass Sie qBittorrent beenden möchten?   (approved by sai)

$ locctl suggest -project 1 -limit 1 -text "Ask for confirmation before removing torrents"
semantic 0.864  Confirm when deleting torrents
         -> Löschen von Torrents bestätigen   (approved by sai)

$ locctl suggest -project 1 -limit 1 -text "Recipe for chocolate cake"
no suggestions above the similarity threshold
```

Suggestions below 0.60 cosine similarity are not shown; see
[docs/tuning.md](docs/tuning.md) for how that number was picked.

## Machine translation goes to review, never to release

The model is asked for a first pass over selected segments. What comes back is
text and nothing else:

- `internal/llm` returns `[]string`. It has no access to the store and no way to
  set a status, record a reviewer or write to the memory.
- `pipeline.Pretranslate` always writes `status = needs_review`,
  `origin = machine`, and no reviewer. There is no argument that could change
  that.
- The `segments` table refuses any approved row without a reviewer:

  ```sql
  CONSTRAINT approved_needs_a_reviewer
      CHECK (status <> 'approved' OR (reviewed_by IS NOT NULL AND reviewed_by <> ''))
  ```

- Model output runs through the same checks as anything else. If it drops a
  placeholder or overflows a budget, approval is blocked until a person fixes it.
- Output that is not well-formed XML is discarded rather than stored, because it
  would corrupt the exported file.
- On export an unreviewed translation carries the file format's own marker:
  `type="unfinished"` in Qt, `state="needs-review-translation"` in XLIFF, and no
  `approved="yes"`. Exporting with `-approved-only` leaves unreviewed entries
  exactly as the upstream file had them.

## Using it with a Qt application

The `.ts` file is the whole contract. The pipeline reads and writes that XML and
nothing else — it never sees the application's source code or its database.

```sh
# 1. The app extracts its strings. Existing translations are kept; new strings
#    arrive as type="unfinished", removed ones as type="vanished".
pyside6-lupdate main.py ui/*.py -ts translations/app_de.ts

# 2. Translate and review.
locctl import -project 1 translations/app_de.ts
#    ... work in the UI at http://localhost:8080 ...
locctl export -file 1 -o translations/app_de.ts

# 3. Qt compiles the result and the app loads it.
pyside6-lrelease translations/app_de.ts -qm translations/app_de.qm
```

Step 2 rewrites only the entries that were actually translated, so the diff a
reviewer sees in the pull request is the translation work and nothing else.

This loop was checked against a real PySide6 application: 81 messages went in
untranslated, came back with 82 approved segments (the plural message holds two
forms), and `lrelease` reported *81 finished, 0 unfinished*. Loading the
compiled `.qm` back into Qt resolved every string, including the `&File`
accelerator, `Sold %1 x %2 for %3.`, both plural forms selected by count, and
`Add Product` translated separately in the two contexts that define it.

## Testing

```sh
docker compose up -d
export TEST_DATABASE_URL="postgres://loc:locpass@localhost:5433/loc_test?sslmode=disable"
export EMBEDDINGS_URL="http://localhost:8081"
go test ./...
```

The tests that need Postgres or the embedding service skip themselves when those
variables are unset, so `go test ./...` still runs the parser, placeholder and
check tests on a bare checkout.

Tests run against five unmodified translation files taken from open source
projects — 10,309 entries in total. See
[testdata/oss/ATTRIBUTION.md](testdata/oss/ATTRIBUTION.md) for the projects and
their licences. The notable ones:

- Every fixture survives import and export **byte-identically**.
- After editing a sample of entries in each fixture, the bytes of every other
  entry are compared one by one against the input and must be identical — 2,791
  entries checked in the PeerTube file alone.
- The placeholder check finds exactly three defects across 10,246 shipped
  translations, and the test asserts that number. All three are real bugs in the
  KeePassXC German translation, where a count placeholder was dropped and the
  singular hardcoded.
- The Anthropic client is tested against a stub of the Messages API: request
  shape, glossary in the prompt, fenced replies, wrong-length replies and API
  errors.

**Not verified here:** no Anthropic API key was available while this was built,
so the client has never made a live call. Everything on this side of the
network — the request it builds, how it parses replies, and the rule that output
lands in review — is tested against the stub. To check the live call, set
`ANTHROPIC_API_KEY`, start the server, and press "Draft with the model" on a
segment.

## Layout

```
cmd/server        HTTP API and the built UI
cmd/locctl        command line for import, sign-off, checks, memory, export
internal/xmlsplice  token scanning with byte offsets, and the splice
internal/format     XLIFF 1.2 and Qt .ts parsing and writing
internal/placeholder  placeholder extraction and comparison
internal/checks     the quality gates
internal/store      SQL, schema migrations
internal/tm         memory normalisation, hashing, ranking
internal/embed      client for the embedding service
internal/llm        Anthropic Messages client
internal/pipeline   the service layer that ties those together
internal/api        HTTP handlers
embedsvc          the Python embedding sidecar
web               React review UI
testdata/oss      unmodified translation files from open source projects
```

## Limitations

- There is no authentication. The reviewer's name comes from a field in the UI
  header. A real deployment would take it from the session.
- XLIFF 1.2 and Qt `.ts` only. XLIFF 2.0, `.po`, `.strings` and Android
  `strings.xml` are not implemented.
- Inside an ICU plural or select message, only the argument itself is treated as
  a placeholder. The nested sub-messages are translatable text, so their braces
  are not compared.
- Inline tags are visible in the editor as text. Bare `&` is handled, but the
  editor is not otherwise tag-aware: it will not stop a translator from mangling
  an `<x id="..."/>` by hand, it will only flag it afterwards.
- Only the first `<file>` element's language pair is read from a multi-file
  XLIFF document, and the import fails if the others disagree with it.
- Qt plural forms are stored as one row per form. The memory indexes only the
  first form, so a single source string does not produce several competing
  answers.
- The embedding model is English-centric. Lookups are done on the source text,
  which is English in all the fixtures.

## Licence

MIT, see [LICENSE](LICENSE). The files under `testdata/oss/` keep the licences of
the projects they came from.
