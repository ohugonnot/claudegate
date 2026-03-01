# internal/job/sqlite.go

## Ce que ça fait

Implémentation concrète de l'interface `Store` avec SQLite. C'est la seule couche du projet qui sait qu'il y a une base de données.

---

## L'import blank — `_ "modernc.org/sqlite"`

```go
import _ "modernc.org/sqlite"
```

Le `_` dit à Go : *"importe ce package uniquement pour ses effets de bord, je n'utilise rien de lui directement"*. En Go, un import inutilisé est une erreur de compilation — le `_` est la façon d'en importer un quand même.

L'effet de bord ici : le package s'enregistre comme driver SQLite auprès de `database/sql`. Après cet import, `sql.Open("sqlite", path)` fonctionne. Sans lui, erreur au runtime : `unknown driver "sqlite"`.

`modernc.org/sqlite` est du **pur Go, zéro CGO**. L'alternative classique `mattn/go-sqlite3` nécessite un compilateur C installé sur la machine. Avec `modernc`, `go build` fonctionne partout sans toolchain C — cross-compilation incluse.

---

## WAL mode — critique pour la concurrence

```go
if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
    db.Close()
    return nil, fmt.Errorf("enable WAL mode: %w", err)
}
```

SQLite en mode par défaut (journal) : chaque **écriture verrouille tout le fichier**. Pendant qu'un worker écrit le résultat d'un job, tous les `GET /jobs` sont bloqués.

En mode WAL (Write-Ahead Log) : les **lectures et écritures sont concurrentes**. Les lectures voient l'état cohérent avant l'écriture en cours, l'écriture se fait dans un fichier séparé. Pour une API avec des lectures fréquentes et des écritures occasionnelles, c'est la nuit et le jour.

C'est activé en premier, avant même la migration — si WAL échoue, on ne continue pas.

---

## La migration idempotente

```go
CREATE TABLE IF NOT EXISTS jobs ( ... )
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status)
```

`IF NOT EXISTS` partout. La migration peut tourner **100 fois sans jamais planter**. Au premier démarrage, elle crée la table et les index. Aux suivants, elle vérifie qu'ils existent et ne fait rien.

```go
// Migration additive pour les colonnes ajoutées après le déploiement initial
s.db.Exec(`ALTER TABLE jobs ADD COLUMN response_format ...`) //nolint:errcheck
```

`ALTER TABLE` n'a pas de `IF NOT EXISTS` en SQLite. Si la colonne existe déjà, ça retourne une erreur. On l'ignore volontairement — c'est le seul cas où ignorer une erreur est correct et documenté.

---

## Les index — pourquoi ces trois là

```sql
CREATE INDEX IF NOT EXISTS idx_jobs_status       ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at   ON jobs(created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_completed_at ON jobs(completed_at);
```

- `idx_jobs_status` — pour `ResetProcessing` : `WHERE status = 'processing'`
- `idx_jobs_created_at` — pour `List` : `ORDER BY created_at DESC`
- `idx_jobs_completed_at` — pour `DeleteTerminalBefore` : `WHERE completed_at < ?`

Sans index, chaque requête fait un **full table scan** — elle lit toutes les lignes. Avec 100 000 jobs, ça devient lent. Avec l'index, SQLite saute directement aux bonnes lignes.

---

## `sql.NullString` et `sql.NullTime` — le NULL en Go

SQLite peut stocker `NULL`. Go ne connaît pas `NULL`. Le bridge entre les deux : les types `sql.Null*`.

```go
var metadata sql.NullString
var startedAt, completedAt sql.NullTime

// Après Scan :
if metadata.Valid {           // Valid = true si la colonne n'était pas NULL
    j.Metadata = []byte(metadata.String)
}
if startedAt.Valid {
    t := startedAt.Time
    j.StartedAt = &t          // on crée un pointeur vers une copie locale
}
```

Pourquoi `t := startedAt.Time` avant `&t` ? Parce que tu ne peux pas faire `&startedAt.Time` directement dans une boucle — à chaque itération `startedAt` serait réutilisé et tous les pointeurs pointeraient vers la même variable. La copie locale garantit une adresse mémoire unique par job.

---

## `UpdateStatus` — le `interface{}` pour le NULL conditionnel

```go
var completedAt interface{}   // nil par défaut = NULL en SQL
if status.IsTerminal() {
    completedAt = now         // une valeur réelle si le job est terminé
}
```

`database/sql` traduit `nil` en `NULL` SQL. En utilisant `interface{}` qui vaut `nil` par défaut, on obtient le comportement voulu sans écrire deux requêtes différentes. Si le job n'est pas terminal, `completed_at` reste `NULL` en base.

---

## `ResetProcessing` — SELECT avant UPDATE

```go
// 1. Récupère les IDs des jobs processing
rows, err := s.db.QueryContext(ctx, `SELECT id FROM jobs WHERE status = ?`, StatusProcessing)

// 2. Seulement après : remet à queued
_, err = s.db.ExecContext(ctx, `UPDATE jobs SET status = ? ... WHERE status = ?`, StatusQueued, StatusProcessing)
```

Pourquoi ne pas faire juste l'UPDATE et ignorer les IDs ? Parce que la queue a besoin des IDs pour re-enqueuer les jobs. L'UPDATE seul ne retourne pas les IDs en SQLite (pas de `RETURNING` dans cette version). Donc : SELECT d'abord pour les IDs, UPDATE ensuite pour changer le statut.

---

## `fmt.Errorf` avec `%w` — l'erreur wrappée

```go
return nil, fmt.Errorf("get job %s: %w", id, err)
```

`%w` (wrap) enveloppe l'erreur originale dans une nouvelle avec du contexte. La chaîne d'appel peut ensuite utiliser `errors.Is()` pour retrouver l'erreur d'origine :

```go
// L'erreur remonte comme : "get job abc-123: sql: no rows in result set"
// On sait où ça a planté ET pourquoi
```

Avec `%v` (sans wrap), tu perds l'erreur originale — tu ne peux plus tester `errors.Is(err, sql.ErrNoRows)` plus haut dans la pile.

---

## Ce qu'un dev junior raterait ici

1. **`defer rows.Close()`** — si tu oublies de fermer un `*sql.Rows`, la connexion DB reste ouverte. Avec un pool de connexions, tu les épuises rapidement. Le `defer` garantit la fermeture même si une erreur se produit au milieu de la boucle.

2. **`rows.Err()` après la boucle** — la boucle `for rows.Next()` peut s'arrêter soit parce qu'il n'y a plus de lignes, soit parce qu'une erreur s'est produite en cours de lecture. Sans `rows.Err()`, tu ne saurais pas si tu as tout lu ou si tu as lu une liste tronquée silencieusement.

3. **`.UTC()` sur toutes les dates** — SQLite stocke les dates sans timezone. Sans `.UTC()`, une date stockée à Paris (+2h) serait relue comme UTC et décalée de 2 heures. Appeler `.UTC()` avant chaque écriture garantit que tout est cohérent en base.
