// Команда loadgen — нагрузочный генератор.
//
// Воспроизводит профиль ТВ-эфира, а не ровный поток (architecture.md §2).
// Ровная нагрузка не проверяет главное — поведение под импульсом, а именно он и
// определяет всю архитектуру.
//
// Что должен показать прогон:
//   - RPS и латентность p50/p95/p99 на приёме голоса;
//   - долю 409 (дедуп работает) и 429 (rate limit работает);
//   - сходимость: принято голосов == агрегат после закрытия;
//   - поведение при убийстве инстанса и при остановке Redis (сценарии
//     запускаются снаружи, генератор просто продолжает слать).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

type options struct {
	target     string
	pollID     string
	adminToken string
	rps        int
	duration   time.Duration
	profile    string
	workers    int
	timeout    time.Duration
	csv        string
	verify     bool
}

func run() error {
	var o options
	flag.StringVar(&o.target, "target", "http://localhost:8080", "базовый адрес")
	flag.StringVar(&o.pollID, "poll", "", "идентификатор опроса; пустой — создать новый")
	flag.StringVar(&o.adminToken, "admin-token", "dev-admin-token", "админский токен")
	flag.IntVar(&o.rps, "rps", 200, "средний темп голосов в секунду")
	flag.DurationVar(&o.duration, "duration", time.Minute, "длительность окна")
	flag.StringVar(&o.profile, "profile", "tv", "профиль нагрузки: tv | flat")
	flag.IntVar(&o.workers, "workers", 256, "число параллельных зрителей")
	flag.DurationVar(&o.timeout, "timeout", 10*time.Second, "таймаут запроса")
	flag.StringVar(&o.csv, "csv", "", "файл для посекундной статистики")
	flag.BoolVar(&o.verify, "verify", true, "сверить принятое с агрегатом после прогона")
	flag.Parse()

	client := newClient(o)

	poll, err := resolvePoll(client, o)
	if err != nil {
		return err
	}
	fmt.Printf("опрос %s, вариантов %d, до %s\n",
		poll.ID, len(poll.Options), poll.EndsAt.Format(time.TimeOnly))

	total := o.rps * int(o.duration.Seconds())
	offsets := schedule(o.profile, total, o.duration)
	fmt.Printf("профиль %s: %d голосов за %s, пик ~%.0f RPS\n\n",
		o.profile, total, o.duration, peakRate(offsets))

	m := newMetrics(total, o.duration)
	fire(client, o, poll, offsets, m)

	m.report(os.Stdout, o)
	if o.csv != "" {
		if err := m.writeCSV(o.csv); err != nil {
			return err
		}
		fmt.Printf("\nпосекундная статистика: %s\n", o.csv)
	}

	if o.verify {
		return verify(client, o, poll, m)
	}
	return nil
}

func newClient(o options) *http.Client {
	// Пул под число зрителей: иначе генератор упрётся в собственные соединения
	// и покажет латентность клиента вместо латентности сервиса.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = o.workers * 2
	tr.MaxIdleConnsPerHost = o.workers * 2
	tr.MaxConnsPerHost = o.workers * 2
	return &http.Client{Transport: tr, Timeout: o.timeout}
}

// --- расписание -------------------------------------------------------------

// schedule возвращает моменты отправки голосов от начала прогона.
//
// Профиль tv — экспоненциальный спад: зрители реагируют на призыв почти
// одновременно, дальше остаётся хвост. Постоянная спада выбрана так, чтобы на
// первую треть окна приходилась ровно половина голосов, как в допущениях
// нагрузочного расчёта (architecture.md §2).
//
// Условие «половина в первой трети» сводится к u²+u−1=0, где u = e^(−D/3τ), то
// есть u равно обратному золотому сечению, а τ = D/1,4436. Пик при этом выше
// среднего в 1,89 раза.
//
// Моменты берутся обратной функцией распределения по равномерным квантилям, а не
// случайной выборкой: профиль получается гладким и воспроизводимым от прогона к
// прогону, что важно для сравнения замеров между собой.
func schedule(profile string, n int, d time.Duration) []time.Duration {
	out := make([]time.Duration, n)
	if n == 0 {
		return out
	}

	switch profile {
	case "flat":
		for i := range out {
			out[i] = time.Duration(float64(d) * float64(i) / float64(n))
		}
	default: // tv
		const k = 1.4436 // τ = D/k, см. вывод выше
		tau := float64(d) / k
		norm := 1 - math.Exp(-float64(d)/tau)
		for i := range out {
			q := float64(i) / float64(n)
			out[i] = time.Duration(-tau * math.Log(1-q*norm))
		}
	}
	return out
}

