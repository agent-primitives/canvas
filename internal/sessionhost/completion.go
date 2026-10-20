package sessionhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const documentCompletionLimit = 4 << 20

func validateDocumentFormat(raw string) error {
	var f struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string         `json:"name"`
			Strict bool           `json:"strict"`
			Schema map[string]any `json:"schema"`
		} `json:"json_schema"`
	}
	if len(raw) > 16384 || !decodeCompletionJSON([]byte(raw), &f, true) || f.Type != "json_schema" || !f.JSONSchema.Strict || f.JSONSchema.Name == "" || len(f.JSONSchema.Name) > 64 || f.JSONSchema.Schema["type"] != "object" {
		return errors.New("model request: native generation requires strict JSON schema")
	}
	return nil
}

// The unary adapter discards finish/refusal metadata. Check the bounded complete
// response before handing it over; document JSON/schema validation remains Canvas's job.
func validateDocumentCompletion(raw []byte) error {
	var response struct {
		Choices []struct {
			Index   int     `json:"index"`
			Finish  *string `json:"finish_reason"`
			Message struct {
				Role    string          `json:"role"`
				Content *string         `json:"content"`
				Refusal json.RawMessage `json:"refusal"`
				Tools   json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	invalid := errors.New("model response: incomplete or refused native completion")
	// Strict/lossless JSON validation rejects duplicate keys and ambiguous text;
	// decode known outcome fields separately to tolerate provider metadata additions.
	if len(raw) > documentCompletionLimit || !decodeCompletionJSON(raw, &response, false) || len(response.Choices) != 1 {
		return invalid
	}
	c := response.Choices[0]
	if c.Index != 0 || c.Finish == nil || *c.Finish != "stop" || (c.Message.Role != "" && c.Message.Role != "assistant") || c.Message.Content == nil || strings.TrimSpace(*c.Message.Content) == "" || presentCompletionField(c.Message.Refusal) || presentCompletionField(c.Message.Tools) {
		return invalid
	}
	return nil
}

// OpenAI payloads are not AgentSuite/JCS documents: schema numbers such as
// 1e400 must survive every validation pass without float64 conversion.
func decodeCompletionJSON(raw []byte, dst any, closed bool) bool {
	if !utf8.Valid(raw) || !validCompletionEscapes(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if uniqueCompletionValue(d, 0, !closed) != nil {
		return false
	}
	if _, err := d.Token(); err != io.EOF {
		return false
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if closed {
		d.DisallowUnknownFields()
	}
	return d.Decode(dst) == nil
}

// The adapter folds provider field names. Reject colliding response keys so a
// differently cased alias cannot overwrite a refusal or a non-stop finish.
// Request schemas keep their case-sensitive property names.
func foldCompletionKey(key string) string {
	return strings.Map(func(r rune) rune {
		for {
			next := unicode.SimpleFold(r)
			if next <= r {
				return next
			}
			r = next
		}
	}, key)
}

// Reject lossy Unicode escapes before encoding/json can replace them with U+FFFD.
// JSON syntax remains the decoder's responsibility; escaped backslashes are data.
func validCompletionEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i == len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		unit, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil || unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
		i += 4
		if unit < 0xd800 || unit > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func uniqueCompletionValue(d *json.Decoder, depth int, foldKeys bool) error {
	if depth > 128 {
		return errors.New("JSON nesting limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("invalid JSON value")
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			token, err = d.Token()
			key, ok := token.(string)
			if foldKeys {
				key = foldCompletionKey(key)
			}
			if err != nil || !ok || seen[key] {
				return errors.New("invalid or duplicate JSON key")
			}
			seen[key] = true
		}
		if err = uniqueCompletionValue(d, depth+1, foldKeys); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func presentCompletionField(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(raw, []byte("null"))
}

func bufferDocumentCompletion(resp *http.Response) (*http.Response, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("model response: missing native completion body")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("model response: native provider did not succeed")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, documentCompletionLimit+1))
	if err != nil {
		return nil, errors.New("model response: native completion unreadable")
	}
	if err = validateDocumentCompletion(raw); err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	return resp, nil
}
