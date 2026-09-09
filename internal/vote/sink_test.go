package vote

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
)

// Классификация решает, можно ли повторить сброс. Ошибка в ней стоит либо
// потерянных голосов, либо задвоенных (architecture.md §5.4).
func TestClassify(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		maybeApplied bool
	}{
		{
			name:         "дедлайн контекста",
			err:          context.DeadlineExceeded,
			maybeApplied: true,
		},
		{
			name:         "дедлайн сокета",
			err:          os.ErrDeadlineExceeded,
			maybeApplied: true,
		},
		{
			name:         "таймаут чтения по установленному соединению",
			err:          &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded},
			maybeApplied: true,
		},
		{
			name:         "обрыв записи",
			err:          &net.OpError{Op: "write", Err: errors.New("broken pipe")},
			maybeApplied: true,
		},
		{
			name:         "дозвон не удался",
			err:          &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			maybeApplied: false,
		},
		{
			name:         "прикладная ошибка",
			err:          errors.New("WRONGTYPE"),
			maybeApplied: false,
		},
	}

	for _, c := range cases {
		got := errors.Is(classify(c.err), ErrMaybeApplied)
		if got != c.maybeApplied {
			t.Errorf("%s: maybeApplied=%v, ожидалось %v", c.name, got, c.maybeApplied)
		}
	}
}
