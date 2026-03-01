# internal/worker/worker.go

## Ce que ça fait

Lance le CLI Claude en sous-processus, lit sa sortie JSON ligne par ligne en temps réel, appelle `onChunk` pour chaque morceau de texte reçu, et retourne le résultat final. C'est le seul endroit du projet qui parle au CLI.

---

## `exec.CommandContext` — un sous-processus lié au context

```go
cmd := exec.CommandContext(ctx, claudePath, args...)
```

`CommandContext` lie la durée de vie du process au context. Si le context est annulé (job annulé par l'utilisateur ou timeout), Go envoie un signal `SIGKILL` au process Claude automatiquement. Sans ça, le process continuerait à tourner après l'annulation — fuite de ressources.

Les arguments construits :

```
claude --print --verbose --model haiku --output-format stream-json
       --dangerously-skip-permissions [--system-prompt "..."] "le prompt"
```

`--verbose` est **obligatoire** pour que `stream-json` fonctionne — sans lui, le CLI refuse d'émettre le format. `--print` active le mode non-interactif.

---

## `filteredEnv` — pourquoi supprimer les variables `CLAUDE*`

```go
cmd.Env = filteredEnv()
```

Quand tu lances ClaudeGate depuis l'intérieur d'une session Claude Code (comme pendant le développement), le processus parent a des variables `CLAUDE_*` dans son environnement. Le CLI Claude détecte ces variables et refuse de démarrer avec l'erreur *"nested session"*.

En filtrant toutes les variables `CLAUDE*` avant de passer l'env au sous-processus, le CLI voit un environnement propre et démarre normalement. Sans ce filtre, ClaudeGate serait inutilisable en développement.

`make([]string, 0, len(env))` — on alloue la slice avec la capacité de l'env entier pour éviter les réallocations pendant l'append.

---

## Le pipe stdout — lire en temps réel

```go
stdout, err := cmd.StdoutPipe()  // crée un pipe vers la sortie du process
cmd.Start()                       // démarre le process (non-bloquant)

scanner := bufio.NewScanner(stdout)
for scanner.Scan() {              // lit ligne par ligne au fur et à mesure
    line := scanner.Bytes()
    // ...
}

cmd.Wait()  // attend que le process se termine
```

C'est le pattern fondamental pour lire la sortie d'un process en streaming :
1. `StdoutPipe` crée le pipe **avant** `Start`
2. `Start` lance le process **sans bloquer**
3. La boucle lit les lignes **au fur et à mesure** qu'elles arrivent
4. `Wait` attend la fin et récupère le code de sortie

Si tu utilisais `cmd.Output()` à la place, tu attendrais que le process se termine **entièrement** avant de lire quoi que ce soit — plus de streaming, les chunks SSE n'arriveraient qu'à la fin.

---

## Le format `stream-json` du CLI Claude

Le CLI émet du **NDJSON** (Newline-Delimited JSON) : un objet JSON par ligne. Exemple :

```json
{"type":"assistant","content":[{"type":"text","text":"Bonjour"}]}
{"type":"assistant","content":[{"type":"text","text":", comment"}]}
{"type":"assistant","content":[{"type":"text","text":" vas-tu ?"}]}
{"type":"result","result":"Bonjour, comment vas-tu ?","duration_ms":1200}
```

Chaque ligne est un événement indépendant. `parseLine` gère deux types :
- `"assistant"` → chunk de texte à envoyer via SSE
- `"result"` → résultat final complet

Tous les autres types (`"system"`, `"tool_use"`, etc.) sont ignorés silencieusement.

---

## `parseLine` — deux niveaux de désérialisation

```go
var raw map[string]json.RawMessage  // désérialise juste les clés de premier niveau
json.Unmarshal(line, &raw)

var msgType string
json.Unmarshal(raw["type"], &msgType)  // désérialise seulement le champ "type"
```

Au lieu de définir une grosse struct avec tous les champs possibles, on désérialise en deux étapes. La map `raw` donne accès à chaque champ comme `json.RawMessage` (JSON brut, non parsé) — on ne parse que les champs dont on a besoin. Les champs `duration_ms`, `session_id`, etc. ne sont jamais désérialisés.

---

## `ChunkWriter` — interface plutôt que callback

```go
type ChunkWriter interface {
    WriteChunk(text string)
}

func Run(ctx context.Context, ..., w ChunkWriter) (string, error)
```

`Run` ne sait pas ce qui se passe avec les chunks — il appelle juste `w.WriteChunk(text)`. C'est la queue qui implémente `ChunkWriter` via `chunkWriter`, et qui décide d'envoyer les chunks aux abonnés SSE.

Ce découplage est **architecturalement obligatoire** : `queue` dépend de `worker`, donc `worker` ne peut pas dépendre de `queue` — Go interdit les imports circulaires. En passant une interface définie dans `worker`, on coupe le cycle.

L'interface rend aussi les tests explicites — `testChunkWriter` est une vraie struct qu'on peut inspecter :

```go
type testChunkWriter struct{ chunks []string }
func (w *testChunkWriter) WriteChunk(text string) { w.chunks = append(w.chunks, text) }

cw := &testChunkWriter{}
Run(ctx, claudePath, "haiku", "prompt", "", cw)
// cw.chunks contient tous les textes reçus
```

---

## `extractAssistantText` — struct anonyme inline

```go
var blocks []struct {
    Type string `json:"type"`
    Text string `json:"text"`
}
```

Une **struct anonyme définie à l'intérieur de la fonction**. Pas besoin de lui donner un nom global — elle n'est utilisée qu'ici. C'est idiomatique Go pour les types utilisés une seule fois, surtout pour le JSON parsing.

`strings.Builder` évite les allocations mémoire multiples qu'une concaténation `string + string` en boucle produirait — chaque `+` crée une nouvelle string en mémoire.

---

## La gestion d'erreur après `cmd.Wait`

```go
if err := cmd.Wait(); err != nil {
    if ctx.Err() != nil {
        return "", ctx.Err()  // annulation/timeout — retourne l'erreur du context
    }
    detail := stderr.String()
    if detail == "" && finalResult != "" {
        detail = finalResult  // l'erreur est souvent dans stdout, pas stderr
    }
    return "", fmt.Errorf("claude exited: %w — %s", err, detail)
}
```

Deux cas distincts :
1. **Le context est annulé** — c'est nous qui avons tué le process. On retourne l'erreur du context (`context.Canceled` ou `context.DeadlineExceeded`) pour que la queue gère correctement le statut.
2. **Erreur réelle du CLI** — on cherche le message d'erreur. Particularité du CLI Claude : les erreurs (ex: *"OAuth token expired"*) arrivent souvent dans stdout (dans le stream JSON) plutôt que dans stderr. D'où le fallback sur `finalResult`.

---

## Ce qu'un dev junior raterait ici

1. **`Start` + `Wait` au lieu de `Run`** — `cmd.Run()` existe et fait Start+Wait en un appel, mais il ne permet pas de lire stdout en streaming pendant l'exécution. Dès qu'on veut du streaming, il faut impérativement `StdoutPipe` + `Start` + boucle de lecture + `Wait`.

2. **`ctx.Err()` avant de construire l'erreur** — si on ne check pas `ctx.Err()`, une annulation volontaire serait traitée comme une erreur CLI et loggée comme une erreur. La distinction est critique pour le statut final du job.

3. **`bufio.NewScanner` a une limite par défaut de 64KB par ligne** — pour des réponses très longues d'un seul tenant, un seul chunk pourrait dépasser cette limite. Pour ClaudeGate c'est acceptable, mais dans un système de production critique il faudrait `scanner.Buffer(buf, maxSize)` pour agrandir la limite.
