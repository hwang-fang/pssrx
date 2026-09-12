"""Python 側 InterrogationPattern の挙動をテストベクタ化する。"""

import json
import os
import sys

sys.path.insert(0, os.getcwd())

from interrogator.domain import INTG_MODE_CODE, InterrogationPattern  # noqa: E402

CASES = [
    # (名前, stagger_ns, 種別文字列)  centrair.txt 実値と、簡約が効く/効かない例
    ("centrair", [2906500], "ACAC"),
    ("centrair_min", [2906500], "AC"),
    ("kx00", [2928400], "AC"),
    ("single", [2906500], "A"),
    ("stagger3", [2900000, 2906500, 2913000], "AC"),
    ("stagger2_mode3", [2900000, 2910000], "ACB"),
    ("redundant", [2906500, 2906500], "ACAC"),
    ("mode4", [2906500], "ABCD"),
]

out = []
for name, stagger, quest in CASES:
    modes = [INTG_MODE_CODE[c] for c in quest]
    p = InterrogationPattern.from_stagger(stagger, modes)
    rec = {
        "name": name,
        "stagger_ns": stagger,
        "quest": quest,
        "modes_in": modes,
        # 展開後（簡約なし）の内部表現
        "raw_length": int(p.length),
        "raw_period": int(p.period),
        "raw_intervals": [int(v) for v in p.intervals],
        "raw_modes": [int(v) for v in p.modes],
        "mean_pri": float(p.mean_pri).hex(),
    }
    # delta / cumulative は簡約しても不変であるべき（これが簡約の正当性）
    rec["cumulative"] = [[n, int(p.cumulative(n))] for n in range(-7, 25)]
    rec["delta"] = [[ph, d, int(p.delta(ph, d))]
                    for ph in range(0, int(p.length) * 2)
                    for d in [1, 2, 3, 7, 12, 49, 1393, 1394]]
    rec["mode_at"] = [[n, int(p.mode_at(n))] for n in range(-5, 13)]

    # _relative_pattern_times
    from interrogator.analyze import _relative_pattern_times  # noqa: E402

    rel = {}
    for p0 in range(0, int(p.length)):
        for count in [0, 1, 2, 5, 17, 100]:
            rel[f"{p0}:{count}"] = [int(v) for v in _relative_pattern_times(p, p0, count)]
    rec["relative_times"] = rel
    out.append(rec)

json.dump(out, sys.stdout, indent=1)
