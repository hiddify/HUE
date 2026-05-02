package xray

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"text/template"

	"github.com/hiddify/hue/pkg/clients"
)

// Renderer turns the ConfigSnapshot HUE returned (template + vars +
// users) into the protocol-specific bytes the running service
// consumes. One renderer covers an entire format family — for xray
// that's "xray-json", and the template itself decides which transports,
// security layers, etc. are configured.
//
// AcceptsFormat lets callers wire multiple renderers to one Client and
// dispatch by Snapshot.TemplateFormat. The default xray Client uses a
// single Renderer; the multi-renderer path is for when one binary
// adapts several protocols.
type Renderer interface {
	Name() string
	AcceptsFormat(format string) bool
	Render(snap clients.ConfigSnapshot) ([]byte, error)
}

// ApplyConfigFunc installs newly-rendered bytes onto the running
// service. Common implementations:
//   - write to a file + send SIGHUP / restart
//   - call xray's HandlerService.AlterInbound RPC
//   - tee to a sidecar that owns the service
type ApplyConfigFunc func(generated []byte) error

// ----------------------------------------------------------------------
// XrayJSON — the default renderer for xray-core inbound JSON.
//
// The template is the literal xray inbounds JSON with Go template
// directives where HUE owns the value. Author's responsibilities:
//   * Reference users via {{range .Users}} ... {{.ID}} ... {{end}}
//     (User.ID is HUE's UUID, used as vless `id` directly).
//   * Reference vars via {{.Vars.<key>}}; missing keys raise an error
//     (Option("missingkey=error")), so typos fail loudly at sync time.
//   * The result MUST parse as JSON — XrayJSON validates this and
//     returns the rendered text alongside the parse error to make
//     debugging fast.
//
// Helper funcs available in templates:
//   * `quote s`       → JSON-encoded string, e.g. {"id": {{.ID | quote}}}
//   * `default d v`   → returns d when v is empty/zero, else v
//   * `json v`        → marshals v to JSON
//   * `atoi s`        → parses string to int (handy for vars carrying
//                       numbers but kept as string in the wire format)
//   * `getVar key d`  → flat lookup into .Vars with default; identical
//                       to {{default "d" (index .Vars "key")}} but lets
//                       templates use dotted keys: {{getVar "xray.path" "/api"}}
//
// Example template (vless over xhttp, with optional WS as a second
// inbound — both reading from the same .Users list):
//
//   {
//     "inbounds": [
//       {
//         "tag": "vless-xhttp",
//         "port": {{.Vars.port}},
//         "protocol": "vless",
//         "settings": {
//           "decryption": "none",
//           "clients": [
//             {{- range $i, $u := .Users -}}
//             {{- if $i}},{{end}}
//             { "id": {{$u.ID | quote}}, "email": {{$u.Username | quote}} }
//             {{- end -}}
//           ]
//         },
//         "streamSettings": {
//           "network": "xhttp",
//           "xhttpSettings": { "path": {{getVar "xray.xhttp.path" "/api" | quote}} }
//         }
//       }
//       {{- if eq (getVar "xray.ws.enabled" "false") "true" -}},
//       {
//         "tag": "vless-ws",
//         "port": {{.Vars.ws_port}},
//         "protocol": "vless",
//         "settings": { "decryption": "none", "clients": [
//           {{- range $i, $u := .Users -}}
//           {{- if $i}},{{end}}
//           { "id": {{$u.ID | quote}} }
//           {{- end -}}
//         ] },
//         "streamSettings": {
//           "network": "ws",
//           "wsSettings": { "path": {{.Vars.ws_path | quote}} }
//         }
//       }
//       {{- end -}}
//     ]
//   }
type XrayJSON struct{}

func (XrayJSON) Name() string { return "xray-json" }

// AcceptsFormat tolerates an empty format string for backward
// compatibility (services created before format was added).
func (XrayJSON) AcceptsFormat(format string) bool {
	return format == "" || format == "xray-json"
}

func (XrayJSON) Render(snap clients.ConfigSnapshot) ([]byte, error) {
	if snap.Template == "" {
		return nil, errors.New("xray: empty config template")
	}

	t, err := template.New("xray-json").
		Option("missingkey=error").
		Funcs(funcMap(snap.Vars)).
		Parse(snap.Template)
	if err != nil {
		return nil, fmt.Errorf("xray: parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, struct {
		Vars  map[string]string
		Users []clients.ConfigUser
	}{snap.Vars, snap.Users}); err != nil {
		return nil, fmt.Errorf("xray: execute template: %w", err)
	}

	// Sanity check: rendered bytes MUST parse as JSON. A template that
	// produces broken JSON gets caught here, with the bad text in the
	// error so the operator can see what slipped through.
	if !json.Valid(buf.Bytes()) {
		return nil, fmt.Errorf("xray: rendered template is not valid JSON:\n%s", buf.String())
	}
	return buf.Bytes(), nil
}

func funcMap(vars map[string]string) template.FuncMap {
	return template.FuncMap{
		"quote": func(s string) string {
			b, _ := json.Marshal(s)
			return string(b)
		},
		"default": func(d, v string) string {
			if v == "" {
				return d
			}
			return v
		},
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"atoi": strconv.Atoi,
		// getVar is the dotted-key escape hatch — Go's text/template
		// can't index a map with a key containing dots via the field
		// shorthand, so we expose this helper.
		"getVar": func(key, def string) string {
			if v, ok := vars[key]; ok && v != "" {
				return v
			}
			return def
		},
	}
}
