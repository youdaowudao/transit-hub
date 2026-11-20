package httpjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var (
	ErrStrictObject = errors.New("请求必须是一个完整JSON对象，不能包含额外内容；请重新读取页面后操作。")
	ErrStrictNull   = errors.New("请求中的字段不能为null；请填写有效值，需要清空列表时请明确提交空数组。")
	ErrStrictFields = errors.New("请求包含不支持的字段或字段类型不正确；请重新读取页面后操作。")
	ErrStrictSize   = errors.New("请求内容超过64KiB或未能完整读取；请缩短内容并检查网络后重试。")
)

// DecodeStrictObject is only for the new analysis operations; legacy Decode is unchanged.
func DecodeStrictObject(w http.ResponseWriter, r *http.Request, target any) (map[string]json.RawMessage, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, ErrStrictSize
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&fields) != nil || fields == nil {
		return nil, ErrStrictObject
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrStrictObject
	}
	for _, field := range fields {
		var value any
		if json.Unmarshal(field, &value) != nil {
			return nil, ErrStrictFields
		}
		if containsNull(value) {
			return nil, ErrStrictNull
		}
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if typed.Decode(target) != nil {
		return nil, ErrStrictFields
	}
	return fields, nil
}

func containsNull(value any) bool {
	if value == nil {
		return true
	}
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			if containsNull(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if containsNull(item) {
				return true
			}
		}
	}
	return false
}
