package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// Коды ответов на голос (architecture.md §8).
//
//	200 голос принят
//	400 невалидные option_ids (несуществующие, дубли, нарушен max_choices)
//	401 нет токена или подпись не сходится
//	404 опроса не существует
//	409 по этому токену уже голосовали
//	410 опрос закрыт (now > ends_at + grace)
//	425 опрос ещё не начался
//	429 превышен лимит частоты — отдаёт nginx, до приложения не доходит
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Заголовки уже ушли, исправить ответ нельзя — только записать факт.
		slog.Error("не удалось записать ответ", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Code: code, Message: msg})
}

// notImplemented — заглушка для ещё не реализованных хендлеров.
//
// Честнее пустого 200: несделанное должно быть отличимо от сделанного и
// сломанного, иначе первый же сквозной прогон введёт в заблуждение.
func notImplemented(w http.ResponseWriter, step string) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "будет реализовано: "+step)
}

// requireAdmin — статический bearer-токен из окружения.
//
// Для тестового задания достаточно; в проде здесь был бы полноценный IdP.
// Сравнение за постоянное время: токен один на всю админку и живёт долго,
// поэтому утечка по времени сравнения — реальный, а не теоретический канал.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	want := []byte(s.cfg.AdminBearerToken)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := bearerToken(r)
		if !ok || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "нужен админский токен")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) ([]byte, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return nil, false
	}
	return []byte(strings.TrimSpace(h[len(prefix):])), true
}
