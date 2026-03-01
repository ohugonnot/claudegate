# internal/config/config.go

## Ce que ça fait

Charge toute la configuration depuis les variables d'environnement, valide chaque valeur, et retourne un `*Config` prêt à l'emploi — ou une erreur claire si quelque chose manque.

---

## Principe : fail fast

```go
cfg, err := config.Load()
if err != nil {
    log.Fatalf("config: %v", err)
}
```

Si une variable est manquante ou invalide, le process **meurt au démarrage** avec un message précis. C'est intentionnel. L'alternative — démarrer quand même et planter plus tard — est bien pire : tu découvres le problème à 3h du matin quand un job échoue, au lieu de le voir immédiatement au `systemctl start`.

---

## La struct Config — un objet de valeurs, pas de comportement

```go
type Config struct {
    ListenAddr             string
    APIKeys                []string
    ClaudePath             string
    ...
}
```

`Config` ne contient que des données, zéro méthode. Elle est passée en paramètre à chaque composant qui en a besoin. En Go, une struct sans méthodes qui transporte de la config s'appelle un **value object** — c'est le bon pattern ici, pas besoin de l'encapsuler davantage.

---

## Parsing des listes : le pattern split + trim

```go
for _, k := range strings.Split(rawKeys, ",") {
    k = strings.TrimSpace(k)
    if k != "" {
        cfg.APIKeys = append(cfg.APIKeys, k)
    }
}
```

Ce pattern revient deux fois (API keys, CORS origins). Il fait trois choses :
1. `Split(",")` — découpe sur la virgule
2. `TrimSpace` — tolère les espaces autour (`key1, key2` fonctionne)
3. `if k != ""` — ignore les entrées vides (double virgule, virgule finale)

La double validation sur les API keys est intentionnelle :
```go
if rawKeys == "" { ... }          // rapide : la variable entière est vide
if len(cfg.APIKeys) == 0 { ... }  // défensif : que des virgules ",,,"
```

---

## Le security prompt — une constante, pas une config

```go
const defaultSecurityPrompt = `You are operating in a sandboxed API environment...`

if getEnv("CLAUDEGATE_UNSAFE_NO_SECURITY_PROMPT", "false") != "true" {
    cfg.SecurityPrompt = defaultSecurityPrompt
}
```

Ce prompt est **hardcodé volontairement**. Si tu le laissais configurable via une variable d'env, n'importe qui ayant accès au `.env` pourrait le vider. Le seul levier exposé est un booléen `UNSAFE_NO_SECURITY_PROMPT` — le nom contient `UNSAFE` pour que la dangerosité soit visible dans les logs et dans le fichier de config.

Quand désactivé, `cfg.SecurityPrompt` vaut `""`. La queue vérifie ensuite simplement `if j.SystemPrompt != ""` — pas besoin de flag booléen séparé.

---

## `getEnv` et `getEnvInt` — deux helpers minimalistes

```go
func getEnv(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

Rien de magique. La seule subtilité : `os.Getenv` retourne `""` si la variable n'existe pas *ou* si elle vaut `""`. Ce comportement est correct ici — une variable vide équivaut à absente pour notre usage.

```go
func getEnvInt(key string, fallback int) (int, error) {
    v := os.Getenv(key)
    if v == "" {
        return fallback, nil
    }
    n, err := strconv.Atoi(v)
    if err != nil {
        return 0, fmt.Errorf("invalid integer %q", v)
    }
    return n, nil
}
```

Retourne une erreur avec la valeur fautive dans le message (`%q` = avec guillemets). Quand l'utilisateur écrit `CLAUDEGATE_CONCURRENCY=deux`, il voit `invalid integer "deux"` — actionnable immédiatement.

---

## Ce qu'un dev junior raterait ici

1. **Valider les entiers après les avoir lus.** Il y a `getEnvInt` pour lire, puis une condition séparée pour valider la plage (`< 1`, `< 0`). Les deux étapes sont nécessaires : `getEnvInt` vérifie que c'est un nombre, la condition vérifie que c'est un nombre *valide* dans le contexte du domaine.

2. **La validation croisée à la fin :**
    ```go
    if cfg.JobTTLHours > 0 && cfg.CleanupIntervalMinutes < 1 { ... }
    ```
    Cette règle ne peut être vérifiée qu'après avoir lu les deux variables. Les validations croisées vont toujours en dernier.

3. **`validModels` est une map, pas un slice.** Pour vérifier l'appartenance à un ensemble, une `map[string]bool` est O(1). Un `[]string` avec une boucle serait O(n). Ici n=3 donc ça n'a pas d'impact réel, mais le pattern est bon à prendre.
