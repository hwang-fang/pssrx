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

`run_python_reference.py` は Python 実装に手を入れず、ディレクトリレイアウトの
違いと読み込み後の整列の有無だけをサブクラスで差し替える。Go 実装との
突き合わせは、両者を同じ設定で走らせて `cmd/intgdiff` に食わせる。

```sh
# Python 側
cd ../../sample_python/interrogator
./.venv/bin/python ../../go/tools/run_python_reference.py \
  --qpkx-root ../../samples --intg-root /tmp/ref \
  --station KX90 --ssr KX90S \
  --from 2026-06-10T00:00 --to 2026-06-11T00:00 \
  --quest AC --quest-cycle-100ns 29499 --around-time-sec 4.04 \
  --ssr-x -127458.67663663127 --ssr-y -31615.025566053235 \
  --st-x -126591.43986481673 --st-y -32549.562701800554 \
  --reduce-pattern --sort-input

# Go 側
cd ../../go
go run ./cmd/interrogator -config testdata/kx90.yaml \
  -qpkx-root ../samples -intg-root /tmp/got \
  -from 2026-06-10T00:00 -to 2026-06-11T00:00

go run ./cmd/intgdiff /tmp/ref /tmp/got
```
