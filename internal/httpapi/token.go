package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/boar/vote-sys/internal/token"
)

type tokenRequest struct {
	PollID string `json:"poll_id"`
}

// handleToken выпускает анонимный токен и ставит его кукой.
//
// Почему это ОТДЕЛЬНЫЙ POST-эндпоинт, а не Set-Cookie при отдаче лендинга
// (architecture.md §5.1):
//
// Токен уникален на зрителя. Если ставить его заголовком на кэшируемом лендинге,
// один закэшированный ответ уедет ВСЕМ зрителям сразу: все получат один и тот же
// токен, первый SET NX выиграет, остальные 10 млн получат 409. Механизм защиты
// уничтожит опрос полностью.
//
// Большинство CDN по умолчанию не кэшируют ответ с Set-Cookie. Опасность в том,
// что дефолт перестаёт спасать при override вида «cache everything» — а это ровно
// то, что включают, готовя лендинг к 100 млн зрителей. Триггером ошибки
// становится сама оптимизация под нагрузку.
//
// Поэтому защита перенесена из конфигурации в структуру: POST не кэшируется ни
// одним CDN по спецификации HTTP. Нет заголовка, который включил бы ошибку, и
// нет page rule, который бы её создал — она невыразима.
//
// Существование опроса здесь НЕ проверяется, и это намеренно. Эндпоинт обязан
// оставаться свободным от обращений к хранилищам: он самый дешёвый в системе и
// первый кандидат на переезд в edge compute. Токен для несуществующего опроса
// бесполезен — /vote всё равно проверит опрос сам.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	// no-store здесь дублирует структурную защиту. Дублирование осознанное:
	// заголовок дешёв, а цена ошибки — весь опрос.
	w.Header().Set("Cache-Control", "no-store")

	var req tokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "не разобрать тело запроса")
		return
	}

	pollID, err := uuid.Parse(req.PollID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "poll_id: ожидается uuid")
		return
	}

	// Минт идемпотентен: если валидный токен для этого опроса уже есть, новый не
	// выдаётся.
	//
	// Без этого дедуп обходился перезагрузкой страницы. Лендинг запрашивает
	// токен при каждой загрузке, новый затирал куку — и зритель получал новую
	// личность, а с ней и право голоса. Проверить наличие куки на клиенте
	// нельзя: она HttpOnly, и это правильно. Значит идемпотентность должна быть
	// на сервере.
	//
	// Перезагрузить страницу умеет любой зритель, поэтому обход был ниже планки,
	// которую задаёт ТЗ («на уровне обычных, не технически подкованных
	// пользователей»). Очистка куки по-прежнему работает — и по-прежнему
	// разрешена (architecture.md §5.3).
	if c, err := r.Cookie(token.CookieName); err == nil {
		if _, ok := s.tokens.Verify(pollID, c.Value, time.Now()); ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	tok, err := s.tokens.Issue(pollID, time.Now())
	if err != nil {
		s.logger.Error("не удалось выпустить токен", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  token.CookieName,
		Value: tok,
		Path:  "/",
		// HttpOnly: скрипту лендинга токен не нужен, браузер отправит куку сам.
		HttpOnly: true,
		// Secure обязателен в проде. На локальном стенде по HTTP браузер такую
		// куку не примет, поэтому флаг вынесен в конфигурацию.
		Secure: s.cfg.CookieSecure,
		// Lax достаточно: голосование идёт с того же origin, что и лендинг.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.TokenTTL.Seconds()),
	})

	w.WriteHeader(http.StatusNoContent)
}
