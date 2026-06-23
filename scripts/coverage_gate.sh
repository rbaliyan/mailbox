#!/usr/bin/env bash
# coverage_gate.sh enforces a minimum unit-test coverage floor on the core
# hand-written packages. Per-package floors are used deliberately: Go credits
# statement coverage to the package under test, so a global aggregate is dragged
# down by packages exercised through other packages' tests (e.g. store/memory is
# covered via the root package) and by backends with no unit tests (mongo,
# postgres, cloud attachment/search stores — these are covered by the
# integration suites instead).
set -euo pipefail

# "import-path floor" pairs (kept bash 3.2 compatible — no associative arrays).
floors="
github.com/rbaliyan/mailbox 78
github.com/rbaliyan/mailbox/server 78
github.com/rbaliyan/mailbox/rules 78
github.com/rbaliyan/mailbox/content 85
github.com/rbaliyan/mailbox/resolver 90
"

pkgs="$(echo "$floors" | awk 'NF {print $1}')"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
# shellcheck disable=SC2086
go test -cover $pkgs 2>/dev/null | tee "$tmp"

status=0
while read -r pkg min; do
  [ -z "$pkg" ] && continue
  pct="$(awk -v p="$pkg" '$1 == "ok" && $2 == p {
            for (i = 1; i <= NF; i++) if ($i ~ /^[0-9.]+%$/) { gsub(/%/, "", $i); print $i }
          }' "$tmp")"
  if [ -z "$pct" ]; then
    echo "GATE FAIL: no coverage reported for $pkg"
    status=1
    continue
  fi
  if awk -v v="$pct" -v m="$min" 'BEGIN { exit !(v + 0 < m + 0) }'; then
    echo "GATE FAIL: $pkg coverage ${pct}% below floor ${min}%"
    status=1
  else
    echo "ok: $pkg coverage ${pct}% (floor ${min}%)"
  fi
done <<EOF
$(echo "$floors" | awk 'NF')
EOF

exit "$status"
