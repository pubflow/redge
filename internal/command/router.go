package command

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pubflow/redge/internal/cache"
	"github.com/pubflow/redge/internal/resp"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

type Session struct {
	Authed  bool
	DB      int
	Name    string
	InMulti bool
	Queued  [][]string
}

type RouterOptions struct {
	Store    store.Store
	Cache    *cache.Memory
	Password string
	Logger   *zap.Logger
}

type Router struct {
	store    store.Store
	cache    *cache.Memory
	password string
	logger   *zap.Logger
}

func NewRouter(opts RouterOptions) *Router {
	return &Router{store: opts.Store, cache: opts.Cache, password: opts.Password, logger: opts.Logger}
}

func (r *Router) Handle(ctx context.Context, s *Session, args []string) []byte {
	if len(args) == 0 {
		return resp.Error("ERR empty command")
	}
	cmd := strings.ToUpper(args[0])
	if r.password != "" && !s.Authed && cmd != "AUTH" && cmd != "PING" && cmd != "QUIT" {
		return resp.Error("NOAUTH Authentication required.")
	}
	if s.InMulti && cmd != "EXEC" && cmd != "DISCARD" && cmd != "MULTI" && cmd != "QUIT" {
		s.Queued = append(s.Queued, append([]string(nil), args...))
		return resp.Simple("QUEUED")
	}
	return r.execute(ctx, s, args)
}

func (r *Router) execute(ctx context.Context, s *Session, args []string) []byte {
	cmd := strings.ToUpper(args[0])
	switch cmd {
	case "AUTH":
		if len(args) != 2 && len(args) != 3 {
			return resp.Error("ERR wrong number of arguments for 'auth' command")
		}
		if len(args) == 3 && !strings.EqualFold(args[1], "default") {
			return resp.Error("WRONGPASS Redge only supports the default Redis user.")
		}
		pass := args[len(args)-1]
		if r.password == "" || pass != r.password {
			return resp.Error("WRONGPASS invalid username-password pair or user is disabled.")
		}
		s.Authed = true
		return resp.Simple("OK")
	case "PING":
		if len(args) > 1 {
			return resp.Bulk([]byte(args[1]))
		}
		return resp.Simple("PONG")
	case "ECHO":
		if len(args) != 2 {
			return resp.Error("ERR wrong number of arguments for 'echo' command")
		}
		return resp.Bulk([]byte(args[1]))
	case "QUIT":
		return resp.Simple("OK")
	case "SELECT":
		if len(args) != 2 {
			return resp.Error("ERR wrong number of arguments for 'select' command")
		}
		db, err := strconv.Atoi(args[1])
		if err != nil || db < 0 {
			return resp.Error("ERR invalid DB index")
		}
		s.DB = db
		return resp.Simple("OK")
	case "CLIENT":
		return r.handleClient(s, args)
	case "INFO":
		return r.info(ctx, s, args)
	case "COMMAND":
		return resp.Array()
	case "DBSIZE":
		return r.dbsize(ctx, s, args)
	case "TYPE":
		return r.keyType(ctx, s, args)
	case "STRLEN":
		return r.strlen(ctx, s, args)
	case "MEMORY":
		return r.memory(ctx, s, args)
	case "MULTI":
		if s.InMulti {
			return resp.Error("ERR MULTI calls can not be nested")
		}
		s.InMulti = true
		s.Queued = nil
		return resp.Simple("OK")
	case "DISCARD":
		if !s.InMulti {
			return resp.Error("ERR DISCARD without MULTI")
		}
		s.InMulti = false
		s.Queued = nil
		return resp.Simple("OK")
	case "EXEC":
		if !s.InMulti {
			return resp.Error("ERR EXEC without MULTI")
		}
		queued := s.Queued
		s.InMulti = false
		s.Queued = nil
		items := make([][]byte, 0, len(queued))
		for _, q := range queued {
			items = append(items, r.execute(ctx, s, q))
		}
		return resp.Array(items...)
	case "GET":
		return r.get(ctx, s, args)
	case "SET":
		return r.set(ctx, s, args)
	case "SETEX":
		return r.setex(ctx, s, args)
	case "DEL":
		return r.del(ctx, s, args)
	case "EXISTS":
		return r.exists(ctx, s, args)
	case "EXPIRE":
		return r.expire(ctx, s, args)
	case "TTL":
		return r.ttl(ctx, s, args, time.Second)
	case "PTTL":
		return r.ttl(ctx, s, args, time.Millisecond)
	case "INCR":
		return r.incr(ctx, s, args, 1)
	case "DECR":
		return r.incr(ctx, s, args, -1)
	case "INCRBY", "DECRBY":
		return r.incrBy(ctx, s, args, cmd == "DECRBY")
	case "ZADD":
		return r.zadd(ctx, s, args)
	case "ZCARD":
		return r.zcard(ctx, s, args)
	case "ZREM":
		return r.zrem(ctx, s, args)
	case "ZREMRANGEBYSCORE":
		return r.zremRangeByScore(ctx, s, args)
	case "ZRANGE":
		return r.zrange(ctx, s, args)
	case "ZSCORE":
		return r.zscore(ctx, s, args)
	case "ZCOUNT":
		return r.zcount(ctx, s, args)
	case "SCAN":
		return r.scan(ctx, s, args)
	default:
		return resp.Error("ERR unsupported command '" + args[0] + "' in Redge")
	}
}

