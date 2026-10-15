package proxy

// Phase 23-05 (HAIKU-02): pool de chaves da API Anthropic das contas Max.
//
// Decisões travadas (23-CONTEXT "Chave esgotada"):
//   - ordem FIXA de uso (primeira chave com saldo), nunca rodízio por chamada —
//     o cache de prompt da Anthropic é por organização, trocar de chave esfria
//     o cache;
//   - chave esgotada fica marcada até o dia de renovação do ciclo da SUA conta
//     Max (não "até dia 1º"), com re-teste a cada AnthropicKeyRetestInterval
//     usando a própria requisição real; um 200 libera antes da renovação;
//   - chave revogada (401) não tem data de liberação: re-teste a cada 24 h.
//
// A troca de chave acontece dentro do RoundTrip, ANTES de qualquer byte ir ao
// cliente (respostas de erro de chave são JSON curtos, drenados e descartados).
// Sem chave utilizável o transporte devolve errNoUsableAnthropicKey, que casa
// errUpstreamRetryable (o ErrorHandler suprime a escrita e o dispatcher
// cascateia para o passo ②) e expõe SkipBreaker()=true (contrato do 23-04:
// falta de crédito não é degradação do upstream, não abre breaker).
//
// Segredo: AnthropicKey.secret não é exportado; log, erro, Redis e Snapshot só
// carregam rótulo + fingerprint (sha256 hex [:8]).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// AnthropicKeyStatePrefix é o prefixo das chaves Redis do estado do pool
// (sufixo = fingerprint). Valor JSON {state,until,next_probe_at,reason}.
const AnthropicKeyStatePrefix = "gw:anthropic:key:"

// AnthropicKeyRetestInterval é o intervalo de re-teste de uma chave esgotada.
const AnthropicKeyRetestInterval = 6 * time.Hour

// anthropicRevokedRetestInterval é o intervalo de re-teste de uma chave revogada.
const anthropicRevokedRetestInterval = 24 * time.Hour

// anthropicDefaultCooldown é o cooldown de um 429 de rate limit sem retry-after.
const anthropicDefaultCooldown = 30 * time.Second

// anthropicAPIVersion é o header anthropic-version padrão.
const anthropicAPIVersion = "2023-06-01"

// anthropicErrPeekMax limita o quanto do corpo de erro é lido para classificar.
const anthropicErrPeekMax = 8 << 10

// anthropicRedisTimeout limita cada operação Redis do pool (não pode travar a
// requisição do cliente quando o Redis está fora).
const anthropicRedisTimeout = 300 * time.Millisecond

// Estados expostos em AnthropicKeyStatus.State.
const (
	anthropicStateOK        = "ok"
	anthropicStateExhausted = "exhausted"
	anthropicStateCooldown  = "cooldown"
	anthropicStateRevoked   = "revoked"
)

// ---------------------------------------------------------------------------
// Chaves
// ---------------------------------------------------------------------------

// AnthropicKey é uma chave de API de uma conta Max. O segredo não é exportado.
type AnthropicKey struct {
	Label       string
	Fingerprint string
	secret      string
	// workspace é o anthropic-workspace-id exigido por chave de usuário
	// (sk-ant-usr-) que não é presa a um workspace; "" = não envia o header.
	workspace string
	// RenewalDay é o dia (1–31) de renovação do ciclo da conta; 0 = desconhecido
	// (esgotada fica marcada só até o próximo re-teste).
	RenewalDay int
}

// String devolve só rótulo + fingerprint — nunca o segredo.
func (k AnthropicKey) String() string {
	return k.Label + "(" + k.Fingerprint + ")"
}

func anthropicFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:8]
}

