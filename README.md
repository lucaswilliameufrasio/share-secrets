# Share Secrets

Ferramenta temporária para compartilhar uma credencial quando a pessoa não consegue acessar o Bitwarden.

## Como funciona

- O segredo é cifrado no navegador com AES-256-GCM; o servidor recebe ciphertext, IV e o hash de uma prova aleatória, nunca a chave nem o segredo em claro.
- A prova aleatória está dentro do próprio payload autenticado. Após descriptografar, o navegador a envia no ACK; o servidor compara seu SHA-256 antes de consumir o segredo.
- A chave de descriptografia fica no fragmento (`#...`) do link, que não é enviado na requisição HTTP.
- O link vence em uma hora por padrão. Na criação, escolha uma validade própria entre 1 minuto e 24 horas; abrir cria uma reserva temporária de 30 segundos. O segredo só é apagado após o navegador confirmar que conseguiu descriptografá-lo.
- No padrão em memória, reiniciar o processo invalida todos os links existentes. Com SQLite explicitamente habilitado e schema migrado, os links ativos sobrevivem a reinícios enquanto o banco local sobreviver. Após uso/expiração, o servidor remove o payload ativo; não há garantia de sobrescrita segura da heap, snapshots ou mídia.
- Limite padrão de 500 segredos ativos (configurável de 1 a 700), 64 KiB por requisição e 256 requisições de API simultâneas. `SECRET_TTL` configura o padrão do servidor (1 minuto–24 horas).
- A lista de acompanhamento fica neste navegador e guarda somente o ID e um token restrito à consulta de estado; não guarda o link completo nem a chave de descriptografia. O servidor retém o estado terminal por até 7 dias, limitado a 10.000 registros.
- O PWA pode ser instalado, mas usa rede exclusivamente: não há cache nem funcionamento offline.

## Executar localmente

Requer Go 1.27.3 (a versão local é fixada em `.mise.toml`) e Node.js 24/npm somente para os testes E2E.

O gerador/orquestrador de benchmarks e seus testes usam Python 3 (somente a
biblioteca padrão); `make all` inclui os testes dessas ferramentas. A campanha
limitada usa Docker rootless local, sem acessar a VPS.

```sh
mise install
go test -count=1 ./...
go run .
```

Abra <http://localhost:8080>.

O site detecta inglês ou português do Brasil pelas preferências do navegador; o seletor no topo permite trocar o idioma e salva a escolha neste navegador.

## Testes e verificações

O workflow do GitHub Actions executa automaticamente formato, vet, testes com race detector, build, lint, verificações de segurança, E2E e auditoria npm em push e pull request.

Os comandos mais usados também estão disponíveis no `Makefile` (`make help` lista os alvos):

```sh
make setup       # Go via mise, ferramentas Go pinadas + npm + Chromium
make test        # testes Go sem cache
make test-race   # detector de data races
make check build # formatação, vet e build
make security    # govulncheck + gosec
make bench       # benchmarks com alocações
make bench-http  # campanha HTTP local (~20–30 min), Docker rootless; docs/performance.md
make e2e         # fluxo real com Playwright
make lint        # golangci-lint, sem instalação global
# make tools-update # atualizar ferramentas; revisar go.mod/go.sum

# Equivalentes Go diretos
go test -count=1 ./...
go test -race -count=1 ./...
```

O E2E sobe a aplicação localmente e verifica criação, validade individual, acompanhamento do estado, descriptografia no navegador e consumo único — inclusive que uma chave alterada não consome o segredo e permite tentar o link original. Também verifica que o service worker não atende offline. Os benchmarks são diagnósticos, não um limite de performance fixo.

## Deploy com Docker Compose

Requer Docker Engine com Compose v2. O serviço publica a porta `8080` apenas no loopback por padrão; coloque um proxy reverso com TLS na frente. Ajuste `APP_PORT` se necessário e `SECRET_TTL` para alterar a validade padrão dos links. A validade de cada link também pode ser escolhida no navegador.

```sh
docker compose up -d --build
docker compose ps
docker compose logs -f app
```

A imagem é compilada em múltiplos estágios e executa um binário estático como usuário não-root, sem shell, em filesystem somente leitura. O healthcheck verifica `/healthz`. Na configuração padrão em memória, o serviço é uma única instância e reiniciar ou recriar o contêiner invalida links ativos. A opção SQLite requer o volume e os cuidados descritos abaixo.

Para métricas agregadas, configure `METRICS_TOKEN` por um gerenciador de segredos/ambiente antes de iniciar o Compose. No deploy blue/green, exporte o valor obtido do gerenciador de segredos antes de executar `make deploy`; a CLI o transfere por stdin/SSH, sem incluí-lo nos argumentos do comando. O Docker ainda mantém variáveis do contêiner em seus metadados, visíveis a quem administra o daemon. Para rate limit atrás de proxy, configure `TRUSTED_PROXIES` com o endereço/CIDR exato do proxy.

