#!/usr/bin/env bash
# fuzz_targets.sh discovers every Go fuzz target in the module. New targets are
# picked up automatically -- no per-target wiring anywhere.
#
#   ./scripts/fuzz_targets.sh          # "<pkg> <FuzzName>" lines
#   ./scripts/fuzz_targets.sh --json   # [{"name":..,"pkg":..}, ...] (CI matrix)
#
# Used by fuzz_all.sh (local sweep) and the CI discover job (matrix) so both
# share one discovery rule and cannot drift.
set -uo pipefail

emit_json="${1:-}"

pairs=()
while IFS= read -r file; do
  dir="$(dirname "$file")"
  pkg="./${dir#./}"
  while IFS= read -r fn; do
    [ -z "$fn" ] && continue
    pairs+=("${pkg} ${fn}")
  done < <(grep -oE '^func (Fuzz[A-Za-z0-9_]+)' "$file" | awk '{print $2}')
done < <(grep -rEl '^func Fuzz' --include='*_test.go' . | grep -v '/\.claude/')

if [ "$emit_json" != "--json" ]; then
  printf '%s\n' ${pairs[@]+"${pairs[@]}"}
  exit 0
fi

# Hand-build the JSON array (no jq dependency on the runner).
out="["
sep=""
for p in ${pairs[@]+"${pairs[@]}"}; do
  pkg="${p%% *}"
  fn="${p##* }"
  out="${out}${sep}{\"name\":\"${fn}\",\"pkg\":\"${pkg}\"}"
  sep=","
done
out="${out}]"
printf '%s\n' "$out"
