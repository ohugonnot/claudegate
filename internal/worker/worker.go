package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ChunkCallback est appelé pour chaque chunk de texte reçu du stream.
type ChunkCallback func(text string)

// Run execute claude CLI et retourne le résultat complet.
func Run(ctx context.Context, claudePath, model, prompt, systemPrompt string, onChunk ChunkCallback) (string, error) {
	args := []string{
		"--print",
		"--verbose",
		"--model", model,
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Env = filteredEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	var finalResult string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		text, result, ok := parseLine(line)
		if !ok {
			continue
		}
		if result != "" {
			finalResult = result
		}
		if text != "" && onChunk != nil {
			onChunk(text)
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// The CLI often reports errors in stdout (JSON stream) rather than stderr.
		// Prefer finalResult when available as it contains the actual error message.
		detail := stderr.String()
		if detail == "" && finalResult != "" {
			detail = finalResult
		}
		return "", fmt.Errorf("claude exited: %w — %s", err, detail)
	}

	return finalResult, nil
}

// filteredEnv retourne os.Environ() sans les variables commençant par CLAUDE.
func filteredEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CLAUDE") {
			filtered = append(filtered, kv)
		}
	}
	return filtered
}

// parseLine extrait le texte assistant et/ou le résultat final d'une ligne JSON.
func parseLine(line []byte) (text, result string, ok bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return "", "", false
	}

	var msgType string
	if err := json.Unmarshal(raw["type"], &msgType); err != nil {
		return "", "", false
	}

	switch msgType {
	case "assistant":
		text = extractAssistantText(raw["content"])
		return text, "", true

	case "result":
		if err := json.Unmarshal(raw["result"], &result); err != nil {
			return "", "", false
		}
		return "", result, true
	}

	return "", "", false
}

// extractAssistantText parcourt le tableau content et concatène les blocs de type "text".
func extractAssistantText(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}
