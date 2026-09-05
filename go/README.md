# interrogator

qpkx（測定局が受信した質問データ）から SSR のドウェル——ビームが測定局を
向いていた区間——を検出し、ドウェルとドウェルの間の質問時刻を内挿して
質問予定表 intg を出力する。

処理はおおまかに 4 段:

1. 振幅ゲートで弱い受信を落とし、時間差でセグメントに切る
2. 動的計画法で、質問間隔と質問種別のパターンに合う連鎖を 1 本選び出す
   （1 つの qpkx には複数の SSR の質問が混在するので、ここで選り分ける）
3. 連鎖の振幅列に放物線を当て、頂点をビーム中心通過時刻とする
4. 隣り合うドウェルの間を内挿し、質問 1 発ごとの時刻と方位を書き出す

## 構成

```
cmd/interrogator   CLI（期間を指定して qpkx -> intg 変換）
cmd/intgdiff       2 つの intg ディレクトリをレコード単位で突き合わせる
internal/analyze   連鎖検出 DP・放物線フィット・ドウェル検出・内挿
internal/pattern   質問パターン（PRI 列と質問種別列、最小周期へ簡約）
internal/store     qpkx 読み込みと intg 書き出し
internal/pipeline  1 分ブロック単位の解析ループ
internal/config    YAML 設定の読み込み
internal/numeric   出力値を一意に決める演算規約（偶数丸め・床除算・pairwise 総和・LU）
internal/nanotime  ナノ秒と JST 日時の変換
tools/             参照ベクタとゴールデンの生成スクリプト（移行期のみ。末尾参照）
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

`testdata/kx90.yaml` を参照。従来の設定ファイル `centrair.txt` の項目との対応:

| centrair.txt | YAML | 備考 |
| --- | --- | --- |
| `Lat` / `Log` / `Kei` | `x` / `y` | 投影変換は範囲外。直交座標を直接与える |
| `Quest` | `pattern` | `"ACAC"` のような質問種別文字列 |
| `QuestCycle` | `quest_cycle_100ns` | 100 ns 単位。`stagger_100ns` で列指定も可 |
| `AroundTime` | `around_time_sec` | 小数。ns へは**切り捨て**で落とす |
| `Stagger` | `stagger` | 0 以外は展開規則が不明なのでエラーにする |

単位はフィールド名に埋めてある。PRI を設定では 100 ns 単位で書く一方
内部では ns で扱うため、名前に単位が無いと 100 倍の取り違えが起きる。

## 検証

```sh
go test ./...                 # 単体・ゴールデン
./tools/gen_golden.sh         # ゴールデンを再生成（移行期のみ。末尾参照）
go run ./cmd/intgdiff A B     # 2 つの intg ディレクトリを突き合わせる
```

検証は 2 段構えになっている。

1. `internal/numeric` と `internal/pattern` のテストは、`testdata/` に固定した
   参照ベクタに対してビット単位の一致を要求する。演算規約が 1 ulp でも
   変われば落ちる。
2. `internal/pipeline` のゴールデンテストは、`testdata/golden/` に固定した
   既知の正しい `.intg` とバイト単位の一致を要求する。入力の qpkx も
   一緒に置いてあるので、3 分ぶん 2 ケースだけで解析全体を通せる。

ゴールデンの 2 ケースはそれぞれ別の性質を守るために選んである。

| ケース | 期間 | 守っているもの |
| --- | --- | --- |
| `sorting` | 00:47–00:50 | 入力に時刻の逆行を含む。読み込み時の安定ソート |
| `rounding` | 00:00–00:03 | 内挿の丸めが 0.5 に当たる質問を含む。偶数丸め |

更新は `tools/gen_golden.sh` で行い、`cmd/interrogator` 自身の出力で
上書きしてはならない。退行を検出できなくなる。

不一致が出たときは `cmd/intgdiff` でレコード単位の内訳を見る。どのファイルの
どのレコードが、方位角にして何 LSB ずれたかが出る。

## 時刻の逆行について

実データの qpkx には**時刻が昇順になっていないファイルが相当数ある**。
2026-06-10 の 1440 ファイル中、KX90 で 236 ファイル、KX00 で 492 ファイル。
逆行は 2 種類ある。

- ファイル先頭の数レコードが前の分に属する（56〜59 秒の逆行）
- ファイル途中で 1〜4 秒巻き戻る

解析はデータが時刻昇順であることを前提にしている。セグメント分割は
隣接レコードの時間差で切るし、連鎖検出の探索窓は二分探索で決めるので、
逆行があるとどちらも意味を失う。つまり**このデータでは前提が破れている**。

`-sort-input`（既定 `true`）は読み込み時に安定ソートしてこれを正す。
ソートの有無で解析結果そのものがどれだけ変わるかは
[NUMERICS.md](NUMERICS.md) の「入力の整列が結果に与える影響」を参照。
差は丸め誤差の水準ではない。

## 演算規約

Go の標準的な演算からずらしている箇所が 2 つある。

- ブラケット内挿の質問時刻は `math.Round` ではなく `math.RoundToEven`。
  0.5 ちょうどに当たる質問が実データに一定数あり、`math.Round` に変えると
  1 日あたり 700 レコードほど方位角が 1 LSB ずれる。
- 方位角の `[0, 2pi)` への畳み込みは `math.Mod` ではなく `analyze.wrapAngle`。
  `math.Mod` は負の値を負のまま返し、それを uint32 へ変換すると
  Go の仕様上「実装依存」の結果になる。

いずれも実測に基づく判断で、差が出なかった規約（床除算・pairwise 総和）は
素の Go の演算に戻してある。実測値と根拠は [NUMERICS.md](NUMERICS.md) を参照。

## 移行期のメモ

以下は Python 実装からの移行中にのみ意味を持つ。Python 実装を退役させたら
このセクションと `tools/` を削除してよい。

`tools/` の 3 つのスクリプトは、Python 実装から参照ベクタとゴールデンを
生成する。生成物（`internal/*/testdata/*.json`, `testdata/golden/`）は
リポジトリに固定してあるので、テストの実行に Python は要らない。

移行の受け入れ条件は「Python 実装と出力 `.intg` がバイト単位で一致すること」
とし、実データに対して次を確認済み。

| 対象 | 期間 | ファイル | レコード | 結果 |
| --- | --- | --- | --- | --- |
| KX90 | 2026-06-10 全日 | 1440 | 29,288,396 | 全バイト一致 |
| KX00 | 2026-06-10 00–02 時 | 120 | 2,457,578 | 全バイト一致 |

KX00 の qpkx には PRI の異なる 2 つの SSR の質問が混在しており、
連鎖検出がそれを選り分ける経路も含めて一致している。
処理時間（解析部のみ、1 スレッド）は KX90 全日で Python 13.6 秒に対し 1.4 秒。

局パラメータ（緯度経度・走査周期など）は未入手のため、上記の検証では
qpkx から推定した暫定値を使っている。座標や走査周期が実物と違っても
両実装が同じ値を使う限りバイト一致の検証は成立する。距離と方位は
タイムスタンプの一定オフセットと方位の定数回転にしか効かず、ドウェル検出
そのものには関与しないため。

旧実装との対応:

| 旧 | 新 |
| --- | --- |
| `main.py` の `test()` | `cmd/interrogator` + `internal/pipeline` |
| `analyze.py` | `internal/analyze` |
| `domain.py` の `InterrogationPattern` | `internal/pattern` |
| `domain.py` の `ChainConfig` / `Chain` / `Dwell` | `internal/analyze` |
| `repository.py` | `internal/store` |
| `config.py`（未使用）+ `centrair.txt` | `internal/config` |
| `timestamp.py` | `internal/nanotime` |

`config.py` の `interval_tolerance_ns` / `count_lag` / `altitude` / `epsg` は
どこからも参照されていなかったため持ち込んでいない。
