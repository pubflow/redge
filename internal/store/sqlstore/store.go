package sqlstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const typeString = store.TypeString
const typeZSet = store.TypeZSet

type KeyRow struct {
	DB          int        `gorm:"column:db;primaryKey;autoIncrement:false"`
	Key         string     `gorm:"column:key;primaryKey;size:768"`
	Type        string     `gorm:"column:type;not null;size:32;index"`
	StringValue []byte     `gorm:"column:string_value"`
	ExpiresAt   *time.Time `gorm:"column:expires_at;index"`
	Version     int64      `gorm:"column:version;not null;default:1"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (KeyRow) TableName() string { return "redge_keys" }

type ZSetMemberRow struct {
	DB         int       `gorm:"column:db;primaryKey;autoIncrement:false"`
	Key        string    `gorm:"column:key;primaryKey;size:768;index:idx_redge_zset_score,priority:1"`
	MemberHash string    `gorm:"column:member_hash;primaryKey;size:64"`
	Member     []byte    `gorm:"column:member;not null"`
	Score      float64   `gorm:"column:score;not null;index:idx_redge_zset_score,priority:2"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (ZSetMemberRow) TableName() string { return "redge_zset_members" }

type Store struct {
	conn *database.Connection
	db   *gorm.DB
	log  *zap.Logger
}

func New(conn *database.Connection, log *zap.Logger) (*Store, error) {
	return &Store{conn: conn, db: conn.DB, log: log}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if err := s.db.WithContext(ctx).AutoMigrate(&KeyRow{}, &ZSetMemberRow{}); err != nil {
		return err
	}
	if s.conn.IsSQLiteLike() {
		_ = s.db.Exec("PRAGMA journal_mode=WAL").Error
		_ = s.db.Exec("PRAGMA busy_timeout=5000").Error
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func (s *Store) Type(ctx context.Context, db int, key string) (string, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil {
		return "", err
	}
	if !found {
		return store.TypeNone, nil
	}
	return row.Type, nil
}

func (s *Store) Get(ctx context.Context, db int, key string) (*store.Value, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	if row.Type != typeString {
		return nil, store.ErrWrongType
	}
	return toValue(row), nil
}

func (s *Store) Set(ctx context.Context, db int, key string, value []byte, opts store.SetOptions) (*store.Value, bool, error) {
	var old *KeyRow
	var wrote bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, found, err := s.getLiveRowTx(ctx, tx, db, key, true)
		if err != nil {
			return err
		}
		if opts.NX && found {
			old = row
			return nil
		}
		if opts.XX && !found {
			return nil
		}
		if found && row.Type != typeString {
			return store.ErrWrongType
		}
		old = row
		now := time.Now()
		var expiresAt *time.Time
		if opts.KeepTTL && found {
			expiresAt = row.ExpiresAt
		}
		if opts.TTL > 0 {
			t := now.Add(opts.TTL)
			expiresAt = &t
		}
		wrote = true
		next := &KeyRow{
			DB: db, Key: key, Type: typeString, StringValue: value,
			ExpiresAt: expiresAt, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if found {
			next.Version = row.Version + 1
			next.CreatedAt = row.CreatedAt
			return tx.Model(&KeyRow{}).
				Where("db = ? AND key = ?", db, key).
				Updates(map[string]any{
					"type":         typeString,
					"string_value": value,
					"expires_at":   expiresAt,
					"version":      next.Version,
					"updated_at":   now,
				}).Error
		}
		return tx.Create(next).Error
	})
	if err != nil {
		return nil, false, err
	}
	return toValue(old), wrote, nil
}

func (s *Store) Delete(ctx context.Context, db int, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("db = ? AND key IN ?", db, keys).Delete(&ZSetMemberRow{}).Error; err != nil {
			return err
		}
		res := tx.Where("db = ? AND key IN ?", db, keys).Delete(&KeyRow{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

func (s *Store) Exists(ctx context.Context, db int, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var n int64
	now := time.Now()
	err := s.db.WithContext(ctx).Model(&KeyRow{}).
		Where("db = ? AND key IN ? AND (expires_at IS NULL OR expires_at > ?)", db, keys, now).
		Count(&n).Error
	return n, err
}

func (s *Store) Expire(ctx context.Context, db int, key string, ttl time.Duration) (bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return false, err
	}
	expiresAt := time.Now().Add(ttl)
	err = s.db.WithContext(ctx).Model(row).Updates(map[string]any{
		"expires_at": expiresAt,
		"version":    row.Version + 1,
	}).Error
	return err == nil, err
}

func (s *Store) TTL(ctx context.Context, db int, key string) (time.Duration, bool, bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, false, false, err
	}
	if row.ExpiresAt == nil {
		return 0, true, false, nil
	}
	return time.Until(*row.ExpiresAt), true, true, nil
}

func (s *Store) IncrBy(ctx context.Context, db int, key string, delta int64) (int64, error) {
	var out int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, found, err := s.getLiveRowTx(ctx, tx, db, key, true)
		if err != nil {
			return err
		}
		current := int64(0)
		if found {
			if row.Type != typeString {
				return store.ErrWrongType
			}
			current, err = strconv.ParseInt(string(row.StringValue), 10, 64)
			if err != nil {
				return store.ErrNotInt
			}
		}
		out = current + delta
		now := time.Now()
		next := &KeyRow{DB: db, Key: key, Type: typeString, StringValue: []byte(strconv.FormatInt(out, 10)), Version: 1, CreatedAt: now, UpdatedAt: now}
		if found {
			next.Version = row.Version + 1
			next.CreatedAt = row.CreatedAt
			next.ExpiresAt = row.ExpiresAt
			return tx.Model(&KeyRow{}).
				Where("db = ? AND key = ?", db, key).
				Updates(map[string]any{
					"string_value": next.StringValue,
					"version":      next.Version,
					"updated_at":   now,
				}).Error
		}
		return tx.Create(next).Error
	})
	return out, err
}

func (s *Store) ZAdd(ctx context.Context, db int, key string, score float64, member []byte) (int64, error) {
	hash := memberHash(member)
	var added int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		keyRow, found, err := s.getLiveRowTx(ctx, tx, db, key, true)
		if err != nil {
			return err
		}
		now := time.Now()
		if found && keyRow.Type != typeZSet {
			return store.ErrWrongType
		}
		if !found {
			if err := tx.Create(&KeyRow{DB: db, Key: key, Type: typeZSet, Version: 1, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(keyRow).Updates(map[string]any{"version": keyRow.Version + 1, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		var existing ZSetMemberRow
		err = tx.Where("db = ? AND key = ? AND member_hash = ?", db, key, hash).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			added = 1
			return tx.Create(&ZSetMemberRow{DB: db, Key: key, MemberHash: hash, Member: member, Score: score, CreatedAt: now, UpdatedAt: now}).Error
		}
		if err != nil {
			return err
		}
		return tx.Model(&existing).Updates(map[string]any{"score": score, "member": member, "updated_at": now}).Error
	})
	return added, err
}

func (s *Store) ZCard(ctx context.Context, db int, key string) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.Type != typeZSet {
		return 0, store.ErrWrongType
	}
	var n int64
	err = s.db.WithContext(ctx).Model(&ZSetMemberRow{}).Where("db = ? AND key = ?", db, key).Count(&n).Error
	return n, err
}

func (s *Store) ZRem(ctx context.Context, db int, key string, members ...[]byte) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.Type != typeZSet {
		return 0, store.ErrWrongType
	}
	hashes := make([]string, 0, len(members))
	for _, member := range members {
		hashes = append(hashes, memberHash(member))
	}
	if len(hashes) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).Where("db = ? AND key = ? AND member_hash IN ?", db, key, hashes).Delete(&ZSetMemberRow{})
	if res.Error == nil && res.RowsAffected > 0 {
		_ = s.db.WithContext(ctx).Model(row).Updates(map[string]any{"version": row.Version + 1}).Error
	}
	return res.RowsAffected, res.Error
}

