# Backend Challenge Go — Processamento Distribuído de Apostas

Serviço distribuído de alta consistência financeira e baixa latência desenvolvido em **Go** com **Uber Fx**, **PostgreSQL 16**, **AWS SQS FIFO (LocalStack)** e autenticação **Keycloak OAuth 2.0 / OIDC**.

---

## 🚀 Tecnologias e Arquitetura

- **Linguagem**: Go (versão `1.27.1` declarada no `go.mod` e `Dockerfile`)
- **Injeção de Dependências & Ciclo de Vida**: Uber Fx (`go.uber.org/fx`)
- **Persistência Relacional**: PostgreSQL 16 com driver nativo `pgx/v5`
- **Mensageria Assíncrona**: AWS SQS FIFO com filas de Dead Letter Queue (DLQ) no LocalStack
- **Autenticação & Autorização**: Keycloak 24 com tokens JWT assinados via RS256 e validação dinâmica via JWKS
- **Padrões Arquiteturais**:
  - **Zero-Float Guarantee**: Valores monetários manipulados estritamente em inteiros mínimos de 64 bits (`int64`).
  - **Append-Only Immutable Ledger**: Livro-razão protegido por trigger nativa no PostgreSQL contra edição e deleção.
  - **Pessimistic Row Locking**: Coordenação atômica por carteira (`SELECT ... FOR UPDATE`), sem bloqueios globais.
  - **Transactional Outbox & Inbox**: Garantia de publicação e consumo *exactly-once* lógico sob entrega *at-least-once*.
  - **Resolução de Reversões Fora de Ordem**: Worker em segundo plano com backoff exponencial e expiração por TTL.
  - **Observabilidade**: Métricas Prometheus em `/metrics` e logs estruturados em JSON via `log/slog`.

---

## 📋 Pré-requisitos

- **Docker** (24.0+) e **Docker Compose** (v2+)
- **Go** (1.22+ ou 1.27+) instalado localmente (ou via container Docker)
- **cURL** e **jq** (opcionais, para execução de scripts de teste manual)

---

## 🛠️ Como Iniciar o Ambiente Local

### 1. Iniciar os Containers (PostgreSQL, LocalStack SQS e Keycloak)
```bash
docker compose up -d
```

O Docker Compose provisionará automaticamente:
- **PostgreSQL 16** na porta `5432` (`wager_db`, user: `postgres`, pass: `postgres`)
- **LocalStack 3.8** na porta `4566` com as filas SQS FIFO:
  - `wager-transactions.fifo` (fila principal com política de redrive após 5 tentativas)
  - `wager-transactions-dlq.fifo` (fila de mensagens mortas DLQ)
  - `wager-events.fifo` (fila de publicação de eventos da outbox)
- **Keycloak 24.0.5** na porta `8082` com o realm `wager-realm` pré-configurado e os clientes:
  - `internal-service` (secret: `internal-secret-123`, roles: `["internal"]`)
  - `provider-a` (secret: `provider-a-secret-123`, roles: `["provider"]`)
  - `provider-b` (secret: `provider-b-secret-123`, roles: `["provider"]`)

### 2. Executar o Serviço Go
As migrations do PostgreSQL são aplicadas **automaticamente** na inicialização do serviço via `fx.Lifecycle`.

```bash
# Executar a aplicação
go run ./cmd/server
```

A API estará disponível em `http://localhost:8080`.

---

## 🧪 Execução de Testes

### 1. Testes Unitários com Detecção de Concorrência (`-race`)
```bash
go test -v -race ./internal/domain/... ./internal/usecase/... ./internal/infrastructure/auth/... ./cmd/server/...
```

### 2. Testes de Integração, Concorrência e Resiliência (Containers Reais)
Com os containers do Docker Compose em execução:
```bash
go test -v -race ./test/integration/...
```

Os testes de integração cobrem integralmente:
1. **50 Requisições Paralelas da Mesma Aposta**: Comprova exatamente 1 débito no saldo e 49 replays idempotentes.
2. **Disputa de Duas Apostas de 80.00 sobre Saldo de 100.00**: 1 aposta processada, 1 rejeitada com `INSUFFICIENT_FUNDS`, saldo final de 20.00 BRL e 1 débito no ledger.
3. **Múltiplas Carteiras Independentes Simultâneas**: Execução em paralelo sem locks globais.
4. **Coordenação Multi-Processo (>= 3 instâncias independentes)**: Concorrência distribuída entre pools e memórias isoladas.
5. **Segurança de Reentrega na Inbox (Crash pós-commit)**: Simulação de falha antes da remoção da mensagem do SQS com deduplicação sem efeitos colaterais.
6. **Publishers Concorrentes da Outbox**: Múltiplos workers disputando a outbox com `FOR UPDATE SKIP LOCKED`.
7. **Reversões Fora de Ordem e TTL**: Chegada de `REFUND` antes da aposta de referência e resolução posterior pelo worker, bem como expiração por TTL com transição para `REJECTED (REFERENCE_NOT_FOUND)`.
8. **Concorrência Cruzada HTTP vs SQS**: Envio simultâneo da mesma operação por REST e fila.
9. **Invariantes do Banco de Dados**: Validação da trigger `trg_ledger_immutable` e constraint `chk_wallets_balance_non_negative`.
10. **Autenticação Real Keycloak**: Rejeição de tokens inválidos e isolamento estrito de tenancy entre provedores.

