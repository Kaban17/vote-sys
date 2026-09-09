package vote

import (
	"sync"
	"testing"
	"unsafe"
)

// Счётчики обязаны лежать в разных строках кэша: без паддинга ядра гоняли бы
// одну строку между собой (architecture.md §5.6).
func TestCounterIsCacheLineSized(t *testing.T) {
	if got := unsafe.Sizeof(counter{}); got != cacheLine {
		t.Errorf("размер counter = %d, ожидался %d", got, cacheLine)
	}
}

// Главный тест батчера: под гонкой ни один инкремент не теряется и не
// задваивается. Сумма всех дельт плюс остаток должна точно сойтись с числом
// вызовов Add.
func TestSwapUnderRaceLosesNothing(t *testing.T) {
	const (
		goroutines = 64
		perG       = 2000
		options    = 4
	)
	c := NewCounters(options)

	var (
		wg      sync.WaitGroup
		stop    = make(chan struct{})
		mu      sync.Mutex
		drained = Delta{Options: make([]int64, options)}
	)

	// Писатели: каждый голос — одна опция плюс один voters.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			idx := []int{g % options}
			for i := 0; i < perG; i++ {
				c.Add(idx)
			}
		}(g)
	}

	// Сборщик: параллельно снимает дельты, как это делает Flush.
	var collector sync.WaitGroup
	collector.Add(1)
	go func() {
		defer collector.Done()
		accumulate := func() {
			d := c.Swap()
			mu.Lock()
			for i, n := range d.Options {
				drained.Options[i] += n
			}
			drained.Voters += d.Voters
			mu.Unlock()
		}
		for {
			select {
			case <-stop:
				accumulate() // финальный сбор, как при graceful shutdown
				return
			default:
				accumulate()
			}
		}
	}()

	wg.Wait()
	close(stop)
	collector.Wait()

	const wantTotal = goroutines * perG
	if drained.Voters != wantTotal {
		t.Errorf("voters = %d, ожидалось %d", drained.Voters, wantTotal)
	}

	var sum int64
	for _, n := range drained.Options {
		sum += n
	}
	if sum != wantTotal {
		t.Errorf("сумма по опциям = %d, ожидалось %d", sum, wantTotal)
	}

	// Каждая опция получила ровно свою долю: goroutines/options писателей.
	wantPerOption := int64(goroutines / options * perG)
	for i, n := range drained.Options {
		if n != wantPerOption {
			t.Errorf("опция %d: %d, ожидалось %d", i, n, wantPerOption)
		}
	}
}

// Swap уже забрал значения из памяти. Без Restore они теряются при полностью
// живом инстансе — не при падении, а просто из-за сетевой ошибки
// (architecture.md §5.6).
func TestRestoreReturnsValues(t *testing.T) {
	c := NewCounters(3)
	c.Add([]int{0})
	c.Add([]int{1})
	c.Add([]int{1, 2}) // multiple: две опции, один голосующий

	d := c.Swap()
	if c.Swap().IsEmpty() != true {
		t.Fatal("после Swap счётчики должны быть пусты")
	}

	c.Restore(d)
	back := c.Swap()

	if back.Voters != d.Voters {
		t.Errorf("voters после Restore = %d, было %d", back.Voters, d.Voters)
	}
	for i := range d.Options {
		if back.Options[i] != d.Options[i] {
			t.Errorf("опция %d после Restore = %d, было %d", i, back.Options[i], d.Options[i])
		}
	}
}

// Restore не должен затирать голоса, пришедшие между неудачным flush и
// возвратом: значения складываются, а не присваиваются.
func TestRestoreAddsRatherThanOverwrites(t *testing.T) {
	c := NewCounters(2)
	c.Add([]int{0})
	d := c.Swap() // дельта: опция 0 = 1, voters = 1

	c.Add([]int{0}) // голос пришёл, пока flush не удался
	c.Restore(d)

	got := c.Swap()
	if got.Options[0] != 2 || got.Voters != 2 {
		t.Errorf("после Restore опция0=%d voters=%d, ожидалось 2 и 2", got.Options[0], got.Voters)
	}
}

// При multiple один запрос увеличивает несколько опций, но voters — ровно один
// раз: иначе проценты от числа участников дадут больше 100% (architecture.md §5.8).
func TestVotersCountedOncePerRequest(t *testing.T) {
	c := NewCounters(4)
	c.Add([]int{0, 1, 2})
	c.Add([]int{3})

	d := c.Swap()
	if d.Voters != 2 {
		t.Errorf("voters = %d, ожидалось 2", d.Voters)
	}
	var sum int64
	for _, n := range d.Options {
		sum += n
	}
	if sum != 4 {
		t.Errorf("сумма голосов = %d, ожидалось 4", sum)
	}
}

// Для single число участников совпадает с суммой голосов. Инвариант дешёвый и
// ловит ошибки батчера бесплатно (architecture.md §5.8).
func TestSingleChoiceInvariant(t *testing.T) {
	c := NewCounters(3)
	for i := 0; i < 100; i++ {
		c.Add([]int{i % 3})
	}
	d := c.Swap()

	var sum int64
	for _, n := range d.Options {
		sum += n
	}
	if sum != d.Voters {
		t.Errorf("для single sum(votes)=%d должно равняться voters=%d", sum, d.Voters)
	}
}

func TestEmptyDelta(t *testing.T) {
	c := NewCounters(3)
	if !c.Swap().IsEmpty() {
		t.Error("свежие счётчики должны давать пустую дельту")
	}
	c.Add([]int{1})
	if c.Swap().IsEmpty() {
		t.Error("после Add дельта не пуста")
	}
}

// Add лежит на пути каждого голоса: аллокаций быть не должно.
func TestAddDoesNotAllocate(t *testing.T) {
	c := NewCounters(8)
	idx := []int{0, 3, 7}
	if n := testing.AllocsPerRun(100, func() { c.Add(idx) }); n != 0 {
		t.Errorf("Add выделяет %v аллокаций на вызов, ожидалось 0", n)
	}
}

func BenchmarkAdd(b *testing.B) {
	c := NewCounters(4)
	idx := []int{2}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Add(idx)
		}
	})
}
