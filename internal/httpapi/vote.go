package httpapi

import "net/http"

type voteRequest struct {
	OptionIDs []string `json:"option_ids"`
}

type voteResponse struct {
	// Цифр здесь нет намеренно.
	//
	// Технически ничто не мешает вернуть агрегат из снапшота в памяти, без
	// единого лишнего round-trip'а. Мы этого не делаем по МЕТОДОЛОГИЧЕСКИМ
	// причинам: показ лидирующего варианта во время голосования создаёт
	// bandwagon effect — зритель видит, что вариант A ведёт, и голосует за A.
	// Для развлекательного голосования допустимо, для национальной компании по
	// проведению опросов — дефект, обесценивающий продукт (architecture.md §5.2).
	Accepted bool `json:"accepted"`
}

// handleVote — горячий путь. Порядок проверок: от бесплатных к дорогим
// (architecture.md §3).
//
//  1. now() >= ends_at + grace       → 410   in-memory, 0 RTT
//  2. валидация option_ids            → 400   in-memory
//  3. SET NX EX dedup                 → 409   единственный RTT
//  4. counters.Add                            in-memory, без блокировок
//  5. 200 {"accepted": true}
//
// Почему именно так: после окончания ролика прилетает много запросов к закрытому
// опросу — зрители досматривают запись и дожимают кнопку. Дешёвая проверка
// первой отсекает их, не тронув Redis.
//
// Rate limit по IP здесь отсутствует: он живёт на nginx (architecture.md §5.3).
func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	// TODO:
	//   poll := cache.Get(id)                        → 404
	//   state := poll.StateAt(now)                   → 425 если armed
	//   !poll.AcceptsVoteAt(now, grace)              → 410
	//   token из куки + issuer.Verify(pollID, tok)   → 401
	//   poll.ValidateChoice(ids)                     → 400
	//
	//   switch dedup.Check(ctx, pollID, token, ttl) {
	//   case dedup.Duplicate: → 409
	//   case dedup.Bypassed:  → считаем голос (fail-open, architecture.md §6)
	//   case dedup.First:     → считаем голос
	//   }
	//
	//   Дедуп идёт ДО инкремента. Обратный порядок допускал бы двойной счёт при
	//   ретраях. Плата: при жёстком отказе инстанса токен уже сожжён, а голос не
	//   долетел — зритель получит 409 и проголосовать не сможет. Масштаб —
	//   ~1600 из 10 млн, при плановом рестарте ноль (architecture.md §5.4).
	//
	//   counters.Add(idx); writeJSON(w, 200, voteResponse{Accepted: true})
	notImplemented(w, "шаг 4")
}
