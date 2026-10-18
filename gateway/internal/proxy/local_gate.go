package proxy

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// LocalGate é a fila curta na frente de um upstream local de CPU (quick
// 261018-efs, local-embed). Duas funções:
//
//  1. Limita as chamadas simultâneas ao upstream (MaxConcurrent). Quem chega
//     com todas as vagas ocupadas espera até MaxWait por uma vaga; a espera
//     acontece ANTES do RoundTrip, então não consome o ResponseHeaderTimeout
//     do transport. Estourou a espera → 503 local_queue_full com Retry-After,
//     sem penalizar o breaker (o upstream está ocupado, não fora do ar).
//  2. Registra chamadas em andamento e o último 2xx real, para a sonda saber
//     que o upstream está ocupado (Busy) e não contar o atraso dela como falha.
//
// Madrugada de 2026-10-18: com 22 chamadas simultâneas no infinity de 3 CPUs,
// 30 passaram de 30 s; e nas ondas das 02:33/02:43/03:05 o breaker abriu pela
// sonda (ping preso atrás de lotes de 15–20 s) entre respostas reais 200.
type LocalGate struct {
	name    string
	next    http.Handler
	slots   chan struct{}
	maxWait time.Duration
	log     *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	inflight int
	lastOK   time.Time
}

// NewLocalGate envolve next. maxConcurrent <= 0 desliga o limite (só rastreia
// ocupação); maxWait <= 0 recusa na hora quando não há vaga.
func NewLocalGate(name string, next http.Handler, maxConcurrent int, maxWait time.Duration, log *slog.Logger) *LocalGate {
	g := &LocalGate{name: name, next: next, maxWait: maxWait, log: log, now: time.Now}
	if maxConcurrent > 0 {
		g.slots = make(chan struct{}, maxConcurrent)
	}
	return g
}

// ServeHTTP espera a vaga, repassa ao proxy e registra o resultado.
func (g *LocalGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.slots != nil {
		if !g.acquire(w, r) {
			return
		}
		defer func() { <-g.slots }()
	}

	g.mu.Lock()
	g.inflight++
	g.mu.Unlock()

	rec := &gateStatusWriter{ResponseWriter: w}
	defer func() {
		g.mu.Lock()
		g.inflight--
		if rec.status >= 200 && rec.status < 300 {
			g.lastOK = g.now()
		}
		g.mu.Unlock()
	}()
	g.next.ServeHTTP(rec, r)
}

// acquire tenta pegar uma vaga. Devolve false quando já respondeu ao cliente
// (espera esgotada → 503; cliente desistiu → 499).
func (g *LocalGate) acquire(w http.ResponseWriter, r *http.Request) bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
	}
	start := g.now()
	var timeout <-chan time.Time
	if g.maxWait > 0 {
		t := time.NewTimer(g.maxWait)
		defer t.Stop()
		timeout = t.C
	} else {
		closed := make(chan time.Time)
		close(closed)
		timeout = closed
	}
	select {
	case g.slots <- struct{}{}:
		g.log.Debug("local gate: vaga liberada após espera",
			"upstream", g.name, "wait_ms", g.now().Sub(start).Milliseconds(),
			"request_id", httpx.RequestIDFrom(r.Context()))
		return true
	case <-r.Context().Done():
		w.WriteHeader(httpx.StatusClientClosedRequest)
	case <-timeout:
		g.log.Warn("local gate: fila cheia; recusando",
			"upstream", g.name, "wait_ms", g.now().Sub(start).Milliseconds(),
			"max_concurrent", cap(g.slots),
			"request_id", httpx.RequestIDFrom(r.Context()))
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(g.maxWait)))
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
			"service_unavailable", "local_queue_full",
			"The local upstream is busy; retry later.")
	}
	// Resposta já escrita: o dispatcher não pode re-despachar nem contar
	// falha no breaker (sem fallthrough).
	if res := dispatchResultFrom(r.Context()); res != nil {
		res.wrote = true
	}
	return false
}

// Busy informa se o upstream está ocupado e saudável: há chamada real em
// andamento E houve um 2xx real dentro de window. Upstream travado (nenhum 2xx
// recente) deixa de ser "ocupado" em window e a sonda volta a valer.
func (g *LocalGate) Busy(window time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inflight > 0 && !g.lastOK.IsZero() && g.now().Sub(g.lastOK) <= window
}

func retryAfterSeconds(d time.Duration) int {
	s := int(d / time.Second)
	if s < 1 {
		return 1
	}
	return s
}

// gateStatusWriter guarda o status escrito pelo proxy.
type gateStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *gateStatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gateStatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap deixa http.ResponseController chegar ao writer de baixo.
func (w *gateStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