func peakRate(offsets []time.Duration) float64 {
	if len(offsets) < 2 {
		return 0
	}
	var peak float64
	for i := 0; i+1 < len(offsets); i++ {
		if gap := offsets[i+1] - offsets[i]; gap > 0 {
			if r := float64(time.Second) / float64(gap); r > peak {
				peak = r
			}
		}
	}
	return peak
}

// --- прогон -----------------------------------------------------------------

type pollInfo struct {
	ID      string    `json:"id"`
	EndsAt  time.Time `json:"ends_at"`
	Options []struct {
		ID string `json:"id"`
	} `json:"options"`
}

func fire(client *http.Client, o options, poll *pollInfo, offsets []time.Duration, m *metrics) {
	jobs := make(chan int, o.workers*4)

	var wg sync.WaitGroup
	for w := 0; w < o.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				castOne(client, o, poll, i, m)
			}
		}()
	}

	start := time.Now()
	for i, off := range offsets {
		if wait := time.Until(start.Add(off)); wait > 0 {
			time.Sleep(wait)
		}
		select {
		case jobs <- i:
		default:
			// Очередь заполнена: воркеры не успевают. Это не ошибка сервиса, а
			// предел генератора, и мерить его надо отдельно — иначе отставание
			// клиента запишется в латентность сервиса.
			m.lagged.Add(1)
		}
	}
	close(jobs)
	wg.Wait()
	m.elapsed = time.Since(start)
}

