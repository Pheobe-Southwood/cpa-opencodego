// Package abi implements the JSON envelope protocol that the CLIProxyAPI
// plugin host speaks over its C ABI.
//
// Every RPC is a JSON envelope in and a JSON envelope out:
//
//	{"ok":true,"result":{...}}   or   {"ok":false,"error":{"code":"...","message":"..."}}
//
// Field names inside result follow the CLIProxyAPI SDK structs. sdk/pluginapi
// types carry no json tags, so they travel with their Go field names
// (PascalCase); rpc wrapper structs use snake_case where noted.
package abi

import (
	"encoding/json"
	"fmt"
)

// HostCall invokes a host callback such as host.stream.emit. It is wired by
// the cgo bridge in package main; it is nil before cliproxy_plugin_init runs.
var HostCall func(method string, payload []byte) ([]byte, error)

// Envelope is the wire format shared by the host and this plugin.
type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a structured plugin error. HTTPStatus is mapped by the host onto the
// downstream client response, so upstream upstream failures keep their status.
type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// StatusError is an error carrying a downstream HTTP status.
type StatusError struct {
	Status  int
	CodeStr string
	Err     error
}

func (e *StatusError) Error() string { return e.Err.Error() }
func (e *StatusError) Unwrap() error { return e.Err }

// StatusErrorf builds a StatusError with a formatted message.
func StatusErrorf(status int, code, format string, args ...any) *StatusError {
	return &StatusError{Status: status, CodeStr: code, Err: fmt.Errorf(format, args...)}
}

// Status extracts the HTTP status carried by err, or 0 when there is none.
func Status(err error) int {
	var target *StatusError
	if ok := asStatusError(err, &target); ok {
		return target.Status
	}
	return 0
}

func asStatusError(err error, target **StatusError) bool {
	for err != nil {
		if current, ok := err.(*StatusError); ok {
			*target = current
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// OK marshals v into a successful envelope.
func OK(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

// FailFor converts err into a failure envelope, preserving its HTTP status.
func FailFor(err error, fallbackCode string) []byte {
	if err == nil {
		return Fail("plugin_error", "unknown plugin error", 500)
	}
	code := fallbackCode
	var statusErr *StatusError
	if ok := asStatusError(err, &statusErr); ok && statusErr.CodeStr != "" {
		code = statusErr.CodeStr
	}
	raw, _ := json.Marshal(Envelope{OK: false, Error: &Error{
		Code:       code,
		Message:    err.Error(),
		HTTPStatus: Status(err),
	}})
	return raw
}

// Fail builds a failure envelope.
func Fail(code, message string, status int) []byte {
	raw, _ := json.Marshal(Envelope{OK: false, Error: &Error{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
	}})
	return raw
}

// CallHost sends a host callback and decodes its result into out. out may be
// nil when the caller does not care about the result.
func CallHost(method string, request any, out any) error {
	if HostCall == nil {
		return fmt.Errorf("host callback %s is unavailable", method)
	}
	var payload []byte
	if request != nil {
		raw, errMarshal := json.Marshal(request)
		if errMarshal != nil {
			return fmt.Errorf("marshal %s request: %w", method, errMarshal)
		}
		payload = raw
	}
	raw, errCall := HostCall(method, payload)
	if errCall != nil {
		return fmt.Errorf("host callback %s: %w", method, errCall)
	}
	var env Envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		return fmt.Errorf("host callback %s: decode envelope: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return fmt.Errorf("host callback %s: %s", method, env.Error.Message)
		}
		return fmt.Errorf("host callback %s failed", method)
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	if errUnmarshal := json.Unmarshal(env.Result, out); errUnmarshal != nil {
		return fmt.Errorf("host callback %s: decode result: %w", method, errUnmarshal)
	}
	return nil
}
