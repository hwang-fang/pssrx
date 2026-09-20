package pssr

import (
	"slices"
	"sort"

	"pssrx/internal/record"
)

// Synchronizer は質問予定と応答の 2 つの流れを時刻で同期する。それぞれを
// 時刻順に溜め、対応づけが閉じた形で処理できる範囲を切り出す。投入の刻み
// （1 分でも 0.1 秒でも）と対応づけの手続きを切り離すためのもので、
// 対応づけそのものは Pair が行う。
//
// 切り出しの規則は「取り出した範囲の外にあるデータが、取り出した範囲の
// データと対になることはない」を保証する。
//
//	質問 q への応答は t_q + TauMin ≤ t_r ≤ t_q + TauMax にある。応答の最新が
//	t_q + TauMax を超えていれば q の応答は出揃っているので、そこまでの質問を
//	取り出し、最後に取り出した質問 q_k について t_r ≤ t_q(k) + TauMax の応答を
//	一緒に取り出す。残る応答はどの取り出し済み質問からも TauMax 以上離れて
//	いるので将来の質問にしか属せず、取り出した応答が未知の次の質問 q_{k+1} に
//	属することも無い（TauMax − TauMin < 最短 PRI ≤ t_q(k+1) − t_q(k)。
//	pipeline.PSSRParams が保証する）。
//
// 過去には戻れない。取り出し済みの時刻より前のデータが後から投入されても
// 捨てる。片側が止まって他方が溜まり続けないよう、取り出しの後になお
// 残っている、保持幅の上限を超えた古いデータも捨てる（投入時ではなく
// 取り出しの後に捨てるのは、投入の刻みが粗いとき、次の投入で対になる
// はずのデータを先に捨てないため）。どちらも件数に数える。
type Synchronizer struct {
	params Params
	cfg    Config

	intg    []record.Interrogation // 時刻順
	replies []record.Reply         // 時刻順

	// 取り出し済みの末尾。これ以前のデータは受け付けない
	intgReleased  int64
	replyReleased int64
	hasReleased   bool
}

// NewSynchronizer は空の Synchronizer を作る。
func NewSynchronizer(params Params, cfg Config) *Synchronizer {
	return &Synchronizer{params: params, cfg: cfg}
}

// PushInterrogations は確定した質問予定を投入する。時刻順でなければならない。
func (m *Synchronizer) PushInterrogations(stats *Stats, intg []record.Interrogation) {
	for _, d := range intg {
		if (m.hasReleased && d.Timestamp <= m.intgReleased) ||
			(len(m.intg) > 0 && d.Timestamp < m.intg[len(m.intg)-1].Timestamp) {
			stats.DroppedIntg++
			continue
		}
		m.intg = append(m.intg, d)
	}
}

// PushReplies は応答を投入する。順不同でよい。
func (m *Synchronizer) PushReplies(stats *Stats, replies []record.Reply) {
	stats.Replies += len(replies)
	for _, r := range replies {
		if m.hasReleased && r.Timestamp <= m.replyReleased {
			stats.DroppedReplies++
			continue
		}
		m.replies = append(m.replies, r)
	}
	slices.SortStableFunc(m.replies, func(a, b record.Reply) int {
		return compareInt64(a.Timestamp, b.Timestamp)
	})
}

// Extract は対応づけが閉じた形で処理できる範囲を切り出して返し、内部から
// 削除する。範囲が無ければ空を返す。last が真なら残りをすべて返す。
// 取り出しの後、保持幅の上限を超えて残っている古いデータは捨てる。
func (m *Synchronizer) Extract(stats *Stats, last bool) (intg []record.Interrogation, replies []record.Reply) {
	if last {
		intg, replies = m.intg, m.replies
		m.intg, m.replies = nil, nil
	} else {
		defer m.enforceRetention(stats)
		if len(m.intg) == 0 || len(m.replies) == 0 {
			return nil, nil
		}
		// 応答が t_q + TauMax まで揃っている質問だけ
		bound := m.replies[len(m.replies)-1].Timestamp - m.params.TauMaxNs
		k := sort.Search(len(m.intg), func(i int) bool { return m.intg[i].Timestamp > bound })
		if k == 0 {
			return nil, nil
		}
		// 最後に取り出す質問の応答が届きうる時刻まで
		until := m.intg[k-1].Timestamp + m.params.TauMaxNs
		j := sort.Search(len(m.replies), func(i int) bool { return m.replies[i].Timestamp > until })
		intg, replies = slices.Clone(m.intg[:k]), slices.Clone(m.replies[:j])
		m.intg = slices.Delete(m.intg, 0, k)
		m.replies = slices.Delete(m.replies, 0, j)
	}
	if len(intg) > 0 {
		m.intgReleased = max(m.intgReleased, intg[len(intg)-1].Timestamp)
		m.hasReleased = true
	}
	if len(replies) > 0 {
		m.replyReleased = max(m.replyReleased, replies[len(replies)-1].Timestamp)
		m.hasReleased = true
	}
	return intg, replies
}

// enforceRetention は保持幅の上限を超えた古いデータを捨てる。
// 片側の投入が止まったときに他方が溜まり続けないための安全弁。
func (m *Synchronizer) enforceRetention(stats *Stats) {
	var latest int64
	if n := len(m.intg); n > 0 {
		latest = max(latest, m.intg[n-1].Timestamp)
	}
	if n := len(m.replies); n > 0 {
		latest = max(latest, m.replies[n-1].Timestamp)
	}
	cut := latest - m.cfg.MaxRetentionNs
	if k := sort.Search(len(m.intg), func(i int) bool { return m.intg[i].Timestamp >= cut }); k > 0 {
		stats.DroppedIntg += k
		m.intg = slices.Delete(m.intg, 0, k)
	}
	if k := sort.Search(len(m.replies), func(i int) bool { return m.replies[i].Timestamp >= cut }); k > 0 {
		stats.DroppedReplies += k
		m.replies = slices.Delete(m.replies, 0, k)
	}
}