// ParseAnthropicKeys lê o env UPSTREAM_LLM_ANTHROPIC_API_KEYS no formato
// `rotulo=<chave>[#<workspace>][@<dia>],rotulo2=<chave>` (ordem = ordem de
// uso; `@<dia>` opcional, 1–31; `#<workspace>` opcional, vira o header
// anthropic-workspace-id — obrigatório para chave não presa a workspace).
// Vazio → (nil, nil). Erros citam só o índice do item, nunca o conteúdo.
func ParseAnthropicKeys(raw string) ([]AnthropicKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	items := strings.Split(raw, ",")
	keys := make([]AnthropicKey, 0, len(items))
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		eq := strings.Index(item, "=")
		if eq <= 0 || eq == len(item)-1 {
			return nil, fmt.Errorf("UPSTREAM_LLM_ANTHROPIC_API_KEYS: item %d malformado (esperado rotulo=<chave>[@dia])", i+1)
		}
		label := strings.TrimSpace(item[:eq])
		secret := strings.TrimSpace(item[eq+1:])
		day := 0
		if at := strings.LastIndex(secret, "@"); at >= 0 {
			suffix := secret[at+1:]
			if n, err := strconv.Atoi(suffix); err == nil && suffix != "" {
				if n < 1 || n > 31 {
					return nil, fmt.Errorf("UPSTREAM_LLM_ANTHROPIC_API_KEYS: item %d com dia de renovação fora de 1-31", i+1)
				}
				day = n
				secret = strings.TrimSpace(secret[:at])
			}
		}
		workspace := ""
		if h := strings.Index(secret, "#"); h >= 0 {
			workspace = strings.TrimSpace(secret[h+1:])
			secret = strings.TrimSpace(secret[:h])
			if workspace == "" {
				return nil, fmt.Errorf("UPSTREAM_LLM_ANTHROPIC_API_KEYS: item %d com workspace vazio após '#'", i+1)
			}
		}
		if label == "" || secret == "" {
			return nil, fmt.Errorf("UPSTREAM_LLM_ANTHROPIC_API_KEYS: item %d malformado (rótulo ou chave vazios)", i+1)
		}
		if seen[label] {
			return nil, fmt.Errorf("UPSTREAM_LLM_ANTHROPIC_API_KEYS: item %d com rótulo duplicado", i+1)
		}
		seen[label] = true
		keys = append(keys, AnthropicKey{
			Label:       label,
			Fingerprint: anthropicFingerprint(secret),
			secret:      secret,
			workspace:   workspace,
			RenewalDay:  day,
		})
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return keys, nil
}

// ---------------------------------------------------------------------------
// Classificador
// ---------------------------------------------------------------------------

type keyVerdict int

const (
	keyOK         keyVerdict = iota
	keyExhausted             // créditos/spend cap: até a renovação
	keyRevoked               // 401
	keyCooldown              // 429 rate limit com retry-after
	notKeyProblem            // 5xx/529/400 de validação: não troca chave
)

func (v keyVerdict) String() string {
	switch v {
	case keyOK:
		return "ok"
	case keyExhausted:
		return anthropicStateExhausted
	case keyRevoked:
		return anthropicStateRevoked
	case keyCooldown:
		return anthropicStateCooldown
	default:
		return "not_key_problem"
	}
}

