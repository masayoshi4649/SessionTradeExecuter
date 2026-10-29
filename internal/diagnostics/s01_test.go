package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestS01CompactSnapshotSeparatesReferenceHoldings は利益床を保存しながら影の巨大状態を転送しないことを確認します。
//
// 引数: t(*testing.T)は人工SQLiteと診断アーカイブの検査対象です。
// 返り値: なし。影の保有枚数と予約は要約に限定し、実保有一覧へ混ぜません。
func TestS01CompactSnapshotSeparatesReferenceHoldings(t *testing.T) {
	cfg := diagnosticConfig(t)
	_, db, _ := diagnosticFixture(t, cfg)
	shadow := map[string]any{
		"Guard":           map[string]any{"Campaign": 3, "InitialQuantity": 2, "InitialDone": true, "Active": true, "Best": 1000, "Floor": 500},
		"RejectedParents": 4,
		"LastRejection":   map[string]any{"Campaign": 4, "BaselineCampaign": 3, "BaselineOccupied": true},
		"Shadow": map[string]any{
			"Research": map[string]any{"Sleeves": []any{map[string]any{"Name": "episode", "Campaign": 3, "Side": -1,
				"Lots": []any{map[string]any{"ID": "仮想だけの玉", "Quantity": 2}, map[string]any{"Quantity": 1}}}}},
			"Orders": map[string]any{"pending": map[string]any{"Signal": map[string]any{"owner": "episode"}, "Done": false},
				"done": map[string]any{"Signal": map[string]any{"owner": "episode"}, "Done": true}},
			"Executions": strings.Repeat("巨大影情報", 10000),
		},
	}
	raw, err := json.Marshal(shadow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE snapshot SET state=json_set(state,'$.runtime_state.Portfolio.S01',json(?)) WHERE id=1`, string(raw)); err != nil {
		t.Fatal(err)
	}
	paths, err := Export(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	files := readArchive(t, paths)
	body := files["NK225mini/current_s01_state.jsonl"]
	var got struct {
		Guard    struct{ InitialQuantity, Floor int }
		Rejected int `json:"rejected_parents"`
		Baseline struct {
			Campaign, Quantity int
			Pending            int `json:"pending_count"`
		} `json:"baseline_episode"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err, string(body))
	}
	if got.Guard.InitialQuantity != 2 || got.Guard.Floor != 500 || got.Rejected != 4 || got.Baseline.Campaign != 3 || got.Baseline.Quantity != 3 || got.Baseline.Pending != 1 {
		t.Fatal("S01の利益床又は仮想基準占有が欠落しました", string(body))
	}
	for name, content := range files {
		if bytes.Contains(content, []byte("巨大影情報")) || bytes.Contains(content, []byte("仮想だけの玉")) {
			t.Fatal("仮想玉の詳細又は巨大履歴が診断へ漏れました", name)
		}
	}
	if len(body) > 2000 {
		t.Fatal("S01の要約が大き過ぎます", len(body))
	}
}
