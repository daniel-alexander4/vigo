// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package scrier

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// guacInstruction represents a parsed Guacamole protocol instruction.
// Format: <len>.<opcode>,<len>.<arg1>,<len>.<arg2>,...;
type guacInstruction struct {
	Opcode string
	Args   []string
}

// String serializes the instruction back to Guacamole wire format.
func (gi *guacInstruction) String() string {
	var b strings.Builder
	lp := func(s string) { fmt.Fprintf(&b, "%d.%s", len(s), s) }
	lp(gi.Opcode)
	for _, arg := range gi.Args {
		b.WriteByte(',')
		lp(arg)
	}
	b.WriteByte(';')
	return b.String()
}

// readInstruction reads one Guacamole instruction from a buffered reader.
// Returns the raw bytes and the parsed instruction.
func readInstruction(r *bufio.Reader) (*guacInstruction, []byte, error) {
	// Read until ';' which terminates the instruction.
	raw, err := r.ReadBytes(';')
	if err != nil {
		return nil, nil, err
	}

	inst, parseErr := parseInstruction(string(raw))
	if parseErr != nil {
		// Return raw bytes even if parse fails — allows passthrough.
		return nil, raw, parseErr
	}
	return inst, raw, nil
}

// parseInstruction parses a Guacamole instruction string.
// Example: "6.select,3.ssh;" → opcode="select", args=["ssh"]
func parseInstruction(s string) (*guacInstruction, error) {
	s = strings.TrimSuffix(s, ";")
	if s == "" {
		return nil, fmt.Errorf("empty instruction")
	}

	parts := splitGuacFields(s)
	if len(parts) == 0 {
		return nil, fmt.Errorf("no fields in instruction")
	}

	inst := &guacInstruction{
		Opcode: parts[0],
		Args:   parts[1:],
	}
	return inst, nil
}

// splitGuacFields splits a Guacamole instruction into its length-prefixed fields.
// Input: "6.select,1.c,3.ssh"
// Output: ["select", "c", "ssh"]
func splitGuacFields(s string) []string {
	var fields []string
	for len(s) > 0 {
		// Parse length prefix.
		dotIdx := strings.IndexByte(s, '.')
		if dotIdx < 1 {
			break
		}
		length, err := strconv.Atoi(s[:dotIdx])
		if err != nil {
			break
		}
		s = s[dotIdx+1:] // skip past the dot

		if len(s) < length {
			break
		}
		fields = append(fields, s[:length])
		s = s[length:]

		// Skip comma separator.
		if len(s) > 0 && s[0] == ',' {
			s = s[1:]
		}
	}
	return fields
}

