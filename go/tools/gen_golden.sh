#!/usr/bin/env bash
# ゴールデン .intg を Python 実装から生成し直す。
#
# ゴールデンは必ず移植元の Python が出力したものでなければならない。
# Go 自身の出力で更新してしまうと、退行を検出できなくなる。
set -euo pipefail

here="$(cd "$(dirname "$0")/.." && pwd)"        # go/
repo="$(cd "$here/.." && pwd)"
py="$repo/sample_python/interrogator/.venv/bin/python"
golden="$here/testdata/golden"

rm -rf "$golden/intg"
(cd "$repo/sample_python/interrogator" && "$py" "$here/tools/run_python_reference.py" \
  --qpkx-root "$golden/qpkx" \
  --intg-root "$golden/intg" \
  --station KX90 --ssr KX90S \
  --from 2026-06-10T00:47 --to 2026-06-10T00:50 \
  --quest AC --quest-cycle-100ns 29499 --around-time-sec 4.04 \
  --ssr-x -127458.67663663127 --ssr-y -31615.025566053235 \
  --st-x -126591.43986481673 --st-y -32549.562701800554 \
  --reduce-pattern --sort-input \
  --stats-json "$golden/expected_stats.json")

echo "生成先: $golden/intg"
du -sh "$golden/intg"
