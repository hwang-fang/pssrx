# pssrx

測定局の受信データから SSR の質問予定表を作り（interrogator）、機体の応答信号を
質問に対応づけて位置を推定する（pssr）。

```sh
# 2 段をメモリで直列に流す（本番の形）
go run ./cmd/pssrx run \
  -config testdata/kx90.yaml -ssr KX90S -station KX90 \
  -data-root samples -intg-root /tmp/intg -out /tmp/fixes.csv \
  -from 2026-06-10T00:00 -to 2026-06-10T00:10 -stats
```

`interrogator` と `pssr` のサブコマンドは段を単独で走らせる。`pssr` は intg
ファイルからの再処理で、`run` と同じ入力ならバイト単位で同じ位置を出す
（`run` は intg をファイル形式と同じに量子化して次の段へ渡す）。

## interrogator

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
cmd/pssrx          CLI。サブコマンド interrogator（qpkx -> intg）, pssr（intg + apkx -> プロット）
cmd/intgdiff       2 つの intg ディレクトリをレコード単位で突き合わせる

internal/pipeline  1 分ブロック単位のループ。段を繋ぎ、設定を段の入力に直す（設定 -> Job -> 解析）

internal/interrogator  質問信号解析の段（連鎖検出 DP・放物線フィット・ドウェル検出・内挿）
internal/pssr      応答信号解析の段（質問との対応づけ、応答列、プロット）
  simtest          既知の質問予定・機体から応答を合成する（テスト用）

internal/config    SSR・測定局の静的な性質。マスタ YAML の読み込み、緯度経度からの距離・方位、
                   質問パターン（PRI 列と質問種別列、最小周期へ簡約）、物理定数
internal/store     qpkx / apkx の読み込みと intg の読み書き、JST の時刻変換
internal/geodesy   WGS84 緯度経度と ENU の変換、JPGEO2024 ジオイド
internal/numeric   出力値を一意に決める演算規約（偶数丸め・床除算・pairwise 総和・LU）

testdata/golden    ゴールデン（入力 qpkx・正解 intg・解析パラメータ）
```

`internal/` は 3 層に分かれる。`pipeline` が段を順に呼び、段（`interrogator/`、
今後の `pssr/`）は処理本体を持ち、残りは段が共有する基盤。依存は
`pipeline -> 段 -> 基盤` の一方向で、段どうしは import せず、基盤は段を
import しない。設定の書式を段の入力に直すのは `pipeline` の仕事で、段は
設定の書式を知らない。

依存は Pure Go のみ（`github.com/goccy/go-yaml` の 1 つ）。cgo は使わない。

## 使い方

```sh
go run ./cmd/pssrx interrogator \
  -config testdata/kx90.yaml \
  -station KX90 -ssr KX90S \
  -qpkx-root samples \
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
apkx: {root}/{YYYYMM}/{station}/{YYYYMMDD}/apkx/{YYYYMMDDHHMM}{station}.apkx
intg: {root}/{YYYYMM}/{ssrid}/{YYYYMMDD}/{YYYYMMDDHHMM}{ssrid}.intg
```

移植元 `repository.py` にはフラット構成と `YYYYMM/` 構成のコードが残っていたが、
実データと実際の出力はいずれも上記なので、これだけを実装している。

### 設定ファイル

`testdata/kx90.yaml` を参照。設定は SSR と測定局のマスタで、ID をキーにした
`ssrs` / `stations` の 2 つの表からなる。

```yaml
ssrs:
  KX90S:
    lat: 34.85058333      # WGS84 [deg]
    lon: 136.82093888
    alt: 0                # 標高 [m]（ジオイド面からの高さ。楕円体高ではない）
    interrogation: {around_time_sec: 4.04, pattern: AC, quest_cycle_100ns: 29499}
stations:
  KX90:
    lat: 34.8583717981495
    lon: 136.810685698149
    alt: 0
