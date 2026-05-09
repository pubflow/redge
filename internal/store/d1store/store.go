package d1store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pubflow/d1http"
	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

const typeString = "string"
const typeZSet = "zset"

type Store struct {
	client *d1http.Client
	log    *zap.Logger
}

func New(cfg *config.Config, log *zap.Logger) (*Store, error) {
	accountID, databaseID, err := cfg.D1Parts()
	if err != nil {
		return nil, err
	}
	if cfg.D1APIToken == "" {
		return nil, fmt.Errorf("D1_API_TOKEN is required for d1 backend")
	}
	client := d1http.New(d1http.Config{
		AccountID:  accountID,
		DatabaseID: databaseID,
		APIToken:   cfg.D1APIToken,
		BaseURL:    cfg.D1BaseURL,
		Retry: d1http.RetryConfig{
			MaxRetries: cfg.D1RetryMax,
			MinBackoff: cfg.D1RetryMinBackoff,
			MaxBackoff: cfg.D1RetryMaxBackoff,
		},
		UserAgent: "redge/0.1 d1http/0.1",
	})
	return &Store{client: client, log: log}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.client.BatchRaw(ctx, []d1http.Statement{
		{SQL: `
CREATE TABLE IF NOT EXISTS redge_keys (
	db INTEGER NOT NULL DEFAULT 0,
	key TEXT NOT NULL,
	type TEXT NOT NULL,
	string_value TEXT,
	expires_at INTEGER,
	version INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (db, key)
)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_redge_keys_expires_at ON redge_keys (expires_at)`},
		{SQL: `
CREATE TABLE IF NOT EXISTS redge_zset_members (
	db INTEGER NOT NULL,
	key TEXT NOT NULL,
	member_hash TEXT NOT NULL,
	member_value TEXT NOT NULL,
	score REAL NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (db, key, member_hash)
)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_redge_zset_score ON redge_zset_members (db, key, score)`},
	})
	return err
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.Raw(ctx, "SELECT 1")
	return err
}

func (s *Store) Get(ctx context.Context, db int, key string) (*store.Value, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	if row.typ != typeString {
		return nil, store.ErrWrongType
	}
	return row.value()
}

