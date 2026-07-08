// Package secure provides a SecureBytes type that holds sensitive data
// (passwords, tokens) in a form that can be explicitly zeroed when no
// longer needed. This reduces the window during which a memory dump or
// core file could leak credentials.
package secure

import "encoding/base64"

// SecureBytes wraps a byte slice and provides a Destroy() method that
// overwrites the underlying memory before releasing it, so the password
// does not linger in process memory.
type SecureBytes struct {
	b []byte
}

// NewSecureBytes creates a SecureBytes from a raw string. The caller
// should not keep a reference to the original string after calling this.
func NewSecureBytes(s string) *SecureBytes {
	return &SecureBytes{b: []byte(s)}
}

// NewSecureBytesFromCopy creates a SecureBytes by copying src into fresh
// memory. The caller is free to zero or reuse src afterwards.
func NewSecureBytesFromCopy(src []byte) *SecureBytes {
	dst := make([]byte, len(src))
	copy(dst, src)
	return &SecureBytes{b: dst}
}

// String returns the underlying data as a string. The caller should zero
// the returned value (using runtime.KeepAlive + manual zeroing or
// secure.ZeroString) when done with it. Prefer Bytes() + explicit zeroing.
//
//lint:ignore U1000 used externally
func (s *SecureBytes) String() string {
	if s == nil || s.b == nil {
		return ""
	}
	return string(s.b)
}

// Bytes returns a copy of the underlying data. The caller should zero the
// returned slice after use to avoid leaking the password.
func (s *SecureBytes) Bytes() []byte {
	if s == nil || s.b == nil {
		return nil
	}
	out := make([]byte, len(s.b))
	copy(out, s.b)
	return out
}

// AppendTo appends a copy of the secure data to dst and returns the
// extended slice. This lets callers build a URL.UserinfoString or similar
// without exposing the raw buffer.
func (s *SecureBytes) AppendTo(dst []byte) []byte {
	if s == nil || s.b == nil {
		return dst
	}
	return append(dst, s.b...)
}

// Len returns the number of bytes in the stored secret.
func (s *SecureBytes) Len() int {
	if s == nil || s.b == nil {
		return 0
	}
	return len(s.b)
}

// Destroy overwrites the underlying memory with zeros and releases the
// reference. After calling Destroy the SecureBytes is in a nil state and
// any subsequent call to Bytes or String returns nil/"".
func (s *SecureBytes) Destroy() {
	if s == nil || s.b == nil {
		return
	}
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}

// EncodeToBase64 returns the base64-encoded form of the secret. Useful
// when storing a fetched password back into config structures that expect
// base64 (e.g. some Vault setups).
func (s *SecureBytes) EncodeToBase64() string {
	if s == nil || s.b == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(s.b)
}

// SafePassword is a convenience type alias that embeds *SecureBytes and
// can be used in config structs. It marshals/unmarshals as a plain string
// (the YAML/JSON field carries the password) but at runtime the value is
// stored in zeroable memory.
//
// When the exporter starts with an inline password it is loaded into a
// SafePassword. SafePassword.Destroy() can be called after use.
type SafePassword struct {
	*SecureBytes
}

// NewSafePassword creates a SafePassword from a string.
func NewSafePassword(s string) SafePassword {
	return SafePassword{SecureBytes: NewSecureBytes(s)}
}

// MarshalText implements encoding.TextMarshaler so that SafePassword does
// not accidentally leak the password when serialised (it renders as
// "[REDACTED]").
func (SafePassword) MarshalText() ([]byte, error) {
	return []byte("[REDACTED]"), nil
}