Para atualizar, rode `docker compose up -d --build`; para parar, `docker compose down` (não há volume de dados para remover).

## Deploy blue/green em VPS

Para publicar atrás do Nginx Proxy Manager em outra VPS da tailnet, use a CLI Go interativa. Ela pede o alias SSH configurado na máquina do operador e o slot que está recebendo tráfego; não há hostname de produção embutido no repositório. Consulte [`DEPLOY.md`](DEPLOY.md) para o passo a passo e a configuração do NPM.

```sh
make deploy
```

## Expor temporariamente com Tailscale Funnel

Use o Funnel somente para um compartilhamento temporário. Instale e autentique o Tailscale na máquina host, confirme que a tailnet permite Funnel e que a porta HTTPS escolhida está livre. O serviço local pode usar uma porta diferente da porta HTTPS pública:

```sh
ADDR=:8088 mise exec -- go run .
```

Em outro terminal, publique o serviço local 8088 pela porta HTTPS 8443:

```sh
tailscale funnel --https=8443 8088
```

O Funnel publica o endpoint na internet; não execute esse comando se quiser acesso somente pela tailnet. Antes, confira `tailscale funnel status` e `tailscale serve status`: uma configuração Serve já existente na porta HTTPS escolhida impede o Funnel de criar outro listener nela. Não desligue a porta sem identificar e aprovar o impacto sobre a rota existente. Use a URL HTTPS mostrada pelo Tailscale para abrir a página e criar o link; a origem pública será usada no link gerado. Ao terminar, desligue somente o listener que você criou (`tailscale funnel --https=8443 off`) e pare o servidor.

### Alternativas

- **Cloudflare Tunnel (`cloudflared tunnel`)**: publica um serviço local por meio de um túnel Cloudflare; requer conta/configuração DNS para hostname estável. Restrinja o hostname com Cloudflare Access quando o destinatário puder autenticar.
- **Reverse proxy com HTTPS** (por exemplo, Caddy ou Nginx atrás de uma VM/VPS): adequado quando já existe domínio e infraestrutura. Configure TLS e restrinja acesso na borda, se possível.

Essas opções também podem tornar o app acessível pela internet. HTTPS protege o transporte, mas não impede que outra pessoa com o link consuma o segredo. Envie o link por canal confiável e evite dados de longa duração ou credenciais de alto privilégio.

## Segurança, métricas e limites operacionais

- O limite padrão de criação é 60 por minuto, com burst de 20 por endereço de cliente; `CREATE_RATE_PER_MINUTE` e `CREATE_RATE_BURST` permitem ajustar esses valores. `ACTIVE_SECRET_LIMIT` configura o limite de links ativos (1–700; padrão 500), respeitando o teto de memória do contêiner. Ao esgotar capacidade, a API responde `503`; excesso de criação por cliente responde `429` com `Retry-After`.
- Configure `TRUSTED_PROXIES` com os IPs/CIDRs exatos dos proxies que encaminham tráfego para que o rate limit use `X-Forwarded-For`. Sem isso, o cabeçalho é ignorado por segurança e clientes atrás do mesmo proxy compartilham o limite do endereço do proxy. Esses limites são por processo e não substituem controles de borda contra DDoS.
- Métricas agregadas estão disponíveis na seção **Métricas de uso** e em `GET /api/metrics`, mas somente quando `METRICS_TOKEN` está configurado (mínimo de 32 caracteres). Informe o token no navegador quando consultar; a interface não o persiste. No modo memória os totais reiniciam; no SQLite os totais de criação/abertura/uso/expiração ficam no banco, enquanto rejeições e uptime continuam por processo. Configure o token por um mecanismo seguro de ambiente; não o coloque no repositório, em argumentos de shell ou em logs.
- Os totais não incluem IPs, IDs de links nem conteúdo. A lista do navegador é por dispositivo/perfil; remover uma linha só apaga o acompanhamento local. A chave do link não é salva no histórico local.
- O PWA é online-only. O service worker encaminha as requisições à rede e não mantém cache offline.

- O modelo de acesso não tem contas: quem possui o link pode tentar abrir o segredo. O token de acompanhamento dá acesso apenas ao estado daquele link.
- No modo memória não há persistência nem coordenação entre instâncias; reiniciar ou trocar de slot invalida links ativos. No modo SQLite, apenas processos que montam o mesmo banco local compartilham o ciclo de vida; rate limit e métricas operacionais continuam por processo. Múltiplos hosts não são suportados.
- Estado terminal é mantido por até 7 dias e pode ser removido antes quando o limite de 10.000 registros for atingido. O limite de concorrência é local (256 requisições de API por processo).