func (s *Store) Set(ctx context.Context, db int, key string, value []byte, opts store.SetOptions) (*store.Value, bool, error) {
	old, found, err := s.getLiveRow(ctx, db, key)
	if err != nil {
		return nil, false, err
	}
	if opts.NX && found {
		v, _ := old.value()
		return v, false, nil
	}
	if opts.XX && !found {
		return nil, false, nil
	}
	if found && old.typ != typeString {
		return nil, false, store.ErrWrongType
	}
	now := unixms(time.Now())
	var expires any
	if opts.KeepTTL && found {
		expires = nullableInt(old.expiresAt)
	}
	if opts.TTL > 0 {
		expires = unixms(time.Now().Add(opts.TTL))
	}
	encoded := base64.StdEncoding.EncodeToString(value)
	version := int64(1)
	created := now
	if found {
		version = old.version + 1
		created = old.createdAt
	}
	if opts.NX {
		res, err := s.client.Exec(ctx, `
INSERT OR IGNORE INTO redge_keys (db, key, type, string_value, expires_at, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			db, key, typeString, encoded, expires, version, created, now,
		)
		if err != nil {
			return nil, false, err
		}
		return nil, res != nil && res.Meta.ChangesInt64() > 0, nil
	}
	if found {
		res, err := s.client.Exec(ctx, `
UPDATE redge_keys
SET type = ?, string_value = ?, expires_at = ?, version = ?, updated_at = ?
WHERE db = ? AND key = ? AND version = ?`,
			typeString, encoded, expires, version, now, db, key, old.version,
		)
		if err != nil {
			return nil, false, err
		}
		if res == nil || res.Meta.ChangesInt64() == 0 {
			return nil, false, fmt.Errorf("D1 optimistic write conflict")
		}
		v, err := old.value()
		return v, true, err
	}
	_, err = s.client.Exec(ctx, `
INSERT INTO redge_keys (db, key, type, string_value, expires_at, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(db, key) DO UPDATE SET
	type = excluded.type,
	string_value = excluded.string_value,
	expires_at = excluded.expires_at,
	version = excluded.version,
	updated_at = excluded.updated_at`,
		db, key, typeString, encoded, expires, version, created, now,
	)
	if err != nil {
		return nil, false, err
	}
	return nil, true, nil
}

func (s *Store) Delete(ctx context.Context, db int, keys ...string) (int64, error) {
	var total int64
	for _, key := range keys {
		if _, err := s.client.Exec(ctx, "DELETE FROM redge_zset_members WHERE db = ? AND key = ?", db, key); err != nil {
			return total, err
		}
		res, err := s.client.Exec(ctx, "DELETE FROM redge_keys WHERE db = ? AND key = ?", db, key)
		if err != nil {
			return total, err
		}
		if res != nil {
			total += res.Meta.ChangesInt64()
		}
	}
	return total, nil
}

func (s *Store) Exists(ctx context.Context, db int, keys ...string) (int64, error) {
	var total int64
	now := unixms(time.Now())
	for _, key := range keys {
		rows, err := s.client.Raw(ctx, "SELECT 1 FROM redge_keys WHERE db = ? AND key = ? AND (expires_at IS NULL OR expires_at > ?) LIMIT 1", db, key, now)
		if err != nil {
			return total, err
		}
		if len(rows) > 0 && len(rows[0].Results.Rows) > 0 {
			total++
		}
	}
	return total, nil
}

func (s *Store) Expire(ctx context.Context, db int, key string, ttl time.Duration) (bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return false, err
	}
	res, err := s.client.Exec(ctx, "UPDATE redge_keys SET expires_at = ?, version = ?, updated_at = ? WHERE db = ? AND key = ?", unixms(time.Now().Add(ttl)), row.version+1, unixms(time.Now()), db, key)
	if err != nil {
		return false, err
	}
	return res != nil && res.Meta.ChangesInt64() > 0, nil
}

func (s *Store) TTL(ctx context.Context, db int, key string) (time.Duration, bool, bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, false, false, err
	}
	if row.expiresAt == nil {
		return 0, true, false, nil
	}
	return time.Until(timeFromUnixms(*row.expiresAt)), true, true, nil
}

func (s *Store) IncrBy(ctx context.Context, db int, key string, delta int64) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil {
		return 0, err
	}
	current := int64(0)
	if found {
		if row.typ != typeString {
			return 0, store.ErrWrongType
		}
		v, err := row.value()
		if err != nil {
			return 0, err
		}
		current, err = strconv.ParseInt(string(v.Data), 10, 64)
		if err != nil {
			return 0, store.ErrNotInt
		}
	}
	next := current + delta
	oldVersion := int64(0)
	var expires any
	created := unixms(time.Now())
	if found {
		oldVersion = row.version
		expires = nullableInt(row.expiresAt)
		created = row.createdAt
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(next, 10)))
	now := unixms(time.Now())
	if found {
		res, err := s.client.Exec(ctx, `
UPDATE redge_keys
SET string_value = ?, version = ?, updated_at = ?
WHERE db = ? AND key = ? AND version = ?`,
			encoded, oldVersion+1, now, db, key, oldVersion,
		)
		if err != nil {
			return 0, err
		}
		if res == nil || res.Meta.ChangesInt64() == 0 {
			return 0, fmt.Errorf("D1 optimistic write conflict")
		}
		return next, nil
	}
	_, err = s.client.Exec(ctx, `
INSERT INTO redge_keys (db, key, type, string_value, expires_at, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		db, key, typeString, encoded, expires, int64(1), created, now,
	)
	return next, err
}

func (s *Store) ZAdd(ctx context.Context, db int, key string, score float64, member []byte) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil {
		return 0, err
	}
	if found && row.typ != typeZSet {
		return 0, store.ErrWrongType
	}
	hash := memberHash(member)
	encoded := base64.StdEncoding.EncodeToString(member)
	now := unixms(time.Now())
	var existing int64
	rows, err := s.client.Raw(ctx, "SELECT 1 FROM redge_zset_members WHERE db = ? AND key = ? AND member_hash = ? LIMIT 1", db, key, hash)
	if err != nil {
		return 0, err
	}
	if len(rows) > 0 && len(rows[0].Results.Rows) > 0 {
		existing = 1
	}
	if !found {
		_, err = s.client.Exec(ctx, "INSERT INTO redge_keys (db, key, type, string_value, expires_at, version, created_at, updated_at) VALUES (?, ?, ?, NULL, NULL, 1, ?, ?)", db, key, typeZSet, now, now)
		if err != nil {
			return 0, err
		}
		_, err = s.client.Exec(ctx, zsetUpsertSQL(), db, key, hash, encoded, score, now, now)
	} else {
		_, err = s.client.Exec(ctx, "UPDATE redge_keys SET version = ?, updated_at = ? WHERE db = ? AND key = ?", row.version+1, now, db, key)
		if err != nil {
			return 0, err
		}
		_, err = s.client.Exec(ctx, zsetUpsertSQL(), db, key, hash, encoded, score, now, now)
	}
	if err != nil {
		return 0, err
	}
	if existing == 0 {
		return 1, nil
	}
	return 0, nil
}

func (s *Store) ZCard(ctx context.Context, db int, key string) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.typ != typeZSet {
		return 0, store.ErrWrongType
	}
	rows, err := s.client.Raw(ctx, "SELECT COUNT(*) FROM redge_zset_members WHERE db = ? AND key = ?", db, key)
	if err != nil {
		return 0, err
	}
	return firstInt(rows), nil
}

func (s *Store) ZRem(ctx context.Context, db int, key string, members ...[]byte) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.typ != typeZSet {
		return 0, store.ErrWrongType
	}
	var total int64
	for _, member := range members {
		res, err := s.client.Exec(ctx, "DELETE FROM redge_zset_members WHERE db = ? AND key = ? AND member_hash = ?", db, key, memberHash(member))
		if err != nil {
			return total, err
		}
		if res != nil {
			total += res.Meta.ChangesInt64()
		}
	}
	if total > 0 {
		_, _ = s.client.Exec(ctx, "UPDATE redge_keys SET version = ?, updated_at = ? WHERE db = ? AND key = ?", row.version+1, unixms(time.Now()), db, key)
	}
	return total, nil
}

