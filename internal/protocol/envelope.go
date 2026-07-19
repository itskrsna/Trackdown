// Package protocol implements Sentry's wire format from scratch: envelopes
// and the event payloads they carry. See https://develop.sentry.dev/sdk/envelopes/
// for the spec this is derived from — but the ground truth used in this
// package's tests is real bytes captured from official Sentry SDKs (see
// testdata/envelopes/ and tools/genfixtures), since SDKs don't always match
// the spec's canonical examples exactly.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// EnvelopeHeader is the first line of a Sentry envelope: metadata that
// applies to the whole envelope rather than any single item.
type EnvelopeHeader struct {
	EventID string   `json:"event_id,omitempty"`
	SentAt  string   `json:"sent_at,omitempty"`
	DSN     string   `json:"dsn,omitempty"`
	SDK     *SDKInfo `json:"sdk,omitempty"`
}

// ItemHeader precedes each item's payload within an envelope.
type ItemHeader struct {
	Type        string `json:"type"`
	Length      *int64 `json:"length,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

// Item is one entry in an envelope: a header plus its raw payload bytes.
// Payload interpretation depends on Header.Type — e.g. ParseEvent for "event".
type Item struct {
	Header  ItemHeader
	Payload []byte
}

// Envelope is a fully parsed Sentry envelope: one header plus zero or more items.
type Envelope struct {
	Header EnvelopeHeader
	Items  []Item
}

// ParseEnvelope parses the Sentry envelope wire format: a newline-delimited
// header line, followed by (item-header-line, item-payload) pairs. An item's
// payload is exactly Length bytes when the item header specifies one;
// otherwise it runs to the next newline (implicit length). Both forms are
// required by the spec and are both exercised by real SDK output.
func ParseEnvelope(data []byte) (*Envelope, error) {
	br := bufio.NewReader(bytes.NewReader(data))

	headerLine, _, err := readLine(br)
	if err != nil {
		return nil, fmt.Errorf("reading envelope header: %w", err)
	}
	var envHeader EnvelopeHeader
	if trimmed := bytes.TrimSpace(headerLine); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &envHeader); err != nil {
			return nil, fmt.Errorf("parsing envelope header: %w", err)
		}
	}

	var items []Item
	for {
		if _, err := br.Peek(1); err == io.EOF {
			break
		}

		itemHeaderLine, atEOF, err := readLine(br)
		if err != nil {
			return nil, fmt.Errorf("reading item header: %w", err)
		}
		if len(bytes.TrimSpace(itemHeaderLine)) == 0 {
			if atEOF {
				break
			}
			continue
		}

		var itemHeader ItemHeader
		if err := json.Unmarshal(itemHeaderLine, &itemHeader); err != nil {
			return nil, fmt.Errorf("parsing item header: %w", err)
		}

		var payload []byte
		if itemHeader.Length != nil {
			payload = make([]byte, *itemHeader.Length)
			if _, err := io.ReadFull(br, payload); err != nil {
				return nil, fmt.Errorf("reading item payload for type %q: %w", itemHeader.Type, err)
			}
			consumeNewline(br)
		} else {
			payload, atEOF, err = readLine(br)
			if err != nil {
				return nil, fmt.Errorf("reading implicit-length payload for type %q: %w", itemHeader.Type, err)
			}
		}

		items = append(items, Item{Header: itemHeader, Payload: payload})
		if atEOF {
			break
		}
	}

	return &Envelope{Header: envHeader, Items: items}, nil
}

// readLine reads bytes up to and including a trailing '\n' (stripped from the
// result, along with a preceding '\r'), reporting whether the underlying
// reader is now exhausted. Unlike bufio.Scanner, this tolerates a final line
// with no trailing newline instead of treating it as an error — envelopes
// commonly end without one.
func readLine(br *bufio.Reader) (line []byte, atEOF bool, err error) {
	line, err = br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	atEOF = err == io.EOF
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line, atEOF, nil
}

// consumeNewline discards a single trailing '\n' after an explicit-length
// item payload, if present. The spec puts a newline after every item's
// payload but tolerates its absence at the very end of the envelope.
func consumeNewline(br *bufio.Reader) {
	b, err := br.Peek(1)
	if err == nil && len(b) > 0 && b[0] == '\n' {
		_, _ = br.Discard(1)
	}
}
