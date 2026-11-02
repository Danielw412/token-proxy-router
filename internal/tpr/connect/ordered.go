package connect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// object is a JSON object that remembers key order and leaves values it does not
// need to change exactly as they were, so a merge shows up as a small diff.
type object struct {
	members []member
}

type member struct {
	key   string
	value json.RawMessage
}

func parseObject(raw []byte) (*object, error) {
	obj := &object{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return obj, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("expected a JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("expected a JSON key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		obj.members = append(obj.members, member{key: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return obj, nil
}

func (o *object) get(key string) (json.RawMessage, bool) {
	for _, m := range o.members {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

func (o *object) set(key string, value json.RawMessage) {
	for i := range o.members {
		if o.members[i].key == key {
			o.members[i].value = value
			return
		}
	}
	o.members = append(o.members, member{key: key, value: value})
}

func (o *object) remove(key string) {
	kept := o.members[:0]
	for _, m := range o.members {
		if m.key != key {
			kept = append(kept, m)
		}
	}
	o.members = kept
}

func (o *object) empty() bool { return len(o.members) == 0 }

func (o *object) raw() (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o.members {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := encodeJSON(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// render writes the object using the original file's indentation and final newline.
func (o *object) render(original []byte) ([]byte, error) {
	compact, err := o.raw()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", detectIndent(original)); err != nil {
		return nil, fmt.Errorf("format JSON: %w", err)
	}
	if len(original) == 0 || bytes.HasSuffix(original, []byte("\n")) {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func detectIndent(original []byte) string {
	for _, line := range strings.Split(string(original), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) != len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// encodeJSON marshals without HTML escaping, so URLs stay readable.
func encodeJSON(value any) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