func (s *Store) ZRemRangeByScore(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.typ != typeZSet {
		return 0, store.ErrWrongType
	}
	where, params := scoreWhere(min, max)
	args := append([]any{db, key}, params...)
	res, err := s.client.Exec(ctx, "DELETE FROM redge_zset_members WHERE db = ? AND key = ?"+where, args...)
	if err != nil {
		return 0, err
	}
	changed := int64(0)
	if res != nil {
		changed = res.Meta.ChangesInt64()
	}
	if changed > 0 {
		_, _ = s.client.Exec(ctx, "UPDATE redge_keys SET version = ?, updated_at = ? WHERE db = ? AND key = ?", row.version+1, unixms(time.Now()), db, key)
	}
	return changed, nil
}

func (s *Store) ZRange(ctx context.Context, db int, key string, start, stop int64) ([]store.ZMember, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	if row.typ != typeZSet {
		return nil, store.ErrWrongType
	}
	start, stop, ok, err := s.normalizeRange(ctx, db, key, start, stop)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := s.client.Raw(ctx, `
SELECT member_value, score
FROM redge_zset_members
WHERE db = ? AND key = ?
ORDER BY score ASC, member_value ASC
LIMIT ? OFFSET ?`, db, key, stop-start+1, start)
	if err != nil {
		return nil, err
	}
	out := make([]store.ZMember, 0)
	if len(rows) == 0 {
		return out, nil
	}
	for _, cols := range rows[0].Results.Rows {
		if len(cols) < 2 {
			continue
		}
		member, err := base64.StdEncoding.DecodeString(d1http.String(cols[0]))
		if err != nil {
			return nil, err
		}
		out = append(out, store.ZMember{Member: member, Score: anyFloat64(cols[1])})
	}
	return out, nil
}

