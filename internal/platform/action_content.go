package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ImageContentDigest freezes exact text and ordered original bytes. Source
// references and platform upload locations do not change content identity.
func ImageContentDigest(body string, images [][]byte) string {
	hashes := make([]string, len(images))
	for i, data := range images {
		sum := sha256.Sum256(data)
		hashes[i] = hex.EncodeToString(sum[:])
	}
	data, _ := json.Marshal(struct {
		Version int      `json:"version"`
		Body    string   `json:"body"`
		Images  []string `json:"images"`
	}{1, body, hashes})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
