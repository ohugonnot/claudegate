# internal/api/handler.go

## Ce que ça fait

Définit les 8 handlers HTTP du projet. Chaque handler fait exactement une chose : valider l'input, appeler le store ou la queue, retourner la réponse. Zéro logique métier ici — c'est de la plomberie HTTP.

---

## `//go:embed static/index.html`

```go
//go:embed static/index.html
var frontendHTML []byte
```

La directive `//go:embed` inclut le fichier HTML **dans le binaire à la compilation**. Au runtime, `frontendHTML` est déjà en mémoire — aucun accès disque, aucune dépendance sur le système de fichiers. Le binaire est autosuffisant.

Sans `//go:embed`, il faudrait distribuer le fichier HTML à côté du binaire et gérer les chemins. Avec, un seul fichier = une seule dépendance.

---

## `Handler` — injection de dépendances manuelle

```go
type Handler struct {
    store job.Store
    queue *queue.Queue
    cfg   *config.Config
}
```

Pas de framework DI. Les dépendances sont passées au constructeur et stockées dans la struct. Chaque handler y accède via `h.store`, `h.queue`, `h.cfg`.

`job.Store` est une **interface** — les tests peuvent injecter un mock sans toucher SQLite. `*queue.Queue` est un pointeur concret, mais la queue elle-même est testable via ses méthodes publiques.

---

## `RegisterRoutes` — le routeur Go 1.22

```go
mux.HandleFunc("GET /api/v1/jobs/{id}", h.GetJob)
mux.HandleFunc("DELETE /api/v1/jobs/{id}", h.DeleteJob)
```

Go 1.22 a ajouté le **method+path routing** natif. Avant, il fallait un package externe (gorilla/mux, chi) ou parser `r.Method` manuellement. Maintenant `"GET /path"` et `"DELETE /path"` sont deux routes distinctes.

`{id}` dans le pattern est un **path parameter** récupéré avec `r.PathValue("id")`. Plus simple que regex ou parsing manuel.

---

## `CreateJob` — la séquence de validation

```go
r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB max
```

**Première ligne** — avant même de lire le body. `MaxBytesReader` arrête la lecture à 1 MB. Sans ça, un client malveillant peut envoyer un body de 10 GB et saturer la mémoire du serveur.

```go
if err := req.Validate(); err != nil {
    writeError(w, http.StatusBadRequest, err.Error())
    return
}
```

`Validate()` est défini dans `model.go` — la logique de validation est dans le domaine (package `job`), pas dans le handler. Le handler gère juste le mapping erreur → code HTTP.

**Ordre critique :** SQLite d'abord, queue ensuite. Si `Enqueue` échoue (queue pleine), le job est déjà en DB et sera récupéré au prochain démarrage via `Recovery()`. L'inverse perdrait le job silencieusement.

```go
if errors.Is(err, queue.ErrQueueFull) {
    writeError(w, http.StatusServiceUnavailable, "server busy, retry later")
}
```

`errors.Is` traverse la chaîne de wrapping (`%w`). `ErrQueueFull` est une **sentinel error** — on teste l'identité de l'erreur, pas son message. La distinction 503 vs 500 est sémantiquement importante : 503 dit au client "réessaye plus tard", 500 dit "bug serveur".

---

## `ListJobs` — null vs slice vide

```go
if jobs == nil {
    jobs = []*job.Job{}
}
```

Quand SQLite retourne zéro lignes, `jobs` est `nil`. `json.Marshal(nil)` produit `null` en JSON, pas `[]`. La plupart des clients s'attendent à un tableau — `null` force chaque client à gérer un cas spécial. On normalise en slice vide avant de sérialiser.

---

## `parseIntParam` — guard clause + fallback

```go
func parseIntParam(s string, fallback int) int {
    if s == "" {
        return fallback
    }
    v, err := strconv.Atoi(s)
    if err != nil {
        return fallback
    }
    return v
}
```

