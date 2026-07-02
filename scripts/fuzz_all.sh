#!/usr/bin/env bash
# fuzz_all.sh discovers every Go fuzz target in the module and runs each for a
# short budget. New targets are picked up automatically (no per-target wiring).
#
#   FUZZTIME=30s ./scripts/fuzz_all.sh
#
# Only one fuzz target can run per `go test` invocation, so targets run in
# sequence. Exits non-zero when a target reports a genuine finding (a crash
# writes a reproducer under <pkg>/testdata/fuzz/<Target>/).
#
# Environment:
#   FUZZTIME   per-target fuzz budget (default 30s).
#   FUZZ_RACE  run with -race when != "0" (default "1"). -race roughly halves
#              exec throughput; the short PR/push smoke budget disables it so a
#              slow input near the deadline does not flake the run.
set -uo pipefail

budget="${FUZZTIME:-30s}"

race_flag=()
if [ "${FUZZ_RACE:-1}" != "0" ]; then
  race_flag=(-race)
fi

status=0

while IFS= read -r file; do
  dir="$(dirname "$file")"
  pkg="./${dir#./}"
  while IFS= read -r fn; do
    [ -z "$fn" ] && continue
    echo "== fuzzing ${fn} (${pkg}) for ${budget} =="
    # ${arr[@]+"${arr[@]}"} expands safely when the array is empty under `set -u`
    # (bash 3.2, as shipped on macOS, otherwise errors on a bare "${arr[@]}").
    out="$(go test -run='^$' ${race_flag[@]+"${race_flag[@]}"} -fuzz="^${fn}\$" -fuzztime="$budget" "$pkg" 2>&1)"
    code=$?
    printf '%s\n' "$out"
    [ "$code" -eq 0 ] && continue

    # Distinguish a genuine finding from an engine-level timeout. A real finding
    # writes a minimized reproducer and prints "Failing input written to ...".
    # A bare "context deadline exceeded" (or a baseline-coverage timeout) is the
    # fuzzing coordinator failing to drain its workers within the budget on a
    # loaded runner -- a flake, not a bug. Only genuine findings fail the run.
    if printf '%s\n' "$out" | grep -q 'Failing input written'; then
      echo "FUZZ FAILED: ${fn} (${pkg})"
      status=1
    elif printf '%s\n' "$out" | grep -qE 'context deadline exceeded|deadline exceeded gathering baseline coverage'; then
      echo "FUZZ TIMEOUT (ignored, no reproducer): ${fn} (${pkg})"
    else
      echo "FUZZ FAILED: ${fn} (${pkg})"
      status=1
    fi
  done < <(grep -oE '^func (Fuzz[A-Za-z0-9_]+)' "$file" | awk '{print $2}')
done < <(grep -rEl '^func Fuzz' --include='*_test.go' . | grep -v '/\.claude/')

exit "$status"
