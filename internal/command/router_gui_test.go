package command

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pubflow/redge/internal/cache"
	"github.com/pubflow/redge/internal/store"
)

func TestGUICompatibilityTypeStringAndMissing(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"SET", "hello", "world"}, "+OK\r\n")
	mustReply(t, router, session, []string{"TYPE", "hello"}, "+string\r\n")
	mustReply(t, router, session, []string{"TYPE", "missing"}, "+none\r\n")
}

func TestGUICompatibilityTypeZSet(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"ZADD", "leaderboard", "10", "ada"}, ":1\r\n")
	mustReply(t, router, session, []string{"TYPE", "leaderboard"}, "+zset\r\n")
}

func TestGUICompatibilityDBSize(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"SET", "a", "1"}, "+OK\r\n")
	mustReply(t, router, session, []string{"ZADD", "z", "1", "m"}, ":1\r\n")
	mustReply(t, router, session, []string{"DBSIZE"}, ":2\r\n")
}

func TestGUICompatibilityStrlen(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"SET", "hello", "world"}, "+OK\r\n")
	mustReply(t, router, session, []string{"STRLEN", "hello"}, ":5\r\n")
	mustReply(t, router, session, []string{"STRLEN", "missing"}, ":0\r\n")
	mustReply(t, router, session, []string{"ZADD", "leaderboard", "10", "ada"}, ":1\r\n")

	out := string(router.Handle(context.Background(), session, []string{"STRLEN", "leaderboard"}))
	if !strings.Contains(out, store.ErrWrongType.Error()) {
		t.Fatalf("expected WRONGTYPE, got %q", out)
	}
}

func TestGUICompatibilityInfo(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"SET", "hello", "world"}, "+OK\r\n")

	all := string(router.Handle(context.Background(), session, []string{"INFO"}))
	if !strings.Contains(all, "# Server\r\n") || !strings.Contains(all, "# Keyspace\r\n") || !strings.Contains(all, "db0:keys=1,expires=0,avg_ttl=0") {
		t.Fatalf("expected server and keyspace info, got %q", all)
	}

	server := string(router.Handle(context.Background(), session, []string{"INFO", "server"}))
	if !strings.Contains(server, "# Server\r\n") || strings.Contains(server, "# Keyspace\r\n") {
		t.Fatalf("expected server-only info, got %q", server)
	}

	keyspace := string(router.Handle(context.Background(), session, []string{"INFO", "keyspace"}))
	if strings.Contains(keyspace, "# Server\r\n") || !strings.Contains(keyspace, "# Keyspace\r\n") {
		t.Fatalf("expected keyspace-only info, got %q", keyspace)
	}
}

func TestGUICompatibilityMemoryUsage(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"SET", "hello", "world"}, "+OK\r\n")
	mustReply(t, router, session, []string{"MEMORY", "USAGE", "hello"}, ":10\r\n")
	mustReply(t, router, session, []string{"MEMORY", "USAGE", "missing"}, "$-1\r\n")

	mustReply(t, router, session, []string{"ZADD", "leaderboard", "10", "ada"}, ":1\r\n")
	mustReply(t, router, session, []string{"MEMORY", "USAGE", "leaderboard"}, ":75\r\n")
}

func TestRESPBatchAndFetchMutatingStringCommands(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"MSET", "a", "1", "b", "2"}, "+OK\r\n")
	mustReply(t, router, session, []string{"MGET", "a", "missing", "b"}, "*3\r\n$1\r\n1\r\n$-1\r\n$1\r\n2\r\n")
	mustReply(t, router, session, []string{"SETNX", "a", "x"}, ":0\r\n")
	mustReply(t, router, session, []string{"SETNX", "c", "3"}, ":1\r\n")
	mustReply(t, router, session, []string{"GETSET", "c", "4"}, "$1\r\n3\r\n")
	mustReply(t, router, session, []string{"GETDEL", "c"}, "$1\r\n4\r\n")
	mustReply(t, router, session, []string{"GET", "c"}, "$-1\r\n")
}

func TestRESPPersistAndZRangeByScore(t *testing.T) {
	router, session := newGUITestRouter()

	mustReply(t, router, session, []string{"SET", "ttl-key", "value", "EX", "60"}, "+OK\r\n")
	mustReply(t, router, session, []string{"PERSIST", "ttl-key"}, ":1\r\n")
	mustReply(t, router, session, []string{"PERSIST", "ttl-key"}, ":0\r\n")
	mustReply(t, router, session, []string{"ZADD", "z", "1", "one", "2", "two", "3", "three"}, ":3\r\n")
	mustReply(t, router, session, []string{"ZRANGEBYSCORE", "z", "1", "3", "WITHSCORES", "LIMIT", "1", "2"}, "*4\r\n$3\r\ntwo\r\n$1\r\n2\r\n$5\r\nthree\r\n$1\r\n3\r\n")
	mustReply(t, router, session, []string{"ZREVRANGEBYSCORE", "z", "3", "1", "LIMIT", "0", "1"}, "*1\r\n$5\r\nthree\r\n")
}

