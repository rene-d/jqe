package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// Item is a JSON value read from the input.
type Item struct {
	Raw   []byte // compact JSON, original key order
	Value any    // normalized value for gojq
}

// loadItems reads a stream of concatenated JSON values (JSON Lines or not).
func loadItems(r io.Reader) ([]Item, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var items []Item
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			return items, nil
		} else if err != nil {
			return items, fmt.Errorf("value #%d: %w", len(items)+1, err)
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return items, err
		}
		v, err := decodeValue(buf.Bytes())
		if err != nil {
			return items, err
		}
		items = append(items, Item{Raw: buf.Bytes(), Value: v})
	}
}

func decodeValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return normalize(v), nil
}

// normalize converts json.Number into the numeric types gojq accepts.
func normalize(v any) any {
	switch v := v.(type) {
	case json.Number:
		s := v.String()
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		if !strings.ContainsAny(s, ".eE") {
			if n, ok := new(big.Int).SetString(s, 10); ok {
				return n
			}
		}
		f, _ := v.Float64()
		return f
	case []any:
		for i, e := range v {
			v[i] = normalize(e)
		}
	case map[string]any:
		for k, e := range v {
			v[k] = normalize(e)
		}
	}
	return v
}

type Kind int

const (
	KNull Kind = iota
	KBool
	KNumber
	KString
	KArray
	KObject
)

// Node is a JSON value that keeps key order, for the tree view.
type Node struct {
	Kind     Kind
	Raw      string   // JSON text of scalars
	Keys     []string // object keys, in original order
	Children []*Node
}

func (n *Node) IsContainer() bool { return n.Kind == KArray || n.Kind == KObject }

func parseNode(data []byte) (*Node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return readNode(dec)
}

func readNode(dec *json.Decoder) (*Node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		n := &Node{Kind: KArray}
		if t == '{' {
			n.Kind = KObject
		}
		for dec.More() {
			if n.Kind == KObject {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				n.Keys = append(n.Keys, kt.(string))
			}
			child, err := readNode(dec)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, child)
		}
		_, err := dec.Token() // closing delimiter
		return n, err
	case string:
		return &Node{Kind: KString, Raw: quoteJSON(t)}, nil
	case json.Number:
		return &Node{Kind: KNumber, Raw: t.String()}, nil
	case bool:
		return &Node{Kind: KBool, Raw: strconv.FormatBool(t)}, nil
	default:
		return &Node{Kind: KNull, Raw: "null"}, nil
	}
}

// quoteJSON returns a JSON string without escaping < > &.
func quoteJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}
