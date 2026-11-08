//go:build integration || e2e

package lark

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/attachment"
)

func TestImageDownloadIdentityFormatLimitsAndCleanup(t *testing.T) {
	parallelClientTest(t)
	for _, mode := range []string{"image_ok", "image_wrong_key", "image_wrong_chat", "image_deleted", "image_format", "image_bad_size", "image_switch", "image_pixels", "image_bytes", "image_stream"} {
		t.Run(mode, func(t *testing.T) {
			c, dir, want := imageTestClient(t, mode)
			s := sampleState(t)
			m := sampleMessage("om_one", "oc_chat", "")
			src := sourceRef(s, m, "key", Chat{ID: m.ChatID})
			limits := attachment.ImageLimits{MaxBytes: 1024, MaxPixels: 16}
			if mode == "image_pixels" {
				limits.MaxPixels = 1
			}
			if mode == "image_bytes" {
				limits.MaxBytes = 1
			}
			data, mime, err := c.Image(context.Background(), s.Binding.Account, src, "img_one", limits)
			if mode == "image_ok" {
				if err != nil || mime != "image/png" || !bytes.Equal(data, want) {
					t.Fatal(mime, err)
				}
			} else if err == nil || len(data) != 0 || mime != "" {
				t.Fatal("failure returned image", err)
			}
			if strings.Contains(mode, "pixels") || strings.Contains(mode, "bytes") || mode == "image_stream" {
				if !errors.Is(err, attachment.ErrLimit) {
					t.Fatal("lost limit category", err)
				}
			}
			if path, err := os.ReadFile(filepath.Join(dir, "download_dir")); err == nil {
				if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
					t.Fatal("temporary download survived", err)
				}
			}
		})
	}
}

func TestImageDownloadCancellationRemovesFiles(t *testing.T) {
	parallelClientTest(t)
	c, dir, _ := imageTestClient(t, "image_block")
	s := sampleState(t)
	m := sampleMessage("om_one", "oc_chat", "")
	src := sourceRef(s, m, "key", Chat{ID: m.ChatID})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := c.Image(ctx, s.Binding.Account, src, "img_one", attachment.ImageLimits{})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "download_dir")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("download never started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled process did not settle")
	}
	path, _ := os.ReadFile(filepath.Join(dir, "download_dir"))
	if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
		t.Fatal("canceled download survived", err)
	}
}

func TestImageSourceAndKeyMismatchNeverDownload(t *testing.T) {
	parallelClientTest(t)
	c, dir, _ := imageTestClient(t, "image_ok")
	s := sampleState(t)
	m := sampleMessage("om_one", "oc_chat", "")
	src := sourceRef(s, m, "key", Chat{ID: m.ChatID})
	src.Reference.RecipientID.Value = "ou_other"
	_, _, err := c.Image(context.Background(), s.Binding.Account, src, "img_one", attachment.ImageLimits{})
	if err == nil {
		t.Fatal("cross-account image accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("mismatched source made remote calls")
	}
}
