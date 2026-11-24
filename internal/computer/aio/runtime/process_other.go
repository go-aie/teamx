//go:build !linux

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-aie/teamx/internal/computer"
	"io"
)

type ProcessDriver struct{}

func NewProcessDriver(string, string, computer.Contract, Policy) (*ProcessDriver, error) {
	return nil, fmt.Errorf("AIO process Runtime requires Linux")
}
func (*ProcessDriver) Validate(string, json.RawMessage) error { return fmt.Errorf("Linux required") }
func (*ProcessDriver) Start(context.Context, Resource) error  { return fmt.Errorf("Linux required") }
func (*ProcessDriver) Tools(context.Context, Resource) (computer.Contract, error) {
	return computer.Contract{}, fmt.Errorf("Linux required")
}
func (*ProcessDriver) Call(context.Context, Resource, string, json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("Linux required")
}
func (*ProcessDriver) Stop(context.Context, Resource) error      { return fmt.Errorf("Linux required") }
func RunSession(context.Context, Resource, string, string) error { return fmt.Errorf("Linux required") }

func (*ProcessDriver) OpenView(context.Context, Resource) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("Linux required")
}
func (*ProcessDriver) ViewAsset(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("Linux required")
}
