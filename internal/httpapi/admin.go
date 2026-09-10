package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/boar/vote-sys/internal/poll"
)

type createPollRequest struct {
	Question   string   `json:"question"`
	Kind       string   `json:"kind"` // single | multiple
	MaxChoices int      `json:"max_choices,omitempty"`
	StartsAt   string   `json:"starts_at"`
	EndsAt     string   `json:"ends_at"`
	Options    []string `json:"options"`
}

type adminPollResponse struct {
	ID         string           `json:"id"`
	Question   string           `json:"question"`
	Kind       string           `json:"kind"`
	MaxChoices int              `json:"max_choices,omitempty"`
	StartsAt   time.Time        `json:"starts_at"`
	EndsAt     time.Time        `json:"ends_at"`
	Generation int              `json:"generation"`
	State      string           `json:"state"`
	Options    []optionResponse `json:"options"`
}

type adminResultsResponse struct {
	PollID string `json:"poll_id"`
	// Voters — знаменатель для процентов. При kind = multiple sum(votes) не
	// равен числу проголосовавших, и проценты от суммы дали бы больше 100%
	// (architecture.md §5.8).
	Voters  int64              `json:"voters"`
	Results []adminOptionCount `json:"results"`
	// StaleFor — насколько устарел снапшот. Результаты eventually consistent:
	// админка отстаёт на секунды, и это честно показывается.
	StaleForMS int64 `json:"stale_for_ms"`
}

type adminOptionCount struct {
	OptionID string  `json:"option_id"`
	Text     string  `json:"text"`
	Votes    int64   `json:"votes"`
	Percent  float64 `json:"percent"` // от Voters, не от суммы
}

// intParam разбирает неотрицательный числовой параметр запроса.
//
// Невалидный ввод — это 400, а не 500 и не молчаливый ноль. До правки
// ?offset=-5 доезжал до SQL и возвращал «внутреннюю ошибку» на ошибку клиента,
// а ?offset=abc молча превращался в ноль: разбор игнорировал и значение, и
// ошибку.
//
// max = 0 означает «верхней границы нет».
func intParam(r *http.Request, name string, def, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: ожидается целое число", name)
	}
	if v < 0 {
		return 0, fmt.Errorf("%s: не может быть отрицательным", name)
	}
	if max > 0 && v > max {
		return 0, fmt.Errorf("%s: не больше %d", name, max)
	}
	return v, nil
}

func stateName(s poll.State) string {
	switch s {
	case poll.StateArmed:
		return "armed"
	case poll.StateOpen:
		return "open"
	default:
		return "closed"
	}
}

func toAdminPoll(p *poll.Poll, now time.Time) adminPollResponse {
	opts := make([]optionResponse, len(p.Options))
	for i, o := range p.Options {
		opts[i] = optionResponse{ID: o.ID.String(), Text: o.Text}
	}
	return adminPollResponse{
		ID: p.ID.String(), Question: p.Question, Kind: string(p.Kind),
		MaxChoices: p.MaxChoices, StartsAt: p.StartsAt, EndsAt: p.EndsAt,
		Generation: p.Generation, State: stateName(p.StateAt(now)), Options: opts,
	}
}

func (s *Server) handleCreatePoll(w http.ResponseWriter, r *http.Request) {
	var req createPollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "не разобрать тело запроса")
		return
	}

	startsAt, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "starts_at: ожидается RFC3339")
		return
	}
	endsAt, err := time.Parse(time.RFC3339, req.EndsAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "ends_at: ожидается RFC3339")
		return
	}

	p, err := s.polls.Create(r.Context(), poll.CreateParams{
		Question:   req.Question,
		Kind:       poll.Kind(req.Kind),
		MaxChoices: req.MaxChoices,
		StartsAt:   startsAt,
		EndsAt:     endsAt,
		Options:    req.Options,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toAdminPoll(p, time.Now()))
}

