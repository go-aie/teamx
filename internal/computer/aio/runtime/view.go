package runtime

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

func newViewPipe() (net.Conn, net.Conn) { return net.Pipe() }
func rfbRead(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

// Only the RFB 3.8 None handshake and bounded, parsed client messages reach
// Xvnc. Read-only clients cannot send keys, pointer events, clipboard or unknown
// extensions, regardless of the browser's viewOnly setting or WS fragmentation.
var errInputFence = errors.New("human input exit was not acknowledged")

func proxyRFB(ctx context.Context, client io.ReadWriteCloser, upstream io.ReadWriteCloser, input bool) (resultErr error) {
	version, err := rfbRead(upstream, 12)
	if err != nil {
		return err
	}
	if string(version) != "RFB 003.008\n" {
		return fmt.Errorf("unsupported RFB version")
	}
	if _, err = client.Write(version); err != nil {
		return err
	}
	answer, err := rfbRead(client, 12)
	if err != nil || string(answer) != string(version) {
		return fmt.Errorf("RFB version mismatch")
	}
	if _, err = upstream.Write(version); err != nil {
		return err
	}
	count, err := rfbRead(upstream, 1)
	if err != nil {
		return err
	}
	methods, err := rfbRead(upstream, int(count[0]))
	if err != nil {
		return err
	}
	none := false
	for _, m := range methods {
		none = none || m == 1
	}
	if !none {
		return fmt.Errorf("local RFB authentication unavailable")
	}
	if _, err = client.Write([]byte{1, 1}); err != nil {
		return err
	}
	choice, err := rfbRead(client, 1)
	if err != nil || choice[0] != 1 {
		return fmt.Errorf("RFB security mismatch")
	}
	if _, err = upstream.Write([]byte{1}); err != nil {
		return err
	}
	result, err := rfbRead(upstream, 4)
	if err != nil || binary.BigEndian.Uint32(result) != 0 {
		return fmt.Errorf("RFB authentication failed")
	}
	if _, err = client.Write(result); err != nil {
		return err
	}
	if _, err = rfbRead(client, 1); err != nil {
		return err
	}
	if _, err = upstream.Write([]byte{1}); err != nil {
		return err
	}
	header, err := rfbRead(upstream, 24)
	if err != nil {
		return err
	}
	nameLen := binary.BigEndian.Uint32(header[20:])
	if nameLen > 4096 {
		return fmt.Errorf("RFB name too long")
	}
	name, err := rfbRead(upstream, int(nameLen))
	if err != nil {
		return err
	}
	if _, err = client.Write(append(header, name...)); err != nil {
		return err
	}

	token := computer.NewID()
	fenced := make(chan struct{}, 1)
	framesDone := make(chan struct{})
	var framesErr error
	go func() {
		defer close(framesDone)
		framesErr = serverFrames(client, upstream, token, fenced)
		client.Close()
	}()
	defer func() {
		client.Close()
		if input {
			if conn, ok := upstream.(net.Conn); ok {
				_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			}
			body := make([]byte, 9)
			body[0] = 248
			binary.BigEndian.PutUint32(body[4:], 0x80000001)
			body[8] = byte(len(token))
			_, err := upstream.Write(append(body, []byte(token)...))
			if err == nil {
				select {
				case <-fenced:
				case <-framesDone:
					err = errInputFence
				case <-time.After(2 * time.Second):
					err = errInputFence
				}
			}
			if err != nil {
				resultErr = errors.Join(resultErr, errInputFence)
			}
		}
		upstream.Close()
		<-framesDone
		resultErr = errors.Join(resultErr, framesErr)
	}()

	for {
		kind, err := rfbRead(client, 1)
		if err != nil {
			return err
		}
		var body []byte
		switch kind[0] {
		case 0:
			body, err = rfbRead(client, 19)
			if err == nil && body[3] != 32 {
				return fmt.Errorf("RFB requires 32-bit pixels")
			}
		case 2:
			body, err = rfbRead(client, 3)
			if err != nil {
				return err
			}
			n := int(binary.BigEndian.Uint16(body[1:]))
			if n > 256 {
				return fmt.Errorf("RFB encodings too large")
			}
			values, readErr := rfbRead(client, n*4)
			if readErr != nil {
				return readErr
			}

			kept := []byte{}
			fenceSeen := false
			for i := 0; i < len(values); i += 4 {
				encoding := int32(binary.BigEndian.Uint32(values[i:]))
				switch encoding {
				case 0, 1, -239, -223, -224, -312:
					kept = append(kept, values[i:i+4]...)
					if encoding == -312 {
						fenceSeen = true
					}
				}
			}
			if !fenceSeen {
				var fenceEncoding [4]byte
				binary.BigEndian.PutUint32(fenceEncoding[:], 0xfffffec8)
				kept = append(kept, fenceEncoding[:]...)
			}

			binary.BigEndian.PutUint16(body[1:], uint16(len(kept)/4))
			body = append(body, kept...)
		case 3:
			body, err = rfbRead(client, 9)
		case 4:
			if !input {
				return fmt.Errorf("read-only RFB rejects keyboard")
			}
			body, err = rfbRead(client, 7)
		case 5:
			if !input {
				return fmt.Errorf("read-only RFB rejects pointer")
			}
			body, err = rfbRead(client, 5)
		case 6:
			if !input {
				return fmt.Errorf("read-only RFB rejects clipboard")
			}
			body, err = rfbRead(client, 7)
			if err != nil {
				return err
			}
			n := binary.BigEndian.Uint32(body[3:])
			if n > 1<<20 {
				return fmt.Errorf("RFB clipboard too large")
			}
			extra, readErr := rfbRead(client, int(n))
			if readErr != nil {
				return readErr
			}
			body = append(body, extra...)
		case 248:
			body, err = rfbRead(client, 8)
			if err != nil {
				return err
			}
			n := int(body[7])
			if n > 64 {
				return fmt.Errorf("RFB fence too large")
			}
			extra, readErr := rfbRead(client, n)
			if readErr != nil {
				return readErr
			}
			body = append(body, extra...)
		default:
			return fmt.Errorf("unsupported RFB client message")
		}
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if _, err = upstream.Write(append(kind, body...)); err != nil {
			return err
		}
	}
}
