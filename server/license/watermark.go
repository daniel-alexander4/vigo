package license

import (
	"strings"
	"unicode/utf8"
)

// Zero-width Unicode characters used for binary encoding.
const (
	zwBit0 = '\u200B' // zero-width space = binary 0
	zwBit1 = '\u200C' // zero-width non-joiner = binary 1
)

// marker is a 4-character sequence that precedes the encoded license ID.
var marker = []rune{zwBit0, zwBit1, zwBit0, zwBit1}

// EncodeWatermark embeds a license ID into HTML content using zero-width
// Unicode characters inserted between words. The watermark is invisible
// to readers but survives copy-paste and can be decoded back to the
// license ID with DecodeWatermark.
func EncodeWatermark(html string, licenseID string) string {
	if licenseID == "" {
		return html
	}

	// Encode license ID as binary using zero-width characters.
	var encoded []rune
	encoded = append(encoded, marker...)
	for _, b := range []byte(licenseID) {
		for bit := 7; bit >= 0; bit-- {
			if b>>uint(bit)&1 == 1 {
				encoded = append(encoded, zwBit1)
			} else {
				encoded = append(encoded, zwBit0)
			}
		}
	}

	watermark := string(encoded)

	// Find word boundaries in text content (outside HTML tags) and insert
	// the watermark spread across the document. We insert the full watermark
	// at the first suitable word boundary for simplicity and reliability —
	// partial copies of the document may not contain it, but any copy that
	// includes a reasonable chunk of text will.
	inserted := false
	var result strings.Builder
	result.Grow(len(html) + len(watermark))
	inTag := false
	wordCount := 0

	for i := 0; i < len(html); {
		r, size := utf8.DecodeRuneInString(html[i:])
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag && r == ' ' {
			wordCount++
			// Insert after the 5th word boundary in text content.
			if !inserted && wordCount == 5 {
				result.WriteRune(r)
				result.WriteString(watermark)
				i += size
				inserted = true
				continue
			}
		}
		result.WriteString(html[i : i+size])
		i += size
	}

	// If document was too short, append at end.
	if !inserted {
		result.WriteString(watermark)
	}

	return result.String()
}

// DecodeWatermark scans text for an embedded watermark and returns the
// decoded license ID. Returns empty string if no watermark is found.
func DecodeWatermark(text string) string {
	runes := []rune(text)

	// Scan for marker sequence.
	markerLen := len(marker)
	start := -1
	for i := 0; i <= len(runes)-markerLen; i++ {
		match := true
		for j := 0; j < markerLen; j++ {
			if runes[i+j] != marker[j] {
				match = false
				break
			}
		}
		if match {
			start = i + markerLen
			break
		}
	}
	if start < 0 {
		return ""
	}

	// Read zero-width characters after the marker.
	var bits []byte
	for i := start; i < len(runes); i++ {
		switch runes[i] {
		case zwBit0:
			bits = append(bits, 0)
		case zwBit1:
			bits = append(bits, 1)
		default:
			// Skip non-watermark characters (regular text between ZW chars).
			continue
		}
	}

	// Convert bits to bytes.
	if len(bits) < 8 {
		return ""
	}
	nBytes := len(bits) / 8
	result := make([]byte, nBytes)
	for i := 0; i < nBytes; i++ {
		var b byte
		for bit := 0; bit < 8; bit++ {
			if bits[i*8+bit] == 1 {
				b |= 1 << uint(7-bit)
			}
		}
		// Stop at null bytes or non-printable characters (end of ID).
		if b == 0 || b < 0x20 {
			return string(result[:i])
		}
		result[i] = b
	}
	return string(result)
}
