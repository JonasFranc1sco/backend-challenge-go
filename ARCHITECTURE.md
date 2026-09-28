# Arquitetura — Processamento Distribuído de Apostas em Go

Este documento detalha as decisões arquiteturais, modelos de domínio, estratégias de concorrência, garantias financeiras e padrões de resiliência implementados no serviço de processamento distribuído de apostas.

---

## 1. Visão Geral da Arquitetura

O serviço foi projetado sob os princípios de **Clean Architecture** (Arquitetura Limpa / Hexagonal) e orquestrado via injeção de dependências e gerenciamento de ciclo de vida com **Uber Fx (`go.uber.org/fx`)**.

```
                           +----------------------------------------+
                           |           Clientes Externos            |
                           |   (Provedores / Serviços Internos)     |
                           +-------------------+--------------------+
                                               |
                     +-------------------------+-------------------------+
                     | OAuth 2.0 (RS256 JWT)   | AWS SQS FIFO            |
                     v                         v                         |
          +-----------------------+  +-------------------+               |
          |  HTTP REST Router     |  | SQS FIFO Consumer |               |
          |  (net/http / mux)     |  |  (Worker Inbox)   |               |
          +-----------+-----------+  +---------+---------+               |
                      |                        |                         |
                      +-----------+------------+                         |
                                  |                                      |
                                  v                                      |
                      +------------------------+                         |
                      |  Casos de Uso (Usecase)|                         |
                      |  - ProcessWager        |                         |
                      |  - OpenWallet          |                         |
                      |  - Reconcile           |                         |
                      +-----------+------------+                         |
                                  |                                      |
                                  v                                      |
                      +------------------------+                         |
                      |   Domínio Financeiro   |                         |
                      |  (Wallet, Money, Tx)   |                         |
                      +-----------+------------+                         |
                                  |                                      |
                    +-------------+-------------+                        |
                    | pgx Transactor (ACID Tx)  |                        |
                    v                           v                        v
          +-------------------+       +-------------------+    +--------------------+
          | PostgreSQL 16     |       | Transactional     |--->| Outbox Publisher   |
          | - wallets         |       | Outbox Table      |    | (FOR UPDATE SKIP   |
          | - transactions    |       | (outbox_events)   |    |  LOCKED -> SQS)    |
          | - ledger (Trigger)|       +-------------------+    +--------------------+
          | - inbox_messages  |
          +-------------------+
```

### Camadas e Responsabilidades:
1. **Domínio (`internal/domain`)**: Value Objects (`Money`), Entidades e Raízes de Agregado (`Wallet`, `WagerTransaction`, `WalletLedgerEntry`), Envelopes e Eventos de Domínio (`EventEnvelope`). O domínio é 100% puro e independente de frameworks, banco de dados, HTTP ou bibliotecas de transporte.
2. **Casos de Uso (`internal/usecase`)**: Orquestração das operações de negócio (`OpenWalletUseCase`, `ProcessWagerTransactionUseCase`, `ReconciliationUseCase`) e cálculo de hash canônico determinístico (`ComputeCanonicalPayloadHash`).
3. **Infraestrutura (`internal/infrastructure`)**:
   - `postgres/`: Pool de conexões com `pgx/v5`, executor de transações atômicas (`Transactor`), migrador de banco de dados (`Migrator`) e repositórios específicos.
   - `sqs/`: Cliente AWS SQS v2 e publicador FIFO (`SQSEventPublisher`).
   - `auth/`: Validador de tokens JWT OAuth 2.0 / OIDC com busca e cache de chaves públicas via JWKS e middlewares de autorização.
4. **Workers em Segundo Plano (`internal/worker`)**:
   - `OutboxPublisherWorker`: Publica eventos pendentes com `SELECT ... FOR UPDATE SKIP LOCKED` e backoff exponencial.
   - `PendingReferenceWorker`: Resolve reversões fora de ordem com row locks e rejeição por TTL.
   - `SQSConsumerWorker`: Consome mensagens SQS FIFO integradas à Inbox Transacional sob a mesma transação SQL.
5. **Entrega HTTP (`internal/delivery/http`)**: Handlers RESTful para abertura de carteira, envio de transações, consulta de ledger com paginação por cursor, reconciliação financeira, endpoints de métricas Prometheus (`/metrics`) e health checks (`/health/live`, `/health/ready`).
6. **Composição e Ciclo de Vida (`go.uber.org/fx`)**: Organização por `fx.Module`, inicialização observável e shutdown gracioso que interrompe o recebimento de novas requisições, drena os workers e finaliza as conexões com o banco ordenadamente.

---

## 2. Garantia Zero-Float e Modelagem Monetária (`Money`)