func newGUITestRouter() (*Router, *Session) {
	return NewRouter(RouterOptions{
		Store: &fakeStore{
			strings: make(map[string][]byte),
			zsets:   make(map[string]map[string]float64),
		},
		Cache: cache.NewMemory(cache.Options{}),
	}), &Session{}
}

func mustReply(t *testing.T, router *Router, session *Session, args []string, want string) {
	t.Helper()
	got := string(router.Handle(context.Background(), session, args))
	if got != want {
		t.Fatalf("%v: expected %q, got %q", args, want, got)
	}
}

type fakeStore struct {
	strings map[string][]byte
	zsets   map[string]map[string]float64
	ttl     map[string]bool
}

func fakeKey(db int, key string) string {
	return strconv.Itoa(db) + ":" + key
}

func (s *fakeStore) Migrate(ctx context.Context) error { return nil }
func (s *fakeStore) Ping(ctx context.Context) error    { return nil }

func (s *fakeStore) Type(ctx context.Context, db int, key string) (string, error) {
	k := fakeKey(db, key)
	if _, ok := s.strings[k]; ok {
		return store.TypeString, nil
	}
	if _, ok := s.zsets[k]; ok {
		return store.TypeZSet, nil
	}
	return store.TypeNone, nil
}

func (s *fakeStore) Get(ctx context.Context, db int, key string) (*store.Value, error) {
	k := fakeKey(db, key)
	if _, ok := s.zsets[k]; ok {
		return nil, store.ErrWrongType
	}
	v, ok := s.strings[k]
	if !ok {
		return nil, nil
	}
	return &store.Value{Type: store.TypeString, Data: append([]byte(nil), v...)}, nil
}

