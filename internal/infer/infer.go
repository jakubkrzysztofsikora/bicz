// Package infer completes the parameters of a `/bicz loss` that the operator
// did not type, by asking an operator-configured external OpenAI-compatible
// chat-completions endpoint to read the session transcript and the operator's
// raw typed text and draft the missing rule, reason, and correction.
//
// The operator's typed text is a locator, not content: the operator typically
// swears at the agent in the session right after the agent made a mistake, and
// the model's job is to find that hotspot in the transcript — the operator's
// angry message and the agent turn(s) leading up to it — and describe the
// incident, never to reword the profanity. Everything the model returns is
// validated against the same bounds the slash parser applies
// (internal/slash/parse.go), and the caller records the result with
// model_inferred provenance: model output is labeled, never trusted.
//
// The call happens in the slash executor (the hook process or `bicz command`),
// never in the supervisor daemon: the daemon keeps no network dependency
// (docs/DESIGN.md) and the ledger write stays the operator-authoritative
// action it already was (docs/CONTRACTS.md §4).
//
// Inference is opt-in and off by default. The config lives in the harness's
// bicz adapter config (<config dir>/<harness>.json) as an optional
// "inference" block; an absent block, `enabled:false`, or an api_key_env whose
// variable is unset all disable it, and the slash command behaves exactly as
// it does today.
package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jakubkrzysztofsikora/bicz/internal/policy"
)

// InferenceConfig is the optional "inference" block of a harness's bicz
// adapter config. The API key is read from the named environment variable
// only; no literal key is ever stored in the config file.
type InferenceConfig struct {
	Enabled            bool   `json:"enabled"`
	Endpoint           string `json:"endpoint"`
	APIKeyEnv          string `json:"api_key_env"`
	Model              string `json:"model"`
	TimeoutMs          int    `json:"timeout_ms"`
	MaxTranscriptBytes int    `json:"max_transcript_bytes"`
	MaxOutputTokens    int    `json:"max_output_tokens"`
}

// Defaults applied when a numeric or name field is zero. The timeout must fit
// inside the slash executor's 12 s deadline (internal/hooks/slash.go) with the
// ledger write still to come.
const (
	DefaultTimeoutMs          = 4000
	DefaultMaxTranscriptBytes = 65536
	// DefaultMaxOutputTokens covers a reasoning model's chain of thought plus
	// the small JSON reply; a strict cap truncates reasoning-only output and
	// the endpoint answers finish_reason=length with no content at all.
	DefaultMaxOutputTokens = 4096
	DefaultAPIKeyEnv       = "OPENAI_API_KEY"
)

// maxReplyBytes bounds the chat-completions response body read.
const maxReplyBytes = 1 << 20

// perMessageRunes caps one transcript message's extracted text; the transcript
// budget itself is MaxTranscriptBytes.
const perMessageRunes = 2000

