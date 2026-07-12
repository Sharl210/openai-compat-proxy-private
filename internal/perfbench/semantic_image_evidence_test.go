package perfbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

func semanticPromptCacheKey(body []byte) string {
	const marker = `"prompt_cache_key":"`
	start := bytes.Index(body, []byte(marker))
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := bytes.IndexByte(body[start:], '"')
	if end < 0 {
		return ""
	}
	return string(body[start : start+end])
}

func decodedSemanticImageFact(body []byte, expected semanticImageFact) (semanticImageFact, error) {
	encoded, err := semanticImageBase64(body, expected.Bytes)
	if err != nil {
		return semanticImageFact{}, err
	}
	digest := sha256.New()
	decodedBytes, err := io.Copy(digest, base64.NewDecoder(base64.StdEncoding, bytes.NewReader(encoded)))
	if err != nil {
		return semanticImageFact{}, fmt.Errorf("decode upstream image: %w", err)
	}
	actual := semanticImageFact{SHA256: fmt.Sprintf("%x", digest.Sum(nil)), Bytes: decodedBytes}
	if actual != expected {
		return semanticImageFact{}, fmt.Errorf("upstream image = %+v, want %+v", actual, expected)
	}
	return actual, nil
}

func semanticImageBase64(body []byte, expectedBytes int64) ([]byte, error) {
	const dataURLMarker = "data:image/png;base64,"
	if start := bytes.Index(body, []byte(dataURLMarker)); start >= 0 {
		start += len(dataURLMarker)
		if end := bytes.IndexByte(body[start:], '"'); end >= 0 {
			return body[start : start+end], nil
		}
	}
	const dataMarker = `"data":"`
	for offset := 0; offset < len(body); {
		index := bytes.Index(body[offset:], []byte(dataMarker))
		if index < 0 {
			break
		}
		start := offset + index + len(dataMarker)
		end := bytes.IndexByte(body[start:], '"')
		if end < 0 {
			break
		}
		candidate := body[start : start+end]
		if int64(base64.StdEncoding.DecodedLen(len(candidate))) >= expectedBytes {
			return candidate, nil
		}
		offset = start + end + 1
	}
	return nil, fmt.Errorf("upstream request has no %d-byte image", expectedBytes)
}