func (s *fakeStore) MGet(ctx context.Context, db int, keys ...string) ([]*store.Value, error) {
	out := make([]*store.Value, len(keys))
	for i, key := range keys {
		v, err := s.Get(ctx, db, key)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (s *fakeStore) Set(ctx context.Context, db int, key string, value []byte, opts store.SetOptions) (*store.Value, bool, error) {
	k := fakeKey(db, key)
	if _, ok := s.zsets[k]; ok {
		return nil, false, store.ErrWrongType
	}
	old, _ := s.Get(ctx, db, key)
	if opts.NX && old != nil {
		return old, false, nil
	}
	if opts.XX && old == nil {
		return nil, false, nil
	}
	s.strings[k] = append([]byte(nil), value...)
	if opts.TTL > 0 {
		if s.ttl == nil {
			s.ttl = map[string]bool{}
		}
		s.ttl[k] = true
	}
	return old, true, nil
}

func (s *fakeStore) MSet(ctx context.Context, db int, pairs []store.KeyValue, opts store.SetOptions) error {
	for _, pair := range pairs {
		if _, _, err := s.Set(ctx, db, pair.Key, pair.Value, opts); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeStore) GetDel(ctx context.Context, db int, key string) (*store.Value, bool, error) {
	v, err := s.Get(ctx, db, key)
	if err != nil || v == nil {
		return v, false, err
	}
	_, err = s.Delete(ctx, db, key)
	return v, err == nil, err
}

func (s *fakeStore) Delete(ctx context.Context, db int, keys ...string) (int64, error) {
	var n int64
	for _, key := range keys {
		k := fakeKey(db, key)
		if _, ok := s.strings[k]; ok {
			delete(s.strings, k)
			delete(s.ttl, k)
			n++
		}
		if _, ok := s.zsets[k]; ok {
			delete(s.zsets, k)
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) Exists(ctx context.Context, db int, keys ...string) (int64, error) {
	var n int64
	for _, key := range keys {
		if typ, _ := s.Type(ctx, db, key); typ != store.TypeNone {
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) Expire(ctx context.Context, db int, key string, ttl time.Duration) (bool, error) {
	typ, _ := s.Type(ctx, db, key)
	if typ != store.TypeNone {
		if s.ttl == nil {
			s.ttl = map[string]bool{}
		}
		s.ttl[fakeKey(db, key)] = true
	}
	return typ != store.TypeNone, nil
}

func (s *fakeStore) Persist(ctx context.Context, db int, key string) (bool, error) {
	k := fakeKey(db, key)
	if s.ttl != nil && s.ttl[k] {
		delete(s.ttl, k)
		return true, nil
	}
	return false, nil
}

func (s *fakeStore) TTL(ctx context.Context, db int, key string) (time.Duration, bool, bool, error) {
	typ, _ := s.Type(ctx, db, key)
	return 0, typ != store.TypeNone, s.ttl != nil && s.ttl[fakeKey(db, key)], nil
}

func (s *fakeStore) IncrBy(ctx context.Context, db int, key string, delta int64) (int64, error) {
	v, err := s.Get(ctx, db, key)
	if err != nil {
		return 0, err
	}
	var current int64
	if v != nil {
		current, err = strconv.ParseInt(string(v.Data), 10, 64)
		if err != nil {
			return 0, store.ErrNotInt
		}
	}
	next := current + delta
	s.strings[fakeKey(db, key)] = []byte(strconv.FormatInt(next, 10))
	return next, nil
}

func (s *fakeStore) ZAdd(ctx context.Context, db int, key string, score float64, member []byte) (int64, error) {
	k := fakeKey(db, key)
	if _, ok := s.strings[k]; ok {
		return 0, store.ErrWrongType
	}
	if s.zsets[k] == nil {
		s.zsets[k] = make(map[string]float64)
	}
	added := int64(0)
	if _, ok := s.zsets[k][string(member)]; !ok {
		added = 1
	}
	s.zsets[k][string(member)] = score
	return added, nil
}

func (s *fakeStore) ZCard(ctx context.Context, db int, key string) (int64, error) {
	k := fakeKey(db, key)
	if _, ok := s.strings[k]; ok {
		return 0, store.ErrWrongType
	}
	return int64(len(s.zsets[k])), nil
}

func (s *fakeStore) ZRem(ctx context.Context, db int, key string, members ...[]byte) (int64, error) {
	z := s.zsets[fakeKey(db, key)]
	var n int64
	for _, member := range members {
		if _, ok := z[string(member)]; ok {
			delete(z, string(member))
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) ZRemRangeByScore(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	return 0, nil
}

func (s *fakeStore) ZRange(ctx context.Context, db int, key string, start, stop int64) ([]store.ZMember, error) {
	z := s.zsets[fakeKey(db, key)]
	members := make([]string, 0, len(z))
	for member := range z {
		members = append(members, member)
	}
	sort.Strings(members)
	out := make([]store.ZMember, 0, len(members))
	for _, member := range members {
		out = append(out, store.ZMember{Member: []byte(member), Score: z[member]})
	}
	return out, nil
}

func (s *fakeStore) ZRangeByScore(ctx context.Context, db int, key string, min, max store.ScoreBound, offset, limit int64, rev bool) ([]store.ZMember, error) {
	z := s.zsets[fakeKey(db, key)]
	out := make([]store.ZMember, 0, len(z))
	for member, score := range z {
		if scoreInRange(score, min, max) {
			out = append(out, store.ZMember{Member: []byte(member), Score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			if rev {
				return string(out[i].Member) > string(out[j].Member)
			}
			return string(out[i].Member) < string(out[j].Member)
		}
		if rev {
			return out[i].Score > out[j].Score
		}
		return out[i].Score < out[j].Score
	})
	if offset > int64(len(out)) {
		return nil, nil
	}
	out = out[offset:]
	if limit >= 0 && limit < int64(len(out)) {
		out = out[:limit]
	}
	return out, nil
}

func scoreInRange(score float64, min, max store.ScoreBound) bool {
	if min.Infinite == 0 {
		if min.Exclusive && score <= min.Value {
			return false
		}
		if !min.Exclusive && score < min.Value {
			return false
		}
	}
	if max.Infinite == 0 {
		if max.Exclusive && score >= max.Value {
			return false
		}
		if !max.Exclusive && score > max.Value {
			return false
		}
	}
	return true
}

func (s *fakeStore) ZScore(ctx context.Context, db int, key string, member []byte) (float64, bool, error) {
	score, ok := s.zsets[fakeKey(db, key)][string(member)]
	return score, ok, nil
}

func (s *fakeStore) ZCount(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	return s.ZCard(ctx, db, key)
}

func (s *fakeStore) Scan(ctx context.Context, db int, cursor string, pattern string, count int) (store.ScanResult, error) {
	return store.ScanResult{Cursor: "0"}, nil
}

func (s *fakeStore) CleanupExpired(ctx context.Context, limit int) (int64, error) {
	return 0, nil
}

func (s *fakeStore) Stats(ctx context.Context, db int) (store.Stats, error) {
	prefix := strconv.Itoa(db) + ":"
	keys := map[string]struct{}{}
	for k := range s.strings {
		if strings.HasPrefix(k, prefix) {
			keys[k] = struct{}{}
		}
	}
	for k := range s.zsets {
		if strings.HasPrefix(k, prefix) {
			keys[k] = struct{}{}
		}
	}
	return store.Stats{Keys: int64(len(keys))}, nil
}

func (s *fakeStore) Close() error { return nil }
