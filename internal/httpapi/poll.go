package httpapi

import (
	"net/http"
	"time"
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

func (s *Server) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	// TODO(шаг 4): из процессного кэша, не из Postgres.
	notImplemented(w, "шаг 4")
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
	notImplemented(w, "шаг 6")
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
