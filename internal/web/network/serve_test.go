package network

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

type failingListener struct{ err error }

func (l failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (failingListener) Close() error                { return nil }
func (failingListener) Addr() net.Addr              { return testAddr("failing") }

type testAddr string

func (a testAddr) Network() string { return string(a) }
func (a testAddr) String() string  { return string(a) }

func TestServeHTTPLogsUnexpectedListenerFailure(t *testing.T) {
	errInjected := errors.New("injected listener failure")
	ServeHTTP(&http.Server{}, failingListener{err: errInjected}, "Test server")

	for _, line := range logger.GetLogs(100, "error") {
		if strings.Contains(line, errInjected.Error()) {
			return
		}
	}
	t.Fatal("unexpected listener failure was not recorded in the panel log")
}

func TestServeHTTPSuppressesNormalServerClose(t *testing.T) {
	const marker = "normal-close-must-stay-silent"
	ServeHTTP(&http.Server{}, failingListener{err: http.ErrServerClosed}, marker)

	for _, line := range logger.GetLogs(100, "error") {
		if strings.Contains(line, marker) {
			t.Fatalf("normal http.ErrServerClosed was recorded as an error: %s", line)
		}
	}
}

func TestProductionHTTPServersUseServeHTTPWrapper(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
	fset := token.NewFileSet()

	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == currentFile || path == filepath.Join(filepath.Dir(currentFile), "serve.go") {
			return nil
		}

		parsed, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		usesHTTP := false
		for _, imp := range parsed.Imports {
			if imp.Path.Value == `"net/http"` {
				usesHTTP = true
				break
			}
		}
		if !usesHTTP {
			return nil
		}

		parsed, err = parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		servers := httpServerNames(parsed)
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Serve" {
				return true
			}
			if !isHTTPServer(selector.X, servers) {
				return true
			}
			position := fset.Position(call.Pos())
			t.Errorf("direct Serve call at %s; production HTTP servers must use network.ServeHTTP", position)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go files: %v", err)
	}
}

// httpServerNames collects the identifiers a file declares from an http.Server
// literal, so a call on a raw TCP router or a package function named Serve is
// not mistaken for a production HTTP server.
func httpServerNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if i < len(n.Rhs) && isHTTPServer(n.Rhs[i], nil) {
					if id, ok := lhs.(*ast.Ident); ok {
						names[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if i < len(n.Values) && isHTTPServer(n.Values[i], nil) {
					names[name.Name] = true
				}
			}
		}
		return true
	})
	return names
}

// isHTTPServer reports whether expr is a *http.Server literal, or an identifier
// already collected as one.
func isHTTPServer(expr ast.Expr, names map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return names[e.Name]
	case *ast.UnaryExpr:
		return isHTTPServerLiteral(e.X)
	case *ast.CompositeLit:
		return isHTTPServerLiteral(e)
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "new" && len(e.Args) == 1 {
			return isHTTPServerType(e.Args[0])
		}
	}
	return false
}

func isHTTPServerLiteral(expr ast.Expr) bool {
	lit, ok := expr.(*ast.CompositeLit)
	return ok && isHTTPServerType(lit.Type)
}

func isHTTPServerType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http" && sel.Sel.Name == "Server"
}