func (s *Store) ZScore(ctx context.Context, db int, key string, member []byte) (float64, bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, false, err
	}
	if row.typ != typeZSet {
		return 0, false, store.ErrWrongType
	}
	rows, err := s.client.Raw(ctx, "SELECT score FROM redge_zset_members WHERE db = ? AND key = ? AND member_hash = ? LIMIT 1", db, key, memberHash(member))
	if err != nil {
		return 0, false, err
	}
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 || len(rows[0].Results.Rows[0]) == 0 {
		return 0, false, nil
	}
	return anyFloat64(rows[0].Results.Rows[0][0]), true, nil
}

func (s *Store) ZCount(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.typ != typeZSet {
		return 0, store.ErrWrongType
	}
	where, params := scoreWhere(min, max)
	args := append([]any{db, key}, params...)
	rows, err := s.client.Raw(ctx, "SELECT COUNT(*) FROM redge_zset_members WHERE db = ? AND key = ?"+where, args...)
	if err != nil {
		return 0, err
	}
	return firstInt(rows), nil
}

func (s *Store) Scan(ctx context.Context, db int, cursor string, pattern string, count int) (store.ScanResult, error) {
	if count <= 0 {
		count = 10
	}
	if count > 1000 {
		count = 1000
	}
	last := ""
	if cursor != "0" {
		last = cursor
	}
	sql := "SELECT key FROM redge_keys WHERE db = ? AND key > ? AND (expires_at IS NULL OR expires_at > ?)"
	params := []any{db, last, unixms(time.Now())}
	if pattern != "" && pattern != "*" {
		sql += " AND key LIKE ? ESCAPE '\\'"
		params = append(params, redisPatternToLike(pattern))
	}
	sql += " ORDER BY key ASC LIMIT ?"
	params = append(params, count)
	rows, err := s.client.Raw(ctx, sql, params...)
	if err != nil {
		return store.ScanResult{}, err
	}
	keys := make([]string, 0)
	next := "0"
	if len(rows) > 0 {
		for _, cols := range rows[0].Results.Rows {
			if len(cols) == 0 {
				continue
			}
			key := d1http.String(cols[0])
			keys = append(keys, key)
			next = key
		}
	}
	if len(keys) < count {
		next = "0"
	}
	return store.ScanResult{Cursor: next, Keys: keys}, nil
}

