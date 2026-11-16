package chanshui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type trackedStreamBody struct {
	reader          io.Reader
	maxRead, closes int
}

func (b *trackedStreamBody) Read(p []byte) (int, error) {
	if len(p) > b.maxRead {
		b.maxRead = len(p)
	}
	return b.reader.Read(p)
}
func (b *trackedStreamBody) Close() error { b.closes++; return nil }
func TestStreamResultPreservesJSONAndImageWithBoundedReads(t *testing.T) {
	for _, item := range []struct {
		name, contentType string
		payload           []byte
	}{{"json", "application/json", []byte(`{"message":"完整中文结果","revision":2}`)}, {"image", "image/png", append([]byte{0x89, 'P', 'N', 'G'}, bytes.Repeat([]byte{0, 1, 2, 255}, 128*1024)...)}} {
		t.Run(item.name, func(t *testing.T) {
			body := &trackedStreamBody{reader: bytes.NewReader(item.payload)}
			response := &QueryResponse{Status: 200, ContentType: item.contentType, Length: int64(len(item.payload)), Body: body}
			w := httptest.NewRecorder()
			writeStreamResult(w, response, "read-image")
			var result struct {
				V           int      `json:"v"`
				ContentType string   `json:"contentType"`
				Length      int64    `json:"length"`
				Payload     string   `json:"payloadBase64"`
				Complete    bool     `json:"complete"`
				Error       *Problem `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			actual, err := base64.StdEncoding.DecodeString(result.Payload)
			if err != nil || !bytes.Equal(actual, item.payload) || result.V != 1 || result.ContentType != item.contentType || result.Length != int64(len(item.payload)) || !result.Complete || result.Error != nil {
				t.Fatalf("business payload or completion lost: type=%s length=%d complete=%v issue=%v decode=%v", result.ContentType, result.Length, result.Complete, result.Error, err)
			}
			if w.Header().Get("Content-Type") != ResultContentType || w.Header().Get("Content-Length") != "" || body.maxRead > 32*1024 || body.closes != 1 {
				t.Fatalf("not bounded stream/one cleanup: maxRead=%d closes=%d headers=%v", body.maxRead, body.closes, w.Header())
			}
		})
	}
}

type interruptedStream struct{ sent bool }

func (r *interruptedStream) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("fixture read interrupted")
	}
	r.sent = true
	return copy(p, []byte("{")), nil
}
func TestStreamPartialReadKeepsCauseAndReturnedByteCount(t *testing.T) {
	body := &trackedStreamBody{reader: &interruptedStream{}}
	w := httptest.NewRecorder()
	writeStreamResult(w, &QueryResponse{Status: 200, ContentType: "application/json", Length: 10, Body: body}, "read-settings")
	var result struct {
		Complete bool     `json:"complete"`
		Error    *Problem `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Error == nil || result.Error.OperationResult != "unknown" || !strings.Contains(result.Error.Message, "1/10字节") || !strings.Contains(result.Error.Message, "fixture read interrupted") || body.closes != 1 {
		t.Fatalf("partial read misreported: %+v closes=%d", result, body.closes)
	}
}
