# cmd/claudegate/main.go

## Ce que ça fait

C'est le chef d'orchestre. Il ne contient **aucune logique métier** — il instancie les dépendances dans le bon ordre et les branche ensemble. C'est tout.

---

## L'ordre de câblage — et pourquoi cet ordre précis

```
config → store → queue → recovery → workers → HTTP server
```

Chaque étape dépend de la précédente. Si tu inverses, ça crashe ou tu as des race conditions.

- **Config en premier** : fail fast. Si une variable d'env est manquante, le process meurt avant de créer quoi que ce soit. Pas de surprise à mi-chemin.
- **Store avant Queue** : la queue a besoin du store pour lire/écrire les jobs.
- **Recovery avant Start** : les workers doivent être au repos quand on remet les jobs en queue. Si tu démarres les workers avant la recovery, un worker peut prendre un job pendant que la recovery essaie de le resetter.
- **Cleanup après Start** : optionnel, pas critique pour l'ordre.

---

## Pattern : Dependency Injection manuelle

```go
store, err := job.NewSQLiteStore(cfg.DBPath)
q := queue.New(cfg, store)
h := api.NewHandler(store, q, cfg)
```

Chaque composant reçoit ses dépendances **en paramètre** au lieu de les créer lui-même. C'est de la **DI sans framework**.

Avantage concret : dans les tests, tu passes un faux store (`mockStore`) à la place du vrai SQLite. La queue et le handler n'y voient que du feu.

Un dev junior ferait `store := job.NewSQLiteStore()` à l'intérieur de la queue — le store est alors caché, non testable, et impossible à swapper.

---

## Le middleware en chaîne

```go
handler := api.Chain(mux,
    api.CORS(cfg.CORSOrigins),
    api.Logging,
    api.RequestID,
    api.Auth(cfg.APIKeys),
)
```

`Chain` applique les middlewares dans l'ordre de lecture : le premier de la liste est le plus à l'extérieur, le dernier est juste avant le routeur. Une requête entrante traverse les couches de haut en bas :

```
CORS → Logging → RequestID → Auth → mux (routes)
```

**CORS est en premier volontairement** : les requêtes `OPTIONS` (preflight navigateur) doivent recevoir les headers CORS *avant* de toucher l'auth. Si tu mets Auth en premier, ton navigateur reçoit un 401 sur le preflight et bloque tout.

Le pattern : tous les middlewares ont le même type `Middleware = func(http.Handler) http.Handler`. Ceux qui ont besoin de config (`CORS`, `Auth`) sont des **factory functions** — elles prennent leur config et retournent un middleware. Ceux sans config (`Logging`, `RequestID`) sont directement des variables de type `Middleware`.

```go
// Factory function — prend de la config, retourne un Middleware
func Auth(keys []string) Middleware { ... }

// Variable — directement un Middleware
var Logging Middleware = func(next http.Handler) http.Handler { ... }
```

`Chain` en interne applique les middlewares en ordre inverse pour que le premier de la liste devienne le plus extérieur :

```go
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
    for i := len(middlewares) - 1; i >= 0; i-- {
        h = middlewares[i](h)
    }
    return h
}
```

---

## Graceful shutdown

```go
go func() {
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh                          // bloque jusqu'à Ctrl+C ou kill
    cancel()                         // arrête les workers
    shutdownCtx, ... := context.WithTimeout(..., 10*time.Second)
    srv.Shutdown(shutdownCtx)        // attend la fin des requêtes en cours (max 10s)
}()
```

Deux choses se passent au signal :
1. `cancel()` — le contexte des workers est annulé, ils finissent leur job en cours et s'arrêtent.
2. `srv.Shutdown()` — le serveur HTTP arrête d'accepter de nouvelles connexions et attend que les requêtes en cours se terminent, avec un timeout de 10 secondes.

Le `WriteTimeout: 120 * time.Second` sur le serveur HTTP est important : les jobs peuvent prendre du temps, et le SSE est une connexion longue durée. 30s aurait coupé les streams en pleine exécution.

---

## Logging avec slog

```go
slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
})))
```

C'est la **première ligne** de `main()`, avant tout le reste. Si la config plante, les logs du crash sont déjà en JSON structuré.

### Pourquoi slog et pas log

`log.Printf("worker: job %s failed: %v", jobID, err)` produit du texte libre. Pour le lire en prod tu fais du grep. Pour le parser dans Loki, Datadog ou CloudWatch tu écris des regex fragiles.

`slog.Error("job failed", "job_id", jobID, "error", err)` produit :
```json
{"time":"2025-01-15T14:32:01Z","level":"ERROR","msg":"job failed","job_id":"abc-123","error":"timeout"}
```
Chaque champ est indexé automatiquement. Tu filtres par `job_id` ou `level` sans regex.

### Niveaux utilisés dans le projet

| Niveau | Usage |
|---|---|
| `slog.Info` | Opérations normales : requête reçue, job créé, cleanup effectué |
| `slog.Warn` | Problème non-fatal : webhook retry, job skippé car annulé |
| `slog.Error` | Erreur qui nécessite attention : DB error, worker crash, retries épuisés |

### Fatals au démarrage

```go
// Pas de log.Fatalf — slog.Error + os.Exit(1)
slog.Error("config", "error", err)
os.Exit(1)
```

`log.Fatalf` appelle `os.Exit(1)` en interne, mais il formate en texte libre. En le remplaçant on garde le JSON cohérent même pour les crashes au démarrage.

---

## Ce qu'un dev junior raterait ici

1. **Pas de `defer store.Close()`** — oublier de fermer la DB proprement peut corrompre le fichier SQLite si le process est tué en pleine écriture. Ici c'est fait avec `defer`.

2. **`if err := srv.ListenAndServe(); err != http.ErrServerClosed`** — quand on appelle `srv.Shutdown()`, `ListenAndServe` retourne `http.ErrServerClosed`. C'est une erreur *attendue*, pas une erreur réelle. Sans cette vérification, le programme logguerait une fausse erreur à chaque arrêt propre.

3. **La goroutine du signal handler** — le signal handler tourne dans une goroutine séparée pour ne pas bloquer `ListenAndServe`. C'est le seul moyen de faire les deux en parallèle.
