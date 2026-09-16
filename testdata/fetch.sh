#!/usr/bin/env bash
# Re-downloads the open source translation files used as test fixtures.
# They are committed to the repository, so this is only needed to refresh them.
# See oss/ATTRIBUTION.md for the projects and their licences.
set -euo pipefail

cd "$(dirname "$0")/oss"

fetch() {
  echo "fetching $2"
  curl -sSfL "$1" -o "$2"
}

fetch "https://raw.githubusercontent.com/symfony/symfony/7.2/src/Symfony/Component/Validator/Resources/translations/validators.de.xlf" \
      "symfony-validators.de.xlf"

fetch "https://raw.githubusercontent.com/mozilla-l10n/firefoxios-l10n/main/de/firefox-ios.xliff" \
      "firefox-ios.de.xliff"

fetch "https://raw.githubusercontent.com/Chocobozzz/PeerTube/develop/client/src/locale/angular.fr-FR.xlf" \
      "peertube-angular.fr-FR.xlf"

fetch "https://raw.githubusercontent.com/qbittorrent/qBittorrent/master/src/lang/qbittorrent_de.ts" \
      "qbittorrent.de.ts"

fetch "https://raw.githubusercontent.com/keepassxreboot/keepassxc/develop/share/translations/keepassxc_de.ts" \
      "keepassxc.de.ts"

echo
echo "The tests assert exact entry and finding counts against the committed"
echo "copies. If upstream has changed, those numbers need updating too."
