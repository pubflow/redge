package d1store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pubflow/d1http"
	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/docstore"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

const typeString = store.TypeString
const typeZSet = store.TypeZSet

type Store struct {
	client          *d1http.Client
	log             *zap.Logger
	retryMax        int
	retryMinBackoff time.Duration
	retryMaxBackoff time.Duration
}

func New(cfg *config.Config, log *zap.Logger) (*Store, error) {
	accountID, databaseID, err := cfg.D1Parts()
	if err != nil {
		return nil, err
	}
	apiToken := cfg.GetD1APIToken()
	if apiToken == "" {
		return nil, fmt.Errorf("D1_API_TOKEN is required for d1 backend")
	}
	client := d1http.New(d1http.Config{
		AccountID:  accountID,
		DatabaseID: databaseID,
		APIToken:   apiToken,
		BaseURL:    cfg.GetD1BaseURL(),
		Retry: d1http.RetryConfig{
			MaxRetries: cfg.D1RetryMax,
			MinBackoff: cfg.D1RetryMinBackoff,
			MaxBackoff: cfg.D1RetryMaxBackoff,
		},
		UserAgent: "redge/0.1 d1http/0.1",
	})
	return &Store{
		client:          client,
		log:             log,
		retryMax:        cfg.D1RetryMax,
		retryMinBackoff: cfg.D1RetryMinBackoff,
		retryMaxBackoff: cfg.D1RetryMaxBackoff,
	}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	return s.execStatements(ctx, []string{
		`
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
)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_keys_expires_at ON redge_keys (expires_at)`,
		`
CREATE TABLE IF NOT EXISTS redge_zset_members (
	db INTEGER NOT NULL,
	key TEXT NOT NULL,
	member_hash TEXT NOT NULL,
	member_value TEXT NOT NULL,
	score REAL NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (db, key, member_hash)
)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_zset_score ON redge_zset_members (db, key, score)`,
	})
}

func (s *Store) MigrateDocs(ctx context.Context) error {
	return s.execStatements(ctx, []string{
		`
CREATE TABLE IF NOT EXISTS redge_docs (
	db INTEGER NOT NULL DEFAULT 0,
	collection TEXT NOT NULL,
	id TEXT NOT NULL,
	value TEXT NOT NULL,
	search_text TEXT,
	expires_at INTEGER,
	version INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (db, collection, id)
)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_docs_collection_id ON redge_docs (db, collection, id)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_docs_expires_at ON redge_docs (expires_at)`,
		`
CREATE TABLE IF NOT EXISTS redge_doc_index_config (
	db INTEGER NOT NULL,
	collection TEXT NOT NULL,
	kind TEXT NOT NULL,
	field TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (db, collection, kind, field)
)`,
		`
CREATE TABLE IF NOT EXISTS redge_doc_indexes (
	db INTEGER NOT NULL,
	collection TEXT NOT NULL,
	field TEXT NOT NULL,
	value_hash TEXT NOT NULL,
	id TEXT NOT NULL,
	display_text TEXT,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (db, collection, field, value_hash, id)
)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_doc_indexes_lookup ON redge_doc_indexes (db, collection, field, value_hash, id)`,
	})
}

func (s *Store) execStatements(ctx context.Context, statements []string) error {
	for _, stmt := range statements {
		if err := s.execMigrationStatement(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) execMigrationStatement(ctx context.Context, stmt string) error {
	attempts := s.retryMax + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			wait := s.retryBackoff(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		if _, err := s.client.Exec(ctx, stmt); err != nil {
			lastErr = err
			if !isRetryableD1TransportError(err) || attempt == attempts-1 {
				return err
			}
			if s.log != nil {
				s.log.Warn("retrying d1 migration statement after transport error", zap.Error(err), zap.Int("attempt", attempt+1))
			}
			continue
		}
		return nil
	}
	return lastErr
}

func (s *Store) retryBackoff(attempt int) time.Duration {
	min := s.retryMinBackoff
	if min <= 0 {
		min = 200 * time.Millisecond
	}
	max := s.retryMaxBackoff
	if max <= 0 {
		max = 2 * time.Second
	}
	wait := min << (attempt - 1)
	if wait > max {
		return max
	}
	return wait
}

func isRetryableD1TransportError(err error) bool {
	if d1http.IsRetryable(err) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "forcibly closed") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "temporary failure")
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.Raw(ctx, "SELECT 1")
	return err
}