func (r *Router) info(ctx context.Context, s *Session, args []string) []byte {
	if len(args) > 2 {
		return resp.Error("ERR wrong number of arguments for 'info' command")
	}
	section := "default"
	if len(args) == 2 {
		section = strings.ToLower(args[1])
	}
	var b strings.Builder
	if section == "default" || section == "all" || section == "server" {
		b.WriteString("# Server\r\nredge_version:0.1.0\r\nredis_version:7.2.0\r\n")
	}
	if section == "default" || section == "all" || section == "keyspace" {
		stats, err := r.store.Stats(ctx, s.DB)
		if err != nil {
			return mapErr(err)
		}
		b.WriteString("# Keyspace\r\n")
		if stats.Keys > 0 {
			b.WriteString("db")
			b.WriteString(strconv.Itoa(s.DB))
			b.WriteString(":keys=")
			b.WriteString(strconv.FormatInt(stats.Keys, 10))
			b.WriteString(",expires=0,avg_ttl=0\r\n")
		}
	}
	return resp.Bulk([]byte(b.String()))
}

func (r *Router) dbsize(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 1 {
		return resp.Error("ERR wrong number of arguments for 'dbsize' command")
	}
	stats, err := r.store.Stats(ctx, s.DB)
	if err != nil {
		return mapErr(err)
	}
	return resp.Int(stats.Keys)
}

func (r *Router) keyType(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 2 {
		return resp.Error("ERR wrong number of arguments for 'type' command")
	}
	typ, err := r.store.Type(ctx, s.DB, args[1])
	if err != nil {
		return mapErr(err)
	}
	return resp.Simple(typ)
}

func (r *Router) strlen(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 2 {
		return resp.Error("ERR wrong number of arguments for 'strlen' command")
	}
	v, err := r.store.Get(ctx, s.DB, args[1])
	if err != nil {
		return mapErr(err)
	}
	if v == nil {
		return resp.Int(0)
	}
	return resp.Int(int64(len(v.Data)))
}

func (r *Router) memory(ctx context.Context, s *Session, args []string) []byte {
	if len(args) < 3 || strings.ToUpper(args[1]) != "USAGE" {
		return resp.Error("ERR unsupported MEMORY subcommand")
	}
	key := args[2]
	typ, err := r.store.Type(ctx, s.DB, key)
	if err != nil {
		return mapErr(err)
	}
	switch typ {
	case store.TypeNone:
		return resp.NullBulk()
	case store.TypeString:
		v, err := r.store.Get(ctx, s.DB, key)
		if err != nil {
			return mapErr(err)
		}
		if v == nil {
			return resp.NullBulk()
		}
		return resp.Int(int64(len(key) + len(v.Data)))
	case store.TypeZSet:
		n, err := r.store.ZCard(ctx, s.DB, key)
		if err != nil {
			return mapErr(err)
		}
		return resp.Int(int64(len(key)) + n*64)
	default:
		return resp.NullBulk()
	}
}

