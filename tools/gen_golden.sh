#!/usr/bin/env bash
# ゴールデン .intg を Python 実装から生成し直す。
#
# ゴールデンは必ず移植元の Python 実装が出力したものでなければならない。
# Go 自身の出力で更新してしまうと、退行を検出できなくなる。
#
# ケースは 2 つあり、それぞれ別の性質を守っている。
#   sorting  : 入力 qpkx に時刻の逆行を含む。読み込み時の安定ソートが要る
#   rounding : ブラケット内挿の丸めが 0.5 ちょうどに当たる質問を含む。偶数丸めが要る
set -euo pipefail

here="$(cd "$(dirname "$0")/.." && pwd)"        # go/
repo="$(cd "$here/.." && pwd)"
py="$repo/sample_python/interrogator/.venv/bin/python"
golden="$here/testdata/golden"

gen() {  # gen <ケース名> <開始> <終了>
  local case=$1 from=$2 to=$3
  rm -rf "$golden/$case/intg"
  (cd "$repo/sample_python/interrogator" && "$py" "$here/tools/run_python_reference.py" \
    --qpkx-root "$golden/$case/qpkx" \
    --intg-root "$golden/$case/intg" \
    --station KX90 --ssr KX90S \
    --from "$from" --to "$to" \
    --quest AC --quest-cycle-100ns 29499 --around-time-sec 4.04 \
    --ssr-x -127458.67663663127 --ssr-y -31615.025566053235 \
    --st-x -126591.43986481673 --st-y -32549.562701800554 \
    --reduce-pattern --sort-input \
    --stats-json "$golden/$case/expected_stats.json" >/dev/null)
  echo "$case: $(du -sh "$golden/$case/intg" | cut -f1)"
}

gen sorting  2026-06-10T00:47 2026-06-10T00:50
gen rounding 2026-06-10T00:00 2026-06-10T00:03