Deux early returns pour les cas invalides, une seule ligne happy path. `fallback` évite de paniquer sur `?limit=abc` — le handler continue avec une valeur saine.

---

## `DeleteJob` — read-before-delete

```go
j, err := h.store.Get(r.Context(), id)
if j == nil {
    writeError(w, http.StatusNotFound, "job not found")
    return
}
if err := h.store.Delete(r.Context(), id); err != nil { ... }
```

On lit le job avant de le supprimer pour retourner un 404 propre si l'ID est inconnu. Sans ce check, `Delete` sur un ID inexistant retournerait probablement `nil` (SQLite supprime 0 lignes sans erreur) et on enverrait un 204 trompeur.

---

## `CancelJob` — annulation en deux phases

```go
if j.Status.IsTerminal() {
    writeError(w, http.StatusConflict, "job already in terminal state")
    return
}

h.store.UpdateStatus(r.Context(), id, job.StatusCancelled, "", "job cancelled by user")
h.queue.Cancel(id)
```

**Phase 1 :** marquer `cancelled` en DB. Si le job est encore dans le channel (pas encore déqueué), `processJob` vérifiera le statut DB avant de commencer et le sautera.

**Phase 2 :** `queue.Cancel(id)` annule le context du worker si le job tourne déjà. Le sous-processus Claude reçoit SIGKILL.

`IsTerminal()` est la source de vérité pour savoir si un job peut encore être annulé — plutôt que lister les statuts manuellement (`== completed || == failed || == cancelled`).

409 Conflict est le bon code HTTP pour "cette ressource est dans un état incompatible avec cette opération".

---

## `Health` — lecture des credentials OAuth

```go
data, err := os.ReadFile(filepath.Join(homeDir, ".claude", ".credentials.json"))
```

`filepath.Join` construit le chemin de façon portable (pas de concaténation string avec `/`). Sur Windows le séparateur est `\` — `filepath.Join` gère ça automatiquement. Sur Linux c'est équivalent, mais c'est la bonne pratique.

Le handler lit `expiresAt` (timestamp UNIX en millisecondes) et calcule le temps restant :

```go
remaining := time.Until(expiresAt)
if remaining > 0 {
    resp["claude_auth"] = "valid"
} else {
    resp["claude_auth"] = "expired"
    remaining = -remaining  // pour afficher une durée positive
}
```

Si la lecture échoue (fichier absent, JSON invalide), `claude_auth` reste `"unknown"` — le handler ne plante pas. **Degraded gracefully** : la santé du serveur ne dépend pas de la lisibilité des credentials.

---

## `writeJSON` / `writeError` — helpers DRY

```go
func writeJSON(w http.ResponseWriter, status int, data any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(data) //nolint:errcheck
}
```

Deux règles HTTP souvent ratées :
1. `Content-Type` **avant** `WriteHeader` — après le premier Write ou WriteHeader, les headers sont figés
2. `WriteHeader` **avant** `Encode` — sinon Go envoie automatiquement un 200 au premier Write

`//nolint:errcheck` — l'erreur de `Encode` sur `http.ResponseWriter` est une erreur réseau (client déconnecté). On ne peut rien y faire, logger serait du bruit.

---

## Ce qu'un dev junior raterait ici

1. **`MaxBytesReader` en première ligne** — mettre la limite après avoir commencé à lire est inutile. Elle doit être posée avant tout accès au body.

2. **Ordre store → queue dans CreateJob** — inverser cet ordre crée une fenêtre de perte de données que `Recovery()` ne peut pas combler.

3. **`jobs = []*job.Job{}` pour éviter `null`** — oublier ce cas fait rager les clients qui font `response.jobs.map(...)` en JavaScript sur un `null`.

4. **`errors.Is` vs `== ErrQueueFull`** — `==` ne traverse pas le wrapping. Si `Enqueue` retourne `fmt.Errorf("%w: ...", ErrQueueFull)`, `err == ErrQueueFull` est `false`. `errors.Is` est obligatoire pour les erreurs wrappées.
