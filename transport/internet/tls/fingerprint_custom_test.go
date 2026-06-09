package tls

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const customChromeSpecJSON = `{
	"cipher_suites": [
		"GREASE",
		"TLS_AES_128_GCM_SHA256",
		"TLS_AES_256_GCM_SHA384",
		"TLS_CHACHA20_POLY1305_SHA256",
		"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
		"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"
	],
	"compression_methods": ["NULL"],
	"extensions": [
		{"name": "GREASE"},
		{"name": "server_name"},
		{"name": "extended_master_secret"},
		{"name": "renegotiation_info"},
		{"name": "supported_groups", "named_group_list": ["GREASE", "x25519", "secp256r1"]},
		{"name": "ec_point_formats", "ec_point_format_list": ["uncompressed"]},
		{"name": "session_ticket"},
		{"name": "application_layer_protocol_negotiation", "protocol_name_list": ["h2", "http/1.1"]},
		{"name": "signature_algorithms", "supported_signature_algorithms": [
			"ecdsa_secp256r1_sha256",
			"rsa_pss_rsae_sha256",
			"rsa_pkcs1_sha256"
		]},
		{"name": "key_share", "client_shares": [
			{"group": "GREASE", "key_exchange": [0]},
			{"group": "x25519"}
		]},
		{"name": "psk_key_exchange_modes", "ke_modes": ["psk_dhe_ke"]},
		{"name": "supported_versions", "versions": ["GREASE", "TLS 1.3", "TLS 1.2"]},
		{"name": "GREASE"},
		{"name": "padding", "len": 0}
	]
}`

func TestNormalizeCustomFingerprint(t *testing.T) {
	normalized, err := NormalizeCustomFingerprint([]byte(customChromeSpecJSON))
	assert.NoError(t, err)
	// The normalized form is compact JSON (no surrounding whitespace/newlines).
	assert.True(t, len(normalized) > 0 && normalized[0] == '{')
	assert.NotContains(t, normalized, "\n")
}

func TestNormalizeCustomFingerprintInvalid(t *testing.T) {
	_, err := NormalizeCustomFingerprint([]byte(`{"cipher_suites": ["NOT_A_REAL_CIPHER"]}`))
	assert.Error(t, err)

	_, err = NormalizeCustomFingerprint([]byte(`{not valid json`))
	assert.Error(t, err)
}

// TestNormalizeCustomFingerprintEmptyObject guards the panic the user hit: uTLS
// dereferences cipher_suites/compression_methods/extensions unconditionally, so
// "{}" (or any object missing them) must yield a clear error, not a panic.
func TestNormalizeCustomFingerprintMissingFields(t *testing.T) {
	assert.NotPanics(t, func() {
		_, err := NormalizeCustomFingerprint([]byte(`{}`))
		assert.Error(t, err)

		_, err = NormalizeCustomFingerprint([]byte(`{"cipher_suites": ["TLS_AES_128_GCM_SHA256"]}`))
		assert.Error(t, err)

		_, err = NormalizeCustomFingerprint([]byte(`{"cipher_suites": null, "compression_methods": null, "extensions": null}`))
		assert.Error(t, err)
	})
}

// TestCustomSpecMalformedNoPanic ensures the dial-time path is also safe when an
// unvalidated spec arrives (e.g. via a .pb config or the gRPC API).
func TestCustomSpecMalformedNoPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		_, ok := CustomSpec(GetFingerprint(`{}`))
		assert.False(t, ok)

		_, ok = CustomSpec(GetFingerprint(`{"extensions": []}`))
		assert.False(t, ok)
	})
}

// TestInlineFingerprintRoundTrip simulates the protobuf boundary: the spec JSON
// is stored verbatim in the fingerprint field, and GetFingerprint/CustomSpec must
// resolve it with no side registry.
func TestInlineFingerprintRoundTrip(t *testing.T) {
	normalized, err := NormalizeCustomFingerprint([]byte(customChromeSpecJSON))
	assert.NoError(t, err)

	// GetFingerprint resolves an inline spec to a non-nil synthetic Custom ID.
	fp := GetFingerprint(normalized)
	assert.NotNil(t, fp)
	assert.Equal(t, customFingerprintClient, fp.Client)
	assert.Equal(t, normalized, fp.Version)

	// CustomSpec parses a populated spec from that ID.
	spec, ok := CustomSpec(fp)
	assert.True(t, ok)
	assert.NotNil(t, spec)
	assert.NotEmpty(t, spec.CipherSuites)
	assert.NotEmpty(t, spec.Extensions)

	// Each call returns a distinct spec instance (uTLS mutates specs in place).
	spec2, ok := CustomSpec(fp)
	assert.True(t, ok)
	assert.NotSame(t, spec, spec2)
}

// TestRawInlineFingerprintNoRegistry proves a raw (un-normalized) JSON object
// arriving directly in the fingerprint field - e.g. from a .pb config or the gRPC
// API - still resolves, since nothing is pre-registered.
func TestRawInlineFingerprintNoRegistry(t *testing.T) {
	fp := GetFingerprint(customChromeSpecJSON)
	assert.NotNil(t, fp)
	spec, ok := CustomSpec(fp)
	assert.True(t, ok)
	assert.NotNil(t, spec)
	assert.NotEmpty(t, spec.Extensions)
}

func TestGetFingerprintPresetUnaffected(t *testing.T) {
	assert.NotNil(t, GetFingerprint("chrome"))
	// A preset is not a custom inline spec.
	_, ok := CustomSpec(GetFingerprint("chrome"))
	assert.False(t, ok)
	// Unknown non-JSON names still do not resolve.
	assert.Nil(t, GetFingerprint("definitely-not-a-fingerprint"))
}

func TestCustomSpecNil(t *testing.T) {
	_, ok := CustomSpec(nil)
	assert.False(t, ok)
}