func (s *Store) CleanupExpired(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	rows, err := s.client.Raw(ctx, "SELECT db, key FROM redge_keys WHERE expires_at IS NOT NULL AND expires_at <= ? LIMIT ?", unixms(time.Now()), limit)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 {
		return 0, nil
	}
	var total int64
	for _, cols := range rows[0].Results.Rows {
		if len(cols) < 2 {
			continue
		}
		deadDB := int(anyInt64(cols[0]))
		deadKey := d1http.String(cols[1])
		n, err := s.Delete(ctx, deadDB, deadKey)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (s *Store) Stats(ctx context.Context) (store.Stats, error) {
	rows, err := s.client.Raw(ctx, "SELECT COUNT(*) FROM redge_keys WHERE expires_at IS NULL OR expires_at > ?", unixms(time.Now()))
	if err != nil {
		return store.Stats{}, err
	}
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 || len(rows[0].Results.Rows[0]) == 0 {
		return store.Stats{}, nil
	}
	return store.Stats{Keys: anyInt64(rows[0].Results.Rows[0][0])}, nil
}

func (s *Store) Close() error { return nil }

type keyRow struct {
	db          int
	key         string
	typ         string
	stringValue string
	expiresAt   *int64
	version     int64
	createdAt   int64
	updatedAt   int64
}

func (s *Store) getLiveRow(ctx context.Context, db int, key string) (*keyRow, bool, error) {
	rows, err := s.client.Raw(ctx, `
SELECT db, key, type, string_value, expires_at, version, created_at, updated_at
FROM redge_keys
WHERE db = ? AND key = ?
LIMIT 1`, db, key)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 {
		return nil, false, nil
	}
	row := parseKeyRow(rows[0].Results.Rows[0])
	if row.expiresAt != nil && *row.expiresAt <= unixms(time.Now()) {
		_, _ = s.Delete(ctx, db, key)
		return nil, false, nil
	}
	return row, true, nil
}

func (r *keyRow) value() (*store.Value, error) {
	data, err := base64.StdEncoding.DecodeString(r.stringValue)
	if err != nil {
		return nil, err
	}
	var expiresAt *time.Time
	if r.expiresAt != nil {
		t := timeFromUnixms(*r.expiresAt)
		expiresAt = &t
	}
	return &store.Value{Type: r.typ, Data: data, ExpiresAt: expiresAt, Version: r.version}, nil
}

func parseKeyRow(cols []any) *keyRow {
	row := &keyRow{}
	if len(cols) > 0 {
		row.db = int(anyInt64(cols[0]))
	}
	if len(cols) > 1 {
		row.key = d1http.String(cols[1])
	}
	if len(cols) > 2 {
		row.typ = d1http.String(cols[2])
	}
	if len(cols) > 3 {
		row.stringValue = d1http.String(cols[3])
	}
	if len(cols) > 4 && cols[4] != nil {
		v := anyInt64(cols[4])
		row.expiresAt = &v
	}
	if len(cols) > 5 {
		row.version = anyInt64(cols[5])
	}
	if len(cols) > 6 {
		row.createdAt = anyInt64(cols[6])
	}
	if len(cols) > 7 {
		row.updatedAt = anyInt64(cols[7])
	}
	return row
}

func (s *Store) normalizeRange(ctx context.Context, db int, key string, start, stop int64) (int64, int64, bool, error) {
	if start < 0 || stop < 0 {
		rows, err := s.client.Raw(ctx, "SELECT COUNT(*) FROM redge_zset_members WHERE db = ? AND key = ?", db, key)
		if err != nil {
			return 0, 0, false, err
		}
		n := firstInt(rows)
		if start < 0 {
			start = n + start
		}
		if stop < 0 {
			stop = n + stop
		}
	}
	if start < 0 {
		start = 0
	}
	if stop < 0 || stop < start {
		return 0, 0, false, nil
	}
	return start, stop, true, nil
}

func memberHash(member []byte) string {
	sum := sha256.Sum256(member)
	return hex.EncodeToString(sum[:])
}

func zsetUpsertSQL() string {
	return `
INSERT INTO redge_zset_members (db, key, member_hash, member_value, score, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(db, key, member_hash) DO UPDATE SET
	member_value = excluded.member_value,
	score = excluded.score,
	updated_at = excluded.updated_at`
}

func scoreWhere(min, max store.ScoreBound) (string, []any) {
	parts := []string{}
	params := []any{}
	if min.Infinite > 0 {
		parts = append(parts, "1 = 0")
	} else if min.Infinite == 0 {
		if min.Exclusive {
			parts = append(parts, "score > ?")
		} else {
			parts = append(parts, "score >= ?")
		}
		params = append(params, min.Value)
	}
	if max.Infinite < 0 {
		parts = append(parts, "1 = 0")
	} else if max.Infinite == 0 {
		if max.Exclusive {
			parts = append(parts, "score < ?")
		} else {
			parts = append(parts, "score <= ?")
		}
		params = append(params, max.Value)
	}
	if len(parts) == 0 {
		return "", params
	}
	return " AND " + strings.Join(parts, " AND "), params
}

func redisPatternToLike(pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteByte('%')
		case '?':
			b.WriteByte('_')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstInt(rows []d1http.RawResult) int64 {
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 || len(rows[0].Results.Rows[0]) == 0 {
		return 0
	}
	return anyInt64(rows[0].Results.Rows[0][0])
}

func unixms(t time.Time) int64 {
	return t.UnixNano() / int64(time.Millisecond)
}

func timeFromUnixms(ms int64) time.Time {
	return time.Unix(0, ms*int64(time.Millisecond))
}

func nullableInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func anyInt64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		if math.Trunc(x) == x {
			return int64(x)
		}
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func anyFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		n, _ := strconv.ParseFloat(x, 64)
		return n
	}
	return 0
}
