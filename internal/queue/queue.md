# internal/queue/queue.go

## Ce que ça fait

C'est le cœur du système. Il gère quatre choses simultanément : la file d'attente des jobs, les workers qui les traitent, le fan-out SSE vers les clients connectés, et l'annulation individuelle des jobs. Tout ça avec des goroutines concurrentes.

---

## La struct Queue — quatre champs, quatre responsabilités

```go
type Queue struct {
    jobs    chan string                   // file d'attente : IDs des jobs à traiter
    store   job.Store                    // accès à la DB
    subs    map[string][]chan SSEEvent   // abonnés SSE par job ID
    cancels map[string]context.CancelFunc // fonctions d'annulation par job ID
    mu      sync.RWMutex                 // protège subs et cancels
    cfg     *config.Config
}
```

`subs` et `cancels` sont des maps accédées par plusieurs goroutines simultanément — le `mu` les protège. `jobs` est un channel Go, qui est thread-safe par nature — pas besoin de mutex pour lui.

---

## Le channel comme file d'attente

```go
jobs: make(chan string, cfg.QueueSize)
```

Un **channel bufferisé** de taille `QueueSize` (défaut 1000). Chaque entrée est un job ID (string), pas le job entier — le job entier vit en DB.

```go
func (q *Queue) Enqueue(jobID string) error {
    select {
    case q.jobs <- jobID:  // essaie d'envoyer dans le channel
        return nil
    default:               // si le channel est plein, retourne une erreur immédiatement
        return fmt.Errorf("queue full: cannot enqueue job %s", jobID)
    }
}
```

Le `select` avec `default` est non-bloquant. Sans `default`, `q.jobs <- jobID` bloquerait la goroutine HTTP jusqu'à ce qu'un worker libère de la place. Avec `default`, on échoue immédiatement avec un 500 propre.

---

## Les workers — N goroutines qui lisent le même channel

```go
func (q *Queue) Start(ctx context.Context) {
    for range q.cfg.Concurrency {
        go q.runWorker(ctx)
    }
}

func (q *Queue) runWorker(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():       // arrêt gracieux
            return
        case jobID := <-q.jobs:  // reçoit le prochain job
            q.processJob(ctx, jobID)
        }
    }
}
```

`Concurrency` goroutines lisent **toutes le même channel** `q.jobs`. Go garantit qu'un message envoyé dans un channel n'est reçu que par **un seul** lecteur — pas de double traitement possible. Si `Concurrency = 3`, trois jobs tournent en parallèle, chaque worker prend le prochain dès qu'il est libre.

---

## `processJob` — la séquence complète d'un job

```
1. Vérifie que le job n'a pas été annulé pendant l'attente
2. Marque "processing" en DB
3. Notifie les clients SSE : "status: processing"
4. Crée un context annulable + timeout optionnel
5. Enregistre la cancel func dans q.cancels
6. Exécute le CLI Claude
7. Détermine le statut final (completed/failed/cancelled)
8. Finalise : DB + SSE result + webhook
```

Le check au début est essentiel :

```go
if j.Status == job.StatusCancelled {
    slog.Info("worker: job already cancelled, skipping", "job_id", jobID)
    return
}
```

Entre le moment où un job est enqueued et le moment où un worker le prend, il peut s'être écoulé plusieurs secondes. L'utilisateur a pu l'annuler entre temps. Sans ce check, le worker exécuterait un job déjà annulé.

---

## Le context en couches — annulation + timeout

```go
// Couche 1 : annulable manuellement (Cancel())
jobCtx, jobCancel := context.WithCancel(ctx)
defer jobCancel()

// Couche 2 (optionnelle) : timeout par-dessus
if q.cfg.JobTimeoutMinutes > 0 {
    jobCtx, timeoutCancel = context.WithTimeout(jobCtx, ...)
    defer timeoutCancel()
}

// Enregistre jobCancel pour que Cancel() puisse l'appeler de l'extérieur
q.cancels[jobID] = jobCancel
```

