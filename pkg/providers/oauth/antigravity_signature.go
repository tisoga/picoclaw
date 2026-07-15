package oauthprovider

import "encoding/base64"

// Antigravity declares thought_signature as bytes. Keep the compatibility
// fallback valid base64 when older session history has no model signature.
var antigravityDefaultThoughtSignature = base64.StdEncoding.EncodeToString([]byte("Thinking..."))