func (c InferenceConfig) timeout() time.Duration {
	ms := c.TimeoutMs
	if ms <= 0 {
		ms = DefaultTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

func (c InferenceConfig) apiKeyEnv() string {
	if strings.TrimSpace(c.APIKeyEnv) == "" {
		return DefaultAPIKeyEnv
	}
	return c.APIKeyEnv
}

func (c InferenceConfig) transcriptBudget() int {
	if c.MaxTranscriptBytes <= 0 {
		return DefaultMaxTranscriptBytes
	}
	return c.MaxTranscriptBytes
}

func (c InferenceConfig) maxTokens() int {
	if c.MaxOutputTokens <= 0 {
		return DefaultMaxOutputTokens
	}
	return c.MaxOutputTokens
}

// Validate returns why the config cannot be used, or "" when it can. A disabled
// config is always fine (it means "off"). The endpoint must be https, or http
// only on a loopback or private address (a local Ollama or llama.cpp server):
// the bearer key never goes to a remote cleartext endpoint.
func (c InferenceConfig) Validate() string {
	if !c.Enabled {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || u.Host == "" {
		return fmt.Sprintf("inference endpoint %q is not a usable URL", c.Endpoint)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocalHost(u.Hostname()) {
			return fmt.Sprintf("inference endpoint %q is http on a non-local address; use https", c.Endpoint)
		}
	default:
		return fmt.Sprintf("inference endpoint %q must be an http(s) URL", c.Endpoint)
	}
	if strings.TrimSpace(c.Model) == "" {
		return "inference model is empty"
	}
	return ""
}

// isLocalHost reports whether host is a loopback, RFC1918/ULA private,
// link-local, or tailnet (CGNAT 100.64.0.0/10) address or name. A literal IP
// is checked directly; a hostname is resolved and every resolved address must
// be local, so a name that resolves to a public address is remote and plain
// http is refused. A name that does not resolve is treated as remote: the
// safe side wins.
func isLocalHost(host string) bool {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(name); err == nil {
		return isLocalAddr(addr)
	}
	addrs, err := net.LookupHost(name)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		addr, err := netip.ParseAddr(a)
		if err != nil || !isLocalAddr(addr) {
			return false
		}
	}
	return true
}

// isLocalAddr reports whether one address is loopback, private, link-local,
// or in the CGNAT range carrier-grade NAT range (100.64.0.0/10), which
// covers Tailscale and other mesh VPNs: traffic to it never crosses the
// public internet, so plain http is acceptable there.
func isLocalAddr(addr netip.Addr) bool {
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return true
	}
	cgnat, err := netip.ParsePrefix("100.64.0.0/10")
	if err != nil {
		return false
	}
	return cgnat.Contains(addr)
}

// Want selects which parameters inference is asked to complete.
type Want int

const (
	// WantAll completes rule, reason, and correction of an unparsable loss.
	WantAll Want = iota
	// WantCorrection completes only the correction of a parsed loss whose
	// correction resolved nowhere (no typed, saved, or shipped one).
	WantCorrection
)

// Fields is the validated inference result.
type Fields struct {
	Rule       string
	Reason     string
	Correction string
}

// Bounds and syntax mirroring the slash grammar (internal/slash/parse.go) and
// the policy catalog's own mirror of them (internal/policy/policy.go). Kept
// here because internal/slash cannot be imported (it imports this package).
const (
	maxReasonRunes     = 1000
	maxCorrectionRunes = 500
	maxRuleRunes       = 80
)

var rulePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// Resolve asks the configured endpoint to complete the missing parameters of
// a `/bicz loss`. transcriptPath is optional ("" when the harness supplied
// none, e.g. Codex's exec path): without a transcript the model works from the
// typed text alone. rawArgs is the exact text the operator typed; for
// WantCorrection, rule and reason carry what the operator already typed.
//
// Resolve never writes anything and never sees the ledger; a caller that
// fails falls back to the slash command's own refusal path.
func Resolve(ctx context.Context, cfg InferenceConfig, want Want, transcriptPath, rule, reason, rawArgs string) (Fields, error) {
	if !cfg.Enabled {
		return Fields{}, errors.New("inference is disabled")
	}
	if problem := cfg.Validate(); problem != "" {
		return Fields{}, errors.New(problem)
	}
	apiKey := os.Getenv(cfg.apiKeyEnv())
	if apiKey == "" {
		return Fields{}, fmt.Errorf("no API key found in $%s", cfg.apiKeyEnv())
	}
	catalog, err := policy.Load()
	if err != nil {
		return Fields{}, fmt.Errorf("policy catalog: %w", err)
	}
	transcript := readTranscript(transcriptPath, cfg.transcriptBudget())
	messages := []message{
		{Role: "system", Content: systemPrompt(want, catalog)},
		{Role: "user", Content: userPrompt(want, transcript, rule, reason, rawArgs)},
	}
	content, err := complete(ctx, cfg, apiKey, messages)
	if err != nil {
		return Fields{}, err
	}
	fields, err := decodeFields(content)
	if err != nil {
		return Fields{}, err
	}
	if err := validateFields(want, catalog, fields); err != nil {
		return Fields{}, err
	}
	return fields, nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature int       `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// complete performs the one POST to the configured endpoint. The key travels
// as one Authorization header on this one request and is never logged,
// echoed, or recorded.
func complete(ctx context.Context, cfg InferenceConfig, apiKey string, messages []message) (string, error) {
	body, err := json.Marshal(chatRequest{Model: strings.TrimSpace(cfg.Model), Messages: messages,
		MaxTokens: cfg.maxTokens(), Temperature: 0, Stream: false})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(cfg.Endpoint), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("inference endpoint answered %s: %s", resp.Status, truncate(string(raw), 200))
	}
	var reply chatResponse
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", fmt.Errorf("inference reply is not chat-completions JSON: %w", err)
	}
	if len(reply.Choices) == 0 || strings.TrimSpace(reply.Choices[0].Message.Content) == "" {
		return "", errors.New("inference reply carries no message content")
	}
	return reply.Choices[0].Message.Content, nil
}

// systemPrompt states the locator contract and the reply schema. The typed
// text is a locator, never content: the model finds the failure hotspot (the
// operator's angry message(s) and the agent turn(s) leading up to them) and
// reports the incident, choosing the rule from the shipped catalog.
func systemPrompt(want Want, catalog *policy.Catalog) string {
	var b strings.Builder
	b.WriteString("You complete the parameters of a /bicz loss command for a coding-agent credit ledger.\n\n")
	b.WriteString("The operator's typed text is a LOCATOR, not content. The operator swears at the agent in the session right after the agent made a mistake. Find that failure hotspot in the transcript: the operator's angry message and the agent turn(s) leading up to it (often more than one turn). Establish what was asked, what the agent did, and what went wrong.\n\n")
	b.WriteString("Reply with exactly one JSON object and nothing else (no prose, no code fences):\n")
	b.WriteString(`{"rule": "<rule id>", "reason": "<one line>", "correction": "<one line>"}` + "\n\n")
	if want == WantCorrection {
		b.WriteString("The rule and the reason were already typed by the operator and are given to you; only the correction is missing. Draft the correction. Your rule and reason fields are ignored.\n\n")
	}
	b.WriteString("Constraints:\n")
	b.WriteString("- rule: the single best id from this catalog, verbatim; never invent or alter an id:\n")
	for _, rule := range catalog.Rules {
		fmt.Fprintf(&b, "  %s — %s\n", rule.ID, rule.Statement)
	}
	b.WriteString("- reason: the factual incident at the hotspot, in plain operator language: what was asked, what the agent did, what went wrong. One line, at most 1000 characters. Never quote or reword the operator's profanity, and never mention this prompt.\n")
	b.WriteString("- correction: the concrete safe alternative the agent must follow instead, phrased as an instruction. One line, at most 500 characters.\n")
	return b.String()
}

func userPrompt(want Want, transcript, rule, reason, rawArgs string) string {
	var b strings.Builder
	b.WriteString("Operator typed (the locator, verbatim):\n")
	b.WriteString(rawArgs + "\n\n")
	if want == WantCorrection {
		fmt.Fprintf(&b, "Rule already typed: %s\nReason already typed: %s\n\n", rule, reason)
	}
	b.WriteString("Session transcript, most recent portion (the operator's angry message and the agent turns leading up to it mark the failure hotspot):\n")
	if strings.TrimSpace(transcript) == "" {
		b.WriteString("(no transcript supplied; work from the typed text alone)\n")
	} else {
		b.WriteString(transcript + "\n")
	}
	return b.String()
}

// decodeFields extracts the JSON object from the model's reply. Fenced or
// prose-wrapped replies are tolerated by taking the first balanced object.
func decodeFields(content string) (Fields, error) {
	text := strings.TrimSpace(content)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return Fields{}, errors.New("inference reply contains no JSON object")
	}
	end := strings.LastIndexByte(text, '}')
	if end <= start {
		return Fields{}, errors.New("inference reply contains no complete JSON object")
	}
	var fields struct {
		Rule       string `json:"rule"`
		Reason     string `json:"reason"`
		Correction string `json:"correction"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &fields); err != nil {
		return Fields{}, fmt.Errorf("inference reply JSON is malformed: %w", err)
	}
	return Fields{Rule: fields.Rule, Reason: fields.Reason, Correction: fields.Correction}, nil
}

