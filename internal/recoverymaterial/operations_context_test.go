package recoverymaterial

import "testing"

func TestOperationsRejectNilContext(t *testing.T) {
	if _, err := Export(nil, "", "", "", "", nil, nil); err == nil {
		t.Fatal("Export accepted a nil context")
	}
	if _, err := Verify(nil, "", nil); err == nil {
		t.Fatal("Verify accepted a nil context")
	}
	if _, err := Import(nil, "", "", "", nil); err == nil {
		t.Fatal("Import accepted a nil context")
	}
}