// classifyAnthropicKeyError decide se uma resposta de erro da Anthropic é
// problema da CHAVE (troca de chave) ou não (resposta segue para o cliente /
// interceptor de status). Fontes: platform.claude.com/docs/en/api/errors,
// /api/rate-limits, /about-claude/api-credits-for-subscribers.
// HIPÓTESE H1 (RESEARCH): o status exato de "credit balance is too low" não é
// documentado — casa por substring em qualquer 4xx.
func classifyAnthropicKeyError(status int, body []byte, h http.Header) (keyVerdict, time.Duration) {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Details struct {
				ErrorCode string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	msg := strings.ToLower(env.Error.Message)
	is4xx := status >= 400 && status < 500
	switch {
	case is4xx && strings.Contains(msg, "credit balance is too low"):
		return keyExhausted, 0
	case status == http.StatusPaymentRequired:
		return keyExhausted, 0
	case status == http.StatusTooManyRequests && env.Error.Details.ErrorCode == "enforced_spend_limit_reached":
		return keyExhausted, 0
	case status == http.StatusBadRequest && strings.Contains(msg, "reached your specified"):
		return keyExhausted, 0
	case status == http.StatusUnauthorized:
		return keyRevoked, 0
	case status == http.StatusTooManyRequests:
		if s, err := strconv.Atoi(strings.TrimSpace(h.Get("retry-after"))); err == nil && s >= 0 {
			return keyCooldown, time.Duration(s) * time.Second
		}
		return keyCooldown, anthropicDefaultCooldown
	}
	return notKeyProblem, 0
}

// ---------------------------------------------------------------------------
// Erro sem chave utilizável
// ---------------------------------------------------------------------------

type noUsableAnthropicKeyError struct{}

func (*noUsableAnthropicKeyError) Error() string {
	return "proxy: nenhuma chave anthropic utilizável (esgotadas, revogadas ou em cooldown)"
}

// Is casa errUpstreamRetryable: o ErrorHandler suprime a escrita e o
// dispatcher cascateia para o próximo candidato (passo ②).
func (*noUsableAnthropicKeyError) Is(target error) bool { return target == errUpstreamRetryable }

// SkipBreaker: falta de chave não é degradação do upstream — não abre breaker.
func (*noUsableAnthropicKeyError) SkipBreaker() bool { return true }

var errNoUsableAnthropicKey error = &noUsableAnthropicKeyError{}

// ---------------------------------------------------------------------------
// Estado
// ---------------------------------------------------------------------------

// anthropicKeyState é o valor JSON gravado no Redis e espelhado em memória.
type anthropicKeyState struct {
	State       string     `json:"state"`
	Until       *time.Time `json:"until,omitempty"`
	NextProbeAt *time.Time `json:"next_probe_at,omitempty"`
	Reason      string     `json:"reason"`
}

// blockedUntil é o instante a partir do qual a chave volta a ser tentada.
func (s *anthropicKeyState) blockedUntil() time.Time {
	if s.State == anthropicStateCooldown {
		if s.Until != nil {
			return *s.Until
		}
		return time.Time{}
	}
	if s.NextProbeAt != nil {
		return *s.NextProbeAt
	}
	return time.Time{}
}

// expired: esgotada/cooldown cuja data de liberação já passou (renovação do
// ciclo ou fim do cooldown) — a marcação deixa de valer.
func (s *anthropicKeyState) expired(now time.Time) bool {
	if s.State == anthropicStateRevoked {
		return false
	}
	return s.Until != nil && !now.Before(*s.Until)
}

// ttl da entrada Redis: o maior dos instantes relevantes − now + 1h.
func (s *anthropicKeyState) ttl(now time.Time) time.Duration {
	end := s.blockedUntil()
	if s.Until != nil && s.Until.After(end) {
		end = *s.Until
	}
	d := end.Sub(now) + time.Hour
	if d < time.Hour {
		d = time.Hour
	}
	return d
}

// moreRestrictive devolve a marcação que bloqueia por mais tempo.
func moreRestrictive(a, b *anthropicKeyState) *anthropicKeyState {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.blockedUntil().After(a.blockedUntil()):
		return b
	default:
		return a
	}
}

