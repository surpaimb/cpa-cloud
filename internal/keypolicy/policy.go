// Package keypolicy implements the independently authored KEY-02 access-key
// policy value object and persistence boundary.
package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Mode string

const (
	ModeAll      Mode = "all"
	ModeSelected Mode = "selected"
)

type ClientProtocol string

const (
	ProtocolOpenAIChat        ClientProtocol = "openai-chat"
	ProtocolOpenAIResponses   ClientProtocol = "openai-responses"
	ProtocolAnthropicMessages ClientProtocol = "anthropic-messages"
	ProtocolGeminiGenerate    ClientProtocol = "gemini-generate-content"
)

var AllClientProtocols = []ClientProtocol{
	ProtocolOpenAIChat,
	ProtocolOpenAIResponses,
	ProtocolAnthropicMessages,
	ProtocolGeminiGenerate,
}

var (
	ErrNotFound         = errors.New("access key was not found")
	ErrPolicyMissing    = errors.New("access key policy is missing")
	ErrInvalidPolicy    = errors.New("invalid access key policy")
	ErrRevisionConflict = errors.New("access key policy revision conflict")
	ErrInvalidSchema    = errors.New("invalid access key policy schema")
)

type Policy struct {
	Revision     int64            `json:"revision"`
	ProtocolMode Mode             `json:"protocol_mode"`
	Protocols    []ClientProtocol `json:"protocols"`
	ModelMode    Mode             `json:"model_mode"`
	Models       []string         `json:"models"`
}

type Replacement struct {
	ProtocolMode Mode             `json:"protocol_mode"`
	Protocols    []ClientProtocol `json:"protocols"`
	ModelMode    Mode             `json:"model_mode"`
	Models       []string         `json:"models"`
}

// Normalize validates an API replacement and returns deterministic, detached
// protocol and model slices for idempotency fingerprints and persistence.
func Normalize(input Replacement) (Replacement, error) {
	return normalizeReplacement(input)
}

func CreateDefaultTx(ctx context.Context, tx *sql.Tx, keyID string, at time.Time) error {
	_, err := CreateTx(ctx, tx, keyID, Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
	}, at)
	return err
}

