package service

import "fmt"

type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return e.Message }

func errBadRequest(msg string) error   { return &HTTPError{Status: 400, Message: msg} }
func errUnauthorized(msg string) error { return &HTTPError{Status: 401, Message: msg} }
func errForbidden(msg string) error    { return &HTTPError{Status: 403, Message: msg} }
func errNotFound(msg string) error     { return &HTTPError{Status: 404, Message: msg} }
func errConflict(msg string) error     { return &HTTPError{Status: 409, Message: msg} }
func errTooMany(msg string) error      { return &HTTPError{Status: 429, Message: msg} }

func StatusFromError(err error) (int, string) {
	if he, ok := err.(*HTTPError); ok {
		return he.Status, he.Message
	}
	return 500, fmt.Sprintf("%v", err)
}