// maxListLimit — потолок размера страницы.
const maxListLimit = 200

func (s *Server) handleListPolls(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 0, maxListLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	offset, err := intParam(r, "offset", 0, 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	polls, err := s.polls.List(r.Context(), limit, offset)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	now := time.Now()
	out := make([]adminPollResponse, 0, len(polls))
	for _, p := range polls {
		out = append(out, toAdminPoll(p, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"polls": out})
}

func (s *Server) handleAdminGetPoll(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id: ожидается uuid")
		return
	}
	p, err := s.polls.Get(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminPoll(p, time.Now()))
}

// handleAdminResults отдаёт агрегаты в любой момент, включая время голосования,
// и не кэшируется. Публичный эндпоинт результатов до закрытия молчит по
// методологическим причинам, но оператор видеть цифры должен.
func (s *Server) handleAdminResults(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id: ожидается uuid")
		return
	}
	p, err := s.cache.Get(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, s.resultsOf(r.Context(), p))
}

// resultsOf собирает ответ, беря поштучный максимум снапшота и Postgres.
//
// Снапшот свежее — раз в секунду против пяти, — но после потери Redis он
// оказывается НИЖЕ сохранённого: счётчики начали отсчёт заново. Отдавать его
// безусловно значит показывать оператору, как результат едет назад, — ровно то,
// от чего GREATEST защищает хранилище (architecture.md §5.7). Замерено на
// стенде: после двадцатисекундного отказа Redis в Postgres было 17246, в
// снапшоте 12386, и админка показывала меньшее.
//
// Максимум не «замораживает» цифру при осознанном сбросе: сброс поднимает
// generation, и upsert перезаписывает Postgres новым, меньшим значением
// безусловно — так что максимум берётся уже из двух актуальных источников.
func (s *Server) resultsOf(ctx context.Context, p *poll.Poll) adminResultsResponse {
	var (
		votes  = make(map[uuid.UUID]int64, len(p.Options))
		voters int64
		stale  int64 = -1 // -1 означает «снапшота нет, данные только из Postgres»
	)

	if agg, err := s.polls.Results(ctx, p.ID); err == nil {
		for id, n := range agg.Votes {
			votes[id] = n
		}
		voters = agg.Voters
	} else {
		s.logger.Error("не удалось прочитать агрегаты из Postgres", "poll_id", p.ID, "err", err)
	}

	if snap, ok := s.snapshots.Get(p.ID); ok {
		for id, n := range snap.Votes {
			if n > votes[id] {
				votes[id] = n
			}
		}
		if snap.Voters > voters {
			voters = snap.Voters
		}
		stale = time.Since(snap.TakenAt).Milliseconds()
	}

	out := adminResultsResponse{
		PollID:     p.ID.String(),
		Voters:     voters,
		StaleForMS: stale,
		Results:    make([]adminOptionCount, len(p.Options)),
	}
	for i, o := range p.Options {
		n := votes[o.ID]
		// Проценты считаются от числа участников, а не от суммы голосов: при
		// kind = multiple один зритель увеличивает несколько счётчиков, и
		// проценты от суммы дали бы больше 100% (architecture.md §5.8).
		var pct float64
		if voters > 0 {
			pct = float64(n) / float64(voters) * 100
		}
		out.Results[i] = adminOptionCount{
			OptionID: o.ID.String(), Text: o.Text, Votes: n, Percent: pct,
		}
	}
	return out
}

// writeStoreError переводит ошибку домена в код ответа.
//
// Разделение важно: ошибка описания опроса — это 400, отсутствие опроса — 404,
// и только неопознанное — 500. Иначе нарушение CHECK'а в схеме прилетело бы
// клиенту как отказ сервера, хотя виноват запрос.
func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	var invalid *poll.ErrInvalidPoll
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "invalid_poll", invalid.Error())
	case errors.Is(err, poll.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "опрос не найден")
	default:
		s.logger.Error("ошибка хранилища", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
	}
}
