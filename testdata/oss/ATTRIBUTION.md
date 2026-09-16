# Third-party test fixtures

The files in this directory are **unmodified translation files taken from open
source projects**. They are included so the round-trip and quality checks are
tested against files real tools actually produce, with the quirks that come with
them: several `<file>` elements in one document, self-closing `<source/>` and
`<target/>`, inline `<x id="..."/>` placeholders, Qt `<numerusform>` plurals,
`type="unfinished"` and `type="vanished"` markers, and non-breaking spaces.

Each file keeps the licence of the project it came from. They are test data, not
part of the pipeline's own source, and the pipeline itself is MIT licensed (see
`LICENSE` at the repository root).

Retrieved 2026-09-16.

| File | Upstream project | Path in that project | Licence |
| --- | --- | --- | --- |
| `symfony-validators.de.xlf` | [symfony/symfony](https://github.com/symfony/symfony) (branch `7.2`) | `src/Symfony/Component/Validator/Resources/translations/validators.de.xlf` | MIT |
| `firefox-ios.de.xliff` | [mozilla-l10n/firefoxios-l10n](https://github.com/mozilla-l10n/firefoxios-l10n) (branch `main`) | `de/firefox-ios.xliff` | MPL-2.0 |
| `peertube-angular.fr-FR.xlf` | [Chocobozzz/PeerTube](https://github.com/Chocobozzz/PeerTube) (branch `develop`) | `client/src/locale/angular.fr-FR.xlf` | AGPL-3.0 |
| `qbittorrent.de.ts` | [qbittorrent/qBittorrent](https://github.com/qbittorrent/qBittorrent) (branch `master`) | `src/lang/qbittorrent_de.ts` | GPL-2.0-or-later (with OpenSSL exception) |
| `keepassxc.de.ts` | [keepassxreboot/keepassxc](https://github.com/keepassxreboot/keepassxc) (branch `develop`) | `share/translations/keepassxc_de.ts` | GPL-2.0 / GPL-3.0 |

## What each file contributes to the tests

| File | Format | Entries | Why it is here |
| --- | --- | --- | --- |
| `symfony-validators.de.xlf` | XLIFF 1.2 | 117 | Small and plain; Twig-style `{{ name }}` placeholders |
| `firefox-ios.de.xliff` | XLIFF 1.2 | 1,949 | Xcode export: 97 `<file>` elements in one document, `<note>` on every entry, ObjC `%@` and `%1$@` placeholders |
| `peertube-angular.fr-FR.xlf` | XLIFF 1.2 | 3,257 | Angular export: 2,452 inline `<x id="..."/>` placeholders, `state` attributes, ICU plural messages, one entry with an empty `<source/>` |
| `qbittorrent.de.ts` | Qt Linguist | 2,519 | Qt `%1`/`%L1` placeholders, `type="unfinished"` and `type="vanished"` |
| `keepassxc.de.ts` | Qt Linguist | 2,467 | 55 `<numerusform>` plural messages, `%n` count placeholders, escaped HTML in sources |

## Findings in these files

The QA checks flag three placeholder defects in the shipped KeePassXC German
translation, where a count placeholder was dropped and the singular hardcoded.
They are asserted by name in `internal/checks/checks_test.go`, so the count is a
real result rather than a claim:

- `Are you sure you want to remove %n attachment(s)?` → `Sind Sie sicher, dass Sie einen Anhang löschen möchten?` (`%n` missing)
- `Password for '%1' has been leaked %2 time(s)!` → `Passwort für '%1' wurde einmal in Datenlecks gefunden!` (`%2` missing)
- `Password is used %1 time(s)` → `Passwort wird einmal verwendet` (`%1` missing)

The consistency report finds 16 English strings in qBittorrent that are shipped
with more than one German wording, for example `Download` as both "Download" and
"Herunterladen", and `Never` as both "Nie" and "Niemals".

None of these are bugs in this pipeline; they are what the checks are for.

## Refreshing the fixtures

`testdata/fetch.sh` downloads the same paths again. The tests assert exact
counts against the committed copies, so re-running it will require updating
those numbers.
