package npcompat

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
)

// テストベクタは tools/gen_npvectors.py が実際の numpy から生成する。
// 浮動小数点は float.hex() 形式なので往復無損失。

type vectors struct {
	NumpyVersion string `json:"numpy_version"`
	PairwiseSum  []struct {
		N    int      `json:"n"`
		A    []string `json:"a"`
		SumA string   `json:"sum_a"`
		SumB string   `json:"sum_b"`
	} `json:"pairwise_sum"`
	MeanInt64 []struct {
		V    []int64 `json:"v"`
		Mean string  `json:"mean"`
	} `json:"mean_int64"`
	Median []struct {
		A      []string `json:"a"`
		Median string   `json:"median"`
	} `json:"median"`
	RoundHalfEven []struct {
		X       string `json:"x"`
		PyRound int64  `json:"py_round"`
		NpRint  string `json:"np_rint"`
	} `json:"round_half_even"`
	NpModTwoPi []struct {
		X, Y, Mod string
	} `json:"np_mod_two_pi"`
	F64ToU32 []struct {
		X   string `json:"x"`
		U32 uint32 `json:"u32"`
	} `json:"f64_to_u32"`
	SearchSorted struct {
		T      []int64 `json:"t"`
		Probes []struct {
			V           string `json:"v"`
			Left, Right int
		} `json:"probes"`
	} `json:"searchsorted"`
	Int64FloatAdd []struct {
		Ti                 int64  `json:"ti"`
		Exp                string `json:"exp"`
		TiPlusExpMinusGate string `json:"ti_plus_exp_minus_gate"`
		TiPlusExpPlusGate  string `json:"ti_plus_exp_plus_gate"`
	} `json:"int64_float_add"`
	Lstsq3 []struct {
		U     []string   `json:"u"`
		Y     []string   `json:"y"`
		A     [][]string `json:"A"`
		B     []string   `json:"b"`
		Beta  []string   `json:"beta"`
		Resid []string   `json:"resid"`
		Sumsq string     `json:"sumsq"`
	} `json:"lstsq3"`
	Waveheight struct {
		Encode []struct {
			Dbm string `json:"dbm"`
			Enc int    `json:"enc"`
		} `json:"encode"`
		Decode []struct {
			Raw int    `json:"raw"`
			Dbm string `json:"dbm"`
		} `json:"decode"`
	} `json:"waveheight"`
}

// maxLUUlp は Solve3 が LAPACK からずれてよい上限。実測は 2 ulp。
const maxLUUlp = 2

