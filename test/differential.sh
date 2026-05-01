#!/bin/bash
# differential.sh — verify the Go reducer matches reduce-state.sh on every fixture.
#
# Pipes each test/fixtures/events-*.jsonl through both reducers, canonicalizes
# both outputs with `jq -S .` (sorted keys, jq's pretty form), and diffs.
#
# This script is temporary: it gets deleted alongside reduce-state.sh in step 10
# of the Phase 1 migration. While both reducers exist, every commit must keep
# this passing.
#
# Run from anywhere: bash test/differential.sh

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASH_REDUCER="$HERE/.cc-cockpit/reduce-state.sh"
FIXTURES_DIR="$HERE/test/fixtures"

[ -x "$BASH_REDUCER" ] || { echo "differential: missing $BASH_REDUCER" >&2; exit 2; }
command -v jq >/dev/null   || { echo "differential: jq not on PATH" >&2; exit 2; }
command -v go >/dev/null   || { echo "differential: go not on PATH" >&2; exit 2; }

GO_REDUCER_BIN="$(mktemp -t cc-cockpit-reduce.XXXXXX)"
trap 'rm -f "$GO_REDUCER_BIN"' EXIT

(cd "$HERE" && go build -o "$GO_REDUCER_BIN" ./cmd/cc-cockpit-reduce) \
  || { echo "differential: go build failed" >&2; exit 2; }

PASS=0
FAIL=0
shopt -s nullglob
for fixture in "$FIXTURES_DIR"/events-*.jsonl; do
  name="$(basename "$fixture")"
  bash_out="$("$BASH_REDUCER" < "$fixture" | jq -S .)"
  go_out="$("$GO_REDUCER_BIN"   < "$fixture" | jq -S .)"
  if [ "$bash_out" = "$go_out" ]; then
    printf '  \033[32mPASS\033[0m %s\n' "$name"
    PASS=$((PASS+1))
  else
    printf '  \033[31mFAIL\033[0m %s\n' "$name"
    diff <(echo "$bash_out") <(echo "$go_out") | sed 's/^/    /'
    FAIL=$((FAIL+1))
  fi
done

echo
printf 'differential: PASS=%d FAIL=%d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
