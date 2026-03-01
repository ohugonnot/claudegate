# internal/api/middleware.go

## Ce que ça fait

Définit la chaîne de middlewares HTTP : `Chain`, `CORS`, `Logging`, `RequestID`, et `Auth`. Chaque middleware enveloppe le handler suivant et ajoute un comportement transversal — sans toucher à la logique métier des handlers.

---

## `type Middleware func(http.Handler) http.Handler`

C'est le type central du fichier. Un middleware Go c'est une fonction qui :
- Prend un `http.Handler` (le suivant dans la chaîne)
- Retourne un `http.Handler` (le handler enrichi)

```go
func MonMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // avant — preprocessing (auth, headers)
        next.ServeHTTP(w, r)
        // après — postprocessing (logging, métriques)
    })
}
```

---

## `Chain` — reverse loop

```go
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
    for i := len(middlewares) - 1; i >= 0; i-- {
        h = middlewares[i](h)
    }
    return h
}
```

On itère **à l'envers** pour que le premier middleware de la liste soit le plus extérieur :

```go
Chain(mux, CORS, Logging, RequestID, Auth)
// Construit : CORS(Logging(RequestID(Auth(mux))))
// Exécution : CORS → Logging → RequestID → Auth → mux
```

---

## `publicPaths` — map d'exemptions

```go
var publicPaths = map[string]bool{
    "/api/v1/health": true,
    "/":              true,
}
```

Avant, la condition d'exemption dans `Auth` était un `||` inline :
```go
// ancienne version — fragile
if r.URL.Path == "/api/v1/health" || r.URL.Path == "/" {
```

Avec une map, ajouter une route publique = une ligne dans `publicPaths`, sans toucher à la logique du middleware. L'intention est aussi plus lisible : `publicPaths[r.URL.Path]` dit clairement "ce chemin est-il public ?".

---

## `Logging` — request_id corrélé

```go
reqID, _ := r.Context().Value(requestIDKey).(string)
slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start), "request_id", reqID)
```

Chaque ligne de log inclut maintenant le `request_id`. Quand un utilisateur signale une erreur et donne son `X-Request-ID`, on peut filtrer les logs :

```bash
journalctl -u claudegate | grep "request_id=abc-123"
```

Le `request_id` est attaché au context par `RequestID` (middleware en amont). `Logging` s'exécute **après** `RequestID` dans la chaîne d'exécution (même si `RequestID` est wrappé après dans `Chain`) — voir la section `Chain` ci-dessous pour comprendre l'ordre.

---

## `Auth` — constant-time comparison

```go
subtle.ConstantTimeCompare([]byte(provided), []byte(key))
```

La comparaison `==` s'arrête **au premier caractère différent** — un attaquant peut mesurer le temps de réponse de milliers de requêtes et déduire combien de caractères de sa clé sont corrects (**timing attack**).

`ConstantTimeCompare` prend **toujours le même temps** quelle que soit la clé soumise. Le temps de réponse ne révèle rien.

---

## `RequestID` — context.WithValue

```go
ctx := context.WithValue(r.Context(), requestIDKey, id)
next.ServeHTTP(w, r.WithContext(ctx))
```

`context.WithValue` attache une valeur au context de la requête. Tous les handlers en aval peuvent la lire avec `r.Context().Value(requestIDKey)`. C'est le mécanisme standard Go pour propager des données à travers la chaîne sans les passer en paramètre.

La clé est de type `contextKey` (type nommé privé) et non une `string` directe — si deux packages utilisaient la même string `"requestID"`, ils se marcheraient dessus. Avec un type nommé privé, la clé est **unique par package**.

---

## `statusResponseWriter` — capturer le status code

```go
type statusResponseWriter struct {
    http.ResponseWriter  // embedding — hérite toutes les méthodes
    status int
}

func (sw *statusResponseWriter) WriteHeader(code int) {
    sw.status = code
    sw.ResponseWriter.WriteHeader(code)  // délègue au vrai writer
}
```

`http.ResponseWriter` n'expose pas de getter pour le status code. Pour le logger après que le handler a répondu, il faut l'intercepter.

**L'embedding** fait que `statusResponseWriter` implémente automatiquement toute l'interface `http.ResponseWriter`. On ne surcharge que `WriteHeader` — toutes les autres méthodes sont déléguées automatiquement.

`Flush()` est nécessaire pour le SSE — le streaming requiert de forcer l'envoi des bytes sans attendre que le buffer soit plein.

---

## `CORS` — le preflight OPTIONS

```go
if r.Method == http.MethodOptions {
    w.WriteHeader(http.StatusNoContent)
    return  // ne passe PAS à next
}
```

Les navigateurs envoient une requête `OPTIONS` **avant** la vraie requête cross-origin (preflight). Si `OPTIONS` atteignait `Auth`, il échouerait — le navigateur n'envoie pas `X-API-Key` sur le preflight. C'est pour ça que `CORS` est le **premier middleware** dans la chaîne.

`Access-Control-Max-Age: 86400` dit au navigateur de mettre en cache la réponse 24 heures.

---

## Ce qu'un dev junior raterait ici

1. **L'embedding vs implémentation manuelle** — sans embedding, il faudrait implémenter manuellement toutes les méthodes de `http.ResponseWriter`. L'embedding les délègue automatiquement.

2. **`r.WithContext(ctx)` crée une nouvelle requête** — `r` est immuable. Pour attacher un context modifié, `WithContext` retourne une **copie** de la requête avec le nouveau context.

3. **La vérification de l'origin vide** — les requêtes non-browser (curl, Postman, API-to-API) n'ont pas d'header `Origin`. Sans ce check, toutes ces requêtes seraient traitées inutilement par la logique CORS.
