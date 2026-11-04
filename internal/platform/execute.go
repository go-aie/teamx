package platform

import "context"

// Execute runs a controlled command. The caller owns cancellation, environment,
// output bounds and process settlement; providers do not own the Room lifecycle.
type Execute func(context.Context, string, map[string]string, string, ...string) ([]byte, error)
