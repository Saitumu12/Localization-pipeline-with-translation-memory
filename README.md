# Localization Pipeline

A review tool for translating XLIFF and Qt `.ts` files. You upload a
localization file, it gets checked and translated in a web UI, and you export
it back out — with the file itself staying intact, not regenerated from
scratch.

Backend is Go, storage is PostgreSQL with the pgvector extension, and the
review UI is React. There's also a command-line tool (`locctl`) that does
everything the UI does, for scripting or for people who'd rather not click
through a browser.

## Round-tripping the file exactly

Most tools that touch XLIFF or `.ts` files parse them into objects and then
write the objects back out as XML. That sounds fine until you actually diff
the result: attribute order shifts, self-closing tags get expanded,
whitespace moves, entities get re-encoded differently. One changed string
turns into a file-wide diff, which makes these things useless for code
review.

This pipeline doesn't do that. Instead of parsing-and-regenerating, it reads
the file while keeping track of exactly which bytes each translation lives
in, and when you edit an entry it only overwrites that slice of the original
file. Everything else — comments, formatting, entries nobody touched — comes
back out identical to what went in. There's a short writeup of how that
works in [docs/round-trip.md](docs/round-trip.md) if you want the mechanics.

## Suggesting prior translations

When a string gets reworded rather than repeated word-for-word, a plain
search won't find the old translation for it. So every approved translation
gets embedded and stored in Postgres with pgvector, and when you're working
on a new string the pipeline looks for the closest matches by meaning, not
just exact text. An exact match on the same source string still wins first,
but the semantic search is what catches the "this is basically the same
sentence but reworded" case that a normal lookup misses.

## Catching problems before they ship

A few checks run automatically on every translation:

- **Placeholders** — if the source has `%1`, `{{name}}`, `<x id="..."/>`, or
  similar, the translation has to have the same ones. It's fine to reorder
  them for grammar, but dropping or inventing one is an error.
- **Length** — some formats declare a hard character limit for a string; if
  they don't, the tool still warns when a translation looks unusually long
  for its source, using a sliding scale since short labels naturally expand
  more than full sentences do.
- **Terminology** — a glossary lets you say "this English word should always
  become that word," and it also flags when the same source line gets
  approved with two different wordings elsewhere in the project.

Placeholder and length problems that are severe enough block approval
outright; terminology issues are shown as warnings for a reviewer to weigh.

## Machine translation stays a draft, not a decision

You can ask the model for a first pass on a batch of untranslated strings.
Whatever comes back is saved as a draft that still needs a person to review
and approve it — there's no path in the code where a machine-written string
becomes "approved" on its own. It goes through the same placeholder and
length checks as anything a person types, and if it fails one of those, it
can't be approved until someone fixes it. The database itself won't let a
translation be marked approved without a named reviewer attached, so this
isn't just an app-level convention.

## Tested against real translation files

The tests don't just check against small hand-written examples — they run
against actual `.ts` and `.xlf` files pulled from real open-source projects
(see [testdata/oss](testdata/oss/ATTRIBUTION.md) for where they're from and
their licenses). After an edit, the tests compare every untouched entry
against the original byte-for-byte, so the round-trip claim above is backed
by more than a couple of toy cases.

## Running it

Postgres (with pgvector) and the embedding service both run in Docker:

```sh
docker compose up -d          # postgres on :5433, embeddings on :8081
cd web && npm install && npm run build && cd ..
go run ./cmd/server           # http://localhost:8080
```

The first `docker compose up` takes a few minutes because it has to build
the embedding service and download the model — after that it starts up
almost instantly. `docker compose ps` will show both services as healthy
once they're ready.

Machine translation only turns on if `ANTHROPIC_API_KEY` is set. Everything
else works fine without it.

| Variable | Default | What it's for |
| --- | --- | --- |
| `DATABASE_URL` | `postgres://loc:locpass@localhost:5433/loc?sslmode=disable` | Postgres connection |
| `EMBEDDINGS_URL` | `http://localhost:8081` | the embedding service |
| `ANTHROPIC_API_KEY` | unset | turns on machine translation |
| `LLM_MODEL` | `claude-sonnet-5` | model used for first-pass drafts |
| `ADDR` | `:8080` | listen address |
| `WEB_DIR` | `web/dist` | built UI to serve |