func (s *Store) Type(ctx context.Context, db int, key string) (string, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil {
		return "", err
	}
	if !found {
		return store.TypeNone, nil
	}
	return row.typ, nil
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

func (s *Store) MGet(ctx context.Context, db int, keys ...string) ([]*store.Value, error) {
	out := make([]*store.Value, len(keys))
	for i, key := range keys {
		v, err := s.Get(ctx, db, key)
		if err != nil {
			if err == store.ErrWrongType {
				continue
			}
			return nil, err
		}
		out[i] = v
	}
	return out, nil
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

func (s *Store) MSet(ctx context.Context, db int, pairs []store.KeyValue, opts store.SetOptions) error {
	for _, pair := range pairs {
		if _, _, err := s.Set(ctx, db, pair.Key, pair.Value, opts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetDel(ctx context.Context, db int, key string) (*store.Value, bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, false, err
	}
	if row.typ != typeString {
		return nil, false, store.ErrWrongType
	}
	v, err := row.value()
	if err != nil {
		return nil, false, err
	}
	n, err := s.Delete(ctx, db, key)
	if err != nil {
		return nil, false, err
	}
	return v, n > 0, nil
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

func (s *Store) Persist(ctx context.Context, db int, key string) (bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found || row.expiresAt == nil {
		return false, err
	}
	res, err := s.client.Exec(ctx, "UPDATE redge_keys SET expires_at = NULL, version = ?, updated_at = ? WHERE db = ? AND key = ?", row.version+1, unixms(time.Now()), db, key)
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

func (s *Store) ZRangeByScore(ctx context.Context, db int, key string, min, max store.ScoreBound, offset, limit int64, rev bool) ([]store.ZMember, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	if row.typ != typeZSet {
		return nil, store.ErrWrongType
	}
	where, params := scoreWhere(min, max)
	args := append([]any{db, key}, params...)
	order := "ASC"
	if rev {
		order = "DESC"
	}
	sql := `
SELECT member_value, score
FROM redge_zset_members
WHERE db = ? AND key = ?` + where + `
ORDER BY score ` + order + `, member_value ` + order
	if limit >= 0 {
		sql += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	rows, err := s.client.Raw(ctx, sql, args...)
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

func (s *Store) ZIncrBy(ctx context.Context, db int, key string, member []byte, delta float64) (float64, error) {
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
	current := 0.0
	scoreRows, err := s.client.Raw(ctx, "SELECT score FROM redge_zset_members WHERE db = ? AND key = ? AND member_hash = ? LIMIT 1", db, key, hash)
	if err != nil {
		return 0, err
	}
	if len(scoreRows) > 0 && len(scoreRows[0].Results.Rows) > 0 && len(scoreRows[0].Results.Rows[0]) > 0 {
		current = anyFloat64(scoreRows[0].Results.Rows[0][0])
	}
	out := current + delta
	if !found {
		_, err = s.client.Exec(ctx, "INSERT INTO redge_keys (db, key, type, string_value, expires_at, version, created_at, updated_at) VALUES (?, ?, ?, NULL, NULL, 1, ?, ?)", db, key, typeZSet, now, now)
		if err != nil {
			return 0, err
		}
	} else {
		_, err = s.client.Exec(ctx, "UPDATE redge_keys SET version = ?, updated_at = ? WHERE db = ? AND key = ?", row.version+1, now, db, key)
		if err != nil {
			return 0, err
		}
	}
	_, err = s.client.Exec(ctx, zsetUpsertSQL(), db, key, hash, encoded, out, now, now)
	return out, err
}

func (s *Store) ZPopMin(ctx context.Context, db int, key string, count int64) ([]store.ZMember, error) {
	return s.zpop(ctx, db, key, count, false)
}

func (s *Store) ZPopMax(ctx context.Context, db int, key string, count int64) ([]store.ZMember, error) {
	return s.zpop(ctx, db, key, count, true)
}

func (s *Store) zpop(ctx context.Context, db int, key string, count int64, max bool) ([]store.ZMember, error) {
	if count <= 0 {
		count = 1
	}
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	if row.typ != typeZSet {
		return nil, store.ErrWrongType
	}
	order := "score ASC, member ASC"
	if max {
		order = "score DESC, member DESC"
	}
	rows, err := s.client.Raw(ctx, "SELECT member, score, member_hash FROM redge_zset_members WHERE db = ? AND key = ? ORDER BY "+order+" LIMIT ?", db, key, count)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 {
		return nil, nil
	}
	out := make([]store.ZMember, 0, len(rows[0].Results.Rows))
	hashes := make([]string, 0, len(rows[0].Results.Rows))
	for _, cols := range rows[0].Results.Rows {
		if len(cols) < 3 {
			continue
		}
		member, err := base64.StdEncoding.DecodeString(d1http.String(cols[0]))
		if err != nil {
			return nil, err
		}
		out = append(out, store.ZMember{Member: member, Score: anyFloat64(cols[1])})
		hashes = append(hashes, d1http.String(cols[2]))
	}
	for _, hash := range hashes {
		if _, err := s.client.Exec(ctx, "DELETE FROM redge_zset_members WHERE db = ? AND key = ? AND member_hash = ?", db, key, hash); err != nil {
			return nil, err
		}
	}
	now := unixms(time.Now())
	if _, err := s.client.Exec(ctx, "UPDATE redge_keys SET version = ?, updated_at = ? WHERE db = ? AND key = ?", row.version+1, now, db, key); err != nil {
		return nil, err
	}
	return out, nil
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

func (s *Store) Stats(ctx context.Context, db int) (store.Stats, error) {
	rows, err := s.client.Raw(ctx, "SELECT COUNT(*) FROM redge_keys WHERE db = ? AND (expires_at IS NULL OR expires_at > ?)", db, unixms(time.Now()))
	if err != nil {
		return store.Stats{}, err
	}
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 || len(rows[0].Results.Rows[0]) == 0 {
		return store.Stats{}, nil
	}
	return store.Stats{Keys: anyInt64(rows[0].Results.Rows[0][0])}, nil
}

func (s *Store) ConfigureIndexes(ctx context.Context, db int, collection string, cfg docstore.IndexConfig) (docstore.IndexConfig, error) {
	cfg = normalizeIndexConfig(cfg)
	now := unixms(time.Now())
	if _, err := s.client.Exec(ctx, "DELETE FROM redge_doc_index_config WHERE db = ? AND collection = ?", db, collection); err != nil {
		return docstore.IndexConfig{}, err
	}
	if _, err := s.client.Exec(ctx, "DELETE FROM redge_doc_indexes WHERE db = ? AND collection = ?", db, collection); err != nil {
		return docstore.IndexConfig{}, err
	}
	for _, field := range cfg.Fields {
		if _, err := s.client.Exec(ctx, "INSERT INTO redge_doc_index_config (db, collection, kind, field, created_at) VALUES (?, ?, ?, ?, ?)", db, collection, "field", field, now); err != nil {
			return docstore.IndexConfig{}, err
		}
	}
	for _, field := range cfg.SearchFields {
		if _, err := s.client.Exec(ctx, "INSERT INTO redge_doc_index_config (db, collection, kind, field, created_at) VALUES (?, ?, ?, ?, ?)", db, collection, "search", field, now); err != nil {
			return docstore.IndexConfig{}, err
		}
	}
	docs, err := s.client.Raw(ctx, "SELECT id, value FROM redge_docs WHERE db = ? AND collection = ? AND (expires_at IS NULL OR expires_at > ?) ORDER BY id ASC", db, collection, now)
	if err != nil {
		return docstore.IndexConfig{}, err
	}
	if len(docs) > 0 {
		for _, cols := range docs[0].Results.Rows {
			if len(cols) < 2 {
				continue
			}
			id := d1http.String(cols[0])
			value := []byte(d1http.String(cols[1]))
			indexRows, searchText, err := buildDocIndexes(db, collection, id, value, cfg)
			if err != nil {
				return docstore.IndexConfig{}, err
			}
			for _, row := range indexRows {
				if _, err := s.client.Exec(ctx, "INSERT INTO redge_doc_indexes (db, collection, field, value_hash, id, display_text, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", row.db, row.collection, row.field, row.valueHash, row.id, row.displayText, row.createdAt); err != nil {
					return docstore.IndexConfig{}, err
				}
			}
			if _, err := s.client.Exec(ctx, "UPDATE redge_docs SET search_text = ? WHERE db = ? AND collection = ? AND id = ?", searchText, db, collection, id); err != nil {
				return docstore.IndexConfig{}, err
			}
		}
	}
	return cfg, nil
}

func (s *Store) GetIndexConfig(ctx context.Context, db int, collection string) (docstore.IndexConfig, error) {
	return s.getDocIndexConfig(ctx, db, collection)
}

func (s *Store) PutDoc(ctx context.Context, db int, collection, id string, value []byte, opts docstore.WriteOptions, createOnly bool) (docstore.Document, bool, error) {
	cfg, err := s.mergeDocIndexConfig(ctx, db, collection, opts)
	if err != nil {
		return docstore.Document{}, false, err
	}
	existing, found, err := s.getLiveDocRow(ctx, db, collection, id)
	if err != nil {
		return docstore.Document{}, false, err
	}
	if createOnly && found {
		return docstore.Document{}, false, docstore.ErrConflict
	}
	indexRows, searchText, err := buildDocIndexes(db, collection, id, value, cfg)
	if err != nil {
		return docstore.Document{}, false, err
	}
	now := unixms(time.Now())
	created := !found
	version := int64(1)
	createdAt := now
	var expires any
	var expiresTime *time.Time
	if found {
		version = existing.version + 1
		createdAt = existing.createdAt
		expires = nullableInt(existing.expiresAt)
		if existing.expiresAt != nil {
			t := timeFromUnixms(*existing.expiresAt)
			expiresTime = &t
		}
	}
	if opts.TTL > 0 {
		expiresMs := unixms(time.Now().Add(opts.TTL))
		expires = expiresMs
		t := timeFromUnixms(expiresMs)
		expiresTime = &t
	}
	if createOnly || !found {
		_, err = s.client.Exec(ctx, `
INSERT INTO redge_docs (db, collection, id, value, search_text, expires_at, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(db, collection, id) DO UPDATE SET
	value = excluded.value,
	search_text = excluded.search_text,
	expires_at = excluded.expires_at,
	version = redge_docs.version + 1,
	updated_at = excluded.updated_at`,
			db, collection, id, string(value), searchText, expires, version, createdAt, now)
	} else {
		_, err = s.client.Exec(ctx, `
UPDATE redge_docs
SET value = ?, search_text = ?, expires_at = ?, version = ?, updated_at = ?
WHERE db = ? AND collection = ? AND id = ?`,
			string(value), searchText, expires, version, now, db, collection, id)
	}
	if err != nil {
		return docstore.Document{}, false, err
	}
	if _, err := s.client.Exec(ctx, "DELETE FROM redge_doc_indexes WHERE db = ? AND collection = ? AND id = ?", db, collection, id); err != nil {
		return docstore.Document{}, false, err
	}
	for _, row := range indexRows {
		if _, err := s.client.Exec(ctx, "INSERT INTO redge_doc_indexes (db, collection, field, value_hash, id, display_text, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", row.db, row.collection, row.field, row.valueHash, row.id, row.displayText, row.createdAt); err != nil {
			return docstore.Document{}, false, err
		}
	}
	return docstore.Document{Collection: collection, ID: id, Value: value, Version: version, CreatedAt: timeFromUnixms(createdAt), UpdatedAt: timeFromUnixms(now), ExpiresAt: expiresTime}, created, nil
}

func (s *Store) GetDoc(ctx context.Context, db int, collection, id string) (docstore.Document, error) {
	row, found, err := s.getLiveDocRow(ctx, db, collection, id)
	if err != nil {
		return docstore.Document{}, err
	}
	if !found {
		return docstore.Document{}, docstore.ErrNotFound
	}
	return row.document(), nil
}

func (s *Store) DeleteDoc(ctx context.Context, db int, collection, id string) (bool, error) {
	if _, err := s.client.Exec(ctx, "DELETE FROM redge_doc_indexes WHERE db = ? AND collection = ? AND id = ?", db, collection, id); err != nil {
		return false, err
	}
	res, err := s.client.Exec(ctx, "DELETE FROM redge_docs WHERE db = ? AND collection = ? AND id = ?", db, collection, id)
	if err != nil {
		return false, err
	}
	return res != nil && res.Meta.ChangesInt64() > 0, nil
}

func (s *Store) QueryDocs(ctx context.Context, db int, collection string, opts docstore.QueryOptions) (docstore.QueryResult, error) {
	limit := normalizeDocLimit(opts.Limit)
	if opts.Where != nil {
		if !strings.EqualFold(opts.Where.Op, "eq") {
			return docstore.QueryResult{}, fmt.Errorf("%w: only eq filters are supported", docstore.ErrInvalidRequest)
		}
		cfg, err := s.GetIndexConfig(ctx, db, collection)
		if err != nil {
			return docstore.QueryResult{}, err
		}
		if !stringInSlice(cfg.Fields, opts.Where.Field) {
			return docstore.QueryResult{}, fmt.Errorf("%w: %s", docstore.ErrInvalidIndex, opts.Where.Field)
		}
		return s.queryDocsByIndex(ctx, db, collection, opts, limit)
	}
	sql := "SELECT collection, id, value, expires_at, version, created_at, updated_at FROM redge_docs WHERE db = ? AND collection = ? AND id > ? AND (expires_at IS NULL OR expires_at > ?)"
	args := []any{db, collection, opts.Cursor, unixms(time.Now())}
	if opts.Search != "" {
		sql += " AND search_text LIKE ? ESCAPE '|'"
		args = append(args, "%"+escapeLike(opts.Search)+"%")
	}
	sql += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.client.Raw(ctx, sql, args...)
	if err != nil {
		return docstore.QueryResult{}, err
	}
	return docQueryResult(parseDocRows(rows), limit), nil
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

type docRow struct {
	collection string
	id         string
	value      string
	expiresAt  *int64
	version    int64
	createdAt  int64
	updatedAt  int64
}

type docIndexRow struct {
	db          int
	collection  string
	field       string
	valueHash   string
	id          string
	displayText string
	createdAt   int64
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

func (s *Store) getLiveDocRow(ctx context.Context, db int, collection, id string) (*docRow, bool, error) {
	rows, err := s.client.Raw(ctx, `
SELECT collection, id, value, expires_at, version, created_at, updated_at
FROM redge_docs
WHERE db = ? AND collection = ? AND id = ?
LIMIT 1`, db, collection, id)
	if err != nil {
		return nil, false, err
	}
	parsed := parseDocRows(rows)
	if len(parsed) == 0 {
		return nil, false, nil
	}
	row := parsed[0]
	if row.expiresAt != nil && *row.expiresAt <= unixms(time.Now()) {
		_, _ = s.DeleteDoc(ctx, db, collection, id)
		return nil, false, nil
	}
	return &row, true, nil
}

func (s *Store) getDocIndexConfig(ctx context.Context, db int, collection string) (docstore.IndexConfig, error) {
	rows, err := s.client.Raw(ctx, "SELECT kind, field FROM redge_doc_index_config WHERE db = ? AND collection = ? ORDER BY kind ASC, field ASC", db, collection)
	if err != nil {
		return docstore.IndexConfig{}, err
	}
	cfg := docstore.IndexConfig{}
	if len(rows) > 0 {
		for _, cols := range rows[0].Results.Rows {
			if len(cols) < 2 {
				continue
			}
			switch d1http.String(cols[0]) {
			case "field":
				cfg.Fields = append(cfg.Fields, d1http.String(cols[1]))
			case "search":
				cfg.SearchFields = append(cfg.SearchFields, d1http.String(cols[1]))
			}
		}
	}
	return normalizeIndexConfig(cfg), nil
}

func (s *Store) mergeDocIndexConfig(ctx context.Context, db int, collection string, opts docstore.WriteOptions) (docstore.IndexConfig, error) {
	cfg, err := s.getDocIndexConfig(ctx, db, collection)
	if err != nil {
		return docstore.IndexConfig{}, err
	}
	next := docstore.IndexConfig{
		Fields:       append([]string(nil), cfg.Fields...),
		SearchFields: append([]string(nil), cfg.SearchFields...),
	}
	next.Fields = append(next.Fields, opts.IndexedFields...)
	next.SearchFields = append(next.SearchFields, opts.SearchFields...)
	next = normalizeIndexConfig(next)
	if slicesEqual(cfg.Fields, next.Fields) && slicesEqual(cfg.SearchFields, next.SearchFields) {
		return next, nil
	}
	now := unixms(time.Now())
	for _, field := range difference(next.Fields, cfg.Fields) {
		if _, err := s.client.Exec(ctx, "INSERT INTO redge_doc_index_config (db, collection, kind, field, created_at) VALUES (?, ?, ?, ?, ?)", db, collection, "field", field, now); err != nil {
			return docstore.IndexConfig{}, err
		}
	}
	for _, field := range difference(next.SearchFields, cfg.SearchFields) {
		if _, err := s.client.Exec(ctx, "INSERT INTO redge_doc_index_config (db, collection, kind, field, created_at) VALUES (?, ?, ?, ?, ?)", db, collection, "search", field, now); err != nil {
			return docstore.IndexConfig{}, err
		}
	}
	return next, nil
}

func (s *Store) queryDocsByIndex(ctx context.Context, db int, collection string, opts docstore.QueryOptions, limit int) (docstore.QueryResult, error) {
	hash := indexValueHash(opts.Where.Value)
	rows, err := s.client.Raw(ctx, `
SELECT id
FROM redge_doc_indexes
WHERE db = ? AND collection = ? AND field = ? AND value_hash = ? AND id > ?
ORDER BY id ASC
LIMIT ?`, db, collection, opts.Where.Field, hash, opts.Cursor, limit+1)
	if err != nil {
		return docstore.QueryResult{}, err
	}
	if len(rows) == 0 || len(rows[0].Results.Rows) == 0 {
		return docstore.QueryResult{}, nil
	}
	ordered := make([]docRow, 0, len(rows[0].Results.Rows))
	for _, cols := range rows[0].Results.Rows {
		if len(cols) == 0 {
			continue
		}
		id := d1http.String(cols[0])
		row, found, err := s.getLiveDocRow(ctx, db, collection, id)
		if err != nil {
			return docstore.QueryResult{}, err
		}
		if !found {
			continue
		}
		if opts.Search != "" && !strings.Contains(row.value, opts.Search) {
			continue
		}
		ordered = append(ordered, *row)
	}
	return docQueryResult(ordered, limit), nil
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

func (r *docRow) document() docstore.Document {
	var expiresAt *time.Time
	if r.expiresAt != nil {
		t := timeFromUnixms(*r.expiresAt)
		expiresAt = &t
	}
	return docstore.Document{
		Collection: r.collection,
		ID:         r.id,
		Value:      []byte(r.value),
		Version:    r.version,
		CreatedAt:  timeFromUnixms(r.createdAt),
		UpdatedAt:  timeFromUnixms(r.updatedAt),
		ExpiresAt:  expiresAt,
	}
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

func parseDocRows(rows []d1http.RawResult) []docRow {
	out := make([]docRow, 0)
	if len(rows) == 0 {
		return out
	}
	for _, cols := range rows[0].Results.Rows {
		if len(cols) < 7 {
			continue
		}
		row := docRow{
			collection: d1http.String(cols[0]),
			id:         d1http.String(cols[1]),
			value:      d1http.String(cols[2]),
			version:    anyInt64(cols[4]),
			createdAt:  anyInt64(cols[5]),
			updatedAt:  anyInt64(cols[6]),
		}
		if cols[3] != nil {
			v := anyInt64(cols[3])
			row.expiresAt = &v
		}
		out = append(out, row)
	}
	return out
}

func docQueryResult(rows []docRow, limit int) docstore.QueryResult {
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	docs := make([]docstore.Document, 0, len(rows))
	next := ""
	for i := range rows {
		docs = append(docs, rows[i].document())
		next = rows[i].id
	}
	if !hasMore {
		next = ""
	}
	return docstore.QueryResult{Documents: docs, NextCursor: next, HasMore: hasMore}
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

func buildDocIndexes(db int, collection, id string, value []byte, cfg docstore.IndexConfig) ([]docIndexRow, string, error) {
	var obj map[string]any
	if err := json.Unmarshal(value, &obj); err != nil {
		return nil, "", fmt.Errorf("%w: document must be a JSON object", docstore.ErrInvalidRequest)
	}
	now := unixms(time.Now())
	rows := make([]docIndexRow, 0, len(cfg.Fields))
	for _, field := range cfg.Fields {
		raw, ok := obj[field]
		if !ok {
			continue
		}
		text, ok := normalizeScalar(raw)
		if !ok {
			continue
		}
		rows = append(rows, docIndexRow{
			db: db, collection: collection, field: field, valueHash: indexValueHash(text),
			id: id, displayText: text, createdAt: now,
		})
	}
	var searchParts []string
	for _, field := range cfg.SearchFields {
		if raw, ok := obj[field]; ok {
			if text, ok := raw.(string); ok && text != "" {
				searchParts = append(searchParts, text)
			}
		}
	}
	return rows, strings.Join(searchParts, " "), nil
}

func normalizeScalar(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	case nil:
		return "null", true
	default:
		return "", false
	}
}

func indexValueHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeIndexConfig(cfg docstore.IndexConfig) docstore.IndexConfig {
	cfg.Fields = uniqueValidFields(cfg.Fields)
	cfg.SearchFields = uniqueValidFields(cfg.SearchFields)
	return cfg
}

func uniqueValidFields(fields []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if !validDocName(field) {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func validDocName(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizeDocLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func stringInSlice(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func difference(next, old []string) []string {
	have := make(map[string]struct{}, len(old))
	for _, item := range old {
		have[item] = struct{}{}
	}
	var out []string
	for _, item := range next {
		if _, ok := have[item]; !ok {
			out = append(out, item)
		}
	}
	return out
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `|`, `||`)
	value = strings.ReplaceAll(value, `%`, `|%`)
	value = strings.ReplaceAll(value, `_`, `|_`)
	return value
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
