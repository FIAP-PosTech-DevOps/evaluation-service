package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/go-redis/redis/v8"
)

// Dublês de teste sem dependência externa:
//   - fakeRedis: servidor TCP que fala o protocolo do Redis (RESP) só para
//     GET, SET e PING, guardando os valores num mapa;
//   - os serviços de flag e de regra são httptest.Server;
//   - o SQS é um httptest.Server no lugar do endpoint da AWS.

type fakeRedis struct {
	mu      sync.Mutex
	data    map[string]string
	failSet bool
	ln      net.Listener
}

func newFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("não foi possível abrir a porta do Redis falso: %v", err)
	}
	r := &fakeRedis{data: map[string]string{}, ln: ln}
	go r.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return r
}

func (r *fakeRedis) client(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: r.ln.Addr().String()})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func (r *fakeRedis) get(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.data[key]
	return v, ok
}

func (r *fakeRedis) set(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = value
}

func (r *fakeRedis) serve() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return // listener fechado no fim do teste
		}
		go r.handle(conn)
	}
}

func (r *fakeRedis) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	rd := bufio.NewReader(conn)
	for {
		args, err := readCommand(rd)
		if err != nil {
			return
		}
		if _, err := io.WriteString(conn, r.reply(args)); err != nil {
			return
		}
	}
}

func (r *fakeRedis) reply(args []string) string {
	switch strings.ToUpper(args[0]) {
	case "PING":
		return "+PONG\r\n"
	case "GET":
		if v, ok := r.get(args[1]); ok {
			return fmt.Sprintf("$%d\r\n%s\r\n", len(v), v)
		}
		return "$-1\r\n"
	case "SET":
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.failSet {
			return "-ERR memória cheia\r\n"
		}
		r.data[args[1]] = args[2]
		return "+OK\r\n"
	default:
		return "-ERR comando não suportado\r\n"
	}
}

// readCommand lê um comando RESP: *<n>\r\n seguido de n strings $<len>\r\n<valor>\r\n.
func readCommand(rd *bufio.Reader) ([]string, error) {
	n, err := readPrefixed(rd, '*')
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		size, err := readPrefixed(rd, '$')
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2) // valor + \r\n
		if _, err := io.ReadFull(rd, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func readPrefixed(rd *bufio.Reader, prefix byte) (int, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return 0, err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 || line[0] != prefix {
		return 0, fmt.Errorf("esperava %q, recebeu %q", prefix, line)
	}
	return strconv.Atoi(line[1:])
}

// jsonServer sobe um serviço HTTP que responde sempre o mesmo status e corpo.
func jsonServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// truncatedServer declara um corpo maior do que envia: a leitura falha no meio.
func truncatedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"name":`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeSQS sobe um endpoint no lugar do SQS e devolve o cliente apontado para ele.
// Os corpos recebidos vão para o canal, para o teste conferir o evento.
func fakeSQS(t *testing.T, status int) (*sqs.SQS, chan string) {
	t.Helper()
	recebidos := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recebidos <- string(body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = io.WriteString(w, `{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"fila não existe"}`)
			return
		}
		_, _ = io.WriteString(w, `{"MessageId":"m-1"}`)
	}))
	t.Cleanup(srv.Close)

	sess := session.Must(session.NewSession(&aws.Config{
		Region:                  aws.String("us-east-1"),
		Endpoint:                aws.String(srv.URL),
		Credentials:             credentials.NewStaticCredentials("teste", "teste", ""),
		DisableComputeChecksums: aws.Bool(true), // o endpoint falso não devolve o MD5 do corpo
		MaxRetries:              aws.Int(0),
	}))
	return sqs.New(sess), recebidos
}