Conforme a **Garantia Obrigatória 1**, representações em ponto flutuante (`float32` ou `float64`) são estritamente proibidas em qualquer etapa do ciclo de vida: parsing, validações, cálculo aritmético, serialização JSON e persistência em banco.

### 2.1. Representação Interna
- `Money` é um Value Object imutável composto por:
  - `units int64`: Valor em unidades mínimas (centavos na escala 2, ex: `25.00` BRL = `2500` centavos).
  - `currency string`: Código de moeda ISO 4217 de 3 letras maiúsculas (ex: `"BRL"`).
- Limites numéricos: Opera no intervalo de inteiros signed de 64 bits (`-9.223.372.036.854.775.808` a `+9.223.372.036.854.775.807` centavos).
- Operações de parsing decimal realizam divisão e multiplicação direta por parsing de caracteres (`strconv.ParseInt`), sem passar por `strconv.ParseFloat`.
- Qualquer entrada contendo escala diferente de 2 casas, notação científica (`e/E`), `NaN`, `Infinity`, caracteres inválidos ou moeda incompatível é rejeitada imediatamente com `domain.ErrInvalidMoneyFormat` ou `domain.ErrCurrencyMismatch`.
- Detecção estrita de overflow em soma (`Add`), subtração (`Sub`) e negação (`Neg`).

### 2.2. Mapeamento no Banco de Dados
- Na base de dados PostgreSQL, os valores monetários são persistidos no schema em `BIGINT` (`amount BIGINT NOT NULL`, `balance BIGINT NOT NULL`), garantindo precisão matemática exata e ordenação eficiente sem arredondamento.

---

## 3. Acesso ao Banco de Dados e Delimitação Transacional

### 3.1. Biblioteca Escolhida: `pgx/v5`
Optou-se por **`pgx/v5` (`github.com/jackc/pgx/v5`)** com SQL nativo explícito pelos seguintes motivos:
- **Controle Fino de Locks e Concorrência**: Permite especificar explicitamente `SELECT ... FOR UPDATE` em nível de linha e `FOR UPDATE SKIP LOCKED` em filas de banco de dados sem abstrações opacas de ORM.
- **Performance e Tipagem**: Protocolo binário nativo do PostgreSQL, alta eficiência de alocação de memória e suporte a pools de alto desempenho (`pgxpool.Pool`).
- **Verificabilidade**: Consultas SQL, índices e constraints permanecem 100% explícitos e auditáveis.

### 3.2. Padrão Unit of Work / Transactor
O gerenciamento transacional é desacoplado através da interface `postgres.Transactor`:
```go
type Transactor interface {
    WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error
}
```
Todos os repositórios aceitam `pgx.Tx` como parâmetro em operações que alteram estado. Isso assegura que:
- O bloqueio da carteira (`Wallet`),
- A criação do registro da transação (`WagerTransaction`),
- A inserção do lançamento no livro-razão (`WalletLedgerEntry`),
- A gravação do evento na Outbox Transacional (`OutboxRecord`), e
- O registro de deduplicação na Inbox (`InboxRecord`)
**compartilhem rigorosamente a mesma transação SQL atômica**, com `COMMIT` ou `ROLLBACK` total.

---

## 4. Agregado `Wallet` e Ledger Append-Only Imutável

### 4.1. Invariantes da Carteira
- A identidade única da carteira é `(playerId, currency)`.
- Cada débito ou crédito valida se a moeda da operação coincide com a da carteira.
- Débitos são impedidos pelo domínio e pelo banco de tornarem o saldo negativo (`balance >= 0`).
- A versão da carteira (`version`) inicia em `1` e é incrementada atomicamente em cada mudança de saldo. Operações sem movimentação financeira (como `LOSS`) não incrementam a versão.

### 4.2. Imutabilidade do Livro-Razão (`wallet_ledger_entries`)
Cada lançamento registra a direção (`DEBIT` ou `CREDIT`), valor, saldo antes e saldo após a transação.
- **Validação de Domínio**: `NewLedgerEntry` exige que matematicamente `balanceAfter = balanceBefore ± amount`.
- **Garantia no Banco via Trigger**: O banco de dados PostgreSQL impõe a trigger nativa `trg_ledger_immutable`:
  ```sql
  CREATE OR REPLACE FUNCTION prevent_ledger_mutation()
  RETURNS TRIGGER AS $$
  BEGIN
      RAISE EXCEPTION 'wallet_ledger_entries is append-only: updates and deletes are strictly prohibited';
  END;
  $$ LANGUAGE plpgsql;
  ```
  Qualquer tentativa manual ou programática de `UPDATE` ou `DELETE` no ledger é abortada com exceção pelo PostgreSQL.

---

## 5. Estratégia de Controle de Concorrência