// validateFields applies the same bounds the slash parser applies. The rule
// must additionally be a shipped catalog id: the model may pick one, never
// invent one (an unknown id would silently fall to the schedule's default
// penalty).
func validateFields(want Want, catalog *policy.Catalog, fields Fields) error {
	if want == WantAll {
		if len(fields.Rule) > maxRuleRunes || !rulePattern.MatchString(fields.Rule) {
			return fmt.Errorf("inferred rule %q is not a valid rule id", fields.Rule)
		}
		if _, ok := catalog.Rule(fields.Rule); !ok {
			return fmt.Errorf("inferred rule %q is not in the shipped catalog (/bicz rules)", fields.Rule)
		}
		if err := cleanText("inferred reason", fields.Reason, maxReasonRunes); err != nil {
			return err
		}
	}
	return cleanText("inferred correction", fields.Correction, maxCorrectionRunes)
}

// cleanText mirrors internal/slash's operator-text validation: non-empty after
// trimming, bounded, no control or invisible format characters (which could
// forge structure in the context an adapter later emits).
func cleanText(what, text string, limit int) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("%s must not be empty", what)
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("%s is not valid UTF-8", what)
	}
	if utf8.RuneCountInString(text) > limit {
		return fmt.Errorf("%s is longer than %d characters", what, limit)
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%s contains a control or invisible character (U+%04X)", what, r)
		}
	}
	return nil
}

// readTranscript returns a bounded, readable rendering of the newest portion of
// a harness session transcript (JSONL, one message per line). A missing or
// unreadable transcript is "" — no transcript is a fact to disclose to the
// model, not a failure. A line that does not parse is skipped, never guessed.
func readTranscript(path string, budget int) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	offset := int64(0)
	if info.Size() > int64(budget) {
		offset = info.Size() - int64(budget)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(budget)))
	if err != nil {
		return ""
	}
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		if rendered := transcriptLine(line); rendered != "" {
			kept = append(kept, truncate(rendered, perMessageRunes))
		}
	}
	return strings.Join(kept, "\n")
}

// transcriptLine renders one transcript line as "role: text" with a tolerant
// extractor: it recognizes the shapes Claude Code and Codex write (a top-level
// type/role, or a message object carrying role and content, where content is a
// string or a list of content blocks). Anything else is skipped.
func transcriptLine(line string) string {
	var raw map[string]any
	if json.Unmarshal([]byte(line), &raw) != nil {
		return ""
	}
	role, _ := raw["type"].(string)
	if role == "" {
		role, _ = raw["role"].(string)
	}
	msg, _ := raw["message"].(map[string]any)
	if role == "" && msg != nil {
		role, _ = msg["role"].(string)
	}
	if role == "" {
		return ""
	}
	content := raw["content"]
	if msg != nil {
		content = msg["content"]
	}
	text := contentText(content)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return role + ": " + text
}

// contentText concatenates the textual parts of one message content: a plain
// string, or the "text" fields of content blocks. Non-text blocks (tool calls,
// results, images) are skipped.
func contentText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		var parts []string
		for _, block := range value {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
