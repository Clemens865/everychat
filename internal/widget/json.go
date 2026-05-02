package widget

import "encoding/json"

// jsonUnmarshal is a tiny indirection so widget.go doesn't import
// encoding/json directly (keeps the surface scannable).
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