A coordenação entre operações concorrentes ocorre **estritamente por carteira**:
1. Ao iniciar a execução de uma operação financeira, a transação SQL adquire um **lock pessimista de linha** na carteira correspondente:
   ```sql
   SELECT id, player_id, currency, balance, version, created_at, updated_at
   FROM wallets
   WHERE id = $1
   FOR UPDATE
   ```
2. **Ausência de Bloqueios Globais**: Transações em carteiras diferentes prosseguem de forma totalmente concorrente e paralela, maximizando a vazão horizontal.
3. **Serialização Segura para a Mesma Carteira**: Se duas apostas concorrentes chegarem para a mesma carteira (por exemplo, duas apostas de `80.00` BRL simultâneas sobre saldo de `100.00` BRL):
   - A primeira transação a obter o lock debita `80.00` BRL, atualiza o saldo para `20.00` BRL, insere o lançamento no ledger e comita.
   - A segunda transação é desbloqueada imediatamente após o commit da primeira, reidrata o saldo atualizado (`20.00` BRL), detecta saldo insuficiente, marca o estado como `REJECTED (INSUFFICIENT_FUNDS)` e comita sem criar lançamento de débito no ledger.
4. **Sem Starvation de Conexões**: Ao adquirir o lock da carteira primeiro dentro da transação, todas as verificações subsequentes de idempotência utilizam a mesma conexão da transação (`tx`), eliminando impasses de esgotamento de conexões no pool.

---

## 6. Idempotência Persistente e Hash Canônico

O cabeçalho HTTP `Idempotency-Key` e o campo `data.idempotencyKey` do SQS garantem deduplicação persistente em múltiplos processos e reinicializações.

### 6.1. Hash Canônico Determinístico
Para detectar conflitos de payload sob a mesma chave, calculamos o hash SHA-256 de um JSON canônico ordenado com os campos de negócio:
- Campos incluídos: `externalTransactionId`, `gameId`, `kind`, `money.amount`, `money.currency`, `playerId`, `providerId`, `referenceExternalTransactionId`, `roundId`, `walletId`.
- Campos excluídos: metadados de transporte, timestamps e a própria chave de idempotência.
- O JSON canônico garante que qualquer permutação na ordem de envio das chaves no corpo gere o mesmo digest SHA-256.

### 6.2. Regras de Replay
- **Mesma Chave + Mesmo Payload**: Retorna imediatamente a resposta original persistida no banco, com `idempotentReplay: true` e o saldo histórico observado no momento do processamento original.
- **Mesma Chave + Payload Diferente**: Retorna `HTTP 409 Conflict` (`idempotency_conflict`).
- **Mesmo `(providerId, externalTransactionId)` + Outra Chave**: Retorna `HTTP 409 Conflict` (`external_id_conflict`), impedindo reaplicação por troca de chave.

---

## 7. Máquina de Estados e Resolução de Reversões Fora de Ordem

### 7.1. Estados da Transação
```
              [Recebimento]
                    |
                    v
               +---------+
               | PENDING |
               +----+----+
                    |
       +------------+------------+
       |                         |
       v                         v
 [Dependência OK]         [Falta Referência]
       |                         |
       v                         v
+--------------+      +-------------------+
|  PROCESSED   |      | PENDING_REFERENCE |
+--------------+      +---------+---------+
                                |
                    +-----------+-----------+
                    |                       |
                    v                       v
            [Ref Encontrada]        [Max Retries / TTL]
                    |                       |
                    v                       v
             +--------------+        +--------------+
             |  PROCESSED   |        |   REJECTED   |
             +--------------+        +--------------+
```

| Estado | Tipo | Descrição |
| --- | --- | --- |
| `PENDING` | Transitório | Registro aceito em processamento síncrono. |
| `PENDING_REFERENCE` | Transitório Durável | Operação de `REFUND` ou `ROLLBACK` recebida antes da transação original referenciada. |
| `PROCESSED` | Terminal | Operação concluída com sucesso. Lançamento no ledger confirmado. |
| `REJECTED` | Terminal | Recusada por regra de negócio (ex: saldo insuficiente, referência não encontrada após expiração de TTL, incompatibilidade de moedas). |
| `FAILED` | Terminal | Falha permanente de infraestrutura registrada para auditoria. |

### 7.2. Resolução pelo `PendingReferenceWorker`
- Transações em `PENDING_REFERENCE` são selecionadas pelo worker com lock de linha `SELECT ... FOR UPDATE SKIP LOCKED` e ordenadas por `next_retry_at ASC`.
- O worker reavalia a chegada da transação referenciada com backoff exponencial bit-shift: `1 << retry_count` segundos (evitando qualquer float).
- **TTL / Esgotamento de Tentativas**: Ao atingir `maxRetries`, a transação transiciona para `REJECTED` com código estável `REFERENCE_NOT_FOUND`, emitindo o evento `WagerTransactionRejected`.
- **Prevenção de Dupla Reversão**: O domínio e as regras de negócio garantem que uma aposta processada não receba mais de um `REFUND` bem-sucedido ou duplo `ROLLBACK`.

