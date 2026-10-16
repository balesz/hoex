package approval

import (
	"path/filepath"
	"testing"
)

func TestStore(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "approved")}
	if _, ok, err := s.Load("/p/a"); ok || err != nil {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	if err := s.Save("/p/a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("/p/b", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("/p/a", []byte("a2")); err != nil {
		t.Fatal(err)
	}
	for root, want := range map[string]string{"/p/a": "a2", "/p/b": "b"} {
		data, ok, err := s.Load(root)
		if !ok || err != nil || string(data) != want {
			t.Errorf("Load(%s) = %q, %v, %v; want %q", root, data, ok, err, want)
		}
	}
	// An approved missing file is stored as empty content.
	if err := s.Save("/p/c", nil); err != nil {
		t.Fatal(err)
	}
	if data, ok, _ := s.Load("/p/c"); !ok || len(data) != 0 {
		t.Errorf("empty content: %q, %v", data, ok)
	}
}
