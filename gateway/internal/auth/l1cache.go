// Package auth (l1cache.go): cache L1 in-process de resultados POSITIVOS de
// verificação de API key (quick 260930-vkt).
//
// Motivação: com Redis fora (erro em GET) todo request caía no caminho caro
// (DB + argon2id 64MiB/3iter) — medido em prod (worker-vm) 50 verifies
// concorrentes = 27s. O L1 segura o hot path enquanto o Redis pisca.
//
// Invariantes de segurança (T-vkt-01 / T-vkt-05):
//   - O L1 só é escrito pelo caminho autoritativo (DB + argon2 OK, status
//     "active"). Hit no Redis NÃO popula o L1 — senão uma entrada Redis de
//     até 60s ganharia mais 30s de vida no L1 e a janela de revogação passaria
//     de 60s.
//   - l1TTL (30s) ≤ cacheTTL (60s) → janela de revogação D-A2 continua ≤ 60s
//     mesmo se a invalidação ativa falhar.
//   - Quick 260930-wpv: revogação ativa. evict() remove a entrada E grava um
//     tombstone (tombstoneTTL) — put() de um lookup que já estava em voo
//     quando a revogação chegou é descartado atomicamente (mesmo mutex), então
//     a key revogada não volta ao L1. flush() esvazia tudo (usado após
//     reconexão do PubSub, quando mensagens podem ter sido perdidas).
//   - Nenhum erro / negativo entra no L1.
//   - Chave do mapa = cacheKeyFor(rawKey) (sha256) — key crua nunca fica em
//     memória como chave.
package auth

import (
	"sync"
	"time"
)

const (
	// l1TTL é a validade de uma entrada L1. DEVE ser ≤ cacheTTL (60s) para
	// preservar a janela de revogação D-A2.
	l1TTL = 30 * time.Second

	// l1MaxEntries limita a memória do L1 (cacheEntry ~200B → ~2MB no teto).
	l1MaxEntries = 10000

	// tombstoneTTL cobre com folga um lookup em voo (sfTimeout = 5s). Key
	// revogada nunca volta a "active", então o tombstone não tem falso
	// positivo; o TTL existe só para limitar memória.
	tombstoneTTL = cacheTTL
)

type l1Item struct {
	entry cacheEntry
	exp   time.Time
}

// l1Cache é um mapa TTL com limite de tamanho. Política de despejo ao encher:
// remove expirados; se ainda cheio, remove uma entrada arbitrária (iteração
// de map). Sem dependência de LRU — o que importa é o teto de memória.
type l1Cache struct {
	mu    sync.Mutex
	items map[string]l1Item
	// tombs: chave → expiração do tombstone de revogação.
	tombs map[string]time.Time
	ttl   time.Duration
	max   int
	now   func() time.Time // injetável nos testes
}

func newL1Cache(ttl time.Duration, max int) *l1Cache {
	return &l1Cache{
		items: make(map[string]l1Item),
		tombs: make(map[string]time.Time),
		ttl:   ttl,
		max:   max,
		now:   time.Now,
	}
}

func (c *l1Cache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return cacheEntry{}, false
	}
	if !c.now().Before(it.exp) {
		delete(c.items, key)
		return cacheEntry{}, false
	}
	return it.entry, true
}

// put grava a entrada, exceto se a chave tem tombstone de revogação vigente
// (retorna false nesse caso).
func (c *l1Cache) put(key string, e cacheEntry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.tombstonedLocked(key, now) {
		return false
	}
	if _, exists := c.items[key]; !exists && len(c.items) >= c.max {
		for k, it := range c.items {
			if !now.Before(it.exp) {
				delete(c.items, k)
			}
		}
		for len(c.items) >= c.max {
			for k := range c.items {
				delete(c.items, k)
				break
			}
		}
	}
	c.items[key] = l1Item{entry: e, exp: now.Add(c.ttl)}
	return true
}

func (c *l1Cache) tombstonedLocked(key string, now time.Time) bool {
	exp, ok := c.tombs[key]
	if !ok {
		return false
	}
	if !now.Before(exp) {
		delete(c.tombs, key)
		return false
	}
	return true
}

// tombstoned informa se a chave foi revogada há menos de tombstoneTTL.
func (c *l1Cache) tombstoned(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tombstonedLocked(key, c.now())
}

// evict remove a entrada e grava o tombstone de revogação.
func (c *l1Cache) evict(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	delete(c.items, key)
	if len(c.tombs) >= c.max {
		for k, exp := range c.tombs {
			if !now.Before(exp) {
				delete(c.tombs, k)
			}
		}
		for len(c.tombs) >= c.max {
			for k := range c.tombs {
				delete(c.tombs, k)
				break
			}
		}
	}
	c.tombs[key] = now.Add(tombstoneTTL)
}

// flush esvazia as entradas (tombstones são mantidos).
func (c *l1Cache) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]l1Item)
}

func (c *l1Cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