---

## 8. Transactional Outbox e Transactional Inbox

### 8.1. Outbox Transacional
Para assegurar a **Garantia Obrigatória 4** (eventos externos só publicados após a confirmação da transação no banco):
- Eventos de domínio são gravados na tabela `outbox_events` na mesma transação atômica que debita a carteira e cria o ledger.
- O `OutboxPublisherWorker` seleciona eventos com `published_at IS NULL AND next_retry_at <= NOW()` utilizando `FOR UPDATE SKIP LOCKED`.
- Suporta múltiplos publishers concorrentes sem colisão de locks.
- Dispara os eventos para o AWS SQS FIFO com `MessageGroupId = aggregateId` (mantendo a ordenação por carteira) e `MessageDeduplicationId = eventId` (deduplicação no broker).
- Atualiza `published_at = NOW()` após o sucesso no SQS.

### 8.2. Inbox Transacional
No consumidor SQS FIFO (`SQSConsumerWorker`):
- O registro na tabela `inbox_messages` é realizado dentro da mesma transação SQL das alterações de domínio.
- Se a mensagem já foi processada anteriormente (`alreadyExists && status == 'PROCESSED'`), a execução do domínio é ignorada e a mensagem é removida do SQS.
- A remoção da mensagem da fila SQS (`DeleteMessage`) ocorre **estritamente após o commit durável no banco**.
- Em caso de crash ou reentrega antes do `DeleteMessage`, a Inbox garante idempotência total sem duplicidade financeira.

---

## 9. Autenticação e Autorização (Keycloak OAuth 2.0 / OIDC)

### 9.1. Decisão do IdP
O **Keycloak 24.0.5** foi escolhido e provisionado via Docker Compose:
- Suporte nativo a OpenID Connect, OAuth 2.0 e fluxo `client_credentials` entre microsserviços.
- Assinatura criptográfica de tokens JWT com chaves assimétricas **RS256**.
- Emissão de metadados padrão OIDC em `/.well-known/openid-configuration` e `certs` (JWKS).

### 9.2. Validação e Caching de Chaves (JWKS)
- O serviço valida a assinatura criptográfica dos tokens sem fazer chamadas HTTP remotas a cada requisição.
- O componente `JWKSKeyFetcher` consulta o endpoint de JWKS do Keycloak, decodifica as chaves públicas RSA (`n` e `e` em Base64 URL) e armazena em cache thread-safe (`sync.RWMutex`) com TTL de 1 hora e refresh sob demanda caso um `kid` desconhecido chegue no header do token.
- São validados: assinatura RS256, expiração (`exp`), emissor (`iss`) e integridade dos claims.

### 9.3. Modelo de Permissões e Tenancy
1. **Papel `internal`**: Concedido a serviços da plataforma interna (`internal-service`). Permite abertura de carteira (`POST /wallets`), consulta e reconciliação. Bloqueado para provedores externos.
2. **Papel `provider`**: Concedido aos provedores de jogos (`provider-a`, `provider-b`).
3. **Isolamento de Tenancy**: O claim `provider_id` extraído do token autenticado é comparado com o `providerId` da transação no payload e na URL. Um provedor não pode executar apostas nem consultar dados de outro provedor (`HTTP 403 Forbidden`).

---

## 10. Observabilidade e Métricas

- **Logs JSON Estruturados**: Implementados via `log/slog` da biblioteca padrão do Go, registrando os identificadores de correlação em todas as operações: `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`. Nenhum dado sensível, segredo ou payload financeiro bruto é logado.
- **Métricas Prometheus (`/metrics`)**:
  - `wager_transactions_total`: Contador particionado por `kind`, `status` e `provider`.
  - `idempotent_replays_total`: Contador de replays por `provider`.
  - `idempotency_conflicts_total`: Contador de conflitos por `reason`.
  - `reconciliation_discrepancies_total`: Contador de divergências financeiras detectadas.
  - `outbox_published_total` e `outbox_lag_seconds`: Vazão e latência de publicação da outbox.
  - `wager_transaction_duration_seconds`: Histograma de latência por tipo de operação.
  - `retries_total` e `dlq_messages_total`: Rastreamento de resiliência e mensagens envenenadas.
- **Health Checks**:
  - `GET /health/live`: Liveness probe do processo da aplicação.
  - `GET /health/ready`: Readiness probe verificando conectividade real com PostgreSQL e LocalStack SQS.