func (s *Store) ZRemRangeByScore(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.Type != typeZSet {
		return 0, store.ErrWrongType
	}
	q := s.db.WithContext(ctx).Where("db = ? AND key = ?", db, key)
	q = applyScoreBounds(q, min, max)
	res := q.Delete(&ZSetMemberRow{})
	if res.Error == nil && res.RowsAffected > 0 {
		_ = s.db.WithContext(ctx).Model(row).Updates(map[string]any{"version": row.Version + 1}).Error
	}
	return res.RowsAffected, res.Error
}

func (s *Store) ZRange(ctx context.Context, db int, key string, start, stop int64) ([]store.ZMember, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	if row.Type != typeZSet {
		return nil, store.ErrWrongType
	}
	start, stop, ok, err := s.normalizeRange(ctx, db, key, start, stop)
	if err != nil || !ok {
		return nil, err
	}
	var rows []ZSetMemberRow
	err = s.db.WithContext(ctx).
		Where("db = ? AND key = ?", db, key).
		Order("score ASC, member ASC").
		Offset(int(start)).
		Limit(int(stop - start + 1)).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]store.ZMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.ZMember{Member: row.Member, Score: row.Score})
	}
	return out, nil
}

func (s *Store) ZScore(ctx context.Context, db int, key string, member []byte) (float64, bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, false, err
	}
	if row.Type != typeZSet {
		return 0, false, store.ErrWrongType
	}
	var memberRow ZSetMemberRow
	err = s.db.WithContext(ctx).
		Select("score").
		Where("db = ? AND key = ? AND member_hash = ?", db, key, memberHash(member)).
		First(&memberRow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return memberRow.Score, true, nil
}

