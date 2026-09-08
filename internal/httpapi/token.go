package httpapi

import "net/http"

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
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	// TODO:
	//   pollID из тела/query → issuer.Issue(pollID, now)
	//   http.SetCookie(w, &http.Cookie{
	//       Name: token.CookieName, Value: tok,
	//       Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	//       MaxAge: int(ttl.Seconds()),
	//   })
	//   w.Header().Set("Cache-Control", "no-store")
	//   204
}
