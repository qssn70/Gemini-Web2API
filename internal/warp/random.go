package warp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func randomBytes(b []byte) ([]byte, error) {
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto/rand.Read: %w", err)
	}
	return b, nil
}

func randomSerial() string {
	b := make([]byte, 16)
	_, _ = randomBytes(b)
	return hex.EncodeToString(b)
}
