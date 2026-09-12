package numeric

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
)

// 参照ベクタ testdata/vectors.json は、このパッケージが返すべき値を固定した
// もの。浮動小数点は 16 進表記で保存してあるので往復無損失で、比較はビット
// 単位で行う。値は移植元の Python 実装（numpy）が返したものを固定した。
// 生成スクリプトは Python 実装とともに退役済みで、以後はこのファイルが
// 唯一の正解になる。

// maxLUUlp は Solve3 が参照値からずれてよい上限。実測は 2 ulp。
const maxLUUlp = 2

type vectors struct {
	MeanInt64 []struct {
		V    []int64 `json:"v"`
		Mean string  `json:"mean"`
	} `json:"mean_int64"`
	Median []struct {
		A      []string `json:"a"`
		Median string   `json:"median"`
	} `json:"median"`
	Bounds struct {
		T      []int64 `json:"t"`
		Probes []struct {
			V           string `json:"v"`
			Left, Right int
		} `json:"probes"`
	} `json:"searchsorted"`
	Lstsq3 []struct {
		U    []string   `json:"u"`
		Y    []string   `json:"y"`
		A    [][]string `json:"A"`
		B    []string   `json:"b"`
		Beta []string   `json:"beta"`
	} `json:"lstsq3"`
}

func load(t *testing.T) *vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	v := new(vectors)
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
	return v
}

func hexf(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("16 進 float %q: %v", s, err)
	}
	return v
}

func hexfs(t *testing.T, ss []string) []float64 {
	t.Helper()
	out := make([]float64, len(ss))
	for i, s := range ss {
		out[i] = hexf(t, s)
	}
	return out
}

// bitEq はビット単位の一致を見る。NaN 同士も一致とみなす。
func bitEq(a, b float64) bool {
	return math.Float64bits(a) == math.Float64bits(b) || (math.IsNaN(a) && math.IsNaN(b))
}

// ulpDiff は 2 つの float64 が何 ulp 離れているかを返す。
func ulpDiff(a, b float64) int64 {
	if a == b {
		return 0
	}
	ia, ib := int64(math.Float64bits(a)), int64(math.Float64bits(b))
	if (ia < 0) != (ib < 0) {
		return math.MaxInt64
	}
	d := ia - ib
	if d < 0 {
		return -d
	}
	return d
}

func TestMeanInt64(t *testing.T) {
	for _, c := range load(t).MeanInt64 {
		if got, want := MeanInt64(c.V), hexf(t, c.Mean); !bitEq(got, want) {
			t.Errorf("n=%d MeanInt64 = %x, 参照値 = %x", len(c.V), got, want)
		}
	}
}

func TestMedian(t *testing.T) {
	for _, c := range load(t).Median {
		a := hexfs(t, c.A)
		orig := append([]float64(nil), a...)
		if got, want := Median(a), hexf(t, c.Median); !bitEq(got, want) {
			t.Errorf("n=%d Median = %x, 参照値 = %x", len(c.A), got, want)
		}
		for i := range a {
			if a[i] != orig[i] {
				t.Fatalf("Median が入力を書き換えている")
			}
		}
	}
}

// TestBoundsOnFloat64View は、Unix ナノ秒を float64 に落とした配列に対する
// 二分探索の位置を固定する。連鎖検出の探索窓がこの形で決まる。
//
// 約 1.78e18 では float64 の刻みが 256 ns あるため、窓の端はこの粒度へ
// 丸まる。窓の広さは 20 us なので実害は無いが、境界に乗った観測が窓に
// 入るかどうかはこの丸めで決まる。
func TestBoundsOnFloat64View(t *testing.T) {
	v := load(t)
	tf := make([]float64, len(v.Bounds.T))
	for i, x := range v.Bounds.T {
		tf[i] = float64(x)
	}
	for _, p := range v.Bounds.Probes {
		val := hexf(t, p.V)
		if got := LowerBound(tf, val); got != p.Left {
			t.Errorf("LowerBound(%v) = %d, 参照値 = %d", val, got, p.Left)
		}
		if got := UpperBound(tf, val); got != p.Right {
			t.Errorf("UpperBound(%v) = %d, 参照値 = %d", val, got, p.Right)
		}
	}
	// 隣り合う 2 つのタイムスタンプが同じ float64 に潰れることの確認。
	// 潰れても配列は非減少なので二分探索は成立する。
	const base = 1_784_000_000_000_000_000
	if float64(base) != float64(base+1) {
		t.Error("1.78e18 で 1 ns の差が float64 に残っている（前提が変わった）")
	}
}

