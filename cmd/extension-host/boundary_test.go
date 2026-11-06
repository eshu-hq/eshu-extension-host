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

// isStdlibImport reports whether p is a standard-library import path.
// Every stdlib first segment is dotless (fmt, net/http); every module
// path has a dotted first segment (github.com/..., go.opentelemetry.io).
// This predicate stays pure stdlib: the own-module carve-out lives in
// isAllowedModuleImport below so this guard keeps its exact meaning.
func isStdlibImport(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return !strings.Contains(first, ".")
}

// ownModulePrefix is the one non-stdlib import root the module test
// allows: this module itself. The worker-loop lane (eshu-hq/eshu#8877
// Lane C) added it so internal/worker can depend on the
// cutovercontract seam without tripping the whole-module guard. The
// trailing slash is load-bearing: it keeps lookalike roots such as
// github.com/eshu-hq/eshu-extension-hostile/ outside the carve-out.
const ownModulePrefix = "github.com/eshu-hq/eshu-extension-host/"

// isAllowedModuleImport reports whether p may appear in this module: the
// standard library, or this module itself. Everything else — the in-tree
// monorepo, every claim-plane-capable dependency — still reads as
// disallowed. The banned-prefix test above stays the first lock; this is
// the second.
func isAllowedModuleImport(p string) bool {
	return isStdlibImport(p) || strings.HasPrefix(p, ownModulePrefix)
}

func TestImportBoundaryListIsNonEmpty(t *testing.T) {
	// A boundary test with an empty ban list passes vacuously. This guard
	// pins the list shape: the banned set only grows by deliberate edit.
	if len(bannedImportPrefixes) < 5 {
		t.Fatalf("banned list has %d entries, want at least 5", len(bannedImportPrefixes))
	}
}

func TestIsStdlibImportRejectsClaimPlanePaths(t *testing.T) {
	// The predicate is the load-bearing half of the stdlib-only guard:
	// every claim-plane-capable path a later leaf could reach for must
	// read as non-stdlib, including paths outside the six-prefix ban
	// list (lib/pq-style drivers, other JWT/Otel/pubsub clients).
	for _, p := range []string{"fmt", "net/http", "go/parser", "strings"} {
		if !isStdlibImport(p) {
			t.Errorf("isStdlibImport(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"github.com/eshu-hq/eshu/go/internal/ifa",
		"github.com/jackc/pgx/v5",
		"github.com/neo4j/neo4j-go-driver/v5/neo4j",
		"go.opentelemetry.io/otel",
		"github.com/aws/aws-sdk-go-v2/service/ec2",
		"github.com/golang-jwt/jwt/v5",
		"github.com/lib/pq",
		"go.uber.org/zap",
	} {
		if isStdlibImport(p) {
			t.Errorf("isStdlibImport(%q) = true, want false", p)
		}
	}
}

func TestOwnModuleAllowlistIsNarrow(t *testing.T) {
	// The worker-loop lane (eshu-hq/eshu#8877 Lane C) carves this module
	// itself out of the stdlib-only module test so internal/worker can
	// depend on the cutovercontract seam. The carve-out must stay exactly
	// this module: any other dotted path, including lookalike org paths,
	// still reads as disallowed.
	for _, p := range []string{
		"github.com/eshu-hq/eshu-extension-host/internal/worker",
		"github.com/eshu-hq/eshu-extension-host/internal/cutovercontract",
		"github.com/eshu-hq/eshu-extension-host/cmd/extension-host",
	} {
		if !isAllowedModuleImport(p) {
			t.Errorf("isAllowedModuleImport(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"fmt", "net/http"} {
		if !isAllowedModuleImport(p) {
			t.Errorf("isAllowedModuleImport(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"github.com/eshu-hq/eshu/go/internal/ifa",
		"github.com/eshu-hq/eshu-extension-hostile/internal/worker",
		"github.com/eshu-hq/eshu-extension-host-evil",
		"github.com/jackc/pgx/v5",
		"go.opentelemetry.io/otel",
	} {
		if isAllowedModuleImport(p) {
			t.Errorf("isAllowedModuleImport(%q) = true, want false", p)
		}
	}
}

func TestModuleImportsAreAllowlisted(t *testing.T) {
	// Whole-module invariant behind the PROVENANCE.md no-external-
	// dependencies claim: every import is stdlib or own-module, so no
	// claim-plane-capable dependency can enter under an unlisted path.
	// The worker-loop lane (eshu-hq/eshu#8877 Lane C) opened the
	// own-module carve-out (see isAllowedModuleImport and
	// TestOwnModuleAllowlistIsNarrow); the ban list above is unchanged.
	// (An unlisted path added to go.mod breaks `go mod tidy -diff` and
	// the lint-lane `go list -m` check first; this test is the second
	// lock on the same door.)
	fset := token.NewFileSet()
	for _, path := range moduleGoFiles(t) {
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if !isAllowedModuleImport(p) {
				t.Errorf("%s imports disallowed %q", path, p)
			}
		}
	}
}
