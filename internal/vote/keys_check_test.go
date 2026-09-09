package vote

import (
	"testing"

	"github.com/google/uuid"
)

// Форма ключей — контракт между пишущей и читающей сторонами. Опечатка в ней не
// ломает сборку и не роняет тесты логики: голоса просто уходят в ключ, который
// никто не читает.
func TestKeyShape(t *testing.T) {
	id := uuid.MustParse("2e22cb8d-d58d-416d-a3e9-1514c8f522b4")

	if got, want := CountsKey(id, 0), "poll:2e22cb8d-d58d-416d-a3e9-1514c8f522b4:counts:0"; got != want {
		t.Errorf("CountsKey = %q, want %q", got, want)
	}
	if got, want := VotersKey(id, 3), "poll:2e22cb8d-d58d-416d-a3e9-1514c8f522b4:voters:3"; got != want {
		t.Errorf("VotersKey = %q, want %q", got, want)
	}
}