// TestSolve3 は LU 分解を切り分けて検証する。参照ベクタの A と b をそのまま
// 与えることで、正規方程式の組み立て（次のテストが扱う）で生じる誤差を
// 混入させずに済む。
//
// 消去と代入の順序を変えると最下位ビットが動くため、順序の候補 16 通り
// （逆数乗算か除算か・列方向か行方向か・FMA の有無・ピボット同値時の
// 先勝ちか後勝ちか）を実測して最良のものを Solve3 に採ってある。それでも
// 36 個中 6 個は 1〜2 ulp 残る。参照値の側が CPU の派生機能でカーネルを
// 切り替える実装なので、完全一致を追うと別マシンで壊れる。ここは ulp 上限で
// 固定し、実害が無いことは TestNormalEquations3Tolerance が物理量で担保する。
func TestSolve3(t *testing.T) {
	var worst int64
	for i, c := range load(t).Lstsq3 {
		var a [3][3]float64
		var b [3]float64
		for r := range 3 {
			for cc := range 3 {
				a[r][cc] = hexf(t, c.A[r][cc])
			}
			b[r] = hexf(t, c.B[r])
		}
		beta, ok := Solve3(a, b)
		if !ok {
			t.Fatalf("試行%d: Solve3 が特異と判定", i)
		}
		for r := range 3 {
			want := hexf(t, c.Beta[r])
			if d := ulpDiff(beta[r], want); d > maxLUUlp {
				t.Errorf("試行%d beta[%d] = %x, 参照値 = %x (ulp差 %d > 許容 %d)",
					i, r, beta[r], want, d, maxLUUlp)
			} else if d > worst {
				worst = d
			}
		}
	}
	t.Logf("LU の最大 ulp 差 %d（許容 %d）", worst, maxLUUlp)
}

// TestSolve3Singular は解けない系で ok=false が返ることを確認する。
func TestSolve3Singular(t *testing.T) {
	// 3 行が一次従属（観測時刻が 1 点に潰れた場合に相当）
	a := [3][3]float64{{1, 1, 1}, {1, 1, 1}, {1, 1, 1}}
	if _, ok := Solve3(a, [3]float64{1, 2, 3}); ok {
		t.Error("特異行列が解けたことになっている")
	}
}

// TestNormalEquations3Tolerance は正規方程式の組み立てに残る差を許容差として
// 固定する。参照値の側は行列積のカーネルを CPU の派生機能で切り替えるため、
// 総和順序を再現できない。素朴・FMA・pairwise・4 アキュムレータのいずれも
// 完全一致しないことを実測済みなので、ビット一致は追わない。代わりに、その差が
// 最終出力に効かないことを物理量で担保する: 頂点時刻の差が 1 ns 丸めの
// 境界（0.5 ns）を跨がないこと。
func TestNormalEquations3Tolerance(t *testing.T) {
	const centerBudgetNs = 1e-3 // 0.5 ns 境界に対して 500 倍の余裕を要求する

	var worstNs float64
	for i, c := range load(t).Lstsq3 {
		u, y := hexfs(t, c.U), hexfs(t, c.Y)
		a, b := NormalEquations3(u, y, nil)
		beta, ok := Solve3(a, b)
		if !ok {
			t.Fatalf("試行%d: Solve3 が特異と判定", i)
		}
		var want [3]float64
		for r := range 3 {
			want[r] = hexf(t, c.Beta[r])
		}
		// 頂点時刻は uc = -a1/(2*a2) を ns へ直したものなので、uc の差をそのまま見る
		ucGot, ucWant := -beta[1]/(2*beta[2]), -want[1]/(2*want[2])
		dNs := math.Abs(ucGot-ucWant) * 1e6
		if dNs > worstNs {
			worstNs = dNs
		}
		if dNs > centerBudgetNs {
			t.Errorf("試行%d: 頂点時刻の差 %.3e ns が許容 %.3e ns を超過", i, dNs, centerBudgetNs)
		}
	}
	t.Logf("頂点時刻の最大差 %.3e ns（1 ns 丸めの境界は 0.5 ns）", worstNs)
}

// TestNormalEquations3Mask は外れ値を落とした行が寄与しないことを確認する。
func TestNormalEquations3Mask(t *testing.T) {
	u := []float64{-2, -1, 0, 1, 2, 100}
	y := []float64{-4, -1, 0, -1, -4, 9999}
	mask := []bool{true, true, true, true, true, false}

	full, fb := NormalEquations3(u[:5], y[:5], nil)
	masked, mb := NormalEquations3(u, y, mask)
	if full != masked || fb != mb {
		t.Errorf("mask で除いた行が結果に残っている\n  mask あり %v %v\n  そもそも渡さない %v %v",
			masked, mb, full, fb)
	}
}
