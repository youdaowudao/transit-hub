package upstream

import (
	"errors"
	"strings"
	"testing"
)

// An in-flight reader has already contacted the upstream. Its local rejection
// must describe discarded results, rather than claiming no request was sent.
func TestSiteReadResultMessageREDKeepsActualExecutionResult(t *testing.T) {
	f := newSiteReadREDFixture(t, 200, "")
	f.onRead = func() {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		siteReadTotalREDPause(t, f)
	}
	_, err := f.svc.Sync(t.Context(), "user", "site")
	var detail *RequestError
	if f.reads.Load() == 0 || !errors.As(err, &detail) || detail.Reason != ErrorRefreshTokenRejected || detail.StatusCode != 0 {
		t.Fatal("读取期间的本地拒绝必须保留已执行请求及原认证分类")
	}
	if strings.Contains(detail.UpstreamMessage, "本次没有请求上游") || !strings.Contains(detail.UpstreamMessage, "结果") || !strings.Contains(detail.UpstreamMessage, "未采用") {
		t.Fatal("已发出请求后的拒绝须说明结果未采用，不能说本次没有请求上游")
	}
}
