package config

// Analysis は設定ファイルの analysis 節。解析の定数（interrogator.Config /
// pssr.Config）のうち、既定値から変えたい項目だけを書く。
//
// 段の Config と同じ名前の項目をポインタで持つ。書かれたかどうかは nil で
// 見分ける（0 も正当な値。max_gap: 0 は「途切れを許さない」の指定）。
// キーは Go のフィールド名の snake_case で、単位を名前に含める。
// 既定値への重ね合わせと検証は pipeline が行い、鏡像が段の Config を
// 漏れなく写していることは pipeline のテストが反射で確かめる。
//
// 段の Config に直接 yaml タグを付けない理由は 2 つ。段が設定の書式を
// 知らずに済むことと、Go のフィールド名を変えてもファイルの形式が
// 変わらないこと。
type Analysis struct {
	Interrogator InterrogatorAnalysis `yaml:"interrogator"`
	PSSR         PSSRAnalysis         `yaml:"pssr"`
}

// InterrogatorAnalysis は interrogator.Config の鏡像。項目の意味はそちらを参照。
type InterrogatorAnalysis struct {
	AmplitudeGateDbm      *float64 `yaml:"amplitude_gate_dbm"`
	GateNs                *int64   `yaml:"gate_ns"`
	MaxSkip               *int64   `yaml:"max_skip"`
	MinChainLength        *int     `yaml:"min_chain_length"`
	SkipPenalty           *float64 `yaml:"skip_penalty"`
	ResidualWeight        *float64 `yaml:"residual_weight"`
	DwellGapPeriods       *float64 `yaml:"dwell_gap_periods"`
	ParabolaMinSamples    *int     `yaml:"parabola_min_samples"`
	RobustIters           *int     `yaml:"robust_iters"`
	ParabolaOutlierK      *float64 `yaml:"parabola_outlier_k"`
	ParabolaSigmaFloorDb  *float64 `yaml:"parabola_sigma_floor_db"`
	VertexMarginFrac      *float64 `yaml:"vertex_margin_frac"`
	ParabolaMaxResidualDb *float64 `yaml:"parabola_max_residual_db"`
	MinPeakDropDb         *float64 `yaml:"min_peak_drop_db"`
	MaxBridgeRotations    *int     `yaml:"max_bridge_rotations"`
}

// PSSRAnalysis は pssr.Config の鏡像。項目の意味はそちらを参照。
type PSSRAnalysis struct {
	TauToleranceNs              *int64   `yaml:"tau_tolerance_ns"`
	MaxGap                      *int     `yaml:"max_gap"`
	MinReplies                  *int     `yaml:"min_replies"`
	MaxAltitudeFt               *int     `yaml:"max_altitude_ft"`
	MaxRetentionNs              *int64   `yaml:"max_retention_ns"`
	SameScanFraction            *float64 `yaml:"same_scan_fraction"`
	AltitudeToleranceFt         *int     `yaml:"altitude_tolerance_ft"`
	DirectTauToleranceNs        *int64   `yaml:"direct_tau_tolerance_ns"`
	ResolveAzimuthSeparationRad *float64 `yaml:"resolve_azimuth_separation_rad"`
	ZMarginM                    *float64 `yaml:"z_margin_m"`
	BaselineMarginM             *float64 `yaml:"baseline_margin_m"`
	CurvatureTolM               *float64 `yaml:"curvature_tol_m"`
	CurvatureMaxIter            *int     `yaml:"curvature_max_iter"`
	SigmaTimingNs               *float64 `yaml:"sigma_timing_ns"`
	SigmaTransponderNs          *float64 `yaml:"sigma_transponder_ns"`
	SigmaAzimuthRad             *float64 `yaml:"sigma_azimuth_rad"`
	DwellFullReplies            *int     `yaml:"dwell_full_replies"`
	AzimuthFragmentFactor       *float64 `yaml:"azimuth_fragment_factor"`
	SigmaAltitudeM              *float64 `yaml:"sigma_altitude_m"`
	TrackMaxSpeedMps            *float64 `yaml:"track_max_speed_mps"`
	TrackMaxClimbFtps           *float64 `yaml:"track_max_climb_ftps"`
	TrackGateSigmas             *float64 `yaml:"track_gate_sigmas"`
	TrackMaxMissedScans         *int     `yaml:"track_max_missed_scans"`
	TrackConfirmHits            *int     `yaml:"track_confirm_hits"`
	LinkMaxGapNs                *int64   `yaml:"link_max_gap_ns"`
	LinkVelocityToleranceMps    *float64 `yaml:"link_velocity_tolerance_mps"`
	LinkClimbToleranceFtps      *float64 `yaml:"link_climb_tolerance_ftps"`
}
