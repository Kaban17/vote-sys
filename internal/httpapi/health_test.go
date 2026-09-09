package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boar/vote-sys/internal/config"
)

// quiet глушит журнал: эти тесты про коды ответа, а не про логи.
func quiet(srv *Server) *Server {
	srv.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return srv
}

// Инстанс не должен объявлять себя готовым до прогрева: nginx начнёт слать на
// него трафик, и первые секунды пика уйдут на установку соединений
// (architecture.md §6).
func TestHealthzGatedOnReadiness(t *testing.T) {
	srv := quiet(NewServer(&config.Config{CounterShards: 4, InstanceOrdinal: 2, AccessLogSampleN: 1}, Deps{}))
	h := srv.Routes()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("до прогрева код %d, want 503", rec.Code)
	}

	srv.MarkReady()

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("после прогрева код %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	// Снятие готовности при остановке должно возвращать инстанс из ротации.
	srv.MarkUnready()

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("после MarkUnready код %d, want 503", rec.Code)
	}
}

// Нереализованные хендлеры обязаны быть отличимы от реализованных: пустой 200
// ввёл бы в заблуждение при первом же сквозном прогоне.
//
// Остались только результаты: всё прочее уже реализовано и отвечает по существу
// (401 без админского токена, 400 на некорректный ввод).
func TestUnimplementedHandlersReturn501(t *testing.T) {
	srv := quiet(NewServer(&config.Config{CounterShards: 1, AccessLogSampleN: 1}, Deps{}))
	srv.MarkReady()
	h := srv.Routes()

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/polls/x/results"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s → %d, want 501", c.method, c.path, rec.Code)
		}
	}
}
