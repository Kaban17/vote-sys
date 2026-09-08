// Package config — конфигурация из окружения. Значения и их смысл описаны в
// .env.example.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr string

	// InstanceOrdinal — порядковый номер инстанса; шард счётчиков — ординал по
	// модулю CounterShards. Ординал, а не hash(instance_id): хеш без нужды
	// схлопнул бы два инстанса в один шард (architecture.md §7).
	InstanceOrdinal int
	CounterShards   int

	PostgresDSN      string
	PostgresMaxConns int32

	RedisAddr     string
	RedisPoolSize int

	// Дедуп — единственный синхронный round-trip и потолок системы
	// (architecture.md §2, §6).
	DedupTimeout          time.Duration
	BreakerErrorThreshold int
	BreakerWindow         time.Duration
	BreakerProbeInterval  time.Duration

	FlushInterval  time.Duration
	FlushThreshold int64

	SnapshotInterval time.Duration
	PersistInterval  time.Duration

	// VoteGracePeriod применяется только к приёму. Клиенту ends_at отдаётся без
	// него (architecture.md §5.5).
	VoteGracePeriod time.Duration

	TokenHMACSecret []byte
	TokenTTL        time.Duration

	AdminBearerToken string

	// LogLevel — debug | info | warn | error.
	LogLevel string
	// AccessLogSampleN — на горячем пути пишется одна строка из N. При 250K RPS
	// строка на запрос была бы второй нагрузочной системой поверх первой
	// (см. internal/httpapi/logstats.go).
	AccessLogSampleN int
	// LogSummaryInterval — как часто выводить агрегат трафика по ручкам.
	// Событие длится минуту, поэтому интервал должен давать несколько точек
	// внутри него, а не одну после.
	LogSummaryInterval time.Duration

	// ShutdownTimeout — сколько ждать завершения текущих запросов и финального
	// flush батчера (architecture.md §5.4).
	ShutdownTimeout time.Duration
}

// Shard возвращает шард счётчиков этого инстанса.
func (c *Config) Shard() int {
	return c.InstanceOrdinal % c.CounterShards
}

// Load читает конфигурацию из окружения.
//
// Все ошибки собираются и возвращаются разом: при старте полезнее увидеть весь
// список проблем, чем чинить их по одной через перезапуск.
func Load() (*Config, error) {
	var l loader

	cfg := &Config{
		HTTPAddr: l.str("HTTP_ADDR", ":8080"),

		InstanceOrdinal: l.integer("INSTANCE_ORDINAL", 0),
		CounterShards:   l.integer("COUNTER_SHARDS", 4),

		PostgresDSN:      l.str("POSTGRES_DSN", ""),
		PostgresMaxConns: int32(l.integer("POSTGRES_MAX_CONNS", 20)),

		RedisAddr:     l.str("REDIS_ADDR", ""),
		RedisPoolSize: l.integer("REDIS_POOL_SIZE", 200),

		DedupTimeout:          l.duration("DEDUP_TIMEOUT", 50*time.Millisecond),
		BreakerErrorThreshold: l.integer("BREAKER_ERROR_THRESHOLD", 20),
		BreakerWindow:         l.duration("BREAKER_WINDOW", time.Second),
		BreakerProbeInterval:  l.duration("BREAKER_PROBE_INTERVAL", 3*time.Second),

		FlushInterval:  l.duration("FLUSH_INTERVAL", 150*time.Millisecond),
		FlushThreshold: int64(l.integer("FLUSH_THRESHOLD", 10000)),

		SnapshotInterval: l.duration("SNAPSHOT_INTERVAL", time.Second),
		PersistInterval:  l.duration("PERSIST_INTERVAL", 5*time.Second),

		VoteGracePeriod: l.duration("VOTE_GRACE_PERIOD", 3*time.Second),

		TokenTTL: l.duration("TOKEN_TTL", 24*time.Hour),

		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", 10*time.Second),

		LogLevel:           l.str("LOG_LEVEL", "info"),
		AccessLogSampleN:   l.integer("ACCESS_LOG_SAMPLE_N", 1000),
		LogSummaryInterval: l.duration("LOG_SUMMARY_INTERVAL", 5*time.Second),
	}

	// Секреты обязаны быть заданы явно: падать на старте лучше, чем молча
	// работать с предсказуемым ключом подписи.
	cfg.TokenHMACSecret = []byte(l.required("TOKEN_HMAC_SECRET"))
	cfg.AdminBearerToken = l.required("ADMIN_BEARER_TOKEN")

	// Адреса хранилищ тоже обязательны: дефолт вида localhost увёл бы инстанс
	// в контейнере в никуда с невнятной ошибкой соединения.
	if cfg.PostgresDSN == "" {
		l.fail("POSTGRES_DSN", errors.New("не задан"))
	}
	if cfg.RedisAddr == "" {
		l.fail("REDIS_ADDR", errors.New("не задан"))
	}

	if cfg.CounterShards <= 0 {
		l.fail("COUNTER_SHARDS", errors.New("должно быть больше нуля"))
	}
	if cfg.InstanceOrdinal < 0 {
		l.fail("INSTANCE_ORDINAL", errors.New("не может быть отрицательным"))
	}
	if cfg.PostgresMaxConns <= 0 {
		l.fail("POSTGRES_MAX_CONNS", errors.New("должно быть больше нуля"))
	}
	if cfg.RedisPoolSize <= 0 {
		l.fail("REDIS_POOL_SIZE", errors.New("должно быть больше нуля"))
	}
	if cfg.AccessLogSampleN <= 0 {
		l.fail("ACCESS_LOG_SAMPLE_N", errors.New("должно быть больше нуля"))
	}
	if _, err := ParseLogLevel(cfg.LogLevel); err != nil {
		l.fail("LOG_LEVEL", err)
	}

	if err := l.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loader накапливает ошибки разбора, чтобы вернуть их одним списком.
type loader struct {
	errs []error
}

func (l *loader) fail(key string, err error) {
	l.errs = append(l.errs, fmt.Errorf("%s: %w", key, err))
}

func (l *loader) err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return errors.Join(l.errs...)
}

func (l *loader) str(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func (l *loader) required(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		l.fail(key, errors.New("не задан"))
	}
	return v
}

func (l *loader) integer(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		l.fail(key, fmt.Errorf("не число: %q", raw))
		return def
	}
	return v
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		l.fail(key, fmt.Errorf("не длительность: %q", raw))
		return def
	}
	if v <= 0 {
		l.fail(key, fmt.Errorf("должна быть положительной: %q", raw))
		return def
	}
	return v
}

// ParseLogLevel переводит значение LOG_LEVEL в уровень slog.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("неизвестный уровень: %q", s)
	}
}
