// Package registry 管理书源实例的生命周期：创建、查找、续期、过期回收。
//
// 过期语义是「滑动空闲 TTL」：每次 Get 都会刷新 lastUsed，实例在 TTL 内无访问即被回收；
// 同时可用 MaxTTL 给实例设置绝对上限，避免被无限续期。
package registry

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"legado-go/internal/booksource"
	"legado-go/internal/model"
)

// Options 是注册表配置
type Options struct {
	// DefaultTTL 创建时未指定 TTL 的默认值（默认 30m）
	DefaultTTL time.Duration
	// MaxTTL 单个实例允许的最大存活时间（0 表示不限）
	MaxTTL time.Duration
	// MaxInstances 最多同时存在的实例数（0 表示不限）
	MaxInstances int
	// SweepInterval 回收扫描间隔（默认 5s；<=0 表示不启动后台扫描）
	SweepInterval time.Duration
	// SourceOptions 传给每个书源实例的选项
	SourceOptions booksource.Options
	// GracePeriod 实例被回收后墓碑的保留时长（默认 10m，期间访问返回 EXPIRED 而非 NOT_FOUND）
	GracePeriod time.Duration
	// OnEvict 实例被回收时的回调
	OnEvict func(info Info)
}

// Entry 是一个书源实例条目
type Entry struct {
	ID       string
	Source   *booksource.Source
	Created  time.Time
	LastUsed time.Time
	TTL      time.Duration
	Owner    string
}

