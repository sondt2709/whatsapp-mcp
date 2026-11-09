package files

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
)

func TestName(t *testing.T) {
	cases := []struct{ given, mime, want string }{
		{"report.pdf", "application/pdf", "report.pdf"},
		{"../../.zshrc", "", "zshrc"},
		{"a/b\\c:d.txt", "", "abcd.txt"},
		{"\u202Egpj.exe", "", "gpj.exe"},
		{"", "image/jpeg", "whatsapp-3EB0ABC.jpg"},
		{"...", "application/pdf", "whatsapp-3EB0ABC.pdf"},
	}
	for _, c := range cases {
		if got := Name(c.given, c.mime, "900002@lid/3EB0ABC"); got != c.want {
			t.Errorf("Name(%q) = %q, want %q", c.given, got, c.want)
		}
	}
	long := strings.Repeat("é", 300) + ".pdf"
	if got := Name(long, "", "x/y"); len(got) > maxName || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("long name: %d bytes, %q", len(got), got[len(got)-8:])
	}
}

func TestSaveNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Save(dir, "report.pdf", strings.NewReader("new"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(s.Path) != "report (1).pdf" {
		t.Fatalf("saved as %q, want report (1).pdf", filepath.Base(s.Path))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "report.pdf")); string(b) != "old" {
		t.Fatal("the old file changed")
	}
	sum := sha256.Sum256([]byte("new"))
	if s.SHA256 != hex.EncodeToString(sum[:]) || s.Size != 3 {
		t.Fatalf("hash or size: %+v", s)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("the folder has %d entries; a temporary file stayed?", len(entries))
	}
	if runtime.GOOS == "darwin" {
		if _, err := getxattr(s.Path, QuarantineAttr); err != nil {
			t.Fatalf("no quarantine attribute: %v", err)
		}
	}
}

func TestSaveMissingFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Save(missing, "a.txt", strings.NewReader("x")); api.CodeOf(err) != api.InvalidArgument {
		t.Fatalf("missing folder: %v", err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("Save created the folder")
	}
}

func TestOpenForSend(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{allowed, filepath.Join(allowed, "sub"), filepath.Join(allowed, ".hidden"), outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string) string {
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := write(filepath.Join(allowed, "sub", "ok.pdf"))
	secret := write(filepath.Join(outside, "id_ed25519"))
	hiddenFile := write(filepath.Join(allowed, ".env"))
	inHidden := write(filepath.Join(allowed, ".hidden", "x.txt"))
	link := filepath.Join(allowed, "link.pdf")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	f, _, err := OpenForSend(ok, []string{allowed})
	if err != nil {
		t.Fatalf("allowed file: %v", err)
	}
	f.Close()
	for name, p := range map[string]string{"outside": secret, "hidden file": hiddenFile, "hidden folder": inHidden, "link out": link,
		"dot-dot": filepath.Join(allowed, "..", "outside", "id_ed25519"), "the folder itself": allowed} {
		if _, _, err := OpenForSend(p, []string{allowed}); api.CodeOf(err) != api.PathNotAllowed {
			t.Errorf("%s: err = %v, want path_not_allowed", name, err)
		}
	}
	if _, _, err := OpenForSend("relative.pdf", []string{allowed}); api.CodeOf(err) != api.InvalidArgument {
		t.Errorf("relative path: %v", err)
	}
	big := filepath.Join(allowed, "big.bin")
	if fh, err := os.Create(big); err == nil {
		_ = fh.Truncate(api.MaxFileSize + 1)
		fh.Close()
	}
	if _, _, err := OpenForSend(big, []string{allowed}); api.CodeOf(err) != api.TooLarge {
		t.Errorf("big file: %v", err)
	}
}
