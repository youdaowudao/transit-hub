package notifications

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"
)

type noticeCursor struct {
	Scope     string    `json:"s"`
	Level     Level     `json:"l,omitempty"`
	Filter    string    `json:"f,omitempty"`
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

func cursorScope(user, workspace string) string {
	raw, _ := json.Marshal([]string{user, workspace})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func decodeCursor(raw, user, workspace string, level Level, filter string) (*noticeCursor, error) {
	if raw == "" {
		return nil, nil
	}
	invalid := func() (*noticeCursor, error) {
		return nil, invalidRequest(workspace, "分页位置不完整、超过512字节，或不属于当前用户、工作区、等级及筛选；本次没有读取其他范围的消息")
	}
	if len(raw) > 512 {
		return invalid()
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return invalid()
	}
	var cursor noticeCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cursor); err != nil {
		return invalid()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || cursor.Scope != cursorScope(user, workspace) || cursor.Level != level || cursor.Filter != filter || cursor.CreatedAt.IsZero() || checkText("分页消息编号", cursor.ID, 512, true, true) != nil {
		return invalid()
	}
	return &cursor, nil
}

func encodeCursor(item Notice, user, workspace string, level Level, filter string) *string {
	raw, _ := json.Marshal(noticeCursor{Scope: cursorScope(user, workspace), Level: level, Filter: filter, CreatedAt: item.CreatedAt, ID: item.ID})
	result := base64.RawURLEncoding.EncodeToString(raw)
	return &result
}
