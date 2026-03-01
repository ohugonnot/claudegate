# internal/api/ratelimit.go

## Ce que ça fait

Implémente un rate limiter **par IP** sur `POST /api/v1/jobs`. Utilise un token bucket (`golang.org/x/time/rate`) pour limiter le nombre de soumissions de jobs par seconde. Si désactivé (`rps=0`), le middleware est un no-op.

---

## Token bucket — le bon algorithme pour ce cas

Il existe deux algorithmes courants pour le rate limiting :

**Fixed window** : compte les requêtes dans une fenêtre fixe (ex: 10 req par minute). Problème : en fin + début de fenêtre, un client peut envoyer 2× la limite en rafale.

**Token bucket** : un seau contient des jetons. Chaque requête consomme un jeton. Les jetons se régénèrent à vitesse fixe. Un seau plein permet une petite rafale.

```
Jeton régénéré à 5/s, seau max = 5
t=0 : 5 jetons disponibles → 5 requêtes passent d'un coup
t=1s : 5 nouveaux jetons → 5 requêtes encore possibles
```

C'est le token bucket. Le code utilise `golang.org/x/time/rate` qui implémente exactement ce comportement. Ici `burst == rps` — la rafale maximale est égale au débit par seconde.

---

## Un limiter par IP, pas global

```go
type RateLimiter struct {
    mu    sync.Mutex
    ips   map[string]*ipLimiter
    rps   rate.Limit
    burst int
}
```

Un seul limiter global serait injuste : un utilisateur légitime se ferait bloquer à cause d'un autre qui flood. La map `ips` garde un limiter séparé par adresse IP.

```go
func (rl *RateLimiter) allow(ip string) bool {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    l, ok := rl.ips[ip]
    if !ok {
        l = &ipLimiter{limiter: rate.NewLimiter(rl.rps, rl.burst)}
        rl.ips[ip] = l
    }
    l.lastSeen = time.Now()
    return l.limiter.Allow()
}
```

Première visite d'une IP → création d'un limiter. Visites suivantes → on réutilise le même. `lastSeen` sert au cleanup.

---

## Cleanup goroutine — éviter la fuite mémoire

```go
func (rl *RateLimiter) cleanup() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    for range ticker.C {
        rl.mu.Lock()
        cutoff := time.Now().Add(-5 * time.Minute)
        for ip, l := range rl.ips {
            if l.lastSeen.Before(cutoff) {
                delete(rl.ips, ip)
            }
        }
        rl.mu.Unlock()
    }
}
```

Sans cleanup, chaque IP qui a touché l'API resterait en mémoire indéfiniment. Une IP active toutes les 5 minutes sur un système en prod pendant un an = des milliers d'entrées accumulées.

Le cleanup supprime les IPs non vues depuis 5 minutes. La goroutine tourne en background pour toute la durée de vie du serveur — on ne la stoppe jamais car elle est liée à `RateLimiter` qui vit aussi longtemps que le serveur.

---

## `clientIP` — respect du proxy

```go
func clientIP(r *http.Request) string {
    if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
        if idx := strings.Index(fwd, ","); idx != -1 {
            return strings.TrimSpace(fwd[:idx])
        }
        return strings.TrimSpace(fwd)
    }
    addr := r.RemoteAddr
    if idx := strings.LastIndex(addr, ":"); idx != -1 {
        return addr[:idx]
    }
    return addr
}
```

En production, ClaudeGate tourne derrière Apache. `r.RemoteAddr` contient l'IP du proxy (`127.0.0.1`), pas du vrai client. Si on rate limitait sur `127.0.0.1`, tous les utilisateurs partageraient le même bucket → un seul utilisateur bloque tout le monde.

`X-Forwarded-For` peut contenir une chaîne de proxies : `"client, proxy1, proxy2"`. On prend le **premier** élément — c'est l'IP du client original.

**Attention** : `X-Forwarded-For` est spoofable si le proxy ne le sanitize pas. Apache en production réécrit cet header, donc ici c'est acceptable.

---

## Le middleware — seulement sur POST /api/v1/jobs

```go
func RateLimit(rps int) Middleware {
    if rps <= 0 {
        return func(next http.Handler) http.Handler { return next }
    }
    rl := NewRateLimiter(rps)
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.Method == http.MethodPost && r.URL.Path == "/api/v1/jobs" {
                ip := clientIP(r)
                if !rl.allow(ip) {
                    writeError(w, http.StatusTooManyRequests, "rate limit exceeded, slow down")
                    return
                }
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

Le rate limiting ne s'applique qu'à la soumission de jobs. Les GET (polling, list, health) sont exempts. Logique : un job consomme une session Claude CLI ; un GET ne coûte que quelques microsecondes de SQLite.

---

## Ce qu'un dev junior raterait ici

1. **`sync.Mutex` pas `sync.RWMutex`** — `allow()` modifie la map (création d'entrée + mise à jour `lastSeen`). Avec `RWMutex`, il faudrait passer en WriteLock à chaque appel de toute façon. `Mutex` est plus simple et tout aussi correct ici.

2. **`rate.Limit` est un `float64`** — `rate.Limit(rps)` est une conversion explicite. `rate.NewLimiter(5, 5)` crée un limiter de 5 tokens/seconde avec burst de 5. `rate.Inf` est disponible pour "illimité".

3. **Le no-op pattern** — `if rps <= 0 { return func(next) { return next } }`. Retourner un middleware qui passe juste au suivant est le pattern standard Go pour "désactiver un middleware". Pas de flag booléen, pas de branchement dans le code principal.

4. **`lastSeen` et `lastSeen.Before(cutoff)`** — `time.Time.Before` retourne `true` si le moment est antérieur. `cutoff = time.Now() - 5min` → les IPs dont le `lastSeen` est avant ce cutoff sont "inactives depuis 5 minutes". C'est l'intuition correcte.
