package analyze

import (
	"math"
	"testing"
)

// TestStationGeometryKnownPair は実運用と同じ桁の ENU 座標に対する
// 距離と方位を固定する。ここがずれると全レコードの時刻と方位が
// まとめてずれるので、値そのものをビット単位で押さえておく。
func TestStationGeometryKnownPair(t *testing.T) {
	// 名古屋の SSR を原点にした測定局の ENU（testdata/kx90.yaml の 2 点）
	dist, az := StationGeometry(-937.6096636162918, 864.0894997525627, -0.17023163525180962)
	if want := 1275.0539493951226; dist != want {
		t.Errorf("dist = %x, 期待 %x", dist, want)
	}
	if want := 5.4570037548680626; az != want {
		t.Errorf("azimuth = %x, 期待 %x", az, want)
	}
}

// TestStationGeometrySlantRange は距離が U を含む斜距離であることを固定する。
// 伝搬遅延の補正に使うので、水平距離にすると高低差ぶん遅延を過小に見積もる。
func TestStationGeometrySlantRange(t *testing.T) {
	dist, _ := StationGeometry(3, 4, 12)
	if dist != 13 {
		t.Errorf("dist = %g, 期待 13 (= sqrt(3^2+4^2+12^2))", dist)
	}
}

// TestStationGeometryQuadrants は ENU の軸の向き（N が北、E が東）に
// 対して方位が [0, 2pi) で北基準・東回りになることを確認する。
func TestStationGeometryQuadrants(t *testing.T) {
	cases := []struct {
		name    string
		n, e    float64
		wantDeg float64
	}{
		{"真北", 1, 0, 0},
		{"真東", 0, 1, 90},
		{"真南", -1, 0, 180},
		{"真西", 0, -1, 270},
		{"北東", 1, 1, 45},
		{"北西", 1, -1, 315},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, az := StationGeometry(c.e, c.n, 0)
			if got := az * 180 / math.Pi; math.Abs(got-c.wantDeg) > 1e-9 {
				t.Errorf("方位 = %g 度, 期待 %g 度", got, c.wantDeg)
			}
			if az < 0 || az >= 2*math.Pi {
				t.Errorf("方位 %g が [0, 2pi) の外", az)
			}
		})
	}
}

// TestWrapAngle は方位角が必ず [0, 2pi) に収まることを確認する。
//
// 負のまま通すと、ファイル書き出しの uint32 変換が Go の仕様上「実装依存」に
// なる（amd64 では 2^32 で巻き戻り、arm64 では 0 に飽和する）。ドウェル先頭では
// 内挿の重みが負になるため、局方位が小さい配置では実際に負の値が来る。
func TestWrapAngle(t *testing.T) {
	const twoPi = 2 * math.Pi
	cases := []float64{
		0, 0.1, math.Pi, twoPi - 1e-9, twoPi, twoPi + 0.1,
		-0.1, -twoPi, -twoPi - 0.1, -1e-15, 3 * twoPi, -3 * twoPi,
		// 局方位 0.225 度で内挿の重みが -0.0057 のときに実際に出る値
		0.003922 - twoPi*0.0057,
	}
	for _, x := range cases {
		got := wrapAngle(x)
		if !(got >= 0 && got < twoPi) {
			t.Errorf("wrapAngle(%v) = %v が [0, 2pi) の外", x, got)
		}
		// 2pi の整数倍だけずらしても同じ結果になること
		if other := wrapAngle(x + 4*twoPi); math.Abs(other-got) > 1e-9 && math.Abs(other-got) < twoPi-1e-9 {
			t.Errorf("wrapAngle(%v) = %v だが 2pi 4 周ぶん足すと %v", x, got, other)
		}
		// uint32 へ落としても飽和・巻き戻りが起きないこと
		if u := uint32(got / twoPi * 0xFFFFFFFF); float64(u) > 0xFFFFFFFF {
			t.Errorf("wrapAngle(%v) = %v の uint32 変換が範囲外 %d", x, got, u)
		}
	}
}
