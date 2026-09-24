package backup

import "testing"

func TestInspectKeyReferenceRejectsNilContext(t *testing.T) {
	if _, err := InspectKeyReference(nil, ""); err == nil {
		t.Fatal("InspectKeyReference accepted a nil context")
	}
}
