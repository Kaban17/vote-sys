package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boar/vote-sys/internal/config"
)

type capture struct {
	buf *bytes.Buffer
}

func newCapture() *capture { return &capture{buf: &bytes.Buffer{}} }

func (c *capture) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(c.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// lines возвращает записи с заданным msg.
func (c *capture) lines(msg string) []map[string]any {
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(c.buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			continue
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func newTestServer(t *testing.T, sampleN int) (*Server, *capture) {
	t.Helper()
	cap := newCapture()
	srv := NewServer(&config.Config{CounterShards: 1, AccessLogSampleN: sampleN})
	srv.logger = cap.logger()
	srv.MarkReady()
	return srv, cap
}

func do(h http.Handler, method, path string) {
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
}

// Горячий путь логируется выборочно: 250K RPS × строка на запрос — это вторая
// нагрузочная система поверх первой (architecture.md §2).
//
// Политика проверяется напрямую, а не через роутер: все горячие ручки сейчас
// отдают 501, то есть 5xx, а 5xx пишется всегда в обход сэмплирования. Через
// роутер тест мерил бы заглушки, а не правило.
func TestHotPathIsSampled(t *testing.T) {
	srv, _ := newTestServer(t, 10)
	rs := srv.stats.route("/api/polls/{id}/vote")

	const n = 100
	logged := 0
	for i := 0; i < n; i++ {
		if srv.shouldLog(rs, http.StatusOK) {
			logged++
		}
	}
	if logged != n/10 {
		t.Errorf("записей %d, ожидалось %d (одна из 10)", logged, n/10)
	}
}

// Сэмплирование не должно проглатывать отказы сервера.
func TestSamplingNeverHidesServerErrors(t *testing.T) {
	srv, _ := newTestServer(t, 1000)
	rs := srv.stats.route("/api/polls/{id}/vote")

	for i := 0; i < 50; i++ {
		if !srv.shouldLog(rs, http.StatusInternalServerError) {
			t.Fatal("5xx на горячем пути должен логироваться всегда")
		}
	}
}

// 409 и 410 на голосе — нормальные исходы (повтор, опоздание), а не ошибки.
// Строку они получают только по сэмплированию, но в агрегате видны поимённо:
// именно их доля показывает, работает ли дедуп и не режет ли лимит живых
// зрителей.
func TestExpectedVoteOutcomesAreNotErrors(t *testing.T) {
	srv, cap := newTestServer(t, 1000)
	rs := srv.stats.route("/api/polls/{id}/vote")

	for _, status := range []int{http.StatusConflict, http.StatusGone, http.StatusTooManyRequests} {
		if srv.shouldLog(rs, status) {
			t.Errorf("статус %d не должен обходить сэмплирование", status)
		}
		rs.record(status, 0)
	}
	srv.FlushStats()

	var found map[string]any
	for _, l := range cap.lines("трафик") {
		if l["route"] == "/api/polls/{id}/vote" {
			found = l
		}
	}
	if found == nil {
		t.Fatal("агрегата по ручке голосования нет")
	}
	for _, key := range []string{"s409", "s410", "s429"} {
		if found[key] != float64(1) {
			t.Errorf("%s = %v, ожидалась 1", key, found[key])
		}
	}
}

// Админка логируется целиком: объём пренебрежим, подробности ценны.
func TestColdPathLogsEveryRequest(t *testing.T) {
	srv, cap := newTestServer(t, 1000)
	h := srv.Routes()

	for i := 0; i < 5; i++ {
		do(h, http.MethodGet, "/api/admin/polls")
	}

	lines := cap.lines("запрос")
	if len(lines) != 5 {
		t.Fatalf("записей %d, ожидалось 5", len(lines))
	}
	// На админской ручке за запросом стоит сотрудник, адрес полезен.
	if _, ok := lines[0]["remote"]; !ok {
		t.Error("на админской ручке ожидается remote")
	}
}

// Адрес голосующего в лог не попадает. Пара «IP + poll_id + время» на каждый
// голос восстановила бы запись, которой мы намеренно не заводим в базе
// (architecture.md §7).
func TestHotPathNeverLogsClientAddress(t *testing.T) {
	srv, cap := newTestServer(t, 1)
	h := srv.Routes()

	do(h, http.MethodPost, "/api/polls/p1/vote")
	do(h, http.MethodPost, "/api/token")

	lines := cap.lines("запрос")
	if len(lines) != 2 {
		t.Fatalf("записей %d, ожидалось 2", len(lines))
	}
	for _, l := range lines {
		if _, ok := l["remote"]; ok {
			t.Errorf("на горячем пути не должно быть remote: %v", l)
		}
	}
}

// Пробы оркестратора не должны забивать лог ровным шумом.
func TestProbeIsSilentWhenHealthy(t *testing.T) {
	srv, cap := newTestServer(t, 1)
	h := srv.Routes()

	for i := 0; i < 20; i++ {
		do(h, http.MethodGet, "/healthz")
	}

	if got := len(cap.lines("запрос")); got != 0 {
		t.Errorf("здоровые пробы дали %d записей, ожидалось 0", got)
	}
}

// Отказ сервера редок по определению, и пропустить его из-за сэмплирования было
// бы худшим возможным разменом.
func TestServerErrorsAlwaysLogged(t *testing.T) {
	srv, cap := newTestServer(t, 1000)
	srv.MarkUnready() // healthz начнёт отдавать 503

	h := srv.Routes()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	lines := cap.lines("запрос")
	if len(lines) != 1 {
		t.Fatalf("записей %d, ожидалось 1: 5xx пишется независимо от класса и сэмплирования", len(lines))
	}
	if lines[0]["level"] != "ERROR" {
		t.Errorf("уровень %v, ожидался ERROR", lines[0]["level"])
	}
}

// Агрегат считается на КАЖДЫЙ запрос, независимо от того, попал он в лог или
// нет: именно он даёт полную картину при сэмплировании.
func TestSummaryCountsEveryRequest(t *testing.T) {
	srv, cap := newTestServer(t, 1000)
	h := srv.Routes()

	const n = 50
	for i := 0; i < n; i++ {
		do(h, http.MethodPost, "/api/polls/p1/vote")
	}
	srv.FlushStats()

	var found map[string]any
	for _, l := range cap.lines("трафик") {
		if l["route"] == "/api/polls/{id}/vote" {
			found = l
		}
	}
	if found == nil {
		t.Fatal("агрегата по ручке голосования нет")
	}
	if found["total"] != float64(n) {
		t.Errorf("total = %v, ожидалось %d", found["total"], n)
	}
	// Заглушки отдают 501, и это должно быть видно поимённо.
	if found["s501"] != float64(n) {
		t.Errorf("s501 = %v, ожидалось %d", found["s501"], n)
	}
}

// Повторный Flush не должен показывать те же запросы дважды.
func TestFlushResetsCounters(t *testing.T) {
	srv, cap := newTestServer(t, 1000)
	h := srv.Routes()

	do(h, http.MethodPost, "/api/polls/p1/vote")
	srv.FlushStats()
	before := len(cap.lines("трафик"))

	srv.FlushStats()
	if got := len(cap.lines("трафик")); got != before {
		t.Errorf("второй Flush добавил записи (%d → %d): счётчики не обнулились", before, got)
	}
}