func (r *Router) setex(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 4 {
		return resp.Error("ERR wrong number of arguments for 'setex' command")
	}
	sec, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil || sec <= 0 {
		return resp.Error("ERR invalid expire time in 'setex' command")
	}
	_, wrote, err := r.store.Set(ctx, s.DB, args[1], []byte(args[3]), store.SetOptions{
		TTL: time.Duration(sec) * time.Second,
	})
	if err != nil {
		return mapErr(err)
	}
	r.cache.Del(cacheKey(s.DB, args[1]))
	if !wrote {
		return resp.NullBulk()
	}
	return resp.Simple("OK")
}

func (r *Router) handleClient(s *Session, args []string) []byte {
	if len(args) >= 3 && strings.ToUpper(args[1]) == "SETNAME" {
		s.Name = args[2]
		return resp.Simple("OK")
	}
	if len(args) >= 2 && strings.ToUpper(args[1]) == "GETNAME" {
		if s.Name == "" {
			return resp.NullBulk()
		}
		return resp.Bulk([]byte(s.Name))
	}
	return resp.Error("ERR unsupported CLIENT subcommand")
}

func (r *Router) get(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 2 {
		return resp.Error("ERR wrong number of arguments for 'get' command")
	}
	ck := cacheKey(s.DB, args[1])
	if val, ok := r.cache.Get(ck); ok {
		return resp.Bulk(val)
	}
	v, err := r.store.Get(ctx, s.DB, args[1])
	if err != nil {
		return mapErr(err)
	}
	if v == nil {
		return resp.NullBulk()
	}
	ttl := time.Duration(0)
	if v.ExpiresAt != nil {
		ttl = time.Until(*v.ExpiresAt)
	}
	r.cache.Set(ck, v.Data, ttl)
	return resp.Bulk(v.Data)
}

func (r *Router) set(ctx context.Context, s *Session, args []string) []byte {
	if len(args) < 3 {
		return resp.Error("ERR wrong number of arguments for 'set' command")
	}
	opts := store.SetOptions{}
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "EX":
			if i+1 >= len(args) {
				return resp.Error("ERR syntax error")
			}
			sec, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || sec <= 0 {
				return resp.Error("ERR invalid expire time in 'set' command")
			}
			opts.TTL = time.Duration(sec) * time.Second
			i++
		case "PX":
			if i+1 >= len(args) {
				return resp.Error("ERR syntax error")
			}
			ms, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || ms <= 0 {
				return resp.Error("ERR invalid expire time in 'set' command")
			}
			opts.TTL = time.Duration(ms) * time.Millisecond
			i++
		case "NX":
			opts.NX = true
		case "XX":
			opts.XX = true
		case "GET":
			opts.Get = true
		case "KEEPTTL":
			opts.KeepTTL = true
		default:
			return resp.Error("ERR syntax error")
		}
	}
	old, wrote, err := r.store.Set(ctx, s.DB, args[1], []byte(args[2]), opts)
	if err != nil {
		return mapErr(err)
	}
	r.cache.Del(cacheKey(s.DB, args[1]))
	if opts.Get {
		if old == nil {
			return resp.NullBulk()
		}
		return resp.Bulk(old.Data)
	}
	if !wrote {
		return resp.NullBulk()
	}
	return resp.Simple("OK")
}

func (r *Router) del(ctx context.Context, s *Session, args []string) []byte {
	if len(args) < 2 {
		return resp.Error("ERR wrong number of arguments for 'del' command")
	}
	n, err := r.store.Delete(ctx, s.DB, args[1:]...)
	if err != nil {
		return mapErr(err)
	}
	for _, key := range args[1:] {
		r.cache.Del(cacheKey(s.DB, key))
	}
	return resp.Int(n)
}