- O Funnel publica o serviço na internet. Qualquer pessoa com o link pode consumir o segredo; compartilhe-o diretamente com o destinatário por um canal confiável.
- O servidor valida a prova que só aparece no plaintext autenticado por AES-GCM; isso verifica a descriptografia, mas não prova que uma pessoa leu a tela. Uma reserva ativa impede aberturas concorrentes até ser confirmada, liberada ou expirar.
- Evite colocar dados de longa duração ou credenciais de alto privilégio nesta POC. Após a pessoa recuperar o acesso ao cofre, prefira armazenar/rotacionar a credencial pelo Bitwarden.
- Os limites locais reduzem abuso simples, mas não oferecem autenticação de remetentes nem proteção contra abuso volumétrico distribuído. Exponha o app apenas com TLS e controles apropriados na borda.

Consulte [`docs/security-usage-design.md`](docs/security-usage-design.md) para os limites e decisões do histórico local, métricas e PWA.

## Persistência, exclusão e capacidade

O padrão permanece armazenamento volátil em memória. O backend SQLite com
`modernc.org/sqlite` está em implementação e validação; não é autorizado para
produção até finalizar o gate e a avaliação de carga. As migrations SQL são
timestamped e gerenciadas pelo CLI Goose. O [roadmap](docs/roadmap.md) inclui
PostgreSQL open-source como alternativa futura escolhida pelo operador.

A exigência de excluir ciphertext após consumo precisa de um contrato explícito
para WAL, backups, snapshots e chaves: `DELETE` não comprova apagamento físico
de todas as cópias. Veja [persistência e exclusão](docs/storage-and-erasure.md).
Os [resultados e plano de performance](docs/performance.md) distinguem os
microbenchmarks executados de um teto de carga ainda não medido.

### Preparar SQLite localmente

Use uma pasta **nova**, local, privada, exclusiva do teste e excluída de backups
e snapshots. O banco inteiro, inclusive WAL e SHM, contém payloads ativos. Nunca
use uma pasta existente de produção para testar ou rodar migrations sem aprovação.

```sh
umask 077
mkdir -p ./local-test-data
mise exec -- go tool goose -env none -dir migrations/sqlite sqlite3 "$(pwd)/local-test-data/secrets.db" up
mise exec -- go tool goose -env none -dir migrations/sqlite sqlite3 "$(pwd)/local-test-data/secrets.db" status
STORAGE_BACKEND=sqlite SQLITE_PATH="$(pwd)/local-test-data/secrets.db" SQLITE_NO_PAYLOAD_BACKUP_ACK=yes mise exec -- go run .
```

`-env none` impede Goose de carregar `.env`. O servidor não cria nem migra o
banco automaticamente e falha se o arquivo ou a versão esperada estiverem
ausentes. O ACK declara que o operador implementou a exclusão dos backups; o
aplicativo não consegue comprovar snapshots ou backups do provedor. Com
`STORAGE_BACKEND=sqlite`, erro de armazenamento retorna `503`, sem fallback
para memória. `GET /api/metrics` protegido inclui `cleanupPending` (0/1): 1
indica remoção confirmada no banco mas checkpoint WAL ainda pendente. Isso não
promete apagamento físico de mídia ou cópias externas.
O SQLite usa fila de escrita por processo, limitada a 64 operações e lotes de
até 32, com confirmação apenas após o commit; fila cheia responde `503`.
Métricas protegidas incluem profundidade/capacidade/rejeições e falhas por etapa
do lote. SIGTERM/SIGINT inicia drain por até oito segundos; uma parada forçada
pode interromper trabalho ainda não confirmado, e perder uma resposta após o
commit deixa o resultado ambíguo para o cliente.

O override `compose.sqlite.yaml` monta uma pasta local escolhida pelo operador;
não execute com essa configuração até migrar o arquivo e garantir permissões
para o UID não-root 65532 dentro do contêiner rootless. Use os dois arquivos
`-f compose.yaml -f compose.sqlite.yaml` após definir `SQLITE_DATA_DIR` e o
ACK. A CLI blue/green prepara o mesmo volume para ambos os slots, mas não aplica
migrations; veja `DEPLOY.md`. A integração e a capacidade ainda estão em
validação; não use `make deploy` para publicar SQLite antes de concluí-las.
O Compose e a CLI de deploy fornecem `/tmp` em tmpfs limitado a 16 MiB, pois
SQLite pode precisar de arquivos temporários para savepoints; o filesystem da
imagem continua somente leitura. Tmpfs não prova exclusão de swap, dumps ou
snapshots do host, que também precisam ser abrangidos pela política do operador.
