#!/usr/bin/env bash
# Runs every scenario against $BASE_URL (default http://localhost:8080).
cd "$(dirname "$0")" || exit 1
failed=0
for s in [0-9][0-9]-*.sh; do
  bash "$s" || failed=$((failed + 1))
  echo
done
if [[ $failed -eq 0 ]]; then echo "All scenarios passed."; else echo "$failed scenario(s) failed."; fi
exit "$failed"
