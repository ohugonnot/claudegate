# internal/api/sse.go

## Ce que ça fait

Ouvre une connexion HTTP longue durée et pousse des événements au client au fur et à mesure que le job avance. C'est le mécanisme "push" côté HTTP — le serveur envoie, le client reçoit sans poller.

---

## SSE vs WebSocket vs polling

| | SSE | WebSocket | Polling |
|---|---|---|---|
| Direction | Serveur → Client uniquement | Bidirectionnel | Client pull |
| Protocole | HTTP standard | Upgrade HTTP → WS | HTTP standard |
| Reconnexion auto | ✅ navigateur gère | ❌ manuel | N/A |
| Implémentation | Triviale | Complexe | Simple |

SSE est le bon choix ici : le client n'a besoin que de recevoir (chunks de texte, statut final). Pas besoin de la complexité WebSocket.

---

## Le format SSE

SSE c'est du texte brut sur une connexion HTTP qui ne se ferme pas. Chaque événement a ce format :

```
event: chunk\n
data: {"text":"Bonjour"}\n
\n
```

Deux `\n` à la fin = séparateur d'événements. C'est tout le protocole.

```go
fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, event.Data)
```

Trois types d'événements dans ClaudeGate :
- `status` — le job est passé en `processing`
- `chunk` — morceau de texte de Claude
- `result` — résultat final, la connexion se ferme après

---

## `http.Flusher` — pourquoi c'est obligatoire

```go
flusher, ok := w.(http.Flusher)
```

C'est une **type assertion** : "est-ce que ce `http.ResponseWriter` implémente aussi `http.Flusher` ?"

Par défaut, Go bufferise les writes HTTP — il accumule les bytes et envoie en une fois quand le buffer est plein ou quand le handler retourne. Pour du streaming, il faut forcer l'envoi immédiat après chaque événement :

```go
fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, event.Data)
flusher.Flush()  // envoie immédiatement au client
```

Sans `Flush()`, le client attend que tous les chunks soient accumulés avant de recevoir quoi que ce soit — le streaming est cassé.

---

## Le cas terminal — réponse immédiate

```go
if j.Status.IsTerminal() {
    writeSSEEvent(w, flusher, "result", j)
    return
}
```

Si le client se connecte **après** que le job soit terminé (il était offline, la page a été rechargée), on envoie le résultat immédiatement et on ferme. Pas besoin de s'abonner à la queue.

Sans ce check, le client se connecterait, s'abonnerait, et attendrait des événements qui n'arriveront jamais — le job est déjà fini.

---

## Subscribe / Unsubscribe — le fan-out SSE

```go
ch := h.queue.Subscribe(id)
defer h.queue.Unsubscribe(id, ch)
```

`Subscribe` crée un channel buffered (capacité 64) et l'ajoute dans `queue.subs[jobID]`. Quand le worker appelle `notify(jobID, event)`, l'event est envoyé à **tous** les channels abonnés à ce job.

`defer Unsubscribe` — quand le handler retourne (client déconnecté, job terminé), le channel est retiré de la map. Sans ça, la map grossit indéfiniment et les goroutines bloquent sur un channel que personne ne lit.

Plusieurs clients peuvent suivre le même job simultanément — chacun a son propre channel.

---

## La boucle `select` — deux cas de sortie

```go
for {
    select {
    case event, open := <-ch:
        if !open {
            return  // channel fermé par notifyAndClose — job terminé
        }
        fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, event.Data)
        flusher.Flush()
    case <-r.Context().Done():
        return  // client déconnecté (ferme l'onglet, timeout réseau)
    }
}
```

**Cas 1 : `!open`** — `notifyAndClose` dans la queue ferme le channel après l'événement `result`. En Go, recevoir depuis un channel fermé retourne immédiatement la valeur zéro avec `open = false`. C'est le signal que le job est fini.

**Cas 2 : `r.Context().Done()`** — le client a fermé la connexion. Go détecte la déconnexion et annule le context de la requête. Sans ce case, le handler tournerait en boucle pour rien, lisant des events sur un channel pour un client qui n'existe plus.

Le `select` bloque jusqu'à ce que l'un des deux cas soit prêt — pas de polling, pas de sleep.

---

## L'événement initial `status`

```go
ch := h.queue.Subscribe(id)
defer h.queue.Unsubscribe(id, ch)

writeSSEEvent(w, flusher, "status", j)  // état actuel du job
```

On s'abonne **avant** d'envoyer l'état initial — important. Si on envoyait l'état puis s'abonnait, on raterait les événements arrivés entre les deux. En s'abonnant d'abord, on ne rate rien.

---

## Ce qu'un dev junior raterait ici

1. **Oublier `Flush()`** — le streaming semble marcher en dev (les buffers sont petits), mais en prod avec un reverse proxy qui bufferise aussi (nginx, Apache), les events n'arrivent pas du tout jusqu'à la fin du job.

2. **Oublier `r.Context().Done()`** — sans ce case, chaque client déconnecté laisse une goroutine bloquée à vie sur `<-ch`. Fuite de goroutines.

3. **S'abonner après l'état initial** — race condition : le worker peut envoyer des events entre le `writeSSEEvent` et le `Subscribe`. Ces events sont perdus. L'ordre subscribe → état initial est obligatoire.

4. **Ne pas vérifier `!open`** — sans ce check, quand le channel est fermé Go retourne des valeurs zéro en boucle infinie. La goroutine tourne à 100% CPU pour rien.
