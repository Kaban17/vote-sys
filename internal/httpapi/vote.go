package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/boar/vote-sys/internal/dedup"
	"github.com/boar/vote-sys/internal/poll"
	"github.com/boar/vote-sys/internal/token"
	"github.com/boar/vote-sys/internal/vote"
)

// maxVoteBody — потолок на тело запроса.
//
// Голос — это несколько uuid; всё, что больше, либо ошибка, либо попытка занять
// память чтением. На горячем пути такое режется до разбора, а не после.
const maxVoteBody = 1 << 10

// dedupTTLMargin — запас поверх окончания приёма голосов.
const dedupTTLMargin = time.Minute

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
//  3. SET NX EX dedup                 → 409   единственный RTT   (шаг 5)
//  4. counters.Add                            in-memory, без блокировок
//  5. 200 {"accepted": true}
//
// Почему именно так: после окончания ролика прилетает много запросов к закрытому
// опросу — зрители досматривают запись и дожимают кнопку. Дешёвая проверка
// первой отсекает их, не тронув Redis.
//
// Rate limit по IP здесь отсутствует: он живёт на nginx (architecture.md §5.3).
func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	pollID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id: ожидается uuid")
		return
	}

	p, err := s.cache.Get(r.Context(), pollID)
	if err != nil {
		if errors.Is(err, poll.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "опрос не найден")
			return
		}
		s.logger.Error("не удалось загрузить опрос", "poll_id", pollID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
		return
	}

	now := time.Now()
	if p.StateAt(now) == poll.StateArmed {
		writeError(w, http.StatusTooEarly, "not_started", "голосование ещё не началось")
		return
	}
	// Grace period — про приём, а не про состояние: голос, отправленный до
	// дедлайна, доезжает после него (architecture.md §5.5).
	if !p.AcceptsVoteAt(now, s.cfg.VoteGracePeriod) {
		writeError(w, http.StatusGone, "closed", "голосование завершено")
		return
	}

	voter, ok := s.voterFrom(r, pollID, now)
	if !ok {
		writeError(w, http.StatusUnauthorized, "no_token", "нет валидного токена")
		return
	}

	var req voteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVoteBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "не разобрать тело запроса")
		return
	}

	// Буфер на стеке: разбор выбора не должен выделять память на пути каждого
	// голоса (architecture.md §3).
	var idBuf [poll.MaxOptions]uuid.UUID
	if len(req.OptionIDs) > len(idBuf) {
		writeError(w, http.StatusBadRequest, "invalid_choice", poll.ErrTooManyChoices.Error())
		return
	}
	ids := idBuf[:0]
	for _, raw := range req.OptionIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_choice", "option_ids: ожидается uuid")
			return
		}
		ids = append(ids, id)
	}

	if err := p.ValidateChoice(ids); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_choice", err.Error())
		return
	}

	// Дедуп идёт ДО инкремента. Обратный порядок допускал бы двойной счёт при
	// ретраях, что для опроса хуже потери (architecture.md §5.4).
	switch res := s.dedup.Check(r.Context(), pollID, string(voter), dedupTTL(p, s.cfg.VoteGracePeriod, now)); res {
	case dedup.Duplicate:
		writeError(w, http.StatusConflict, "already_voted", "по этому токену уже голосовали")
		return
	case dedup.Bypassed:
		// fail-open: Redis недоступен или цепь разомкнута. Голос принимается —
		// результат опроса ценнее защиты от накрутки, которую ТЗ и так называет
		// обходимой (architecture.md §6). Отдельного счётчика тут нет: сигнал,
		// который нужен во время эфира, — это смена состояния breaker'а, и она
		// логируется в месте, где происходит.
	}

	var idxBuf [poll.MaxOptions]int
	idx := idxBuf[:0]
	for _, id := range ids {
		i, _ := p.IndexOf(id) // существование уже проверено ValidateChoice
		idx = append(idx, i)
	}

	s.counters(p).Add(idx)
	writeJSON(w, http.StatusOK, voteResponse{Accepted: true})
}

// dedupTTL — сколько помнить, что этот зритель уже голосовал.
//
// Ровно до конца приёма голосов плюс небольшой запас: держать ключи дольше
// незачем, а память под дедуп определяет размер кластера не слабее, чем
// пропускная способность (architecture.md §2). Нижняя граница нужна для
// опросов, доживающих последние секунды: нулевой или отрицательный TTL Redis
// не примет.
func dedupTTL(p *poll.Poll, grace time.Duration, now time.Time) time.Duration {
	ttl := p.EndsAt.Add(grace).Sub(now) + dedupTTLMargin
	if ttl < dedupTTLMargin {
		return dedupTTLMargin
	}
	return ttl
}

// voterFrom достаёт токен из куки и проверяет подпись.
func (s *Server) voterFrom(r *http.Request, pollID uuid.UUID, now time.Time) (token.VoterID, bool) {
	c, err := r.Cookie(token.CookieName(pollID))
	if err != nil {
		return "", false
	}
	return s.tokens.Verify(pollID, c.Value, now)
}

// counters возвращает счётчики опроса, заводя их при первом голосе.
//
// Быстрый путь не выделяет памяти вовсе. Срез имён полей для HINCRBY строится
// только при регистрации, то есть один раз за всё время жизни опроса, а не на
// каждый голос.
func (s *Server) counters(p *poll.Poll) *vote.Counters {
	if c, ok := s.batcher.Lookup(p.ID); ok {
		return c
	}
	ids := make([]string, len(p.Options))
	for i, o := range p.Options {
		ids[i] = o.ID.String()
	}
	return s.batcher.Register(p.ID, ids)
}
