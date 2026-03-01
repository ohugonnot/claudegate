# internal/job/model.go

## Ce que ça fait

Définit les types de données du domaine : ce qu'est un `Job`, ce qu'est un `Status`, et comment valider une requête de création. C'est le **vocabulaire** du projet — tous les autres packages parlent en termes de `job.Job`, `job.Status`, etc.

---

## `type Status string` — un type fort sur une string

```go
type Status string

const (
    StatusQueued     Status = "queued"
    StatusProcessing Status = "processing"
    StatusCompleted  Status = "completed"
    StatusFailed     Status = "failed"
    StatusCancelled  Status = "cancelled"
)
```

Go te laisse définir un nouveau type basé sur un type primitif. `Status` se comporte comme une `string` mais ce n'en est pas une — le compilateur refuse de mélanger les deux sans conversion explicite.

**Sans ça :**
```go
func UpdateStatus(id string, status string) // tu peux passer n'importe quelle string
UpdateStatus("abc", "DONE")  // compile, explose en prod
```

**Avec ça :**
```go
func UpdateStatus(id string, status Status) // seul un Status est accepté
UpdateStatus("abc", "DONE")               // erreur de compilation
UpdateStatus("abc", StatusCompleted)      // OK
```

Le compilateur devient ton filet de sécurité. C'est l'un des usages les plus courants des types nommés en Go.

---

## `IsTerminal()` — méthode sur un type

```go
func (s Status) IsTerminal() bool {
    return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}
```

Cette méthode est attachée au type `Status`. Au lieu de dupliquer cette condition partout dans le code :
```go
// Sans IsTerminal — copié/collé dans handler.go, queue.go, etc.
if status == job.StatusCompleted || status == job.StatusFailed || status == job.StatusCancelled {
```

Tu écris une seule fois la règle métier, et tu l'appelles partout :
```go
if j.Status.IsTerminal() {
```

Si demain tu ajoutes un statut `StatusExpired`, tu modifies **un seul endroit**. C'est le **Single Responsibility Principle** appliqué à un type.

---

## La struct `Job` — les tags JSON

```go
type Job struct {
    ID          string     `json:"job_id"`
    Prompt      string     `json:"prompt"`
    Result      string     `json:"result,omitempty"`
    StartedAt   *time.Time `json:"started_at,omitempty"`
    CompletedAt *time.Time `json:"completed_at,omitempty"`
}
```

**`json:"job_id"` alors que le champ Go s'appelle `ID`** — le nom JSON et le nom Go sont découplés. L'API expose `job_id` parce que c'est plus explicite pour un consommateur externe. En interne, `j.ID` est plus court et idiomatique.

**`omitempty`** — le champ est omis du JSON s'il est vide (`""`, `0`, `nil`, `false`). Sans ça, chaque réponse contiendrait `"result": ""` et `"error": ""` même pour un job en cours. Avec `omitempty`, la réponse est propre : seuls les champs qui ont une valeur apparaissent.

---

## Pointeurs sur `time.Time` — pourquoi `*time.Time` ?

```go
StartedAt   *time.Time `json:"started_at,omitempty"`
CompletedAt *time.Time `json:"completed_at,omitempty"`
```

`time.Time` est une struct, pas un type nullable. Sa valeur zéro est `0001-01-01 00:00:00` — ce n'est pas "absent", c'est juste une date bizarre.

En mettant un **pointeur** `*time.Time`, la valeur peut être `nil` (vraiment absent) ou une date réelle. `omitempty` sur un pointeur nil → le champ disparaît du JSON. C'est la seule façon propre de représenter un champ datetime optionnel en Go.

`CreatedAt` par contre est `time.Time` sans pointeur — un job a *toujours* une date de création, jamais nil.

---

## `CreateRequest` séparé de `Job`

```go
type CreateRequest struct {
    Prompt         string          `json:"prompt"`
    Model          string          `json:"model,omitempty"`
    ...
}
```

`CreateRequest` n'est pas `Job`. Le client envoie un `CreateRequest`, le serveur crée un `Job`. La séparation est importante : le client ne choisit pas son `ID`, son `Status`, ses timestamps — ce sont des champs serveur. Exposer directement `Job` en entrée laisserait le client les renseigner.

---

## `json.RawMessage` pour les métadonnées

```go
Metadata json.RawMessage `json:"metadata,omitempty"`
```

`json.RawMessage` est un `[]byte` qui contient du JSON déjà encodé. Le serveur ne sait pas ce que le client met dans `metadata` — ça peut être `{"user_id": 42}` ou `{"tags": ["a","b"]}`. Plutôt que de désérialiser puis re-sérialiser (avec perte possible), on stocke le JSON brut tel quel et on le retourne intact.

---

## Ce qu'un dev junior raterait ici

1. **`type Status string` vs `string` partout** — la tentation est d'utiliser des strings directement. Le type nommé semble du sur-engineering jusqu'au premier bug de statut mal orthographié en prod.

2. **`omitempty` sur les strings vides** — sans ça, chaque réponse JSON contient des dizaines de champs vides. Ça pollue les réponses et complique les clients qui doivent distinguer "champ absent" de "champ vide".

3. **`Validate()` sur `CreateRequest`, pas sur `Job`** — la validation est sur l'objet entrant (request), pas sur l'objet interne. `Job` est créé par le serveur dans un état contrôlé — il n'a pas besoin d'être validé.
