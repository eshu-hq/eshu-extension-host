// SPDX-License-Identifier: MIT
// Copyright (c) 2026 eshu-hq

// Import boundary for the dark binary (eshu-hq/eshu#8877 Leaf 2,
// template eshu-hq/eshu#6710): the dark host must not import the in-tree
// monorepo or any claim-plane-capable dependency. Postgres, graph,
// telemetry, cloud, and JWT libraries would each be a claim-flow foothold
// with no dark-mode consumer, so their presence fails the build. A later
// leaf that wires the enabled flow extends this list deliberately.
package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var bannedImportPrefixes = []string{
	"github.com/eshu-hq/eshu/", // in-tree monorepo: cutover uses published contracts, never internal handles
	"github.com/jackc/",        // Postgres: the dark host opens no database
	"github.com/neo4j/",        // graph writes stay core-owned
	"go.opentelemetry.io/",     // no telemetry SDK in the dark binary
	"github.com/aws/aws-sdk-go-v2",
	"github.com/golang-jwt/",
}

func moduleGoFiles(t *testing.T) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("go.mod not found above the test file")
		}
		root = parent
	}
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found under the module root")
	}
	return files
}

func TestImportBoundaryRejectsClaimPlaneImports(t *testing.T) {
	fset := token.NewFileSet()
	for _, path := range moduleGoFiles(t) {
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, banned := range bannedImportPrefixes {
				if strings.HasPrefix(p, banned) {
					t.Fatalf("%s imports banned %q (prefix %q)", path, p, banned)
				}
			}
		}
	}
}

func TestImportBoundaryListIsNonEmpty(t *testing.T) {
	// A boundary test with an empty ban list passes vacuously. This guard
	// pins the list shape: the banned set only grows by deliberate edit.
	if len(bannedImportPrefixes) < 5 {
		t.Fatalf("banned list has %d entries, want at least 5", len(bannedImportPrefixes))
	}
}
