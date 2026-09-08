package httpapi

import "net/http"

type createPollRequest struct {
	Question   string   `json:"question"`
	Kind       string   `json:"kind"` // single | multiple
	MaxChoices int      `json:"max_choices,omitempty"`
	StartsAt   string   `json:"starts_at"`
	EndsAt     string   `json:"ends_at"`
	Options    []string `json:"options"`
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

func (s *Server) handleCreatePoll(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "шаг 2")
}
func (s *Server) handleListPolls(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "шаг 2")
}
func (s *Server) handleAdminGetPoll(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "шаг 2")
}

// handleAdminResults отдаёт агрегаты в любой момент, включая время голосования,
// и не кэшируется. Публичный эндпоинт результатов до закрытия молчит по
// методологическим причинам, но оператор видеть цифры должен.
func (s *Server) handleAdminResults(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "шаг 6")
}
