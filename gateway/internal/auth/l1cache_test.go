package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestL1Cache_PutGet(t *testing.T) {
	c := newL1Cache(time.Minute, 10)
	e := cacheEntry{TenantID: "t1", Status: "active"}
	c.put("k", e)
	got, ok := c.get("k")
	if !ok || got != e {
		t.Fatalf("get=%+v ok=%v", got, ok)
	}
	if _, ok := c.get("outra"); ok {
		t.Fatal("miss esperado")
	}
}

func TestL1Cache_TTLExpiry(t *testing.T) {
	c := newL1Cache(time.Second, 10)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.put("k", cacheEntry{TenantID: "t1"})
	now = now.Add(999 * time.Millisecond)
	if _, ok := c.get("k"); !ok {
		t.Fatal("ainda válido")
	}
	now = now.Add(time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Fatal("expirado deve ser miss")
	}
	if c.len() != 0 {
		t.Fatalf("expirado deve ser removido, len=%d", c.len())
	}
}

func TestL1Cache_SizeLimit(t *testing.T) {
	const max = 100
	c := newL1Cache(time.Minute, max)
	for i := 0; i < max+50; i++ {
		c.put(fmt.Sprintf("k%d", i), cacheEntry{TenantID: "t"})
		if c.len() > max {
			t.Fatalf("len=%d > max=%d", c.len(), max)
		}
	}
	if _, ok := c.get(fmt.Sprintf("k%d", max+49)); !ok {
		t.Fatal("última entrada deveria estar no cache")
	}
}

func TestL1Cache_SizeLimitEvictsExpiredFirst(t *testing.T) {
	c := newL1Cache(time.Second, 3)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.put("old1", cacheEntry{})
	c.put("old2", cacheEntry{})
	now = now.Add(500 * time.Millisecond)
	c.put("fresh", cacheEntry{})
	now = now.Add(600 * time.Millisecond) // old1/old2 expirados, fresh não
	c.put("new", cacheEntry{})
	if _, ok := c.get("fresh"); !ok {
		t.Fatal("fresh não deveria ser despejado (havia expirados)")
	}
	if c.len() != 2 {
		t.Fatalf("len=%d want 2", c.len())
	}
}

func TestL1Cache_Concurrent(t *testing.T) {
	c := newL1Cache(time.Minute, 50)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				k := fmt.Sprintf("k%d", (g*500+i)%200)
				c.put(k, cacheEntry{TenantID: k})
				_, _ = c.get(k)
				_ = c.len()
			}
		}(g)
	}
	wg.Wait()
	if c.len() > 50 {
		t.Fatalf("len=%d > 50", c.len())
	}
}
