package strategy

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

// activeSession145 は外部接続のない長い人工セッションを作ります。
//
// 引数: kind(string)。返り値: Session。
func activeSession145(kind string) Session {
	return Session{ID: "人工_" + kind, Kind: kind, Start: 0, ForceClose: 20000, End: 20600, USOpen: 10000}
}

// TestD07ExitUnitAndNextRow はUを判断時点で固定し、同一原行・不正板で約定しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ExitUnitAndNextRow(t *testing.T) {
	for _, v := range []struct{ risk, atr, want float64 }{{200, 20, 80}, {200, 100, 150}, {100, 200, 100}, {200, math.NaN(), 200}} {
		if got := exitUnit145(v.risk, v.atr, 10); got != v.want {
			t.Fatalf("U=%v、期待%v", got, v.want)
		}
	}
	s := newResearchActive145(activeSession145("Day"), 3)
	o := researchTestObservation(1, 1, 1000)
	o.ATR = 100
	s.queueInitial(o, 1, 2, 144, 200, 1000, 10)
	s.filled145(o, false)
	if s.Book.total() != 0 {
		t.Fatal("判断原行で約定しました")
	}
	o.Index = 2
	o.ATR = 1000
	o.QuoteOK = false
	s.filled145(o, false)
	if s.Book.total() != 0 {
		t.Fatal("不正板で約定しました")
	}
	o.Index = 3
	o.QuoteOK = true
	s.filled145(o, false)
	if s.Book.total() != 2 || s.ExitRisk != 150 || s.Book.Floor != -200 {
		t.Fatal("入口R又はUが変化しました", s)
	}
}

// TestD07PartialRotationUsesActualCloseAndUniqueTickets は部分利確約定後だけ回転を準備し、同じ玉番号を再利用しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07PartialRotationUsesActualCloseAndUniqueTickets(t *testing.T) {
	session := activeSession145("Day")
	s := newResearchActive145(session, 3)
	s.Book.Parent = 0
	s.Book.Side = 1
	s.Book.Risk = 200
	s.Book.Base = 1000
	s.Book.Floor = -200
	s.Book.Cash = -200000
	s.Book.Held[0] = 2
	s.Book.Entries[0] = 1000
	s.ExitRisk = 80
	s.ActualInitialRisk = 400
	o := researchTestObservation(1, 1, 1080)
	e := s.step(o, session, 10, false)
	if len(e) != 1 || e[0].Reason != 14510 || e[0].Quantity != 1 || s.Rotation {
		t.Fatal("部分利確前に回転しました", e)
	}
	o = researchTestObservation(2, 2, 1090)
	s.step(o, session, 10, false)
	if !s.Rotation || s.RotationAnchor != 1090 || s.Book.total() != 1 {
		t.Fatal("利確約定からの準備ではありません", s)
	}
	o = researchTestObservation(3, 3, 1020)
	s.step(o, session, 10, false)
	o = researchTestObservation(4, 4, 1040)
	o.Research.Known[12] = true
	o.Research.Known[13] = true
	o.Research.Values[12] = 1
	o.Research.Values[13] = 1
	if got := s.step(o, session, 10, false); len(got) != 0 {
		t.Fatal("15秒確認前に回転しました")
	}
	o.Index = 5
	o.Time = 19
	e = s.step(o, session, 10, false)
	if len(e) != 1 || e[0].Reason != 14511 || e[0].Ticket != 1 {
		t.Fatal("回転新規が不正です", e)
	}
	o.Index = 6
	o.Time = 20
	s.step(o, session, 10, false)
	if s.Book.total() != 2 || s.LotUID[1] != 1 || s.Book.Entries[1] != 1050 {
		t.Fatal("回転玉約定が不正です")
	}
	o = researchTestObservation(7, 21, 1130)
	e = s.step(o, session, 10, false)
	if len(e) != 1 || e[0].Reason != 14510 || e[0].Ticket != 1 {
		t.Fatal("回転玉自身の1Uを利確しません", e)
	}
	o.Index = 8
	o.Time = 22
	s.step(o, session, 10, false)
	if !s.Rotation || s.Book.Held[1] != 0 {
		t.Fatal("利確後の再武装がありません")
	}
	// 物理的な6枠を超えるUIDでも、空き枠へ約定することを明示的に確認します。
	s.Book.UID = 9
	s.PendingSlot = 1
	s.Book.Pending = []ResearchEvent{{Action: "new", Side: 1, Quantity: 1, Campaign: 0, Ticket: 10, Reason: 14511, Risk: 200}}
	s.Book.PendingIndex = 8
	s.Book.PendingKind = 6
	o.Index = 9
	o.Time = 23
	s.filled145(o, false)
	if s.LotUID[1] != 10 || s.Book.Held[1] != 1 || s.Book.UID != 10 {
		t.Fatal("物理枠と一意玉番号が混線しました")
	}
}

