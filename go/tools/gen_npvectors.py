"""numpy/Python の数値意味論を実測して Go 側テストベクタ(JSON)を吐く。

Go 実装が numpy の挙動をビット単位で再現できているかを検証するのが目的なので、
浮動小数点値はすべて float.hex() で往復無損失に出力する。
"""

import json
import os
import sys

sys.path.insert(0, os.getcwd())

import numpy as np


def h(x):
    return float(x).hex()


def hl(xs):
    return [h(v) for v in xs]


out = {"numpy_version": np.__version__, "python_version": sys.version.split()[0]}

# --- np.sum (pairwise summation) --------------------------------------
rng = np.random.default_rng(20260905)
sums = []
for n in [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 15, 16, 17, 20, 24, 31, 32, 33, 63, 64, 65, 127, 128, 129, 200, 257]:
    a = rng.uniform(-1e3, 1e3, n) if n else np.zeros(0)
    # 残差二乗和と同じ形（非負・桁の揃わない値）も試す
    b = a * a
    sums.append({"n": n, "a": hl(a), "sum_a": h(np.sum(a)), "sum_b": h(np.sum(b))})
out["pairwise_sum"] = sums

# --- np.mean (int64 配列) ----------------------------------------------
means = []
for n in [1, 2, 3, 5, 8, 13, 17, 20, 33]:
    v = rng.integers(-50_000, 50_000, n, dtype=np.int64)
    means.append({"v": [int(x) for x in v], "mean": h(np.mean(v))})
out["mean_int64"] = means

# --- np.median ----------------------------------------------------------
medians = []
for n in [1, 2, 3, 4, 5, 8, 9, 16, 17]:
    a = rng.uniform(-100, 100, n)
    medians.append({"a": hl(a), "median": h(np.median(a))})
out["median"] = medians

# --- round / np.rint（偶数丸め）-----------------------------------------
rnd = []
for v in [-2.5, -1.5, -0.5, 0.5, 1.5, 2.5, 3.5, 0.49999999999999994,
          2.675, -2.675, 1e15 + 0.5, 4.05e9, 1393.5, 1394.5, -1393.5]:
    rnd.append({"x": h(v), "py_round": int(round(v)), "np_rint": h(np.rint(v))})
out["round_half_even"] = rnd

# --- np.mod（float, 除数の符号に合わせる）-------------------------------
two_pi = 2.0 * np.pi
mods = []
for v in [0.1, -0.1, 6.28, -6.28, 7.0, -7.0, 0.0, -0.0, 1e-18, -1e-18,
          two_pi, -two_pi, 2 * two_pi, -1e-30, 5.413015981160358, -12.5663706]:
    mods.append({"x": h(v), "y": h(two_pi), "mod": h(np.mod(v, two_pi))})
out["np_mod_two_pi"] = mods

# --- float -> uint32 キャスト（切り捨て）--------------------------------
casts = []
arr = np.array([0.0, 0.5, 1.9999, 4294967294.9999, 4294967295.0,
                3700149760.4, 0.9999999999, 1234.5678], dtype=np.float64)
u = np.empty(arr.size, dtype="<u4")
u[:] = arr
for x, y in zip(arr, u):
    casts.append({"x": h(x), "u32": int(y)})
out["f64_to_u32"] = casts

# --- searchsorted: int64 配列 + float64 スカラ（float64 に昇格）---------
base = 1_784_000_000_000_000_000  # 2026 年の unix nanos 相当
t = np.sort(base + rng.integers(0, 60_000_000_000, 37, dtype=np.int64))
ss = []
for off in [-1.0, 0.0, 1.0, 12345.6, 2906500.0, 20000.0, 1e9, 3.5e10, 6e10]:
    for probe_i in [0, 5, 18, 36]:
        v = float(t[probe_i]) + off
        ss.append({"v": h(v),
                   "left": int(np.searchsorted(t, v, side="left")),
                   "right": int(np.searchsorted(t, v, side="right"))})
out["searchsorted"] = {"t": [int(x) for x in t], "probes": ss}

# --- int64 + float64 の加算（1.78e18 での精度落ち）------------------------
prec = []
for ti in [base, base + 1, base + 255, base + 256, base + 12_345_678_901]:
    for exp in [2906500.0, 5813000.0, 8719500.0]:
        prec.append({"ti": int(ti), "exp": h(exp),
                     "ti_plus_exp_minus_gate": h(ti + exp - 20000.0),
                     "ti_plus_exp_plus_gate": h(ti + exp + 20000.0)})
out["int64_float_add"] = prec

# --- np.linalg.solve（3x3, 放物線フィットと同じ形）------------------------
solves = []
rng2 = np.random.default_rng(7)
for trial in range(12):
    n = int(rng2.integers(6, 25))
    u = np.sort(rng2.uniform(-25.0, 25.0, n))
    y = -0.01 * u * u + 0.3 * u - 30.0 + rng2.normal(0, 0.4, n)
    x = np.column_stack([np.ones_like(u), u, u * u])
    A = x.T @ x
    b = x.T @ y
    beta = np.linalg.solve(A, b)
    resid = y - x @ beta
    solves.append({
        "u": hl(u), "y": hl(y),
        "A": [hl(r) for r in A], "b": hl(b),
        "beta": hl(beta),
        "resid": hl(resid),
        "sumsq": h(np.sum(resid ** 2)),
    })
out["lstsq3"] = solves

# --- 波高値エンコード/デコード -------------------------------------------
from interrogator.domain import decode_waveheight, encode_waveheight  # noqa: E402

wh = []
for v in [-35.0, -0.0, -255.0, -12.34, -1.0 / 256, -100.5]:
    wh.append({"dbm": h(v), "enc": encode_waveheight(v)})
dec = []
vals = np.array([0, 1, 255, 56575, 60030, 65535], dtype=np.uint16)
for v, d in zip(vals, decode_waveheight(vals)):
    dec.append({"raw": int(v), "dbm": h(d)})
out["waveheight"] = {"encode": wh, "decode": dec}

json.dump(out, sys.stdout, indent=1)
