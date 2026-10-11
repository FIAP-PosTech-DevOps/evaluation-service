package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	(&App{}).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("corpo inesperado: %s", rec.Body.String())
	}
}

func TestEvaluateExigeParametros(t *testing.T) {
	for _, query := range []string{"", "?user_id=u1", "?flag_name=f1"} {
		t.Run(query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			(&App{}).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/evaluate"+query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, esperado 400", rec.Code)
			}
		})
	}
}