// AnthropicKeyStatus é a visão de uma chave para o admin (23-12).
type AnthropicKeyStatus struct {
	Label       string     `json:"label"`
	Fingerprint string     `json:"fingerprint"`
	State       string     `json:"state"`
	Until       *time.Time `json:"until,omitempty"`
	NextProbeAt *time.Time `json:"next_probe_at,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

// KeyPool mantém as chaves em ordem fixa e o estado de cada uma (memória +
// Redis, valendo sempre o mais restritivo dos dois).
type KeyPool struct {
	keys []AnthropicKey
	rdb  redis.Cmdable
	now  func() time.Time
	loc  *time.Location
	log  *slog.Logger

	mu             sync.Mutex
	mem            map[string]*anthropicKeyState // por fingerprint
	pendingDeletes map[string]bool               // limpezas que não chegaram ao Redis
	redisDown      bool
	redisDirty     bool
}

// NewKeyPool cria o pool. rdb nil = só memória. loc nil = America/Sao_Paulo
// (fallback UTC). now nil = time.Now.
func NewKeyPool(keys []AnthropicKey, rdb redis.Cmdable, now func() time.Time, loc *time.Location, log *slog.Logger) *KeyPool {
	if now == nil {
		now = time.Now
	}
	if loc == nil {
		if l, err := time.LoadLocation("America/Sao_Paulo"); err == nil {
			loc = l
		} else {
			loc = time.UTC
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &KeyPool{
		keys:           append([]AnthropicKey(nil), keys...),
		rdb:            rdb,
		now:            now,
		loc:            loc,
		log:            log,
		mem:            make(map[string]*anthropicKeyState, len(keys)),
		pendingDeletes: make(map[string]bool),
	}
}

func (p *KeyPool) redisKey(fp string) string { return AnthropicKeyStatePrefix + fp }

func (p *KeyPool) redisCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), anthropicRedisTimeout)
}

// redisFailed registra falha de operação Redis (WARN só na transição).
func (p *KeyPool) redisFailed(err error) {
	p.mu.Lock()
	wasDown := p.redisDown
	p.redisDown = true
	p.mu.Unlock()
	if !wasDown {
		p.log.Warn("anthropic keypool: redis indisponível; usando estado em memória", "err", err)
	}
}

// redisOK registra operação Redis bem-sucedida: se estava fora, loga a volta;
// se há marcações só em memória, regrava-as (reconciliação).
func (p *KeyPool) redisOK(ctx context.Context) {
	p.mu.Lock()
	wasDown := p.redisDown
	p.redisDown = false
	dirty := p.redisDirty
	p.mu.Unlock()
	if wasDown {
		p.log.Warn("anthropic keypool: redis recuperado; reconciliando estado")
	}
	if dirty {
		p.reconcile(ctx)
	}
}

// reconcile regrava no Redis as marcações em memória ainda no futuro e aplica
// as limpezas pendentes.
func (p *KeyPool) reconcile(ctx context.Context) {
	now := p.now()
	p.mu.Lock()
	type write struct {
		fp      string
		buf     []byte
		ttl     time.Duration
		blocked time.Time
	}
	var writes []write
	for fp, st := range p.mem {
		if st.expired(now) {
			continue
		}
		buf, err := json.Marshal(st)
		if err != nil {
			continue
		}
		writes = append(writes, write{fp: fp, buf: buf, ttl: st.ttl(now), blocked: st.blockedUntil()})
	}
	var dels []string
	for fp := range p.pendingDeletes {
		dels = append(dels, fp)
	}
	p.redisDirty = false
	p.mu.Unlock()

	rctx, cancel := p.redisCtx(ctx)
	defer cancel()
	failed := false
	for _, w := range writes {
		// Não sobrescreve marcação mais restritiva de outra réplica.
		if cur := p.readOne(rctx, w.fp); cur != nil && cur.blockedUntil().After(w.blocked) {
			continue
		}
		if err := p.rdb.Set(rctx, p.redisKey(w.fp), string(w.buf), w.ttl).Err(); err != nil {
			failed = true
			break
		}
	}
	if !failed && len(dels) > 0 {
		keys := make([]string, 0, len(dels))
		for _, fp := range dels {
			keys = append(keys, p.redisKey(fp))
		}
		if err := p.rdb.Del(rctx, keys...).Err(); err != nil {
			failed = true
		}
	}
	p.mu.Lock()
	if failed {
		p.redisDirty = true
	} else {
		for _, fp := range dels {
			delete(p.pendingDeletes, fp)
		}
	}
	p.mu.Unlock()
}

func (p *KeyPool) readOne(ctx context.Context, fp string) *anthropicKeyState {
	raw, err := p.rdb.Get(ctx, p.redisKey(fp)).Result()
	if err != nil {
		return nil
	}
	var st anthropicKeyState
	if json.Unmarshal([]byte(raw), &st) != nil || st.State == "" {
		return nil
	}
	return &st
}

// states devolve o estado efetivo por fingerprint (memória ⊕ Redis, mais
// restritivo), já sem marcações expiradas.
func (p *KeyPool) states(ctx context.Context) map[string]*anthropicKeyState {
	now := p.now()
	var fromRedis map[string]*anthropicKeyState
	if p.rdb != nil && len(p.keys) > 0 {
		keys := make([]string, len(p.keys))
		for i, k := range p.keys {
			keys[i] = p.redisKey(k.Fingerprint)
		}
		rctx, cancel := p.redisCtx(ctx)
		vals, err := p.rdb.MGet(rctx, keys...).Result()
		cancel()
		if err != nil {
			p.redisFailed(err)
		} else {
			p.redisOK(ctx)
			fromRedis = make(map[string]*anthropicKeyState, len(vals))
			for i, v := range vals {
				s, ok := v.(string)
				if !ok {
					continue
				}
				var st anthropicKeyState
				if json.Unmarshal([]byte(s), &st) != nil || st.State == "" {
					continue
				}
				fromRedis[p.keys[i].Fingerprint] = &st
			}
		}
	}

	out := make(map[string]*anthropicKeyState, len(p.keys))
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		fp := k.Fingerprint
		mem := p.mem[fp]
		if mem != nil && mem.expired(now) {
			delete(p.mem, fp)
			mem = nil
		}
		var rs *anthropicKeyState
		if fromRedis != nil {
			rs = fromRedis[fp]
			if rs != nil && (rs.expired(now) || p.pendingDeletes[fp]) {
				rs = nil
			}
		}
		if st := moreRestrictive(mem, rs); st != nil {
			cp := *st
			out[fp] = &cp
		}
	}
	return out
}

// Snapshot devolve o estado de cada chave, na ordem de uso.
func (p *KeyPool) Snapshot(ctx context.Context) []AnthropicKeyStatus {
	sts := p.states(ctx)
	out := make([]AnthropicKeyStatus, 0, len(p.keys))
	for _, k := range p.keys {
		s := AnthropicKeyStatus{Label: k.Label, Fingerprint: k.Fingerprint, State: anthropicStateOK}
		if st := sts[k.Fingerprint]; st != nil {
			s.State = st.State
			s.Until = st.Until
			s.NextProbeAt = st.NextProbeAt
			s.Reason = st.Reason
		}
		out = append(out, s)
	}
	return out
}

type anthropicCandidate struct {
	key    AnthropicKey
	marked bool // tinha marcação (re-teste): 2xx limpa o estado
}

// candidates devolve, em ordem fixa, as chaves que podem ser tentadas agora.
func (p *KeyPool) candidates(ctx context.Context) []anthropicCandidate {
	now := p.now()
	sts := p.states(ctx)
	out := make([]anthropicCandidate, 0, len(p.keys))
	for _, k := range p.keys {
		st := sts[k.Fingerprint]
		if st != nil && now.Before(st.blockedUntil()) {
			continue
		}
		out = append(out, anthropicCandidate{key: k, marked: st != nil})
	}
	return out
}

// nextRenewal: próxima data com dia = day (clamp ao último dia do mês) às
// 00:00 em loc, estritamente após now.
func nextRenewal(now time.Time, day int, loc *time.Location) time.Time {
	n := now.In(loc)
	y, m := n.Year(), n.Month()
	for i := 0; i < 3; i++ {
		last := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
		d := day
		if d > last {
			d = last
		}
		c := time.Date(y, m, d, 0, 0, 0, 0, loc)
		if c.After(now) {
			return c
		}
		m++
		if m > 12 {
			m = 1
			y++
		}
	}
	return now.Add(AnthropicKeyRetestInterval)
}

// mark grava a marcação da chave (memória primeiro, Redis best-effort).
func (p *KeyPool) mark(ctx context.Context, k AnthropicKey, v keyVerdict, cooldown time.Duration, status int) {
	now := p.now()
	reason := fmt.Sprintf("%s_http_%d", v, status)
	st := &anthropicKeyState{Reason: reason}
	switch v {
	case keyExhausted:
		st.State = anthropicStateExhausted
		probe := now.Add(AnthropicKeyRetestInterval)
		until := probe
		if k.RenewalDay > 0 {
			until = nextRenewal(now, k.RenewalDay, p.loc)
		}
		st.Until = &until
		st.NextProbeAt = &probe
		p.log.Warn("anthropic key esgotada; usando a próxima da ordem",
			"key_label", k.Label, "key_fp", k.Fingerprint, "reason", reason,
			"until", until, "next_probe_at", probe)
	case keyRevoked:
		st.State = anthropicStateRevoked
		probe := now.Add(anthropicRevokedRetestInterval)
		st.NextProbeAt = &probe
		p.log.Error("anthropic key revogada (401); re-teste em 24h",
			"key_label", k.Label, "key_fp", k.Fingerprint, "reason", reason,
			"next_probe_at", probe)
	case keyCooldown:
		st.State = anthropicStateCooldown
		until := now.Add(cooldown)
		st.Until = &until
		p.log.Info("anthropic key em cooldown (429)",
			"key_label", k.Label, "key_fp", k.Fingerprint, "reason", reason,
			"until", until)
	default:
		return
	}

	p.mu.Lock()
	p.mem[k.Fingerprint] = st
	delete(p.pendingDeletes, k.Fingerprint)
	p.mu.Unlock()

	if p.rdb == nil {
		return
	}
	buf, err := json.Marshal(st)
	if err != nil {
		return
	}
	rctx, cancel := p.redisCtx(ctx)
	err = p.rdb.Set(rctx, p.redisKey(k.Fingerprint), string(buf), st.ttl(now)).Err()
	cancel()
	if err != nil {
		p.mu.Lock()
		p.redisDirty = true
		p.mu.Unlock()
		p.redisFailed(err)
		return
	}
	p.redisOK(ctx)
}

// clear apaga a marcação da chave (respondeu 2xx no re-teste).
func (p *KeyPool) clear(ctx context.Context, k AnthropicKey) {
	p.mu.Lock()
	delete(p.mem, k.Fingerprint)
	p.mu.Unlock()
	p.log.Info("anthropic key liberada (2xx no re-teste)", "key_label", k.Label, "key_fp", k.Fingerprint)
	if p.rdb == nil {
		return
	}
	rctx, cancel := p.redisCtx(ctx)
	err := p.rdb.Del(rctx, p.redisKey(k.Fingerprint)).Err()
	cancel()
	if err != nil {
		p.mu.Lock()
		p.pendingDeletes[k.Fingerprint] = true
		p.redisDirty = true
		p.mu.Unlock()
		p.redisFailed(err)
		return
	}
	p.redisOK(ctx)
}

// ---------------------------------------------------------------------------
// Transporte
// ---------------------------------------------------------------------------

type anthropicKeyPoolTransport struct {
	base http.RoundTripper
	pool *KeyPool
	log  *slog.Logger
}

// NewAnthropicKeyPoolTransport envolve base: tenta as chaves do pool em ordem
// fixa, trocando de chave (antes de qualquer byte ao cliente) quando a
// resposta indica problema da chave. Remove Authorization do cliente e seta
// x-api-key + anthropic-version.
func NewAnthropicKeyPoolTransport(base http.RoundTripper, pool *KeyPool, log *slog.Logger) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if log == nil {
		log = slog.Default()
	}
	return &anthropicKeyPoolTransport{base: base, pool: pool, log: log}
}

type peekedBody struct {
	io.Reader
	io.Closer
}

func (t *anthropicKeyPoolTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx := r.Context()
	cands := t.pool.candidates(ctx)
	if len(cands) == 0 {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, errNoUsableAnthropicKey
	}

	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("anthropic keypool: lendo body: %w", err)
		}
		body = b
	}

	for _, c := range cands {
		req := r.Clone(ctx)
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			}
		} else {
			req.Body = http.NoBody
			req.ContentLength = 0
		}
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", c.key.secret)
		if c.key.workspace != "" {
			req.Header.Set("anthropic-workspace-id", c.key.workspace)
		}
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", anthropicAPIVersion)
		}

		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 400 {
			if c.marked && resp.StatusCode < 300 {
				t.pool.clear(ctx, c.key)
			}
			return resp, nil
		}

		peek, _ := io.ReadAll(io.LimitReader(resp.Body, anthropicErrPeekMax))
		verdict, cooldown := classifyAnthropicKeyError(resp.StatusCode, peek, resp.Header)
		if verdict == notKeyProblem || verdict == keyOK {
			resp.Body = peekedBody{Reader: io.MultiReader(bytes.NewReader(peek), resp.Body), Closer: resp.Body}
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, anthropicErrPeekMax))
		_ = resp.Body.Close()
		t.pool.mark(ctx, c.key, verdict, cooldown, resp.StatusCode)
	}
	return nil, errNoUsableAnthropicKey
}
