# tools

Python 実装からの移行期にのみ使うスクリプト。Python 実装を退役させたら
このディレクトリごと削除してよい。

生成物はリポジトリに固定してあるので、`go test ./...` の実行に Python は要らない。

| スクリプト | 生成物 | 用途 |
| --- | --- | --- |
| `gen_npvectors.py` | `internal/numeric/testdata/vectors.json` | 演算規約の参照ベクタ |
| `gen_patternvectors.py` | `internal/pattern/testdata/patternvectors.json` | 質問パターンの参照ベクタ |
| `gen_golden.sh` | `testdata/golden/intg/` | ゴールデン出力 |
| `run_python_reference.py` | 任意の出力先 | Python 実装を samples/ のレイアウトで走らせる |
| `stationgeometry/` | 標準出力 | 設定の緯度経度から距離と方位を出す（Python に渡す用） |

`run_python_reference.py` は Python 実装に手を入れず、ディレクトリレイアウトの
違いと読み込み後の整列の有無だけをサブクラスで差し替える。距離と方位は
Python 側に ENU 変換が無いので、`stationgeometry` が出した値をそのまま渡す。
Go 実装との突き合わせは、両者を同じ設定で走らせて `cmd/intgdiff` に食わせる。

```sh
# 距離と方位
read -r dist az < <(go run ./tools/stationgeometry -config testdata/kx90.yaml -station KX90 -ssr KX90S)

# Python 側
(cd sample_python/interrogator && ./.venv/bin/python ../../tools/run_python_reference.py \
  --qpkx-root ../../samples --intg-root /tmp/ref \
  --station KX90 --ssr KX90S \
  --from 2026-06-10T00:00 --to 2026-06-11T00:00 \
  --quest AC --quest-cycle-100ns 29499 --around-time-sec 4.04 \
  --st-dist-m "$dist" --st-azimuth-rad "$az" \
  --reduce-pattern --sort-input)

# Go 側
go run ./cmd/interrogator -config testdata/kx90.yaml -station KX90 -ssr KX90S \
  -qpkx-root samples -intg-root /tmp/got \
  -from 2026-06-10T00:00 -to 2026-06-11T00:00

go run ./cmd/intgdiff /tmp/ref /tmp/got
```