func castOne(client *http.Client, o options, poll *pollInfo, i int, m *metrics) {
	// Каждый зритель — свой токен: дедуп должен видеть разные личности.
	name, tok, _, err := mint(client, o, poll.ID)
	if err != nil || tok == "" {
		// Отказ минта считается отдельно: он не должен попадать в латентность
		// голоса, но и молчать о нём нельзя — без токена голос невозможен.
		m.mintFails.Add(1)
		return
	}

	opt := poll.Options[i%len(poll.Options)].ID
	body := fmt.Sprintf(`{"option_ids":["%s"]}`, opt)

	req, _ := http.NewRequest(http.MethodPost,
		o.target+"/api/polls/"+poll.ID+"/vote", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", name+"="+tok)

	began := time.Now()
	resp, err := client.Do(req)
	took := time.Since(began)
	if err != nil {
		m.record(took, 0, err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	m.record(took, resp.StatusCode, nil)
}

// cookiePrefix дублирует token.CookiePrefix: генератор — внешний клиент и лезть
// во внутренние пакеты сервиса не должен.
const cookiePrefix = "vote_token_"

func mint(client *http.Client, o options, pollID string) (string, string, int, error) {
	body := fmt.Sprintf(`{"poll_id":"%s"}`, pollID)
	req, _ := http.NewRequest(http.MethodPost, o.target+"/api/token", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	// Имя куки включает идентификатор опроса, поэтому ищем по префиксу.
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, cookiePrefix) {
			return c.Name, c.Value, resp.StatusCode, nil
		}
	}
	return "", "", resp.StatusCode, nil
}

// --- опрос ------------------------------------------------------------------

func resolvePoll(client *http.Client, o options) (*pollInfo, error) {
	if o.pollID != "" {
		return fetchPoll(client, o, o.pollID)
	}

	now := time.Now()
	body, _ := json.Marshal(map[string]any{
		"question":  "Нагрузочный прогон",
		"kind":      "single",
		"starts_at": now.Add(-5 * time.Second).Format(time.RFC3339),
		"ends_at":   now.Add(o.duration + time.Minute).Format(time.RFC3339),
		"options":   []string{"Вариант A", "Вариант B", "Вариант C"},
	})

	req, _ := http.NewRequest(http.MethodPost, o.target+"/api/admin/polls", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.adminToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("создание опроса: %s: %s", resp.Status, msg)
	}

	var p pollInfo
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func fetchPoll(client *http.Client, o options, id string) (*pollInfo, error) {
	resp, err := client.Get(o.target + "/api/polls/" + id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("опрос %s: %s", id, resp.Status)
	}
	var p pollInfo
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// verify сверяет принятое генератором с агрегатом сервиса.
//
// Это главная проверка корректности во всём прогоне: RPS сам по себе ничего не
// доказывает, а расхождение счётчиков — доказывает.
func verify(client *http.Client, o options, poll *pollInfo, m *metrics) error {
	// Дать долететь последним батчам и снапшоту.
	time.Sleep(3 * time.Second)

	req, _ := http.NewRequest(http.MethodGet,
		o.target+"/api/admin/polls/"+poll.ID+"/results", nil)
	req.Header.Set("Authorization", "Bearer "+o.adminToken)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var res struct {
		Voters  int64 `json:"voters"`
		Results []struct {
			Votes int64 `json:"votes"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}

	var sum int64
	for _, r := range res.Results {
		sum += r.Votes
	}
	accepted := m.byCode(http.StatusOK)

	fmt.Printf("\nСходимость\n")
	fmt.Printf("  принято генератором   %d\n", accepted)
	fmt.Printf("  участников в сервисе  %d\n", res.Voters)
	fmt.Printf("  сумма голосов         %d\n", sum)

	if sum != res.Voters {
		return fmt.Errorf("инвариант single нарушен: сумма %d != участников %d", sum, res.Voters)
	}
	if diff := accepted - res.Voters; diff != 0 {
		fmt.Printf("  расхождение           %+d (%.3f%%)\n",
			-diff, float64(diff)/float64(max64(accepted, 1))*100)
	} else {
		fmt.Printf("  расхождение           нет\n")
	}
	return nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// --- метрики ----------------------------------------------------------------

type metrics struct {
	mu        sync.Mutex
	votes     []time.Duration
	codes     map[int]int64
	errs      int64
	buckets   []bucket
	start     time.Time
	elapsed   time.Duration
	lagged    atomic.Int64
	mintFails atomic.Int64
}

type bucket struct {
	sent, ok, dup, limited, other int64
	latSum                        time.Duration
	latMax                        time.Duration
}

func newMetrics(n int, d time.Duration) *metrics {
	return &metrics{
		votes:   make([]time.Duration, 0, n),
		codes:   make(map[int]int64),
		buckets: make([]bucket, int(d.Seconds())+2),
		start:   time.Now(),
	}
}

func (m *metrics) record(took time.Duration, code int, err error) {
	sec := int(time.Since(m.start).Seconds())

	m.mu.Lock()
	defer m.mu.Unlock()

	m.votes = append(m.votes, took)
	if err != nil {
		m.errs++
		m.codes[0]++
	} else {
		m.codes[code]++
	}

	if sec >= 0 && sec < len(m.buckets) {
		b := &m.buckets[sec]
		b.sent++
		b.latSum += took
		if took > b.latMax {
			b.latMax = took
		}
		switch code {
		case http.StatusOK:
			b.ok++
		case http.StatusConflict:
			b.dup++
		case http.StatusTooManyRequests:
			b.limited++
		default:
			b.other++
		}
	}
}

func (m *metrics) byCode(code int) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codes[code]
}

func (m *metrics) percentile(p float64) time.Duration {
	if len(m.votes) == 0 {
		return 0
	}
	i := int(float64(len(m.votes)-1) * p)
	return m.votes[i]
}

func (m *metrics) report(w io.Writer, o options) {
	m.mu.Lock()
	sort.Slice(m.votes, func(i, j int) bool { return m.votes[i] < m.votes[j] })
	total := int64(len(m.votes))
	m.mu.Unlock()

	fmt.Fprintf(w, "Голоса\n")
	fmt.Fprintf(w, "  отправлено        %d за %s\n", total, m.elapsed.Round(time.Millisecond))
	if m.elapsed > 0 {
		fmt.Fprintf(w, "  фактический RPS   %.0f\n", float64(total)/m.elapsed.Seconds())
	}
	if n := m.lagged.Load(); n > 0 {
		fmt.Fprintf(w, "  НЕ ОТПРАВЛЕНО     %d — генератор не успел, замер занижен\n", n)
	}
	if n := m.mintFails.Load(); n > 0 {
		fmt.Fprintf(w, "  минт не удался    %d — эти зрители до голосования не дошли\n", n)
	}

	fmt.Fprintf(w, "\nЛатентность\n")
	for _, p := range []struct {
		name string
		q    float64
	}{{"p50", 0.50}, {"p95", 0.95}, {"p99", 0.99}, {"max", 1.0}} {
		fmt.Fprintf(w, "  %-4s %8.2f мс\n", p.name, float64(m.percentile(p.q))/float64(time.Millisecond))
	}

	fmt.Fprintf(w, "\nКоды ответа\n")
	m.mu.Lock()
	codes := make([]int, 0, len(m.codes))
	for c := range m.codes {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	for _, c := range codes {
		name := fmt.Sprint(c)
		if c == 0 {
			name = "сбой"
		}
		fmt.Fprintf(w, "  %-5s %8d  %5.1f%%  %s\n", name, m.codes[c],
			float64(m.codes[c])/float64(max64(total, 1))*100, codeMeaning(c))
	}
	m.mu.Unlock()

	m.plot(w)
}

func codeMeaning(c int) string {
	switch c {
	case http.StatusOK:
		return "принят"
	case http.StatusConflict:
		return "повтор — дедуп сработал"
	case http.StatusTooManyRequests:
		return "лимит nginx"
	case http.StatusGone:
		return "опрос закрыт"
	case http.StatusUnauthorized:
		return "нет токена"
	case 0:
		return "сеть/таймаут"
	default:
		return ""
	}
}

// plot рисует профиль нагрузки прямо в терминале: без него «профиль ТВ» —
// утверждение, а не наблюдение.
func (m *metrics) plot(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var peak int64
	last := 0
	for i, b := range m.buckets {
		if b.sent > 0 {
			last = i
			if b.sent > peak {
				peak = b.sent
			}
		}
	}
	if peak == 0 {
		return
	}

	fmt.Fprintf(w, "\nПрофиль (голосов в секунду, пик %d)\n", peak)
	const width = 48
	for i := 0; i <= last; i++ {
		b := m.buckets[i]
		bars := int(float64(b.sent) / float64(peak) * width)
		avg := 0.0
		if b.sent > 0 {
			avg = float64(b.latSum) / float64(b.sent) / float64(time.Millisecond)
		}
		fmt.Fprintf(w, "  %3ds │%-*s│ %5d  %6.1f мс\n",
			i, width, bar(bars), b.sent, avg)
	}
}

func bar(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '#'
	}
	return string(out)
}

func (m *metrics) writeCSV(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	fmt.Fprintln(f, "second,sent,ok,duplicate,limited,other,avg_ms,max_ms")
	for i, b := range m.buckets {
		if b.sent == 0 {
			continue
		}
		fmt.Fprintf(f, "%d,%d,%d,%d,%d,%d,%.3f,%.3f\n", i, b.sent, b.ok, b.dup, b.limited, b.other,
			float64(b.latSum)/float64(b.sent)/float64(time.Millisecond),
			float64(b.latMax)/float64(time.Millisecond))
	}
	return nil
}
