# 設定ファイル

`pssrx` の各サブコマンドに `-config` で渡す YAML ファイルの書き方。
SSR と測定局のマスタ（`ssrs` / `stations`）と、解析の定数の上書き
（`analysis`、省略可）の 3 節からなる。

```yaml
ssrs:
  KX90S:
    name: KX90_SSR
    lat: 34.85058333
    lon: 136.82093888
    alt: 0
    max_range_m: 400000
    interrogation:
      around_time_sec: 4.04
      mode_pattern: AC
      interval_pattern_ns: [2949900]
      clockwise: true
stations:
  KX90:
    name: KX90_STATION
    lat: 34.8583717981495
    lon: 136.810685698149
    alt: 0
analysis:
  pssr:
    max_altitude_ft: 70000
```

## 全体の規則

- **未知のキーはエラー**になる。打ち間違いが黙って無視されることはない。
- **単位はキー名に含める**（`_m`、`_ns`、`_sec`、`_ft`、`_dbm`、`_rad`）。
  時間は ns か秒で書く。
- `ssrs` / `stations` は **ID をキーにした表**で、同じ ID を 2 回書くと
  エラーになる。ID はコマンドの `-ssr` / `-station` / `-reply-stations` で
  指定し、出力（intg のファイル名、CSV の列）にそのまま使われる。
- **どの局とどの SSR を組み合わせるかは設定に書かない。** 組み合わせは
  コマンドごとに異なる（`interrogator` は 1 対 1、`pssr` / `run` は
  質問解析局と応答局を別に指定できる）ので、実行時に引数で決める。
- 設定は起動時に読み込んで検証する。稼働中の再読み込みは無い。

## `ssrs`

質問を出す二次監視レーダー（SSR）。

| キー | 型 | 必須 | 説明 |
| --- | --- | --- | --- |
| `name` | 文字列 | 任意 | 表示用の名前。処理には使わない |
| `lat` | 数値 | 必須 | WGS84 緯度 [deg]。−90〜90 |
| `lon` | 数値 | 必須 | WGS84 経度 [deg]。−180〜180 |
| `alt` | 数値 | 必須 | 標高 [m]。ジオイド面からの高さで、楕円体高ではない。0 でも省略できない |
| `max_range_m` | 数値 | 必須 | 覆域 [m]。正の値。応答の対応づけで受け付ける遅延の上限を決める（下記） |
| `interrogation` | 節 | 必須 | 質問の仕様（下記） |

位置は SSR を原点にした ENU 座標へ変換して、測定局までの距離（斜距離）と
方位（真北基準）を出す。標高はジオイドモデル（JPGEO2024）で楕円体高に
直すため、**日本国外の位置はエラー**になる。

### `interrogation`

| キー | 型 | 必須 | 説明 |
| --- | --- | --- | --- |
| `around_time_sec` | 数値 | 必須 | 走査周期 [s]。ビームが 1 回転する時間。正の値。ns へは切り捨てで落とす |
| `mode_pattern` | 文字列 | 必須 | 質問種別の繰り返し。1 文字が質問 1 発 |
| `interval_pattern_ns` | 数値の列 | 必須 | 質問間隔 [ns] の繰り返し。1 要素以上、各要素は正 |
| `clockwise` | 真偽 | 任意 | ビームの回転方向。省略時は `true`（時計回り） |

**質問パターン**は `mode_pattern` と `interval_pattern_ns` の対で表す。
`mode_pattern` は種別の順、`interval_pattern_ns` は「その質問から次の質問
までの間隔」の順で、どちらも 1 周期ぶんを書く。2 つの長さは同じで
なくてよく、最小公倍数の長さに展開してから最小の繰り返し単位へ簡約する
（`ACAC` は `AC` と同じ）。

```yaml
# 一定 PRI 2.9499 ms で Mode A と Mode C を交互に
mode_pattern: AC
interval_pattern_ns: [2949900]

# スタガ運用（間隔を 3 段で周期的に変える）。種別 2 × 間隔 3 = 1 周期 6 発
mode_pattern: AC
interval_pattern_ns: [2900000, 2906500, 2913000]
```

`mode_pattern` に使える文字:

| 文字 | 質問種別 |
| --- | --- |
| `A` または `3` | Mode A（Mode 3/A。応答はスコーク） |
| `C` | Mode C（応答は気圧高度） |
| `1` `2` `B` `D` | Mode 1 / 2 / B / D |

位置推定には Mode A（スコーク）と Mode C（高度）の両方の応答が要るので、
`pssr` / `run` で使う SSR は両方を含むパターンにする。

### 覆域と質問間隔の関係

応答が「どの質問へのものか」を一意に決めるため、応答の遅延の上限
`TauMax` が最短の質問間隔より短くなければならない。