// doGuacdHandshake performs the server-side Guacamole handshake with guacd,
// injecting the proxy address as the connection target. Returns the args
// instruction (so the WebSocket bridge knows what guacd expects) and the
// ready instruction.
//
// Handshake flow:
// 1. Server → guacd: select,<protocol>
// 2. guacd → Server: args,<param-names...>
// 3. Server → guacd: size,<w>,<h>,<dpi>
// 4. Server → guacd: audio (empty)
// 5. Server → guacd: video (empty)
// 6. Server → guacd: image (empty)
// 7. Server → guacd: connect,<param-values matching args order>
// 8. guacd → Server: ready,<connection-id>
func doGuacdHandshake(guacdRW *bufio.ReadWriter, protocol, proxyHost, proxyPort string) (connectionID string, err error) {
	// Step 1: select protocol.
	selectInst := &guacInstruction{Opcode: "select", Args: []string{protocol}}
	if _, err := guacdRW.WriteString(selectInst.String()); err != nil {
		return "", fmt.Errorf("send select: %w", err)
	}
	guacdRW.Flush()

	// Step 2: read args from guacd.
	argsInst, _, err := readInstruction(guacdRW.Reader)
	if err != nil {
		return "", fmt.Errorf("read args: %w", err)
	}
	if argsInst == nil || argsInst.Opcode != "args" {
		return "", fmt.Errorf("expected args instruction, got %q", argsInst)
	}

	// Step 3-6: send size, audio, video, image.
	for _, inst := range []*guacInstruction{
		{Opcode: "size", Args: []string{"1024", "768", "96"}},
		{Opcode: "audio", Args: []string{}},
		{Opcode: "video", Args: []string{}},
		{Opcode: "image", Args: []string{}},
	} {
		if _, err := guacdRW.WriteString(inst.String()); err != nil {
			return "", fmt.Errorf("send %s: %w", inst.Opcode, err)
		}
	}

	// Step 7: build connect instruction with values matching the args order.
	connectArgs := buildConnectArgs(argsInst.Args, protocol, proxyHost, proxyPort)
	connectInst := &guacInstruction{Opcode: "connect", Args: connectArgs}
	if _, err := guacdRW.WriteString(connectInst.String()); err != nil {
		return "", fmt.Errorf("send connect: %w", err)
	}
	guacdRW.Flush()

	// Step 8: read ready from guacd.
	readyInst, _, err := readInstruction(guacdRW.Reader)
	if err != nil {
		return "", fmt.Errorf("read ready: %w", err)
	}
	if readyInst == nil {
		return "", fmt.Errorf("nil response from guacd after connect")
	}
	if readyInst.Opcode == "error" {
		errMsg := ""
		if len(readyInst.Args) > 0 {
			errMsg = readyInst.Args[0]
		}
		return "", fmt.Errorf("guacd error: %s", errMsg)
	}
	if readyInst.Opcode != "ready" {
		return "", fmt.Errorf("expected ready, got %q", readyInst.Opcode)
	}

	connID := ""
	if len(readyInst.Args) > 0 {
		connID = readyInst.Args[0]
	}
	return connID, nil
}

// buildConnectArgs maps guacd's required arg names to values, injecting
// the proxy address for hostname/port. Unknown args get empty strings.
func buildConnectArgs(argNames []string, protocol, host, port string) []string {
	values := make([]string, len(argNames))
	for i, name := range argNames {
		switch name {
		case "hostname":
			values[i] = host
		case "port":
			values[i] = port
		// SSH-specific defaults.
		case "terminal-type":
			values[i] = "xterm-256color"
		case "color-scheme":
			values[i] = "gray-black"
		case "font-size":
			values[i] = "14"
		case "enable-sftp":
			if protocol == "ssh" {
				values[i] = "true"
			}
		// RDP-specific defaults.
		case "ignore-cert":
			if protocol == "rdp" {
				values[i] = "true"
			}
		case "security":
			if protocol == "rdp" {
				values[i] = "any"
			}
		case "enable-drive":
			values[i] = "true"
		case "create-drive-path":
			values[i] = "true"
		case "disable-audio":
			values[i] = "true"
		case "server-layout":
			if protocol == "rdp" {
				values[i] = "en-us-qwerty"
			}
		default:
			values[i] = "" // empty = use guacd default
		}
	}
	return values
}

// tunnelPong checks if a WebSocket message is a Guacamole tunnel-level ping
// (internal keepalive with empty opcode) and returns the pong response.
// Format: "0.,4.ping,<len>.<timestamp>;" → "0.,4.pong,<len>.<timestamp>;"
func tunnelPong(raw string) (pong string, isPing bool) {
	if !strings.HasPrefix(raw, "0.,4.ping,") {
		return "", false
	}
	rest := raw[len("0.,4.ping,"):]
	dotIdx := strings.IndexByte(rest, '.')
	semiIdx := strings.IndexByte(rest, ';')
	if dotIdx < 0 || semiIdx < 0 || dotIdx >= semiIdx {
		return "", false
	}
	ts := rest[dotIdx+1 : semiIdx]
	return fmt.Sprintf("0.,4.pong,%d.%s;", len(ts), ts), true
}
