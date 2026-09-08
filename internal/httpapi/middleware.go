package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// statusRecorder запоминает код ответа, который иначе не виден снаружи хендлера.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap нужен http.ResponseController: без него обёртка отрезала бы Flush и
// управление дедлайнами у нижележащего writer'а.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog — журналирование запросов с политикой по классу ручки.
//
// Агрегат считается ВСЕГДА и стоит фиксированную величину: несколько атомарных
// инкрементов. Отдельная строка пишется избирательно (см. routeClass):
//
//	classCold  — каждый запрос;
//	classHot   — одна из ACCESS_LOG_SAMPLE_N;
//	classProbe — молча, пока не сломано;
//	любой 5xx  — всегда, независимо от класса.
//
// Чего в логе НЕТ на горячем пути: адреса клиента и токена.
//
// Это не про объём, а про анонимность. Пара «IP + poll_id + время» на каждый
// голос восстанавливает ровно ту запись, которой мы намеренно не заводим в базе
// (architecture.md §7). Обезличенность, построенная на отсутствии таблицы,
// обнуляется логом, который эту таблицу воспроизводит. На админских ручках
// адрес пишется: там за запросом стоит сотрудник, а не зритель.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		dur := time.Since(start)
		pattern := chi.RouteContext(r.Context()).RoutePattern()
		rs := s.stats.route(pattern)
		rs.record(rec.status, dur)

		if !s.shouldLog(rs, rec.status) {
			return
		}

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", float64(dur) / float64(time.Millisecond),
		}
		if rs.class == classCold {
			attrs = append(attrs, "remote", r.RemoteAddr)
		}
		if rs.class == classHot {
			attrs = append(attrs, "sampled_1_in", s.stats.sampleN)
		}

		if rec.status >= http.StatusInternalServerError {
			s.logger.Error("запрос", attrs...)
			return
		}
		s.logger.Info("запрос", attrs...)
	})
}

func (s *Server) shouldLog(rs *routeStats, status int) bool {
	// Отказ сервера виден всегда: он редок по определению, и пропустить его
	// из-за политики сэмплирования было бы худшим возможным разменом.
	if status >= http.StatusInternalServerError {
		return true
	}
	switch rs.class {
	case classCold:
		return true
	case classHot:
		return s.stats.sampled()
	default: // classProbe
		return false
	}
}
