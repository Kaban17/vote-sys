package httpapi

import "net/http"

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
	// TODO
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	// TODO
}

// requireAdmin — статический bearer-токен из окружения.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	// TODO: subtle.ConstantTimeCompare
	return next
}