```
TauMax = 3 µs（応答遅延）+ (2 × max_range_m + 基線長) / c
TauMax < min(interval_pattern_ns)
```

基線長は SSR から応答局までの距離。たとえば覆域 400 km・基線 1.3 km なら
`TauMax` ≈ 2.68 ms で、間隔 2.95 ms に収まる。覆域 450 km にすると
3.0 ms を超えて起動時にエラーになる。この検査は `pssr` / `run` で行う。

## `stations`

質問と応答を受信する測定局。

| キー | 型 | 必須 | 説明 |
| --- | --- | --- | --- |
| `name` | 文字列 | 任意 | 表示用の名前 |
| `lat` / `lon` / `alt` | 数値 | 必須 | SSR と同じ。3 つとも必要 |

局の時計は GPS で同期している前提。応答局と質問解析局が別でも、
時刻はそのまま比較する。

## `analysis`

解析の手続きの定数。**節ごと省略でき、書いた項目だけが既定値を
上書きする。** 既定値で運用でき、実データの分布を見て調整する項目。

```yaml
analysis:
  interrogator:
    amplitude_gate_dbm: -38
  pssr:
    min_replies: 4
    max_altitude_ft: 70000
```

- 不正な値は起動時に `analysis.pssr: ...` のように節の場所を付けて
  エラーになる。
- 既定値から変えた項目は起動時に INFO ログ
  （`解析の定数を既定値から変更`）に出る。
- SSR ごと・局ごとの上書きは無い。

### `analysis.interrogator`

質問予定表を作る段（qpkx → intg）の定数。手順の順に並べる。

| キー | 既定 | 説明 |
| --- | --- | --- |
| `amplitude_gate_dbm` | −35 | 振幅ゲート [dBm]。これより弱い受信は捨てる。ドウェル端より十分低い粗い値にする。−256〜0 |
| `gate_ns` | 20000 | 連鎖検出で、質問パターンから予測した時刻の前後にどれだけ受信を探すか [ns] |
| `max_skip` | 3 | 連鎖の途中で許す欠測の段数 |
| `min_chain_length` | 8 | 連鎖として認める最小の質問数。これ未満のセグメントは捨てる |
| `skip_penalty` | 0.30 | 連鎖の評価で、欠測 1 段あたりに引く点 |
| `residual_weight` | 0.05 | 連鎖の評価で、時刻残差の 2 乗に掛ける重み（同点の解消用） |
| `dwell_gap_periods` | 0.2 | 受信をドウェルに切り分ける間隙を、走査周期に対する割合で表す。0 より大きく 1 未満。ブロック末尾の繰り越しにも同じ幅を使う |
| `parabola_min_samples` | 6 | 放物線フィットに要る最小の点数。3 以上 |
| `robust_iters` | 3 | 外れ値を除いてフィットし直す回数 |
| `parabola_outlier_k` | 4.5 | 外れ値の判定閾値（残差の MAD に対する倍率） |
| `parabola_sigma_floor_db` | 0.3 | 外れ値判定で仮定する振幅雑音の下限 [dB] |
| `vertex_margin_frac` | 0.25 | 放物線の頂点がドウェルの時間範囲の外にはみ出してよい割合 |
| `parabola_max_residual_db` | 1.0 | フィットの残差 RMS の上限 [dB]。超えたらドウェルとして採らない |
| `min_peak_drop_db` | 3.0 | ドウェルの両端で頂点からこれ以上振幅が落ちていること [dB]。落ちていなければビーム中心を通っていない |
| `max_bridge_rotations` | 2 | 隣り合うドウェルの間隔が走査何回分までなら内挿するか。1 本の取りこぼしを埋めるには 2。これを超えて空いた区間（データの欠落など）は質問予定を出さない |

### `analysis.pssr`

応答を質問に対応づけて位置を出す段（intg + apkx → 位置）の定数。

**対応づけ**

| キー | 既定 | 説明 |
| --- | --- | --- |
| `tau_tolerance_ns` | 1000 | 同じ応答列とみなす遅延 τ の差の上限 [ns]。応答遅延の公差 ±0.5 µs が支配的 |
| `max_gap` | 2 | 応答列の途中で応答の無い質問を何回まで許すか。超えたら列を閉じる。0 なら途切れを許さない |
| `min_replies` | 3 | 応答列として残す最小の応答数。これ未満は FRUIT（他 SSR への応答）として捨てる |
| `max_altitude_ft` | 60000 | 気圧高度の上限 [ft]。超える列は FRUIT の偶然の一致とみなして捨てる。高高度の軍用機まで拾うなら上げる |
| `max_retention_ns` | 120000000000 | 質問予定と応答を溜めておく時間幅の上限 [ns]（2 分 = 投入の刻みの最大である 1 分ファイルの 2 倍）。片方が止まっても他方が溜まり続けないための安全弁。取り出しの後に適用する |