// flipObservation145 は通常の帯端入口を起こさず、確認ドテンの資料だけを揃えます。
//
// 引数: index(int64)、at/forty(float64)。返り値: Observation。
func flipObservation145(index int64, at, forty float64) Observation {
	o := researchTestObservation(index, at, 990)
	for c := 0; c < 24; c++ {
		o.Research.Known[c] = true
	}
	o.Research.Values[0] = 1200
	o.Research.Values[1] = 800
	o.Research.Values[2] = .25
	o.Research.Values[3] = -20
	o.Research.Values[4] = forty
	o.Research.Values[9] = 100
	o.Research.Values[10] = 995
	o.Research.Values[12] = -1
	o.Research.Values[13] = -1
	o.Research.MA20Known = true
	o.Research.MA20 = 1050
	return o
}

// TestD07FlipRequiresFortyAndContinuousConfirmation は40分支持と15秒の継続確認をドテンだけに要求します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07FlipRequiresFortyAndContinuousConfirmation(t *testing.T) {
	session := activeSession145("Night")
	s := newResearchActive145(session, 3)
	s.ClosedContext = true
	s.LastClosedSide = 1
	s.ClosedUnit = 80
	s.ClosedPrice = 1100
	s.ReentryExtreme = 1100
	for i, at := range []float64{1, 20} {
		if v := s.step(flipObservation145(int64(i+1), at, 20), session, 10, false); len(v) > 0 {
			t.Fatal("40分が逆なのにドテンしました", v)
		}
	}
	o := flipObservation145(3, 21, -20)
	if v := s.step(o, session, 10, false); len(v) > 0 {
		t.Fatal("確認を省略しました")
	}
	o.Index = 4
	o.Time = 36
	v := s.step(o, session, 10, false)
	if len(v) != 1 || v[0].Reason != 14513 || v[0].Side != -1 {
		t.Fatal("確認ドテンが不正です", v)
	}
	// 約定待ち状態をJSON保存しても次の判断に未来情報は補いません。
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored ResearchActive145
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	o.Index = 5
	o.Time = 37
	if !reflect.DeepEqual(s.step(o, session, 10, false), restored.step(o, session, 10, false)) || !reflect.DeepEqual(s, &restored) {
		t.Fatal("保存復元で状態が変わりました")
	}
}

// TestD07ProfitCooldownAndLosingCooldown は利益60秒と損切り120秒・連敗900秒の区別を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ProfitCooldownAndLosingCooldown(t *testing.T) {
	for _, v := range []struct {
		bid  float64
		lost int
		wait float64
	}{{1100, 0, 60}, {900, 0, 120}, {900, 1, 900}} {
		s := newResearchActive145(activeSession145("Night"), 3)
		s.Book.Parent = 0
		s.Book.Side = 1
		s.Book.Base = 1000
		s.Book.Risk = 200
		s.Book.Floor = -200
		s.Book.Held[0] = 1
		s.Book.Entries[0] = 1000
		s.Book.Cash = -100000
		s.ExitRisk = 80
		s.Lost = v.lost
		o := researchTestObservation(1, 10, v.bid)
		s.closeAll145(o, 70)
		if s.ClosedContext {
			t.Fatal("全返済の判断だけで再参加を開始しました")
		}
		o.Index = 2
		o.Time = 11
		s.filled145(o, true)
		if s.NextReady != 11+v.wait || !s.ClosedContext {
			t.Fatal("待機時計が不正です", s.NextReady, v)
		}
	}
}

// TestD07EpisodeFeeFreeThreshold は別入口の保護床と追撃条件も費用0で再現することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。TG04の往復費用0.77ptは維持します。
func TestD07EpisodeFeeFreeThreshold(t *testing.T) {
	for _, free := range []bool{false, true} {
		s := &ResearchEpisode{Book: ResearchBook{Side: 1, Parent: 0, Base: 1000, Risk: 80, Floor: -80, FeeFree: free}, Initial: 2, Maximum: 2}
		s.Book.Held[0] = 2
		s.Book.Entries[0] = 1000
		o := researchTestObservation(1, 1, 1030)
		s.Book.queueClose(o, 4, 1, 1435, true)
		o.Index = 2
		o.Time = 2
		s.step(o, activeSession145("Day"), 10, false)
		want := 10.77
		if free {
			want = 10
		}
		if s.Book.Floor != want {
			t.Fatal("費用による保護床が不正です", free, s.Book.Floor, want)
		}
	}
}
