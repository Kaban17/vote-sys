package httpapi

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// routeClass задаёт политику логирования ручки.
//
// Единая политика тут невозможна: админку хочется видеть целиком, а голос идёт
// со скоростью 250 000 запросов в секунду (architecture.md §2). Строка на
// запрос означала бы 250 000 строк в секунду — это не наблюдаемость, а вторая
// нагрузочная система поверх первой, причём с диском на горячем пути.
type routeClass uint8

const (
	// classCold — админка и то, что закрыто CDN. Логируется каждый запрос:
	// объём пренебрежим, а подробности ценны.
	classCold routeClass = iota
	// classHot — голос, минт токена, метаданные опроса. Сэмплирование плюс
	// агрегат: полная картина по числам, отдельные строки — выборочно.
	classHot
	// classProbe — healthz. Пишется только когда сломан: иначе пробы
	// оркестратора забьют лог ровным шумом.
	classProbe
)

var routePolicy = map[string]routeClass{
	"/api/token":                    classHot,
	"/api/polls/{id}":               classHot,
	"/api/polls/{id}/vote":          classHot,
	"/api/polls/{id}/results":       classCold,
	"/api/admin/polls":              classCold,
	"/api/admin/polls/{id}":         classCold,
	"/api/admin/polls/{id}/results": classCold,
	"/healthz":                      classProbe,
}

// trackedStatuses — коды, которые считаются по отдельности. Остальные попадают
// в общее ведро.
//
// Набор не случайный: 409, 410 и 429 на голосе — это не ошибки, а нормальные
// исходы (повтор, опоздание, лимит), и именно их доля показывает, работает ли
// дедуп и не режет ли rate limit живых зрителей.
var trackedStatuses = []int{200, 201, 204, 400, 401, 404, 409, 410, 425, 429, 500, 501, 503}

var statusIndex = func() map[int]int {
	m := make(map[int]int, len(trackedStatuses))
	for i, s := range trackedStatuses {
		m[s] = i
	}
	return m
}()

const cacheLineSize = 64

// padded — счётчик, выровненный по кэш-линии.
//
// Тот же приём, что и у счётчиков голосов (architecture.md §5.6): без паддинга
// соседние счётчики лягут в одну строку кэша, и ядра будут гонять её между
// собой. Здесь это особенно обидно — потерять такт на статистике о запросе
// дороже, чем на самом запросе.
type padded struct {
	v atomic.Int64
	_ [cacheLineSize - 8]byte
}

type routeStats struct {
	pattern string
	class   routeClass

	// counts[i] — число ответов со статусом trackedStatuses[i];
	// последний элемент — всё остальное.
	counts []padded
	latSum padded // наносекунды
	latMax padded
}

func newRouteStats(pattern string, class routeClass) *routeStats {
	return &routeStats{
		pattern: pattern,
		class:   class,
		counts:  make([]padded, len(trackedStatuses)+1),
	}
}

func (rs *routeStats) record(status int, d time.Duration) {
	i, ok := statusIndex[status]
	if !ok {
		i = len(trackedStatuses)
	}
	rs.counts[i].v.Add(1)
	rs.latSum.v.Add(int64(d))

	for {
		cur := rs.latMax.v.Load()
		if int64(d) <= cur || rs.latMax.v.CompareAndSwap(cur, int64(d)) {
			break
		}
	}
}

// accessStats — агрегатор трафика по ручкам.
//
// Карта маршрутов строится один раз при старте и дальше только читается,
// поэтому блокировки не нужны вовсе: набор ручек известен заранее, как и набор
// вариантов ответа у счётчиков голосов.
type accessStats struct {
	routes    map[string]*routeStats
	unmatched *routeStats

	// sampleN — писать одну строку из N на горячем пути.
	sampleN uint64
	seq     atomic.Uint64
}

func newAccessStats(sampleN uint64) *accessStats {
	if sampleN == 0 {
		sampleN = 1
	}
	a := &accessStats{
		routes:    make(map[string]*routeStats, len(routePolicy)),
		unmatched: newRouteStats("<не найдено>", classCold),
		sampleN:   sampleN,
	}
	for pattern, class := range routePolicy {
		a.routes[pattern] = newRouteStats(pattern, class)
	}
	return a
}

func (a *accessStats) route(pattern string) *routeStats {
	if rs, ok := a.routes[pattern]; ok {
		return rs
	}
	return a.unmatched
}

// sampled решает, писать ли отдельную строку для очередного горячего запроса.
//
// Детерминированный счётчик, а не генератор случайных чисел: дешевле и не
// требует источника энтропии на пути каждого голоса.
func (a *accessStats) sampled() bool {
	return a.seq.Add(1)%a.sampleN == 0
}

// Run периодически выводит агрегат и обнуляет счётчики.
//
// Это замена построчному логу, а не дополнение к нему: за интервал одна строка
// на ручку вместо сотен тысяч, и при этом видно ровно то, что нужно во время
// эфира — доли кодов ответа и латентность.
func (a *accessStats) Run(ctx context.Context, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			a.Flush(log)
		case <-ctx.Done():
			// Финальный вывод: интервал почти наверняка не закрыт, а последние
			// секунды события — самые интересные.
			a.Flush(log)
			return
		}
	}
}

// Flush выводит и обнуляет накопленное.
func (a *accessStats) Flush(log *slog.Logger) {
	for _, rs := range a.routes {
		flushRoute(log, rs)
	}
	flushRoute(log, a.unmatched)
}

func flushRoute(log *slog.Logger, rs *routeStats) {
	attrs := make([]any, 0, 8)
	var total int64

	for i := range rs.counts {
		n := rs.counts[i].v.Swap(0)
		if n == 0 {
			continue
		}
		total += n
		name := "other"
		if i < len(trackedStatuses) {
			name = statusKey(trackedStatuses[i])
		}
		attrs = append(attrs, name, n)
	}

	sum := rs.latSum.v.Swap(0)
	max := rs.latMax.v.Swap(0)
	if total == 0 {
		return
	}

	attrs = append([]any{"route", rs.pattern, "total", total}, attrs...)
	attrs = append(attrs,
		"avg_ms", float64(sum)/float64(total)/float64(time.Millisecond),
		"max_ms", float64(max)/float64(time.Millisecond),
	)
	log.Info("трафик", attrs...)
}

func statusKey(code int) string {
	switch code {
	case 200:
		return "s200"
	case 201:
		return "s201"
	case 204:
		return "s204"
	case 400:
		return "s400"
	case 401:
		return "s401"
	case 404:
		return "s404"
	case 409:
		return "s409"
	case 410:
		return "s410"
	case 425:
		return "s425"
	case 429:
		return "s429"
	case 500:
		return "s500"
	case 501:
		return "s501"
	case 503:
		return "s503"
	default:
		return "other"
	}
}
