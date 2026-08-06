package sqlstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/docstore"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const typeString = store.TypeString
const typeZSet = store.TypeZSet

type KeyRow struct {
	DB          int        `gorm:"column:db;primaryKey;autoIncrement:false"`
	Key         string     `gorm:"column:key;primaryKey;size:512"`
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
	Key        string    `gorm:"column:key;primaryKey;size:512;index:idx_redge_zset_score,priority:1"`
	MemberHash string    `gorm:"column:member_hash;primaryKey;size:64"`
	Member     []byte    `gorm:"column:member;not null"`
	Score      float64   `gorm:"column:score;not null;index:idx_redge_zset_score,priority:2"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (ZSetMemberRow) TableName() string { return "redge_zset_members" }

type DocRow struct {
	DB         int        `gorm:"column:db;primaryKey;autoIncrement:false"`
	Collection string     `gorm:"column:collection;primaryKey;size:191;index:idx_redge_docs_collection_id,priority:1"`
	ID         string     `gorm:"column:id;primaryKey;size:191;index:idx_redge_docs_collection_id,priority:2"`
	Value      []byte     `gorm:"column:value;not null"`
	SearchText string     `gorm:"column:search_text;type:text"`
	ExpiresAt  *time.Time `gorm:"column:expires_at;index"`
	Version    int64      `gorm:"column:version;not null;default:1"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (DocRow) TableName() string { return "redge_docs" }

type DocIndexConfigRow struct {
	DB         int       `gorm:"column:db;primaryKey;autoIncrement:false"`
	Collection string    `gorm:"column:collection;primaryKey;size:191"`
	Kind       string    `gorm:"column:kind;primaryKey;size:32"`
	Field      string    `gorm:"column:field;primaryKey;size:191"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (DocIndexConfigRow) TableName() string { return "redge_doc_index_config" }

type DocIndexRow struct {
	DB          int       `gorm:"column:db;primaryKey;autoIncrement:false"`
	Collection  string    `gorm:"column:collection;primaryKey;size:191;index:idx_redge_doc_indexes_lookup,priority:1"`
	Field       string    `gorm:"column:field;primaryKey;size:191;index:idx_redge_doc_indexes_lookup,priority:2"`
	ValueHash   string    `gorm:"column:value_hash;primaryKey;size:64;index:idx_redge_doc_indexes_lookup,priority:3"`
	ID          string    `gorm:"column:id;primaryKey;size:191;index:idx_redge_doc_indexes_lookup,priority:4"`
	DisplayText string    `gorm:"column:display_text;type:text"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (DocIndexRow) TableName() string { return "redge_doc_indexes" }

type Store struct {
	conn *database.Connection
	db   *gorm.DB
	log  *zap.Logger
}

func New(conn *database.Connection, log *zap.Logger) (*Store, error) {
	return &Store{conn: conn, db: conn.DB, log: log}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if s.conn.IsPostgres() {
		return s.migratePostgres(ctx)
	}
	if err := s.db.WithContext(ctx).AutoMigrate(&KeyRow{}, &ZSetMemberRow{}); err != nil {
		return err
	}
	if s.conn.Type() == "sqlite" {
		_ = s.db.Exec("PRAGMA journal_mode=WAL").Error
		_ = s.db.Exec("PRAGMA busy_timeout=5000").Error
	}
	return nil
}

func (s *Store) MigrateDocs(ctx context.Context) error {
	if s.conn.IsPostgres() {
		return s.migrateDocsPostgres(ctx)
	}
	return s.db.WithContext(ctx).AutoMigrate(&DocRow{}, &DocIndexConfigRow{}, &DocIndexRow{})
}

func (s *Store) migratePostgres(ctx context.Context) error {
	return s.execStatements(ctx, []string{
		`CREATE TABLE IF NOT EXISTS redge_keys (
			db BIGINT NOT NULL,
			"key" VARCHAR(512) NOT NULL,
			"type" VARCHAR(32) NOT NULL,
			string_value BYTEA,
			expires_at TIMESTAMPTZ,
			version BIGINT NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ,
			PRIMARY KEY (db, "key")
		)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_keys_type ON redge_keys ("type")`,
		`CREATE INDEX IF NOT EXISTS idx_redge_keys_expires_at ON redge_keys (expires_at)`,
		`CREATE TABLE IF NOT EXISTS redge_zset_members (
			db BIGINT NOT NULL,
			"key" VARCHAR(512) NOT NULL,
			member_hash VARCHAR(64) NOT NULL,
			member BYTEA NOT NULL,
			score DOUBLE PRECISION NOT NULL,
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ,
			PRIMARY KEY (db, "key", member_hash)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_zset_score ON redge_zset_members ("key", score)`,
	})
}

func (s *Store) migrateDocsPostgres(ctx context.Context) error {
	return s.execStatements(ctx, []string{
		`CREATE TABLE IF NOT EXISTS redge_docs (
			db BIGINT NOT NULL,
			collection VARCHAR(191) NOT NULL,
			id VARCHAR(191) NOT NULL,
			value BYTEA NOT NULL,
			search_text TEXT,
			expires_at TIMESTAMPTZ,
			version BIGINT NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ,
			PRIMARY KEY (db, collection, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_docs_collection_id ON redge_docs (collection, id)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_docs_expires_at ON redge_docs (expires_at)`,
		`CREATE TABLE IF NOT EXISTS redge_doc_index_config (
			db BIGINT NOT NULL,
			collection VARCHAR(191) NOT NULL,
			kind VARCHAR(32) NOT NULL,
			field VARCHAR(191) NOT NULL,
			created_at TIMESTAMPTZ,
			PRIMARY KEY (db, collection, kind, field)
		)`,
		`CREATE TABLE IF NOT EXISTS redge_doc_indexes (
			db BIGINT NOT NULL,
			collection VARCHAR(191) NOT NULL,
			field VARCHAR(191) NOT NULL,
			value_hash VARCHAR(64) NOT NULL,
			id VARCHAR(191) NOT NULL,
			display_text TEXT,
			created_at TIMESTAMPTZ,
			PRIMARY KEY (db, collection, field, value_hash, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_redge_doc_indexes_lookup ON redge_doc_indexes (collection, field, value_hash, id)`,
	})
}

func (s *Store) execStatements(ctx context.Context, statements []string) error {
	for _, stmt := range statements {
		if err := s.db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return err
		}
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

func (s *Store) MGet(ctx context.Context, db int, keys ...string) ([]*store.Value, error) {
	out := make([]*store.Value, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	var rows []KeyRow
	if err := s.db.WithContext(ctx).
		Where("db = ? AND "+s.keyCol()+" IN ? AND type = ? AND (expires_at IS NULL OR expires_at > ?)", db, keys, typeString, time.Now()).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	byKey := make(map[string]*KeyRow, len(rows))
	for i := range rows {
		byKey[rows[i].Key] = &rows[i]
	}
	for i, key := range keys {
		out[i] = toValue(byKey[key])
	}
	return out, nil
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
				Where("db = ? AND "+s.keyCol()+" = ?", db, key).
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

func (s *Store) MSet(ctx context.Context, db int, pairs []store.KeyValue, opts store.SetOptions) error {
	if len(pairs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, pair := range pairs {
			row, found, err := s.getLiveRowTx(ctx, tx, db, pair.Key, true)
			if err != nil {
				return err
			}
			if opts.NX && found {
				continue
			}
			if opts.XX && !found {
				continue
			}
			if found && row.Type != typeString {
				return store.ErrWrongType
			}
			now := time.Now()
			var expiresAt *time.Time
			if opts.KeepTTL && found {
				expiresAt = row.ExpiresAt
			}
			if opts.TTL > 0 {
				t := now.Add(opts.TTL)
				expiresAt = &t
			}
			if found {
				if err := tx.Model(&KeyRow{}).
					Where("db = ? AND "+s.keyCol()+" = ?", db, pair.Key).
					Updates(map[string]any{
						"type":         typeString,
						"string_value": pair.Value,
						"expires_at":   expiresAt,
						"version":      row.Version + 1,
						"updated_at":   now,
					}).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Create(&KeyRow{
				DB: db, Key: pair.Key, Type: typeString, StringValue: pair.Value,
				ExpiresAt: expiresAt, Version: 1, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetDel(ctx context.Context, db int, key string) (*store.Value, bool, error) {
	var out *store.Value
	var deleted bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, found, err := s.getLiveRowTx(ctx, tx, db, key, true)
		if err != nil || !found {
			return err
		}
		if row.Type != typeString {
			return store.ErrWrongType
		}
		out = toValue(row)
		if err := tx.Where("db = ? AND "+s.keyCol()+" = ?", db, key).Delete(&KeyRow{}).Error; err != nil {
			return err
		}
		deleted = true
		return nil
	})
	return out, deleted, err
}

func (s *Store) Delete(ctx context.Context, db int, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	var n int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("db = ? AND "+s.keyCol()+" IN ?", db, keys).Delete(&ZSetMemberRow{}).Error; err != nil {
			return err
		}
		res := tx.Where("db = ? AND "+s.keyCol()+" IN ?", db, keys).Delete(&KeyRow{})
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
		Where("db = ? AND "+s.keyCol()+" IN ? AND (expires_at IS NULL OR expires_at > ?)", db, keys, now).
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

func (s *Store) Persist(ctx context.Context, db int, key string) (bool, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found || row.ExpiresAt == nil {
		return false, err
	}
	err = s.db.WithContext(ctx).Model(row).Updates(map[string]any{
		"expires_at": nil,
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
				Where("db = ? AND "+s.keyCol()+" = ?", db, key).
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
		err = tx.Where("db = ? AND "+s.keyCol()+" = ? AND member_hash = ?", db, key, hash).First(&existing).Error
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
	err = s.db.WithContext(ctx).Model(&ZSetMemberRow{}).Where("db = ? AND "+s.keyCol()+" = ?", db, key).Count(&n).Error
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
	res := s.db.WithContext(ctx).Where("db = ? AND "+s.keyCol()+" = ? AND member_hash IN ?", db, key, hashes).Delete(&ZSetMemberRow{})
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
	q := s.db.WithContext(ctx).Where("db = ? AND "+s.keyCol()+" = ?", db, key)
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
		Where("db = ? AND "+s.keyCol()+" = ?", db, key).
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

func (s *Store) ZRangeByScore(ctx context.Context, db int, key string, min, max store.ScoreBound, offset, limit int64, rev bool) ([]store.ZMember, error) {
	row, found, err := s.getLiveRow(ctx, db, key)
	if err != nil || !found {
		return nil, err
	}
	if row.Type != typeZSet {
		return nil, store.ErrWrongType
	}
	q := s.db.WithContext(ctx).Where("db = ? AND "+s.keyCol()+" = ?", db, key)
	q = applyScoreBounds(q, min, max)
	if rev {
		q = q.Order("score DESC, member DESC")
	} else {
		q = q.Order("score ASC, member ASC")
	}
	if offset > 0 {
		q = q.Offset(int(offset))
	}
	if limit >= 0 {
		q = q.Limit(int(limit))
	}
	var rows []ZSetMemberRow
	if err := q.Find(&rows).Error; err != nil {
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
		Where("db = ? AND "+s.keyCol()+" = ? AND member_hash = ?", db, key, memberHash(member)).
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
	q := s.db.WithContext(ctx).Model(&ZSetMemberRow{}).Where("db = ? AND "+s.keyCol()+" = ?", db, key)
	q = applyScoreBounds(q, min, max)
	err = q.Count(&n).Error
	return n, err
}

func (s *Store) ZIncrBy(ctx context.Context, db int, key string, member []byte, delta float64) (float64, error) {
	hash := memberHash(member)
	var out float64
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
		err = tx.Where("db = ? AND "+s.keyCol()+" = ? AND member_hash = ?", db, key, hash).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			out = delta
			return tx.Create(&ZSetMemberRow{DB: db, Key: key, MemberHash: hash, Member: member, Score: out, CreatedAt: now, UpdatedAt: now}).Error
		}
		if err != nil {
			return err
		}
		out = existing.Score + delta
		return tx.Model(&existing).Updates(map[string]any{"score": out, "member": member, "updated_at": now}).Error
	})
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
	var out []store.ZMember
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		keyRow, found, err := s.getLiveRowTx(ctx, tx, db, key, true)
		if err != nil {
			return err
		}
		if !found {
			out = nil
			return nil
		}
		if keyRow.Type != typeZSet {
			return store.ErrWrongType
		}
		order := "score ASC, member ASC"
		if max {
			order = "score DESC, member DESC"
		}
		var rows []ZSetMemberRow
		if err := tx.Where("db = ? AND "+s.keyCol()+" = ?", db, key).Order(order).Limit(int(count)).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			out = nil
			return nil
		}
		hashes := make([]string, 0, len(rows))
		out = make([]store.ZMember, 0, len(rows))
		for _, row := range rows {
			out = append(out, store.ZMember{Member: row.Member, Score: row.Score})
			hashes = append(hashes, row.MemberHash)
		}
		if err := tx.Where("db = ? AND "+s.keyCol()+" = ? AND member_hash IN ?", db, key, hashes).Delete(&ZSetMemberRow{}).Error; err != nil {
			return err
		}
		now := time.Now()
		return tx.Model(keyRow).Updates(map[string]any{"version": keyRow.Version + 1, "updated_at": now}).Error
	})
	return out, err
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
		Where("db = ? AND "+s.keyCol()+" > ? AND (expires_at IS NULL OR expires_at > ?)", db, last, time.Now()).
		Order(s.keyCol() + " ASC").
		Limit(count)
	if pattern != "" && pattern != "*" {
		q = q.Where(s.keyCol()+" LIKE ? ESCAPE '\\'", redisPatternToLike(pattern))
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
	res := s.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at <= ? AND "+s.keyCol()+" IN ?", time.Now(), keys).Delete(&KeyRow{})
	return res.RowsAffected, res.Error
}

func (s *Store) Stats(ctx context.Context, db int) (store.Stats, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&KeyRow{}).Where("db = ? AND (expires_at IS NULL OR expires_at > ?)", db, time.Now()).Count(&n).Error
	return store.Stats{Keys: n}, err
}

func (s *Store) ConfigureIndexes(ctx context.Context, db int, collection string, cfg docstore.IndexConfig) (docstore.IndexConfig, error) {
	cfg = normalizeIndexConfig(cfg)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("db = ? AND collection = ?", db, collection).Delete(&DocIndexConfigRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("db = ? AND collection = ?", db, collection).Delete(&DocIndexRow{}).Error; err != nil {
			return err
		}
		now := time.Now()
		rows := make([]DocIndexConfigRow, 0, len(cfg.Fields)+len(cfg.SearchFields))
		for _, field := range cfg.Fields {
			rows = append(rows, DocIndexConfigRow{DB: db, Collection: collection, Kind: "field", Field: field, CreatedAt: now})
		}
		for _, field := range cfg.SearchFields {
			rows = append(rows, DocIndexConfigRow{DB: db, Collection: collection, Kind: "search", Field: field, CreatedAt: now})
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		var docs []DocRow
		if err := tx.Where("db = ? AND collection = ? AND (expires_at IS NULL OR expires_at > ?)", db, collection, now).Find(&docs).Error; err != nil {
			return err
		}
		for i := range docs {
			indexRows, searchText, err := buildDocIndexes(db, collection, docs[i].ID, docs[i].Value, cfg)
			if err != nil {
				return err
			}
			if len(indexRows) > 0 {
				if err := tx.Create(&indexRows).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&DocRow{}).
				Where("db = ? AND collection = ? AND id = ?", db, collection, docs[i].ID).
				Update("search_text", searchText).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return cfg, err
}

func (s *Store) GetIndexConfig(ctx context.Context, db int, collection string) (docstore.IndexConfig, error) {
	return s.getDocIndexConfigTx(ctx, s.db, db, collection)
}

func (s *Store) PutDoc(ctx context.Context, db int, collection, id string, value []byte, opts docstore.WriteOptions, createOnly bool) (docstore.Document, bool, error) {
	var out docstore.Document
	var created bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cfg, err := s.mergeDocIndexConfigTx(ctx, tx, db, collection, opts)
		if err != nil {
			return err
		}
		row, found, err := s.getLiveDocRowTx(ctx, tx, db, collection, id, true)
		if err != nil {
			return err
		}
		if createOnly && found {
			return docstore.ErrConflict
		}
		indexRows, searchText, err := buildDocIndexes(db, collection, id, value, cfg)
		if err != nil {
			return err
		}
		now := time.Now()
		var expiresAt *time.Time
		if opts.TTL > 0 {
			t := now.Add(opts.TTL)
			expiresAt = &t
		} else if found {
			expiresAt = row.ExpiresAt
		}
		version := int64(1)
		createdAt := now
		if found {
			version = row.Version + 1
			createdAt = row.CreatedAt
		} else {
			created = true
		}
		if found {
			if err := tx.Model(&DocRow{}).
				Where("db = ? AND collection = ? AND id = ?", db, collection, id).
				Updates(map[string]any{
					"value":       value,
					"search_text": searchText,
					"expires_at":  expiresAt,
					"version":     version,
					"updated_at":  now,
				}).Error; err != nil {
				return err
			}
		} else if err := tx.Create(&DocRow{
			DB: db, Collection: collection, ID: id, Value: value, SearchText: searchText,
			ExpiresAt: expiresAt, Version: version, CreatedAt: createdAt, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("db = ? AND collection = ? AND id = ?", db, collection, id).Delete(&DocIndexRow{}).Error; err != nil {
			return err
		}
		if len(indexRows) > 0 {
			if err := tx.Create(&indexRows).Error; err != nil {
				return err
			}
		}
		out = docstore.Document{Collection: collection, ID: id, Value: value, Version: version, CreatedAt: createdAt, UpdatedAt: now, ExpiresAt: expiresAt}
		return nil
	})
	return out, created, err
}

func (s *Store) GetDoc(ctx context.Context, db int, collection, id string) (docstore.Document, error) {
	row, found, err := s.getLiveDocRowTx(ctx, s.db, db, collection, id, false)
	if err != nil {
		return docstore.Document{}, err
	}
	if !found {
		return docstore.Document{}, docstore.ErrNotFound
	}
	return docFromRow(row), nil
}

func (s *Store) DeleteDoc(ctx context.Context, db int, collection, id string) (bool, error) {
	var deleted bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("db = ? AND collection = ? AND id = ?", db, collection, id).Delete(&DocIndexRow{}).Error; err != nil {
			return err
		}
		res := tx.Where("db = ? AND collection = ? AND id = ?", db, collection, id).Delete(&DocRow{})
		deleted = res.RowsAffected > 0
		return res.Error
	})
	return deleted, err
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
	q := s.db.WithContext(ctx).
		Where("db = ? AND collection = ? AND id > ? AND (expires_at IS NULL OR expires_at > ?)", db, collection, opts.Cursor, time.Now()).
		Order("id ASC").
		Limit(limit + 1)
	if opts.Search != "" {
		q = q.Where("search_text LIKE ? ESCAPE '|'", "%"+escapeLike(opts.Search)+"%")
	}
	var rows []DocRow
	if err := q.Find(&rows).Error; err != nil {
		return docstore.QueryResult{}, err
	}
	return docQueryResult(rows, limit), nil
}

func (s *Store) Close() error { return nil }

func (s *Store) getLiveRow(ctx context.Context, db int, key string) (*KeyRow, bool, error) {
	return s.getLiveRowTx(ctx, s.db, db, key, false)
}

func (s *Store) getLiveRowTx(ctx context.Context, tx *gorm.DB, db int, key string, lock bool) (*KeyRow, bool, error) {
	var row KeyRow
	q := tx.WithContext(ctx).Where("db = ? AND "+s.keyCol()+" = ?", db, key)
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
		_ = tx.WithContext(ctx).Where("db = ? AND "+s.keyCol()+" = ?", db, key).Delete(&ZSetMemberRow{}).Error
		_ = tx.WithContext(ctx).Where("db = ? AND "+s.keyCol()+" = ?", db, key).Delete(&KeyRow{}).Error
		return nil, false, nil
	}
	return &row, true, nil
}

func (s *Store) getLiveDocRowTx(ctx context.Context, tx *gorm.DB, db int, collection, id string, lock bool) (*DocRow, bool, error) {
	var row DocRow
	q := tx.WithContext(ctx).Where("db = ? AND collection = ? AND id = ?", db, collection, id)
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
		_ = tx.WithContext(ctx).Where("db = ? AND collection = ? AND id = ?", db, collection, id).Delete(&DocIndexRow{}).Error
		_ = tx.WithContext(ctx).Where("db = ? AND collection = ? AND id = ?", db, collection, id).Delete(&DocRow{}).Error
		return nil, false, nil
	}
	return &row, true, nil
}

func (s *Store) getDocIndexConfigTx(ctx context.Context, tx *gorm.DB, db int, collection string) (docstore.IndexConfig, error) {
	var rows []DocIndexConfigRow
	if err := tx.WithContext(ctx).
		Where("db = ? AND collection = ?", db, collection).
		Order("kind ASC, field ASC").
		Find(&rows).Error; err != nil {
		return docstore.IndexConfig{}, err
	}
	cfg := docstore.IndexConfig{}
	for _, row := range rows {
		switch row.Kind {
		case "field":
			cfg.Fields = append(cfg.Fields, row.Field)
		case "search":
			cfg.SearchFields = append(cfg.SearchFields, row.Field)
		}
	}
	return normalizeIndexConfig(cfg), nil
}

func (s *Store) mergeDocIndexConfigTx(ctx context.Context, tx *gorm.DB, db int, collection string, opts docstore.WriteOptions) (docstore.IndexConfig, error) {
	cfg, err := s.getDocIndexConfigTx(ctx, tx, db, collection)
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
	now := time.Now()
	for _, field := range difference(next.Fields, cfg.Fields) {
		if err := tx.Create(&DocIndexConfigRow{DB: db, Collection: collection, Kind: "field", Field: field, CreatedAt: now}).Error; err != nil {
			return docstore.IndexConfig{}, err
		}
	}
	for _, field := range difference(next.SearchFields, cfg.SearchFields) {
		if err := tx.Create(&DocIndexConfigRow{DB: db, Collection: collection, Kind: "search", Field: field, CreatedAt: now}).Error; err != nil {
			return docstore.IndexConfig{}, err
		}
	}
	return next, nil
}

func (s *Store) queryDocsByIndex(ctx context.Context, db int, collection string, opts docstore.QueryOptions, limit int) (docstore.QueryResult, error) {
	hash := indexValueHash(opts.Where.Value)
	var idxRows []DocIndexRow
	if err := s.db.WithContext(ctx).
		Where("db = ? AND collection = ? AND field = ? AND value_hash = ? AND id > ?", db, collection, opts.Where.Field, hash, opts.Cursor).
		Order("id ASC").
		Limit(limit + 1).
		Find(&idxRows).Error; err != nil {
		return docstore.QueryResult{}, err
	}
	if len(idxRows) == 0 {
		return docstore.QueryResult{}, nil
	}
	ids := make([]string, 0, len(idxRows))
	for _, row := range idxRows {
		ids = append(ids, row.ID)
	}
	q := s.db.WithContext(ctx).
		Where("db = ? AND collection = ? AND id IN ? AND (expires_at IS NULL OR expires_at > ?)", db, collection, ids, time.Now())
	if opts.Search != "" {
		q = q.Where("search_text LIKE ? ESCAPE '|'", "%"+escapeLike(opts.Search)+"%")
	}
	var docRows []DocRow
	if err := q.Find(&docRows).Error; err != nil {
		return docstore.QueryResult{}, err
	}
	byID := make(map[string]DocRow, len(docRows))
	for _, row := range docRows {
		byID[row.ID] = row
	}
	ordered := make([]DocRow, 0, len(docRows))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			ordered = append(ordered, row)
		}
	}
	return docQueryResult(ordered, limit), nil
}

func (s *Store) normalizeRange(ctx context.Context, db int, key string, start, stop int64) (int64, int64, bool, error) {
	var n int64
	if start < 0 || stop < 0 {
		if err := s.db.WithContext(ctx).Model(&ZSetMemberRow{}).Where("db = ? AND "+s.keyCol()+" = ?", db, key).Count(&n).Error; err != nil {
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

func docFromRow(row *DocRow) docstore.Document {
	return docstore.Document{
		Collection: row.Collection,
		ID:         row.ID,
		Value:      row.Value,
		Version:    row.Version,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
		ExpiresAt:  row.ExpiresAt,
	}
}

func docQueryResult(rows []DocRow, limit int) docstore.QueryResult {
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	docs := make([]docstore.Document, 0, len(rows))
	next := ""
	for i := range rows {
		docs = append(docs, docFromRow(&rows[i]))
		next = rows[i].ID
	}
	if !hasMore {
		next = ""
	}
	return docstore.QueryResult{Documents: docs, NextCursor: next, HasMore: hasMore}
}

func memberHash(member []byte) string {
	sum := sha256.Sum256(member)
	return hex.EncodeToString(sum[:])
}

func buildDocIndexes(db int, collection, id string, value []byte, cfg docstore.IndexConfig) ([]DocIndexRow, string, error) {
	var obj map[string]any
	if err := json.Unmarshal(value, &obj); err != nil {
		return nil, "", fmt.Errorf("%w: document must be a JSON object", docstore.ErrInvalidRequest)
	}
	now := time.Now()
	rows := make([]DocIndexRow, 0, len(cfg.Fields))
	for _, field := range cfg.Fields {
		raw, ok := obj[field]
		if !ok {
			continue
		}
		text, ok := normalizeScalar(raw)
		if !ok {
			continue
		}
		rows = append(rows, DocIndexRow{
			DB: db, Collection: collection, Field: field, ValueHash: indexValueHash(text),
			ID: id, DisplayText: text, CreatedAt: now,
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

func (s *Store) keyCol() string {
	if s.conn != nil && s.conn.IsMySQL() {
		return "`key`"
	}
	return "key"
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
