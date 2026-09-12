// Command stationgeometry は設定から SSR・測定局の距離 [m] と方位 [rad] を
// 出力する。ゴールデン生成で Python 参照実装に渡すためのもの。
//
// Python 側には緯度経度から ENU への変換が無いので、Go が出した距離と
// 方位をそのまま渡す。出力は shortest repr なので Python の float() で
// ビット単位に復元できる。
package main

import (
	"flag"
	"fmt"
	"os"

	"pssrx/internal/config"
	"pssrx/internal/geodesy/geoid"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "SSR・測定局のマスタ YAML (必須)")
	stationID := flag.String("station", "", "測定局 ID (必須)")
	ssrID := flag.String("ssr", "", "SSR ID (必須)")
	flag.Parse()
	if *cfgPath == "" || *stationID == "" || *ssrID == "" {
		flag.Usage()
		return fmt.Errorf("-config, -station, -ssr は必須です")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	station, err := cfg.Station(*stationID)
	if err != nil {
		return err
	}
	ssr, err := cfg.SSR(*ssrID)
	if err != nil {
		return err
	}
	gm, err := geoid.Load()
	if err != nil {
		return err
	}
	dist, azimuth, err := config.Geometry(ssr, station, gm)
	if err != nil {
		return err
	}
	fmt.Printf("%v %v\n", dist, azimuth)
	return nil
}
