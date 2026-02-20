package resserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleIPText_LocalMode_ReturnsLoginServer(t *testing.T) {
	rs := New(Config{LocalServerMode: true})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ip.txt", nil)
	w := httptest.NewRecorder()
	rs.handleRequest(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /ip.txt 期望 200，得到 %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	// 本地模式应返回 TCP 登录服地址，客户端据此发 104/105
	if body != "127.0.0.1:1863" {
		t.Errorf("GET /ip.txt 本地模式应返回 127.0.0.1:1863，得到 %q", body)
	}
}
