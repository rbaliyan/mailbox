#!/usr/bin/env bash
# fuzz_all.sh discovers every Go fuzz target in the module and runs each for a
# short budget. New targets are picked up automatically (no per-target wiring).
#
#   FUZZTIME=30s ./scripts/fuzz_all.sh
#
# Only one fuzz target can run per `go test` invocation, so targets run in
# sequence. Exits non-zero if any target fails (a crash writes a reproducer
# under <pkg>/testdata/fuzz/<Target>/).
set -uo pipefail

budget="${FUZZTIME:-30s}"
status=0

while IFS= read -r file; do
  dir="$(dirname "$file")"
  pkg="./${dir#./}"
  while IFS= read -r fn; do
    [ -z "$fn" ] && continue
    echo "== fuzzing ${fn} (${pkg}) for ${budget} =="
    if ! go test -run='^$' -fuzz="^${fn}\$" -fuzztime="$budget" "$pkg"; then
      echo "FUZZ FAILED: ${fn} (${pkg})"
      status=1
    fi
  done < <(grep -oE '^func (Fuzz[A-Za-z0-9_]+)' "$file" | awk '{print $2}')
done < <(grep -rEl '^func Fuzz' --include='*_test.go' . | grep -v '/\.claude/')

exit "$status"
