package httpapi

import (
	"encoding/json"
	"errors"
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

func (s *Server) handleListPolls(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

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
	notImplemented(w, "шаг 6")
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
