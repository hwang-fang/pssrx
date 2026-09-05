# interrogator (Go)

`sample_python/interrogator` の Go 移植。qpkx（受信した質問データ）から
SSR のドウェルを検出し、その間を内挿した質問予定表 intg を出力する。

移植の受け入れ条件は **Python 実装と出力 `.intg` がバイト単位で一致すること**。
現在の到達点は下の「検証状況」を参照。

## 構成

```
cmd/interrogator   CLI（期間を指定して qpkx -> intg 変換）
cmd/intgdiff       2 つの intg ディレクトリをレコード単位で突き合わせる
internal/npcompat  Python / numpy の数値意味論の再現（偶数丸め・床除算・pairwise 総和・LU）
internal/pattern   質問パターン（PRI 列と質問種別列、最小周期へ簡約）
internal/store     qpkx 読み込みと intg 書き出し
internal/analyze   連鎖検出 DP・放物線フィット・ドウェル検出・内挿
internal/pipeline  1 分ブロック単位の解析ループ
internal/config    YAML 設定の読み込み
internal/nanotime  ナノ秒と JST 日時の変換
tools/             Python 側からテストベクタとゴールデンを生成するスクリプト
```

依存は Pure Go のみ（`github.com/goccy/go-yaml` の 1 つ）。cgo は使わない。

## 使い方

```sh
go run ./cmd/interrogator \
  -config testdata/kx90.yaml \
  -qpkx-root ../samples \
  -intg-root /tmp/out \
  -from 2026-06-10T00:00 \
  -to   2026-06-11T00:00 \
  -stats
```

時刻は JST 固定。`.qpkx` / `.intg` のファイル名が JST 前提で組まれているため、
オフセット付きの指定は受け付けない。

主なフラグ:

| フラグ | 既定 | 説明 |
| --- | --- | --- |
| `-sort-input` | `true` | qpkx 読み込み後にタイムスタンプで安定ソートする（後述） |
| `-append` | `false` | 既存 intg を切り詰めず追記する（移植元と同じ挙動） |
| `-stats` | `false` | 棄却理由別の件数と処理時間を出力する |
| `-v` | `false` | 棄却の詳細を DEBUG ログに出す |

### ディレクトリレイアウト

```
qpkx: {root}/{YYYYMM}/{station}/{YYYYMMDD}/qpkx/{YYYYMMDDHHMM}{station}.qpkx
intg: {root}/{YYYYMM}/{ssrid}/{YYYYMMDD}/{YYYYMMDDHHMM}{ssrid}.intg
```

移植元 `repository.py` にはフラット構成と `YYYYMM/` 構成のコードが残っていたが、
実データと実際の出力はいずれも上記なので、これだけを実装している。

### 設定ファイル

`testdata/kx90.yaml` を参照。`centrair.txt` の項目との対応:

| centrair.txt | YAML | 備考 |
| --- | --- | --- |
| `Lat` / `Log` / `Kei` | `x` / `y` | 投影変換は範囲外。直交座標を直接与える |
| `Quest` | `pattern` | `"ACAC"` のような質問種別文字列 |
| `QuestCycle` | `quest_cycle_100ns` | 100 ns 単位。`stagger_100ns` で列指定も可 |
| `AroundTime` | `around_time_sec` | 小数。`int(sec * 1e9)` と**切り捨て**で ns 化する |
| `Stagger` | `stagger` | 0 以外は展開規則が不明なのでエラーにする |

移植元で参照箇所が無かった項目（`interval_tolerance_ns` / `count_lag` /
`altitude` / `epsg`）は持ち込んでいない。

## 検証

```sh
go test ./...                 # 単体・ゴールデン
./tools/gen_golden.sh         # ゴールデンを Python から再生成
go run ./cmd/intgdiff A B     # 2 つの intg ディレクトリを突き合わせる
```

数値の一致検証は 2 段構えになっている。

1. `internal/npcompat` と `internal/pattern` のテストは、実際の numpy /
   Python から生成したテストベクタ（`tools/gen_npvectors.py`,
   `tools/gen_patternvectors.py`）に対してビット単位で一致を要求する。
2. `internal/pipeline` のゴールデンテストは、Python 実装が出力した `.intg`
   とバイト単位で一致することを要求する。ゴールデンは必ず
   `tools/gen_golden.sh`（= Python 実装）で更新すること。Go 自身の出力で
   更新すると退行を検出できなくなる。

### 検証状況

実データ（`samples/`）に対する Python 実装との突き合わせ結果:

| 対象 | 期間 | ファイル | レコード | 結果 |
| --- | --- | --- | --- | --- |
| KX90 | 2026-06-10 全日 | 1440 | 29,288,396 | **全バイト一致** |
| KX00 | 2026-06-10 00–02 時 | 120 | 2,457,578 | **全バイト一致** |

KX00 の qpkx には PRI の異なる 2 つの SSR の質問が混在しており、
連鎖検出 DP がそれを選り分ける経路も含めて一致している。

処理時間（解析部のみ、1 スレッド）は KX90 全日で Python 13.6 秒に対し Go 1.4 秒。

なお局パラメータ（緯度経度・走査周期など）は未入手のため、上記の検証では
qpkx から推定した暫定値を使っている。座標や走査周期が実物と違っても
両実装が同じ値を使う限りバイト一致の検証は成立する（距離と方位は
タイムスタンプの一定オフセットと方位の定数回転にしか効かず、ドウェル検出
そのものには関与しない）。

## 時刻の逆行について

実データの qpkx には**時刻が昇順になっていないファイルが相当数ある**。
2026-06-10 の 1440 ファイル中、KX90 で 236 ファイル、KX00 で 492 ファイル。
逆行は 2 種類ある。

- ファイル先頭の数レコードが前の分に属する（56〜59 秒の逆行）
- ファイル途中で 1〜4 秒巻き戻る

移植元 `analyze_qdata` の docstring は昇順を前提と明記しており、
セグメント分割の `np.diff` も連鎖検出の `np.searchsorted` も
ソート済みを仮定している。つまり**このデータでは移植元の前提が破れている**。

`-sort-input`（既定 `true`）は読み込み時に安定ソートしてこれを正す。
Python 側でも同じソートを掛けた上で突き合わせているため、上表の
バイト一致はこの前処理を両者に等しく適用した結果である。

ソートしない場合、Python と Go は逆行を含む分だけ食い違う
（KX90 の 1 時間で 60 分中 2 分）。これは `np.searchsorted` も本実装の
二分探索も非単調な配列に対しては未定義であり、同じ答えを返す保証が
無いためで、移植の誤りではない。`TestUnsortedInputDivergesFromGolden`
がこの状態を固定している。

ソートの有無で解析結果そのものがどれだけ変わるかは
[NUMERICS.md](NUMERICS.md) の「ソートの影響」を参照。

## 数値の一致について

Python / numpy と Go では丸め・除算・総和の規則が異なる。詳細と
実測した許容差は [NUMERICS.md](NUMERICS.md) にまとめてある。
