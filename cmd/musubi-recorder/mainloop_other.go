//go:build !darwin

package main

// withMainLoop runs body: only macOS needs its main thread kept for AppKit.
func withMainLoop(body func() error) error { return body() }