### Or from the terminal

```sh
go build -o locctl ./cmd/locctl

./locctl new-project -name qbittorrent-de -source en -target de
./locctl import -project 1 testdata/oss/qbittorrent.de.ts
./locctl files -project 1
./locctl issues -file 1 -kind placeholder

# Sign off everything the file already had translated, which also
# loads it into the translation memory.
./locctl adopt -file 1 -reviewer "sai"

./locctl suggest -project 1 -text "Do you really want to exit qBittorrent?"
./locctl inconsistencies -project 1
./locctl export -file 1 -o /tmp/out.ts
```

## How review works

Every translation has a status (where it is in the review process) and an
origin (where the text came from — typed by a person, produced by the model,
pulled from the translation memory, or already in the file on import).
Approval is the only way a translation reaches the memory, and it's refused
while a blocking issue is still open on it.

Re-uploading a file you've worked on before isn't a reset — this is the
normal workflow when someone re-runs their string extraction tool after
changing the source code. An entry whose source and translation both come
back unchanged keeps whatever review status it already had. An entry whose
wording actually changed starts over, because approval is tied to a specific
pair of texts, not a slot in the file.

Typing an `&` in a translation (very common in Qt menu labels, like `&File`)
just works — it gets escaped automatically instead of being rejected for
looking like broken XML.

## I built a small app to test this against

[ShopTrack](https://github.com/Saitumu12/ShopTrack) is a tiny inventory
tracker I wrote specifically so this project would have a real, messy Qt
`.ts` file to work against instead of only hand-crafted test cases —
accelerators, plural forms, disambiguated contexts, the works. The loop
(extract strings → translate here → export → compile with Qt's own
`lrelease`) works end to end on it.

## Testing

```sh
docker compose up -d
export TEST_DATABASE_URL="postgres://loc:locpass@localhost:5433/loc_test?sslmode=disable"
export EMBEDDINGS_URL="http://localhost:8081"
go test ./...
```

Tests that need Postgres or the embedding service skip themselves if those
environment variables aren't set, so `go test ./...` still runs the core
parsing and checking tests on a plain checkout with nothing running.

One thing that isn't covered: the Anthropic client has only ever been tested
against a local stub of the API, never the live one, since I didn't have a
key available while building this. Everything up to the network call is
tested — the request it builds, how it reads the response back, the rule
that output always lands as a draft. To see the real call, set
`ANTHROPIC_API_KEY` and use "Draft with the model" in the UI.

## Layout

```
cmd/server            HTTP API and the built UI
cmd/locctl            command line version of the same thing
internal/xmlsplice     the byte-offset scanning and splicing
internal/format        XLIFF and Qt .ts parsing and writing
internal/placeholder   placeholder extraction and comparison
internal/checks        the quality checks
internal/store         SQL and schema migrations
internal/tm            translation memory logic
internal/embed         client for the embedding service
internal/llm           Anthropic client
internal/pipeline      service layer tying it together
internal/api           HTTP handlers
embedsvc              the Python embedding sidecar
web                   the React UI
testdata/oss          real translation files used in tests
```

## Limitations

- No authentication — the reviewer name is just a field in the UI. A real
  deployment would pull it from a session instead.
- Only XLIFF and Qt `.ts`. No XLIFF 2.0, `.po`, `.strings`, or Android
  `strings.xml`.
- Inside an ICU plural/select message, only the variable itself counts as a
  placeholder — the nested text isn't checked.
- The editor treats inline tags as visible text rather than protecting them,
  so a translator could still mangle a placeholder tag by hand; it gets
  caught afterward, not prevented.
- A multi-file XLIFF document only reads the language pair from the first
  file, and import fails if the others don't match it.
- Qt plural entries only index their first form in the memory.
- The embedding model works best on English source text, which is all the
  test files use anyway.

## License

MIT, see [LICENSE](LICENSE). Files under `testdata/oss/` keep whatever
license the project they came from uses.
