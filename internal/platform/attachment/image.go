// Package attachment contains provider-independent image validation.
package attachment

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"

	_ "golang.org/x/image/webp"
)

const MaxBytes = 20 << 20

type ImageLimits struct {
	MaxBytes  int64
	MaxPixels int64
}

var (
	ErrUnavailable = errors.New("attachment unavailable")
	ErrLimit       = errors.New("attachment exceeds image limit")
	ErrFormat      = errors.New("attachment image format invalid")
)

func ValidateImage(data []byte, limits ImageLimits) (string, error) {
	if limits.MaxBytes == 0 {
		limits.MaxBytes = MaxBytes
	}
	if limits.MaxBytes < 1 || limits.MaxBytes > MaxBytes || limits.MaxPixels < 0 || int64(len(data)) > limits.MaxBytes {
		return "", ErrLimit
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", ErrFormat
	}
	if limits.MaxPixels > 0 {
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || "image/"+format != mime || config.Width <= 0 || config.Height <= 0 {
			return "", ErrFormat
		}
		if int64(config.Width) > limits.MaxPixels/int64(config.Height) {
			return "", ErrLimit
		}
	}
	return mime, nil
}
