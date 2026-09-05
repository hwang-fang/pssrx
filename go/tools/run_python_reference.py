"""Python 実装を samples/ のレイアウトで動かして参照 .intg を吐く。

移植元のコードには手を入れず、ディレクトリレイアウトの違いと
読み込み後のソート有無だけをサブクラスで差し替える。

  --sort-input   読み込み後にタイムスタンプで安定ソートする（Q7 の (b)）
  なし           移植元そのままの挙動（Q7 の (a)）
"""

import argparse
import json
import math
import os
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path

sys.path.insert(0, os.getcwd())

import numpy as np  # noqa: E402

from interrogator.analyze import analyze_qdata  # noqa: E402
from interrogator.domain import ChainConfig, Dwell, InterrogationParameter  # noqa: E402
from interrogator.repository import IntgRepository, QdataRepository  # noqa: E402
from interrogator.timestamp import JST_TZ, datetime_to_nano, nano_to_datetime  # noqa: E402


class SamplesQdataRepository(QdataRepository):
    """samples/ の実レイアウトに合わせたパス解決＋任意の安定ソート。"""

    def __init__(self, root_dir: Path, sort_input: bool):
        super().__init__(root_dir)
        self.sort_input = sort_input

    def _resolve_file_path(self, stationid, start, end):
        st = nano_to_datetime(start).replace(second=0, microsecond=0)
        ed = nano_to_datetime(end - 1).replace(second=0, microsecond=0)
        files, dt = [], st
        while dt <= ed:
            qpkx = self.root_dir / f"{dt:%Y%m}/{stationid}/{dt:%Y%m%d}/qpkx/{dt:%Y%m%d%H%M}{stationid}.qpkx"
            if qpkx.is_file():
                files.append((datetime_to_nano(dt), qpkx))
            dt += timedelta(minutes=1)
        return files

    def fetch_data(self, stationid, start, end):
        q = super().fetch_data(stationid, start, end)
        if self.sort_input and q.size:
            # kind="stable" が Go 側の SortStableFunc に対応する
            q = q[np.argsort(q["timestamp"], kind="stable")]
        return q


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--qpkx-root", required=True)
    ap.add_argument("--intg-root", required=True)
    ap.add_argument("--station", required=True)
    ap.add_argument("--ssr", required=True)
    ap.add_argument("--from", dest="frm", required=True, help="JST 2026-06-10T00:00")
    ap.add_argument("--to", required=True)
    ap.add_argument("--quest", required=True, help="質問種別文字列 例 AC")
    ap.add_argument("--quest-cycle-100ns", type=int, required=True)
    ap.add_argument("--around-time-sec", type=float, required=True)
    ap.add_argument("--ssr-x", type=float, required=True)
    ap.add_argument("--ssr-y", type=float, required=True)
    ap.add_argument("--st-x", type=float, required=True)
    ap.add_argument("--st-y", type=float, required=True)
    ap.add_argument("--sort-input", action="store_true")
    ap.add_argument("--reduce-pattern", action="store_true",
                    help="質問種別を最小周期へ簡約する（Go 側の既定挙動に合わせる）")
    ap.add_argument("--stats-json", default="")
    args = ap.parse_args()

    from interrogator.domain import INTG_MODE_CODE

    modes = [INTG_MODE_CODE[c] for c in args.quest]
    if args.reduce_pattern:
        for p in range(1, len(modes) + 1):
            if len(modes) % p == 0 and all(modes[i] == modes[i % p] for i in range(len(modes))):
                modes = modes[:p]
                break

    intg_param = InterrogationParameter(
        around_time_ns=int(args.around_time_sec * 1e9),
        stagger_ns=[args.quest_cycle_100ns * 100],
        modes=modes,
        interval_tolerance_ns=30000,
        clockwise=True,
    )
    cfg = ChainConfig()

    dx, dy = args.st_x - args.ssr_x, args.st_y - args.ssr_y
    st_dist = math.sqrt(dx * dx + dy * dy)
    st_azimuth = math.atan2(dy, dx)
    if st_azimuth < 0:
        st_azimuth += 2 * math.pi

    q_repo = SamplesQdataRepository(Path(args.qpkx_root), args.sort_input)
    i_repo = IntgRepository(Path(args.intg_root))

    start = datetime.strptime(args.frm, "%Y-%m-%dT%H:%M").replace(tzinfo=JST_TZ)
    end = datetime.strptime(args.to, "%Y-%m-%dT%H:%M").replace(tzinfo=JST_TZ)
    one_minute = timedelta(minutes=1)
    period = int(intg_param.pattern.period * cfg.dwell_gap_periods)

    cur, put_off, last_dwell = start, None, None
    perf, total_records = [], 0
    while cur < end:
        st, ed = datetime_to_nano(cur), datetime_to_nano(cur + one_minute)
        is_last = (cur + one_minute) >= end

        qdata = q_repo.fetch_data(args.station, st, ed)
        if put_off is not None and put_off.size > 0:
            qdata = np.concatenate([put_off, qdata])

        t1 = time.perf_counter()
        intg, put_off, last_dwell = analyze_qdata(
            qdata, st_dist, st_azimuth, intg_param, cfg,
            put_off_ts=ed - period if not is_last else None,
            last_dwell=last_dwell,
        )
        perf.append(time.perf_counter() - t1)
        total_records += int(intg.size)
        i_repo.save_data(args.ssr, intg)
        cur += one_minute

    stats = {
        "pattern_length": int(intg_param.pattern.length),
        "pattern_period_ns": int(intg_param.pattern.period),
        "around_time_ns": int(intg_param.around_time_ns),
        "st_dist": st_dist,
        "st_dist_hex": float(st_dist).hex(),
        "st_azimuth": st_azimuth,
        "st_azimuth_hex": float(st_azimuth).hex(),
        "delay_ns": int(round(st_dist / 0.299792458)),
        "blocks": len(perf),
        "records": total_records,
        "mean_sec": float(np.mean(perf)),
        "min_sec": float(min(perf)),
        "max_sec": float(max(perf)),
        "total_sec": float(np.sum(perf)),
    }
    print(json.dumps(stats, indent=1, ensure_ascii=False))
    if args.stats_json:
        Path(args.stats_json).write_text(json.dumps(stats, indent=1, ensure_ascii=False))


if __name__ == "__main__":
    main()