### 3. Rodar Todos os Testes do Projeto
```bash
go test -race ./...
```

---

## 📡 Guia de Utilização da API HTTP com cURL

### 1. Obter Token de Autenticação OAuth 2.0 (Keycloak)
Utilize o script utilitário `scripts/get-token.sh`:

```bash
# Token para serviço interno
INTERNAL_TOKEN=$(./scripts/get-token.sh internal-service)

# Token para Provedor A
PROVIDER_A_TOKEN=$(./scripts/get-token.sh provider-a)
```

---

### 2. Abertura de Carteira (`POST /wallets`)
*Restrito a chamadas com a role `internal`.*

```bash
curl -i -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer ${INTERNAL_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "initialBalance": { "amount": "1000.00", "currency": "BRL" }
  }'
```

**Resposta (`HTTP 201 Created`):**
```json
{
  "id": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1
}
```

---

### 3. Envio de Operação de Aposta (`POST /wagering/transactions`)
*Requer token do provedor e cabeçalho `Idempotency-Key` obrigatório.*

```bash
curl -i -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer ${PROVIDER_A_TOKEN}" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:bet-tx-001" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "bet-tx-001",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }'
```

**Resposta (`HTTP 200 OK`):**
```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "status": "PROCESSED",
  "balance": { "amount": "975.00", "currency": "BRL" },
  "idempotentReplay": false
}
```

*Reenvio da mesma requisição com a mesma chave e payload retornará `HTTP 200 OK` com `"idempotentReplay": true` e o saldo histórico `975.00 BRL`.*

---

### 4. Envio de Operação de Reembolso (`REFUND`)
```bash
curl -i -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer ${PROVIDER_A_TOKEN}" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:refund-tx-001" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "refund-tx-001",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "REFUND",
    "money": { "amount": "25.00", "currency": "BRL" },
    "referenceExternalTransactionId": "bet-tx-001"
  }'
```

---

### 5. Consulta do Livro-Razão com Cursor (`GET /wallets/:walletId/ledger`)
```bash
curl -i -X GET "http://localhost:8080/wallets/0192f291-27dd-7d3f-8071-5f8685deef37/ledger?limit=10" \
  -H "Authorization: Bearer ${INTERNAL_TOKEN}"
```

**Resposta:**
```json
{
  "items": [
    {
      "id": "0192f298-345e-7e38-af88-ledger-1",
      "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
      "direction": "DEBIT",
      "amount": { "amount": "25.00", "currency": "BRL" },
      "balanceBefore": { "amount": "1000.00", "currency": "BRL" },
      "balanceAfter": { "amount": "975.00", "currency": "BRL" },
      "createdAt": "2026-09-28T00:00:00Z"
    }
  ],
  "nextCursor": "MTcyNzQ4..."
}
```

---

### 6. Auditoria de Reconciliação Financeira (`POST /wallets/:walletId/reconciliation`)
Reconstrói o saldo da carteira a partir da soma dos lançamentos de créditos e débitos do ledger e audita contra o saldo persistido.

```bash
curl -i -X POST http://localhost:8080/wallets/0192f291-27dd-7d3f-8071-5f8685deef37/reconciliation \
  -H "Authorization: Bearer ${INTERNAL_TOKEN}"
```

**Resposta:**
```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "storedBalance": { "amount": "975.00", "currency": "BRL" },
  "calculatedBalance": { "amount": "975.00", "currency": "BRL" },
  "difference": { "amount": "0.00", "currency": "BRL" },
  "consistent": true,
  "checkedEntries": 2
}
```

---

### 7. Health Checks e Métricas Prometheus
- **Liveness**: `curl -i http://localhost:8080/health/live` (`HTTP 200 {"status":"UP"}`)
- **Readiness**: `curl -i http://localhost:8080/health/ready` (`HTTP 200 {"status":"UP","components":{"postgres":"UP","sqs":"UP"}}`)
- **Prometheus Metrics**: `curl -s http://localhost:8080/metrics`

---

## 📑 Documentação Detalhada
Para uma explicação aprofundada de todas as garantias financeiras, mapeamento de banco de dados, locks pessimistas, máquina de estados e padrões de resiliência, consulte o documento [`ARCHITECTURE.md`](ARCHITECTURE.md).