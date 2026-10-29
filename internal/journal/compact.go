package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

const compactFieldLimit = 64 * 1024

// IncludeTraceEvent は転送向けトレースへ含めるイベントを判定します。
//
// 引数: kind(string)はSQLiteのイベント分類です。
// 返り値: boolは記録対象です。個々の原文保存通知は省略しますが、booksとeventsの正本は保持します。
func IncludeTraceEvent(kind string) bool {
	return kind != "入力原文保存"
}

// CompactPayload は巨大な再現用資料を参照情報へ置換し、転送用イベントを作ります。
//
// 引数: kind(string)はイベント分類、raw(json.RawMessage)は正本のJSONです。
// 返り値: json.RawMessageは独立した転送用JSON、errorはJSON不正です。正本を変更しません。
// 発注判断と約定確定の根拠は全量維持します。省略した資料の種類・大きさ・SHA256を明示します。
func CompactPayload(kind string, raw json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("イベント内容が有効なJSONではありません: %s", kind)
	}
	if kind == "発注判断" || kind == "約定確定" {
		return append(json.RawMessage(nil), raw...), nil
	}
	trimmed := bytes.TrimSpace(raw)
	if trimmed[0] != '{' {
		if len(raw) > compactFieldLimit {
			return omittedPayload(raw, "大きな補助資料はSQLite eventsに保持"), nil
		}
		return append(json.RawMessage(nil), raw...), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	omitted := []string{}
	for key, value := range fields {
		if key == "_trace_compaction" || alreadyOmitted(value) {
			continue
		}
		reason := ""
		switch key {
		case "state":
			if kind == "台帳更新" {
				reason = "旧版の全状態複製はSQLite eventsに保持、最新の復元状態はsnapshotを参照"
			}
		case "raw":
			reason = "原文はSQLite booksまたはeventsに保持"
		case "candles":
			reason = "足の全履歴はSQLite eventsに保持"
		case "previous_state":
			reason = "移行前の全状態はSQLite eventsに保持"
		default:
			if len(value) > compactFieldLimit && key != "evidence" && key != "observation" {
				reason = "大きな補助資料はSQLite eventsに保持"
			}
		}
		if reason != "" {
			fields[key] = omittedPayload(value, reason)
			omitted = append(omitted, key)
		}
	}
	if len(omitted) == 0 {
		return append(json.RawMessage(nil), raw...), nil
	}
	sort.Strings(omitted)
	metadata, err := json.Marshal(map[string]any{"version": 1, "omitted_fields": omitted, "original_payload_bytes": len(raw), "original_payload_sha256": digest(raw)})
	if err != nil {
		return nil, err
	}
	fields["_trace_compaction"] = metadata
	return json.Marshal(fields)
}

// alreadyOmitted は二重圧縮によって元資料の参照情報を失わないように判定します。
//
// 引数: value(json.RawMessage)は候補フィールドです。
// 返り値: boolはこのパッケージが作った省略記録ならtrueです。
func alreadyOmitted(value json.RawMessage) bool {
	var marker struct {
		Omitted bool   `json:"_omitted"`
		SHA256  string `json:"json_sha256"`
	}
	return json.Unmarshal(value, &marker) == nil && marker.Omitted && len(marker.SHA256) == 64
}

// omittedPayload はJSON値そのものの指紋と省略理由を記録します。
//
// 引数: raw(json.RawMessage)は省略前の値、reason(string)は正本の参照先を含む理由です。
// 返り値: json.RawMessageは常に有効な省略記録です。文字列の長さ・指紋はJSON引用符も含みます。
func omittedPayload(raw json.RawMessage, reason string) json.RawMessage {
	typeName := "number"
	trimmed := bytes.TrimSpace(raw)
	switch trimmed[0] {
	case '{':
		typeName = "object"
	case '[':
		typeName = "array"
	case '"':
		typeName = "string"
	case 't', 'f':
		typeName = "boolean"
	case 'n':
		typeName = "null"
	}
	encoded, _ := json.Marshal(map[string]any{"_omitted": true, "reason": reason, "json_bytes": len(raw), "json_sha256": digest(raw), "json_type": typeName})
	return encoded
}
