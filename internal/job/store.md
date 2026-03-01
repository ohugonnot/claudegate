# internal/job/store.go

## Ce que ça fait

Définit le **contrat** de la couche de persistance. Rien d'autre. Aucune implémentation, juste une liste de méthodes que quelque chose doit implémenter.

---

## L'interface en Go — le concept le plus important du langage

```go
type Store interface {
    Create(ctx context.Context, j *Job) error
    Get(ctx context.Context, id string) (*Job, error)
    UpdateStatus(ctx context.Context, id string, status Status, result, errMsg string) error
    ...
}
```

Une interface en Go dit : *"peu importe ce que tu es, si tu as ces méthodes, tu es un `Store`"*.

La clé : **l'implémentation ne déclare pas qu'elle implémente l'interface**. Pas de `implements Store`, pas de `extends`. Si `SQLiteStore` a toutes ces méthodes avec les bonnes signatures, Go considère automatiquement que c'est un `Store`. C'est ce qu'on appelle le **duck typing structurel**.

```go
// SQLiteStore n'écrit nulle part "implements Store"
// Mais elle a toutes les méthodes → c'est un Store
var _ Store = (*SQLiteStore)(nil)  // vérification statique optionnelle
```

---

## Pourquoi cette séparation change tout

Sans interface, le handler dépend directement de SQLite :

```go
// Sans interface — couplage fort
type Handler struct {
    store *job.SQLiteStore  // lié à SQLite pour toujours
}
```

Avec l'interface, le handler ne sait pas ce qu'il y a derrière :

```go
// Avec interface — couplage faible
type Handler struct {
    store job.Store  // peut être SQLite, Postgres, Redis, ou un mock
}
```

Conséquences concrètes :

**1. Les tests deviennent triviaux.** Dans `handler_test.go`, on passe un `mockStore` en mémoire au lieu d'une vraie DB SQLite :
```go
store := &mockStore{}  // implémente Store, stocke tout en mémoire
h := api.NewHandler(store, q, cfg)
```
Les tests sont instantanés, pas de fichier créé, pas de cleanup.

**2. Tu peux changer de DB sans toucher au reste du code.** Demain tu veux passer à Postgres — tu écris `PostgresStore` qui implémente `Store`, tu changes une ligne dans `main.go`. Le handler, la queue, les tests : rien ne change.

---

## `context.Context` partout — pourquoi ?

```go
Create(ctx context.Context, j *Job) error
Get(ctx context.Context, id string) (*Job, error)
```

Chaque méthode prend un `context.Context` en premier paramètre. C'est une **convention absolue** en Go pour tout ce qui touche I/O (DB, réseau, fichiers).

Le context transporte deux choses :
1. **Annulation** — si la requête HTTP est annulée (client déconnecté), le context est annulé, et la requête SQL en cours est annulée avec lui. Pas de requêtes zombies.
2. **Deadline** — tu peux imposer un timeout sur toute une chaîne d'appels.

```go
// Le context de la requête HTTP se propage jusqu'à SQLite
r.Context() → handler → store.Get(ctx, id) → sql.QueryRowContext(ctx, ...)
```

Si le client se déconnecte à mi-chemin, toute la chaîne s'arrête.

---

## `Get` retourne `(*Job, error)` — deux valeurs

```go
Get(ctx context.Context, id string) (*Job, error)
```

Go ne connaît pas les exceptions. Toute fonction qui peut échouer retourne une erreur en dernier paramètre. L'appelant **doit** la vérifier — pas d'exception silencieuse possible.

Ici il y a trois cas possibles :
```go
j, err := store.Get(ctx, id)

if err != nil  { /* erreur DB — problème technique */ }
if j == nil    { /* pas d'erreur mais job introuvable — 404 */ }
if j != nil    { /* job trouvé */ }
```

Le `nil, nil` (pas d'erreur ET pas de résultat) est le signal Go pour "not found" — pas d'erreur spéciale, juste un pointeur nil. Chaque handler qui appelle `Get` doit gérer ces trois cas.

---

## Ce qu'un dev junior raterait ici

1. **Définir l'interface dans le package qui la consomme, pas qui l'implémente.** En Go, la convention est que c'est le *consommateur* qui définit l'interface dont il a besoin. Ici c'est dans le package `job` parce que c'est le package central — acceptable. Mais si l'interface était définie dans `api`, la queue pourrait avoir sa propre vision plus étroite du Store.

2. **Interfaces petites > interfaces grandes.** 8 méthodes c'est déjà beaucoup pour une interface Go. La philosophie Go préfère des interfaces à 1-2 méthodes (`io.Reader`, `io.Writer`). Ici c'est justifié parce que c'est un contrat de repository complet, mais c'est une limite à garder en tête.

3. **Ne pas oublier le `nil, nil` de `Get`.** C'est le piège classique — on vérifie `err != nil` et on oublie de vérifier `j == nil`. Le compilateur ne t'aide pas ici.
