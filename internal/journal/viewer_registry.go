package journal

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

type viewerSession struct {
	Session, Product, End, ContentHash, ResourceIDs string
	Dry, Emitted                                    bool
	PointValue                                      float64
	LastTrade, LastEvent                            int64
}

const viewerCandidateSelect = `SELECT v.session,v.dryrun,v.product,v.end_at,v.point_value,v.emitted,v.last_trade_seq,v.last_event_seq,v.content_sha256,v.resource_ids FROM viewer_sessions v`
const viewerCandidateChange = `(v.emitted=0 OR EXISTS
(SELECT 1 FROM trades t WHERE t.session=v.session AND t.dryrun=v.dryrun AND t.seq>v.last_trade_seq)
OR EXISTS (SELECT 1 FROM events e WHERE e.kind IN ('保有玉の管理外移管','起動時状態切替') AND e.seq>v.last_event_seq))`

// viewerFinitePositive は円倍率等が有限な正数か確認します。
//
// 引数: value(float64)。返り値: bool。
func viewerFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// viewerReference は発注商品の既知系列だけを判断用ラージへ対応させます。
//
// 引数: product(string)。返り値: string。不明商品は空文字です。
func viewerReference(product string) string {
	switch product {
	case "NK225", "NK225mini", "NK225micro":
		return "NK225"
	case "TOPIX", "TOPIXmini":
		return "TOPIX"
	}
	return ""
}

// registerViewerSession は導入後に明示した場だけを表示用出力へ登録します。
//
// 引数: db(*sql.DB)、session/product(string)、dry(bool)、end(time.Time)、point(float64)。返り値: error。
func registerViewerSession(db *sql.DB, session string, dry bool, product string, end time.Time, point float64) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO viewer_sessions(session,dryrun,product,end_at,point_value) VALUES(?,?,?,?,?)`, session, dry, product, end.UTC().Format(time.RFC3339Nano), point)
	if err != nil {
		return err
	}
	var oldEnd string
	var oldPoint float64
	if err = db.QueryRow(`SELECT end_at,point_value FROM viewer_sessions WHERE session=? AND dryrun=? AND product=?`, session, dry, product).Scan(&oldEnd, &oldPoint); err != nil {
		return err
	}
	prior, err := time.Parse(time.RFC3339Nano, oldEnd)
	if err != nil || !prior.Equal(end) || oldPoint != point {
		return fmt.Errorf("登録済みViewerの終了時計・円倍率は変更できません")
	}
	return nil
}

// viewerCandidates は未出力又は確定約定・管理外移管が増えた場だけを読み取ります。
//
// 引数: q(queryer)。返り値: []viewerSessionとerror。毎tickのsnapshot更新は候補にしません。
func viewerCandidates(q queryer) ([]viewerSession, error) {
	rows, err := q.Query(viewerCandidateSelect + ` WHERE ` + viewerCandidateChange + ` ORDER BY v.session,v.product,v.dryrun`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []viewerSession{}
	for rows.Next() {
		var v viewerSession
		if err = rows.Scan(&v.Session, &v.Dry, &v.Product, &v.End, &v.PointValue, &v.Emitted, &v.LastTrade, &v.LastEvent, &v.ContentHash, &v.ResourceIDs); err != nil {
			return nil, err
		}
		if !sessionPattern.MatchString(v.Session) || viewerReference(v.Product) == "" || !viewerFinitePositive(v.PointValue) {
			return nil, fmt.Errorf("保存済みViewer登録が不正です")
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
