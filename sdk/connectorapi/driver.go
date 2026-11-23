package connectorapi

import (
	"context"
	"encoding/json"
)

// Driver is a printer dialect. It is registered at compile time and keeps no
// per-device state. A driver also implements Outbound, Inbound or both.
type Driver interface {
	// Baseline is everything this driver can do, for any device. It is called
	// once at registration. A profile or a probe can only narrow it.
	Baseline() Set
}

// Session is one connected printer.
type Session interface {
	// Identity is what the device said it is.
	Identity() Identity

	// Capabilities is the enabled set and its epoch. Neither changes within
	// an epoch.
	Capabilities() (caps Set, epoch uint64)

	// Do runs one command. The host has already validated the arguments
	// against Capabilities and filled in defaults.
	Do(ctx context.Context, cmd Command) (Result, error)

	// State emits updates to the state tree. The channel is closed when the
	// session closes.
	//
	// A send must never block. A driver reads its connection on one
	// goroutine, so a blocked send would also delay command answers.
	//
	// When the host falls behind, drop the oldest patch. Keep a value the
	// device sends only once, such as a dialog text. Count every drop and
	// report the total through CountsLink.
	State() <-chan StatePatch

	// Close releases the session. It is safe to call more than once.
	Close() error
}

// Endpoint is how to reach one device, read from configuration. Only Outbound
// uses it. A device that dials the host identifies itself in the handshake.
type Endpoint struct {
	// Address is the driver-specific location of the device, such as a host and
	// port or a serial device path. The host refuses an address holding "@" or
	// "%40", so a driver never receives a user name or password here.
	Address string `json:"address"`
	// Options carries driver-specific settings the host does not interpret.
	Options map[string]string `json:"options,omitempty"`
}

// Identity is what a device said about itself during the connect exchange.
// The host compares identities with ==, so every field is a plain value.
type Identity struct {
	Vendor   string        `json:"vendor"`
	Model    string        `json:"model"`
	Firmware FirmwareLabel `json:"firmware"`
	Serial   string        `json:"serial"`
	UnitUID  string        `json:"unit_uid"`

	// UnitUIDAlias is a second spelling of the same unit id, for a dialect
	// whose devices send a long and a short form. Empty otherwise.
	//
	// Report it only from a handshake that checked a credential. The short
	// form travels in the clear, so knowing it proves nothing.
	//
	// The host uses it to find the device a new spelling belongs to. It never
	// authorizes anything with it.
	UnitUIDAlias string `json:"unit_uid_alias,omitempty"`
}

// FirmwareLabel is a firmware version as the device reported it.
//
// It is a struct so that < and > do not compile on it. Firmware versions are
// not ordered: "6.4.0" < "6.10.0" is false as text, and a lineup can fork.
// The core selects behaviour from capabilities, never by comparing versions.
// Do not compare the String results either.
type FirmwareLabel struct {
	raw string
}

// NewFirmwareLabel returns the label for a reported firmware string.
func NewFirmwareLabel(raw string) FirmwareLabel { return FirmwareLabel{raw: raw} }

// String returns the reported text.
func (f FirmwareLabel) String() string { return f.raw }

// IsZero reports whether the device reported no firmware at all.
func (f FirmwareLabel) IsZero() bool { return f.raw == "" }

// MarshalText encodes the label as the reported text.
func (f FirmwareLabel) MarshalText() ([]byte, error) { return []byte(f.raw), nil }

// UnmarshalText decodes a reported firmware string.
func (f *FirmwareLabel) UnmarshalText(text []byte) error {
	f.raw = string(text)

	return nil
}

// Command is one request for a printer to do something.
type Command struct {
	// Verb is the capability key being invoked.
	Verb Capability `json:"verb"`
	// Args are the argument values, as text, already validated by the host.
	Args map[string]string `json:"args,omitempty"`
	// Key is the caller's idempotency key. An empty key means the command is
	// not deduplicated.
	Key string `json:"key,omitempty"`
}

// Result is the printer's answer to a command.
//
// A refusal by the printer is a result, not an error: OK is false, FaultCode
// names the refusal and Do returns nil. Return an error when the command did
// not reach the printer, or the printer took it and failed.
type Result struct {
	// OK is whether the printer accepted the command.
	OK bool `json:"ok"`
	// FaultCode is the registered code for a refusal. Empty when OK is true.
	// Clients branch on this, not on MachineReason.
	FaultCode string `json:"fault_code,omitempty"`
	// MachineReason is the printer's own word for the refusal, unchanged.
	// Empty when OK is true or the dialect has no such word. The host keeps
	// at least RetainedTextFloor bytes and truncates the rest.
	MachineReason string `json:"machine_reason,omitempty"`
	// Ignored names the parameters the printer dropped.
	Ignored []string `json:"ignored,omitempty"`
	// Substituted names the parameters the printer replaced, and with what.
	Substituted map[string]string `json:"substituted,omitempty"`
	// Raw is the vendor's own reply, verbatim. The host does not store it.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// StatePatch is one update to the addressable state tree.
type StatePatch struct {
	// Path addresses the node, "/"-separated from the printer root. The host
	// keeps at least RetainedTextFloor bytes and refuses a path it would have
	// to cut.
	Path string `json:"path"`
	// Value is the node's new value. Null removes the node.
	//
	// The host keeps at least RetainedTextFloor bytes of a JSON string and
	// truncates the rest. It also limits the size of one device's whole tree.
	Value json.RawMessage `json:"value"`
}
