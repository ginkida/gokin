// Package testkit provides cross-package test helpers for gokin.
//
// This package contains test doubles (MockClient), fixture helpers
// (ResolvedTempDir), and utilities shared between test suites in multiple
// packages. It is only imported from _test.go files and should never appear
// in production builds.
package testkit
