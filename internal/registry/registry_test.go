package registry_test

import (
	"testing"
	"time"

	"legado-go/internal/model"
	"legado-go/internal/registry"
)

func cfg(name, url string) *model.Config {
	return &model.Config{BookSourceName: name, BookSourceURL: url, SearchURL: "/s?q={{key}}"}
}

func TestCreateGetDelete(t *testing.T) {
	reg := registry.New(registry.Options{DefaultTTL: time.Minute, SweepInterval: -1})
	defer reg.Close()

	entry, err := reg.Create(cfg("源A", "http://a.example"), 0, "tester")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if entry.ID == "" || entry.Source.Name() != "源A" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if got, err := reg.Get(entry.ID); err != nil || got.ID != entry.ID {
		t.Fatalf("get: %v %v", got, err)
	}
	if list := reg.List(); len(list) != 1 {
		t.Fatalf("want 1 instance, got %d", len(list))
	}
	if err := reg.Delete(entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := reg.Get(entry.ID); model.KindOf(err) != model.KindNotFound {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
}

func TestTTLExpiryAndTouch(t *testing.T) {
	reg := registry.New(registry.Options{DefaultTTL: 60 * time.Millisecond, SweepInterval: -1})
	defer reg.Close()

	entry, err := reg.Create(cfg("源B", "http://b.example"), 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(90 * time.Millisecond)
	if _, err := reg.Get(entry.ID); model.KindOf(err) != model.KindExpired {
		t.Fatalf("want EXPIRED on access, got %v", err)
	}
	if list := reg.List(); len(list) != 0 {
		t.Fatalf("expired instance still listed: %+v", list)
	}

	// 续期应重新计时
	entry2, err := reg.Create(cfg("源C", "http://c.example"), 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := reg.Touch(entry2.ID, 200*time.Millisecond); err != nil {
		t.Fatalf("touch: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := reg.Get(entry2.ID); err != nil {
		t.Fatalf("touched instance should be alive: %v", err)
	}
}

func TestSweepEvicts(t *testing.T) {
	var evicted []string
	reg := registry.New(registry.Options{
		DefaultTTL:    30 * time.Millisecond,
		SweepInterval: -1,
		OnEvict:       func(info registry.Info) { evicted = append(evicted, info.ID) },
	})
	defer reg.Close()

	entry, err := reg.Create(cfg("源D", "http://d.example"), 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := reg.Sweep(); n != 1 {
		t.Fatalf("want 1 evicted, got %d", n)
	}
	if len(evicted) != 1 || evicted[0] != entry.ID {
		t.Fatalf("OnEvict not called: %+v", evicted)
	}
	if reg.Stats().Evicted != 1 {
		t.Fatalf("stats not updated: %+v", reg.Stats())
	}
}

func TestMaxInstancesAndMaxTTL(t *testing.T) {
	reg := registry.New(registry.Options{
		DefaultTTL:    time.Hour,
		MaxTTL:        500 * time.Millisecond,
		MaxInstances:  1,
		SweepInterval: -1,
	})
	defer reg.Close()

	if _, err := reg.Create(cfg("源E", "http://e.example"), 0, ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := reg.Create(cfg("源F", "http://f.example"), 0, ""); model.KindOf(err) != model.KindLimit {
		t.Fatalf("want INSTANCE_LIMIT, got %v", err)
	}

	reg2 := registry.New(registry.Options{DefaultTTL: time.Hour, MaxTTL: 50 * time.Millisecond, SweepInterval: -1})
	defer reg2.Close()
	entry, err := reg2.Create(cfg("源G", "http://g.example"), 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(70 * time.Millisecond)
	if _, err := reg2.Get(entry.ID); model.KindOf(err) != model.KindExpired {
		t.Fatalf("want EXPIRED by MaxTTL, got %v", err)
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	reg := registry.New(registry.Options{DefaultTTL: time.Minute, SweepInterval: -1})
	defer reg.Close()
	if _, err := reg.Create(&model.Config{}, 0, ""); model.KindOf(err) != model.KindInvalidConfig {
		t.Fatalf("want INVALID_CONFIG, got %v", err)
	}
}

func TestTombstoneReportsExpiredAfterSweep(t *testing.T) {
	reg := registry.New(registry.Options{
		DefaultTTL:    30 * time.Millisecond,
		SweepInterval: -1,
		GracePeriod:   time.Minute,
	})
	defer reg.Close()

	entry, err := reg.Create(cfg("源E", "http://e.example"), 0, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := reg.Sweep(); n != 1 {
		t.Fatalf("want 1 evicted, got %d", n)
	}
	// 已被后台扫描回收，但墓碑未过期 → 应报 EXPIRED 而非 NOT_FOUND
	_, err = reg.Get(entry.ID)
	if model.KindOf(err) != model.KindExpired {
		t.Fatalf("want EXPIRED after sweep, got %v", err)
	}
	_, err = reg.Lookup("src-does-not-exist")
	if model.KindOf(err) != model.KindNotFound {
		t.Fatalf("unknown id should be NOT_FOUND, got %v", err)
	}
}

func TestCreateWithID(t *testing.T) {
	r := registry.New(registry.Options{DefaultTTL: time.Minute, SweepInterval: -1})
	defer r.Close()
	c := cfg("A", "http://x.example")

	entry, err := r.CreateWithID("my-source", c, 0, "")
	if err != nil {
		t.Fatalf("create with id: %v", err)
	}
	if entry.ID != "my-source" {
		t.Fatalf("id = %s", entry.ID)
	}
	if got, err := r.Get("my-source"); err != nil || got.ID != "my-source" {
		t.Fatalf("get by custom id: %v", err)
	}

	// 重复 id → INVALID_CONFIG
	if _, err := r.CreateWithID("my-source", c, 0, ""); err == nil {
		t.Fatal("expected duplicate id error")
	} else if model.KindOf(err) != model.KindInvalidConfig {
		t.Fatalf("kind = %s", model.KindOf(err))
	}

	// 非法 id
	if _, err := r.CreateWithID("bad id!", c, 0, ""); model.KindOf(err) != model.KindInvalidConfig {
		t.Fatalf("expected invalid id error, got %v", err)
	}

	// 删除后同 id 可重建（墓碑被清理）
	if err := r.Delete("my-source"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.CreateWithID("my-source", c, 0, ""); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

func TestGetSourceConvenience(t *testing.T) {
	reg := registry.New(registry.Options{DefaultTTL: time.Minute, SweepInterval: -1})
	defer reg.Close()

	entry, err := reg.Create(cfg("源A", "http://a.example"), 0, "tester")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	src, err := reg.GetSource(entry.ID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if src.Name() != "源A" {
		t.Fatalf("name = %q", src.Name())
	}
	if _, err := reg.GetSource("nope"); model.KindOf(err) != model.KindNotFound {
		t.Fatalf("missing: %v", err)
	}
}
