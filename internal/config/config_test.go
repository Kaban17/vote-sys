package config

import (
	"strings"
	"testing"
	"time"
)

// setMinimalEnv задаёт только обязательное, чтобы остальное бралось из дефолтов.
func setMinimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("POSTGRES_DSN", "postgres://vote:vote@localhost:5432/vote")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("TOKEN_HMAC_SECRET", "secret")
	t.Setenv("ADMIN_BEARER_TOKEN", "admin")
}

func TestLoadDefaults(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	// Дефолты горячего пути важнее прочих: они определяют и потерю при отказе,
	// и нагрузку на Redis (architecture.md §5.4, §5.6).
	if cfg.FlushInterval != 150*time.Millisecond {
		t.Errorf("FlushInterval = %v, want 150ms", cfg.FlushInterval)
	}
	if cfg.DedupTimeout != 50*time.Millisecond {
		t.Errorf("DedupTimeout = %v, want 50ms", cfg.DedupTimeout)
	}
	if cfg.VoteGracePeriod != 3*time.Second {
		t.Errorf("VoteGracePeriod = %v, want 3s", cfg.VoteGracePeriod)
	}
}

// Секреты обязаны быть заданы явно: молча работать с предсказуемым ключом
// подписи опаснее, чем не стартовать.
func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://localhost/vote")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("TOKEN_HMAC_SECRET", "")
	t.Setenv("ADMIN_BEARER_TOKEN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load без секретов должен возвращать ошибку")
	}
	for _, want := range []string{"TOKEN_HMAC_SECRET", "ADMIN_BEARER_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ошибка не упоминает %s: %v", want, err)
		}
	}
}

// Ошибки собираются разом: при старте полезнее увидеть весь список, чем чинить
// по одной через перезапуск.
func TestLoadReportsAllErrorsAtOnce(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("TOKEN_HMAC_SECRET", "")
	t.Setenv("ADMIN_BEARER_TOKEN", "")
	t.Setenv("FLUSH_INTERVAL", "не-длительность")
	t.Setenv("COUNTER_SHARDS", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("Load должен вернуть ошибку")
	}
	for _, want := range []string{
		"POSTGRES_DSN", "REDIS_ADDR", "TOKEN_HMAC_SECRET",
		"ADMIN_BEARER_TOKEN", "FLUSH_INTERVAL", "COUNTER_SHARDS",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ошибка не упоминает %s:\n%v", want, err)
		}
	}
}

func TestLoadRejectsNonPositiveDurations(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("DEDUP_TIMEOUT", "0s")

	if _, err := Load(); err == nil {
		t.Fatal("нулевой DEDUP_TIMEOUT должен отвергаться: он отключил бы таймаут целиком")
	}
}

// Шард — ординал по модулю числа шардов, а не hash(instance_id): хеш без нужды
// схлопнул бы два инстанса в один шард (architecture.md §7).
func TestShardUsesOrdinal(t *testing.T) {
	cfg := &Config{CounterShards: 4}
	for ordinal, want := range map[int]int{0: 0, 1: 1, 3: 3, 4: 0, 5: 1} {
		cfg.InstanceOrdinal = ordinal
		if got := cfg.Shard(); got != want {
			t.Errorf("ординал %d → шард %d, want %d", ordinal, got, want)
		}
	}
}