func (s *Store) ZCount(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return 0, err
	}
	if row.Type != typeZSet {
		return 0, store.ErrWrongType
	}
	var n int64
	q := s.db.WithContext(ctx).Model(&ZSetMemberRow{}).Where("db = ? AND key = ?", db, key)
	q = applyScoreBounds(q, min, max)
	err = q.Count(&n).Error
	return n, err
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
	var rows []KeyRow
	q := s.db.WithContext(ctx).
		Select("key").
		Where("db = ? AND key > ? AND (expires_at IS NULL OR expires_at > ?)", db, last, time.Now()).
		Order("key ASC").
		Limit(count)
	if pattern != "" && pattern != "*" {
		q = q.Where("key LIKE ? ESCAPE '\\'", redisPatternToLike(pattern))
	}
	if err := q.Find(&rows).Error; err != nil {
		return store.ScanResult{}, err
	}
	keys := make([]string, 0, len(rows))
	next := "0"
	for _, row := range rows {
		keys = append(keys, row.Key)
		next = row.Key
	}
	if len(rows) < count {
		next = "0"
	}
	return store.ScanResult{Cursor: next, Keys: keys}, nil
}

func (s *Store) CleanupExpired(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	var rows []KeyRow
	if err := s.db.WithContext(ctx).Select("db", "key").Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now()).Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Key)
	}
	res := s.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at <= ? AND key IN ?", time.Now(), keys).Delete(&KeyRow{})
	return res.RowsAffected, res.Error
}

func (s *Store) Stats(ctx context.Context, db int) (store.Stats, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&KeyRow{}).Where("db = ? AND (expires_at IS NULL OR expires_at > ?)", db, time.Now()).Count(&n).Error
	return store.Stats{Keys: n}, err
}

func (s *Store) Close() error { return nil }

func (s *Store) getLiveRow(ctx context.Context, db int, key string) (*KeyRow, bool, error) {
	return s.getLiveRowTx(ctx, s.db, db, key, false)
}

func (s *Store) getLiveRowTx(ctx context.Context, tx *gorm.DB, db int, key string, lock bool) (*KeyRow, bool, error) {
	var row KeyRow
	q := tx.WithContext(ctx).Where("db = ? AND key = ?", db, key)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if row.ExpiresAt != nil && !row.ExpiresAt.After(time.Now()) {
		_ = tx.WithContext(ctx).Where("db = ? AND key = ?", db, key).Delete(&ZSetMemberRow{}).Error
		_ = tx.WithContext(ctx).Where("db = ? AND key = ?", db, key).Delete(&KeyRow{}).Error
		return nil, false, nil
	}
	return &row, true, nil
}

func (s *Store) normalizeRange(ctx context.Context, db int, key string, start, stop int64) (int64, int64, bool, error) {
	var n int64
	if start < 0 || stop < 0 {
		if err := s.db.WithContext(ctx).Model(&ZSetMemberRow{}).Where("db = ? AND key = ?", db, key).Count(&n).Error; err != nil {
			return 0, 0, false, err
		}
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

func toValue(row *KeyRow) *store.Value {
	if row == nil {
		return nil
	}
	return &store.Value{Type: row.Type, Data: row.StringValue, ExpiresAt: row.ExpiresAt, Version: row.Version}
}

func memberHash(member []byte) string {
	sum := sha256.Sum256(member)
	return hex.EncodeToString(sum[:])
}

func applyScoreBounds(q *gorm.DB, min, max store.ScoreBound) *gorm.DB {
	if min.Infinite > 0 {
		q = q.Where("1 = 0")
	} else if min.Infinite == 0 {
		if min.Exclusive {
			q = q.Where("score > ?", min.Value)
		} else {
			q = q.Where("score >= ?", min.Value)
		}
	}
	if max.Infinite < 0 {
		q = q.Where("1 = 0")
	} else if max.Infinite == 0 {
		if max.Exclusive {
			q = q.Where("score < ?", max.Value)
		} else {
			q = q.Where("score <= ?", max.Value)
		}
	}
	return q
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