// Info 是实例的对外视图
type Info struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Group     string    `json:"group,omitempty"`
	Owner     string    `json:"owner,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed"`
	TTL       int       `json:"ttlSeconds"`
	Remaining int       `json:"remainingSeconds"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Stats 是注册表统计
type Stats struct {
	Instances int `json:"instances"`
	Sweeps    int `json:"sweeps"`
	Evicted   int `json:"evicted"`
	Created   int `json:"created"`
}

// Registry 是线程安全的实例注册表
type Registry struct {
	opts   Options
	mu     sync.Mutex
	items  map[string]*Entry
	graves map[string]time.Time
	stats  Stats
	stop   chan struct{}
	once   sync.Once
}

// New 创建注册表并（按需）启动回收协程
func New(opts Options) *Registry {
	if opts.DefaultTTL <= 0 {
		opts.DefaultTTL = 30 * time.Minute
	}
	if opts.SweepInterval == 0 {
		opts.SweepInterval = 5 * time.Second
	}
	if opts.GracePeriod <= 0 {
		opts.GracePeriod = 10 * time.Minute
	}
	r := &Registry{opts: opts, items: map[string]*Entry{}, graves: map[string]time.Time{}, stop: make(chan struct{})}
	if opts.SweepInterval > 0 {
		go r.loop(opts.SweepInterval)
	}
	return r
}

func (r *Registry) loop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.Sweep()
		}
	}
}

// Close 停止回收协程并释放所有实例
func (r *Registry) Close() {
	r.once.Do(func() { close(r.stop) })
	r.mu.Lock()
	entries := make([]*Entry, 0, len(r.items))
	for _, e := range r.items {
		entries = append(entries, e)
	}
	r.items = map[string]*Entry{}
	r.graves = map[string]time.Time{}
	r.mu.Unlock()
	for _, e := range entries {
		_ = e.Source.Close()
	}
}

// Create 由配置创建并登记新实例
func (r *Registry) Create(cfg *model.Config, ttl time.Duration, owner string) (*Entry, error) {
	return r.CreateWithID("", cfg, ttl, owner)
}

// CreateWithID 与 Create 相同，但允许调用方指定实例 ID（便于幂等创建）。
// id 为空时自动生成；格式非法或已存在时返回 INVALID_CONFIG。
func (r *Registry) CreateWithID(id string, cfg *model.Config, ttl time.Duration, owner string) (*Entry, error) {
	if id != "" {
		if !validID(id) {
			return nil, model.NewError(model.KindInvalidConfig, "instance id 非法（仅允许 [A-Za-z0-9._-]，长度 1-64）: %q", id)
		}
	}
	if ttl <= 0 {
		ttl = r.opts.DefaultTTL
	}
	if r.opts.MaxTTL > 0 && ttl > r.opts.MaxTTL {
		ttl = r.opts.MaxTTL
	}
	r.mu.Lock()
	if r.opts.MaxInstances > 0 && len(r.items) >= r.opts.MaxInstances {
		r.mu.Unlock()
		return nil, model.NewError(model.KindLimit, "instance limit reached (%d)", r.opts.MaxInstances)
	}
	if id == "" {
		id = newID()
	}
	r.mu.Unlock()

	src, err := booksource.New(cfg, r.opts.SourceOptions)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	entry := &Entry{ID: id, Source: src, Created: now, LastUsed: now, TTL: ttl, Owner: owner}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opts.MaxInstances > 0 && len(r.items) >= r.opts.MaxInstances {
		_ = src.Close()
		return nil, model.NewError(model.KindLimit, "instance limit reached (%d)", r.opts.MaxInstances)
	}
	if existing, ok := r.items[id]; ok {
		_ = src.Close()
		return nil, model.NewError(model.KindInvalidConfig, "instance id %s 已存在（created %s）", id, existing.Created.Format(time.RFC3339))
	}
	r.items[id] = entry
	delete(r.graves, id) // 同 ID 重建时清掉旧墓碑
	r.stats.Created++
	return entry, nil
}

// Get 查找实例并刷新空闲时间
func (r *Registry) Get(id string) (*Entry, error) {
	r.mu.Lock()
	now := time.Now()
	entry, ok := r.items[id]
	if ok && r.expiredAt(entry, now) {
		r.evictLocked(entry, now)
		r.mu.Unlock()
		_ = entry.Source.Close()
		if r.opts.OnEvict != nil {
			r.opts.OnEvict(entry.info(now))
		}
		return nil, model.NewError(model.KindExpired, "instance %s expired", id)
	}
	if ok {
		entry.LastUsed = now
	}
	buried := !ok && r.buriedLocked(id, now)
	r.mu.Unlock()
	if buried {
		return nil, model.NewError(model.KindExpired, "instance %s expired", id)
	}
	if !ok {
		return nil, model.NewError(model.KindNotFound, "instance %s not found", id)
	}
	return entry, nil
}

// GetSource 是 Get 的便捷版，直接返回书源实例（同时刷新空闲时间）。
func (r *Registry) GetSource(id string) (*booksource.Source, error) {
	entry, err := r.Get(id)
	if err != nil {
		return nil, err
	}
	return entry.Source, nil
}

// Lookup 查找实例但不刷新空闲时间
func (r *Registry) Lookup(id string) (*Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.items[id]
	if !ok {
		if r.buriedLocked(id, time.Now()) {
			return nil, model.NewError(model.KindExpired, "instance %s expired", id)
		}
		return nil, model.NewError(model.KindNotFound, "instance %s not found", id)
	}
	if r.expiredAt(entry, time.Now()) {
		return nil, model.NewError(model.KindExpired, "instance %s expired", id)
	}
	return entry, nil
}

// Touch 手动续期
func (r *Registry) Touch(id string, ttl time.Duration) (*Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.items[id]
	if !ok {
		if r.buriedLocked(id, time.Now()) {
			return nil, model.NewError(model.KindExpired, "instance %s expired", id)
		}
		return nil, model.NewError(model.KindNotFound, "instance %s not found", id)
	}
	now := time.Now()
	if r.expiredAt(entry, now) {
		r.evictLocked(entry, now)
		go func() { _ = entry.Source.Close() }()
		return nil, model.NewError(model.KindExpired, "instance %s expired", id)
	}
	if ttl <= 0 {
		ttl = entry.TTL
	}
	if r.opts.MaxTTL > 0 && ttl > r.opts.MaxTTL {
		ttl = r.opts.MaxTTL
	}
	entry.TTL = ttl
	entry.LastUsed = now
	return entry, nil
}

// Delete 主动删除实例
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	entry, ok := r.items[id]
	if ok {
		delete(r.items, id)
	}
	r.mu.Unlock()
	if !ok {
		return model.NewError(model.KindNotFound, "instance %s not found", id)
	}
	return entry.Source.Close()
}

// List 返回所有实例视图
func (r *Registry) List() []Info {
	r.mu.Lock()
	now := time.Now()
	out := make([]Info, 0, len(r.items))
	for _, e := range r.items {
		if r.expiredAt(e, now) {
			continue
		}
		out = append(out, e.info(now))
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Sweep 回收过期实例，返回回收数量
func (r *Registry) Sweep() int {
	now := time.Now()
	var expired []*Entry
	r.mu.Lock()
	for _, e := range r.items {
		if r.expiredAt(e, now) {
			r.evictLocked(e, now)
			expired = append(expired, e)
		}
	}
	r.pruneGravesLocked(now)
	r.stats.Sweeps++
	r.mu.Unlock()
	for _, e := range expired {
		_ = e.Source.Close()
		if r.opts.OnEvict != nil {
			r.opts.OnEvict(e.info(now))
		}
	}
	return len(expired)
}

// DefaultTTL 返回默认 TTL
func (r *Registry) DefaultTTL() time.Duration { return r.opts.DefaultTTL }

// Stats 返回统计信息
func (r *Registry) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Instances = len(r.items)
	return s
}

// evictLocked 移除实例并留下墓碑（调用方需持有锁）
func (r *Registry) evictLocked(e *Entry, now time.Time) {
	delete(r.items, e.ID)
	r.graves[e.ID] = now
	r.stats.Evicted++
	if len(r.graves) > 4096 {
		r.pruneGravesLocked(now)
	}
}

// buriedLocked 判断 id 是否在墓碑保留期内
func (r *Registry) buriedLocked(id string, now time.Time) bool {
	at, ok := r.graves[id]
	if !ok {
		return false
	}
	if now.Sub(at) > r.opts.GracePeriod {
		delete(r.graves, id)
		return false
	}
	return true
}

// pruneGravesLocked 清理过期的墓碑
func (r *Registry) pruneGravesLocked(now time.Time) {
	for id, at := range r.graves {
		if now.Sub(at) > r.opts.GracePeriod {
			delete(r.graves, id)
		}
	}
}

func (r *Registry) expiredAt(e *Entry, now time.Time) bool {
	if now.Sub(e.LastUsed) > e.TTL {
		return true
	}
	if r.opts.MaxTTL > 0 && now.Sub(e.Created) > r.opts.MaxTTL {
		return true
	}
	return false
}

// Info 返回条目视图
func (e *Entry) Info() Info { return e.info(time.Now()) }

// TTLOf 返回条目 TTL
func (e *Entry) TTLOf() time.Duration { return e.TTL }

func (e *Entry) info(now time.Time) Info {
	expires := e.LastUsed.Add(e.TTL)
	if remaining := expires.Sub(now); remaining < 0 {
		remaining = 0
		return Info{
			ID:        e.ID,
			Name:      e.Source.Name(),
			URL:       e.Source.URL(),
			Group:     e.Source.Config().Group,
			Owner:     e.Owner,
			CreatedAt: e.Created,
			LastUsed:  e.LastUsed,
			TTL:       int(e.TTL.Seconds()),
			Remaining: 0,
			ExpiresAt: expires,
		}
	}
	return Info{
		ID:        e.ID,
		Name:      e.Source.Name(),
		URL:       e.Source.URL(),
		Group:     e.Source.Config().Group,
		Owner:     e.Owner,
		CreatedAt: e.Created,
		LastUsed:  e.LastUsed,
		TTL:       int(e.TTL.Seconds()),
		Remaining: int(expires.Sub(now).Seconds()),
		ExpiresAt: expires,
	}
}

// validID 校验调用方自定义的实例 ID
func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func newID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("src-%d", time.Now().UnixNano())
	}
	return "src-" + hex.EncodeToString(buf)
}
