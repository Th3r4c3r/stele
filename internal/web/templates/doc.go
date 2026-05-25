// Package templates contains Templ source (.templ) and the generated
// Go code (*_templ.go). Run `go generate ./...` (or `templ generate`)
// from the repo root after editing any .templ file.
//
// Per ADR-006 D1, the *_templ.go files ARE committed so the build
// pipeline does not depend on the templ binary.
package templates

//go:generate go run github.com/a-h/templ/cmd/templ generate