func hexf(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("hex float %q: %v", s, err)
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

func load(t *testing.T) *vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/npvectors.json")
	if err != nil {
		t.Fatal(err)
	}
	v := new(vectors)
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSumMatchesNumpy(t *testing.T) {
	v := load(t)
	for _, c := range v.PairwiseSum {
		a := hexfs(t, c.A)
		if got, want := Sum(a), hexf(t, c.SumA); !bitEq(got, want) {
			t.Errorf("n=%d Sum(a) = %x, numpy = %x (差 %g)", c.N, got, want, got-want)
		}
		b := make([]float64, len(a))
		for i, x := range a {
			b[i] = x * x
		}
		if got, want := Sum(b), hexf(t, c.SumB); !bitEq(got, want) {
			t.Errorf("n=%d Sum(a*a) = %x, numpy = %x (差 %g)", c.N, got, want, got-want)
		}
	}
}

func TestMeanInt64MatchesNumpy(t *testing.T) {
	v := load(t)
	for _, c := range v.MeanInt64 {
		if got, want := MeanInt64(c.V), hexf(t, c.Mean); !bitEq(got, want) {
			t.Errorf("n=%d MeanInt64 = %x, numpy = %x", len(c.V), got, want)
		}
	}
}

func TestMedianMatchesNumpy(t *testing.T) {
	v := load(t)
	for _, c := range v.Median {
		if got, want := Median(hexfs(t, c.A)), hexf(t, c.Median); !bitEq(got, want) {
			t.Errorf("n=%d Median = %x, numpy = %x", len(c.A), got, want)
		}
	}
}

func TestRoundHalfEvenMatchesPython(t *testing.T) {
	v := load(t)
	for _, c := range v.RoundHalfEven {
		x := hexf(t, c.X)
		if got := RoundToInt64(x); got != c.PyRound {
			t.Errorf("RoundToInt64(%v) = %d, Python round() = %d", x, got, c.PyRound)
		}
		if got, want := RoundHalfEven(x), hexf(t, c.NpRint); !bitEq(got, want) {
			t.Errorf("RoundHalfEven(%v) = %v, np.rint = %v", x, got, want)
		}
		// math.Round との差が実際に出ることを確認しておく（この差が移植バグの温床）
		if r := math.Round(x); r != RoundHalfEven(x) && math.Abs(x-math.Trunc(x)) == 0.5 {
			t.Logf("参考: math.Round(%v)=%v は偶数丸め %v と異なる", x, r, RoundHalfEven(x))
		}
	}
}

func TestModMatchesNumpy(t *testing.T) {
	v := load(t)
	for _, c := range v.NpModTwoPi {
		x, y := hexf(t, c.X), hexf(t, c.Y)
		if got, want := Mod(x, y), hexf(t, c.Mod); !bitEq(got, want) {
			t.Errorf("Mod(%v, 2pi) = %v, np.mod = %v", x, got, want)
		}
	}
}

func TestTruncToUint32MatchesNumpy(t *testing.T) {
	v := load(t)
	for _, c := range v.F64ToU32 {
		x := hexf(t, c.X)
		if got := TruncToUint32(x); got != c.U32 {
			t.Errorf("TruncToUint32(%v) = %d, numpy = %d", x, got, c.U32)
		}
	}
}

// TestSearchSortedMatchesNumpy は int64 配列に float64 スカラで検索したときの
// 挙動を確認する。numpy は共通型 float64 に昇格させるため、1.78e18 付近では
// タイムスタンプが 256 ns に量子化される。best_chain の窓決めがこの精度で
// 動いているので、Go 側も同じく float64 化してから二分探索する必要がある。
func TestSearchSortedMatchesNumpy(t *testing.T) {
	v := load(t)
	tf := make([]float64, len(v.SearchSorted.T))
	for i, x := range v.SearchSorted.T {
		tf[i] = float64(x)
	}
	for _, p := range v.SearchSorted.Probes {
		val := hexf(t, p.V)
		if got := SearchSortedLeft(tf, val); got != p.Left {
			t.Errorf("SearchSortedLeft(%v) = %d, numpy = %d", val, got, p.Left)
		}
		if got := SearchSortedRight(tf, val); got != p.Right {
			t.Errorf("SearchSortedRight(%v) = %d, numpy = %d", val, got, p.Right)
		}
	}
}

// TestInt64FloatAddPrecision は int64 + float64 の精度落ちを Go が同じく
// 再現することを確認する。ここが一致しないと DP の探索窓がずれる。
func TestInt64FloatAddPrecision(t *testing.T) {
	v := load(t)
	const gate = 20000.0
	for _, c := range v.Int64FloatAdd {
		exp := hexf(t, c.Exp)
		lo := float64(c.Ti) + exp - gate
		hi := float64(c.Ti) + exp + gate
		if want := hexf(t, c.TiPlusExpMinusGate); !bitEq(lo, want) {
			t.Errorf("ti=%d exp=%v: lo = %x, numpy = %x", c.Ti, exp, lo, want)
		}
		if want := hexf(t, c.TiPlusExpPlusGate); !bitEq(hi, want) {
			t.Errorf("ti=%d exp=%v: hi = %x, numpy = %x", c.Ti, exp, hi, want)
		}
	}
}

// TestSolve3MatchesLAPACK は LU 分解そのものが LAPACK dgesv を再現できて
// いるかを切り分けて検証する。numpy が算出した A と b をそのまま与え、
// beta がビット単位で一致することを要求する。行列積（BLAS dgemm）の
// 誤差を混入させないため、ここでは NormalEquations3 を通さない。
//
// dgetf2/dtrsv の実装バリアント 16 通り（逆数乗算か除算か・列方向か行方向か・
// FMA の有無・ピボット同値時の先勝ちか後勝ちか）を総当たりで実測した結果、
// 本実装の組み合わせが最良で 30/36 がビット一致、残りも 1-2 ulp だった。
// numpy が呼ぶのは参照 LAPACK ではなく OpenBLAS の dgetrf であり、その
// カーネルは CPU の派生機能で切り替わる。完全一致を追うと別マシンで
// 壊れる実装になるため、ここは ulp 上限で固定し、実害の有無は
// TestNormalEquations3Tolerance が物理量で担保する。
func TestSolve3MatchesLAPACK(t *testing.T) {
	v := load(t)
	var worst int64
	for i, c := range v.Lstsq3 {
		var a [3][3]float64
		var b [3]float64
		for r := 0; r < 3; r++ {
			for cc := 0; cc < 3; cc++ {
				a[r][cc] = hexf(t, c.A[r][cc])
			}
			b[r] = hexf(t, c.B[r])
		}
		beta, ok := Solve3(a, b)
		if !ok {
			t.Fatalf("試行%d: Solve3 が特異と判定", i)
		}
		for r := 0; r < 3; r++ {
			want := hexf(t, c.Beta[r])
			if d := ulpDiff(beta[r], want); d > maxLUUlp {
				t.Errorf("試行%d beta[%d] = %x, numpy = %x (ulp差 %d > 許容 %d)",
					i, r, beta[r], want, d, maxLUUlp)
			} else if d > worst {
				worst = d
			}
		}
	}
	t.Logf("LU の最大 ulp 差 %d（許容 %d）", worst, maxLUUlp)
}

// TestNormalEquations3Tolerance は行列積の総和順序の差を許容差として固定する。
//
// numpy の x.T @ x は BLAS の dgemm を呼ぶため、総和順序が CPU 派生の
// カーネル選択に依存する。素朴・FMA・pairwise・4 アキュムレータのいずれも
// 完全一致しないことを実測で確認済みなので、ビット一致は追わない。
// 代わりに、その差が最終出力に効かないことを物理量で担保する:
// 頂点時刻 center の差が 1 ns 丸めの境界（0.5 ns）を跨がないこと。
func TestNormalEquations3Tolerance(t *testing.T) {
	v := load(t)
	const centerBudgetNs = 1e-3 // 0.5 ns 境界に対して 500 倍の余裕を要求する

	var worstNs float64
	for i, c := range v.Lstsq3 {
		u, y := hexfs(t, c.U), hexfs(t, c.Y)
		a, b := NormalEquations3(u, y, nil)
		beta, ok := Solve3(a, b)
		if !ok {
			t.Fatalf("試行%d: Solve3 が特異と判定", i)
		}
		var bn [3]float64
		for r := 0; r < 3; r++ {
			bn[r] = hexf(t, c.Beta[r])
		}
		// center = t0 + round(uc * 1e6) なので uc の差をそのまま ns で見る
		ucGo, ucNp := -beta[1]/(2*beta[2]), -bn[1]/(2*bn[2])
		dNs := math.Abs(ucGo-ucNp) * 1e6
		if dNs > worstNs {
			worstNs = dNs
		}
		if dNs > centerBudgetNs {
			t.Errorf("試行%d: 頂点時刻の差 %.3e ns が許容 %.3e ns を超過", i, dNs, centerBudgetNs)
		}
	}
	t.Logf("頂点時刻の最大差 %.3e ns（1 ns 丸めの境界は 0.5 ns）", worstNs)
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
	if d := ia - ib; d < 0 {
		return -d
	} else {
		return d
	}
}

func TestFloorDivMod(t *testing.T) {
	// Python の // と % の定義に対する直接の表。負値で Go の演算子と食い違う。
	cases := []struct{ a, b, div, mod int64 }{
		{7, 3, 2, 1}, {-7, 3, -3, 2}, {7, -3, -3, -2}, {-7, -3, 2, -1},
		{-1, 4, -1, 3}, {-2, 4, -1, 2}, {0, 4, 0, 0}, {-4, 4, -1, 0},
		{-1393, 2, -697, 1}, {1393, 2, 696, 1},
	}
	for _, c := range cases {
		if got := FloorDiv(c.a, c.b); got != c.div {
			t.Errorf("FloorDiv(%d, %d) = %d, Python // = %d", c.a, c.b, got, c.div)
		}
		if got := FloorMod(c.a, c.b); got != c.mod {
			t.Errorf("FloorMod(%d, %d) = %d, Python %% = %d", c.a, c.b, got, c.mod)
		}
		if c.a < 0 && c.b > 0 && c.a%c.b != 0 && c.a%c.b == c.mod {
			t.Errorf("FloorMod(%d,%d) が Go の %% と同じになっている（テストの意味がない）", c.a, c.b)
		}
	}
}
