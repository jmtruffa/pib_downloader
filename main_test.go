package main

import "testing"

func TestEvaluateCountDrop(t *testing.T) {
	tests := []struct {
		name      string
		parsed    int
		current   int
		force     bool
		wantError bool
	}{
		{"tabla vacía: primera carga", 9655, 0, false, false},
		{"mismo conteo", 9655, 9655, false, false},
		{"crecimiento por publicación nueva", 10075, 9655, false, false},
		{"caída chica dentro del margen", 9000, 9655, false, false},
		{"caída justo en el límite del 10%", 8690, 9655, false, false},
		{"caída apenas por encima del límite", 8600, 9655, false, true},
		{"archivo equivocado: 1062 contra 9655", 1062, 9655, false, true},
		{"caída grande con -force", 1062, 9655, true, false},
		{"cero observaciones contra tabla poblada", 0, 9655, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := evaluateCountDrop(tt.parsed, tt.current, tt.force)
			if tt.wantError && err == nil {
				t.Errorf("evaluateCountDrop(%d, %d, %v) = nil, se esperaba error",
					tt.parsed, tt.current, tt.force)
			}
			if !tt.wantError && err != nil {
				t.Errorf("evaluateCountDrop(%d, %d, %v) = %v, se esperaba nil",
					tt.parsed, tt.current, tt.force, err)
			}
		})
	}
}

func TestNormalizeSheetName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"cuadro 1", "cuadro 1"},
		{"Cuadro 1", "cuadro 1"},
		{"CUADRO 1", "cuadro 1"},
		{"  cuadro   1  ", "cuadro 1"},
		{"cuadro 10", "cuadro 10"},
	}

	for _, tt := range tests {
		if got := normalizeSheetName(tt.in); got != tt.want {
			t.Errorf("normalizeSheetName(%q) = %q, se esperaba %q", tt.in, got, tt.want)
		}
	}

	// The reason lookups use exact equality on the normalized name instead of a
	// prefix match: "cuadro 1" must never resolve to "cuadro 10".
	if normalizeSheetName("cuadro 1") == normalizeSheetName("cuadro 10") {
		t.Error("\"cuadro 1\" y \"cuadro 10\" no deben normalizar al mismo valor")
	}
}
