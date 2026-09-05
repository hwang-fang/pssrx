package numeric

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
)

// 参照ベクタ testdata/vectors.json は、このパッケージが返すべき値を
// 固定したもの。浮動小数点は 16 進表記で保存してあるので往復無損失で、
// 比較はビット単位で行う。値の出どころは tools/gen_npvectors.py を参照。

type vectors struct {
	PairwiseSum []struct {
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

// maxLUUlp は Solve3 が参照値からずれてよい上限。実測は 2 ulp。
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

func TestSumUsesPairwiseSummation(t *testing.T) {
	v := load(t)
	for _, c := range v.PairwiseSum {
		a := hexfs(t, c.A)
		if got, want := Sum(a), hexf(t, c.SumA); !bitEq(got, want) {
			t.Errorf("n=%d Sum(a) = %x, 参照値 = %x (差 %g)", c.N, got, want, got-want)
		}
		b := make([]float64, len(a))
		for i, x := range a {
			b[i] = x * x
		}
		if got, want := Sum(b), hexf(t, c.SumB); !bitEq(got, want) {
			t.Errorf("n=%d Sum(a*a) = %x, 参照値 = %x (差 %g)", c.N, got, want, got-want)
		}
	}
}

func TestMeanInt64(t *testing.T) {
	v := load(t)
	for _, c := range v.MeanInt64 {
		if got, want := MeanInt64(c.V), hexf(t, c.Mean); !bitEq(got, want) {
			t.Errorf("n=%d MeanInt64 = %x, 参照値 = %x", len(c.V), got, want)
		}
	}
}

func TestMedian(t *testing.T) {
	v := load(t)
	for _, c := range v.Median {
		if got, want := Median(hexfs(t, c.A)), hexf(t, c.Median); !bitEq(got, want) {
			t.Errorf("n=%d Median = %x, 参照値 = %x", len(c.A), got, want)
		}
	}
}

func TestRoundHalfEven(t *testing.T) {
	v := load(t)
	for _, c := range v.RoundHalfEven {
		x := hexf(t, c.X)
		if got := RoundToInt64(x); got != c.PyRound {
			t.Errorf("RoundToInt64(%v) = %d, 参照値 = %d", x, got, c.PyRound)
		}
		if got, want := RoundHalfEven(x), hexf(t, c.NpRint); !bitEq(got, want) {
			t.Errorf("RoundHalfEven(%v) = %v, 参照値 = %v", x, got, want)
		}
		// math.Round との差が実際に出ることを確認しておく（この差が移植バグの温床）
		if r := math.Round(x); r != RoundHalfEven(x) && math.Abs(x-math.Trunc(x)) == 0.5 {
			t.Logf("参考: math.Round(%v)=%v は偶数丸め %v と異なる", x, r, RoundHalfEven(x))
		}
	}
}

func TestModTakesSignOfDivisor(t *testing.T) {
	v := load(t)
	for _, c := range v.NpModTwoPi {
		x, y := hexf(t, c.X), hexf(t, c.Y)
		if got, want := Mod(x, y), hexf(t, c.Mod); !bitEq(got, want) {
			t.Errorf("Mod(%v, 2pi) = %v, 参照値 = %v", x, got, want)
		}
	}
}

func TestTruncToUint32(t *testing.T) {
	v := load(t)
	for _, c := range v.F64ToU32 {
		x := hexf(t, c.X)
		if got := TruncToUint32(x); got != c.U32 {
			t.Errorf("TruncToUint32(%v) = %d, 参照値 = %d", x, got, c.U32)
		}
	}
}

// TestBoundsOnFloat64View はタイムスタンプを float64 に落としてから
// 二分探索したときの位置を固定する。
//
// 連鎖検出の探索窓は int64 のタイムスタンプではなく float64 に変換した
// 値の上で決まる。Unix ナノ秒（約 1.78e18）では float64 の刻みが 256 ns
// あるため、窓の端はこの粒度に丸まる。窓の広さは gate = 20000 ns なので
// 実害は無いが、境界に乗った観測が窓に入るかどうかはこの丸めで決まる。
func TestBoundsOnFloat64View(t *testing.T) {
	v := load(t)
	tf := make([]float64, len(v.SearchSorted.T))
	for i, x := range v.SearchSorted.T {
		tf[i] = float64(x)
	}
	for _, p := range v.SearchSorted.Probes {
		val := hexf(t, p.V)
		if got := LowerBound(tf, val); got != p.Left {
			t.Errorf("LowerBound(%v) = %d, 参照値 = %d", val, got, p.Left)
		}
		if got := UpperBound(tf, val); got != p.Right {
			t.Errorf("UpperBound(%v) = %d, 参照値 = %d", val, got, p.Right)
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
			t.Errorf("ti=%d exp=%v: lo = %x, 参照値 = %x", c.Ti, exp, lo, want)
		}
		if want := hexf(t, c.TiPlusExpPlusGate); !bitEq(hi, want) {
			t.Errorf("ti=%d exp=%v: hi = %x, 参照値 = %x", c.Ti, exp, hi, want)
		}
	}
}

// TestSolve3 は LU 分解そのものを切り分けて検証する。参照ベクタの A と b を
// そのまま与えることで、正規方程式の組み立て（次のテストが扱う）で生じる
// 誤差を混入させずに済む。
//
// 消去と代入の順序を変えると最下位ビットが動くため、順序の候補 16 通り
// （逆数乗算か除算か・列方向か行方向か・FMA の有無・ピボット同値時の
// 先勝ちか後勝ちか）を実測して最良のものを Solve3 に採ってある。それでも
// 36 個中 6 個は 1〜2 ulp 残る。参照値の側が CPU の派生機能でカーネルを
// 切り替える実装なので、完全一致を追うと別マシンで壊れる。ここは ulp 上限で
// 固定し、実害が無いことは TestNormalEquations3Tolerance が物理量で担保する。
func TestSolve3(t *testing.T) {
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
				t.Errorf("試行%d beta[%d] = %x, 参照値 = %x (ulp差 %d > 許容 %d)",
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
// 参照値の側は行列積のカーネルを CPU の派生機能で切り替えるため、総和順序を
// 再現できない。素朴・FMA・pairwise・4 アキュムレータのいずれも完全一致
// しないことを実測済みなので、ビット一致は追わない。代わりに、その差が
// 最終出力に効かないことを物理量で担保する: 頂点時刻の差が 1 ns 丸めの
// 境界（0.5 ns）を跨がないこと。
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
	// 床除算・床剰余の定義そのものの表。負値で Go の / と % に食い違う。
	cases := []struct{ a, b, div, mod int64 }{
		{7, 3, 2, 1}, {-7, 3, -3, 2}, {7, -3, -3, -2}, {-7, -3, 2, -1},
		{-1, 4, -1, 3}, {-2, 4, -1, 2}, {0, 4, 0, 0}, {-4, 4, -1, 0},
		{-1393, 2, -697, 1}, {1393, 2, 696, 1},
	}
	for _, c := range cases {
		if got := FloorDiv(c.a, c.b); got != c.div {
			t.Errorf("FloorDiv(%d, %d) = %d, 期待 %d", c.a, c.b, got, c.div)
		}
		if got := FloorMod(c.a, c.b); got != c.mod {
			t.Errorf("FloorMod(%d, %d) = %d, 期待 %d", c.a, c.b, got, c.mod)
		}
		if c.a < 0 && c.b > 0 && c.a%c.b != 0 && c.a%c.b == c.mod {
			t.Errorf("FloorMod(%d,%d) が Go の %% と同じ値になっている（表が退化していて検証にならない）", c.a, c.b)
		}
	}
}
