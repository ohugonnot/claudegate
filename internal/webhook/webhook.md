# internal/webhook/webhook.go

## Ce que ça fait

Envoie le résultat d'un job terminé à une URL de callback, de façon asynchrone, avec retries. C'est le mécanisme "push" du projet — au lieu que le client poll en boucle, il reçoit une notification dès que le job est fini.

---

## Fire-and-forget — `go send(...)`

```go
func Send(callbackURL string, payload []byte) {
    if err := validateURL(callbackURL); err != nil {
        slog.Warn("webhook: rejected callback URL", ...)
        return
    }
    go send(callbackURL, payload)  // goroutine détachée, on n'attend pas
}
```

`Send` retourne **immédiatement**. La livraison HTTP se fait dans une goroutine séparée. Le worker qui appelle `Send` n'est pas bloqué — il peut passer au job suivant.

La validation par contre est **synchrone**, avant de lancer la goroutine. Si l'URL est invalide, pas la peine de créer une goroutine pour rien.

---

## Protection SSRF — `validateURL`

SSRF (Server-Side Request Forgery) : un attaquant soumet un job avec `callback_url: "http://169.254.169.254/latest/meta-data/"` — l'endpoint AWS qui expose les credentials de l'instance. Sans protection, ton serveur ferait lui-même cette requête et renverrait les credentials à l'attaquant.

La défense en deux étapes :

```go
// 1. Schème : seulement http/https
if u.Scheme != "https" && u.Scheme != "http" {
    return fmt.Errorf("unsupported scheme: %s", u.Scheme)
}
```

Bloque `file://`, `ftp://`, `gopher://` etc.

```go
// 2. Résolution DNS + vérification IP
ips, err := net.LookupHost(host)
for _, ipStr := range ips {
    ip := net.ParseIP(ipStr)
    if ip.IsLoopback() ||           // 127.0.0.1
       ip.IsPrivate() ||            // 192.168.x.x, 10.x.x.x, 172.16-31.x.x
       ip.IsLinkLocalUnicast() ||   // 169.254.x.x — metadata AWS/GCP/Azure
       ip.IsLinkLocalMulticast() ||
       ip.IsUnspecified() {         // 0.0.0.0
        return fmt.Errorf("private/internal IP blocked: %s", ipStr)
    }
}
```

On résout le DNS **avant** d'envoyer la requête. Toutes les plages d'IP privées sont couvertes.

---

## Exponential backoff avec full jitter

```go
const (
    retryAttempts = 8
    retryBase     = time.Second
    retryCap      = 5 * time.Minute
)

func jitter(attempt int) time.Duration {
    exp := retryBase * (1 << attempt) // base * 2^attempt
    if exp > retryCap {
        exp = retryCap
    }
    return time.Duration(rand.Int63n(int64(exp)))
}
```

**8 retries**, délais aléatoires bornés par `min(cap, base * 2^attempt)` :

| Attempt | Fenêtre max |
|---|---|
| 1 | 0–2s |
| 2 | 0–4s |
| 3 | 0–8s |
| 4 | 0–16s |
| 5 | 0–32s |
| 6–8 | 0–5 min (cap) |

Fenêtre totale : ~35 minutes dans le pire cas.

### Pourquoi le jitter est obligatoire

Sans jitter, si 50 jobs finissent en même temps et que le serveur cible est surchargé, tous les retries partent exactement à `t+1s`, `t+2s`, `t+4s` — ça martèle un serveur déjà en difficulté (thundering herd). Le jitter étale les retries de façon aléatoire : chaque client attend une durée différente, la charge se distribue naturellement.

C'est la recommandation AWS (article "Exponential Backoff and Jitter") : le **full jitter** (`random(0, cap)`) est le meilleur compromis entre vitesse de retry et protection du serveur cible.

### Comparaison industrie

| | ClaudeGate | Shopify | Calendly |
|---|---|---|---|
| Retries | 8 | 8 | 25 |
| Fenêtre | ~35 min | 4 heures | 24 heures |
| Jitter | ✅ oui | non documenté | non documenté |

---

## `post` — le client HTTP avec timeout

```go
client := &http.Client{Timeout: 30 * time.Second}
```

Le timeout est sur le **client** — chaque tentative attend au maximum 30 secondes. Sans timeout, une connexion à un serveur qui ne répond pas bloquerait la goroutine indéfiniment.

```go
defer resp.Body.Close()
```

Obligatoire même si on ne lit pas le body. Sans ça, la connexion HTTP reste ouverte dans le pool — fuite de connexions.

---

## Ce qu'un dev junior raterait ici

1. **`validateURL` avant la goroutine** — si tu lances la goroutine d'abord et valides dedans, tu as créé une goroutine pour rien. La validation synchrone avant `go send(...)` évite l'allocation inutile.

2. **DNS lookup = protection insuffisante seule** — un attaquant sophistiqué peut utiliser le DNS rebinding : `validateURL` résout `evil.com` → IP publique ✅, puis la vraie requête résout `evil.com` → `127.0.0.1`. Pour une protection complète, il faudrait un `http.Transport` custom qui vérifie l'IP **au moment de la connexion TCP**. Ici c'est un premier niveau de défense acceptable.

3. **Pas de dead-letter queue** — si les 3 retries échouent, le webhook est perdu. Documenté comme limitation connue. Pour un système critique il faudrait stocker les webhooks en échec en DB et les rejouer.
