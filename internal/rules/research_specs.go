package rules

import "strings"

// researchSpecs はTG04・PG04の担当別入口・管理・出口を独立した設定として定義します。
//
// 引数: profile(string)はtg04又はpg04。返り値: []Specは当該手法専用の式です。
// D07と共通の既定条件も名前空間と設定ファイルを分離し、変更を共有しません。
func researchSpecs(profile string) []Spec {
	if profile != "tg04" && profile != "pg04" {
		return nil
	}
	var out []Spec
	shared := map[string]bool{"adverse": true, "adverse_confirm": true, "opposite": true, "support": true, "add_trigger": true, "add_ready": true}
	for _, spec := range addonSpecs() {
		if strings.HasPrefix(spec.ID, "addon.entry.") {
			spec.ID = strings.Replace(spec.ID, "addon.entry.", "research.entry.", 1)
			out = append(out, spec)
		} else if name := strings.TrimPrefix(spec.ID, "addon."); shared[name] {
			spec.ID = "research.active." + name
			out = append(out, spec)
		}
	}
	out = append(out,
		Spec{ID: "research.entry.range_initial", Description: "安全確認を加えない独立夜間枠は、極値から0.25R反発し境界内側へ戻ることを確認。", Kind: "bool", Expression: "rebound >= 0.25 * risk && boundary_distance >= 0", Env: map[string]any{"rebound": float64(0), "risk": float64(0), "boundary_distance": float64(0)}},
		Spec{ID: "research.active.cooldown", Description: "全返済後は120秒準備し、連敗2回以上は900秒停止。", Kind: "number", Expression: "losses >= 2 ? 900.0 : 120.0", Env: map[string]any{"losses": float64(0)}},
		Spec{ID: "research.active.trail_active", Description: "最高順行幅が1.25R以上へ届いたら利益追尾を有効にする。", Kind: "bool", Expression: "best >= 1.25 * risk", Env: map[string]any{"best": float64(0), "risk": float64(0)}},
		Spec{ID: "research.active.trail_width", Description: "利益追尾の戻り幅は0.75R。停止価格を不利な方向へ緩めない。", Kind: "number", Expression: "0.75 * risk", Env: map[string]any{"risk": float64(0)}},
		Spec{ID: "research.active.exit", Description: "現在の順行幅が初期損切り又は利益追尾の停止幅以下なら全返済。", Kind: "bool", Expression: "gain <= floor", Env: map[string]any{"gain": float64(0), "floor": float64(0)}},
		// ----------------------------------------
		Spec{ID: "research.episode.fixed_r", Description: "日中の帯外失敗担当の初期リスク幅は8ティックと停止距離の大きい方。", Kind: "number", Expression: "max(8 * tick, distance)", Env: map[string]any{"tick": float64(0), "distance": float64(0)}},
		Spec{ID: "research.episode.partial_floor", Description: "半分の利確が約定したら停止を往復費用と1ティックの利益位置以上へ上げる。", Kind: "number", Expression: "round_trip + tick", Env: map[string]any{"round_trip": float64(0), "tick": float64(0)}},
		Spec{ID: "research.episode.trail_active", Description: "日中の帯外失敗担当は最高順行幅1Rから追尾する。", Kind: "bool", Expression: "best >= risk", Env: map[string]any{"best": float64(0), "risk": float64(0)}},
		Spec{ID: "research.episode.trail_width", Description: "日中の帯外失敗担当の追尾幅は8ティックと固定20分帯幅の半分の大きい方。", Kind: "number", Expression: "max(8 * tick, 0.5 * band_width)", Env: map[string]any{"tick": float64(0), "band_width": float64(0)}},
		Spec{ID: "research.episode.exit", Description: "日中の帯外失敗担当は停止価格へ戻ったら新しい玉から全返済。", Kind: "bool", Expression: "gain <= floor", Env: map[string]any{"gain": float64(0), "floor": float64(0)}},
		Spec{ID: "research.episode.partial", Description: "固定帯中心へ到達し往復費用と1ティックを超える含み益なら半数利確。", Kind: "bool", Expression: "center_distance >= 0 && gain > round_trip + tick", Env: map[string]any{"center_distance": float64(0), "gain": float64(0), "round_trip": float64(0), "tick": float64(0)}},
		Spec{ID: "research.episode.add_trigger", Description: "帯外失敗担当の追撃は費用を守る停止を確保し、60秒以上経過し、前回追撃より0.5R進んだとき。", Kind: "bool", Expression: "floor >= round_trip && elapsed >= 60 && gain >= last_gain + 0.5 * risk", Env: map[string]any{"floor": float64(0), "round_trip": float64(0), "elapsed": float64(0), "gain": float64(0), "last_gain": float64(0), "risk": float64(0)}},
		Spec{ID: "research.episode.add_ready", Description: "追撃後の停止損失を初期予算内に制限し、気配差は固定帯幅の10%以内。", Kind: "bool", Expression: "exposure <= budget && spread <= 0.1 * band_width", Env: map[string]any{"exposure": float64(0), "budget": float64(0), "spread": float64(0), "band_width": float64(0)}},
		Spec{ID: "research.episode.unlock", Description: "同方向の再参加は、新たに公開された帯端又は価格極値が2ティック以上更新された後に許す。", Kind: "bool", Expression: "(new_public && edge_change >= 2 * tick) || extreme_change >= 2 * tick", Env: map[string]any{"new_public": false, "edge_change": float64(0), "extreme_change": float64(0), "tick": float64(0)}},
		Spec{ID: "research.episode.qualified", Description: "20分帯幅が16ティック以上あり、気配差が帯幅の10%以内なら日中別入口を準備。", Kind: "bool", Expression: "band_width >= 16 * tick && spread <= 0.1 * band_width", Env: map[string]any{"band_width": float64(0), "tick": float64(0), "spread": float64(0)}},
		Spec{ID: "research.episode.expired", Description: "日中別入口の準備が20分超、又は帯の資格を失ったら準備を破棄。", Kind: "bool", Expression: "elapsed > 1200 || !qualified", Env: map[string]any{"elapsed": float64(0), "qualified": false}},
		Spec{ID: "research.episode.side", Description: "20分帯の下側を2ティック抜けたら買い回復候補、上側を抜けたら売り回復候補。", Kind: "number", Expression: "mid <= low - 2 * tick ? 1.0 : (mid >= high + 2 * tick ? -1.0 : 0.0)", Env: map[string]any{"mid": float64(0), "low": float64(0), "high": float64(0), "tick": float64(0)}},
		Spec{ID: "research.episode.stop_offset", Description: "最初の極値から2ティック又は固定帯幅の10%だけ外側を初期停止位置にする。", Kind: "number", Expression: "max(2 * tick, 0.1 * band_width)", Env: map[string]any{"tick": float64(0), "band_width": float64(0)}},
		Spec{ID: "research.episode.recovered", Description: "帯の境界より内側へ2ティック回復したかを確認。", Kind: "bool", Expression: "distance >= 2 * tick", Env: map[string]any{"distance": float64(0), "tick": float64(0)}},
		Spec{ID: "research.episode.acceptable", Description: "停止距離は正で固定帯幅75%以内、帯中心までの余地は4ティック又は気配差2倍以上。", Kind: "bool", Expression: "distance > 0 && distance <= 0.75 * band_width && reward >= max(4 * tick, 2 * spread)", Env: map[string]any{"distance": float64(0), "band_width": float64(0), "reward": float64(0), "tick": float64(0), "spread": float64(0)}},
		Spec{ID: "research.episode.confirmed", Description: "帯内回復と利益余地の条件が15秒連続してから初回注文する。", Kind: "bool", Expression: "elapsed >= 15", Env: map[string]any{"elapsed": float64(0)}},
		// ----------------------------------------
		Spec{ID: "research.core.fixed_r", Description: "TG04/PG04基本枠の初回Rは8ティックとATRの大きい方。", Kind: "number", Expression: "max(8 * tick, atr)", Env: map[string]any{"tick": float64(0), "atr": float64(0)}},
		Spec{ID: "research.core.day_gate", Description: "日中基本枠は既知の120秒変化が入口R換算で-0.25未満なら見送る。未知は既存条件どおり見送り根拠にしない。", Kind: "bool", Expression: "!own_known || signed_move_r >= -0.25", Env: map[string]any{"own_known": false, "signed_move_r": float64(0)}},
		Spec{ID: "research.core.night_gate", Description: "夜間基本枠は新鮮な二市場の120秒変化が両方逆方向の場合に見送る。", Kind: "bool", Expression: "!(own_known && peer_known && own_move < 0 && peer_move < 0)", Env: map[string]any{"own_known": false, "peer_known": false, "own_move": float64(0), "peer_move": float64(0)}},
		Spec{ID: "research.core.peer_fresh", Description: "夜間入口判断に使う相手市場は観測後15秒以内。未来時刻は実行側で拒否。", Kind: "bool", Expression: "age <= 15", Env: map[string]any{"age": float64(0)}},
		Spec{ID: "research.core.day_adverse", Description: "日中基本枠は120秒・20分が逆行し、初回固定20分平均から0.1R以上逆側なら反証を開始。", Kind: "bool", Expression: "own120 < 0 && own20 < 0 && ma_distance <= -0.1 * risk", Env: map[string]any{"own120": float64(0), "own20": float64(0), "ma_distance": float64(0), "risk": float64(0)}},
		Spec{ID: "research.core.adverse_duration", Description: "日中基本枠の固定平均破綻が15秒続いたら返済。", Kind: "number", Expression: "15.0", Env: map[string]any{}},
		Spec{ID: "research.residual.eligible_reason", Description: "元の利益追尾による全返済通知だけを残り1枚の延長候補にする。", Kind: "bool", Expression: "reason == 2 || reason == 70 || reason == 1437", Env: map[string]any{"reason": float64(0)}},
		Spec{ID: "research.residual.profitable_lot", Description: "残す1枚は現在の返済手数料を引いても利益がある玉に限る。", Kind: "bool", Expression: "gain_yen > close_fee", Env: map[string]any{"gain_yen": float64(0), "close_fee": float64(0)}},
		Spec{ID: "research.residual.profitable_budget", Description: "残す1枚以外の実現・見込利益が費用後で正のときだけ延長する。", Kind: "bool", Expression: "cash_yen > 0", Env: map[string]any{"cash_yen": float64(0)}},
		Spec{ID: "research.residual.budget", Description: "他の玉が返済約定した後の実現黒字を、残り1枚の損失予算にする。", Kind: "number", Expression: "max(0, cash_yen)", Env: map[string]any{"cash_yen": float64(0)}},
		Spec{ID: "research.residual.width", Description: "TG04/PG04の残り1枚は初回固定Rの3倍で利益追尾。D07夜の幅上限は適用しない。", Kind: "number", Expression: "3 * risk", Env: map[string]any{"risk": float64(0)}},
		Spec{ID: "research.residual.exit", Description: "残り1枚が利益予算の停止幅又は追尾幅へ戻ったら返済する。", Kind: "bool", Expression: "gain <= max(budget_floor, trail_floor)", Env: map[string]any{"gain": float64(0), "budget_floor": float64(0), "trail_floor": float64(0)}},
		Spec{ID: "research.residual.quiet_before", Description: "残玉担当は米国市場開始の900秒前から静観。", Kind: "number", Expression: "900.0", Env: map[string]any{}},
		Spec{ID: "research.residual.quiet_after", Description: "残玉担当は米国市場開始の900秒後まで静観。", Kind: "number", Expression: "900.0", Env: map[string]any{}},
	)
	return out
}