**幽霊抑圧**

| キー | 既定 | 説明 |
| --- | --- | --- |
| `same_scan_fraction` | 0.75 | 同じ走査とみなす時刻差の上限を、走査周期に対する割合で表す。0 より大きく 1 未満 |
| `altitude_tolerance_ft` | 200 | 同じ機体とみなす高度差の上限 [ft] |
| `direct_tau_tolerance_ns` | 5000 | 直接照射（主ビーム・サイドローブ）とみなす τ の幅 [ns]。これより τ の大きい同じ機体のプロットは反射として落とす |

**位置推定**

| キー | 既定 | 説明 |
| --- | --- | --- |
| `z_margin_m` | 200 | 解の一意性判定に持たせる余裕 [m]。SSR 直上の除外円錐の広さを決める |
| `baseline_margin_m` | 500 | 双基地距離 − 基線長の下限 [m]。機体が基線に近すぎる幾何を解かない |
| `curvature_tol_m` | 0.05 | 地球の曲率を取り込む反復の収束判定 [m] |
| `curvature_max_iter` | 5 | 同じ反復の上限回数。1 以上 |
| `sigma_timing_ns` | 100 | 質問時刻と受信時刻のジッタを合成した標準偏差 [ns]。共分散の計算に使う |
| `sigma_transponder_ns` | 288.7 | 応答遅延の公差の標準偏差 [ns]（±0.5 µs の一様分布 = 0.5 / √3 µs） |
| `sigma_azimuth_rad` | 0.003491 | 応答列が完全なときのビーム中心の方位の標準偏差 [rad]（0.20 度。ADS-B との比較で較正） |
| `dwell_full_replies` | 14 | 応答列を完全とみなす応答数。これに満たない列はドウェルの断片で、方位の標準偏差が欠けた応答 1 件あたり `azimuth_fragment_factor` × （1 質問あたりのビームの回転角）だけ増える |
| `azimuth_fragment_factor` | 0.77 | 欠けた応答 1 件あたりに増す方位の標準偏差の倍率（1 質問あたりの回転角に対して） |
| `sigma_altitude_m` | 8.8 | 高さの標準偏差 [m]（Mode C の 100 ft 量子化 = 30.48 / √12） |

`sigma_*` は出力の σ_E / σ_N / σ_U（位置の標準偏差）と、連続性の門の幅に
効く。位置そのものは変えない。

**連続性の判定**

| キー | 既定 | 説明 |
| --- | --- | --- |
| `track_max_speed_mps` | 350 | 門の速度上限 [m/s]。前の点からの移動がこれ × Δt に位置の誤差を足した幅を超えたら別の便 |
| `track_max_climb_ftps` | 100 | 門の高度変化率の上限 [ft/s]（6,000 ft/min） |
| `track_gate_sigmas` | 3 | 門に足す位置の標準偏差（`sigma_*` から出る σ_E / σ_N）の倍率 |
| `track_max_missed_scans` | 2 | 便を打ち切らずに許す欠測の走査数 |
| `track_confirm_hits` | 3 | 便を確定するのに要る点数。確定しなかった便の点は `status: unconfirmed` になる。1 にすると棄却しない |

## 複数の SSR・局を書く

`ssrs` / `stations` には処理対象の全部を書いておき、実行ごとに引数で選ぶ。

```yaml
ssrs:
  KX90S: { ... }
  KX00S: { ... }
stations:
  KX90: { ... }
  KX00: { ... }
```

```sh
pssrx run -config master.yaml -ssr KX90S -station KX90 ...
pssrx run -config master.yaml -ssr KX00S -station KX00 -reply-stations KX00 ...
```

1 つの局が複数の SSR の質問を受信している場合（PRI の異なる SSR が混在）、
SSR ごとに `pssrx run` を実行する。質問予定表を作る段が、指定した SSR の
質問パターンに合う連鎖だけを選り分ける。

## エラーの例

| 症状 | 原因 |
| --- | --- |
| `unknown field "pattern"` | キー名の打ち間違い、または古い形式。行番号が示される |
| `lat, lon, alt は 3 つとも必要です` | 位置の要素が欠けている。`alt: 0` も省略できない |
| `ssrs.KX90S: interrogation.interval_pattern_ns は 1 要素以上必要です` | 質問間隔が空 |
| `SSR KX90S の覆域 450000 m では遅延の上限 ... が最短 PRI ... 以上になり` | `max_range_m` が質問間隔に対して広すぎる |
| `geoid: coordinate out of JPGEO2024 range` | 位置が日本国外 |
| `analysis.pssr: 対応づけの定数が不正` | `analysis` の値が範囲外（`min_replies: 0` など） |