```

どの局とどの SSR を組み合わせるかは設定には書かず、`-station` / `-ssr` で
実行時に指定する。局と SSR の対応はコマンドによって異なる（interrogator は
1 対 1、後続の PSSR では SSR 1 つに受信局が複数）ので、設定側に固定すると
コマンドごとに設定を分けることになる。同じ ID を 2 回書くと読み込み時に
エラーになる。

位置は緯度経度で書き、SSR を原点にした ENU に変換して距離（斜距離）と
方位（真北基準）を出す。移植元は平面直角座標に投影した座標差から出して
いたので、同じ 2 点でも投影の縮尺係数（距離で約 0.01%）と子午線収差
（名古屋で方位約 0.2 度）のぶん値が変わる。標高はジオイド（JPGEO2024）で
楕円体高に直すため、ジオイドの範囲外（日本国外）はエラーになる。

従来の設定ファイル `centrair.txt` の項目との対応:

| centrair.txt | YAML | 備考 |
| --- | --- | --- |
| `Lat` / `Log` / `Height` | `lat` / `lon` / `alt` | WGS84。`Kei`（系番号）は不要 |
| `Quest` | `pattern` | `"ACAC"` のような質問種別文字列 |
| `QuestCycle` | `quest_cycle_100ns` | 100 ns 単位。`stagger_100ns` で列指定も可 |
| `AroundTime` | `around_time_sec` | 小数。ns へは**切り捨て**で落とす |
| （無し） | `max_range_m` | SSR の覆域 [m]。応答の対応づけの遅延上限に使う |
| `Stagger` | `stagger` | 0 以外は展開規則が不明なのでエラーにする |

単位はフィールド名に埋めてある。PRI を設定では 100 ns 単位で書く一方
内部では ns で扱うため、名前に単位が無いと 100 倍の取り違えが起きる。

## pssr

intg（質問予定表）と apkx（測定局が受信した Mode A/C 応答）から、機体ごと・
ドウェルごとのプロットを作り、単局で位置を推定して CSV に書く。

```sh
go run ./cmd/pssrx pssr \
  -config testdata/kx90.yaml \
  -ssr KX90S -station KX90 \
  -intg-root /tmp/out -data-root samples \
  -from 2026-06-10T00:00 -to 2026-06-10T00:10 \
  -stats -out /tmp/fixes.csv
