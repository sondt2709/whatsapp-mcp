package whatsapp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbidden are the whatsmeow functions that send a read receipt, presence
// or a read state of a chat. whatsapp-mcp must keep messages unread.
var forbidden = map[string]bool{
	"MarkRead":            true,
	"SendPresence":        true,
	"SubscribePresence":   true,
	"SendChatPresence":    true,
	"BuildMarkChatAsRead": true,
}

// TestNoReadStateCalls parses every Go file of the module (not the tests)
// and fails when a file calls one of the forbidden functions.
func TestNoReadStateCalls(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s", root)
	}
	files := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "tmp" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		files++
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && forbidden[sel.Sel.Name] {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s calls %s: whatsapp-mcp must not send a read receipt, presence or a read state", rel, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 5 {
		t.Fatalf("parsed only %d files; the walk is wrong", files)
	}
}
