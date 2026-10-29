package strategy

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// researchRules143 は指定手法の式を人工値試験用に読み込みます。
//
// 引数: t(*testing.T)、profile(string)、changed(map[string]string)。返り値: *rules.Runtimeです。
func researchRules143(t *testing.T, profile string, changed map[string]string) *rules.Runtime {
	t.Helper()
	base, err := rules.DefaultFor(profile)
	if err != nil {
		t.Fatal(err)
	}
	expressions := base.Expressions()
	for id, code := range changed {
		expressions[id] = code
	}
	runtime, err := rules.NewRuntimeFor(profile, expressions)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// TestResearchRules143ActiveParity は人工価格で外部式と従来式の準備・約定・管理が一致することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。保存された市場データを再生しません。
func TestResearchRules143ActiveParity(t *testing.T) {
	for _, profile := range []string{"tg04", "pg04"} {
		for _, guarded := range []bool{false, true} {
			legacy := ResearchActive{Book: ResearchBook{Parent: -1}, Initial: 2, Maximum: 2, Guard: guarded}
			external := legacy
			external.ResearchRules = true
			external.Rules = researchRules143(t, profile, nil)
			points := []struct{ at, mid float64 }{{1, 90}, {2, 93}, {3, 92}, {4, 94}, {34, 94}, {35, 95}, {36, 110}, {37, 102}, {38, 102}}
			session := Session{Kind: "Day", Start: 0, ForceClose: 1000}
			for i, point := range points {
				o := Observation{Index: int64(i), Time: point.at, Bid: point.mid - .5, Ask: point.mid + .5, QuoteOK: true}
				for k := 0; k < 24; k++ {
					o.Research.Known[k] = true
				}
				a := &o.Research.Values
				a[0], a[1], a[2], a[9], a[10] = 110, 90, .1, 8, point.mid
				a[12], a[13], a[20], a[21] = .1, .2, .1, .2
				left := legacy.step(o, session, 1, false)
				right := external.step(o, session, 1, false)
				if !reflect.DeepEqual(left, right) || external.Rules.Err() != nil {
					t.Fatalf("%s guard=%t 行%d: %v/%v %v", profile, guarded, i, left, right, external.Rules.Err())
				}
				copyState := external
				copyState.ResearchRules = false
				one, _ := json.Marshal(legacy)
				two, _ := json.Marshal(copyState)
				if string(one) != string(two) {
					t.Fatalf("%s 行%dの状態が相違\n%s\n%s", profile, i, one, two)
				}
			}
		}
	}
}

// TestResearchRules143EpisodeParity は人工の帯外抜け・回復・利確・停止で日中別入口の同値性を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchRules143EpisodeParity(t *testing.T) {
	for _, profile := range []string{"tg04", "pg04"} {
		legacy := ResearchEpisode{Book: ResearchBook{Parent: -1}, Initial: 2, Maximum: 2}
		external := legacy
		external.ResearchRules = true
		external.Rules = researchRules143(t, profile, nil)
		points := []struct{ at, mid float64 }{{1, 980}, {2, 1020}, {17, 1020}, {18, 1030}, {19, 1105}, {20, 1110}, {21, 1130}, {22, 1030}, {23, 1030}}
		session := Session{Kind: "Day", Start: 0, ForceClose: 1000}
		events := 0
		for i, point := range points {
			o := researchTestObservation(int64(i), point.at, point.mid-5)
			o.Research.Band20.Known = true
			o.Research.Band20.Values[0], o.Research.Band20.Values[1] = 1200, 1000
			left := legacy.step(o, session, 10, false)
			right := external.step(o, session, 10, false)
			events += len(left)
			if !reflect.DeepEqual(left, right) || external.Rules.Err() != nil {
				t.Fatalf("%s 行%d: %v/%v %v", profile, i, left, right, external.Rules.Err())
			}
			copyState := external
			copyState.ResearchRules = false
			one, _ := json.Marshal(legacy)
			two, _ := json.Marshal(copyState)
			if string(one) != string(two) {
				t.Fatalf("%s 行%dの状態が相違\n%s\n%s", profile, i, one, two)
			}
		}
		if events != 3 || legacy.Book.total() != 0 {
			t.Fatal("新規・部分返済・全返済の人工ケースを通過していません", profile, events, legacy.Book.total())
		}
	}
}

// TestResearchRules143ProfileIsolation は変更したTG04式がPG04又はD07へ混入しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchRules143ProfileIsolation(t *testing.T) {
	changed := researchRules143(t, "tg04", map[string]string{"research.active.trail_width": "20.0", "research.entry.confirmed": "elapsed >= 60", "research.episode.trail_width": "25.0", "research.core.day_gate": "false", "research.residual.width": "50.0"})
	base := researchRules143(t, "pg04", nil)
	tg := ResearchActive{Rules: changed, ResearchRules: true}
	pg := ResearchActive{Rules: base, ResearchRules: true}
	if tg.activeNumber("trail_width", 75, map[string]any{"risk": float64(100)}) != 20 || pg.activeNumber("trail_width", 75, map[string]any{"risk": float64(100)}) != 75 {
		t.Fatal("TG04とPG04の変更が分離されていません")
	}
	if tg.entryBool("addon.entry.confirmed", true, map[string]any{"elapsed": float64(30)}) || !pg.entryBool("addon.entry.confirmed", true, map[string]any{"elapsed": float64(30)}) {
		t.Fatal("入口確認時間が個別式を参照していません")
	}
	d07Episode := ResearchEpisode{Book: ResearchBook{FeeFree: true}, Rules: changed}
	if got := d07Episode.episodeNumber("trail_width", 100, map[string]any{"tick": float64(10), "band_width": float64(200)}); got != 100 {
		t.Fatal("D07独立入口にTG04式が混入しました", got)
	}
	p := researchTestPortfolio(t, "TG04", 10)
	p.Rules = changed
	o := researchTestObservation(1, 1, 1000)
	if p.researchGate(o, 1, 80) {
		t.Fatal("基本枠の外部見送り式が適用されません")
	}
	s := p.sleeve("core")
	s.FixedR = 100
	if width, capped := p.researchResidualWidth(s); width != 50 || capped {
		t.Fatal("TG04残玉の外部追尾幅が適用されません", width, capped)
	}
}