```

`-station` は intg を作った質問解析局、`-reply-stations` は応答を受信した局
（省略時は質問解析局と同じ局の単局計算）。intg には局の情報が残らないので
別々に指定する。局の時計は GPS で同期している前提。

処理は次のとおり（`internal/pssr`）。手続きは `Pair` → `Suppress` → `Locate` →
`Sink` の関数の直列で、`pipeline` が 1 分ブロックごとに順に呼ぶ。ブロックを
またいで持ち越す記録は `PairState`（開いている列と保留中の応答）と
`SuppressState`（判定待ちのプロット）だけで、件数は呼び出し側の `Stats` に足す。
原理・手順・実装の詳しい解説は [PSSR.md](PSSR.md) を参照。

1. 応答受信時刻 t_r から、遅延 τ = t_r − t_q が `TauMin`（応答遅延 3 µs +
   基線長 / c）以上になる最新の質問 t_q を対にする。τ が `TauMax`
   （3 µs + (2·覆域 + 基線長) / c）を超える応答は捨てる。`TauMax` は最短の
   PRI より短くなければならず、そうでない覆域は設定の段階で拒否する
2. 対になった応答を列にまとめる。鍵は τ の連続性（1 µs）と Mode A 符号の
   一致。Mode C 符号は鍵にしない（上昇・降下中は 1 ドウェルの間に 100 ft の
   境界をまたぐ）。応答の無い質問が 3 回続くと列を閉じ、3 応答未満の列は
   FRUIT（他 SSR への応答）として捨てる
3. 閉じた列を 1 プロットにする。時刻と方位は最初と最後の質問の中点、τ は平均。
   Mode A 符号はスコークに、Mode C 符号は Gillham 復号で気圧高度にする。
   列の中の高度が 100 ft 以内に収まればプロット時刻に最も近い応答の値、
   散っていれば（ガーブル）捨てる
4. 幽霊を落とす。実データには 1 機が同じ走査に複数方位で現れる幽霊が実プロットと
   同じ程度ある。近い機体がサイドローブ質問に応答したもの（τ は主ビームと同じ）と、
   ビームが反射体に当たって遠い機体を照射したもの（τ が経路差ぶん大きく、方位は
   反射体の方向）。同じ走査・同じスコーク・高度差 200 ft 以内の群で、τ 最小から
   5 µs 以内の候補のうち応答数最多を残し、それより τ の大きいものを落とす。
   方位は使わない（反射体の方向は機体と無関係）
5. 位置を解く。機体は「SSR を焦点の一つとする双基地距離の回転楕円体」
   「SSR からのビーム方位の鉛直面」「気圧高度」の交点。SSR の ENU で高さ z を
   与えると地上距離 ρ の 2 次方程式になり、実効半直弦 ℓ を使った閉形式で解ける。
   解の一意性は |z| < ℓ の 1 判定で決まり、曖昧な幾何（基線の近傍・SSR 直上）は
   解かない。z は地球の曲率で ρ に依存するので、ρ を解いてから `geodesy` で厳密な
   z を求め直す反復を 2〜3 回行う。観測量の分散を線形伝播した ENU の共分散も出す。大気屈折は無視し、気圧高度は標準大気
   基準のまま標高に使う（QNH 補正は `HeightFromPressureAltitude` に集約して
   後から足す）
6. `Sink` へ書く。いまは CSV（時刻 JST、SSR、局、スコーク、気圧高度 [ft]、
   緯度、経度、標高 [m]、方位 [rad]、τ [ns]、応答数、σ_E/σ_N/σ_U [m]）

応答符号のビット配置は仕様書が無く、実データから決めた（`internal/pssr/decode.go`）。
局の時計は GPS で同期している前提。

apkx は 8 バイト固定長（分先頭からの経過 [100 ns]、12 ビット応答符号、
波高値）。応答符号はスコーク（Mode A）か高度符号（Mode C）で、どちらかは
対応づいた質問の種別で決まる。

## 検証

```sh
go test ./...                 # 単体・ゴールデン
go run ./cmd/intgdiff A B     # 2 つの intg ディレクトリを突き合わせる
```

検証は固定データに対する一致で行い、外部の参照実装には依存しない。

1. `internal/numeric` と `internal/config`（質問パターン）のテストは、`testdata/` に固定した
   参照ベクタに対してビット単位の一致を要求する。演算規約が 1 ulp でも
   変われば落ちる。
2. `internal/pipeline` のゴールデンテストは、`testdata/golden/` に固定した
   既知の正しい `.intg` とバイト単位の一致を要求する。入力の qpkx も
   一緒に置いてあるので、3 分ぶん 2 ケースだけで解析全体を通せる。
3. PSSR は `rounding` ケースに apkx を添え、intg + apkx から出した位置
   `fixes.csv` とのバイト一致、メモリ直列（`run`）とファイル再処理（`pssr`）
   の一致、`run` が書く intg とゴールデンの一致を確認する。`fixes.csv` は
   参照実装が無いので現行実装の出力を固定したもので、仕様を意図して変える
   ときだけ `go test ./internal/pipeline -update-pssr-golden` で更新し、差分を
   記録する。段ごとの規則は `internal/pssr` の単体テストが合成データで守る。
4. 設定から解析パラメータと幾何を導く層（`config.Geometry`、
   `pipeline.InterrogatorParams`、`pipeline.PSSRParams`、`Options.Job`）は
   単体テストで検証する。

ゴールデンは解析本体だけを通す。解析パラメータ（走査周期・PRI 列・
質問種別・距離・方位）は `testdata/golden/golden.yaml` にリテラルで固定し、
`pipeline.RunJob` へ直接渡す。設定ファイルの書式や緯度経度からの幾何計算は
通さないので、それらの仕様を変えてもゴールデンは変えずに済む。

ゴールデンの 2 ケースはそれぞれ別の性質を守るために選んである。

| ケース | 期間 | 守っているもの |
| --- | --- | --- |
| `sorting` | 00:47–00:50 | 入力に時刻の逆行を含む。読み込み時の安定ソート |
| `rounding` | 00:00–00:03 | 内挿の丸めが 0.5 に当たる質問を含む。偶数丸め |

ゴールデンは移植元の Python 実装が出力したものを固定してあり、Python 実装は
退役済みで、以後はこれが唯一の正解になる。更新してよいのは解析本体の仕様を
意図して変えるときだけで、その際は `cmd/intgdiff` で旧ゴールデンとの差分を
確かめて記録する。実装の都合で出た差分をゴールデンの更新で吸収してはならない。
退行を検出できなくなる。

不一致が出たときは `cmd/intgdiff` でレコード単位の内訳を見る。どのファイルの
どのレコードが、方位角にして何 LSB ずれたかが出る。

## 常駐運転について

長時間動かしても保持量が増え続けないようにしてある。

- `store.IntgRepository` は書き込み済みファイルを SSR ごとの「到達済みの
  最新の分」1 個だけで判定する。書き込み先の分は進む一方なので、
  ファイル名を溜める必要が無い。
- `pipeline.Timing` は所要時間の標本を持たず、件数・合計・最小・最大だけを
  更新する。
- `analyze.Analyzer` が持ち越すのは先送り生データ 1 セグメントぶんと
  最終ドウェル 1 本だけで、いずれも呼び出しごとに入れ替わる。

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
- 方位角の `[0, 2pi)` への畳み込みは `math.Mod` ではなく `interrogator.wrapAngle`。
  `math.Mod` は負の値を負のまま返し、それを uint32 へ変換すると
  Go の仕様上「実装依存」の結果になる。

いずれも実測に基づく判断で、差が出なかった規約（床除算・pairwise 総和）は
素の Go の演算に戻してある。実測値と根拠は [NUMERICS.md](NUMERICS.md) を参照。

## 移植の記録

Python 実装からの移植は完了し、Python 実装と生成スクリプトは退役した。
参照ベクタ（`internal/*/testdata/*.json`）とゴールデン（`testdata/golden/`）は
その時点の Python 実装の出力を固定したもので、以後の正解はこれらになる。

移植の受け入れ条件は「Python 実装と出力 `.intg` がバイト単位で一致すること」
とし、実データに対して次を確認した。

| 対象 | 期間 | ファイル | レコード | 結果 |
| --- | --- | --- | --- | --- |
| KX90 | 2026-06-10 全日 | 1440 | 29,288,396 | 全バイト一致 |
| KX00 | 2026-06-10 00–02 時 | 120 | 2,457,578 | 全バイト一致 |

KX00 の qpkx には PRI の異なる 2 つの SSR の質問が混在しており、
連鎖検出がそれを選り分ける経路も含めて一致している。
処理時間（解析部のみ、1 スレッド）は KX90 全日で Python 13.6 秒に対し 1.4 秒。

局パラメータ（緯度経度・走査周期など）は未入手のため、上記の検証と
ゴールデンでは qpkx から推定した暫定値を使っている。座標や走査周期が
実物と違っても両実装が同じ値を使う限りバイト一致の検証は成立する。距離と
方位はタイムスタンプの一定オフセットと方位の定数回転にしか効かず、
ドウェル検出そのものには関与しないため。

移植後、位置の指定を投影済み座標から WGS84 緯度経度へ変えた際に、
ゴールデンは Python 実装を同じ距離・方位で走らせて再生成した（差分は
方位の一律 0.198 度回転のみ）。それが Python 実装を使った最後の生成である。

旧実装との対応:

| 旧 | 新 |
| --- | --- |
| `main.py` の `test()` | `cmd/pssrx interrogator` + `internal/pipeline` |
| `analyze.py` | `internal/interrogator` |
| `domain.py` の `InterrogationPattern` | `internal/config`（`Pattern`） |
| `domain.py` の `ChainConfig` / `Chain` / `Dwell` | `internal/interrogator` |
| `repository.py` | `internal/store` |
| `config.py`（未使用）+ `centrair.txt` | `internal/config` |
| `timestamp.py` | `internal/store`（`ToTime` / `JST`） |

`config.py` の `interval_tolerance_ns` / `count_lag` / `altitude` / `epsg` は
どこからも参照されていなかったため持ち込んでいない。