func CreateTx(ctx context.Context, tx *sql.Tx, keyID string, replacement Replacement, at time.Time) (Policy, error) {
	if tx == nil || strings.TrimSpace(keyID) == "" || at.IsZero() {
		return Policy{}, ErrInvalidPolicy
	}
	normalized, err := Normalize(replacement)
	if err != nil {
		return Policy{}, err
	}
	if err := validateModelsExist(ctx, tx, normalized.Models); err != nil {
		return Policy{}, err
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policies(key_id,revision,protocol_mode,model_mode,created_at,updated_at) VALUES(?,1,?,?,?,?)`,
		keyID, normalized.ProtocolMode, normalized.ModelMode, stamp, stamp); err != nil {
		return Policy{}, fmt.Errorf("create key policy: %w", err)
	}
	if err := replaceMembersTx(ctx, tx, keyID, normalized); err != nil {
		return Policy{}, err
	}
	return Policy{Revision: 1, ProtocolMode: normalized.ProtocolMode, Protocols: normalized.Protocols, ModelMode: normalized.ModelMode, Models: normalized.Models}, nil
}

func LoadTx(ctx context.Context, tx *sql.Tx, keyID string) (Policy, error) {
	if tx == nil || strings.TrimSpace(keyID) == "" {
		return Policy{}, ErrNotFound
	}
	var item Policy
	err := tx.QueryRowContext(ctx, `SELECT revision,protocol_mode,model_mode FROM access_key_policies WHERE key_id=?`, keyID).
		Scan(&item.Revision, &item.ProtocolMode, &item.ModelMode)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if keyErr := tx.QueryRowContext(ctx, `SELECT 1 FROM access_keys WHERE id=?`, keyID).Scan(&exists); errors.Is(keyErr, sql.ErrNoRows) {
			return Policy{}, ErrNotFound
		} else if keyErr != nil {
			return Policy{}, fmt.Errorf("check access key: %w", keyErr)
		}
		return Policy{}, ErrPolicyMissing
	}
	if err != nil {
		return Policy{}, fmt.Errorf("load key policy: %w", err)
	}
	item.Protocols, err = loadProtocols(ctx, tx, keyID)
	if err != nil {
		return Policy{}, err
	}
	item.Models, err = loadModels(ctx, tx, keyID)
	if err != nil {
		return Policy{}, err
	}
	if err := validateStored(item); err != nil {
		return Policy{}, err
	}
	return item, nil
}

func Replace(ctx context.Context, db *sql.DB, keyID string, expected int64, replacement Replacement, at time.Time) (Policy, error) {
	if db == nil || expected < 1 || strings.TrimSpace(keyID) == "" || at.IsZero() {
		return Policy{}, ErrInvalidPolicy
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, fmt.Errorf("begin key policy replacement: %w", err)
	}
	defer tx.Rollback()
	policy, err := ReplaceTx(ctx, tx, keyID, expected, replacement, at)
	if err != nil {
		return Policy{}, err
	}
	if err := tx.Commit(); err != nil {
		return Policy{}, fmt.Errorf("commit key policy replacement: %w", err)
	}
	return policy, nil
}

func ReplaceTx(ctx context.Context, tx *sql.Tx, keyID string, expected int64, replacement Replacement, at time.Time) (Policy, error) {
	if tx == nil || expected < 1 || strings.TrimSpace(keyID) == "" || at.IsZero() {
		return Policy{}, ErrInvalidPolicy
	}
	normalized, err := Normalize(replacement)
	if err != nil {
		return Policy{}, err
	}
	current, err := LoadTx(ctx, tx, keyID)
	if err != nil {
		return Policy{}, err
	}
	if current.Revision != expected || current.Revision >= 9007199254740991 {
		return Policy{}, ErrRevisionConflict
	}
	if err := validateModelsExist(ctx, tx, normalized.Models); err != nil {
		return Policy{}, err
	}
	nextRevision := current.Revision + 1
	result, err := tx.ExecContext(ctx, `UPDATE access_key_policies SET revision=?,protocol_mode=?,model_mode=?,updated_at=? WHERE key_id=? AND revision=?`,
		nextRevision, normalized.ProtocolMode, normalized.ModelMode, at.UTC().Format(time.RFC3339Nano), keyID, expected)
	if err != nil {
		return Policy{}, fmt.Errorf("replace key policy: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Policy{}, fmt.Errorf("read key policy replacement result: %w", err)
	}
	if changed != 1 {
		return Policy{}, ErrRevisionConflict
	}
	if err := replaceMembersTx(ctx, tx, keyID, normalized); err != nil {
		return Policy{}, err
	}
	return Policy{
		Revision: nextRevision, ProtocolMode: normalized.ProtocolMode, Protocols: normalized.Protocols,
		ModelMode: normalized.ModelMode, Models: normalized.Models,
	}, nil
}

func replaceMembersTx(ctx context.Context, tx *sql.Tx, keyID string, normalized Replacement) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM access_key_policy_protocols WHERE key_id=?`, keyID); err != nil {
		return fmt.Errorf("clear key policy protocols: %w", err)
	}
	for _, protocol := range normalized.Protocols {
		if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_protocols(key_id,protocol) VALUES(?,?)`, keyID, protocol); err != nil {
			return fmt.Errorf("store key policy protocol: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM access_key_policy_models WHERE key_id=?`, keyID); err != nil {
		return fmt.Errorf("clear key policy models: %w", err)
	}
	for _, model := range normalized.Models {
		if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_models(key_id,model_id) VALUES(?,?)`, keyID, model); err != nil {
			return fmt.Errorf("store key policy model: %w", err)
		}
	}
	return nil
}

func Allows(policy Policy, protocol ClientProtocol, publicModel string) bool {
	if !knownProtocol(protocol) || strings.TrimSpace(publicModel) == "" || validateStored(policy) != nil {
		return false
	}
	if policy.ProtocolMode == ModeSelected && !containsProtocol(policy.Protocols, protocol) {
		return false
	}
	return policy.ModelMode == ModeAll || containsString(policy.Models, publicModel)
}

func normalizeReplacement(input Replacement) (Replacement, error) {
	if input.Protocols == nil || input.Models == nil || !validMode(input.ProtocolMode) || !validMode(input.ModelMode) {
		return Replacement{}, ErrInvalidPolicy
	}
	if input.ProtocolMode == ModeAll && len(input.Protocols) != 0 || input.ModelMode == ModeAll && len(input.Models) != 0 {
		return Replacement{}, ErrInvalidPolicy
	}
	seenProtocols := make(map[ClientProtocol]struct{}, len(input.Protocols))
	protocols := make([]ClientProtocol, len(input.Protocols))
	copy(protocols, input.Protocols)
	for _, protocol := range protocols {
		if !knownProtocol(protocol) {
			return Replacement{}, ErrInvalidPolicy
		}
		if _, duplicate := seenProtocols[protocol]; duplicate {
			return Replacement{}, ErrInvalidPolicy
		}
		seenProtocols[protocol] = struct{}{}
	}
	sort.Slice(protocols, func(i, j int) bool { return protocolOrder(protocols[i]) < protocolOrder(protocols[j]) })
	seenModels := make(map[string]struct{}, len(input.Models))
	models := make([]string, len(input.Models))
	copy(models, input.Models)
	for _, model := range models {
		if !validModelID(model) {
			return Replacement{}, ErrInvalidPolicy
		}
		if _, duplicate := seenModels[model]; duplicate {
			return Replacement{}, ErrInvalidPolicy
		}
		seenModels[model] = struct{}{}
	}
	sort.Strings(models)
	return Replacement{ProtocolMode: input.ProtocolMode, Protocols: protocols, ModelMode: input.ModelMode, Models: models}, nil
}

func validateStored(policy Policy) error {
	if policy.Revision < 1 || !validMode(policy.ProtocolMode) || !validMode(policy.ModelMode) || policy.Protocols == nil || policy.Models == nil {
		return ErrInvalidPolicy
	}
	_, err := normalizeReplacement(Replacement{ProtocolMode: policy.ProtocolMode, Protocols: policy.Protocols, ModelMode: policy.ModelMode, Models: policy.Models})
	return err
}

func validateModelsExist(ctx context.Context, tx *sql.Tx, models []string) error {
	for _, model := range models {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM models WHERE id=? AND archived=0`, model).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidPolicy
		} else if err != nil {
			return fmt.Errorf("validate key policy model: %w", err)
		}
	}
	return nil
}