func (r *Router) exists(ctx context.Context, s *Session, args []string) []byte {
	if len(args) < 2 {
		return resp.Error("ERR wrong number of arguments for 'exists' command")
	}
	n, err := r.store.Exists(ctx, s.DB, args[1:]...)
	if err != nil {
		return mapErr(err)
	}
	return resp.Int(n)
}

func (r *Router) expire(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 3 {
		return resp.Error("ERR wrong number of arguments for 'expire' command")
	}
	sec, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	ok, err := r.store.Expire(ctx, s.DB, args[1], time.Duration(sec)*time.Second)
	if err != nil {
		return mapErr(err)
	}
	r.cache.Del(cacheKey(s.DB, args[1]))
	if ok {
		return resp.Int(1)
	}
	return resp.Int(0)
}

func (r *Router) ttl(ctx context.Context, s *Session, args []string, unit time.Duration) []byte {
	if len(args) != 2 {
		return resp.Error("ERR wrong number of arguments for 'ttl' command")
	}
	d, exists, hasTTL, err := r.store.TTL(ctx, s.DB, args[1])
	if err != nil {
		return mapErr(err)
	}
	if !exists {
		return resp.Int(-2)
	}
	if !hasTTL {
		return resp.Int(-1)
	}
	return resp.Int(int64(d / unit))
}

func (r *Router) incr(ctx context.Context, s *Session, args []string, delta int64) []byte {
	if len(args) != 2 {
		return resp.Error("ERR wrong number of arguments for '" + strings.ToLower(args[0]) + "' command")
	}
	n, err := r.store.IncrBy(ctx, s.DB, args[1], delta)
	if err != nil {
		return mapErr(err)
	}
	r.cache.Del(cacheKey(s.DB, args[1]))
	return resp.Int(n)
}

func (r *Router) incrBy(ctx context.Context, s *Session, args []string, neg bool) []byte {
	if len(args) != 3 {
		return resp.Error("ERR wrong number of arguments for '" + strings.ToLower(args[0]) + "' command")
	}
	d, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	if neg {
		d = -d
	}
	return r.incr(ctx, s, []string{args[0], args[1]}, d)
}

func (r *Router) zadd(ctx context.Context, s *Session, args []string) []byte {
	if len(args) < 4 || len(args)%2 != 0 {
		return resp.Error("ERR wrong number of arguments for 'zadd' command")
	}
	var added int64
	for i := 2; i < len(args); i += 2 {
		score, err := strconv.ParseFloat(args[i], 64)
		if err != nil {
			return resp.Error("ERR value is not a valid float")
		}
		n, err := r.store.ZAdd(ctx, s.DB, args[1], score, []byte(args[i+1]))
		if err != nil {
			return mapErr(err)
		}
		added += n
	}
	r.cache.Del(cacheKey(s.DB, args[1]))
	return resp.Int(added)
}

func (r *Router) zcard(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 2 {
		return resp.Error("ERR wrong number of arguments for 'zcard' command")
	}
	n, err := r.store.ZCard(ctx, s.DB, args[1])
	if err != nil {
		return mapErr(err)
	}
	return resp.Int(n)
}

func (r *Router) zrem(ctx context.Context, s *Session, args []string) []byte {
	if len(args) < 3 {
		return resp.Error("ERR wrong number of arguments for 'zrem' command")
	}
	members := make([][]byte, 0, len(args)-2)
	for _, arg := range args[2:] {
		members = append(members, []byte(arg))
	}
	n, err := r.store.ZRem(ctx, s.DB, args[1], members...)
	if err != nil {
		return mapErr(err)
	}
	r.cache.Del(cacheKey(s.DB, args[1]))
	return resp.Int(n)
}

func (r *Router) zremRangeByScore(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 4 {
		return resp.Error("ERR wrong number of arguments for 'zremrangebyscore' command")
	}
	min, err := parseScoreBound(args[2])
	if err != nil {
		return resp.Error("ERR min or max is not a float")
	}
	max, err := parseScoreBound(args[3])
	if err != nil {
		return resp.Error("ERR min or max is not a float")
	}
	n, err := r.store.ZRemRangeByScore(ctx, s.DB, args[1], min, max)
	if err != nil {
		return mapErr(err)
	}
	r.cache.Del(cacheKey(s.DB, args[1]))
	return resp.Int(n)
}

