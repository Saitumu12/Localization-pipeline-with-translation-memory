# Two numbers that needed measuring

Both defaults in this project started as guesses that turned out to be wrong on
real files. This is what replaced them.

## The similarity threshold for memory suggestions

`pipeline.DefaultMinScore` is 0.60 cosine similarity. Below that a suggestion is
not shown.

### Setup

The qBittorrent German translation was imported and signed off, giving a memory
of 2,203 entries from 2,445 approved translations. Sources are embedded with
`sentence-transformers/all-MiniLM-L6-v2` and ranked by pgvector's cosine
distance.

### Rewordings of strings that are in the memory

Four English strings from the memory were reworded by hand so they share almost
no distinctive vocabulary with the original, then looked up again. Only an
embedding search can connect these; a substring or trigram search cannot.

| Query | Top match | Score |
| --- | --- | --- |
| Do you really want to exit qBittorrent? | Are you sure you want to quit qBittorrent? | 0.924 |
| Select a directory for exported .torrent files | Choose folder to save exported .torrent files | 0.898 |
| Ask for confirmation before removing torrents | Confirm when deleting torrents | 0.864 |
| Resolve host names through the proxy | Perform hostname lookup via proxy | 0.850 |

These four are asserted in `internal/pipeline/tm_test.go`, which requires the
top match to be semantic, to be the right translation, and to score above 0.70.

### Strings that are not in the memory

27 English strings were sampled from KeePassXC — a different application, the
same broad domain — and looked up against the qBittorrent memory with the
threshold switched off. This is the hard case: mostly there is no right answer,
and the search should say so.

| Score band | Count | What the top match looked like |
| --- | --- | --- |
| 0.70 – 0.75 | 1 | "Download successful." → "Download completed" — usable |
| 0.60 – 0.70 | 3 | mixed: "Limit the total size of history items per entry to:" → "History length" is weak context; "Password is used %1 time(s)" → "Invalid password" is wrong |
| 0.40 – 0.60 | 17 | unrelated |
| below 0.40 | 6 | unrelated |

Obviously unrelated text ("Recipe for chocolate cake", "The rain in Spain falls
mainly on the plain") returns nothing at all, even with the threshold dropped to
0.30.

### Conclusion

- **≥ 0.85** — almost always a rewording of an entry in the memory.
- **0.70 – 0.85** — related and usually worth offering.
- **0.60 – 0.70** — mixed. Shown, because a reviewer can dismiss a suggestion in
  a moment but cannot recover one they never saw.
- **< 0.60** — noise, hidden.

Reproduce it with `locctl suggest -project N -text "..." -min-score 0.01`.

## The character expansion allowance

The length check started as a single percentage: a translation could be up to
130% of the source, with a 20 character floor. On the shipped German files it
fired on a fifth of every file:

| File | Entries | Length warnings at a flat 130% |
| --- | --- | --- |
| qbittorrent.de.ts | 2,502 | 532 (21%) |
| keepassxc.de.ts | 2,522 | 663 (26%) |

Those were not bugs. "Copy to clipboard" → "In die Zwischenablage kopieren" is
+76% and completely normal German. A check that flags a fifth of a correct file
is one people turn off.

The problem is that expansion is not linear: short labels grow much more than
sentences, because a sentence has room to absorb a longer word. So the allowance
shrinks as the source gets longer, following the usual localization rule of
thumb, with an absolute floor because a percentage is meaningless on a
two-character label ("OK" → "Einverstanden" is fine).

| Source length | Allowance |
| --- | --- |
| ≤ 10 | 300% |
| ≤ 20 | 220% |
| ≤ 30 | 180% |
| ≤ 50 | 160% |
| ≤ 70 | 140% |
| > 70 | 130% |

with a floor of 25 characters, and scaled by the project's `length_tolerance`.

| File | Entries | Length warnings now |
| --- | --- | --- |
| qbittorrent.de.ts | 2,502 | 90 (3.6%) |
| keepassxc.de.ts | 2,522 | 101 (4.0%) |

What remains are genuine near-misses, for example "Add to top of queue" → "In der
Warteschlange an erster Stelle hinzufügen" at 48 characters against a budget of
41 — the sort of string that does overflow a narrow menu.

This is a heuristic and stays a warning. The hard error is reserved for a budget
the file states itself, through XLIFF's `maxwidth` with `size-unit="char"`.

Reproduce it with `locctl issues -file N -kind length`.
