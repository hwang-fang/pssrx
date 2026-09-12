package pssr

// 応答符号のビット配置。apkx の 12 ビット符号は MSB から
//
//	D1 D2 D4 A1 A2 A4 B1 B2 B4 C1 C2 C4
//
// の順に詰まっている。仕様書が無いため実データから決めた。Mode C 応答で
// 常に 0 のビット（D1 と、62,700 ft 以上でしか立たない D2）が bit 11, 10 に
// あること、Gillham 復号が 99.7% の応答で妥当な高度になり、列の中の高度が
// 99.6% で連続すること（次点の配置は 92%）から一意に決まる。
const (
	bitD1 = 11
	bitD2 = 10
	bitD4 = 9
	bitA1 = 8
	bitA2 = 7
	bitA4 = 6
	bitB1 = 5
	bitB2 = 4
	bitB4 = 3
	bitC1 = 2
	bitC2 = 1
	bitC4 = 0
)

func bit(code uint16, k int) uint16 { return (code >> k) & 1 }

// Squawk は Mode A 応答符号を 4 桁 8 進のスコーク（ABCD の順）に直す。
// 戻り値は 8 進で読む値そのもの（0o0000〜0o7777）。
func Squawk(code uint16) uint16 {
	digit := func(b1, b2, b4 int) uint16 {
		return bit(code, b1) | bit(code, b2)<<1 | bit(code, b4)<<2
	}
	return digit(bitA1, bitA2, bitA4)<<9 |
		digit(bitB1, bitB2, bitB4)<<6 |
		digit(bitC1, bitC2, bitC4)<<3 |
		digit(bitD1, bitD2, bitD4)
}

// Altitude は Mode C 応答符号を Gillham 符号として復号し、気圧高度 [ft] を返す。
//
// 500 ft 刻みは D2 D4 A1 A2 A4 B1 B2 B4 のグレイ符号、100 ft 刻みは
// C1 C2 C4 のグレイ符号で、500 ft の段が奇数なら向きが反転する。
// D1 が立っている、C の値が 0, 5, 6 のいずれか（定義されない組み合わせ）
// なら復号できない。
func Altitude(code uint16) (ft int, ok bool) {
	if bit(code, bitD1) != 0 {
		return 0, false
	}
	gray500 := bit(code, bitD2)<<7 | bit(code, bitD4)<<6 |
		bit(code, bitA1)<<5 | bit(code, bitA2)<<4 | bit(code, bitA4)<<3 |
		bit(code, bitB1)<<2 | bit(code, bitB2)<<1 | bit(code, bitB4)
	n500 := int(grayToBinary(gray500))

	gray100 := bit(code, bitC1)<<2 | bit(code, bitC2)<<1 | bit(code, bitC4)
	n100 := int(grayToBinary(gray100))
	switch n100 {
	case 0, 5, 6:
		return 0, false
	case 7:
		n100 = 5
	}
	if n500%2 == 1 {
		n100 = 6 - n100
	}
	return n500*500 + n100*100 - 1300, true
}

func grayToBinary(g uint16) uint16 {
	var b uint16
	for k := 15; k >= 0; k-- {
		b |= ((b >> (k + 1)) ^ (g >> k)) & 1 << k
	}
	return b
}
