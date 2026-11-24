package runtime

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Once the downstream closes, drain upstream replies until our input fence is
// acknowledged. Client disconnect must not discard the stop proof.
type frameSink struct {
	w      io.Writer
	failed bool
}

func (s *frameSink) Write(b []byte) (int, error) {
	if !s.failed {
		if _, err := s.w.Write(b); err != nil {
			s.failed = true
		}
	}
	return len(b), nil
}
func serverFrames(client io.Writer, upstream io.Reader, token string, fenced chan<- struct{}) error {
	sink := &frameSink{w: client}
	forward := func(n int) error {
		if n < 0 || n > 64<<20 {
			return fmt.Errorf("RFB server message exceeds bound")
		}
		_, err := io.CopyN(sink, upstream, int64(n))
		return err
	}
	for {
		kind, err := rfbRead(upstream, 1)
		if err != nil {
			return err
		}
		if kind[0] == 248 {
			body, err := rfbRead(upstream, 8)
			if err != nil {
				return err
			}
			n := int(body[7])
			if n > 64 {
				return fmt.Errorf("RFB server fence too large")
			}
			payload, err := rfbRead(upstream, n)
			if err != nil {
				return err
			}
			if string(payload) == token && binary.BigEndian.Uint32(body[3:7])&0x80000000 == 0 {
				select {
				case fenced <- struct{}{}:
				default:
				}
				continue
			}
			_, _ = sink.Write(append(append(kind, body...), payload...))
			continue
		}
		_, _ = sink.Write(kind)
		switch kind[0] {
		case 0:
			header, err := rfbRead(upstream, 3)
			if err != nil {
				return err
			}
			_, _ = sink.Write(header)
			n := int(binary.BigEndian.Uint16(header[1:]))
		rectangles:
			for i := 0; i < n; i++ {
				rect, err := rfbRead(upstream, 12)
				if err != nil {
					return err
				}
				_, _ = sink.Write(rect)
				w, h := int(binary.BigEndian.Uint16(rect[4:])), int(binary.BigEndian.Uint16(rect[6:]))
				switch int32(binary.BigEndian.Uint32(rect[8:])) {
				case 0:
					err = forward(w * h * 4) // transport requests 32-bit raw pixels
				case 1:
					err = forward(4)
				case -239:
					err = forward(w*h*4 + ((w+7)/8)*h)
				case -223: // DesktopSize, no payload
				case -224:
					break rectangles // LastRect
				default:
					return fmt.Errorf("unnegotiated RFB server encoding")
				}
				if err != nil {
					return err
				}
			}
		case 1:
			body, err := rfbRead(upstream, 5)
			if err != nil {
				return err
			}
			_, _ = sink.Write(body)
			if err = forward(int(binary.BigEndian.Uint16(body[3:])) * 6); err != nil {
				return err
			}
		case 2: // Bell
		case 3:
			body, err := rfbRead(upstream, 7)
			if err != nil {
				return err
			}
			_, _ = sink.Write(body)
			n := binary.BigEndian.Uint32(body[3:])
			if n > 1<<20 {
				return fmt.Errorf("RFB clipboard too large")
			}
			if err = forward(int(n)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unnegotiated RFB server message")
		}
	}
}
