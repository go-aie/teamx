package room

// Test fixtures for the pre-S1 direct adapter tests. Production uses the
// persisted coordinator and never chooses a target inside the adapter.
func rotateAdapterForTest(b dshBackend, opts SessionOptions, owned bool) (Backend, error) {
	from, err := b.sessionGeneration(opts)
	if err != nil {
		return nil, err
	}
	if err := b.checkRotationClosed(opts, from, owned); err != nil {
		return nil, err
	}
	id, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	return b.switchSession(opts, from, "session.json.dsh-"+id)
}
func (b rotatingControlledBackend) sessionGeneration(SessionOptions) (string, error) {
	return "session.json.dsh", nil
}
func (b rotatingControlledBackend) checkRotationClosed(SessionOptions, string, bool) error {
	return nil
}
func (b rotatingControlledBackend) switchSession(SessionOptions, string, string) (Backend, error) {
	return b, nil
}