func loadProtocols(ctx context.Context, tx *sql.Tx, keyID string) ([]ClientProtocol, error) {
	rows, err := tx.QueryContext(ctx, `SELECT protocol FROM access_key_policy_protocols WHERE key_id=? ORDER BY CASE protocol WHEN 'openai-chat' THEN 1 WHEN 'openai-responses' THEN 2 WHEN 'anthropic-messages' THEN 3 WHEN 'gemini-generate-content' THEN 4 ELSE 5 END`, keyID)
	if err != nil {
		return nil, fmt.Errorf("load key policy protocols: %w", err)
	}
	defer rows.Close()
	items := make([]ClientProtocol, 0)
	for rows.Next() {
		var item ClientProtocol
		if err := rows.Scan(&item); err != nil {
			return nil, fmt.Errorf("scan key policy protocol: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate key policy protocols: %w", err)
	}
	return items, nil
}

func loadModels(ctx context.Context, tx *sql.Tx, keyID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT model_id FROM access_key_policy_models WHERE key_id=? ORDER BY model_id`, keyID)
	if err != nil {
		return nil, fmt.Errorf("load key policy models: %w", err)
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, fmt.Errorf("scan key policy model: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate key policy models: %w", err)
	}
	return items, nil
}

func validMode(mode Mode) bool { return mode == ModeAll || mode == ModeSelected }

func knownProtocol(protocol ClientProtocol) bool {
	switch protocol {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerate:
		return true
	default:
		return false
	}
}

func protocolOrder(protocol ClientProtocol) int {
	for index, candidate := range AllClientProtocols {
		if candidate == protocol {
			return index
		}
	}
	return len(AllClientProtocols)
}

func validModelID(value string) bool {
	if value == "" || len([]byte(value)) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if !(char == '-' || char == '_' || char == '.' || char == ':' || char == '/' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func containsProtocol(values []ClientProtocol, target ClientProtocol) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