Les deux mécanismes **composent** : si l'utilisateur annule manuellement, `context.Canceled`. Si le timeout expire, `context.DeadlineExceeded`. Dans les deux cas, `worker.Run` reçoit un context fermé et s'arrête.

```go
switch {
case errors.Is(runErr, context.Canceled):         → StatusCancelled
case errors.Is(runErr, context.DeadlineExceeded): → StatusFailed
default:                                          → StatusFailed
}
```

`errors.Is` traverse la chaîne d'erreurs wrappées — même si l'erreur a été enveloppée avec `fmt.Errorf("%w", ...)` plusieurs fois, `Is` retrouve l'erreur d'origine.

---

## Le SSE fan-out — `subs` + `notify`/`notifyAndClose`

Plusieurs clients peuvent écouter le même job en SSE simultanément. La map `subs` contient une **slice de channels** par job :

```go
subs: map[string][]chan SSEEvent
//        job ID     ↑ slice car plusieurs clients possibles
```

**`notify`** — envoie sans bloquer, drop si le client est lent :

```go
func (q *Queue) notify(jobID string, event SSEEvent) {
    q.mu.RLock()           // lecture seule → plusieurs goroutines peuvent lire en même temps
    chans := q.subs[jobID]
    q.mu.RUnlock()

    for _, ch := range chans {
        select {
        case ch <- event:  // envoie si le channel a de la place
        default:           // drop silencieux si le client ne consomme pas assez vite
        }
    }
}
```

Le `RLock` permet à plusieurs goroutines de lire `subs` simultanément. Le `Lock` complet n'est nécessaire que pour les écritures (`Subscribe`, `Unsubscribe`, `notifyAndClose`).

**`notifyAndClose`** — pour l'événement final :

```go
q.mu.Lock()
chans := q.subs[jobID]
delete(q.subs, jobID)  // supprime de la map avant d'itérer
q.mu.Unlock()

for _, ch := range chans {
    select {
    case ch <- event:
    default:
    }
    close(ch)  // ferme le channel → le lecteur SSE sait que c'est fini
}
```

Supprimer de la map **avant** d'itérer est critique : si on supprimait après, un nouveau `Subscribe` entre le unlock et le close recevrait un channel déjà fermé — panic.

---

## `sync.RWMutex` — lecture partagée, écriture exclusive

```go
mu sync.RWMutex
```

Un `RWMutex` a deux modes :
- `RLock()` / `RUnlock()` — plusieurs goroutines peuvent lire en même temps
- `Lock()` / `Unlock()` — écriture exclusive, personne d'autre ne peut lire ou écrire

Pour `subs` : les notifications (lectures) sont fréquentes, les subscribe/unsubscribe (écritures) sont rares. `RWMutex` évite les contentions inutiles en lecture.

---

## Ce qu'un dev junior raterait ici

1. **`defer jobCancel()` même quand le timeout est actif** — quand on crée un timeout context, `jobCancel` annule le contexte *parent* du timeout. Sans ce defer, le contexte parent resterait ouvert même après que le job se termine — fuite de ressources.

2. **`chans := q.subs[jobID]` copie la slice avant d'unlock** — en Go, assigner une slice ne copie pas les éléments, mais copie le header (pointeur + len + cap). Ici c'est suffisant : on libère le lock et on itère sur la copie du header. Aucune autre goroutine ne peut modifier la slice pendant l'itération puisqu'on l'a retirée de la map.

3. **Le channel bufferisé à 64 dans `Subscribe`** — sans buffer, `notify` bloquerait dès qu'un client est lent. Avec 64 slots, le worker peut envoyer 64 chunks sans attendre que le client les consomme. Si le client est vraiment trop lent, les chunks sont droppés (le `default` du select) — acceptable pour du streaming.
