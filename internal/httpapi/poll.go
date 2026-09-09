package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/boar/vote-sys/internal/poll"
)

type pollResponse struct {
	ID       string           `json:"id"`
	Question string           `json:"question"`
	Kind     string           `json:"kind"`
	Options  []optionResponse `json:"options"`

	// EndsAt отдаётся БЕЗ grace period. Запас применяется только к приёму
	// голоса; если показать его клиенту, клиентский таймер и серверная отсечка
	// разойдутся, и появятся жалобы «кнопка активна, но голос не принят»
	// (architecture.md §5.5).
	EndsAt time.Time `json:"ends_at"`

	// ServerTime — опора для клиентского таймера.
	//
	// Клиент обязан считать по offset'у, а не по часам устройства: на
	// потребительских телефонах они врут на минуты, и без этого у части
	// зрителей кнопка погаснет на десятой секунде ролика. Расхождение NTP между
	// серверами при этом — десятки миллисекунд, полностью покрывается grace
	// period (architecture.md §5.5).
	ServerTime time.Time `json:"server_time"`
}

type optionResponse struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// handleGetPoll отдаёт метаданные опроса зрителю — из процессного кэша, а не из
// Postgres: горячий путь базы не касается (architecture.md §2).
func (s *Server) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	// Метаданные меняться не могут (опрос неизменяем), но ends_at и server_time
	// должны быть свежими у каждого зрителя, иначе таймер поедет.
	w.Header().Set("Cache-Control", "no-store")

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id: ожидается uuid")
		return
	}

	p, err := s.cache.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, poll.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "опрос не найден")
			return
		}
		s.logger.Error("не удалось загрузить опрос", "poll_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
		return
	}

	opts := make([]optionResponse, len(p.Options))
	for i, o := range p.Options {
		opts[i] = optionResponse{ID: o.ID.String(), Text: o.Text}
	}

	writeJSON(w, http.StatusOK, pollResponse{
		ID:       p.ID.String(),
		Question: p.Question,
		Kind:     string(p.Kind),
		Options:  opts,
		// ends_at БЕЗ grace period: запас применяется только к приёму, и если
		// показать его клиенту, таймер и серверная отсечка разойдутся
		// (architecture.md §5.5).
		EndsAt:     p.EndsAt,
		ServerTime: time.Now(),
	})
}

// handlePublicResults — публичные результаты, доступны только после закрытия.
//
// Two-phase кэширование (architecture.md §3):
//
//	опрос идёт              → 425, Cache-Control: no-store
//	ends_at .. +N секунд    → max-age=5, stale-while-revalidate=30
//	после финального flush  → max-age=3600, immutable
//
// После закрытия ответ одинаков для всех, поэтому целиком уезжает в CDN: до
// origin доезжают единицы запросов в секунду от edge-нод вместо десяти
// миллионов. Вторая волна трафика приходит именно сюда, синхронно по таймеру
// у всех зрителей.
func (s *Server) handlePublicResults(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusBadRequest, "bad_request", "id: ожидается uuid")
		return
	}

	p, err := s.cache.Get(r.Context(), id)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		if errors.Is(err, poll.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "опрос не найден")
			return
		}
		s.logger.Error("не удалось загрузить опрос", "poll_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
		return
	}

	now := time.Now()
	if p.StateAt(now) != poll.StateClosed {
		// Промежуточные результаты не показываются: показ лидера во время
		// голосования создаёт bandwagon effect, а для национальной компании по
		// проведению опросов это методологический дефект (architecture.md §5.2).
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusTooEarly, "not_finished", "результаты будут доступны после завершения")
		return
	}

	// Two-phase кэширование (architecture.md §3). Пока досходятся последние
	// батчи — короткий TTL со stale-while-revalidate; дальше результат
	// неизменяем и кэшируется как статика. Именно тогда приходит основная волна
	// читателей: у всех зрителей таймер истекает в одну и ту же секунду.
	if now.Before(p.EndsAt.Add(s.cfg.VoteGracePeriod + resultsSettle)) {
		w.Header().Set("Cache-Control", "public, max-age=5, stale-while-revalidate=30")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600, immutable")
	}

	res := s.resultsOf(r.Context(), p)
	out := publicResults{PollID: res.PollID, Question: p.Question, Voters: res.Voters}
	out.Results = make([]publicOptionCount, len(res.Results))
	for i, r := range res.Results {
		out.Results[i] = publicOptionCount{Text: r.Text, Votes: r.Votes, Percent: r.Percent}
	}
	writeJSON(w, http.StatusOK, out)
}

// resultsSettle — сколько после окончания приёма голосов результат ещё считается
// неокончательным.
//
// За это время досходятся последние батчи со всех инстансов и отрабатывает
// финальный flush при остановке. Дальше цифра не меняется никогда.
const resultsSettle = 30 * time.Second

// publicResults — то же, что видит админка, но без идентификаторов вариантов:
// зрителю они не нужны, а меньше полей — меньше поверхности.
type publicResults struct {
	PollID   string              `json:"poll_id"`
	Question string              `json:"question"`
	Voters   int64               `json:"voters"`
	Results  []publicOptionCount `json:"results"`
}

type publicOptionCount struct {
	Text    string  `json:"text"`
	Votes   int64   `json:"votes"`
	Percent float64 `json:"percent"`
}

// handleHealth — readiness, а не liveness.
//
// Отдаёт 200 только после прогрева: пулы подняты, метаданные armed-опросов в
// памяти. До этого 503, и nginx не шлёт на инстанс трафик. Разница существенна
// именно из-за импульсного профиля — инстанс, вставший в строй недопрогретым,
// встретит пик установкой соединений (architecture.md §6).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.ready.Load() {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "инстанс ещё не прогрет")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"shard":  s.cfg.Shard(),
	})
}