func (r *Router) zrange(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 4 && len(args) != 5 {
		return resp.Error("ERR wrong number of arguments for 'zrange' command")
	}
	start, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	stop, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return resp.Error("ERR value is not an integer or out of range")
	}
	withScores := false
	if len(args) == 5 {
		if strings.ToUpper(args[4]) != "WITHSCORES" {
			return resp.Error("ERR syntax error")
		}
		withScores = true
	}
	members, err := r.store.ZRange(ctx, s.DB, args[1], start, stop)
	if err != nil {
		return mapErr(err)
	}
	items := make([][]byte, 0, len(members)*2)
	for _, member := range members {
		items = append(items, resp.Bulk(member.Member))
		if withScores {
			items = append(items, resp.Bulk([]byte(formatScore(member.Score))))
		}
	}
	return resp.Array(items...)
}

func (r *Router) zscore(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 3 {
		return resp.Error("ERR wrong number of arguments for 'zscore' command")
	}
	score, ok, err := r.store.ZScore(ctx, s.DB, args[1], []byte(args[2]))
	if err != nil {
		return mapErr(err)
	}
	if !ok {
		return resp.NullBulk()
	}
	return resp.Bulk([]byte(formatScore(score)))
}

func (r *Router) zcount(ctx context.Context, s *Session, args []string) []byte {
	if len(args) != 4 {
		return resp.Error("ERR wrong number of arguments for 'zcount' command")
	}
	min, err := parseScoreBound(args[2])
	if err != nil {
		return resp.Error("ERR min or max is not a float")
	}
	max, err := parseScoreBound(args[3])
	if err != nil {
		return resp.Error("ERR min or max is not a float")
	}
	n, err := r.store.ZCount(ctx, s.DB, args[1], min, max)
	if err != nil {
		return mapErr(err)
	}
	return resp.Int(n)
}

func (r *Router) scan(ctx context.Context, s *Session, args []string) []byte {
	if len(args) < 2 {
		return resp.Error("ERR wrong number of arguments for 'scan' command")
	}
	cursor := args[1]
	pattern := "*"
	count := 10
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "MATCH":
			if i+1 >= len(args) {
				return resp.Error("ERR syntax error")
			}
			pattern = args[i+1]
			i++
		case "COUNT":
			if i+1 >= len(args) {
				return resp.Error("ERR syntax error")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return resp.Error("ERR value is not an integer or out of range")
			}
			count = n
			i++
		default:
			return resp.Error("ERR syntax error")
		}
	}
	result, err := r.store.Scan(ctx, s.DB, cursor, pattern, count)
	if err != nil {
		return mapErr(err)
	}
	keys := make([][]byte, 0, len(result.Keys))
	for _, key := range result.Keys {
		keys = append(keys, resp.Bulk([]byte(key)))
	}
	return resp.Array(resp.Bulk([]byte(result.Cursor)), resp.Array(keys...))
}

func parseScoreBound(raw string) (store.ScoreBound, error) {
	bound := store.ScoreBound{}
	if strings.HasPrefix(raw, "(") {
		bound.Exclusive = true
		raw = strings.TrimPrefix(raw, "(")
	}
	switch strings.ToLower(raw) {
	case "-inf":
		bound.Infinite = -1
		return bound, nil
	case "+inf", "inf":
		bound.Infinite = 1
		return bound, nil
	default:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return store.ScoreBound{}, err
		}
		bound.Value = n
		return bound, nil
	}
}

func formatScore(score float64) string {
	return strconv.FormatFloat(score, 'f', -1, 64)
}

func mapErr(err error) []byte {
	switch {
	case errors.Is(err, store.ErrWrongType):
		return resp.Error(store.ErrWrongType.Error())
	case errors.Is(err, store.ErrNotInt):
		return resp.Error(store.ErrNotInt.Error())
	default:
		return resp.Error("ERR " + err.Error())
	}
}

func cacheKey(db int, key string) string {
	return strconv.Itoa(db) + ":" + key
}
