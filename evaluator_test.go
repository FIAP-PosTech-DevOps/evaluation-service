package main

import (
	"fmt"
	"testing"
)

func info(flagEnabled bool, rule *TargetingRule) *CombinedFlagInfo {
	return &CombinedFlagInfo{
		Flag: &Flag{Name: "novo-checkout", IsEnabled: flagEnabled},
		Rule: rule,
	}
}

func percentual(valor any) *TargetingRule {
	return &TargetingRule{IsEnabled: true, Rules: Rule{Type: "PERCENTAGE", Value: valor}}
}

func TestRunEvaluationLogic(t *testing.T) {
	app := &App{}
	casos := []struct {
		nome string
		info *CombinedFlagInfo
		want bool
	}{
		{"flag inexistente", &CombinedFlagInfo{}, false},
		{"flag desligada", info(false, nil), false},
		{"flag ligada sem regra", info(true, nil), true},
		{"regra desligada vale para todos", info(true, &TargetingRule{IsEnabled: false}), true},
		{"100% dos usuários", info(true, percentual(100.0)), true},
		{"0% dos usuários", info(true, percentual(0.0)), false},
		{"valor da regra não numérico", info(true, percentual("cinquenta")), false},
		{"tipo de regra desconhecido", info(true, &TargetingRule{IsEnabled: true, Rules: Rule{Type: "USER_LIST"}}), false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if got := app.runEvaluationLogic(c.info, "user-42"); got != c.want {
				t.Errorf("resultado = %v, esperado %v", got, c.want)
			}
		})
	}
}

func TestBucketDeterministicoENoIntervalo(t *testing.T) {
	for i := 0; i < 1000; i++ {
		entrada := fmt.Sprintf("user-%d:novo-checkout", i)
		b := getDeterministicBucket(entrada)
		if b < 0 || b > 99 {
			t.Fatalf("bucket fora de 0-99: %d", b)
		}
		if b != getDeterministicBucket(entrada) {
			t.Fatalf("bucket mudou para a mesma entrada %q", entrada)
		}
	}
}

func TestBucketDistribuido(t *testing.T) {
	// Com 10.000 usuários, um rollout de 50% deve ficar perto de 50%.
	dentro := 0
	for i := 0; i < 10000; i++ {
		if getDeterministicBucket(fmt.Sprintf("user-%d", i)) < 50 {
			dentro++
		}
	}
	if dentro < 4700 || dentro > 5300 {
		t.Errorf("distribuição desbalanceada: %d de 10000 no rollout de 50%%", dentro)
	}
}

func TestNotFoundErrorMensagem(t *testing.T) {
	err := &NotFoundError{FlagName: "x"}
	if err.Error() != "flag ou regra 'x' não encontrada" {
		t.Errorf("mensagem inesperada: %s", err.Error())
	}
}
