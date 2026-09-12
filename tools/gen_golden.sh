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

here="$(cd "$(dirname "$0")/.." && pwd)"
py="$here/sample_python/interrogator/.venv/bin/python"
golden="$here/testdata/golden"

# 距離と方位は Go 側で緯度経度から出す（Python 側に ENU 変換が無い）
read -r dist azimuth < <(cd "$here" && go run ./tools/stationgeometry \
  -config "$golden/config.yaml" -station KX90 -ssr KX90S)
echo "dist=$dist m, azimuth=$azimuth rad"

gen() {  # gen <ケース名> <開始> <終了>
  local case=$1 from=$2 to=$3
  rm -rf "$golden/$case/intg"
  (cd "$here/sample_python/interrogator" && "$py" "$here/tools/run_python_reference.py" \
    --qpkx-root "$golden/$case/qpkx" \
    --intg-root "$golden/$case/intg" \
    --station KX90 --ssr KX90S \
    --from "$from" --to "$to" \
    --quest AC --quest-cycle-100ns 29499 --around-time-sec 4.04 \
    --st-dist-m "$dist" --st-azimuth-rad "$azimuth" \
    --reduce-pattern --sort-input \
    --stats-json "$golden/$case/expected_stats.json" >/dev/null)
  echo "$case: $(du -sh "$golden/$case/intg" | cut -f1)"
}

gen sorting  2026-06-10T00:47 2026-06-10T00:50
gen rounding 2026-06-10T00:00 2026-06-10T00:03
