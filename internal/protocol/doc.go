// Package protocol defines the shared wire formats, message envelopes,
// WebSocket frames, and REST schemas used by the TalkIntent Hub, Client
// Daemons, CLI, and Web UI.
//
// All types in this package are frozen across work packages to guarantee
// decoupled, parallel implementation without merge conflicts.
package protocol
