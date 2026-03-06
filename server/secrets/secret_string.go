package secrets

import "log/slog"

const redacted = "[REDACTED]"

// SecretString wraps a secret value so it can never be accidentally leaked
// through String(), JSON marshaling, or structured logging.
type SecretString struct {
	value string
}

// NewSecretString creates a SecretString holding the given value.
func NewSecretString(v string) SecretString {
	return SecretString{value: v}
}

// String always returns "[REDACTED]" to prevent accidental secret leakage.
func (s SecretString) String() string {
	return redacted
}

// Value returns the actual secret. Call this only when you truly need the
// plaintext (e.g. passing to a database driver).
func (s SecretString) Value() string {
	return s.value
}

// MarshalJSON ensures secrets are never serialized to JSON.
func (s SecretString) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}

// MarshalText ensures secrets are never serialized as text (used by
// encoding/json for map keys, etc.).
func (s SecretString) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// LogValue implements slog.LogValuer so structured loggers never emit
// the actual secret.
func (s SecretString) LogValue() slog.Value {
	return slog.StringValue(redacted)
}
