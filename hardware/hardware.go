package hardware

// Watcher is implemented by each hardware component. Start begins background
// polling and publishes events to the bus. Stop shuts the polling loop down
// cleanly and must be safe to call more than once.
type Watcher interface {
	Start() error
	Stop()
}
