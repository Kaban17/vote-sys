// Package vote содержит счётчики голосов и их сброс в Redis.
package vote

import "sync/atomic"

// cacheLine — размер строки кэша на amd64/arm64.
const cacheLine = 64

// counter — счётчик одной опции, выровненный по кэш-линии.
//
// Паддинг обязателен. Вариантов ответа 2-10, и без него все счётчики легли бы
// в одну-две кэш-линии: ядра гоняли бы их между собой (false sharing), теряя
// больше, чем стоила бы блокировка (architecture.md §5.6).
type counter struct {
	v atomic.Int64
	_ [cacheLine - 8]byte
}

// Counters — счётчики одного опроса: массив по индексам опций плюс отдельный
// счётчик проголосовавших.
//
// Массив, а не канал: канал был бы точкой сериализации, все горутины-хендлеры
// упирались бы в одного читателя. Число опций мало и известно на старте, поэтому
// индексация прямая и обходится без блокировок (architecture.md §5.6).
//
// voters считается отдельно от суммы: при kind = multiple один запрос
// увеличивает несколько счётчиков, и sum(votes) перестаёт быть числом
// участников (architecture.md §5.8).
type Counters struct {
	options []counter
	voters  counter
}

func NewCounters(numOptions int) *Counters {
	return &Counters{options: make([]counter, numOptions)}
}

// Add регистрирует голос: инкремент каждой выбранной опции и один инкремент
// voters независимо от их числа.
func (c *Counters) Add(optionIdx []int) {
	// TODO
}

// Delta — снятые значения счётчиков, готовые к отправке в Redis.
type Delta struct {
	Options []int64
	Voters  int64
}

// Swap атомарно забирает накопленное и обнуляет счётчики.
func (c *Counters) Swap() Delta {
	// TODO
	return Delta{}
}

// Restore возвращает значения обратно после неудачного flush.
//
// Swap уже забрал их из памяти, поэтому без возврата они теряются при полностью
// живом инстансе — не при падении, а просто из-за сетевой ошибки
// (architecture.md §5.6).
func (c *Counters) Restore(d Delta) {
	// TODO
}

// IsEmpty — нечего сбрасывать.
func (d Delta) IsEmpty() bool {
	// TODO
	return true
}
