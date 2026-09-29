// Package wire names what masters and nodes exchange over NATS; it has no
// dependencies so the node agent stays small.
package wire

import "time"

// ProbeSubject is where a node measures a master; the reply carries the
// requested number of bytes and the master's load in the Load header.
func ProbeSubject(master string) string { return "probe." + master }

// MaxProbeBytes caps a probe reply below NATS' 1 MiB max payload, headers included.
const MaxProbeBytes = 512 << 10

// NodeCN is the certificate common name of a node's client certificate.
func NodeCN(node string) string { return "node." + node }

// NodeInbox is the reply prefix a node must use (nats.CustomInboxPrefix).
func NodeInbox(node string) string { return "_INBOX." + node }

// DesiredSubject is where a node asks master for its desired state.
func DesiredSubject(master, node string) string { return "desired." + master + "." + node }

// ReportSubject is where a node bound to master publishes its reports.
func ReportSubject(master, node string) string { return "rep." + master + "." + node }

// ReportRetention is how long masters keep raw node reports in JetStream;
// a master away for longer loses them (Postgres keeps the totals).
const ReportRetention = 7 * 24 * time.Hour
