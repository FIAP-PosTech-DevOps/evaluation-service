package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Fluxo completo do GET /evaluate: cache no Redis, chamada aos serviços de
// flag e de regra, decisão e envio do evento. Os dublês estão em fakes_test.go.

const (
	flagLigada   = `{"id":1,"name":"novo-checkout","is_enabled":true}`
	regraTodos   = `{"flag_name":"novo-checkout","is_enabled":true,"rules":{"type":"PERCENTAGE","value":100}}`
	regraNinguem = `{"flag_name":"novo-checkout","is_enabled":true,"rules":{"type":"PERCENTAGE","value":0}}`
)

type cenario struct {
	app   *App
	redis *fakeRedis
}

func novoCenario(t *testing.T, flagURL, ruleURL string) cenario {
	t.Helper()
	r := newFakeRedis(t)
	return cenario{
		redis: r,
		app: &App{
			RedisClient:         r.client(t),
			HttpClient:          &http.Client{Timeout: 2 * time.Second},
			FlagServiceURL:      flagURL,
			TargetingServiceURL: ruleURL,
		},
	}
}

func avaliar(t *testing.T, app *App) (int, EvaluationResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/evaluate?user_id=user-42&flag_name=novo-checkout", nil)
	app.routes().ServeHTTP(rec, req)
	var resp EvaluationResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("resposta não é JSON: %v (%s)", err, rec.Body.String())
		}
	}
	return rec.Code, resp
}

func TestEvaluateBuscaNosServicosEGuardaNoCache(t *testing.T) {
	t.Setenv("SERVICE_API_KEY", "tm_key_servico")
	authRecebido := make(chan string, 1)
	flagSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authRecebido <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(flagLigada))
	}))
	t.Cleanup(flagSrv.Close)
	c := novoCenario(t, flagSrv.URL, jsonServer(t, http.StatusOK, regraTodos).URL)

	code, resp := avaliar(t, c.app)

	if code != http.StatusOK || !resp.Result {
		t.Fatalf("status = %d, result = %v; esperado 200 e true", code, resp.Result)
	}
	if got := <-authRecebido; got != "Bearer tm_key_servico" {
		t.Errorf("flag-service recebeu Authorization %q", got)
	}
	if _, ok := c.redis.get("flag_info:novo-checkout"); !ok {
		t.Error("a decisão não foi guardada no cache")
	}
}

func TestEvaluateUsaOCache(t *testing.T) {
	// Serviços fora do ar: se o cache não fosse usado, a resposta seria 502.
	c := novoCenario(t, jsonServer(t, http.StatusInternalServerError, "").URL, "")
	c.redis.set("flag_info:novo-checkout", `{"Flag":{"name":"novo-checkout","is_enabled":true},"Rule":null}`)

	code, resp := avaliar(t, c.app)

	if code != http.StatusOK || !resp.Result {
		t.Errorf("status = %d, result = %v; esperado 200 e true", code, resp.Result)
	}
}

func TestEvaluateCacheCorrompidoViraMiss(t *testing.T) {
	c := novoCenario(t, jsonServer(t, http.StatusOK, flagLigada).URL, jsonServer(t, http.StatusOK, regraNinguem).URL)
	c.redis.set("flag_info:novo-checkout", "{isto não é json")

	code, resp := avaliar(t, c.app)

	if code != http.StatusOK || resp.Result {
		t.Errorf("status = %d, result = %v; esperado 200 e false (regra de 0%%)", code, resp.Result)
	}
}

func TestEvaluateFalhaAoGravarCacheNaoAfetaResposta(t *testing.T) {
	c := novoCenario(t, jsonServer(t, http.StatusOK, flagLigada).URL, jsonServer(t, http.StatusNotFound, "").URL)
	c.redis.failSet = true

	code, resp := avaliar(t, c.app)

	if code != http.StatusOK || !resp.Result {
		t.Errorf("status = %d, result = %v; esperado 200 e true", code, resp.Result)
	}
}

func TestEvaluateFlagInexistenteRespondeFalse(t *testing.T) {
	c := novoCenario(t, jsonServer(t, http.StatusNotFound, "").URL, jsonServer(t, http.StatusNotFound, "").URL)

	code, resp := avaliar(t, c.app)

	if code != http.StatusOK || resp.Result {
		t.Errorf("status = %d, result = %v; esperado 200 e false", code, resp.Result)
	}
}

func TestEvaluateErroNoFlagServiceRetorna502(t *testing.T) {
	regra := jsonServer(t, http.StatusOK, regraTodos).URL
	fora := httptest.NewServer(http.NotFoundHandler())
	fora.Close() // endereço válido, mas ninguém escutando

	casos := map[string]string{
		"status 500":         jsonServer(t, http.StatusInternalServerError, "").URL,
		"JSON inválido":      jsonServer(t, http.StatusOK, "{").URL,
		"resposta truncada":  truncatedServer(t).URL,
		"serviço fora do ar": fora.URL,
		"URL inválida":       "http://[::1",
	}
	for nome, flagURL := range casos {
		t.Run(nome, func(t *testing.T) {
			c := novoCenario(t, flagURL, regra)
			if code, _ := avaliar(t, c.app); code != http.StatusBadGateway {
				t.Errorf("status = %d, esperado 502", code)
			}
		})
	}
}

func TestEvaluateErroNaRegraUsaOPadrao(t *testing.T) {
	// Sem regra utilizável, vale a flag: ligada = true para todos.
	casos := map[string]string{
		"status 500":        jsonServer(t, http.StatusInternalServerError, "").URL,
		"JSON inválido":     jsonServer(t, http.StatusOK, "{").URL,
		"resposta truncada": truncatedServer(t).URL,
		"URL inválida":      "http://[::1",
	}
	flag := jsonServer(t, http.StatusOK, flagLigada).URL
	for nome, ruleURL := range casos {
		t.Run(nome, func(t *testing.T) {
			c := novoCenario(t, flag, ruleURL)
			code, resp := avaliar(t, c.app)
			if code != http.StatusOK || !resp.Result {
				t.Errorf("status = %d, result = %v; esperado 200 e true", code, resp.Result)
			}
		})
	}
}

func TestEnviaEventoParaOSQS(t *testing.T) {
	cliente, recebidos := fakeSQS(t, http.StatusOK)
	app := &App{SqsSvc: cliente, SqsQueueURL: "https://sqs.us-east-1.amazonaws.com/000000000000/eventos"}

	app.sendEvaluationEvent("user-42", "novo-checkout", true)

	corpo := <-recebidos
	for _, trecho := range []string{`"QueueUrl":"https://sqs.us-east-1.amazonaws.com/000000000000/eventos"`, `novo-checkout`, `user-42`} {
		if !strings.Contains(corpo, trecho) {
			t.Errorf("requisição ao SQS sem %s: %s", trecho, corpo)
		}
	}
}

func TestErroDoSQSNaoDerrubaOServico(t *testing.T) {
	cliente, recebidos := fakeSQS(t, http.StatusBadRequest)
	app := &App{SqsSvc: cliente, SqsQueueURL: "https://sqs.us-east-1.amazonaws.com/000000000000/nao-existe"}

	app.sendEvaluationEvent("user-42", "novo-checkout", false) // só registra o erro no log

	<-recebidos
}

func TestSemSQSConfiguradoSoRegistraNoLog(t *testing.T) {
	(&App{}).sendEvaluationEvent("user-42", "novo-checkout", true)
}

func TestWriteJSONComValorInvalido(t *testing.T) {
	// Um canal não vira JSON: o erro só é registrado no log, sem pânico.
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, make(chan int))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, esperado 200", rec.Code)
	}
}
