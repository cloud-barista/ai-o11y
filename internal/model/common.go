// Package model holds the request and response bodies of the REST API.
package model

// SimpleMsg is the generic message body returned for errors and simple results.
type SimpleMsg struct {
	Message string `json:"message" example:"Service is ready"`
}
